package recipe

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// fakeKeyPacket is the smallest thing pgp accepts as a public key: an
// old-format public-key packet holding a version 4 body.
var fakeKeyPacket = []byte{0x99, 0x00, 0x03, 0x04, 0x00, 0x00}

// writeKey writes fakeKeyPacket at keyPath under dir.
func writeKey(t *testing.T, dir, keyPath string) {
	t.Helper()
	fullPath := filepath.Join(dir, filepath.FromSlash(keyPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, fakeKeyPacket, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestSourceEqualTreatsNilAndEmptyComponentsAsTheSame(t *testing.T) {
	left := Source{Name: "llvm", URL: "https://apt.llvm.org/jammy", Suite: "llvm-toolchain-jammy", Key: "keys/llvm.asc"}
	right := left
	right.Components = []string{}
	if !left.Equal(right) {
		t.Fatal("nil components and empty components must be the same source")
	}
	right.URL = "https://apt.llvm.org/noble"
	if left.Equal(right) {
		t.Fatal("a different URL must not compare equal")
	}
}

func TestLoadRecipeWithSources(t *testing.T) {
	imageRecipe, err := Load(testdataPath("sources.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantSources := []Source{
		{Name: "docker", URL: "https://download.docker.com/linux/ubuntu", Components: []string{"stable"}, Key: "keys/docker.asc"},
		{Name: "ppa-deadsnakes-ppa", URL: "https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu", Suite: "noble", Key: "keys/ppa-deadsnakes-ppa.asc"},
	}
	if !reflect.DeepEqual(imageRecipe.Sources, wantSources) {
		t.Fatalf("Sources =\n%+v\nwant\n%+v", imageRecipe.Sources, wantSources)
	}
	if problems := Validate(imageRecipe); len(problems) != 0 {
		t.Fatalf("Validate = %q, want no problems", problems)
	}
	docker := imageRecipe.Sources[0]
	if docker.SuiteFor("noble") != "noble" || !reflect.DeepEqual(docker.ComponentsOrDefault(), []string{"stable"}) {
		t.Errorf("docker: suite %q, components %v", docker.SuiteFor("noble"), docker.ComponentsOrDefault())
	}
	deadsnakes := imageRecipe.Sources[1]
	if deadsnakes.SuiteFor("jammy") != "noble" || !reflect.DeepEqual(deadsnakes.ComponentsOrDefault(), []string{"main"}) {
		t.Errorf("deadsnakes: suite %q, components %v", deadsnakes.SuiteFor("jammy"), deadsnakes.ComponentsOrDefault())
	}

	// Round trip through Save keeps the sources.
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := Save(path, imageRecipe); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, imageRecipe) {
		t.Fatalf("round trip =\n%+v\nwant\n%+v", reloaded, imageRecipe)
	}
}

func TestSaveRecipeWithoutSourcesOmitsTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := Save(path, loadValidRecipe(t)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "sources") {
		t.Errorf("a recipe without sources must not mention them:\n%s", content)
	}
}

func TestValidateSources(t *testing.T) {
	good := Source{Name: "docker", URL: "https://download.docker.com/linux/ubuntu", Suite: "noble", Components: []string{"stable"}, Key: "keys/docker.asc"}
	testCases := []struct {
		name        string
		adjust      func(*Source)
		wantMessage string
	}{
		{"uppercase name", func(source *Source) { source.Name = "Docker" }, "source name"},
		{"empty name", func(source *Source) { source.Name = "" }, "source name"},
		{"name with slash", func(source *Source) { source.Name = "ppa/x" }, "source name"},
		{"long name", func(source *Source) { source.Name = strings.Repeat("a", 33) }, "source name"},
		{"ftp url", func(source *Source) { source.URL = "ftp://x/y" }, "source url"},
		{"url without host", func(source *Source) { source.URL = "https:///y" }, "source url"},
		{"url with space", func(source *Source) { source.URL = "https://x/a b" }, "source url"},
		{"url with fragment", func(source *Source) { source.URL = "https://download.docker.com/linux/ubuntu#stable" }, "source url"},
		// Apt comments to the end of the line wherever the # sits, so a
		// trailing one kills the suite and components just as a fragment
		// does. url.Parse reports no Fragment for it, which is why the
		// character itself is what the check looks for.
		{"url with trailing hash", func(source *Source) { source.URL = "https://download.docker.com/linux/ubuntu#" }, "source url"},
		{"url with hash inside the path", func(source *Source) { source.URL = "https://x/a#b" }, "source url"},
		// Apt counts these as whitespace and would take the rest of the URL
		// for the suite. url.Parse refuses them first; these pin the rule
		// whatever net/url does with control characters later.
		{"url with vertical tab", func(source *Source) { source.URL = "https://x/a\vb" }, "source url"},
		{"url with form feed", func(source *Source) { source.URL = "https://x/a\fb" }, "source url"},
		{"url with bracket", func(source *Source) { source.URL = "https://x/a]" }, "source url"},
		{"empty url", func(source *Source) { source.URL = "" }, "source url"},
		// The password would be committed in the recipe, written twice into
		// the lock, and shipped in the image's own sources.list.
		{"url with credentials", func(source *Source) {
			source.URL = "https://buildbot:s3cret@apt.corp.example/ubuntu"
		}, "credentials"},
		{"url with a user and no password", func(source *Source) {
			source.URL = "https://buildbot@apt.corp.example/ubuntu"
		}, "credentials"},
		{"suite with space", func(source *Source) { source.Suite = "noble main" }, "suite"},
		{"suite with slash", func(source *Source) { source.Suite = "./" }, "suite"},
		{"component with space", func(source *Source) { source.Components = []string{"main universe"} }, "component"},
		{"uppercase component", func(source *Source) { source.Components = []string{"Main"} }, "component"},
		{"absolute key", func(source *Source) { source.Key = "/etc/passwd" }, "key path"},
		{"key escaping the directory", func(source *Source) { source.Key = "../keys/x.asc" }, "key path"},
		{"key with dot-dot inside", func(source *Source) { source.Key = "keys/../../x.asc" }, "key path"},
		{"empty key", func(source *Source) { source.Key = "" }, "key path"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			source := good
			source.Components = []string{"stable"}
			testCase.adjust(&source)
			imageRecipe := loadValidRecipe(t)
			imageRecipe.Sources = []Source{source}
			problems := Validate(imageRecipe)
			if !strings.Contains(strings.Join(problems, "\n"), testCase.wantMessage) || !strings.Contains(strings.Join(problems, "\n"), "sources[0]") {
				t.Fatalf("Validate = %q, want a sources[0] problem mentioning %q", problems, testCase.wantMessage)
			}
		})
	}
	imageRecipe := loadValidRecipe(t)
	imageRecipe.Sources = []Source{good, {Name: "docker", URL: "https://other.example/x", Key: "keys/other.asc"}}
	if problems := Validate(imageRecipe); len(problems) != 1 || !strings.Contains(problems[0], "used twice") {
		t.Errorf("duplicate names: Validate = %q", problems)
	}
	// %23 reaches apt still encoded, so the "deb" line stays one line and
	// the suite and components still apply: refusing it would be stricter
	// than the format needs.
	for _, goodURL := range []string{"http://apt.llvm.org/noble/", "https://packages.microsoft.com/repos/code", "https://ppa.launchpadcontent.net/git-core/ppa/ubuntu", "https://x/a%23b"} {
		if err := CheckSourceURL(goodURL); err != nil {
			t.Errorf("CheckSourceURL(%q) = %v", goodURL, err)
		}
	}
	for _, goodKey := range []string{"keys/docker.asc", "docker.gpg", "keys/sub/x.asc"} {
		if err := CheckKeyPath(goodKey); err != nil {
			t.Errorf("CheckKeyPath(%q) = %v", goodKey, err)
		}
	}
}

// A refusal is printed to a terminal, kept in a CI log, and quoted in
// capture's report, which sits beside a recipe people commit: it must name
// the URL without repeating the secret it refuses.
func TestCredentialRefusalsDoNotQuoteTheSecret(t *testing.T) {
	refusals := map[string]error{
		"source url":         CheckSourceURL("https://buildbot:s3cret@apt.corp.example/ubuntu"),
		"source url, token":  CheckSourceURL("https://s3cret@apt.corp.example/ubuntu"),
		"python index_url":   CheckPythonIndexURL("https://buildbot:s3cret@nexus.example/simple"),
		"python index token": CheckPythonIndexURL("https://s3cret@nexus.example/simple"),
	}
	for name, err := range refusals {
		if err == nil {
			t.Errorf("%s: no refusal", name)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("%s: the refusal quotes the secret: %v", name, err)
		}
		if !strings.Contains(err.Error(), "REDACTED@") || !strings.Contains(err.Error(), ".example/") {
			t.Errorf("%s: the refusal should still name the host and path: %v", name, err)
		}
	}
	for _, unchanged := range []string{"https://download.docker.com/linux/ubuntu", "not a url at all", "https://x/a%23b", ""} {
		if got := RedactURLCredentials(unchanged); got != unchanged {
			t.Errorf("RedactURLCredentials(%q) = %q, want it unchanged", unchanged, got)
		}
	}
}

func TestCheckSourceKeys(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir, "keys/docker.asc")
	if err := os.WriteFile(filepath.Join(dir, "html.asc"), []byte("<html>404</html>"), 0o644); err != nil {
		t.Fatal(err)
	}
	sources := []Source{
		{Name: "docker", URL: "https://x", Key: "keys/docker.asc"},
		{Name: "missing", URL: "https://x", Key: "keys/missing.asc"},
		{Name: "html", URL: "https://x", Key: "html.asc"},
		{Name: "escape", URL: "https://x", Key: "../x.asc"}, // Validate's problem, not repeated here
	}
	problems := CheckSourceKeys(dir, sources)
	if len(problems) != 2 {
		t.Fatalf("problems = %q, want the missing file and the HTML page", problems)
	}
	if !strings.Contains(problems[0], "source missing: key file keys/missing.asc") || !strings.Contains(problems[1], "source html: key file html.asc") || !strings.Contains(problems[1], "not an OpenPGP public key") {
		t.Errorf("problems = %q", problems)
	}
	if got := KeyPath(dir, sources[0]); got != filepath.Join(dir, "keys", "docker.asc") {
		t.Errorf("KeyPath = %q", got)
	}
	if problems := CheckSourceKeys(dir, nil); len(problems) != 0 {
		t.Errorf("no sources, no problems; got %q", problems)
	}
}
