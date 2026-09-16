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
	answers        []string
	questionsAsked []string
}

func (p *scriptedPrompt) Ask(question, defaultAnswer string) (string, error) {
	answerIndex := len(p.questionsAsked)
	p.questionsAsked = append(p.questionsAsked, question)
	if answerIndex >= len(p.answers) || p.answers[answerIndex] == "" {
		return defaultAnswer, nil
	}
	return p.answers[answerIndex], nil
}

// runInitWithAnswers runs `frostroot init` in recipeDir, answering its
// questions from answers, and returns the exit code, standard output and
// standard error.
func runInitWithAnswers(recipeDir string, answers []string, args ...string) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: &scriptedPrompt{answers: answers}}
	exitCode = app.Run(append([]string{"init"}, args...))
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

// loadWrittenRecipe loads the frostroot.toml init wrote in recipeDir.
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
	answers := []string{"cpp-lab", "22.04", "student", "Europe/Istanbul", "build-essential", "cmake"}
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
	for _, wantPackage := range []string{"build-essential", "cmake"} {
		if !slices.Contains(imageRecipe.Packages.Include, wantPackage) {
			t.Errorf("packages %#v lack %q", imageRecipe.Packages.Include, wantPackage)
		}
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
	app := App{Stdout: io.Discard, Stderr: io.Discard, RecipeDir: t.TempDir(), Prompt: prompt}
	if exitCode := app.Run([]string{"init"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d", exitCode)
	}
	wantTopics := []string{"image name", "release", "user", "timezone", "preset", "packages"}
	if len(prompt.questionsAsked) != len(wantTopics) {
		t.Fatalf("questions asked = %q, want one about each of %q", prompt.questionsAsked, wantTopics)
	}
	for i, wantTopic := range wantTopics {
		if !strings.Contains(strings.ToLower(prompt.questionsAsked[i]), wantTopic) {
			t.Errorf("question %d %q should be about %s", i, prompt.questionsAsked[i], wantTopic)
		}
	}
}

func TestInitPresetsExpandAndExtrasAppend(t *testing.T) {
	// Extras may be separated by commas, spaces or both: people type them the
	// way they would for apt install.
	extraPackageAnswers := []string{" numpy-dev , ,git,python3 ", "numpy-dev git python3", "numpy-dev, git  python3"}
	for _, extraPackages := range extraPackageAnswers {
		t.Run(extraPackages, func(t *testing.T) {
			recipeDir := t.TempDir()
			answers := []string{"py", "22.04", "", "", "python-lab", extraPackages}
			if exitCode, _, stderr := runInitWithAnswers(recipeDir, answers); exitCode != exitSuccess {
				t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
			}
			want := []string{"python3", "python3-pip", "python3-venv", "git", "numpy-dev"}
			if got := loadWrittenRecipe(t, recipeDir).Packages.Include; !reflect.DeepEqual(got, want) {
				t.Errorf("packages = %#v, want %#v", got, want)
			}
		})
	}
}

func TestInitPythonLabNoteOnlyOnNoble(t *testing.T) {
	_, stdout, _ := runInitWithAnswers(t.TempDir(), []string{"py", "24.04", "", "", "python-lab", ""})
	if !strings.Contains(stdout, "venv") || !strings.Contains(stdout, "PEP 668") {
		t.Errorf("24.04 python-lab should warn about PEP 668:\n%s", stdout)
	}
	_, stdout, _ = runInitWithAnswers(t.TempDir(), []string{"py", "22.04", "", "", "python-lab", ""})
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
	app := App{Stdout: io.Discard, Stderr: &stderr, RecipeDir: recipeDir, Prompt: prompt}
	if exitCode := app.Run([]string{"init"}); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if !strings.Contains(stderr.String(), "--force") {
		t.Errorf("stderr should mention --force:\n%s", stderr.String())
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
	answersByInvalidField := map[string][]string{
		"release":  {"cpp-lab", "18.04", "student", "UTC", "none", ""},
		"user":     {"cpp-lab", "24.04", "root", "UTC", "none", ""},
		"timezone": {"cpp-lab", "24.04", "student", "../etc/passwd", "none", ""},
		"package":  {"cpp-lab", "24.04", "student", "UTC", "none", "git; rm -rf /"},
		"preset":   {"cpp-lab", "24.04", "student", "UTC", "gaming", ""},
	}
	for invalidField, answers := range answersByInvalidField {
		t.Run(invalidField, func(t *testing.T) {
			recipeDir := t.TempDir()
			exitCode, _, stderr := runInitWithAnswers(recipeDir, answers)
			if exitCode != exitUserError {
				t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
			}
			if stderr == "" {
				t.Error("expected an explanation on stderr")
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
		Stdin:     strings.NewReader("cpp-lab\n22.04\n\n\nbuild-essential\n"), // then end of input: accept the rest
		Stdout:    &stdout,
		Stderr:    io.Discard,
		RecipeDir: recipeDir,
	}
	if exitCode := app.Run([]string{"init"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stdout %s", exitCode, stdout.String())
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if imageRecipe.Image.Name != "cpp-lab" || imageRecipe.Image.Release != "22.04" ||
		imageRecipe.User.Name != "student" || len(imageRecipe.Packages.Include) != 4 {
		t.Errorf("recipe = %+v", imageRecipe)
	}
	if !strings.Contains(stdout.String(), "[24.04]") {
		t.Errorf("prompts should show defaults:\n%s", stdout.String())
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
