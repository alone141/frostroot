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
	"frostroot/internal/sources"
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

func TestSourcesFieldsAndValidators(t *testing.T) {
	fieldsByKey := map[string]Field{}
	for _, field := range Fields(noHost) {
		fieldsByKey[field.Key] = field
	}
	if fieldsByKey[KeySources].Page != PageSources || fieldsByKey[KeyPPAs].Page != PageSources {
		t.Error("the source fields belong on the Sources page")
	}
	if len(fieldsByKey[KeySources].Options) != len(sources.Catalog()) {
		t.Errorf("the sources field offers %d options, want the whole catalog", len(fieldsByKey[KeySources].Options))
	}
	for value, valid := range map[string]bool{"": true, "deadsnakes/ppa git-core/ppa": true, "ppa:deadsnakes/ppa, x/y": true, "deadsnakes": false, "Dead/ppa": false} {
		if err := fieldsByKey[KeyPPAs].Validate(value); (err == nil) != valid {
			t.Errorf("ppas.Validate(%q) = %v, want valid=%v", value, err, valid)
		}
	}
}

func TestFromRecipeSplitsSources(t *testing.T) {
	custom := recipe.Source{Name: "corp", URL: "https://apt.corp.example/ubuntu", Key: "keys/corp.asc"}
	imageRecipe := recipe.Recipe{Sources: []recipe.Source{
		sources.PPA("deadsnakes", "ppa"), custom, {Name: "docker", URL: "https://download.docker.com/linux/ubuntu", Components: []string{"stable"}, Key: "keys/docker.asc"},
	}}
	values := FromRecipe(imageRecipe)
	if got := values.Strings(KeySources); !slices.Equal(got, []string{"docker"}) {
		t.Errorf("catalog selections = %q", got)
	}
	if got := values.String(KeyPPAs); got != "deadsnakes/ppa" {
		t.Errorf("ppas = %q", got)
	}
	if got := values.Sources(keyOriginalSources); !reflect.DeepEqual(got, imageRecipe.Sources) {
		t.Errorf("original sources = %+v", got)
	}
	cloned := values.Clone()
	cloned.Sources(keyOriginalSources)[0].Name = "changed"
	if values.Sources(keyOriginalSources)[0].Name != "ppa-deadsnakes-ppa" {
		t.Error("Clone must copy source lists")
	}
}

func TestToRecipeRewritesUneditedCatalogSourcesWhenTheReleaseChanges(t *testing.T) {
	llvm, _ := sources.Lookup("llvm")
	imageRecipe := recipe.Recipe{
		Image:   recipe.Image{Name: "lab", Release: "22.04", Arch: "amd64"},
		Sources: []recipe.Source{llvm.Source("jammy")},
	}
	values := FromRecipe(imageRecipe)
	if values.String(keyOriginalRelease) != "22.04" {
		t.Fatalf("original release = %q, want 22.04", values.String(keyOriginalRelease))
	}
	values[KeyRelease] = "24.04"
	got := ToRecipe(values)
	if len(got.Sources) != 1 || !reflect.DeepEqual(got.Sources[0], llvm.Source("noble")) {
		t.Errorf("Sources = %+v, want LLVM for noble", got.Sources)
	}
}

func TestMergeSources(t *testing.T) {
	docker, _ := sources.Lookup("docker")
	llvm, _ := sources.Lookup("llvm")
	custom := recipe.Source{Name: "corp", URL: "https://apt.corp.example/ubuntu", Key: "keys/corp.asc"}
	editedDocker := recipe.Source{Name: "docker", URL: "https://mirror.example/docker", Components: []string{"stable"}, Key: "keys/docker.asc"}
	testCases := []struct {
		name     string
		selected []string
		ppas     string
		original []recipe.Source
		want     []recipe.Source
	}{
		{name: "nothing", want: nil},
		{name: "catalog entries resolved for the release", selected: []string{"llvm", "docker"}, want: []recipe.Source{llvm.Source("jammy"), docker.Source("jammy")}},
		{name: "ppas parsed and deduplicated", ppas: "deadsnakes/ppa, ppa:deadsnakes/ppa git-core/ppa", want: []recipe.Source{sources.PPA("deadsnakes", "ppa"), sources.PPA("git-core", "ppa")}},
		{name: "custom sources kept, deselected dropped, order kept", selected: []string{"docker"}, original: []recipe.Source{llvm.Source("jammy"), custom, editedDocker}, want: []recipe.Source{custom, editedDocker}},
		{name: "new selections follow the originals", selected: []string{"docker", "llvm"}, ppas: "git-core/ppa", original: []recipe.Source{custom, docker.Source("jammy")}, want: []recipe.Source{custom, docker.Source("jammy"), llvm.Source("jammy"), sources.PPA("git-core", "ppa")}},
		{name: "unknown selection ignored", selected: []string{"nope"}, want: nil},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got := MergeSources(testCase.selected, testCase.ppas, testCase.original, "jammy", "jammy")
			if !reflect.DeepEqual(got, testCase.want) {
				t.Errorf("MergeSources =\n%+v\nwant\n%+v", got, testCase.want)
			}
		})
	}
}

