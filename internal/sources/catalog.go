// Package sources knows the third-party apt repositories frostroot offers by
// name, how a PPA becomes a source, and how to fetch and check a source's
// signing key. It decides nothing about images; the recipe carries the
// result.
package sources

import (
	"fmt"
	"regexp"
	"slices"
	"strings"

	"frostroot/internal/recipe"
)

// Entry is one repository the form offers. URL and Suite may contain
// "{suite}", replaced by the release's code name. Fingerprint pins the
// signing key: a fetched key that does not match is refused.
type Entry struct {
	Name        string // the recipe source name; stable
	Title       string
	Description string // what it is for, in a few words
	Category    string
	URL         string
	Suite       string // "" means the release's code name
	Components  []string
	KeyURL      string
	Fingerprint string // uppercase hex, no spaces
}

// catalog lists the repositories init offers. Every entry was checked on
// 2026-09-17 to publish an InRelease for 20.04, 22.04 and 24.04 (or for its
// single suite), and every fingerprint was computed with gpg from the key
// its KeyURL served that day.
var catalog = []Entry{
	{
		Name: "deadsnakes", Title: "deadsnakes PPA", Description: "newer and older Python versions (python3.12, python3.13...)", Category: "Languages",
		URL: "https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu", KeyURL: keyserverURL("F23C5A6CF475977595C89F51BA6932366A755776"),
		Fingerprint: "F23C5A6CF475977595C89F51BA6932366A755776",
	},
	{
		Name: "nodesource", Title: "NodeSource", Description: "Node.js 22", Category: "Languages",
		URL: "https://deb.nodesource.com/node_22.x", Suite: "nodistro", KeyURL: "https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key",
		Fingerprint: "6F71F525282841EEDAF851B42F59B5F99B1BE0B4",
	},
	{
		Name: "llvm", Title: "LLVM", Description: "the current clang, lld and lldb from apt.llvm.org", Category: "Languages",
		URL: "https://apt.llvm.org/{suite}", Suite: "llvm-toolchain-{suite}", KeyURL: "https://apt.llvm.org/llvm-snapshot.gpg.key",
		Fingerprint: "6084F3CF814B57C1CF12EFD515CF4D18AF4F7421",
	},
	{
		Name: "git-core", Title: "git-core PPA", Description: "the current git", Category: "Tools",
		URL: "https://ppa.launchpadcontent.net/git-core/ppa/ubuntu", KeyURL: keyserverURL("F911AB184317630C59970973E363C90F8F1B6217"),
		Fingerprint: "F911AB184317630C59970973E363C90F8F1B6217",
	},
	{
		Name: "github-cli", Title: "GitHub CLI", Description: "the gh command", Category: "Tools",
		URL: "https://cli.github.com/packages", Suite: "stable", KeyURL: "https://cli.github.com/packages/githubcli-archive-keyring.gpg",
		Fingerprint: "2C6106201985B60E6C7AC87323F3D4EA75716059",
	},
	{
		Name: "kitware", Title: "Kitware", Description: "the current CMake", Category: "Tools",
		URL: "https://apt.kitware.com/ubuntu", KeyURL: "https://apt.kitware.com/keys/kitware-archive-latest.asc",
		Fingerprint: "4DBEBE3EEC96E7B8C6EC5BE99E92FDC6C5B9BA75",
	},
	{
		Name: "docker", Title: "Docker", Description: "Docker Engine and Compose (docker-ce)", Category: "Tools",
		URL: "https://download.docker.com/linux/ubuntu", Components: []string{"stable"}, KeyURL: "https://download.docker.com/linux/ubuntu/gpg",
		Fingerprint: "9DC858229FC7DD38854AE2D88D81803C0EBFCD88",
	},
	{
		Name: "vscode", Title: "Visual Studio Code", Description: "the code package from Microsoft", Category: "Editors",
		URL: "https://packages.microsoft.com/repos/code", Suite: "stable", KeyURL: "https://packages.microsoft.com/keys/microsoft.asc",
		Fingerprint: "BC528686B50D79E339D3721CEB3E94ADBE1229CF",
	},
}

// Catalog returns every entry in display order.
func Catalog() []Entry { return slices.Clone(catalog) }

