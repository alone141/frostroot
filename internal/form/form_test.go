package form

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// fakeHost returns a Host whose files are the given map; anything else does
// not exist.
func fakeHost(files map[string]string) Host {
	return Host{ReadFile: func(name string) ([]byte, error) {
		content, found := files[name]
		if !found {
			return nil, os.ErrNotExist
		}
		return []byte(content), nil
	}}
}

// noHost is a Host that can read nothing.
var noHost = fakeHost(nil)

func TestFieldsAreWellFormed(t *testing.T) {
	fields := Fields(noHost)
	seenKeys := map[string]bool{}
	pages := Pages()
	lastPageIndex := 0
	for _, field := range fields {
		if field.Key == "" || strings.ToLower(field.Key) != field.Key || seenKeys[field.Key] {
			t.Errorf("field key %q must be lowercase and unique", field.Key)
		}
		seenKeys[field.Key] = true
		if field.Title == "" {
			t.Errorf("%s has no title", field.Key)
		}
		pageIndex := slices.Index(pages, field.Page)
		if pageIndex < 0 {
			t.Errorf("%s is on unknown page %q", field.Key, field.Page)
		}
		if pageIndex < lastPageIndex {
			t.Errorf("%s is on %s after a field on %s; fields must follow page order", field.Key, field.Page, pages[lastPageIndex])
		}
		lastPageIndex = max(lastPageIndex, pageIndex)
		switch field.Kind {
		case KindSelect, KindMultiSelect:
			if len(field.Options) == 0 {
				t.Errorf("%s offers no options", field.Key)
			}
			values := map[string]bool{}
			for _, option := range field.Options {
				if option.Value == "" || values[option.Value] {
					t.Errorf("%s has an empty or duplicate option %q", field.Key, option.Value)
				}
				values[option.Value] = true
			}
		case KindInput:
			if field.Validate == nil {
				t.Errorf("%s accepts free text without validation", field.Key)
			}
		case KindConfirm:
		}
	}
	// Every default answers a field, and every answer is one of the options.
	defaults := Defaults(noHost)
	for _, field := range fields {
		answer, answered := defaults[field.Key]
		if !answered {
			t.Errorf("no default for %s", field.Key)
			continue
		}
		if field.Kind == KindSelect && !slices.ContainsFunc(field.Options, func(option Option) bool { return option.Value == answer }) {
			t.Errorf("default %v for %s is not an option", answer, field.Key)
		}
	}
}

func TestInputValidators(t *testing.T) {
	fieldsByKey := map[string]Field{}
	for _, field := range Fields(noHost) {
		fieldsByKey[field.Key] = field
	}
	testCases := []struct {
		key   string
		value string
		valid bool
	}{
		{KeyImageName, "cpp-lab", true},
		{KeyImageName, "../x", false},
		{KeyImageName, "", false},
		{KeyUserName, "student", true},
		{KeyUserName, "root", false},
		{KeyUserName, "Student", false},
		{KeyOtherPackages, "", true},
		{KeyOtherPackages, "git, cmake  ninja-build", true},
		{KeyOtherPackages, "git; rm -rf /", false},
		{KeyOtherPackages, "git=1.0", false},
	}
	for _, testCase := range testCases {
		err := fieldsByKey[testCase.key].Validate(testCase.value)
		if (err == nil) != testCase.valid {
			t.Errorf("%s.Validate(%q) = %v, want valid=%v", testCase.key, testCase.value, err, testCase.valid)
		}
	}
}

func TestDefaultsMakeAValidRecipe(t *testing.T) {
	imageRecipe := ToRecipe(Defaults(noHost))
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Fatalf("the defaults do not validate: %v", problems)
	}
	want := recipe.Recipe{
		Image:    recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{}},
	}
	if !reflect.DeepEqual(imageRecipe, want) {
		t.Errorf("recipe from defaults:\n got %+v\nwant %+v", imageRecipe, want)
	}
}

