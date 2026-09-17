package capture

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"

	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// aptSource is one entry of an apt source file, for one URI and one suite.
type aptSource struct {
	file       string // root-relative path with a leading slash
	uri        string
	suite      string
	components []string
	// signedBy is the Signed-By option: a path inside the root, an inline
	// armored key, or "" when the entry relies on the trusted keyrings.
	signedBy string
}

// keyPathPrefix is how an inline deb822 key starts, as opposed to a path.
const armoredKeyPrefix = "-----BEGIN PGP PUBLIC KEY BLOCK-----"

// aptSources reads every "deb" entry of /etc/apt/sources.list and
// sources.list.d, one-line and deb822 alike, in file order. deb-src entries
// install nothing and are left out.
func (root systemRoot) aptSources() []aptSource {
	var entries []aptSource
	candidates := []string{aptSourcesPath}
	if directoryEntries, err := os.ReadDir(root.path(aptSourcesDir)); err == nil {
		for _, entry := range directoryEntries {
			if !entry.IsDir() && (strings.HasSuffix(entry.Name(), ".list") || strings.HasSuffix(entry.Name(), ".sources")) {
				candidates = append(candidates, aptSourcesDir+"/"+entry.Name())
			}
		}
	}
	for _, candidate := range candidates {
		content, ok := root.readText(candidate)
		if !ok {
			continue
		}
		if strings.HasSuffix(candidate, ".sources") {
			entries = append(entries, parseDeb822Sources("/"+candidate, content)...)
		} else {
			entries = append(entries, parseOneLineSources("/"+candidate, content)...)
		}
	}
	return entries
}

// parseOneLineSources reads "deb [options] URI suite components..." lines.
func parseOneLineSources(file, content string) []aptSource {
	var entries []aptSource
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "deb" {
			continue
		}
		fields = fields[1:]
		signedBy := ""
		if strings.HasPrefix(fields[0], "[") {
			closing := slices.IndexFunc(fields, func(field string) bool { return strings.HasSuffix(field, "]") })
			if closing < 0 {
				continue
			}
			options := strings.Trim(strings.Join(fields[:closing+1], " "), "[]")
			for _, option := range strings.Fields(options) {
				if value, isSignedBy := strings.CutPrefix(option, "signed-by="); isSignedBy {
					signedBy, _, _ = strings.Cut(value, ",")
				}
			}
			fields = fields[closing+1:]
		}
		if len(fields) < 2 {
			continue
		}
		entries = append(entries, aptSource{file: file, uri: fields[0], suite: fields[1], components: fields[2:], signedBy: signedBy})
	}
	return entries
}

// parseDeb822Sources reads the stanzas of a .sources file.
func parseDeb822Sources(file, content string) []aptSource {
	var entries []aptSource
	for _, stanza := range readStanzas(strings.NewReader(content)) {
		if !slices.Contains(strings.Fields(stanza["Types"]), "deb") || strings.EqualFold(stanza["Enabled"], "no") {
			continue
		}
		signedBy := deb822SignedBy(stanza["Signed-By"])
		for _, uri := range strings.Fields(stanza["URIs"]) {
			for _, suite := range strings.Fields(stanza["Suites"]) {
				entries = append(entries, aptSource{file: file, uri: uri, suite: suite, components: strings.Fields(stanza["Components"]), signedBy: signedBy})
			}
		}
	}
	return entries
}

// deb822SignedBy returns a Signed-By value as a path or as the inline armored
// key it holds: continuation lines lose their leading space, and a line of a
// single dot stands for an empty line.
func deb822SignedBy(value string) string {
	lines := strings.Split(value, "\n")
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if line == "." {
			line = ""
		}
		lines[index] = line
	}
	joined := strings.TrimSpace(strings.Join(lines, "\n"))
	if strings.HasPrefix(joined, armoredKeyPrefix) {
		return joined + "\n"
	}
	return joined
}

// carriedSource is a third-party source the recipe can hold, with its key.
type carriedSource struct {
	source recipe.Source
	key    []byte // armored
	from   string // where the entry and the key were found
}

// leftSource is a third-party source the recipe cannot hold, and why.
type leftSource struct {
	description string // file and entry
	reason      string
}

func (s leftSource) String() string { return s.description + ": " + s.reason }

