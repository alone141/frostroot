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