func TestDefaultTimezoneFollowsTheHost(t *testing.T) {
	host := fakeHost(map[string]string{"/etc/timezone": "Europe/Istanbul\n"})
	if got := Defaults(host).String(KeyTimezone); got != "Europe/Istanbul" {
		t.Errorf("timezone default = %q, want the host's", got)
	}
	unknownHost := fakeHost(map[string]string{"/etc/timezone": "Mars/Olympus_Mons"})
	if got := Defaults(unknownHost).String(KeyTimezone); got != "UTC" {
		t.Errorf("timezone default = %q, want UTC for an unknown host zone", got)
	}
}

func TestRecipeRoundTrip(t *testing.T) {
	fixtures, err := filepath.Glob(filepath.Join("..", "..", "testdata", "*.toml"))
	if err != nil || len(fixtures) == 0 {
		t.Fatalf("fixtures: %v, %v", fixtures, err)
	}
	for _, fixture := range fixtures {
		t.Run(filepath.Base(fixture), func(t *testing.T) {
			imageRecipe, err := recipe.Load(fixture)
			if err != nil {
				t.Skipf("fixture does not parse: %v", err)
			}
			if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
				t.Skipf("fixture is deliberately invalid: %v", problems)
			}
			roundTripped := ToRecipe(FromRecipe(imageRecipe))
			// The form always writes the defaults the recipe may omit.
			want := imageRecipe
			want.WSL.DefaultUser = recipe.DefaultUser(imageRecipe)
			if want.Locale.Lang == "" {
				want.Locale.Lang = "en_US.UTF-8"
			}
			if want.Locale.Timezone == "" {
				want.Locale.Timezone = "UTC"
			}
			if want.Packages.Include == nil {
				want.Packages.Include = []string{}
			}
			if !reflect.DeepEqual(roundTripped, want) {
				t.Errorf("round trip changed the recipe:\n got %+v\nwant %+v", roundTripped, want)
			}
		})
	}
}

func TestFromRecipeSplitsPackages(t *testing.T) {
	imageRecipe := recipe.Recipe{Packages: recipe.Packages{Include: []string{"git", "libfoo-dev", "cmake", "mytool"}}}
	values := FromRecipe(imageRecipe)
	if got := values.Strings(KeyPackages); !slices.Equal(got, []string{"git", "cmake"}) {
		t.Errorf("catalog selections = %q, want git and cmake", got)
	}
	if got := values.String(KeyOtherPackages); got != "libfoo-dev mytool" {
		t.Errorf("other packages = %q", got)
	}
}

func TestMergePackages(t *testing.T) {
	testCases := []struct {
		name            string
		selected        []string
		other           string
		originalInclude []string
		want            []string
	}{
		{name: "nothing", want: []string{}},
		{name: "selection order without an original", selected: []string{"cmake", "git"}, want: []string{"cmake", "git"}},
		{name: "others split and deduplicated", selected: []string{"git"}, other: " ninja-build, git  libfoo-dev ", want: []string{"git", "ninja-build", "libfoo-dev"}},
		{name: "original order kept", selected: []string{"build-essential", "cmake", "git"}, originalInclude: []string{"git", "build-essential", "cmake"}, want: []string{"git", "build-essential", "cmake"}},
		{name: "deselected originals dropped, new ones appended", selected: []string{"cmake", "gdb"}, other: "x", originalInclude: []string{"git", "cmake"}, want: []string{"cmake", "gdb", "x"}},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := MergePackages(testCase.selected, testCase.other, testCase.originalInclude)
			if !slices.Equal(got, testCase.want) {
				t.Errorf("MergePackages = %q, want %q", got, testCase.want)
			}
		})
	}
}

