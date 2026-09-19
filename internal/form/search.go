package form

import (
	"context"
	"fmt"
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
// chosen on the Image page, and the sources chosen on the Sources page.
func IndexRequestFor(values Values) IndexRequest {
	return IndexRequest{Release: values.String(KeyRelease), Sources: ToRecipe(values).Sources}
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
	if warning := UnknownPackagesWarning(UnknownPackages(values, apt), values); warning != "" {
		blocks = append(blocks, warning)
	}
	if warning := unknownPythonWarning(UnknownPythonPackages(values, python)); warning != "" {
		blocks = append(blocks, warning)
	}
	return strings.Join(blocks, "\n\n")
}

// UnknownPackagesWarning renders apt names unknown to the archive, or "".
func UnknownPackagesWarning(unknown []UnknownPackage, values Values) string {
	if len(unknown) == 0 {
		return ""
	}
	var warning strings.Builder
	fmt.Fprintf(&warning, "Not in Ubuntu's %s archive:\n", releaseSuite(values.String(KeyRelease)))
	writeUnknownNames(&warning, unknown)
	if hasThirdPartySources(values) {
		warning.WriteString("One of the recipe's other sources may provide them; otherwise build will\nstop at \"Unable to locate package\".")
	} else {
		warning.WriteString("The recipe has no other source that could provide them, so build will\nstop at \"Unable to locate package\" unless one is added.")
	}
	return warning.String()
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
