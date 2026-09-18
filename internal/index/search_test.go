package index

import (
	"context"
	"slices"
	"testing"
)

// openExcerpt returns the index of the testdata excerpts.
func openExcerpt(t *testing.T) *Index {
	t.Helper()
	opened, err := Open(context.Background(), optionsFor(t, newArchive(t, ".gz")))
	if err != nil {
		t.Fatal(err)
	}
	return opened
}

func TestSearchRanksNamesAboveDescriptions(t *testing.T) {
	opened := openExcerpt(t)
	tests := []struct {
		name    string
		query   string
		section string
		limit   int
		want    []string
		total   int
	}{
		{
			name: "exact, then prefix by length, then contains, then description", query: "cmake", limit: 10,
			// cmake is exact; cmake-data, cmake-extras and cmake-format start
			// with it, shortest first; extra-cmake-modules contains it;
			// ninja-build and meson do not mention it.
			want: []string{"cmake", "cmake-data", "cmake-extras", "cmake-format", "extra-cmake-modules"}, total: 5,
		},
		{name: "case does not matter", query: "CMake-D", limit: 10, want: []string{"cmake-data"}, total: 1},
		{
			name: "descriptions are searched too", query: "json", limit: 10,
			// python3-json-tricks has it in its name; jq and libjq1 only in
			// their descriptions.
			want: []string{"python3-json-tricks", "jq", "libjq1"}, total: 3,
		},
		{name: "the limit cuts the list, not the count", query: "cmake", limit: 2, want: []string{"cmake", "cmake-data"}, total: 5},
		{name: "a section narrows", query: "cmake", section: "libs", limit: 10, want: []string{"cmake-extras", "extra-cmake-modules"}, total: 2},
		{name: "an empty query browses a section by name", query: "", section: "utils", limit: 10, want: []string{"jq", "libjq1", "rar", "unrar", "zip", "zipcmp"}, total: 6},
		{name: "nothing matches", query: "ninja-buld", limit: 10, want: nil, total: 0},
		{name: "spaces around the query are not part of it", query: "  meson ", limit: 10, want: []string{"meson"}, total: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			matches, total := opened.Search(test.query, test.section, test.limit)
			if got := names(matches); !slices.Equal(got, test.want) || total != test.total {
				t.Errorf("Search(%q, %q) = %v of %d, want %v of %d", test.query, test.section, got, total, test.want, test.total)
			}
		})
	}
}

func TestHas(t *testing.T) {
	opened := openExcerpt(t)
	for name, want := range map[string]bool{"git": true, "zipcmp": true, "catch2": true, "gi": false, "gitk": false, "": false, "zzz": false, "GIT": false} {
		if got := opened.Has(name); got != want {
			t.Errorf("Has(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestNearest(t *testing.T) {
	opened := openExcerpt(t)
	tests := []struct {
		name  string
		limit int
		want  []string
	}{
		{name: "ninja-buld", limit: 3, want: []string{"ninja-build"}},
		{name: "cmkae", limit: 3, want: []string{"cmake"}},
		{name: "zp", limit: 3, want: []string{"zip", "jq"}}, // one edit, then two
		{name: "unrar", limit: 1, want: []string{"unrar"}},
		{name: "rar", limit: 3, want: []string{"rar", "unrar"}},
		{name: "extra-cmake-module", limit: 3, want: []string{"extra-cmake-modules"}},
		{name: "docker-ce", limit: 3, want: nil}, // a different package, not a slip
		{name: "python3-json-trick", limit: 3, want: []string{"python3-json-tricks"}},
	}
	for _, test := range tests {
		if got := opened.Nearest(test.name, test.limit); !slices.Equal(got, test.want) {
			t.Errorf("Nearest(%q) = %v, want %v", test.name, got, test.want)
		}
	}
}

func TestEditDistanceIsBounded(t *testing.T) {
	rows := [2][]int{make([]int, 32), make([]int, 32)}
	tests := []struct {
		left, right string
		bound, want int
	}{
		{"cmake", "cmake", 2, 0},
		{"cmake", "cmak", 2, 1},
		{"cmkae", "cmake", 2, 2},
		{"cmake", "meson", 2, 3}, // over the bound: bound+1, whatever the real distance
		{"", "ab", 2, 2},
		{"ninja-buld", "ninja-build", 2, 1},
	}
	for _, test := range tests {
		if got := editDistance(test.left, test.right, test.bound, rows); got != test.want {
			t.Errorf("editDistance(%q, %q, %d) = %d, want %d", test.left, test.right, test.bound, got, test.want)
		}
	}
}

func TestSections(t *testing.T) {
	opened := openExcerpt(t)
	all := opened.Sections()
	if len(all) < 2 || all[0] != (SectionCount{Name: "devel", Count: 6}) || all[1] != (SectionCount{Name: "utils", Count: 6}) {
		t.Errorf("Sections = %v, want the largest first, and a tie by name", all)
	}
	matching := opened.SectionsMatching("cmake")
	want := []SectionCount{{Name: "devel", Count: 3}, {Name: "libs", Count: 2}}
	if !slices.Equal(matching, want) {
		t.Errorf("SectionsMatching(cmake) = %v, want %v", matching, want)
	}
	// The caller may keep what it is given.
	all[0].Count = 0
	if opened.Sections()[0].Count != 6 {
		t.Error("Sections hands out its own slice")
	}
}

func TestGroupThousands(t *testing.T) {
	for number, want := range map[int]string{0: "0", 17: "17", 999: "999", 1000: "1,000", 85855: "85,855", 1234567: "1,234,567"} {
		if got := groupThousands(number); got != want {
			t.Errorf("groupThousands(%d) = %q, want %q", number, got, want)
		}
	}
}

func TestLookup(t *testing.T) {
	opened := openExcerpt(t)
	meson, isThere := opened.Lookup("meson")
	if want := (Entry{Name: "meson", Version: "1.3.2-1ubuntu1", Component: "universe", Section: "devel", Description: "high-productivity build system"}); !isThere || meson != want {
		t.Errorf("Lookup(meson) = %+v, %v; want %+v", meson, isThere, want)
	}
	for _, name := range []string{"meso", "mesonx", "", "zzz", "0"} {
		if found, isThere := opened.Lookup(name); isThere {
			t.Errorf("Lookup(%q) = %+v, want nothing", name, found)
		}
	}
}
