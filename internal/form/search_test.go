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
	host := Host{OpenIndex: func(context.Context, string, func(int64, int64)) (PackageIndex, error) {
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
		if _, err := field.OpenIndex(context.Background(), "24.04", nil); err != nil || !opened {
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

func TestUnknownPackagesWarning(t *testing.T) {
	unknown := []UnknownPackage{{Name: "ninja-buld", Nearest: []string{"ninja-build"}}, {Name: "docker-ce"}}
	values := Defaults(Host{})
	values[KeyRelease] = "24.04"

	alone := UnknownPackagesWarning(unknown, values)
	for _, wantText := range []string{"Not in Ubuntu's noble archive:", "  ninja-buld  nearest: ninja-build\n", "  docker-ce\n", "no other source", "Unable to locate package"} {
		if !strings.Contains(alone, wantText) {
			t.Errorf("warning lacks %q:\n%s", wantText, alone)
		}
	}

	values[KeySources] = []string{"docker"}
	withSource := UnknownPackagesWarning(unknown, values)
	if !strings.Contains(withSource, "other sources may provide them") || strings.Contains(withSource, "no other source") {
		t.Errorf("with a source chosen the warning should allow for it:\n%s", withSource)
	}

	if got := UnknownPackagesWarning(nil, values); got != "" {
		t.Errorf("nothing unknown, nothing said; got %q", got)
	}
}
