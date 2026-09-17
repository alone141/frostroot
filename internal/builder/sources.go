package builder

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"frostroot/internal/distro"
	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
)

// ImageKeyringDir is where the image keeps the signing keys of its extra
// sources, as apt's signed-by expects.
const ImageKeyringDir = "/etc/apt/keyrings"

// ErrSourceKey means a source's key file could not be read or is not a key.
var ErrSourceKey = errors.New("unusable source key")

// KeyringFileName names a source's keyring in the image and in the stage.
func KeyringFileName(sourceName string) string { return "frostroot-" + sourceName + ".gpg" }

// SourceLines returns every "deb" line of a build: the archive's three
// pockets (from mirrorURL when set), then one line per extra source whose
// key sits in keyringDir. The same function renders the lines mmdebstrap
// gets, with keyringDir the stage directory on the host, and the lines the
// image keeps, with keyringDir ImageKeyringDir; apt resolves signed-by
// where it runs.
func SourceLines(release distro.Release, mirrorURL string, sources []recipe.Source, keyringDir string) []string {
	lines := release.SourceLines(mirrorURL)
	for _, source := range sources {
		lines = append(lines, fmt.Sprintf("deb [signed-by=%s/%s] %s %s %s",
			keyringDir, KeyringFileName(source.Name), strings.TrimRight(source.URL, "/"), source.SuiteFor(release.Suite), strings.Join(source.ComponentsOrDefault(), " ")))
	}
	return lines
}

// sourceKey is a recipe source's key, read and checked.
type sourceKey struct {
	binary []byte
	sha256 string // of the recipe's key file as written
}

// readSourceKeys reads and parses every source's key file. Any failure is
// ErrSourceKey naming the source: a build must not start without the keys
// it will trust.
func readSourceKeys(recipeDir string, sources []recipe.Source) (map[string]sourceKey, error) {
	keys := map[string]sourceKey{}
	for _, source := range sources {
		if err := recipe.CheckKeyPath(source.Key); err != nil {
			return nil, fmt.Errorf("%w: source %s: %w", ErrSourceKey, source.Name, err)
		}
		data, err := os.ReadFile(recipe.KeyPath(recipeDir, source))
		if err != nil {
			return nil, fmt.Errorf("%w: source %s: %w", ErrSourceKey, source.Name, err)
		}
		key, err := pgp.ParsePublicKey(data)
		if err != nil {
			return nil, fmt.Errorf("%w: source %s: %s: %w", ErrSourceKey, source.Name, source.Key, err)
		}
		digest := sha256.Sum256(data)
		keys[source.Name] = sourceKey{binary: key.Binary, sha256: hex.EncodeToString(digest[:])}
	}
	return keys, nil
}

// lockRepositories describes the sources for the lock.
func lockRepositories(release distro.Release, sources []recipe.Source, keys map[string]sourceKey) []recipe.LockRepository {
	var repositories []recipe.LockRepository
	for _, source := range sources {
		repositories = append(repositories, recipe.LockRepository{
			Name:       source.Name,
			URL:        strings.TrimRight(source.URL, "/"),
			Suite:      source.SuiteFor(release.Suite),
			Components: append([]string(nil), source.ComponentsOrDefault()...),
			KeySHA256:  keys[source.Name].sha256,
		})
	}
	return repositories
}

// indexOrigin says which source an apt index file name belongs to: the
// file name starts with prefix, followed by "_dists_<suite>_".
type indexOrigin struct {
	source string // "" for the archive
	prefix string // apt's rendering of the base URL
	suite  string
	rank   int // higher wins when two indexes list one version
}

// aptListPrefix renders a base URL the way apt names its list files: the
// scheme dropped, a trailing slash removed, every slash an underscore.
// http://archive.ubuntu.com/ubuntu becomes archive.ubuntu.com_ubuntu.
func aptListPrefix(baseURL string) string {
	_, rest, found := strings.Cut(baseURL, "://")
	if !found {
		rest = baseURL
	}
	return strings.ReplaceAll(strings.TrimRight(rest, "/"), "/", "_")
}

// indexOrigins lists what a build's apt indexes can come from, so that
// recordChecksums can attribute each one. When two indexes list one version
// with one checksum, the archive's file name wins over a source's (the
// -security pocket first), and an earlier source's over a later one's.
func indexOrigins(release distro.Release, archiveURL string, sources []recipe.Source) []indexOrigin {
	var origins []indexOrigin
	for index, source := range sources {
		origins = append(origins, indexOrigin{source: source.Name, prefix: aptListPrefix(source.URL), suite: source.SuiteFor(release.Suite), rank: len(sources) - index})
	}
	archivePrefix := aptListPrefix(archiveURL)
	for pocketIndex, pocket := range []string{"", "-updates", "-security"} {
		origins = append(origins, indexOrigin{prefix: archivePrefix, suite: release.Suite + pocket, rank: len(sources) + 1 + pocketIndex})
	}
	sort.SliceStable(origins, func(i, j int) bool { return origins[i].rank < origins[j].rank })
	return origins
}

// originOf finds the origin of an apt index file by name.
func originOf(fileName string, origins []indexOrigin) (indexOrigin, bool) {
	for _, origin := range origins {
		if strings.HasPrefix(fileName, origin.prefix+"_dists_"+origin.suite+"_") {
			return origin, true
		}
	}
	return indexOrigin{}, false
}
