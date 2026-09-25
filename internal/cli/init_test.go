package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// scriptedPrompt answers questions from a list, recording each question. An
// empty or missing answer accepts the default, like pressing Enter.
type scriptedPrompt struct {
	answers         []string
	questionsAsked  []string
	defaultsOffered []string // the default shown with each question
}

func (p *scriptedPrompt) Ask(question, defaultAnswer string) (string, error) {
	answerIndex := len(p.questionsAsked)
	p.questionsAsked = append(p.questionsAsked, question)
	p.defaultsOffered = append(p.defaultsOffered, defaultAnswer)
	if answerIndex >= len(p.answers) || p.answers[answerIndex] == "" {
		return defaultAnswer, nil
	}
	return p.answers[answerIndex], nil
}

// The plain init asks these questions, in this order. Tests script answers
// by position; "" accepts the default.
const (
	answerImageName = iota
	answerRelease
	answerUserName
	answerSudo
	answerTimezone
	answerLocale
	answerSystemd
	answerSources
	answerPPAs
	answerPackages
	answerOtherPackages
	answerPythonPackages
	answerCertificates
	answerWrite
	answerCount
)

// answersWith returns a default-accepting answer list with the given
// positions overridden.
func answersWith(overrides map[int]string) []string {
	answers := make([]string, answerCount)
	for position, answer := range overrides {
		answers[position] = answer
	}
	return answers
}

// noHostFile stands in for os.ReadFile on a host with no timezone
// configuration, so defaults do not depend on the machine running the tests.
func noHostFile(string) ([]byte, error) { return nil, os.ErrNotExist }

// runInitWithAnswers runs `frostroot init` in recipeDir, answering its
// questions from answers, and returns the exit code, standard output and
// standard error. Without a terminal, init uses the plain interface.
func runInitWithAnswers(recipeDir string, answers []string, args ...string) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: &scriptedPrompt{answers: answers}, ReadFile: noHostFile}
	exitCode = app.Run(append([]string{"init"}, args...))
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

// loadWrittenRecipe loads the frostroot.toml in recipeDir.
func loadWrittenRecipe(t *testing.T, recipeDir string) recipe.Recipe {
	t.Helper()
	imageRecipe, err := recipe.Load(filepath.Join(recipeDir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return imageRecipe
}

func TestInitWritesValidRecipe(t *testing.T) {
	recipeDir := t.TempDir()
	answers := answersWith(map[int]string{
		answerImageName: "cpp-lab", answerRelease: "22.04", answerTimezone: "Europe/Istanbul",
		answerPackages: "build-essential cmake",
	})
	if exitCode, _, stderr := runInitWithAnswers(recipeDir, answers); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if wantImage := (recipe.Image{Name: "cpp-lab", Release: "22.04", Arch: "amd64"}); imageRecipe.Image != wantImage {
		t.Errorf("image = %+v, want %+v", imageRecipe.Image, wantImage)
	}
	if wantLocale := (recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Europe/Istanbul"}); imageRecipe.Locale != wantLocale {
		t.Errorf("locale = %+v, want %+v", imageRecipe.Locale, wantLocale)
	}
	if want := []string{"build-essential", "cmake"}; !slices.Equal(imageRecipe.Packages.Include, want) {
		t.Errorf("packages = %q, want %q", imageRecipe.Packages.Include, want)
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("init wrote a recipe that does not validate: %v", problems)
	}
	// And the validate command agrees.
	if exitCode, _, stderr := runValidateIn(recipeDir); exitCode != exitSuccess {
		t.Errorf("validate: exit code = %d, stderr %s", exitCode, stderr)
	}
}

func TestInitDefaults(t *testing.T) {
	recipeDir := t.TempDir()
	if exitCode, _, stderr := runInitWithAnswers(recipeDir, nil); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	want := recipe.Recipe{
		Image:    recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{}},
	}
	if got := loadWrittenRecipe(t, recipeDir); !reflect.DeepEqual(got, want) {
		t.Errorf("defaults:\n got %+v\nwant %+v", got, want)
	}
}

func TestInitAsksInOrder(t *testing.T) {
	prompt := &scriptedPrompt{}
	app := App{Stdout: io.Discard, Stderr: io.Discard, RecipeDir: t.TempDir(), Prompt: prompt, ReadFile: noHostFile}
	if exitCode := app.Run([]string{"init"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d", exitCode)
	}
	wantTopics := []string{"image name", "release", "user name", "sudo", "timezone", "locale", "systemd", "apt sources", "other ppas", "packages", "other packages", "python packages", "certificate authorities", "write frostroot.toml"}
	if len(prompt.questionsAsked) != len(wantTopics) {
		t.Fatalf("questions asked = %q, want one about each of %q", prompt.questionsAsked, wantTopics)
	}
	for i, wantTopic := range wantTopics {
		if !strings.Contains(strings.ToLower(prompt.questionsAsked[i]), wantTopic) {
			t.Errorf("question %d %q should be about %s", i, prompt.questionsAsked[i], wantTopic)
		}
	}
}

func TestInitListsOptionsForSelects(t *testing.T) {
	_, stdout, _ := runInitWithAnswers(t.TempDir(), nil)
	for _, wantText := range []string{"Ubuntu 24.04 LTS (noble)", "Ubuntu 20.04 LTS (focal)  (past standard support", "C/C++", "build-essential", "choices, for example UTC", "English (United States)"} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("plain init should list %q among the options:\n%s", wantText, stdout)
		}
	}
	if strings.Contains(stdout, "Africa/Abidjan") {
		t.Errorf("plain init must not dump the whole timezone list:\n%s", stdout[:min(2000, len(stdout))])
	}
	if !strings.Contains(stdout, "Image     lab, Ubuntu 24.04 amd64") {
		t.Errorf("plain init should show the summary before writing:\n%s", stdout)
	}
}

