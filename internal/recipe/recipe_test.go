package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testdata(name string) string { return filepath.Join("..", "..", "testdata", name) }

func loadValid(t *testing.T) Recipe {
	t.Helper()
	r, err := Load(testdata("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestLoadValid(t *testing.T) {
	r := loadValid(t)
	if r.Image.Name != "cpp-lab" || r.Image.Release != "22.04" || r.Image.Arch != "amd64" {
		t.Fatalf("image: %+v", r.Image)
	}
	if r.User.Name != "student" || !r.User.Sudo {
		t.Fatalf("user: %+v", r.User)
	}
	if !r.WSL.Systemd || r.WSL.DefaultUser != "student" {
		t.Fatalf("wsl: %+v", r.WSL)
	}
	if r.Locale.Lang != "en_US.UTF-8" || r.Locale.Timezone != "UTC" {
		t.Fatalf("locale: %+v", r.Locale)
	}
	if len(r.Packages.Include) != 3 {
		t.Fatalf("include: %#v", r.Packages.Include)
	}
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	// A [package] typo must fail loudly, not silently yield an empty include.
	_, err := Load(testdata("unknown-field.toml"))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
	if !strings.Contains(err.Error(), "unknown-field.toml") || !strings.Contains(err.Error(), "[package]") {
		t.Fatalf("error should name the file and the offending table: %v", err)
	}
}

func TestLoadMissingFileNamesPath(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "frostroot.toml"))
	if err == nil || !strings.Contains(err.Error(), "frostroot.toml") {
		t.Fatalf("want an error naming the file, got %v", err)
	}
}

func TestLoadSyntaxErrorNamesPath(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := os.WriteFile(path, []byte("[image]\nname = \n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("want an error naming the file, got %v", err)
	}
}

func TestLoadWrongTypeIsAnError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := os.WriteFile(path, []byte("[user]\nname = \"student\"\nsudo = \"yes\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("sudo must be a boolean")
	}
}

func TestValidateProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Recipe)
		want string
	}{
		{"unknown release", func(r *Recipe) { r.Image.Release = "18.04" }, "unknown ubuntu release"},
		{"empty release", func(r *Recipe) { r.Image.Release = "" }, "unknown ubuntu release"},
		{"empty arch", func(r *Recipe) { r.Image.Arch = "" }, "amd64"},
		{"arm64", func(r *Recipe) { r.Image.Arch = "arm64" }, "amd64"},
		{"root user", func(r *Recipe) { r.User.Name = "root" }, "user name"},
		{"empty user", func(r *Recipe) { r.User.Name = "" }, "user name"},
		{"bad user chars", func(r *Recipe) { r.User.Name = "Student!" }, "user name"},
		{"long user", func(r *Recipe) { r.User.Name = strings.Repeat("a", 33) }, "user name"},
		{"empty image name", func(r *Recipe) { r.Image.Name = "" }, "image name"},
		{"bad image name", func(r *Recipe) { r.Image.Name = "../evil" }, "image name"},
		{"image name with slash", func(r *Recipe) { r.Image.Name = "a/b" }, "image name"},
		{"default_user mismatch", func(r *Recipe) { r.WSL.DefaultUser = "someone" }, "default_user"},
		{"bad package token", func(r *Recipe) { r.Packages.Include = []string{"git; rm -rf /"} }, "package name"},
		{"version pin", func(r *Recipe) { r.Packages.Include = []string{"git=1:2.34.1-1ubuntu1.11"} }, "package name"},
		{"one-char package", func(r *Recipe) { r.Packages.Include = []string{"a"} }, "package name"},
		{"bad lang", func(r *Recipe) { r.Locale.Lang = "en_US.UTF-8; touch /pwned" }, "locale lang"},
		{"lang without codeset", func(r *Recipe) { r.Locale.Lang = "en_US" }, "locale lang"},
		{"lang leading dash", func(r *Recipe) { r.Locale.Lang = "-x.UTF-8" }, "locale lang"},
		{"bad timezone", func(r *Recipe) { r.Locale.Timezone = "../../etc/shadow" }, "timezone"},
		{"absolute timezone", func(r *Recipe) { r.Locale.Timezone = "/etc/passwd" }, "timezone"},
		{"timezone leading dash", func(r *Recipe) { r.Locale.Timezone = "-rf" }, "timezone"},
		{"timezone with quote", func(r *Recipe) { r.Locale.Timezone = "Europe/Ist'anbul" }, "timezone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := loadValid(t)
			tc.mut(&r)
			probs := Validate(r)
			if len(probs) == 0 {
				t.Fatal("expected a problem")
			}
			if !strings.Contains(strings.Join(probs, "\n"), tc.want) {
				t.Fatalf("want %q in %v", tc.want, probs)
			}
		})
	}
}

