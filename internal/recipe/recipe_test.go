package recipe

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// testdataPath returns the path of a fixture in the repository's testdata
// directory.
func testdataPath(name string) string { return filepath.Join("..", "..", "testdata", name) }

// loadValidRecipe loads testdata/valid.toml, which passes validation.
func loadValidRecipe(t *testing.T) Recipe {
	t.Helper()
	imageRecipe, err := Load(testdataPath("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return imageRecipe
}

// writeRecipeFile writes content to a frostroot.toml in a new temporary
// directory and returns its path.
func writeRecipeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadValid(t *testing.T) {
	got := loadValidRecipe(t)
	want := Recipe{
		Image:    Image{Name: "cpp-lab", Release: "22.04", Arch: "amd64"},
		User:     User{Name: "student", Sudo: true},
		WSL:      WSL{Systemd: true, DefaultUser: "student"},
		Locale:   Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: Packages{Include: []string{"git", "build-essential", "cmake"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load(valid.toml) =\n%+v\nwant\n%+v", got, want)
	}
	if problems := Validate(got); len(problems) != 0 {
		t.Fatalf("Validate(valid.toml) = %q, want no problems", problems)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	// A [package] typo must fail loudly, not silently yield an empty include.
	_, err := Load(testdataPath("unknown-field.toml"))
	if err == nil {
		t.Fatal("Load succeeded, want an error for the unknown [package] table")
	}
	for _, wantText := range []string{"unknown-field.toml", "[package]"} {
		if !strings.Contains(err.Error(), wantText) {
			t.Errorf("error should mention %s: %v", wantText, err)
		}
	}
}

func TestLoadErrorsNameTheFile(t *testing.T) {
	testCases := []struct {
		name    string
		content string // empty means the file does not exist
	}{
		{name: "missing file"},
		{name: "syntax error", content: "[image]\nname = \n"},
		{name: "wrong type", content: "[user]\nname = \"student\"\nsudo = \"yes\"\n"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "frostroot.toml")
			if testCase.content != "" {
				path = writeRecipeFile(t, testCase.content)
			}
			_, err := Load(path)
			if err == nil {
				t.Fatal("Load succeeded, want an error")
			}
			if !strings.Contains(err.Error(), "frostroot.toml") {
				t.Errorf("error should name the file: %v", err)
			}
		})
	}
}

func TestLoadAcceptsFilesFromWindowsEditors(t *testing.T) {
	// Recipes get edited in Notepad: CRLF line endings, sometimes a UTF-8 BOM.
	content, err := os.ReadFile(testdataPath("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	withCRLF := strings.ReplaceAll(string(content), "\n", "\r\n")
	variants := map[string]string{
		"CRLF":         withCRLF,
		"BOM":          utf8ByteOrderMark + string(content),
		"BOM and CRLF": utf8ByteOrderMark + withCRLF,
	}
	for name, variant := range variants {
		t.Run(name, func(t *testing.T) {
			imageRecipe, err := Load(writeRecipeFile(t, variant))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(imageRecipe, loadValidRecipe(t)) {
				t.Fatalf("Load = %+v, want the same recipe as valid.toml", imageRecipe)
			}
		})
	}
}

func TestValidateReportsProblem(t *testing.T) {
	testCases := []struct {
		name        string
		breakRecipe func(*Recipe)
		wantMessage string
	}{
		{"unknown release", func(imageRecipe *Recipe) { imageRecipe.Image.Release = "18.04" }, "unknown ubuntu release"},
		{"empty release", func(imageRecipe *Recipe) { imageRecipe.Image.Release = "" }, "unknown ubuntu release"},
		{"empty arch", func(imageRecipe *Recipe) { imageRecipe.Image.Arch = "" }, "amd64"},
		{"arm64", func(imageRecipe *Recipe) { imageRecipe.Image.Arch = "arm64" }, "amd64"},
		{"root user", func(imageRecipe *Recipe) { imageRecipe.User.Name = "root" }, "user name"},
		{"empty user", func(imageRecipe *Recipe) { imageRecipe.User.Name = "" }, "user name"},
		{"invalid characters in user name", func(imageRecipe *Recipe) { imageRecipe.User.Name = "Student!" }, "user name"},
		{"user name too long", func(imageRecipe *Recipe) { imageRecipe.User.Name = strings.Repeat("a", 33) }, "user name"},
		{"empty image name", func(imageRecipe *Recipe) { imageRecipe.Image.Name = "" }, "image name"},
		{"image name with path traversal", func(imageRecipe *Recipe) { imageRecipe.Image.Name = "../evil" }, "image name"},
		{"image name with slash", func(imageRecipe *Recipe) { imageRecipe.Image.Name = "a/b" }, "image name"},
		{"default_user differs from user", func(imageRecipe *Recipe) { imageRecipe.WSL.DefaultUser = "someone" }, "default_user"},
		{"shell in package name", func(imageRecipe *Recipe) { imageRecipe.Packages.Include = []string{"git; rm -rf /"} }, "package name"},
		{"version pin", func(imageRecipe *Recipe) { imageRecipe.Packages.Include = []string{"git=1:2.34.1-1ubuntu1.11"} }, "package name"},
		{"one-character package name", func(imageRecipe *Recipe) { imageRecipe.Packages.Include = []string{"a"} }, "package name"},
		{"shell in locale", func(imageRecipe *Recipe) { imageRecipe.Locale.Lang = "en_US.UTF-8; touch /pwned" }, "locale lang"},
		{"locale without codeset", func(imageRecipe *Recipe) { imageRecipe.Locale.Lang = "en_US" }, "locale lang"},
		{"locale with leading dash", func(imageRecipe *Recipe) { imageRecipe.Locale.Lang = "-x.UTF-8" }, "locale lang"},
		{"timezone with path traversal", func(imageRecipe *Recipe) { imageRecipe.Locale.Timezone = "../../etc/shadow" }, "timezone"},
		{"absolute timezone", func(imageRecipe *Recipe) { imageRecipe.Locale.Timezone = "/etc/passwd" }, "timezone"},
		{"timezone with leading dash", func(imageRecipe *Recipe) { imageRecipe.Locale.Timezone = "-rf" }, "timezone"},
		{"timezone with quote", func(imageRecipe *Recipe) { imageRecipe.Locale.Timezone = "Europe/Ist'anbul" }, "timezone"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			imageRecipe := loadValidRecipe(t)
			testCase.breakRecipe(&imageRecipe)
			problems := Validate(imageRecipe)
			if !strings.Contains(strings.Join(problems, "\n"), testCase.wantMessage) {
				t.Fatalf("Validate = %q, want a problem mentioning %q", problems, testCase.wantMessage)
			}
		})
	}
}

func TestValidateReportsReleaseAndArchSeparately(t *testing.T) {
	imageRecipe := loadValidRecipe(t)
	imageRecipe.Image.Release = "18.04"
	imageRecipe.Image.Arch = "arm64"
	problems := Validate(imageRecipe)
	if len(problems) != 2 {
		t.Fatalf("Validate = %q, want one problem for the release and one for the arch", problems)
	}
	if !strings.Contains(problems[0], "unknown ubuntu release") || !strings.Contains(problems[1], "arm64") {
		t.Fatalf("Validate = %q, want the release problem then the arch problem", problems)
	}
	for _, problem := range problems {
		if strings.Contains(problem, "\n") {
			t.Errorf("each problem should be one line: %q", problem)
		}
	}
}

func TestValidateAcceptsRealisticValues(t *testing.T) {
	testCases := []struct {
		name      string
		adjust    func(imageRecipe *Recipe, value string)
		goodValue []string
	}{
		{
			name:      "locale",
			adjust:    func(imageRecipe *Recipe, value string) { imageRecipe.Locale.Lang = value },
			goodValue: []string{"en_US.UTF-8", "C.UTF-8", "tr_TR.UTF-8", "de_DE.ISO-8859-1", "en_US.utf8"},
		},
		{
			name:      "timezone",
			adjust:    func(imageRecipe *Recipe, value string) { imageRecipe.Locale.Timezone = value },
			goodValue: []string{"UTC", "Europe/Istanbul", "America/Argentina/Buenos_Aires", "Etc/GMT+3", "Etc/GMT-14"},
		},
		{
			name:      "package",
			adjust:    func(imageRecipe *Recipe, value string) { imageRecipe.Packages.Include = []string{value} },
			goodValue: []string{"g++", "libstdc++6", "python3.12", "xz-utils", "r-base"},
		},
		{
			name:      "image name",
			adjust:    func(imageRecipe *Recipe, value string) { imageRecipe.Image.Name = value },
			goodValue: []string{"cpp-lab", "Lab_2026.v1", "x"},
		},
		{
			name: "user name",
			adjust: func(imageRecipe *Recipe, value string) {
				imageRecipe.User.Name = value
				imageRecipe.WSL.DefaultUser = ""
			},
			goodValue: []string{"student", "_lab", "ta-2", strings.Repeat("a", 32)},
		},
	}
	for _, testCase := range testCases {
		for _, value := range testCase.goodValue {
			t.Run(testCase.name+" "+value, func(t *testing.T) {
				imageRecipe := loadValidRecipe(t)
				testCase.adjust(&imageRecipe, value)
				if problems := Validate(imageRecipe); len(problems) != 0 {
					t.Errorf("Validate = %q, want no problems", problems)
				}
			})
		}
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	// An empty recipe has at least an empty image name, an unknown release
	// and arch, and an empty user name.
	if problems := Validate(Recipe{}); len(problems) < 3 {
		t.Fatalf("Validate(Recipe{}) = %q, want several problems", problems)
	}
}

func TestValidateRecipeWithoutUserSection(t *testing.T) {
	path := writeRecipeFile(t, "[image]\nname = \"lab\"\nrelease = \"24.04\"\narch = \"amd64\"\n")
	imageRecipe, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	problems := Validate(imageRecipe)
	if len(problems) != 1 || !strings.Contains(problems[0], "user name") {
		t.Fatalf("Validate = %q, want exactly one problem, about the missing user name", problems)
	}
}

func TestValidateAllowsOptionalSections(t *testing.T) {
	testCases := []struct {
		name   string
		adjust func(*Recipe)
	}{
		{"empty include", func(imageRecipe *Recipe) { imageRecipe.Packages.Include = nil }},
		// The builder falls back to en_US.UTF-8 and UTC.
		{"omitted locale", func(imageRecipe *Recipe) { imageRecipe.Locale = Locale{} }},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			imageRecipe := loadValidRecipe(t)
			testCase.adjust(&imageRecipe)
			if problems := Validate(imageRecipe); len(problems) != 0 {
				t.Fatalf("Validate = %q, want no problems", problems)
			}
		})
	}
}

func TestDefaultUserFallsBackToUserName(t *testing.T) {
	imageRecipe := Recipe{User: User{Name: "student"}}
	if got := DefaultUser(imageRecipe); got != "student" {
		t.Fatalf("DefaultUser without default_user = %q, want student", got)
	}
	imageRecipe.WSL.DefaultUser = "student"
	if got := DefaultUser(imageRecipe); got != "student" {
		t.Fatalf("DefaultUser with default_user = %q, want student", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	original := loadValidRecipe(t)
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := Save(path, original); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, original) {
		t.Fatalf("round trip =\n%+v\nwant\n%+v", reloaded, original)
	}
}