func TestInitPackagesByNumberOrName(t *testing.T) {
	// Catalog positions: 1 build-essential, 2 cmake, 3 gdb. Names and
	// numbers may be mixed, separated by commas, spaces or both.
	for _, packagesAnswer := range []string{"1, 3 git", "build-essential gdb 13", "1,gdb,git"} {
		t.Run(packagesAnswer, func(t *testing.T) {
			recipeDir := t.TempDir()
			answers := answersWith(map[int]string{answerPackages: packagesAnswer, answerOtherPackages: " numpy-dev , libfoo-dev"})
			if exitCode, _, stderr := runInitWithAnswers(recipeDir, answers); exitCode != exitSuccess {
				t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
			}
			want := []string{"build-essential", "gdb", "git", "numpy-dev", "libfoo-dev"}
			if got := loadWrittenRecipe(t, recipeDir).Packages.Include; !slices.Equal(got, want) {
				t.Errorf("packages = %q, want %q", got, want)
			}
		})
	}
}

func TestInitPythonNoteOnlyOnNoble(t *testing.T) {
	_, stdout, _ := runInitWithAnswers(t.TempDir(), answersWith(map[int]string{answerRelease: "24.04", answerPackages: "python3-pip"}))
	if !strings.Contains(stdout, "venv") || !strings.Contains(stdout, "PEP 668") {
		t.Errorf("24.04 with pip should warn about PEP 668:\n%s", stdout)
	}
	_, stdout, _ = runInitWithAnswers(t.TempDir(), answersWith(map[int]string{answerRelease: "22.04", answerPackages: "python3-pip"}))
	if strings.Contains(stdout, "PEP 668") {
		t.Errorf("22.04 has no PEP 668 restriction:\n%s", stdout)
	}
}

