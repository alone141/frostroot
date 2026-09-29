package form

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// keysOf returns the keys of fields, in order.
func keysOf(fields []Field) []string {
	keys := make([]string, 0, len(fields))
	for _, field := range fields {
		keys = append(keys, field.Key)
	}
	return keys
}

// fieldOf returns the field of key that family asks.
func fieldOf(t *testing.T, family distro.Family, key string) Field {
	t.Helper()
	for _, field := range FieldsFor(Fields(noHost), family) {
		if field.Key == key {
			return field
		}
	}
	t.Fatalf("%s asks no %s", family, key)
	return Field{}
}

// TestTheFamilyIsAskedOnTheImagePage: after the name and before the
// release, whose options follow it.
func TestTheFamilyIsAskedOnTheImagePage(t *testing.T) {
	var onImagePage []Field
	for _, field := range Fields(noHost) {
		if field.Page == PageImage {
			onImagePage = append(onImagePage, field)
		}
	}
	if got, want := keysOf(onImagePage), []string{KeyImageName, KeyDistro, KeyRelease, KeyRelease}; !slices.Equal(got, want) {
		t.Fatalf("the Image page asks %q, want %q", got, want)
	}
	family := onImagePage[1]
	if family.Kind != KindSelect || len(family.Families) != 0 {
		t.Errorf("the family question = %+v, want a select every family asks", family)
	}
	var labels []string
	for _, option := range family.Options {
		labels = append(labels, option.Value+"="+option.DisplayLabel())
	}
	if want := []string{"ubuntu=Ubuntu", "fedora=Fedora"}; !slices.Equal(labels, want) {
		t.Errorf("the families offered are %q, want %q", labels, want)
	}
	for _, release := range onImagePage[2:] {
		if len(release.Families) != 1 {
			t.Errorf("%q is asked for %q, want one family", release.Title, release.Families)
		}
	}
}

// TestFieldsForKeepsThePlainQuestions: the plain interface asks what it
// asked before there was a second family, in the same order, because people
// pipe answers into it. Fedora's questions are Ubuntu's without the sources,
// Python and certificates, which a Fedora recipe cannot hold yet.
func TestFieldsForKeepsThePlainQuestions(t *testing.T) {
	ubuntu := FieldsFor(Fields(noHost), distro.Ubuntu)
	want := []string{KeyImageName, KeyRelease, KeyUserName, KeySudo, KeyTimezone, KeyLocale, KeySystemd, KeySources, KeyPPAs, KeyPackages, KeyOtherPackages, KeyPythonPackages, KeyCertificates}
	if got := keysOf(ubuntu); !slices.Equal(got, want) {
		t.Errorf("Ubuntu asks %q, want %q", got, want)
	}
	if release := fieldOf(t, distro.Ubuntu, KeyRelease); release.Title != "Ubuntu release" || len(release.Options) != len(distro.SupportedVersions(distro.Ubuntu)) {
		t.Errorf("Ubuntu's release = %q with %d options", release.Title, len(release.Options))
	}

	fedora := FieldsFor(Fields(noHost), distro.Fedora)
	want = []string{KeyImageName, KeyRelease, KeyUserName, KeySudo, KeyTimezone, KeyLocale, KeySystemd, KeyPackages, KeyOtherPackages}
	if got := keysOf(fedora); !slices.Equal(got, want) {
		t.Errorf("Fedora asks %q, want %q", got, want)
	}
	release := fieldOf(t, distro.Fedora, KeyRelease)
	if release.Title != "Fedora release" || len(release.Options) != 1 || release.Options[0].Value != "44" || release.Options[0].DisplayLabel() != "Fedora 44" {
		t.Errorf("Fedora's release = %+v", release)
	}
	var offered []string
	for _, option := range fieldOf(t, distro.Fedora, KeyPackages).Options {
		offered = append(offered, option.Value)
	}
	var catalog []string
	for _, entry := range Catalog(distro.Fedora) {
		catalog = append(catalog, entry.Name)
	}
	if !slices.Equal(offered, catalog) {
		t.Errorf("Fedora's packages offer %q, want its catalog", offered)
	}
}

