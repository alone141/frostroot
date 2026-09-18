package cli

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestEditCanAddACertificateAuthority(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	writeTestCertificate(t, recipeDir, "certs/corp-root.pem")

	exitCode, _, stderr, _ := runEditWithAnswers(recipeDir, answersWith(map[int]string{answerCertificates: "certs/corp-root.pem"}))
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	if got := loadWrittenRecipe(t, recipeDir).CertificatePaths(); !slices.Equal(got, []string{"certs/corp-root.pem"}) {
		t.Errorf("the recipe names certificates %v, want the one typed", got)
	}
}

func TestEditRefusesAPrivateKeyTypedAsACertificate(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(recipeDir, "certs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipeDir, "certs", "secret.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	original := loadWrittenRecipe(t, recipeDir)

	exitCode, _, stderr, _ := runEditWithAnswers(recipeDir, answersWith(map[int]string{answerCertificates: "certs/secret.pem"}))
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr)
	}
	if !strings.Contains(stderr, "private key") || !strings.Contains(stderr, "nothing written") {
		t.Errorf("stderr should name the private key and write nothing:\n%s", stderr)
	}
	if got := loadWrittenRecipe(t, recipeDir); got.Certificates != nil || got.Image.Name != original.Image.Name {
		t.Errorf("the recipe changed although nothing should have been written: %+v", got)
	}
}