// Lookup returns the catalog entry named name.
func Lookup(name string) (Entry, bool) {
	for _, entry := range catalog {
		if entry.Name == name {
			return entry, true
		}
	}
	return Entry{}, false
}

// KeysDirName is where recipe directories keep signing keys.
const KeysDirName = "keys"

// KeyPathFor returns the recipe-relative key path of a source by name.
func KeyPathFor(name string) string { return KeysDirName + "/" + name + ".asc" }

// Source returns the recipe source for entry on a release.
func (e Entry) Source(releaseSuite string) recipe.Source {
	return recipe.Source{
		Name:       e.Name,
		URL:        strings.ReplaceAll(e.URL, "{suite}", releaseSuite),
		Suite:      strings.ReplaceAll(e.Suite, "{suite}", releaseSuite),
		Components: slices.Clone(e.Components),
		Key:        KeyPathFor(e.Name),
	}
}

// Launchpad locations.
const (
	ppaHost          = "ppa.launchpadcontent.net"
	launchpadAPIBase = "https://api.launchpad.net/1.0"
	keyserverBase    = "https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x"
)

// ppaNamePattern is what Launchpad allows in an owner or archive name.
var ppaNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]*$`)

// ParsePPA reads "owner/name" or "ppa:owner/name".
func ParsePPA(text string) (owner, name string, err error) {
	text = strings.TrimPrefix(strings.TrimSpace(text), "ppa:")
	owner, name, found := strings.Cut(text, "/")
	if !found || !ppaNamePattern.MatchString(owner) || !ppaNamePattern.MatchString(name) {
		return "", "", fmt.Errorf("invalid PPA %q (expected owner/name, such as deadsnakes/ppa)", text)
	}
	return owner, name, nil
}

// PPA returns the recipe source for a Launchpad PPA on the release's suite.
func PPA(owner, name string) recipe.Source {
	sourceName := ppaSourceName(owner, name)
	return recipe.Source{Name: sourceName, URL: ppaURL(owner, name), Key: KeyPathFor(sourceName)}
}

// ppaSourceName turns owner and name into a source name: "ppa-<owner>-<name>"
// with the characters a source name cannot hold replaced.
func ppaSourceName(owner, name string) string {
	unsafe := regexp.MustCompile(`[^a-z0-9-]+`)
	return "ppa-" + unsafe.ReplaceAllString(owner, "-") + "-" + unsafe.ReplaceAllString(name, "-")
}

func ppaURL(owner, name string) string {
	return "https://" + ppaHost + "/" + owner + "/" + name + "/ubuntu"
}

// PPAOf reports whether sourceURL is a Launchpad PPA and which.
func PPAOf(sourceURL string) (owner, name string, isPPA bool) {
	rest, found := strings.CutPrefix(strings.TrimSuffix(sourceURL, "/"), "https://"+ppaHost+"/")
	if !found {
		rest, found = strings.CutPrefix(strings.TrimSuffix(sourceURL, "/"), "http://"+ppaHost+"/")
	}
	if !found {
		return "", "", false
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 3 || parts[2] != "ubuntu" || !ppaNamePattern.MatchString(parts[0]) || !ppaNamePattern.MatchString(parts[1]) {
		return "", "", false
	}
	return parts[0], parts[1], true
}

// PPAFilesURL returns where Launchpad serves a file published to a PPA,
// whatever pool path it had: the fallback when the PPA has dropped it.
func PPAFilesURL(owner, name, fileName string) string {
	return "https://launchpad.net/~" + owner + "/+archive/ubuntu/" + name + "/+files/" + fileName
}

// launchpadArchiveURL is the API resource describing a PPA, whose
// signing_key_fingerprint says which key signs it.
func launchpadArchiveURL(owner, name string) string {
	return launchpadAPIBase + "/~" + owner + "/+archive/ubuntu/" + name
}

// keyserverURL returns where the Ubuntu keyserver serves a key by
// fingerprint, armored.
func keyserverURL(fingerprint string) string { return keyserverBase + fingerprint }

// Describe returns a short description of a recipe source for summaries:
// the catalog title, "PPA owner/name", or the URL.
func Describe(source recipe.Source) string {
	if entry, isCatalog := Lookup(source.Name); isCatalog {
		return entry.Title
	}
	if owner, name, isPPA := PPAOf(source.URL); isPPA {
		return "PPA " + owner + "/" + name
	}
	return source.URL
}