// TestFedorasOtherPackagesAreFedoraNames: typed names are checked by the
// rule a Fedora recipe is validated by, and searched in Fedora's index.
func TestFedorasOtherPackagesAreFedoraNames(t *testing.T) {
	opened := IndexRequest{}
	host := Host{OpenIndex: func(_ context.Context, request IndexRequest, _ func(int64, int64)) (PackageIndex, error) {
		opened = request
		return fakeIndex{}, nil
	}}
	var fedora, ubuntu Field
	for _, field := range Fields(host) {
		if field.Key != KeyOtherPackages {
			continue
		}
		if field.AskedFor(distro.Fedora) {
			fedora = field
		} else {
			ubuntu = field
		}
	}
	if fedora.Kind != KindSearch || fedora.OpenIndex == nil || fedora.Sourced {
		t.Fatalf("Fedora's field = %+v, want a search of the host's index that no source changes", fedora)
	}
	if _, err := fedora.OpenIndex(context.Background(), IndexRequest{Family: distro.Fedora, Release: "44"}, nil); err != nil || opened.Family != distro.Fedora {
		t.Errorf("the field does not open the host's index: %v, %+v", err, opened)
	}
	if !strings.Contains(fedora.Description, "Fedora") {
		t.Errorf("the field says %q, which names no Fedora", fedora.Description)
	}
	for text, valid := range map[string]bool{"": true, "NetworkManager perl-File-Temp, git": true, "git=2.55": false, "@core": false, "vim*": false} {
		if err := fedora.Validate(text); (err == nil) != valid {
			t.Errorf("Fedora's Validate(%q) = %v, want valid=%v", text, err, valid)
		}
	}
	if err := ubuntu.Validate("NetworkManager"); err == nil {
		t.Error("Ubuntu's field accepted NetworkManager, which apt cannot install")
	}
}

func TestForFamily(t *testing.T) {
	values := Defaults(noHost)
	values[KeyPackages] = []string{"build-essential", "git", "cmake"}
	values[KeyOtherPackages] = "libfoo-dev"

	fedora := ForFamily(values, distro.Fedora)
	if Family(fedora) != distro.Fedora || fedora.String(KeyRelease) != "44" {
		t.Errorf("switched to Fedora: %s %s, want fedora 44", fedora.String(KeyDistro), fedora.String(KeyRelease))
	}
	// build-essential is Ubuntu's; git and cmake are in both catalogs.
	if got := fedora.Strings(KeyPackages); !slices.Equal(got, []string{"git", "cmake"}) {
		t.Errorf("catalog selections = %q, want git and cmake", got)
	}
	// A typed name stays, for the index to say whether Fedora has it.
	if got := fedora.String(KeyOtherPackages); got != "libfoo-dev" {
		t.Errorf("other packages = %q", got)
	}
	if values.String(KeyRelease) != "26.04" || len(values.Strings(KeyPackages)) != 3 {
		t.Error("ForFamily changed the answers it was given")
	}

	// Back to Ubuntu: 44 is none of its releases, so it starts on the newest.
	if back := ForFamily(fedora, distro.Ubuntu); back.String(KeyRelease) != distro.NewestVersion(distro.Ubuntu) || Family(back) != distro.Ubuntu {
		t.Errorf("switched back: %s %s", back.String(KeyDistro), back.String(KeyRelease))
	}
	// A release that is the family's own is kept.
	values[KeyRelease] = "22.04"
	if got := ForFamily(values, distro.Ubuntu).String(KeyRelease); got != "22.04" {
		t.Errorf("release = %q, want 22.04 kept", got)
	}
}