func TestValidateReportsReleaseAndArchTogether(t *testing.T) {
	r := loadValid(t)
	r.Image.Release = "18.04"
	r.Image.Arch = "arm64"
	probs := Validate(r)
	if len(probs) != 2 {
		t.Fatalf("want one problem for the release and one for the arch, got %v", probs)
	}
	if !strings.Contains(probs[0], "unknown ubuntu release") || !strings.Contains(probs[1], "arm64") {
		t.Fatalf("got %v", probs)
	}
	for _, p := range probs {
		if strings.Contains(p, "\n") {
			t.Fatalf("one problem per line: %q", p)
		}
	}
}

func TestLoadAcceptsWindowsEditors(t *testing.T) {
	// Recipes get edited in Notepad: CRLF line endings, sometimes a UTF-8 BOM.
	body, err := os.ReadFile(testdata("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	crlf := strings.ReplaceAll(string(body), "\n", "\r\n")
	const bom = "\xef\xbb\xbf" // UTF-8 encoding of U+FEFF
	for name, text := range map[string]string{
		"crlf":     crlf,
		"bom":      bom + string(body),
		"bom+crlf": bom + crlf,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "frostroot.toml")
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			r, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if r.Image.Name != "cpp-lab" || len(r.Packages.Include) != 3 {
				t.Fatalf("got %+v", r)
			}
			if probs := Validate(r); len(probs) != 0 {
				t.Fatal(probs)
			}
		})
	}
}

func TestValidateAcceptsRealisticValues(t *testing.T) {
	for _, lang := range []string{"en_US.UTF-8", "C.UTF-8", "tr_TR.UTF-8", "de_DE.ISO-8859-1", "en_US.utf8"} {
		r := loadValid(t)
		r.Locale.Lang = lang
		if probs := Validate(r); len(probs) != 0 {
			t.Errorf("lang %q: %v", lang, probs)
		}
	}
	for _, tz := range []string{"UTC", "Europe/Istanbul", "America/Argentina/Buenos_Aires", "Etc/GMT+3", "Etc/GMT-14"} {
		r := loadValid(t)
		r.Locale.Timezone = tz
		if probs := Validate(r); len(probs) != 0 {
			t.Errorf("timezone %q: %v", tz, probs)
		}
	}
	r := loadValid(t)
	r.Packages.Include = []string{"g++", "libstdc++6", "python3.12", "xz-utils", "r-base"}
	if probs := Validate(r); len(probs) != 0 {
		t.Errorf("packages: %v", probs)
	}
	for _, name := range []string{"cpp-lab", "Lab_2026.v1", "x"} {
		r := loadValid(t)
		r.Image.Name = name
		if probs := Validate(r); len(probs) != 0 {
			t.Errorf("image name %q: %v", name, probs)
		}
	}
	for _, user := range []string{"student", "_lab", "ta-2", strings.Repeat("a", 32)} {
		r := loadValid(t)
		r.User.Name = user
		r.WSL.DefaultUser = ""
		if probs := Validate(r); len(probs) != 0 {
			t.Errorf("user %q: %v", user, probs)
		}
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	probs := Validate(Recipe{})
	// empty image name, unknown arch, empty user name at the very least
	if len(probs) < 3 {
		t.Fatalf("expected several problems, got %v", probs)
	}
}

func TestValidateMissingSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	body := "[image]\nname = \"lab\"\nrelease = \"24.04\"\narch = \"amd64\"\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	probs := Validate(r)
	if len(probs) != 1 || !strings.Contains(probs[0], "user name") {
		t.Fatalf("a recipe without [user] must be rejected, and only for that: %v", probs)
	}
}

func TestValidateAllowsEmptyInclude(t *testing.T) {
	r := loadValid(t)
	r.Packages.Include = nil
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("empty include is legal: %v", probs)
	}
}

func TestValidateAllowsOmittedLocale(t *testing.T) {
	// The builder falls back to en_US.UTF-8 and UTC.
	r := loadValid(t)
	r.Locale = Locale{}
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("omitted locale is legal: %v", probs)
	}
}

func TestDefaultUserFallsBackToUserName(t *testing.T) {
	r := Recipe{User: User{Name: "student"}}
	if got := DefaultUser(r); got != "student" {
		t.Fatalf("got %q", got)
	}
	r.WSL.DefaultUser = "student"
	if got := DefaultUser(r); got != "student" {
		t.Fatalf("got %q", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	in := loadValid(t)
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Image != in.Image || out.User != in.User || out.WSL != in.WSL || out.Locale != in.Locale {
		t.Fatalf("got %+v want %+v", out, in)
	}
	if strings.Join(out.Packages.Include, ",") != strings.Join(in.Packages.Include, ",") {
		t.Fatalf("include %#v", out.Packages.Include)
	}
}
