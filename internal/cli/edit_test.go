package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// runEditWithAnswers runs `frostroot edit` in recipeDir with scripted
// answers; "" keeps the current value.
func runEditWithAnswers(recipeDir string, answers []string, args ...string) (exitCode int, stdout, stderr string, prompt *scriptedPrompt) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	prompt = &scriptedPrompt{answers: answers}
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: prompt, ReadFile: noHostFile}
	exitCode = app.Run(append([]string{"edit"}, args...))
	return exitCode, stdoutBuffer.String(), stderrBuffer.String(), prompt
}

func TestEditKeepsAnUnchangedRecipe(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	original := loadWrittenRecipe(t, recipeDir)
	exitCode, _, stderr, prompt := runEditWithAnswers(recipeDir, nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	if len(prompt.questionsAsked) != answerCount {
		t.Errorf("edit asked %d questions, want the same %d as init", len(prompt.questionsAsked), answerCount)
	}
	want := original
	want.WSL.DefaultUser = recipe.DefaultUser(original)
	if got := loadWrittenRecipe(t, recipeDir); !reflect.DeepEqual(got, want) {
		t.Errorf("edit changed a recipe nobody touched:\n got %+v\nwant %+v", got, want)
	}
}

// TestEditKeepsTheRecipeFamily: the form asks for no family yet, so edit
// carries [image] distro through, and a recipe that names none gains none.
func TestEditKeepsTheRecipeFamily(t *testing.T) {
	for fixture, wantDistroLine := range map[string]bool{"distro.toml": true, "valid.toml": false} {
		t.Run(fixture, func(t *testing.T) {
			recipeDir := newRecipeDir(t, fixture)
			if exitCode, _, stderr, _ := runEditWithAnswers(recipeDir, nil); exitCode != exitSuccess {
				t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
			}
			content, err := os.ReadFile(filepath.Join(recipeDir, "frostroot.toml"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Contains(string(content), "\ndistro = \"ubuntu\"\n"); got != wantDistroLine {
				t.Errorf("distro line written = %v, want %v:\n%s", got, wantDistroLine, content)
			}
		})
	}
}

// TestEditKeepsAnUnchangedFedoraRecipe: edit opens a Fedora recipe with
// Fedora's questions, which are Ubuntu's without the sources, Python and
// certificates, and writes back what it read.
func TestEditKeepsAnUnchangedFedoraRecipe(t *testing.T) {
	recipeDir := newRecipeDir(t, "fedora.toml")
	original := loadWrittenRecipe(t, recipeDir)
	exitCode, stdout, stderr, prompt := runEditWithAnswers(recipeDir, nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	want := []string{"Image name", "Fedora release (number or value)", "User name", "Passwordless sudo (y/n)", "Timezone", "Locale (number or value)", "Boot with systemd (y/n)", "Packages (numbers or names, separated by spaces or commas)", "Other packages", "Write frostroot.toml? (y/n)"}
	if !slices.Equal(prompt.questionsAsked, want) {
		t.Errorf("questions =\n%q\nwant\n%q", prompt.questionsAsked, want)
	}
	// The recipe's own values are the defaults: its release, and its
	// packages split into the catalog's and the rest.
	if got := prompt.defaultsOffered[1]; got != "44" {
		t.Errorf("release default = %q", got)
	}
	if got := prompt.defaultsOffered[7] + " | " + prompt.defaultsOffered[8]; got != "git gcc | NetworkManager-tui perl-File-Temp" {
		t.Errorf("packages defaults = %q", got)
	}
	if !strings.Contains(stdout, "Image     fedora-lab, Fedora 44 amd64") || strings.Contains(stdout, "Sources ") {
		t.Errorf("the summary does not describe a Fedora recipe:\n%s", stdout)
	}
	if got := loadWrittenRecipe(t, recipeDir); !reflect.DeepEqual(got, original) {
		t.Errorf("edit changed a recipe nobody touched:\n got %+v\nwant %+v", got, original)
	}
}

func TestEditPreselectsCurrentValues(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	_, _, _, prompt := runEditWithAnswers(recipeDir, nil)
	// Each question's default is the recipe's own value.
	wantDefaults := map[int]string{
		answerImageName: "cpp-lab", answerRelease: "22.04", answerUserName: "student", answerSudo: "y",
		answerTimezone: "UTC", answerLocale: "en_US.UTF-8", answerSystemd: "y",
		answerPackages: "git build-essential cmake", answerOtherPackages: "", answerSources: "", answerPPAs: "",
	}
	for position, wantDefault := range wantDefaults {
		if position >= len(prompt.defaultsOffered) {
			t.Fatalf("only %d questions were asked", len(prompt.defaultsOffered))
		}
		if got := prompt.defaultsOffered[position]; got != wantDefault {
			t.Errorf("question %d (%s) offered default %q, want %q", position, prompt.questionsAsked[position], got, wantDefault)
		}
	}
}

func TestEditChangesOnlyWhatWasAnswered(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	answers := answersWith(map[int]string{answerRelease: "24.04", answerOtherPackages: "ninja-build"})
	if exitCode, _, stderr, _ := runEditWithAnswers(recipeDir, answers); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	got := loadWrittenRecipe(t, recipeDir)
	if got.Image.Release != "24.04" || got.Image.Name != "cpp-lab" || got.Locale.Timezone != "UTC" {
		t.Errorf("recipe = %+v, want only the release changed", got)
	}
	if want := []string{"git", "build-essential", "cmake", "ninja-build"}; !slices.Equal(got.Packages.Include, want) {
		t.Errorf("packages = %q, want the original order plus the new one", got.Packages.Include)
	}
}

func TestEditWithoutRecipe(t *testing.T) {
	exitCode, _, stderr, prompt := runEditWithAnswers(t.TempDir(), nil)
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if !strings.Contains(stderr, "frostroot init") {
		t.Errorf("stderr should suggest init:\n%s", stderr)
	}
	if len(prompt.questionsAsked) != 0 {
		t.Error("nothing to edit, so nothing to ask")
	}
}

func TestEditRefusesAnInvalidRecipe(t *testing.T) {
	exitCode, _, stderr, prompt := runEditWithAnswers(newRecipeDir(t, "bad-user.toml"), nil)
	if exitCode != exitUserError || !strings.Contains(stderr, "user name") || len(prompt.questionsAsked) != 0 {
		t.Errorf("edit must report the problems and ask nothing: exit %d, %d questions, stderr %s", exitCode, len(prompt.questionsAsked), stderr)
	}
}

func TestEditRejectsArguments(t *testing.T) {
	if exitCode, _, _, _ := runEditWithAnswers(newRecipeDir(t, "valid.toml"), nil, "other.toml"); exitCode != exitUserError {
		t.Errorf("exit code = %d, want %d", exitCode, exitUserError)
	}
}
