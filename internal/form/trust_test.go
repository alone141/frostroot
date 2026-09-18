package form

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

	"frostroot/internal/recipe"
)

// writePEM writes a self-signed certificate, or a private key, at a path
// relative to dir.
func writePEM(t *testing.T, dir, relativePath string, privateKey bool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	var block *pem.Block
	if privateKey {
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			t.Fatalf("marshaling a key: %v", err)
		}
		block = &pem.Block{Type: "EC PRIVATE KEY", Bytes: der}
	} else {
		template := &x509.Certificate{
			SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Corp Root CA"},
			NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
			IsCA: true, BasicConstraintsValid: true,
		}
		der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
		if err != nil {
			t.Fatalf("creating a certificate: %v", err)
		}
		block = &pem.Block{Type: "CERTIFICATE", Bytes: der}
	}
	full := filepath.Join(dir, filepath.FromSlash(relativePath))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, pem.EncodeToMemory(block), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestTheTrustPageAsksForCertificateFiles(t *testing.T) {
	var field *Field
	for _, candidate := range Fields(Host{}) {
		if candidate.Key == KeyCertificates {
			field = &candidate
		}
	}
	if field == nil {
		t.Fatal("no field asks for certificates")
	}
	if field.Page != PageTrust || field.Kind != KindInput || field.Validate == nil {
		t.Errorf("the certificates field is %+v; want an input on the Trust page with a validator", *field)
	}
	if pages := Pages(); pages[len(pages)-1] != PageTrust {
		t.Errorf("pages = %v; want Trust last", pages)
	}
}

func TestCheckCertificateList(t *testing.T) {
	recipeDir := t.TempDir()
	writePEM(t, recipeDir, "certs/corp.pem", false)
	writePEM(t, recipeDir, "certs/secret.pem", true)

	tests := []struct {
		name     string
		host     Host
		text     string
		wantText string // in the error; "" means accepted
	}{
		{name: "nothing", text: ""},
		{name: "a path, checked for shape only without a recipe directory", text: "certs/corp.pem"},
		{name: "a path leaving the directory", text: "../corp.pem", wantText: "invalid certificate path"},
		{name: "a name with a quote", text: "certs/x'y.pem", wantText: "invalid certificate file name"},
		{name: "a file that is there", host: Host{RecipeDir: recipeDir}, text: "certs/corp.pem"},
		{name: "two files, one missing", host: Host{RecipeDir: recipeDir}, text: "certs/corp.pem, certs/missing.pem", wantText: "certs/missing.pem"},
		{name: "a private key", host: Host{RecipeDir: recipeDir}, text: "certs/secret.pem", wantText: "private key"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := checkCertificateList(test.host)(test.text)
			if test.wantText == "" {
				if err != nil {
					t.Errorf("refused %q: %v", test.text, err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Errorf("checking %q gave %v, want an error mentioning %q", test.text, err, test.wantText)
			}
		})
	}
}

func TestCertificatePathsRoundTripThroughTheField(t *testing.T) {
	// A path the field cannot hold is carried, the others are shown; the
	// recipe written back names all of them.
	original := recipe.Recipe{
		Image:        recipe.Image{Name: "lab", Release: "24.04", Arch: "amd64"},
		User:         recipe.User{Name: "student"},
		Certificates: &recipe.Certificates{Include: []string{"certs/corp.pem", "my certs/other.pem", "second.crt"}},
	}
	values := FromRecipe(original)
	if got := values.String(KeyCertificates); got != "certs/corp.pem second.crt" {
		t.Errorf("the field shows %q, want the two paths it can hold", got)
	}
	if got := values.Strings(keyOriginalCertificates); !slices.Equal(got, []string{"my certs/other.pem"}) {
		t.Errorf("carried %v, want the path with a space", got)
	}
	roundTripped := ToRecipe(values)
	want := []string{"my certs/other.pem", "certs/corp.pem", "second.crt"}
	if got := roundTripped.CertificatePaths(); !slices.Equal(got, want) {
		t.Errorf("ToRecipe names %v, want %v", got, want)
	}
	if summary := Summary(values); !strings.Contains(summary, "my certs/other.pem") || !strings.Contains(summary, "second.crt") {
		t.Errorf("the summary does not show every certificate:\n%s", summary)
	}

	// Typing into the field adds to what was carried.
	values[KeyCertificates] = "certs/corp.pem, third.pem"
	if got := ToRecipe(values).CertificatePaths(); !slices.Equal(got, []string{"my certs/other.pem", "certs/corp.pem", "third.pem"}) {
		t.Errorf("after typing, ToRecipe names %v", got)
	}

	// Emptying the field and carrying nothing leaves no table at all.
	values[KeyCertificates] = ""
	values[keyOriginalCertificates] = []string(nil)
	if table := ToRecipe(values).Certificates; table != nil {
		t.Errorf("an empty answer left a table: %+v", table)
	}
}
