package form

import (
	"context"
	"fmt"
	"strings"

	"frostroot/internal/recipe"
)

// Match is one package a Search field offers.
type Match struct {
	Name        string
	Version     string
	Component   string // main, restricted, universe or multiverse
	Section     string // devel, python, libs
	Description string // one line
}

// SectionCount is an archive section and how many matches it holds.
type SectionCount struct {
	Name  string
	Count int
}

// PackageIndex is what a Search field looks names up in: the packages of
// one Ubuntu release. internal/index implements it; the form knows nothing
// of archives, caches or the network.
type PackageIndex interface {
	// Search returns the best matches for query, at most limit, and how
	// many matched in all. section, when not empty, narrows to one section.
	Search(query, section string, limit int) (matches []Match, total int)
	// SectionsMatching returns the sections holding matches for query,
	// largest first.
	SectionsMatching(query string) []SectionCount
	// Lookup returns the package of exactly this name, cheaply.
	Lookup(name string) (Match, bool)
	// Has reports whether the release has a package of exactly this name.
	Has(name string) bool
	// Nearest returns names close to one the release lacks, closest first.
	Nearest(name string, limit int) []string
	// Describe says what the index is in a few words.
	Describe() string
}

// IndexOpener opens the index of an Ubuntu release such as "24.04".
// progress is told how the download goes, from another goroutine. An error
// means there is no index to be had, which is never the form's failure: the
// field goes on as a list of typed names.
type IndexOpener func(ctx context.Context, releaseVersion string, progress func(doneBytes, totalBytes int64)) (PackageIndex, error)

// nearestCount is how many "did you mean" names a warning offers.
const nearestCount = 3

// UnknownPackage is a requested name the release's archive does not have.
type UnknownPackage struct {
	Name    string
	Nearest []string // close names the archive does have, or none
}

// UnknownPackages returns the names of the "Other packages" answer that
// index lacks, in the order they were given. Catalog names are not checked:
// the catalog's own integration test proves them. A nil index knows nothing
// and so objects to nothing. Names a recipe could not hold anyway are left
// to validation, which refuses them.
func UnknownPackages(values Values, index PackageIndex) []UnknownPackage {
	if index == nil {
		return nil
	}
	var unknown []UnknownPackage
	seen := map[string]bool{}
	for _, name := range splitPackageList(values.String(KeyOtherPackages)) {
		if seen[name] || recipe.CheckPackageName(name) != nil || index.Has(name) {
			continue
		}
		seen[name] = true
		unknown = append(unknown, UnknownPackage{Name: name, Nearest: index.Nearest(name, nearestCount)})
	}
	return unknown
}

// UnknownPackagesWarning renders unknown for the summary, or "" when there
// is nothing to say. It is a warning and never a refusal: a third-party
// source may well provide docker-ce, and the index cannot know.
func UnknownPackagesWarning(unknown []UnknownPackage, values Values) string {
	if len(unknown) == 0 {
		return ""
	}
	var warning strings.Builder
	fmt.Fprintf(&warning, "Not in Ubuntu's %s archive:\n", releaseSuite(values.String(KeyRelease)))
	width := 0
	for _, item := range unknown {
		width = max(width, len(item.Name))
	}
	for _, item := range unknown {
		if len(item.Nearest) == 0 {
			fmt.Fprintf(&warning, "  %s\n", item.Name)
			continue
		}
		fmt.Fprintf(&warning, "  %-*s  nearest: %s\n", width, item.Name, strings.Join(item.Nearest, ", "))
	}
	if hasThirdPartySources(values) {
		warning.WriteString("One of the recipe's other sources may provide them; otherwise build will\nstop at \"Unable to locate package\".")
	} else {
		warning.WriteString("The recipe has no other source that could provide them, so build will\nstop at \"Unable to locate package\" unless one is added.")
	}
	return warning.String()
}

// hasThirdPartySources reports whether the answers name any apt source
// besides Ubuntu's archive: a catalog source, a PPA, or one the recipe
// already had written by hand.
func hasThirdPartySources(values Values) bool {
	return len(ToRecipe(values).Sources) > 0
}
