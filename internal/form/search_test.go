package form

import (
	"context"
	"slices"
	"strings"
	"testing"
)

// fakeIndex has the names it is given and suggests what it is told to.
type fakeIndex struct {
	names   []string
	nearest map[string][]string
}

func (f fakeIndex) Search(string, string, int) ([]Match, int) { return nil, 0 }
func (f fakeIndex) SectionsMatching(string) []SectionCount    { return nil }
func (f fakeIndex) Has(name string) bool                      { return slices.Contains(f.names, name) }
func (f fakeIndex) Lookup(name string) (Match, bool)          { return Match{Name: name}, f.Has(name) }
func (f fakeIndex) Nearest(name string, _ int) []string       { return f.nearest[name] }
func (f fakeIndex) Describe() string                          { return "fake" }

func TestOtherPackagesIsASearchFieldWithTheHostsIndex(t *testing.T) {
	opened := false
	host := Host{OpenIndex: func(context.Context, IndexRequest, func(int64, int64)) (PackageIndex, error) {
		opened = true
		return fakeIndex{}, nil
	}}
	for _, field := range Fields(host) {
		if field.Key != KeyOtherPackages {
			continue
		}
		if field.Kind != KindSearch || field.OpenIndex == nil || field.Validate == nil {
			t.Fatalf("field = %+v, want a validated search field with the host's index", field)
		}
		if _, err := field.OpenIndex(context.Background(), IndexRequest{Release: "24.04"}, nil); err != nil || !opened {
			t.Errorf("the field does not open the host's index: %v", err)
		}
		return
	}
	t.Fatal("no Other packages field")
}

func TestUnknownPackages(t *testing.T) {
	index := fakeIndex{
		names:   []string{"ninja-build", "valgrind", "git"},
		nearest: map[string][]string{"ninja-buld": {"ninja-build"}},
	}
	values := Defaults(Host{})
	values[KeyPackages] = []string{"git", "not-even-checked"} // the catalog's answer is the catalog's business
	values[KeyOtherPackages] = "valgrind ninja-buld, docker-ce ninja-buld Not_A_Name"

	unknown := UnknownPackages(values, index)
	want := []UnknownPackage{{Name: "ninja-buld", Nearest: []string{"ninja-build"}}, {Name: "docker-ce"}}
	if len(unknown) != len(want) {
		t.Fatalf("UnknownPackages = %+v, want %+v", unknown, want)
	}
	for position := range want {
		if unknown[position].Name != want[position].Name || !slices.Equal(unknown[position].Nearest, want[position].Nearest) {
			t.Errorf("UnknownPackages[%d] = %+v, want %+v", position, unknown[position], want[position])
		}
	}
	if got := UnknownPackages(values, nil); got != nil {
		t.Errorf("without an index nothing is unknown, got %+v", got)
	}
}

// searchedIndex is a fakeIndex that can also say which repositories it
// searched, as the apt index can.
type searchedIndex struct {
	fakeIndex
	searched, missing []string
}

func (s searchedIndex) Sources() ([]string, []string) { return s.searched, s.missing }

