package capture

import (
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strconv"
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
		// Apt takes "#" as a comment to the end of the line wherever it
		// sits, not only at the start, so a hand-added "# vendor" after the
		// components is not two more components. Cutting there also matches
		// what apt does with a "#" inside the URL: the rest of the line
		// goes with it, and what is left names no suite, so nothing is
		// carried. Verified against apt 2.8.3 with apt-get indextargets.
		line, _, _ = strings.Cut(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
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
		if !slices.Contains(strings.Fields(stanza["Types"]), "deb") || deb822Disabled(stanza["Enabled"]) {
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

// deb822Disabled reports whether an Enabled value turns a stanza off, the
// way apt reads it (StringToBool in apt-pkg/contrib/strutl.cc, checked
// against apt 2.8.3): the number 0, and "no", "false", "without", "off" and
// "disable" in any case, do; every other value leaves the stanza on, "yes"
// and a word apt does not know alike, because apt falls back to its default
// of enabled. Only "no" used to count, so a repository its owner had turned
// off with "false" was carried as live, and re-enabled in the image.
func deb822Disabled(value string) bool {
	value = strings.TrimSpace(value)
	if number, err := strconv.ParseInt(value, 0, 64); err == nil {
		return number == 0
	}
	switch strings.ToLower(value) {
	case "no", "false", "without", "off", "disable":
		return true
	}
	return false
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
	// One URI and suite can be listed more than once: a .sources file added
	// by a vendor's current instructions, beside the .list its older ones
	// left behind. They are one repository, so they are decided together
	// rather than the first copy standing for all of them. A trailing slash
	// on the URI is not a difference: apt fetches the same repository, and
	// everything downstream trims it, so the identity does too, or the two
	// copies became docker and docker-2 with two key files.
	var identities []string
	copies := map[string][]aptSource{}
	for _, entry := range root.aptSources() {
		if isUbuntuArchiveURI(entry.uri) {
			continue
		}
		identity := strings.TrimRight(entry.uri, "/") + " " + entry.suite
		if _, grouped := copies[identity]; !grouped {
			identities = append(identities, identity)
		}
		copies[identity] = append(copies[identity], entry)
	}
	usedNames := map[string]bool{}
	for _, identity := range identities {
		source, problem := root.carryOneSource(copies[identity], releaseSuite, usedNames)
		if problem != nil {
			left = append(left, *problem)
			continue
		}
		usedNames[source.source.Name] = true
		carried = append(carried, source)
	}
	return carried, left
}

// describeAptSource names an entry for the report. The report is a file
// written beside the recipe, so a machine whose sources.list carries a
// password must not have it copied there: the recipe refuses such a URL,
// and the line that says so is this one.
func describeAptSource(entry aptSource) string {
	return fmt.Sprintf("%s (%s %s)", entry.file, recipe.RedactURLCredentials(entry.uri), entry.suite)
}

// carryOneSource turns every copy of one URI and suite into a single recipe
// source, or reports why none of them can be carried. Copies that name a
// signing key are tried first, so a signed .sources entry wins over the
// unsigned .list beside it instead of the file order deciding; the same
// order picks which failure to report, because "the key could not be read"
// tells a person more than "no signed-by key".
func (root systemRoot) carryOneSource(entries []aptSource, releaseSuite string, usedNames map[string]bool) (carriedSource, *leftSource) {
	var ordered []aptSource
	for _, entry := range entries {
		if entry.signedBy != "" {
			ordered = append(ordered, entry)
		}
	}
	for _, entry := range entries {
		if entry.signedBy == "" {
			ordered = append(ordered, entry)
		}
	}
	// The components belong to the repository, not to whichever copy wins.
	components := unionComponents(entries)
	var firstProblem *leftSource
	refuse := func(entry aptSource, reason string) {
		if firstProblem == nil {
			firstProblem = &leftSource{describeAptSource(entry), reason}
		}
	}
	for _, entry := range ordered {
		if entry.suite == "./" || entry.suite == "/" || strings.HasSuffix(entry.suite, "/") {
			refuse(entry, "a flat repository without a suite, which the recipe cannot express")
			continue
		}
		if entry.signedBy == "" {
			refuse(entry, "no signed-by key: it relies on /etc/apt/trusted.gpg.d or apt-key, and the recipe needs the key as a file")
			continue
		}
		key, keyOrigin, err := root.sourceKey(entry.signedBy)
		if err != nil {
			refuse(entry, err.Error())
			continue
		}
		source := recipe.Source{
			Name:       sourceNameFor(entry.uri, entry.suite, releaseSuite, usedNames),
			URL:        strings.TrimRight(entry.uri, "/"),
			Suite:      entry.suite,
			Components: slices.Clone(components),
		}
		if source.Suite == releaseSuite {
			source.Suite = ""
		}
		if slices.Equal(source.Components, recipe.DefaultComponents) {
			source.Components = nil
		}
		source.Key = sources.KeyPathFor(source.Name)
		if problems := recipe.CheckSource(source); len(problems) > 0 {
			refuse(entry, "fields the recipe cannot express: "+problems[0].Error())
			continue
		}
		return carriedSource{source: source, key: key, from: sourceOrigin(entry, entries, keyOrigin)}, nil
	}
	return carriedSource{}, firstProblem
}

// unionComponents returns every component the copies of one repository name,
// once each and in the order the files list them. Apt fetched all of them,
// so carrying only the winning copy's would quietly drop what the other was
// installing from. A name the recipe cannot express is left out rather than
// allowed to spoil the source: the entry it came from is reported anyway.
func unionComponents(entries []aptSource) []string {
	var components []string
	for _, entry := range entries {
		for _, component := range entry.components {
			if recipe.CheckComponent(component) == nil && !slices.Contains(components, component) {
				components = append(components, component)
			}
		}
	}
	return components
}

// sourceOrigin says where a carried source came from, naming the other files
// that list the same repository so that folding them together is visible.
func sourceOrigin(chosen aptSource, entries []aptSource, keyOrigin string) string {
	var others []string
	for _, entry := range entries {
		if entry.file != chosen.file && !slices.Contains(others, entry.file) {
			others = append(others, entry.file)
		}
	}
	origin := chosen.file + ", key " + keyOrigin
	if len(others) > 0 {
		origin += " (also listed in " + strings.Join(others, ", ") + ")"
	}
	return origin
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
	keyPath, inside := root.pathInRoot(strings.TrimPrefix(signedBy, "/"))
	if !inside {
		return nil, "", fmt.Errorf("the signed-by key %s is outside %s, so it belongs to this machine rather than the one being captured", signedBy, root)
	}
	data, err := os.ReadFile(keyPath)
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
		if len(name) > recipe.MaxSourceNameLength-3 {
			name = strings.TrimRight(name[:recipe.MaxSourceNameLength-3], "-")
		}
	}
	unique := name
	for suffix := 2; usedNames[unique]; suffix++ {
		unique = fmt.Sprintf("%s-%d", name, suffix)
	}
	return unique
}
