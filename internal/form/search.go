package form

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"frostroot/internal/recipe"
)

// Match is one package a Search field offers. An index that publishes no
// version, section or description — PyPI does not — fills Name alone.
type Match struct {
	Name        string
	Version     string
	Component   string // main, restricted, universe or multiverse
	Section     string // devel, python, libs
	Description string // one line
	Origin      string // the recipe source it comes from, or "" for the archive
}

// SectionCount is an archive section and how many matches it holds.
type SectionCount struct {
	Name  string
	Count int
}

// PackageIndex is what a Search field looks names up in: the packages of
// one Ubuntu release, or the projects on PyPI. internal/index implements
// both; the form knows nothing of archives, caches or the network.
type PackageIndex interface {
	// Search returns the best matches for query, at most limit, and how
	// many matched in all. section, when not empty, narrows to one section;
	// an index without sections ignores it.
	Search(query, section string, limit int) (matches []Match, total int)
	// SectionsMatching returns the sections holding matches for query,
	// largest first. An index without sections returns none.
	SectionsMatching(query string) []SectionCount
	// Lookup returns the package of exactly this name, cheaply.
	Lookup(name string) (Match, bool)
	// Has reports whether the index has a package of exactly this name.
	Has(name string) bool
	// Nearest returns names close to one the index lacks, closest first.
	Nearest(name string, limit int) []string
	// Describe says what the index is in a few words.
	Describe() string
}

// PackageRepositories is an index that can also say which repositories it
// searched and which of them it could not read. The apt index implements
// it; PyPI, which is one index and no repositories, does not.
type PackageRepositories interface {
	// Sources returns the repositories the index holds and those it could
	// not read, the release's archive first.
	Sources() (searched, missing []string)
}

// PackageSummaries is an index that can also say what a package is, one
// name at a time, because it publishes no descriptions in bulk. The apt
// index carries its descriptions already and implements none of this; the
// PyPI one implements all of it.
type PackageSummaries interface {
	// Summary returns what is already known about name, never blocking: it
	// is what a View draws from. known distinguishes a summary known to be
	// empty from a name nobody has asked about.
	Summary(name string) (summary string, known bool)
	// FetchSummary looks one up and remembers it. It blocks, so a caller
	// runs it away from the drawing.
	FetchSummary(ctx context.Context, name string)
}

// IndexRequest says which packages an opener should offer: those of one
// Ubuntu release, and those of the apt sources the answers so far add to it.
// Sources come from the Sources page, which is asked before the packages are,
// so that a name only Docker's repository has is a name the picker can find.
type IndexRequest struct {
	Release string          // such as "24.04"
	Sources []recipe.Source // resolved against Release; empty for PyPI
}

// Key identifies a request, so that a field that has already opened this
// index does not open it again. Two requests with the same key ask for the
// same packages.
func (r IndexRequest) Key() string {
	var key strings.Builder
	key.WriteString(r.Release)
	for _, source := range r.Sources {
		fmt.Fprintf(&key, "\n%s\t%s\t%s\t%s", source.Name, source.URL, source.Suite, strings.Join(source.Components, ","))
	}
	return key.String()
}

// IndexRequestFor is what the answers so far ask of an index: the release
// chosen on the Image page, and the sources chosen on the Sources page,
// resolved as the repositories they point at. Resolving here rather than in
// the caller keeps one answer to "which repository is this": the key below,
// the fetch and the cache all read the same suite and components.
func IndexRequestFor(values Values) IndexRequest {
	release := values.String(KeyRelease)
	suite := releaseSuite(release)
	sources := mergeAnswerSources(values)
	resolved := make([]recipe.Source, 0, len(sources))
	for _, source := range sources {
		source.Suite, source.Components = source.SuiteFor(suite), source.ComponentsOrDefault()
		source.URL = strings.TrimRight(source.URL, "/")
		resolved = append(resolved, source)
	}
	return IndexRequest{Release: release, Sources: resolved}
}

// IndexOpener opens an index for a request the form has been told about.
// progress is told how the download goes, from another goroutine. An error
// means there is no index to be had, which is never the form's failure: the
// field goes on as a list of typed names.
type IndexOpener func(ctx context.Context, request IndexRequest, progress func(doneBytes, totalBytes int64)) (PackageIndex, error)

// nearestCount is how many "did you mean" names a warning offers.
const nearestCount = 3

// UnknownPackage is a requested name its index does not have.
type UnknownPackage struct {
	Name    string
	Nearest []string // close names the index does have, or none
}

// unknownIn returns the names index lacks, in the order they were given.
// A nil index knows nothing and so objects to nothing. Names a recipe could
// not hold anyway are left to validation, which refuses them outright.
func unknownIn(names []string, index PackageIndex, check func(string) error) []UnknownPackage {
	if index == nil {
		return nil
	}
	var unknown []UnknownPackage
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] || check(name) != nil || index.Has(name) {
			continue
		}
		seen[name] = true
		unknown = append(unknown, UnknownPackage{Name: name, Nearest: index.Nearest(name, nearestCount)})
	}
	return unknown
}

// UnknownPackages returns the apt names of the "Other packages" answer that
// index lacks. Catalog names are not checked: the catalog's own integration
// test proves them.
func UnknownPackages(values Values, index PackageIndex) []UnknownPackage {
	return unknownIn(splitPackageList(values.String(KeyOtherPackages)), index, recipe.CheckPackageName)
}

