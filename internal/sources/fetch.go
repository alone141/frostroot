package sources

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
)

// Errors of key fetching. Compare with errors.Is.
var (
	// ErrNoKeySource means frostroot does not know where the key of a
	// custom source comes from; the user saves it.
	ErrNoKeySource = errors.New("no known place to fetch the key from")
	// ErrFingerprintMismatch means the fetched key is not the one expected.
	ErrFingerprintMismatch = errors.New("the fetched key's fingerprint is not the expected one")
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
	Client    *http.Client // nil means one honoring the proxy environment with a timeout
	UserAgent string
}

// Get implements Client: a GET that must answer 200.
func (c HTTPClient) Get(ctx context.Context, url string) ([]byte, error) {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: httpTimeout}
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

// FetchedKey is a key that was downloaded and checked.
type FetchedKey struct {
	Armored     []byte // what to write as the key file
	Fingerprint string
	SourceURL   string // where it came from
}

// FetchKey fetches and checks the signing key of source: a catalog entry's
// key from its key URL against the pinned fingerprint, a PPA's from the
// Ubuntu keyserver against the fingerprint Launchpad's API publishes. A
// source that is neither gets ErrNoKeySource.
func FetchKey(ctx context.Context, client Client, source recipe.Source) (FetchedKey, error) {
	keyURL, wantFingerprint, err := keyLocation(ctx, client, source)
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
	if !strings.EqualFold(key.Fingerprint, wantFingerprint) {
		return FetchedKey{}, fmt.Errorf("%w: %s from %s has %s, expected %s", ErrFingerprintMismatch, source.Name, keyURL, pgp.FormatFingerprint(key.Fingerprint), pgp.FormatFingerprint(wantFingerprint))
	}
	return FetchedKey{Armored: pgp.Armor(key.Binary), Fingerprint: key.Fingerprint, SourceURL: keyURL}, nil
}

// keyLocation says where source's key is and which fingerprint it must have.
func keyLocation(ctx context.Context, client Client, source recipe.Source) (keyURL, fingerprint string, err error) {
	if entry, isCatalog := Lookup(source.Name); isCatalog {
		return entry.KeyURL, entry.Fingerprint, nil
	}
	owner, name, isPPA := PPAOf(source.URL)
	if !isPPA {
		return "", "", fmt.Errorf("%w for %s; save its public key as %s", ErrNoKeySource, source.Name, source.Key)
	}
	fingerprint, err = launchpadFingerprint(ctx, client, owner, name)
	if err != nil {
		return "", "", err
	}
	return keyserverURL(fingerprint), fingerprint, nil
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
