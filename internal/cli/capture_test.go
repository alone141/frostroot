package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// fakeUbuntuRoot builds the least an Ubuntu root needs for capture: a
// release, a hostname, a user, and a dpkg status with two packages asked for.
func fakeUbuntuRoot(t *testing.T, release string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"etc/os-release":      "ID=ubuntu\nVERSION_ID=\"" + release + "\"\n",
		"etc/hostname":        "lab-pc\n",
		"etc/passwd":          "root:x:0:0::/root:/bin/bash\nmelik:x:1000:1000::/home/melik:/bin/bash\n",
		"etc/group":           "sudo:x:27:melik\nmelik:x:1000:\n",
		"etc/timezone":        "Europe/Istanbul\n",
		"home/melik/.bashrc":  "",
		"home/melik/.ssh/key": "",
		"var/lib/dpkg/status": "Package: dpkg\nStatus: install ok installed\nPriority: required\nArchitecture: amd64\n\n" +
			"Package: git\nStatus: install ok installed\nPriority: optional\nArchitecture: amd64\n\n" +
			"Package: mytool\nStatus: install ok installed\nPriority: optional\nArchitecture: amd64\n\n",
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// runCaptureWithAnswers runs `frostroot capture --root root` in recipeDir with
// scripted answers.
func runCaptureWithAnswers(recipeDir, root string, answers []string, args ...string) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: &scriptedPrompt{answers: answers}, ReadFile: noHostFile}
	exitCode = app.Run(append([]string{"capture", "--root", root}, args...))
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

func TestCaptureWritesRecipeAndReport(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, stdout, stderr := runCaptureWithAnswers(recipeDir, fakeUbuntuRoot(t, "22.04"), nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if imageRecipe.Image.Name != "lab-pc" || imageRecipe.Image.Release != "22.04" || imageRecipe.User.Name != "melik" || !imageRecipe.User.Sudo || imageRecipe.Locale.Timezone != "Europe/Istanbul" {
		t.Errorf("recipe = %+v, want the captured machine", imageRecipe)
	}
	if want := []string{"git", "mytool"}; !slices.Equal(imageRecipe.Packages.Include, want) {
		t.Errorf("packages = %q, want %q", imageRecipe.Packages.Include, want)
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("captured recipe does not validate: %v", problems)
	}
	report, err := os.ReadFile(filepath.Join(recipeDir, captureReportFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, wantText := range []string{"# frostroot capture report", "user \"melik\" from /etc/wsl.conf", ".ssh (secrets: never copy)"} {
		if wantText == "user \"melik\" from /etc/wsl.conf" {
			wantText = "user \"melik\": the first account" // no wsl.conf in this fixture
		}
		if !strings.Contains(string(report), wantText) {
			t.Errorf("report lacks %q:\n%s", wantText, report)
		}
	}
	for _, wantText := range []string{"Read ", "Ubuntu 22.04, 3 packages installed, 2 asked for", "Not captured (details in frostroot-capture.md)", "The user's home directory"} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout)
		}
	}
}

func TestCaptureRefusesUnsupportedRelease(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, _, stderr := runCaptureWithAnswers(recipeDir, fakeUbuntuRoot(t, "23.10"), nil)
	if exitCode != exitUserError || !strings.Contains(stderr, "unsupported Ubuntu release") || !strings.Contains(stderr, "23.10") {
		t.Errorf("exit code = %d, stderr %q; want a refusal naming the release", exitCode, stderr)
	}
	if entries, _ := os.ReadDir(recipeDir); len(entries) != 0 {
		t.Errorf("nothing may be written, found %v", entries)
	}
}

func TestCaptureRefusesToOverwriteWithoutForce(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	root := fakeUbuntuRoot(t, "24.04")
	if exitCode, _, stderr := runCaptureWithAnswers(recipeDir, root, nil); exitCode != exitUserError || !strings.Contains(stderr, "--force") {
		t.Errorf("exit code = %d, stderr %q; want a refusal mentioning --force", exitCode, stderr)
	}
	if exitCode, _, stderr := runCaptureWithAnswers(recipeDir, root, nil, "--force"); exitCode != exitSuccess {
		t.Errorf("--force: exit code = %d, stderr %s", exitCode, stderr)
	}
	if got := loadWrittenRecipe(t, recipeDir); got.Image.Name != "lab-pc" {
		t.Errorf("--force should have replaced the recipe, got %+v", got)
	}
}

func TestCaptureDeclinedWritesNothing(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, _, _ := runCaptureWithAnswers(recipeDir, fakeUbuntuRoot(t, "24.04"), answersWith(map[int]string{answerWrite: "n"}))
	if exitCode != exitUserError {
		t.Errorf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if entries, _ := os.ReadDir(recipeDir); len(entries) != 0 {
		t.Errorf("declining must write neither file, found %v", entries)
	}
}