// UnknownPythonPackages returns the PyPI names of the "Python packages"
// answer that index lacks. Names are compared as PEP 503 compares them, by
// the index, so Flask_SQLAlchemy is not reported missing.
func UnknownPythonPackages(values Values, index PackageIndex) []UnknownPackage {
	return unknownIn(splitPackageList(values.String(KeyPythonPackages)), index, recipe.CheckPythonPackageName)
}

// Warnings is what the last page says above the recipe, or "": the names
// no archive has, then the names no Python index has. Each is a warning and
// never a refusal — a third-party source may provide docker-ce, and a
// private index or a project published this morning may provide a PyPI name
// — so the write question below is unchanged either way.
func Warnings(values Values, apt, python PackageIndex) string {
	var blocks []string
	if warning := UnknownPackagesWarning(UnknownPackages(values, apt), values, apt); warning != "" {
		blocks = append(blocks, warning)
	}
	if warning := unknownPythonWarning(UnknownPythonPackages(values, python)); warning != "" {
		blocks = append(blocks, warning)
	}
	return strings.Join(blocks, "\n\n")
}

// UnknownPackagesWarning renders the apt names index does not have, or "".
// What it says of them depends on what was searched: the picker covers the
// sources the recipe adds, so a name missing from all of them is missing for
// good, and only a repository that could not be read leaves room for doubt.
func UnknownPackagesWarning(unknown []UnknownPackage, values Values, index PackageIndex) string {
	if len(unknown) == 0 {
		return ""
	}
	suite := releaseSuite(values.String(KeyRelease))
	searched, missing := repositoriesOf(index)
	// A repository that was only read from a cache is in both lists. It is
	// named as one that could not be read, below, so reciting it here as
	// one that was searched would have the warning contradict itself. The
	// archive may itself be the one that is missing, so which repositories
	// were read is decided by name and never by position.
	read := slices.DeleteFunc(slices.Clone(searched), func(name string) bool { return slices.Contains(missing, name) })
	archiveRead := slices.Contains(read, suite)
	sourcesRead := slices.DeleteFunc(slices.Clone(read), func(name string) bool { return name == suite })
	var warning strings.Builder
	switch {
	case archiveRead && len(sourcesRead) > 0:
		fmt.Fprintf(&warning, "In neither Ubuntu's %s archive nor %s:\n", suite, andList(sourcesRead))
	case len(sourcesRead) > 0:
		fmt.Fprintf(&warning, "Not in %s:\n", andList(sourcesRead))
	default:
		fmt.Fprintf(&warning, "Not in Ubuntu's %s archive:\n", suite)
	}
	writeUnknownNames(&warning, unknown)
	switch {
	case len(missing) > 0:
		fmt.Fprintf(&warning, "%s could not be read, so it may provide them; otherwise build\nwill stop at \"Unable to locate package\".", andList(missing))
	case len(sourcesRead) > 0:
		// Every repository the recipe has was searched, and none of them
		// has these names: there is nothing left for one to provide.
		warning.WriteString("build will stop at \"Unable to locate package\".")
	case hasThirdPartySources(values):
		// The index could not say what it searched, so the old answer
		// stands: a source the recipe has may still provide them.
		warning.WriteString("One of the recipe's other sources may provide them; otherwise build will\nstop at \"Unable to locate package\".")
	default:
		warning.WriteString("The recipe has no other source that could provide them, so build will\nstop at \"Unable to locate package\" unless one is added.")
	}
	return warning.String()
}

// repositoriesOf asks an index what it searched. An index that cannot say —
// a test double, or one built before the sources were searched too — is
// treated as having said nothing, and the warning falls back to what the
// answers themselves show.
func repositoriesOf(index PackageIndex) (searched, missing []string) {
	if repositories, canSay := index.(PackageRepositories); canSay {
		return repositories.Sources()
	}
	return nil, nil
}

// andList renders names the way a sentence takes them: "docker", "docker and
// kitware", "docker, kitware and llvm".
func andList(names []string) string {
	switch len(names) {
	case 0:
		return ""
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

// unknownPythonWarning renders PyPI names no index has, or "".
func unknownPythonWarning(unknown []UnknownPackage) string {
	if len(unknown) == 0 {
		return ""
	}
	var warning strings.Builder
	warning.WriteString("Not on PyPI:\n")
	writeUnknownNames(&warning, unknown)
	warning.WriteString("build will stop when pip cannot resolve them, unless they come from an\nindex of your own.")
	return warning.String()
}

// writeUnknownNames lists the names with their suggestions, the names
// padded to one width so the suggestions line up.
func writeUnknownNames(warning *strings.Builder, unknown []UnknownPackage) {
	width := 0
	for _, item := range unknown {
		width = max(width, len(item.Name))
	}
	for _, item := range unknown {
		if len(item.Nearest) == 0 {
			fmt.Fprintf(warning, "  %s\n", item.Name)
			continue
		}
		fmt.Fprintf(warning, "  %-*s  nearest: %s\n", width, item.Name, strings.Join(item.Nearest, ", "))
	}
}

// hasThirdPartySources reports whether the answers name any apt source
// besides Ubuntu's archive: a catalog source, a PPA, or one the recipe
// already had written by hand.
func hasThirdPartySources(values Values) bool {
	return len(ToRecipe(values).Sources) > 0
}
