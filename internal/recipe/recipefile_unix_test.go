//go:build unix

package recipe

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRecipeFilesRefuseANamedPipeWithoutOpeningIt(t *testing.T) {
	recipeDir := t.TempDir()
	for _, path := range []string{"keys/vendor.asc", "certs/corp.pem"} {
		full := filepath.Join(recipeDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkfifo(full, 0o644); err != nil {
			t.Skipf("cannot make a named pipe here: %v", err)
		}
	}
	type result struct{ keyProblems, certificateProblems []string }
	done := make(chan result, 1)
	go func() {
		keyProblems, certificateProblems := checkRecipeFiles(recipeDir)
		done <- result{keyProblems, certificateProblems}
	}()
	select {
	case got := <-done:
		wantOneProblem(t, "the key", got.keyProblems, "keys/vendor.asc", "a named pipe, not a regular file")
		wantOneProblem(t, "the certificate", got.certificateProblems, "certs/corp.pem", "a named pipe, not a regular file")
	case <-time.After(10 * time.Second):
		t.Fatal("the checks blocked on a named pipe")
	}
}

func TestRecipeFilesSayALinkLoops(t *testing.T) {
	recipeDir := t.TempDir()
	symlinkOrSkip(t, "vendor.asc", filepath.Join(recipeDir, "keys", "vendor.asc"))
	symlinkOrSkip(t, "corp.pem", filepath.Join(recipeDir, "certs", "corp.pem"))

	keyProblems, certificateProblems := checkRecipeFiles(recipeDir)
	wantOneProblem(t, "the key", keyProblems, "keys/vendor.asc", "it is a symbolic link to vendor.asc, which cannot be followed: the links loop")
	wantOneProblem(t, "the certificate", certificateProblems, "certs/corp.pem", "it is a symbolic link to corp.pem, which cannot be followed: the links loop")
}
