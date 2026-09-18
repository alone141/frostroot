package recipe

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
	"strings"
	"testing"
	"time"
)

// writeCertificate writes a self-signed certificate at path, and returns the
// key it was signed with.
func writeCertificate(t *testing.T, path string) *ecdsa.PrivateKey {
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
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("making a directory: %v", err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatalf("writing a certificate: %v", err)
	}
	return key
}

func TestCertificatePathsReadsTheTableOrNothing(t *testing.T) {
	var without Recipe
	if paths := without.CertificatePaths(); paths != nil {
		t.Errorf("a recipe with no table returned %v, want nil", paths)
	}
	with := Recipe{Certificates: &Certificates{Include: []string{"certs/corp.pem"}}}
	if paths := with.CertificatePaths(); len(paths) != 1 || paths[0] != "certs/corp.pem" {
		t.Errorf("CertificatePaths returned %v, want [certs/corp.pem]", paths)
	}
}

func TestCertificateNameIsTheBaseNameWithoutItsExtension(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{path: "certs/corp-root.pem", want: "corp-root"},
		{path: "corp.crt", want: "corp"},
		{path: "corp", want: "corp"},
		{path: "certs/corp.root.pem", want: "corp.root"},
		{path: "a/b/c/deep_root.cer", want: "deep_root"},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			if got := CertificateName(test.path); got != test.want {
				t.Errorf("CertificateName(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}
}

func TestCheckCertificatePathRefusesWhatWouldLeaveTheRecipeDirectory(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		wantError bool
	}{
		{name: "a file beside the recipe", path: "corp-root.pem"},
		{name: "a file in a subdirectory", path: "certs/corp-root.pem"},
		{name: "empty", path: "", wantError: true},
		{name: "absolute", path: "/etc/ssl/certs/corp.pem", wantError: true},
		{name: "a parent directory", path: "../secrets/corp.pem", wantError: true},
		{name: "a parent directory in the middle", path: "certs/../../corp.pem", wantError: true},
		{name: "a windows separator", path: `certs\corp.pem`, wantError: true},
		{name: "the directory itself", path: ".", wantError: true},
		{name: "a name starting with a dash", path: "certs/-corp.pem", wantError: true},
		{name: "a name with a space", path: "certs/corp root.pem", wantError: true},
		{name: "a name with a quote", path: "certs/corp'; rm -rf /; x.pem", wantError: true},
		{name: "no name at all", path: "certs/.pem", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckCertificatePath(test.path)
			if test.wantError && err == nil {
				t.Errorf("CheckCertificatePath(%q) accepted it", test.path)
			}
			if !test.wantError && err != nil {
				t.Errorf("CheckCertificatePath(%q) refused it: %v", test.path, err)
			}
		})
	}
}

func TestValidateRefusesTwoCertificatesWithOneNameInTheImage(t *testing.T) {
	valid := Recipe{
		Image:        Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:         User{Name: "student"},
		Certificates: &Certificates{Include: []string{"certs/corp.pem", "other/corp.crt"}},
	}
	problems := Validate(valid)
	found := false
	for _, problem := range problems {
		if strings.Contains(problem, "corp.crt") {
			found = true
		}
	}
	if !found {
		t.Errorf("Validate did not report the collision: %v", problems)
	}
}

func TestCheckCertificateFilesReadsWhatIsThere(t *testing.T) {
	recipeDir := t.TempDir()
	key := writeCertificate(t, filepath.Join(recipeDir, "certs", "corp.pem"))

	keyBytes, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling a key: %v", err)
	}
	keyPath := filepath.Join(recipeDir, "certs", "secret.pem")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyBytes}), 0o600); err != nil {
		t.Fatalf("writing a key: %v", err)
	}

	tests := []struct {
		name        string
		paths       []string
		wantProblem string
	}{
		{name: "a certificate", paths: []string{"certs/corp.pem"}},
		{name: "no certificates at all", paths: nil},
		{name: "a file that is not there", paths: []string{"certs/missing.pem"}, wantProblem: "certs/missing.pem"},
		{name: "a private key", paths: []string{"certs/secret.pem"}, wantProblem: "private key"},
		{name: "a path Validate already refused", paths: []string{"../corp.pem"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			problems := CheckCertificateFiles(recipeDir, test.paths)
			if test.wantProblem == "" {
				if len(problems) != 0 {
					t.Errorf("CheckCertificateFiles reported %v, want none", problems)
				}
				return
			}
			if len(problems) != 1 || !strings.Contains(problems[0], test.wantProblem) {
				t.Errorf("CheckCertificateFiles reported %v, want one mentioning %q", problems, test.wantProblem)
			}
		})
	}
}

func TestCheckCertificateFilesReportsInstallNameCollisionsAfterABundleSplit(t *testing.T) {
	// corp.pem holding two certificates installs as corp.crt and corp-2.crt;
	// a second file named corp-2.pem would also install as corp-2.crt.
	recipeDir := t.TempDir()
	writeCertificate(t, filepath.Join(recipeDir, "certs", "corp.pem"))
	writeCertificate(t, filepath.Join(recipeDir, "certs", "corp-2.pem"))
	first, err := os.ReadFile(filepath.Join(recipeDir, "certs", "corp.pem"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(recipeDir, "certs", "corp-2.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipeDir, "certs", "corp.pem"), append(first, second...), 0o644); err != nil {
		t.Fatal(err)
	}
	problems := CheckCertificateFiles(recipeDir, []string{"certs/corp.pem", "certs/corp-2.pem"})
	if len(problems) != 1 || !strings.Contains(problems[0], "corp-2.crt") {
		t.Errorf("CheckCertificateFiles reported %v, want a collision on corp-2.crt", problems)
	}
}