// TestAFedoraRecipeHoldsWhatFedoraCan: an Ubuntu recipe switched to Fedora
// loses what a Fedora recipe cannot hold, whatever its answers carried, and
// validates.
func TestAFedoraRecipeHoldsWhatFedoraCan(t *testing.T) {
	llvm, _ := sources.Lookup("llvm")
	ubuntu := recipe.Recipe{
		Image:        recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:         recipe.User{Name: "student", Sudo: true},
		WSL:          recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:       recipe.Locale{Lang: "tr_TR.UTF-8", Timezone: "Europe/Istanbul"},
		Packages:     recipe.Packages{Include: []string{"build-essential", "git", "mytool"}},
		Sources:      []recipe.Source{llvm.Source("noble"), sources.PPA("deadsnakes", "ppa")},
		Python:       &recipe.Python{Include: []string{"requests"}, IndexURL: "https://pypi.example.com/simple"},
		Certificates: &recipe.Certificates{Include: []string{"corp.pem"}},
	}
	got := ToRecipe(ForFamily(FromRecipe(ubuntu), distro.Fedora))
	want := recipe.Recipe{
		Image:    recipe.Image{Name: "lab", Distro: "fedora", Release: "44", Arch: "amd64"},
		User:     ubuntu.User,
		WSL:      ubuntu.WSL,
		Locale:   ubuntu.Locale,
		Packages: recipe.Packages{Include: []string{"git", "mytool"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("switched to Fedora:\n got %+v\nwant %+v", got, want)
	}
	if problems := recipe.Validate(got); len(problems) != 0 {
		t.Errorf("Validate = %q", problems)
	}
}

// TestTheRecipeNamesItsFamilyOnlyWhenItMust: an Ubuntu recipe that init
// writes or edit rewrites is the bytes it always was, without the line; a
// Fedora recipe needs it, and a recipe that named its family keeps naming
// it.
func TestTheRecipeNamesItsFamilyOnlyWhenItMust(t *testing.T) {
	named := func(distroName string) recipe.Recipe {
		return recipe.Recipe{Image: recipe.Image{Name: "lab", Distro: distroName, Release: "44", Arch: "amd64"}}
	}
	testCases := []struct {
		name   string
		values Values
		want   string
	}{
		{"init", Defaults(noHost), ""},
		{"init on Fedora", ForFamily(Defaults(noHost), distro.Fedora), "fedora"},
		{"an edit of a recipe that names none", FromRecipe(named("")), ""},
		{"an edit of a recipe that names Ubuntu", FromRecipe(named("ubuntu")), "ubuntu"},
		{"an edit of a Fedora recipe", FromRecipe(named("fedora")), "fedora"},
		{"a Fedora recipe edited into an Ubuntu one", ForFamily(FromRecipe(named("fedora")), distro.Ubuntu), "ubuntu"},
		{"an Ubuntu recipe edited into a Fedora one", ForFamily(FromRecipe(named("")), distro.Fedora), "fedora"},
	}
	for _, testCase := range testCases {
		if got := ToRecipe(testCase.values).Image.Distro; got != testCase.want {
			t.Errorf("%s: distro = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}

func TestSummaryNamesTheFamily(t *testing.T) {
	values := ForFamily(Defaults(noHost), distro.Fedora)
	values[KeyPackages] = []string{"gcc", "build-essential"}
	// Answers an Ubuntu recipe carried into the switch; Fedora has no such
	// questions and the recipe holds none of them.
	values[KeySources] = []string{"docker"}
	values[KeyPythonPackages] = "requests"
	values[KeyCertificates] = "corp.pem"
	want := "Image     lab, Fedora 44 amd64\nUser      student, passwordless sudo\nSystem    UTC, en_US.UTF-8, systemd on\nPackages  gcc"
	if got := Summary(values); got != want {
		t.Errorf("Summary =\n%s\nwant\n%s", got, want)
	}
	ubuntu := "Image     lab, Ubuntu 26.04 amd64\nUser      student, passwordless sudo\nSystem    UTC, en_US.UTF-8, systemd on\nSources   Ubuntu's archive only\nPackages  none"
	if got := Summary(Defaults(noHost)); got != ubuntu {
		t.Errorf("Ubuntu's Summary =\n%s\nwant it as it was:\n%s", got, ubuntu)
	}
}

func TestIndexRequestForFedora(t *testing.T) {
	values := ForFamily(Defaults(noHost), distro.Fedora)
	values[KeySources] = []string{"docker"} // carried from an Ubuntu recipe
	request := IndexRequestFor(values)
	if request.Family != distro.Fedora || request.Release != "44" || len(request.Sources) != 0 {
		t.Errorf("request = %+v, want Fedora 44 and no apt source", request)
	}
	if request.Key() == (IndexRequest{Release: "44"}).Key() {
		t.Errorf("key %q does not tell Fedora from Ubuntu", request.Key())
	}
	// An Ubuntu request's key is what it was, family or none.
	if (IndexRequest{Family: distro.Ubuntu, Release: "24.04"}).Key() != (IndexRequest{Release: "24.04"}).Key() {
		t.Error("naming Ubuntu changed the key of an Ubuntu request")
	}
}

func TestUnknownPackagesOfAFedoraRecipe(t *testing.T) {
	index := searchedIndex{
		fakeIndex: fakeIndex{names: []string{"git", "NetworkManager"}, nearest: map[string][]string{"ninja-buld": {"ninja-build"}}},
		searched:  []string{"Fedora 44"},
	}
	values := ForFamily(Defaults(noHost), distro.Fedora)
	values[KeyOtherPackages] = "NetworkManager ninja-buld"
	values[KeyPythonPackages] = "nosuchproject" // carried from an Ubuntu recipe; never asked for
	unknown := UnknownPackages(values, index)
	if len(unknown) != 1 || unknown[0].Name != "ninja-buld" {
		t.Fatalf("UnknownPackages = %+v, want ninja-buld alone: NetworkManager is a Fedora name", unknown)
	}
	want := "Not in Fedora 44's repositories:\n  ninja-buld  nearest: ninja-build\nbuild will stop at \"No match for argument\"."
	if got := Warnings(values, index, fakeIndex{}); got != want {
		t.Errorf("Warnings =\n%s\nwant\n%s", got, want)
	}
	index.missing = []string{"repositories"}
	if got := Warnings(values, index, fakeIndex{}); !strings.Contains(got, "could not be read") || !strings.HasSuffix(got, "\"No match for argument\".") {
		t.Errorf("with an older copy of the index, Warnings =\n%s", got)
	}
}
