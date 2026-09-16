package cli

import (
	"bytes"
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

func TestEditPreselectsCurrentValues(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	_, _, _, prompt := runEditWithAnswers(recipeDir, nil)
	// Each question's default is the recipe's own value.
	wantDefaults := map[int]string{
		answerImageName: "cpp-lab", answerRelease: "22.04", answerUserName: "student", answerSudo: "y",
		answerTimezone: "UTC", answerLocale: "en_US.UTF-8", answerSystemd: "y",
		answerPackages: "git build-essential cmake", answerOtherPackages: "",
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