func TestInitRefusesToOverwrite(t *testing.T) {
	recipeDir := t.TempDir()
	recipePath := filepath.Join(recipeDir, "frostroot.toml")
	if err := os.WriteFile(recipePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := &scriptedPrompt{}
	var stderr bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &stderr, RecipeDir: recipeDir, Prompt: prompt, ReadFile: noHostFile}
	if exitCode := app.Run([]string{"init"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	for _, wantText := range []string{"--force", "frostroot edit"} {
		if !strings.Contains(stderr.String(), wantText) {
			t.Errorf("stderr should mention %s:\n%s", wantText, stderr.String())
		}
	}
	if len(prompt.questionsAsked) != 0 {
		t.Error("init must refuse before asking anything")
	}
	recipeText, err := os.ReadFile(recipePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(recipeText) != "original" {
		t.Error("the existing recipe must not be touched")
	}
}

func TestInitForceOverwrites(t *testing.T) {
	recipeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipeDir, "frostroot.toml"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	if exitCode, _, stderr := runInitWithAnswers(recipeDir, nil, "--force"); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	loadWrittenRecipe(t, recipeDir)
}

func TestInitRejectsInvalidAnswers(t *testing.T) {
	testCases := map[string]struct {
		answers      []string
		wantInStderr string
	}{
		"release":       {answersWith(map[int]string{answerRelease: "18.04"}), "unknown ubuntu release"},
		"user":          {answersWith(map[int]string{answerUserName: "root"}), "invalid user name"},
		"sudo":          {answersWith(map[int]string{answerSudo: "maybe"}), "answer y or n"},
		"timezone":      {answersWith(map[int]string{answerTimezone: "../etc/passwd"}), "unknown timezone"},
		"package":       {answersWith(map[int]string{answerOtherPackages: "git; rm -rf /"}), "invalid package name"},
		"catalog":       {answersWith(map[int]string{answerPackages: "gaming"}), "not one of the options"},
		"option number": {answersWith(map[int]string{answerLocale: "99"}), "unknown locale"},
		"declined":      {answersWith(map[int]string{answerWrite: "n"}), "nothing written"},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			recipeDir := t.TempDir()
			exitCode, _, stderr := runInitWithAnswers(recipeDir, testCase.answers)
			if exitCode != exitUserError {
				t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
			}
			if !strings.Contains(stderr, testCase.wantInStderr) || !strings.Contains(stderr, "nothing written") {
				t.Errorf("stderr should explain with %q and say nothing was written:\n%s", testCase.wantInStderr, stderr)
			}
			entries, err := os.ReadDir(recipeDir)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Errorf("init must not write anything, found %v", entries)
			}
		})
	}
}