// sourcesForRecipe classifies the machine's apt sources: Ubuntu's own are
// skipped, entries with a readable signing key become recipe sources, the
// rest are reported. releaseSuite is the machine's code name, so a source
// on that suite gets the recipe's default.
func (root systemRoot) sourcesForRecipe(releaseSuite string) (carried []carriedSource, left []leftSource) {
	usedNames := map[string]bool{}
	seen := map[string]bool{}
	for _, entry := range root.aptSources() {
		if isUbuntuArchiveURI(entry.uri) {
			continue
		}
		identity := entry.uri + " " + entry.suite
		if seen[identity] {
			continue
		}
		seen[identity] = true
		description := fmt.Sprintf("%s (%s %s)", entry.file, entry.uri, entry.suite)
		if entry.suite == "./" || entry.suite == "/" || strings.HasSuffix(entry.suite, "/") {
			left = append(left, leftSource{description, "a flat repository without a suite, which the recipe cannot express"})
			continue
		}
		if entry.signedBy == "" {
			left = append(left, leftSource{description, "no signed-by key: it relies on /etc/apt/trusted.gpg.d or apt-key, and the recipe needs the key as a file"})
			continue
		}
		key, keyOrigin, err := root.sourceKey(entry.signedBy)
		if err != nil {
			left = append(left, leftSource{description, err.Error()})
			continue
		}
		source := recipe.Source{Name: sourceNameFor(entry.uri, entry.suite, releaseSuite, usedNames), URL: strings.TrimRight(entry.uri, "/"), Suite: entry.suite, Components: slices.Clone(entry.components)}
		if source.Suite == releaseSuite {
			source.Suite = ""
		}
		if slices.Equal(source.Components, recipe.DefaultComponents) {
			source.Components = nil
		}
		source.Key = sources.KeyPathFor(source.Name)
		if problems := recipe.CheckSource(source); len(problems) > 0 {
			left = append(left, leftSource{description, "fields the recipe cannot express: " + problems[0].Error()})
			continue
		}
		usedNames[source.Name] = true
		carried = append(carried, carriedSource{source: source, key: key, from: entry.file + ", key " + keyOrigin})
	}
	return carried, left
}

// sourceKey reads a Signed-By value as a key: inline, or a file under the
// root. It returns the key armored and where it came from.
func (root systemRoot) sourceKey(signedBy string) (armored []byte, origin string, err error) {
	if strings.HasPrefix(signedBy, armoredKeyPrefix) {
		key, err := pgp.ParsePublicKey([]byte(signedBy))
		if err != nil {
			return nil, "", fmt.Errorf("the inline signed-by key is unreadable: %w", err)
		}
		return pgp.Armor(key.Binary), "inline in the source file", nil
	}
	data, err := os.ReadFile(root.path(strings.TrimPrefix(signedBy, "/")))
	if err != nil {
		return nil, "", fmt.Errorf("the signed-by key %s could not be read: %w", signedBy, err)
	}
	key, err := pgp.ParsePublicKey(data)
	if err != nil {
		return nil, "", fmt.Errorf("the signed-by key %s: %w", signedBy, err)
	}
	return pgp.Armor(key.Binary), signedBy, nil
}

// sourceNameSeparators replaces every run of characters a source name cannot
// hold.
var sourceNameSeparators = regexp.MustCompile(`[^a-z0-9-]+`)

// maxSourceNameLength mirrors the recipe's rule.
const maxSourceNameLength = 32

// sourceNameFor names a captured source: the catalog's name when the URL
// and suite are a catalog entry's, "ppa-<owner>-<name>" for a PPA, otherwise
// the host with dots turned into dashes; made unique against usedNames.
func sourceNameFor(uri, suite, releaseSuite string, usedNames map[string]bool) string {
	trimmed := strings.TrimRight(uri, "/")
	name := ""
	for _, entry := range sources.Catalog() {
		if resolved := entry.Source(releaseSuite); resolved.URL == trimmed && resolved.SuiteFor(releaseSuite) == suite {
			name = entry.Name
			break
		}
	}
	if name == "" {
		if owner, ppaName, isPPA := sources.PPAOf(trimmed); isPPA {
			name = sources.PPA(owner, ppaName).Name
		}
	}
	if name == "" {
		host := trimmed
		if parsed, err := url.Parse(trimmed); err == nil && parsed.Hostname() != "" {
			host = parsed.Hostname()
		}
		name = strings.Trim(sourceNameSeparators.ReplaceAllString(strings.ToLower(host), "-"), "-")
		if name == "" {
			name = "source"
		}
		if len(name) > maxSourceNameLength-3 {
			name = strings.TrimRight(name[:maxSourceNameLength-3], "-")
		}
	}
	unique := name
	for suffix := 2; usedNames[unique]; suffix++ {
		unique = fmt.Sprintf("%s-%d", name, suffix)
	}
	return unique
}

// hostOfURI returns the host of a source URI, as apt's index file names
// start with it.
func hostOfURI(uri string) string {
	parsed, err := url.Parse(uri)
	if err != nil {
		return ""
	}
	return parsed.Host
}
