package sources

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"frostroot/internal/pgp"
	"frostroot/internal/pki"
	"frostroot/internal/recipe"
)

// Errors of key fetching. Compare with errors.Is.
var (
	// ErrNoKeySource means frostroot does not know where the key of a
	// custom source comes from; the user saves it.
	ErrNoKeySource = errors.New("no known place to fetch the key from")
	// ErrFingerprintMismatch means the fetched key file holds a primary key
	// that is not pinned.
	ErrFingerprintMismatch = errors.New("the fetched key file holds a key that is not pinned")
)

// Client fetches a URL and returns the body of a successful response.
// Tests replace it.
type Client interface {
	Get(ctx context.Context, url string) ([]byte, error)
}

// maxKeyBytes bounds a downloaded key or API answer; keys are kilobytes.
const maxKeyBytes = 1 << 20

// httpTimeout bounds one key fetch.
const httpTimeout = 60 * time.Second

// HTTPClient is the Client that uses the network.
type HTTPClient struct {
	Client *http.Client // nil means one honoring the proxy environment with a timeout
	// RootCAs is what the default client verifies HTTPS against; nil means
	// the host's own roots. Ignored when Client is set.
	RootCAs   *x509.CertPool
	UserAgent string
}

// Get implements Client: a GET that must answer 200.
func (c HTTPClient) Get(ctx context.Context, url string) ([]byte, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: httpTimeout, Transport: pki.Transport(c.RootCAs)}
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if c.UserAgent != "" {
		request.Header.Set("User-Agent", c.UserAgent)
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }() // the body has been read or the fetch failed
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %s", url, response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxKeyBytes))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return body, nil
}

// FetchedKey is a key file that was downloaded and checked.
type FetchedKey struct {
	Armored     []byte // what to write as the key file
	Fingerprint string // the first primary key of the file
	// Fingerprints names every primary key the file holds, each of them
	// checked against the pinned set. This is what installing the file
	// trusts, since apt accepts a Release signed by any key in a signed-by
	// keyring; Fingerprint alone says less than that.
	Fingerprints []string
	SourceURL    string // where it came from
}

// FetchKey fetches and checks the signing key file of source: a catalog
// entry's from its key URL against the fingerprints the entry pins, a PPA's
// from the Ubuntu keyserver against the fingerprint Launchpad's API
// publishes. Every primary key the file holds has to be pinned, not only the
// first. A source that is neither gets ErrNoKeySource.
func FetchKey(ctx context.Context, client Client, source recipe.Source) (FetchedKey, error) {
	keyURL, pinned, err := keyLocation(ctx, client, source)
	if err != nil {
		return FetchedKey{}, err
	}
	data, err := client.Get(ctx, keyURL)
	if err != nil {
		return FetchedKey{}, fmt.Errorf("fetching the key of %s: %w", source.Name, err)
	}
	key, err := pgp.ParsePublicKey(data)
	if err != nil {
		return FetchedKey{}, fmt.Errorf("the key of %s from %s: %w", source.Name, keyURL, err)
	}
	if unpinned, found := firstUnpinned(key.Fingerprints, pinned); found {
		return FetchedKey{}, fmt.Errorf("%w: the file %s serves for %s holds %s; pinned is %s", ErrFingerprintMismatch, keyURL, source.Name, pgp.FormatFingerprint(unpinned), pgp.FormatFingerprints(pinned))
	}
	return FetchedKey{Armored: pgp.Armor(key.Binary), Fingerprint: key.Fingerprint, Fingerprints: key.Fingerprints, SourceURL: keyURL}, nil
}

// firstUnpinned returns the first of the fetched primary keys that pinned
// does not name.
//
// Every key in the file is checked, not only the first. The file is written
// whole into the source's signed-by keyring, and apt accepts a Release signed
// by any key in that keyring, so a server that appends a second primary key
// to the expected one would otherwise have it trusted: the pin would match on
// the first key and the second would ship with it.
func firstUnpinned(fetched, pinned []string) (string, bool) {
	for _, fingerprint := range fetched {
		if !slices.ContainsFunc(pinned, func(want string) bool { return strings.EqualFold(want, fingerprint) }) {
			return fingerprint, true
		}
	}
	return "", false
}

// keyLocation says where source's key is and which keys its file may hold.
func keyLocation(ctx context.Context, client Client, source recipe.Source) (keyURL string, pinned []string, err error) {
	if entry, isCatalog := Lookup(source.Name); isCatalog {
		return entry.KeyURL, entry.Fingerprints, nil
	}
	owner, name, isPPA := PPAOf(source.URL)
	if !isPPA {
		return "", nil, fmt.Errorf("%w for %s; save its public key as %s", ErrNoKeySource, source.Name, source.Key)
	}
	fingerprint, err := launchpadFingerprint(ctx, client, owner, name)
	if err != nil {
		return "", nil, err
	}
	// Launchpad publishes one signing key for a PPA, so that key is the whole
	// pinned set and a keyserver answer holding any other is refused.
	return keyserverURL(fingerprint), []string{fingerprint}, nil
}

// launchpadFingerprint asks Launchpad's API which key signs a PPA.
func launchpadFingerprint(ctx context.Context, client Client, owner, name string) (string, error) {
	apiURL := launchpadArchiveURL(owner, name)
	body, err := client.Get(ctx, apiURL)
	if err != nil {
		return "", fmt.Errorf("asking Launchpad about PPA %s/%s: %w", owner, name, err)
	}
	var archive struct {
		Fingerprint string `json:"signing_key_fingerprint"`
	}
	if err := json.Unmarshal(body, &archive); err != nil {
		return "", fmt.Errorf("the answer of Launchpad about PPA %s/%s is not what was expected: %w", owner, name, err)
	}
	fingerprint := strings.ToUpper(strings.TrimSpace(archive.Fingerprint))
	if len(fingerprint) < 40 {
		return "", fmt.Errorf("no signing key is published by Launchpad for PPA %s/%s", owner, name)
	}
	return fingerprint, nil
}
