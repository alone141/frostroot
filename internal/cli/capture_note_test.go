package cli

import (
	"strings"
	"testing"
)

func TestCaptureSaysWhatItCouldNotCarryBeforeAsking(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, stdout, stderr := runCaptureWithAnswers(recipeDir, fakeUbuntuRoot(t, "24.04"), nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	noteAt := strings.Index(stdout, "A recipe cannot carry:")
	imagePageAt := strings.Index(stdout, "\nImage\n")
	if noteAt < 0 || imagePageAt < 0 {
		t.Fatalf("stdout lacks the note (%d) or the Image page (%d):\n%s", noteAt, imagePageAt, stdout)
	}
	if noteAt > imagePageAt {
		t.Errorf("the note comes after the first page of questions:\n%s", stdout)
	}
	for _, wantText := range []string{"What capture found", "Read ", "packages installed", "frostroot-capture.md"} {
		if !strings.Contains(stdout[:imagePageAt], wantText) {
			t.Errorf("the note lacks %q:\n%s", wantText, stdout[:imagePageAt])
		}
	}
}
