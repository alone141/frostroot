package pool

import (
	"context"
	"crypto/x509"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// privateWheelServer serves wheels over HTTPS with a certificate no public
// root knows, which is what a network that inspects TLS looks like from the
// inside. It returns the server and the authority that signed it.
func privateWheelServer(t *testing.T) (*httptest.Server, *x509.CertPool) {
	t.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		fileName := strings.TrimPrefix(request.URL.Path, "/packages/")
		if !strings.HasSuffix(fileName, ".whl") {
			http.NotFound(writer, request)
			return
		}
		_, _ = writer.Write(wheelContent(fileName))
	}))
	t.Cleanup(server.Close)
	trusted := x509.NewCertPool()
	trusted.AddCert(server.Certificate())
	return server, trusted
}

func TestFetchRefusesAnAuthorityItDoesNotKnow(t *testing.T) {
	server, _ := privateWheelServer(t)
	entry := wheelEntry(server.URL, "numpy", "2.5.3")

	_, err := Fetch(context.Background(), FetchOptions{Dir: t.TempDir(), Entries: []Entry{entry}})
	if err == nil {
		t.Fatal("Fetch trusted a certificate no root signed")
	}
	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Errorf("the error does not say the certificate is the problem: %v", err)
	}
}

func TestFetchTrustsTheAuthorityItIsGiven(t *testing.T) {
	server, trusted := privateWheelServer(t)
	entry := wheelEntry(server.URL, "numpy", "2.5.3")
	poolDir := t.TempDir()

	summary, err := Fetch(context.Background(), FetchOptions{Dir: poolDir, Entries: []Entry{entry}, RootCAs: trusted})
	if err != nil {
		t.Fatalf("Fetch with the authority: %v", err)
	}
	if summary.Fetched != 1 {
		t.Errorf("fetched %d files, want 1", summary.Fetched)
	}
	if status, err := Verify(poolDir, []Entry{entry}, nil); err != nil || len(status.Missing) != 0 {
		t.Errorf("the wheel did not land: %+v (%v)", status, err)
	}
}

func TestFetchInsecureAcceptsAnyCertificate(t *testing.T) {
	// --insecure: the authority is not needed, because nothing is verified.
	server, _ := privateWheelServer(t)
	entry := wheelEntry(server.URL, "numpy", "2.5.3")
	poolDir := t.TempDir()

	summary, err := Fetch(context.Background(), FetchOptions{Dir: poolDir, Entries: []Entry{entry}, Insecure: true})
	if err != nil {
		t.Fatalf("Fetch with --insecure: %v", err)
	}
	if summary.Fetched != 1 {
		t.Errorf("fetched %d files, want 1", summary.Fetched)
	}
}

func TestFetchInsecureStillChecksTheLock(t *testing.T) {
	// The transport trusts anyone; the lock trusts nobody. A file that is not
	// the lock's bytes is refused whatever connection it came over, which is
	// why skipping verification costs vendor nothing.
	server, _ := privateWheelServer(t)
	entry := wheelEntry(server.URL, "numpy", "2.5.3")
	entry.SHA256 = strings.Repeat("0", 64)

	_, err := Fetch(context.Background(), FetchOptions{Dir: t.TempDir(), Entries: []Entry{entry}, Insecure: true})
	if !errors.Is(err, ErrMismatch) {
		t.Fatalf("Fetch with --insecure of a file that is not the lock's = %v, want ErrMismatch", err)
	}
}
