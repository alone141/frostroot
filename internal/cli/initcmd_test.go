package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

type scriptedPrompt struct {
	answers   []string
	i         int
	questions []string
}

func (s *scriptedPrompt) Ask(question, defaultValue string) (string, error) {
	s.questions = append(s.questions, question)
	if s.i >= len(s.answers) {
		return defaultValue, nil
	}
	a := s.answers[s.i]
	s.i++
	if a == "" {
		return defaultValue, nil
	}
	return a, nil
}

func runInit(t *testing.T, dir string, answers []string, args ...string) (int, string, string) {
	t.Helper()
	var out, errb bytes.Buffer
	app := App{Stdout: &out, Stderr: &errb, Dir: dir, Prompt: &scriptedPrompt{answers: answers}}
	code := app.Run(append([]string{"init"}, args...))
	return code, out.String(), errb.String()
}

func TestInitWritesValidRecipe(t *testing.T) {
	dir := t.TempDir()
	code, _, errs := runInit(t, dir, []string{"cpp-lab", "22.04", "student", "Europe/Istanbul", "build-essential", "cmake"})
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	r, err := recipe.Load(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Image.Name != "cpp-lab" || r.Image.Release != "22.04" || r.Image.Arch != "amd64" {
		t.Fatalf("image %+v", r.Image)
	}
	if r.Locale.Timezone != "Europe/Istanbul" || r.Locale.Lang != "en_US.UTF-8" {
		t.Fatalf("locale %+v", r.Locale)
	}
	if !contains(r.Packages.Include, "build-essential") || !contains(r.Packages.Include, "cmake") {
		t.Fatalf("include %#v", r.Packages.Include)
	}
	if probs := recipe.Validate(r); len(probs) != 0 {
		t.Fatalf("init wrote a recipe that does not validate: %v", probs)
	}
	// And the validate command agrees.
	var errb bytes.Buffer
	if code := (&App{Stdout: io.Discard, Stderr: &errb, Dir: dir}).Run([]string{"validate"}); code != 0 {
		t.Fatalf("validate: %s", errb.String())
	}
}

func TestInitDefaults(t *testing.T) {
	dir := t.TempDir()
	if code, _, errs := runInit(t, dir, nil); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	r, err := recipe.Load(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := recipe.Recipe{
		Image:    recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{}},
	}
	if !reflect.DeepEqual(r, want) {
		t.Fatalf("defaults:\n got %+v\nwant %+v", r, want)
	}
}

func TestInitAsksInOrder(t *testing.T) {
	p := &scriptedPrompt{}
	app := App{Stdout: io.Discard, Stderr: io.Discard, Dir: t.TempDir(), Prompt: p}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	want := []string{"image name", "release", "user", "timezone", "preset", "packages"}
	if len(p.questions) != len(want) {
		t.Fatalf("questions %q", p.questions)
	}
	for i, w := range want {
		if !strings.Contains(strings.ToLower(p.questions[i]), w) {
			t.Fatalf("question %d %q should be about %s", i, p.questions[i], w)
		}
	}
}

func TestInitPresetsExpandAndExtrasAppend(t *testing.T) {
	dir := t.TempDir()
	code, _, errs := runInit(t, dir, []string{"py", "22.04", "", "", "python-lab", " numpy-dev , ,git,python3 "})
	if code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	r, err := recipe.Load(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"python3", "python3-pip", "python3-venv", "git", "numpy-dev"}
	if !reflect.DeepEqual(r.Packages.Include, want) {
		t.Fatalf("include %#v want %#v", r.Packages.Include, want)
	}
}

func TestInitPythonLabNoteOnlyOnNoble(t *testing.T) {
	_, out, _ := runInit(t, t.TempDir(), []string{"py", "24.04", "", "", "python-lab", ""})
	if !strings.Contains(out, "venv") || !strings.Contains(out, "PEP 668") {
		t.Fatalf("24.04 python-lab should warn about PEP 668: %s", out)
	}
	_, out, _ = runInit(t, t.TempDir(), []string{"py", "22.04", "", "", "python-lab", ""})
	if strings.Contains(out, "PEP 668") {
		t.Fatalf("22.04 has no PEP 668 restriction: %s", out)
	}
}

func TestInitRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frostroot.toml")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	p := &scriptedPrompt{}
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: dir, Prompt: p}
	if code := app.Run([]string{"init"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "--force") {
		t.Fatalf("stderr should mention --force: %s", errb.String())
	}
	if len(p.questions) != 0 {
		t.Fatal("must refuse before asking anything")
	}
	if body, _ := os.ReadFile(path); string(body) != "original" {
		t.Fatal("existing recipe must not be touched")
	}
}

func TestInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frostroot.toml")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, _, errs := runInit(t, dir, nil, "--force"); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	if _, err := recipe.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestInitRejectsBadAnswers(t *testing.T) {
	for name, answers := range map[string][]string{
		"release":  {"cpp-lab", "18.04", "student", "UTC", "none", ""},
		"user":     {"cpp-lab", "24.04", "root", "UTC", "none", ""},
		"timezone": {"cpp-lab", "24.04", "student", "../etc/passwd", "none", ""},
		"package":  {"cpp-lab", "24.04", "student", "UTC", "none", "git; rm -rf /"},
		"preset":   {"cpp-lab", "24.04", "student", "UTC", "gaming", ""},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			code, _, errs := runInit(t, dir, answers)
			if code != 1 {
				t.Fatalf("code %d", code)
			}
			if errs == "" {
				t.Fatal("expected an explanation on stderr")
			}
			entries, _ := os.ReadDir(dir)
			if len(entries) != 0 {
				t.Fatalf("must not write anything, found %v", entries)
			}
		})
	}
}

func TestInitWritesComments(t *testing.T) {
	dir := t.TempDir()
	if code, _, errs := runInit(t, dir, nil); code != 0 {
		t.Fatalf("code %d: %s", code, errs)
	}
	body, err := os.ReadFile(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, want := range []string{"#", "frostroot.lock", "20.04 | 22.04 | 24.04"} {
		if !strings.Contains(text, want) {
			t.Fatalf("recipe is hand-edited; missing %q in:\n%s", want, text)
		}
	}
}

func TestInitWithLinePrompt(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	app := App{
		Stdin:  strings.NewReader("cpp-lab\n22.04\n\n\nbuild-essential\n"), // then EOF: accept the rest
		Stdout: &out, Stderr: io.Discard, Dir: dir,
	}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("code %d: %s", code, out.String())
	}
	r, err := recipe.Load(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Image.Name != "cpp-lab" || r.Image.Release != "22.04" || r.User.Name != "student" || len(r.Packages.Include) != 4 {
		t.Fatalf("got %+v", r)
	}
	if !strings.Contains(out.String(), "[24.04]") {
		t.Fatalf("prompts should show defaults: %s", out.String())
	}
}

func contains(xs []string, w string) bool {
	for _, x := range xs {
		if x == w {
			return true
		}
	}
	return false
}
