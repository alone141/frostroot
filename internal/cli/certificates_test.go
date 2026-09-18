package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// writeTestCertificate writes a self-signed certificate at a path relative to
// recipeDir, as a recipe's [certificates] file.
func writeTestCertificate(t *testing.T, recipeDir, relativePath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "Corp Root CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}
	full := filepath.Join(recipeDir, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatalf("making a directory: %v", err)
	}
	if err := os.WriteFile(full, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatalf("writing a certificate: %v", err)
	}
}

func TestValidateReportsACertificateThatIsNotThere(t *testing.T) {
	// A build must not start without the authorities it will trust, so the
	// file is checked the way a source's key file is.
	recipeDir := newRecipeDir(t, "certificates.toml")
	exitCode, _, stderr := runValidateIn(recipeDir)
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr)
	}
	if !strings.Contains(stderr, "certs/corp-root.pem") {
		t.Errorf("stderr does not name the missing certificate: %s", stderr)
	}

	// With the file there, the same recipe validates.
	writeTestCertificate(t, recipeDir, "certs/corp-root.pem")
	if exitCode, _, stderr = runValidateIn(recipeDir); exitCode != exitSuccess {
		t.Fatalf("exit code = %d with the certificate in place, stderr %s", exitCode, stderr)
	}
}

func TestValidateReportsAPrivateKeyWhereACertificateBelongs(t *testing.T) {
	recipeDir := newRecipeDir(t, "certificates.toml")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling a key: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(recipeDir, "certs"), 0o755); err != nil {
		t.Fatalf("making a directory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(recipeDir, "certs", "corp-root.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatalf("writing a key: %v", err)
	}

	exitCode, _, stderr := runValidateIn(recipeDir)
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr)
	}
	if !strings.Contains(stderr, "private key") {
		t.Errorf("stderr does not say the file is a private key: %s", stderr)
	}
}

func TestEditKeepsCertificatesItNeverAsksAbout(t *testing.T) {
	// The form has no question about certificate authorities, so an edit
	// that regenerates the recipe from the template has to write them back
	// rather than quietly drop the image's trust.
	recipeDir := newRecipeDir(t, "certificates.toml")
	writeTestCertificate(t, recipeDir, "certs/corp-root.pem")

	exitCode, _, stderr, _ := runEditWithAnswers(recipeDir, nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	written := loadWrittenRecipe(t, recipeDir)
	if got := written.CertificatePaths(); !slices.Equal(got, []string{"certs/corp-root.pem"}) {
		t.Errorf("edit wrote certificates %v, want the recipe's own", got)
	}
}
