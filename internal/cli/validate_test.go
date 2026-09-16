package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runValidateIn runs `frostroot validate` in recipeDir and returns the exit
// code, standard output and standard error.
func runValidateIn(recipeDir string, args ...string) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir}
	exitCode = app.Run(append([]string{"validate"}, args...))
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

func TestValidateValidRecipe(t *testing.T) {
	exitCode, stdout, stderr := runValidateIn(newRecipeDir(t, "valid.toml"))
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %q", exitCode, stderr)
	}
	if want := "frostroot.toml: ok (cpp-lab, Ubuntu 22.04 amd64, 3 packages requested)\n"; stdout != want {
		t.Errorf("stdout = %q, want %q", stdout, want)
	}
	if stderr != "" {
		t.Errorf("stderr = %q, want nothing", stderr)
	}
}

func TestValidateReportsProblems(t *testing.T) {
	testCases := []struct {
		fixtureName  string
		wantInStderr string
	}{
		{fixtureName: "bad-user.toml", wantInStderr: "user name"},
		{fixtureName: "bad-release.toml", wantInStderr: "18.04"},
		{fixtureName: "bad-locale.toml", wantInStderr: "locale lang"},
		{fixtureName: "unknown-field.toml", wantInStderr: "[package]"}, // points at the typo
	}
	for _, testCase := range testCases {
		t.Run(testCase.fixtureName, func(t *testing.T) {
			exitCode, _, stderr := runValidateIn(newRecipeDir(t, testCase.fixtureName))
			if exitCode != exitUserError {
				t.Errorf("exit code = %d, want %d", exitCode, exitUserError)
			}
			if !strings.Contains(stderr, testCase.wantInStderr) {
				t.Errorf("stderr should mention %q: %q", testCase.wantInStderr, stderr)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	recipeDir := t.TempDir()
	recipeText := "[image]\nname = \"../x\"\nrelease = \"18.04\"\narch = \"amd64\"\n\n[user]\nname = \"root\"\n\n[packages]\ninclude = [\"ok\", \"no way\"]\n"
	if err := os.WriteFile(filepath.Join(recipeDir, "frostroot.toml"), []byte(recipeText), 0o644); err != nil {
		t.Fatal(err)
	}
	exitCode, _, stderr := runValidateIn(recipeDir)
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	for _, wantProblem := range []string{"image name", "unknown ubuntu release", "user name", `"no way"`} {
		if !strings.Contains(stderr, wantProblem) {
			t.Errorf("stderr lacks %q:\n%s", wantProblem, stderr)
		}
	}
}

func TestValidateWithoutRecipe(t *testing.T) {
	exitCode, _, stderr := runValidateIn(t.TempDir())
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	for _, wantText := range []string{"frostroot.toml", "frostroot init"} {
		if !strings.Contains(stderr, wantText) {
			t.Errorf("stderr should mention %q: %q", wantText, stderr)
		}
	}
}

func TestValidateRejectsArguments(t *testing.T) {
	if exitCode, _, _ := runValidateIn(newRecipeDir(t, "valid.toml"), "other.toml"); exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
}

func TestPackageCount(t *testing.T) {
	testCases := map[int]string{0: "0 packages", 1: "1 package", 368: "368 packages"}
	for count, want := range testCases {
		if got := packageCount(count); got != want {
			t.Errorf("packageCount(%d) = %q, want %q", count, got, want)
		}
	}
}