func TestMergeSourcesRewritesUneditedCatalogEntriesForTheNewRelease(t *testing.T) {
	// edit that only changes the Ubuntu release must not leave LLVM on
	// jammy: the catalog URL and suite embed {suite}. A hand-edited URL
	// stays as typed.
	llvm, _ := sources.Lookup("llvm")
	docker, _ := sources.Lookup("docker")
	custom := recipe.Source{Name: "corp", URL: "https://apt.corp.example/ubuntu", Key: "keys/corp.asc"}
	editedDocker := recipe.Source{Name: "docker", URL: "https://mirror.example/docker", Components: []string{"stable"}, Key: "keys/docker.asc"}
	got := MergeSources([]string{"llvm", "docker"}, "", []recipe.Source{llvm.Source("jammy"), custom, editedDocker}, "jammy", "noble")
	want := []recipe.Source{llvm.Source("noble"), custom, editedDocker}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("MergeSources =\n%+v\nwant\n%+v", got, want)
	}
	// Docker's catalog row has no {suite}; jammy and noble resolve the same.
	got = MergeSources([]string{"docker"}, "", []recipe.Source{docker.Source("jammy")}, "jammy", "noble")
	if !reflect.DeepEqual(got, []recipe.Source{docker.Source("noble")}) {
		t.Errorf("docker = %+v, want the catalog row for noble", got)
	}
	// A catalog row loaded with an empty components array is still unedited.
	jammyLLVM := llvm.Source("jammy")
	jammyLLVM.Components = []string{}
	got = MergeSources([]string{"llvm"}, "", []recipe.Source{jammyLLVM}, "jammy", "noble")
	if !reflect.DeepEqual(got, []recipe.Source{llvm.Source("noble")}) {
		t.Errorf("empty components = %+v, want LLVM for noble", got)
	}
}

func TestToRecipeResolvesSourcesForTheRelease(t *testing.T) {
	values := Defaults(noHost)
	values[KeyRelease] = "22.04"
	values[KeySources] = []string{"llvm"}
	values[KeyPPAs] = "deadsnakes/ppa"
	imageRecipe := ToRecipe(values)
	if len(imageRecipe.Sources) != 2 || imageRecipe.Sources[0].URL != "https://apt.llvm.org/jammy" || imageRecipe.Sources[0].Suite != "llvm-toolchain-jammy" || imageRecipe.Sources[1].Name != "ppa-deadsnakes-ppa" {
		t.Errorf("Sources = %+v", imageRecipe.Sources)
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("Validate = %q", problems)
	}
	summary := Summary(values)
	if !strings.Contains(summary, "Sources   LLVM, PPA deadsnakes/ppa") {
		t.Errorf("summary lacks the sources line:\n%s", summary)
	}
	if plain := Summary(Defaults(noHost)); !strings.Contains(plain, "Sources   Ubuntu's archive only") {
		t.Errorf("summary without sources:\n%s", plain)
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

// TestPPAFieldRefusesTwoPPAsWithOneName: the recipe holds one source per
// name, so a pair that folds to the same name must be refused in the field,
// where it can still be fixed, rather than one of them being dropped
// silently on the way to the summary.
func TestPPAFieldRefusesTwoPPAsWithOneName(t *testing.T) {
	err := checkPPAList("foo.bar/baz foo-bar/baz")
	if err == nil {
		t.Fatal("two PPAs that fold to one name were accepted")
	}
	for _, want := range []string{"foo.bar/baz", "foo-bar/baz", "ppa-foo-bar-baz"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
	// The same PPA written twice is not a collision: it is one source.
	if err := checkPPAList("deadsnakes/ppa ppa:deadsnakes/ppa"); err != nil {
		t.Errorf("the same PPA twice = %v", err)
	}
	// A PPA too long to name in full is now usable, not refused after the form.
	if err := checkPPAList("canonical-server/server-backports"); err != nil {
		t.Errorf("a long PPA = %v", err)
	}
}

// TestEditKeepsThePythonIndexURL: [python] index_url is written by hand and
// has no field of its own, so the form has to give it back. Dropping it
// would silently move an image's resolution back to PyPI the first time
// somebody ran frostroot edit.
func TestEditKeepsThePythonIndexURL(t *testing.T) {
	const index = "https://nexus.example.com/repository/pypi/simple"
	original := recipe.Recipe{
		Image:  recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:   recipe.User{Name: "student"},
		Python: &recipe.Python{Include: []string{"requests"}, IndexURL: index},
	}
	roundTripped := ToRecipe(FromRecipe(original))
	if got := roundTripped.PythonIndexURL(); got != index {
		t.Errorf("index_url after an edit = %q, want %q", got, index)
	}
	// Removing every package removes the table, index and all: there is
	// nothing left for an index to resolve.
	values := FromRecipe(original)
	values[KeyPythonPackages] = ""
	if table := ToRecipe(values).Python; table != nil {
		t.Errorf("python table = %+v, want none once no package is asked for", table)
	}
}
