package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// vendorSource is one source whose key the recipe keeps at keys/vendor.asc.
var vendorSource = []recipe.Source{{Name: "vendor", URL: "https://x", Key: "keys/vendor.asc"}}

// linkOrSkip makes a symbolic link, or skips the test where the system
// cannot.
func linkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
}

// wantRecipeFileRefused fails unless err is sentinel, names path as the
// recipe wrote it and says wantText.
func wantRecipeFileRefused(t *testing.T, err, sentinel error, path, wantText string) {
	t.Helper()
	if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), wantText) {
		t.Errorf("error = %v, want %v naming %s and saying %q", err, sentinel, path, wantText)
	}
}

func TestBuildReadsRefuseALinkOutOfTheRecipeDirectory(t *testing.T) {
	outside := t.TempDir()
	writeRecipeFile(t, outside, "vendor.asc", fakeKeyPacket)
	writeRecipeFile(t, outside, "corp.pem", certificatePEM(t, "Corp Root CA"))
	recipeDir := t.TempDir()
	linkOrSkip(t, filepath.Join(outside, "vendor.asc"), filepath.Join(recipeDir, "keys", "vendor.asc"))
	linkOrSkip(t, filepath.Join(outside, "corp.pem"), filepath.Join(recipeDir, "certs", "corp.pem"))

	_, err := readSourceKeys(recipeDir, vendorSource)
	wantRecipeFileRefused(t, err, ErrSourceKey, "keys/vendor.asc", "outside the recipe directory")
	_, err = ReadCertificates(recipeDir, []string{"certs/corp.pem"})
	wantRecipeFileRefused(t, err, ErrCertificate, "certs/corp.pem", "outside the recipe directory")
}

func TestBuildReadsFollowALinkInsideTheRecipeDirectory(t *testing.T) {
	recipeDir := t.TempDir()
	writeRecipeFile(t, recipeDir, "shared/vendor.asc", fakeKeyPacket)
	writeRecipeFile(t, recipeDir, "shared/corp.pem", certificatePEM(t, "Corp Root CA"))
	linkOrSkip(t, filepath.Join("..", "shared", "vendor.asc"), filepath.Join(recipeDir, "keys", "vendor.asc"))
	linkOrSkip(t, filepath.Join("..", "shared", "corp.pem"), filepath.Join(recipeDir, "certs", "corp.pem"))
	linkedDir := filepath.Join(t.TempDir(), "linked")
	linkOrSkip(t, recipeDir, linkedDir)

	for _, dir := range []string{recipeDir, linkedDir} {
		if keys, err := readSourceKeys(dir, vendorSource); err != nil || len(keys) != 1 {
			t.Errorf("readSourceKeys in %s: %v, %v; want the linked key", dir, keys, err)
		}
		if set, err := ReadCertificates(dir, []string{"certs/corp.pem"}); err != nil || len(set.Files) != 1 {
			t.Errorf("ReadCertificates in %s: %v; want the linked certificate", dir, err)
		}
	}
}

func TestBuildReadsRefuseADirectory(t *testing.T) {
	recipeDir := t.TempDir()
	for _, path := range []string{"keys/vendor.asc", "certs/corp.pem"} {
		if err := os.MkdirAll(filepath.Join(recipeDir, filepath.FromSlash(path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	_, err := readSourceKeys(recipeDir, vendorSource)
	wantRecipeFileRefused(t, err, ErrSourceKey, "keys/vendor.asc", "a directory, not a regular file")
	_, err = ReadCertificates(recipeDir, []string{"certs/corp.pem"})
	wantRecipeFileRefused(t, err, ErrCertificate, "certs/corp.pem", "a directory, not a regular file")
}

func TestBuildReadsRefuseAFileLargerThanTheCap(t *testing.T) {
	recipeDir := t.TempDir()
	writeRecipeFile(t, recipeDir, "keys/vendor.asc", make([]byte, recipe.MaxKeyFileBytes+1))
	writeRecipeFile(t, recipeDir, "certs/corp.pem", make([]byte, recipe.MaxCertificateFileBytes+1))
	_, err := readSourceKeys(recipeDir, vendorSource)
	wantRecipeFileRefused(t, err, ErrSourceKey, "keys/vendor.asc", "larger than")
	_, err = ReadCertificates(recipeDir, []string{"certs/corp.pem"})
	wantRecipeFileRefused(t, err, ErrCertificate, "certs/corp.pem", "larger than")
}
