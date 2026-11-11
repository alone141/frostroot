package cli

import (
	"bytes"
	"context"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyClientTrustsTheCABundle(t *testing.T) {
	// init/edit/capture apply --ca-bundle to the package index; they must
	// apply it to signing-key HTTPS as well. A private-CA server is what a
	// TLS-inspecting network looks like.
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	bundlePath := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(bundlePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}

	var stderr bytes.Buffer
	app := App{Stderr: &stderr, RecipeDir: t.TempDir(), ReadFile: noHostFile}
	app.withDefaults()
	if _, ok := app.packageIndexes(&indexFlags{caBundle: bundlePath}); !ok {
		t.Fatalf("packageIndexes: %s", stderr.String())
	}
	body, err := app.KeyClient.Get(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("KeyClient did not trust --ca-bundle: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
}

// privateAuthorityServer is a server whose certificate no host trusts, as
// behind a network that inspects TLS, and the recipe directory holding that
// certificate as certs/corp-root.pem, the file testdata/certificates.toml
// names.
func privateAuthorityServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)
	recipeDir := newRecipeDir(t, "certificates.toml")
	certificatePath := filepath.Join(recipeDir, "certs", "corp-root.pem")
	if err := os.MkdirAll(filepath.Dir(certificatePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o644); err != nil {
		t.Fatal(err)
	}
	return server, recipeDir
}

// TestKeyClientTrustsTheRecipesCertificates: build and vendor trust the
// recipe's [certificates]; edit and capture fetched with the host's alone,
// so behind a network that inspects TLS every key and index failed.
func TestKeyClientTrustsTheRecipesCertificates(t *testing.T) {
	server, recipeDir := privateAuthorityServer(t)
	var stderr bytes.Buffer
	app := App{Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile}
	app.withDefaults()
	if _, ok := app.packageIndexes(&indexFlags{certificates: []string{"certs/corp-root.pem"}}); !ok {
		t.Fatalf("packageIndexes: %s", stderr.String())
	}
	if _, err := app.KeyClient.Get(context.Background(), server.URL); err != nil {
		t.Errorf("KeyClient did not trust the recipe's certificate: %v", err)
	}
}

// TestKeyClientTrustsTheCertificatesTheWrittenRecipeNames: init's Trust
// page names a certificate only as the form ends, and the keys are fetched
// after the recipe is written.
func TestKeyClientTrustsTheCertificatesTheWrittenRecipeNames(t *testing.T) {
	server, recipeDir := privateAuthorityServer(t)
	var stderr bytes.Buffer
	app := App{Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile}
	app.withDefaults()
	indexes, ok := app.packageIndexes(&indexFlags{})
	if !ok {
		t.Fatalf("packageIndexes: %s", stderr.String())
	}
	if _, err := app.KeyClient.Get(context.Background(), server.URL); err == nil {
		t.Fatal("KeyClient trusted the private authority before any recipe named it")
	}
	app.trustWrittenCertificates([]string{"certs/corp-root.pem"}, indexes)
	if _, err := app.KeyClient.Get(context.Background(), server.URL); err != nil {
		t.Errorf("KeyClient did not trust the certificate the written recipe names: %v", err)
	}

	injected := &App{Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile, KeyClient: &fakeKeyClient{}}
	injected.withDefaults()
	if indexes, ok = injected.packageIndexes(&indexFlags{}); !ok {
		t.Fatalf("packageIndexes: %s", stderr.String())
	}
	injected.trustWrittenCertificates([]string{"certs/corp-root.pem"}, indexes)
	if _, isFake := injected.KeyClient.(*fakeKeyClient); !isFake {
		t.Errorf("KeyClient = %T, want the client the test injected kept", injected.KeyClient)
	}
}

// TestEditFetchesKeysTrustingACertificateItAdds: the keys are fetched after
// the recipe is written, and trust what it names by then.
func TestEditFetchesKeysTrustingACertificateItAdds(t *testing.T) {
	server, certificateDir := privateAuthorityServer(t)
	recipeDir := newRecipeDir(t, "valid.toml")
	certificate, err := os.ReadFile(filepath.Join(certificateDir, "certs", "corp-root.pem"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(recipeDir, "certs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipeDir, "certs", "corp-root.pem"), certificate, 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := App{
		Stdout: &stdout, Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile,
		Prompt: &scriptedPrompt{answers: answersWith(map[int]string{answerCertificates: "certs/corp-root.pem"})},
		Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": t.TempDir()}),
	}
	if exitCode := app.Run([]string{"edit", "--plain"}); exitCode != exitSuccess {
		t.Fatalf("exit %d\nstderr: %s", exitCode, stderr.String())
	}
	if _, err := app.KeyClient.Get(context.Background(), server.URL); err != nil {
		t.Errorf("the key fetch after writing did not trust the certificate the recipe now names: %v", err)
	}
}

// TestEditWarnsOfACertificateItCannotRead: edit may be how the recipe gets
// fixed, so an unreadable certificate file is a warning, and the form
// still opens; writing it is the form's to refuse.
func TestEditWarnsOfACertificateItCannotRead(t *testing.T) {
	recipeDir := newRecipeDir(t, "certificates.toml")
	if err := os.MkdirAll(filepath.Join(recipeDir, "certs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(recipeDir, "certs", "corp-root.pem"), []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	app := App{
		Stdout: &stdout, Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile,
		Prompt: &scriptedPrompt{}, Getenv: environmentWith(map[string]string{"XDG_CACHE_HOME": t.TempDir()}),
		KeyClient: &fakeKeyClient{},
	}
	exitCode := app.Run([]string{"edit", "--plain"})
	if !strings.Contains(stderr.String(), "warning: ") || !strings.Contains(stderr.String(), "certs/corp-root.pem") {
		t.Errorf("edit did not read the recipe's certificate, or did not say it could not (exit %d):\n%s", exitCode, stderr.String())
	}
	if !strings.HasPrefix(strings.TrimLeft(stdout.String(), "\n"), "Image\n") {
		t.Errorf("edit did not go on to the form (exit %d):\nstdout: %s\nstderr: %s", exitCode, stdout.String(), stderr.String())
	}
}

func TestKeyClientWithoutCABundleRefusesThePrivateAuthority(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	var stderr bytes.Buffer
	app := App{Stderr: &stderr, RecipeDir: t.TempDir(), ReadFile: noHostFile}
	app.withDefaults()
	if _, ok := app.packageIndexes(&indexFlags{}); !ok {
		t.Fatalf("packageIndexes: %s", stderr.String())
	}
	_, err := app.KeyClient.Get(context.Background(), server.URL)
	if err == nil {
		t.Fatal("KeyClient trusted a certificate no root signed")
	}
	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Errorf("the error does not say the certificate is the problem: %v", err)
	}
}
