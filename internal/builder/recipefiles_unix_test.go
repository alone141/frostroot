//go:build unix

package builder

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestBuildReadsRefuseANamedPipeWithoutOpeningIt(t *testing.T) {
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
	type result struct{ keyErr, certificateErr error }
	done := make(chan result, 1)
	go func() {
		_, keyErr := readSourceKeys(recipeDir, vendorSource)
		_, certificateErr := ReadCertificates(recipeDir, []string{"certs/corp.pem"})
		done <- result{keyErr, certificateErr}
	}()
	select {
	case got := <-done:
		wantRecipeFileRefused(t, got.keyErr, ErrSourceKey, "keys/vendor.asc", "a named pipe, not a regular file")
		wantRecipeFileRefused(t, got.certificateErr, ErrCertificate, "certs/corp.pem", "a named pipe, not a regular file")
	case <-time.After(10 * time.Second):
		t.Fatal("the build's reads blocked on a named pipe")
	}
}