func TestCatalog(t *testing.T) {
	seen := map[string]bool{}
	categories := Categories()
	for _, entry := range Catalog() {
		if seen[entry.Name] {
			t.Errorf("%s is listed twice", entry.Name)
		}
		seen[entry.Name] = true
		if err := recipe.CheckPackageName(entry.Name); err != nil {
			t.Error(err)
		}
		if entry.Description == "" || !slices.Contains(categories, entry.Category) {
			t.Errorf("%+v needs a description and a known category", entry)
		}
	}
	// The v0.1 presets are still expressible.
	for _, presetPackage := range []string{"build-essential", "git", "cmake", "pkg-config", "python3", "python3-pip", "python3-venv"} {
		if !InCatalog(presetPackage) {
			t.Errorf("%s was in a v0.1 preset and must stay in the catalog", presetPackage)
		}
	}
	if !slices.Equal(categories[:2], []string{"C/C++", "Python"}) {
		t.Errorf("categories = %q, want C/C++ first", categories)
	}
}

func TestTimezones(t *testing.T) {
	table := "# comment\nTR\t+4101+02858\tEurope/Istanbul\nJP\t+353916+1394441\tAsia/Tokyo\nbad line\n"
	host := fakeHost(map[string]string{"/usr/share/zoneinfo/zone1970.tab": table})
	if got := Timezones(host); !slices.Equal(got, []string{"UTC", "Asia/Tokyo", "Europe/Istanbul"}) {
		t.Errorf("Timezones = %q, want UTC first, then the table sorted", got)
	}
	embedded := Timezones(noHost)
	if len(embedded) < 300 || embedded[0] != "UTC" || !slices.Contains(embedded, "Europe/Istanbul") || !slices.Contains(embedded, "America/Argentina/Buenos_Aires") {
		t.Errorf("embedded zone table gives %d zones starting %q", len(embedded), embedded[:min(3, len(embedded))])
	}
	if !slices.IsSorted(embedded[1:]) {
		t.Error("zones after UTC must be sorted")
	}
	for _, zone := range embedded {
		if err := recipe.CheckTimezone(zone); err != nil {
			t.Error(err)
		}
	}
	emptyHost := fakeHost(map[string]string{"/usr/share/zoneinfo/zone1970.tab": "# nothing\n"})
	if got := Timezones(emptyHost); len(got) != len(embedded) {
		t.Errorf("an empty host table must fall back to the embedded one, got %d zones", len(got))
	}
}

func TestHostTimezone(t *testing.T) {
	if got := HostTimezone(Host{}); got != "UTC" {
		t.Errorf("HostTimezone(no reader) = %q", got)
	}
	if got := HostTimezone(fakeHost(map[string]string{"/etc/timezone": "Asia/Tokyo\n"})); got != "Asia/Tokyo" {
		t.Errorf("HostTimezone = %q, want Asia/Tokyo", got)
	}
	failingHost := Host{ReadFile: func(string) ([]byte, error) { return nil, errors.New("disk on fire") }}
	if got := HostTimezone(failingHost); got != "UTC" {
		t.Errorf("HostTimezone(failing reader) = %q", got)
	}
}

func TestLocales(t *testing.T) {
	locales := Locales()
	if len(locales) == 0 || locales[0].Value != "en_US.UTF-8" {
		t.Fatalf("Locales = %+v, want en_US.UTF-8 first", locales)
	}
	for _, locale := range locales {
		if err := recipe.CheckLocale(locale.Value); err != nil {
			t.Error(err)
		}
		if locale.Label == "" {
			t.Errorf("%s has no label", locale.Value)
		}
	}
}

func TestValuesAccessors(t *testing.T) {
	values := Values{"text": "hi", "flag": true, "list": []string{"a"}}
	if values.String("text") != "hi" || values.String("flag") != "" || !values.Bool("flag") || values.Bool("text") {
		t.Errorf("typed accessors misread %+v", values)
	}
	cloned := values.Clone()
	cloned.Strings("list")[0] = "changed"
	if values.Strings("list")[0] != "a" {
		t.Error("Clone must copy lists")
	}
}