func TestInitWritesComments(t *testing.T) {
	recipeDir := t.TempDir()
	if exitCode, _, stderr := runInitWithAnswers(recipeDir, nil); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	recipeText, err := os.ReadFile(filepath.Join(recipeDir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	// The recipe is edited by hand, so it explains itself.
	for _, wantText := range []string{"#", "frostroot.lock", "20.04 | 22.04 | 24.04"} {
		if !strings.Contains(string(recipeText), wantText) {
			t.Errorf("recipe lacks %q:\n%s", wantText, recipeText)
		}
	}
}

func TestInitWithLinePrompt(t *testing.T) {
	recipeDir := t.TempDir()
	var stdout bytes.Buffer
	app := App{
		// image name, release, then defaults up to packages — the two
		// source questions among them — one package by number, then end of
		// input: accept the rest.
		Stdin:     strings.NewReader("cpp-lab\n22.04\n\n\n\n\n\n\n\n1\n"),
		Stdout:    &stdout,
		Stderr:    io.Discard,
		RecipeDir: recipeDir,
		ReadFile:  noHostFile,
	}
	if exitCode := app.Run([]string{"init"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stdout %s", exitCode, stdout.String())
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if imageRecipe.Image.Name != "cpp-lab" || imageRecipe.Image.Release != "22.04" ||
		imageRecipe.User.Name != "student" || !slices.Equal(imageRecipe.Packages.Include, []string{"build-essential"}) {
		t.Errorf("recipe = %+v", imageRecipe)
	}
	if !strings.Contains(stdout.String(), "[24.04]") {
		t.Errorf("prompts should show defaults:\n%s", stdout.String())
	}
}

func TestInitPlainFlagIsAccepted(t *testing.T) {
	if exitCode, _, stderr := runInitWithAnswers(t.TempDir(), nil, "--plain"); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
}

func TestTOMLQuote(t *testing.T) {
	testCases := []struct {
		value string
		want  string
	}{
		{value: "plain", want: `"plain"`},
		{value: `say "hi"\now`, want: `"say \"hi\"\\now"`},
		{value: "tab\there", want: `"tab\u0009here"`},
	}
	for _, testCase := range testCases {
		if got := tomlQuote(testCase.value); got != testCase.want {
			t.Errorf("tomlQuote(%q) = %s, want %s", testCase.value, got, testCase.want)
		}
	}
}

// TestRenderRecipeKeepsThePythonIndexURL: the form regenerates the whole
// file from this template, so a field the template does not write is lost
// the first time somebody runs frostroot edit, whatever the form carried.
func TestRenderRecipeKeepsThePythonIndexURL(t *testing.T) {
	const index = "https://nexus.example.com/repository/pypi/simple"
	imageRecipe := recipe.Recipe{
		Image:  recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:   recipe.User{Name: "student"},
		Python: &recipe.Python{Include: []string{"requests"}, IndexURL: index},
	}
	rendered, err := renderRecipe(imageRecipe)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(rendered, "index_url = \""+index+"\"") {
		t.Fatalf("the rendered recipe lost index_url:\n%s", rendered)
	}
	// And it survives being read back, which is what edit does next.
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := os.WriteFile(path, []byte(rendered), 0o644); err != nil {
		t.Fatal(err)
	}
	reloaded, err := recipe.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := reloaded.PythonIndexURL(); got != index {
		t.Errorf("index_url after a round trip = %q, want %q", got, index)
	}
	// A recipe with no index writes no line, so nothing changes for anyone else.
	plain := imageRecipe
	plain.Python = &recipe.Python{Include: []string{"requests"}}
	renderedPlain, err := renderRecipe(plain)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(renderedPlain, "index_url") {
		t.Errorf("a recipe naming no index must not mention one:\n%s", renderedPlain)
	}
}

// interferingPrompt answers like scriptedPrompt and, before one question,
// does what another program might while the form is open.
type interferingPrompt struct {
	scriptedPrompt
	beforeQuestion string // a substring of the question to act before
	act            func()
}

func (p *interferingPrompt) Ask(question, defaultAnswer string) (string, error) {
	if strings.Contains(question, p.beforeQuestion) {
		p.act()
	}
	return p.scriptedPrompt.Ask(question, defaultAnswer)
}

// TestInitLeavesARecipeThatAppearedWhileTheFormWasOpen: the check for an
// existing recipe is made before the form opens, and the form takes as long
// as a person takes. A recipe that appeared meanwhile, from a checkout, an
// editor or a second frostroot, was replaced without a word, exit 0.
func TestInitLeavesARecipeThatAppearedWhileTheFormWasOpen(t *testing.T) {
	recipeDir := t.TempDir()
	recipePath := filepath.Join(recipeDir, "frostroot.toml")
	prompt := &interferingPrompt{beforeQuestion: "Write frostroot.toml", act: func() {
		if err := os.WriteFile(recipePath, []byte("theirs\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}}
	var stdout, stderr bytes.Buffer
	app := App{Stdout: &stdout, Stderr: &stderr, RecipeDir: recipeDir, Prompt: prompt, ReadFile: noHostFile}
	if exitCode := app.Run([]string{"init"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--force") {
		t.Errorf("stderr should say how to overwrite on purpose:\n%s", stderr.String())
	}
	if content, err := os.ReadFile(recipePath); err != nil || string(content) != "theirs\n" {
		t.Errorf("the recipe that appeared was touched: %q, %v", content, err)
	}
	leftovers, err := filepath.Glob(filepath.Join(recipeDir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temporary files left behind: %q", leftovers)
	}
}