func TestUnknownPackagesWarning(t *testing.T) {
	unknown := []UnknownPackage{{Name: "ninja-buld", Nearest: []string{"ninja-build"}}, {Name: "docker-ce"}}
	values := Defaults(Host{})
	values[KeyRelease] = "24.04"
	archiveAlone := searchedIndex{searched: []string{"noble"}}

	alone := UnknownPackagesWarning(unknown, values, archiveAlone)
	for _, wantText := range []string{"Not in Ubuntu's noble archive:", "  ninja-buld  nearest: ninja-build\n", "  docker-ce\n", "no other source", "Unable to locate package"} {
		if !strings.Contains(alone, wantText) {
			t.Errorf("warning lacks %q:\n%s", wantText, alone)
		}
	}

	// The picker searched the sources too, so a name none of them has is
	// missing for good: saying one of them may provide it would send
	// someone to write a recipe that cannot build.
	values[KeySources] = []string{"docker"}
	searched := UnknownPackagesWarning(unknown, values, searchedIndex{searched: []string{"noble", "docker"}})
	if !strings.Contains(searched, "In neither Ubuntu's noble archive nor docker:") || strings.Contains(searched, "may provide them") {
		t.Errorf("with the source searched the warning must not hold out hope:\n%s", searched)
	}

	// A repository that could not be read is the one case where hope is
	// warranted, and it says which one. A source read only from a cache is
	// in both lists, and must not be recited as searched while being named
	// as unread in the next breath.
	unread := UnknownPackagesWarning(unknown, values, searchedIndex{searched: []string{"noble", "docker"}, missing: []string{"docker"}})
	if !strings.Contains(unread, "docker could not be read, so it may provide them") {
		t.Errorf("a repository that was not read must be named:\n%s", unread)
	}
	if strings.Contains(unread, "nor docker:") {
		t.Errorf("a repository that could not be read was also called searched:\n%s", unread)
	}

	// An index that cannot say what it searched leaves the old answer.
	old := UnknownPackagesWarning(unknown, values, fakeIndex{})
	if !strings.Contains(old, "other sources may provide them") {
		t.Errorf("without a list of repositories the warning should allow for them:\n%s", old)
	}

	if got := UnknownPackagesWarning(nil, values, archiveAlone); got != "" {
		t.Errorf("nothing unknown, nothing said; got %q", got)
	}
}

func TestAndList(t *testing.T) {
	for _, want := range []struct {
		names []string
		text  string
	}{
		{nil, ""},
		{[]string{"docker"}, "docker"},
		{[]string{"docker", "kitware"}, "docker and kitware"},
		{[]string{"docker", "kitware", "llvm"}, "docker, kitware and llvm"},
	} {
		if got := andList(want.names); got != want.text {
			t.Errorf("andList(%q) = %q, want %q", want.names, got, want.text)
		}
	}
}

func TestSourcesAreAskedBeforeThePackagesThatSearchThem(t *testing.T) {
	pages := Pages()
	sourcesAt, packagesAt := slices.Index(pages, PageSources), slices.Index(pages, PagePackages)
	if sourcesAt < 0 || packagesAt < 0 || sourcesAt > packagesAt {
		t.Fatalf("pages = %q; the sources must be chosen before the packages that search them", pages)
	}
	// The order fields are asked in must agree with the order of the pages,
	// which is what the line-by-line interface walks.
	var sawSources bool
	for _, field := range Fields(noHost) {
		switch field.Page {
		case PageSources:
			sawSources = true
		case PagePackages:
			if !sawSources {
				t.Errorf("%s is asked before any source is", field.Key)
			}
		}
	}
}

func TestIndexRequestCarriesTheSourcesChosenSoFar(t *testing.T) {
	values := Defaults(noHost)
	values[KeyRelease] = "24.04"
	plain := IndexRequestFor(values)
	if plain.Release != "24.04" || len(plain.Sources) != 0 {
		t.Errorf("request = %+v, want the release and no source", plain)
	}

	values[KeySources] = []string{"docker"}
	values[KeyPPAs] = "deadsnakes/ppa"
	withSources := IndexRequestFor(values)
	var named []string
	for _, source := range withSources.Sources {
		named = append(named, source.Name)
	}
	if !slices.Contains(named, "docker") || !slices.Contains(named, "ppa-deadsnakes-ppa") {
		t.Errorf("request names %q, want the catalog source and the PPA", named)
	}
	// Resolved, not as they were typed: the picker fetches what these say.
	for _, source := range withSources.Sources {
		if source.URL == "" {
			t.Errorf("%s has no URL to fetch an index from", source.Name)
		}
	}
	// The key is what decides whether an index already opened still answers
	// the question being asked. Adding a source must change it, or the
	// picker would go on searching the archive alone.
	if plain.Key() == withSources.Key() {
		t.Errorf("key %q is unchanged by adding two sources", plain.Key())
	}
	if withSources.Key() != IndexRequestFor(values).Key() {
		t.Error("the same answers must ask for the same index")
	}
}
