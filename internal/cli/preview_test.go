package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// runEditWithPrompt runs `frostroot edit` in recipeDir with a scripted
// prompt the caller can inspect afterwards.
func runEditWithPrompt(recipeDir string, prompt *scriptedPrompt) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: prompt, ReadFile: noHostFile}
	exitCode = app.Run([]string{"edit"})
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

func TestInitShowsTheRecipeBeforeAskingToWriteIt(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, stdout, stderr := runInitWithAnswers(recipeDir, nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	// The preview is the file, byte for byte, shown before the question.
	written, err := os.ReadFile(filepath.Join(recipeDir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "This is what frostroot.toml will say:") {
		t.Errorf("stdout lacks the heading:\n%s", stdout)
	}
	if !strings.Contains(stdout, strings.TrimRight(string(written), "\n")) {
		t.Errorf("stdout does not show the recipe that was written:\n%s", stdout)
	}
	// The scripted prompt asks without printing, so the order that can be
	// checked is heading, then recipe.
	if strings.Index(stdout, "This is what frostroot.toml will say:") > strings.Index(stdout, "[image]") {
		t.Error("the heading comes after the recipe, not before it")
	}
}

func TestEditShowsWhatChangesAsADiff(t *testing.T) {
	recipeDir := t.TempDir()
	if exitCode, _, stderr := runInitWithAnswers(recipeDir, nil); exitCode != exitSuccess {
		t.Fatalf("init: exit code = %d, stderr %s", exitCode, stderr)
	}
	// Rename the image; everything else keeps its default.
	prompt := &scriptedPrompt{answers: []string{"cpp-lab"}}
	exitCode, stdout, stderr := runEditWithPrompt(recipeDir, prompt)
	if exitCode != exitSuccess {
		t.Fatalf("edit: exit code = %d, stderr %s", exitCode, stderr)
	}
	for _, wantText := range []string{
		"2 lines change:",
		`- name = "lab"`,
		`+ name = "cpp-lab"`,
		`  release = "24.04"`,
	} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout)
		}
	}
	if strings.Contains(stdout, "own comments") {
		t.Errorf("no comment of the user's own was in the file, yet the heading says so:\n%s", stdout)
	}
	if got := loadWrittenRecipe(t, recipeDir).Image.Name; got != "cpp-lab" {
		t.Errorf("image name written = %q, want cpp-lab", got)
	}
}

func TestEditSaysWhenTheFilesOwnCommentsGo(t *testing.T) {
	recipeDir := t.TempDir()
	if exitCode, _, stderr := runInitWithAnswers(recipeDir, nil); exitCode != exitSuccess {
		t.Fatalf("init: exit code = %d, stderr %s", exitCode, stderr)
	}
	recipePath := filepath.Join(recipeDir, "frostroot.toml")
	current, err := os.ReadFile(recipePath)
	if err != nil {
		t.Fatal(err)
	}
	withNote := strings.Replace(string(current), "[packages]\n", "# keep gdb: the students debug with it\n[packages]\n", 1)
	if err := os.WriteFile(recipePath, []byte(withNote), 0o644); err != nil {
		t.Fatal(err)
	}
	prompt := &scriptedPrompt{answers: []string{"cpp-lab"}}
	exitCode, stdout, stderr := runEditWithPrompt(recipeDir, prompt)
	if exitCode != exitSuccess {
		t.Fatalf("edit: exit code = %d, stderr %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "your own comments in the file are replaced by the template's") {
		t.Errorf("the heading does not warn about the comment:\n%s", stdout)
	}
	if !strings.Contains(stdout, "- # keep gdb: the students debug with it") {
		t.Errorf("the diff does not show the comment going:\n%s", stdout)
	}
}

func TestEditThatChangesNothingDefaultsToNotWriting(t *testing.T) {
	recipeDir := t.TempDir()
	if exitCode, _, stderr := runInitWithAnswers(recipeDir, nil); exitCode != exitSuccess {
		t.Fatalf("init: exit code = %d, stderr %s", exitCode, stderr)
	}
	before, err := os.Stat(filepath.Join(recipeDir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	prompt := &scriptedPrompt{}
	exitCode, stdout, stderr := runEditWithPrompt(recipeDir, prompt)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, want success for an edit that had nothing to do; stderr %s", exitCode, stderr)
	}
	if !strings.Contains(stdout, "Nothing changes: frostroot.toml already says this.") || !strings.Contains(stdout, "nothing changes; nothing written") {
		t.Errorf("stdout does not say nothing changes:\n%s", stdout)
	}
	last := len(prompt.questionsAsked) - 1
	if last < 0 || !strings.Contains(prompt.questionsAsked[last], "Nothing changes. Write anyway?") || prompt.defaultsOffered[last] != "n" {
		t.Errorf("the last question was %q with default %q; want the write-anyway question defaulting to n", prompt.questionsAsked[max(last, 0)], prompt.defaultsOffered[max(last, 0)])
	}
	after, err := os.Stat(filepath.Join(recipeDir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Error("the file was rewritten although nothing changed and the default was no")
	}
}
