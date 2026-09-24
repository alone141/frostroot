package index

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
)

// overTLS serves what plain serves again over HTTPS, with a certificate no
// root signed: a network that inspects TLS, seen without its authority.
func overTLS(t *testing.T, plain *httptest.Server) *httptest.Server {
	t.Helper()
	server := httptest.NewTLSServer(plain.Config.Handler)
	t.Cleanup(server.Close)
	return server
}

func TestOpenInsecureAcceptsAnyCertificate(t *testing.T) {
	served := newArchive(t, ".gz")
	options := optionsFor(t, served)
	options.Mirror = overTLS(t, served.server).URL

	if _, err := Open(context.Background(), options); err == nil {
		t.Fatal("Open trusted a certificate no root signed")
	} else if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Errorf("the error does not say the certificate is the problem: %v", err)
	}
	options.Insecure = true
	opened, err := Open(context.Background(), options)
	if err != nil {
		t.Fatalf("Open with --insecure: %v", err)
	}
	if opened.Len() == 0 {
		t.Error("the index opened over an unverified connection is empty")
	}
}

func TestOpenPyPIInsecureAcceptsAnyCertificate(t *testing.T) {
	served := newSimpleIndex(t)
	options := pypiOptionsFor(t, served)
	options.Mirror = overTLS(t, served.server).URL

	if _, err := OpenPyPI(context.Background(), options); err == nil {
		t.Fatal("OpenPyPI trusted a certificate no root signed")
	}
	options.Insecure = true
	opened, err := OpenPyPI(context.Background(), options)
	if err != nil {
		t.Fatalf("OpenPyPI with --insecure: %v", err)
	}
	if opened.Len() != 15 {
		t.Errorf("Len = %d, want the 15 of the excerpt", opened.Len())
	}
}

func TestSummariesInsecureAcceptAnyCertificate(t *testing.T) {
	_, served := newSummaryServer(t)
	secure := overTLS(t, served.server)

	// A failed lookup is remembered as an empty summary, so that is what a
	// refused certificate looks like from here.
	verifying := NewSummaries(Options{Mirror: secure.URL})
	verifying.Fetch(context.Background(), "requests")
	if summary, _ := verifying.Cached("requests"); summary != "" {
		t.Fatalf("a verifying lookup accepted a certificate no root signed: %q", summary)
	}
	insecure := NewSummaries(Options{Mirror: secure.URL, Insecure: true})
	insecure.Fetch(context.Background(), "requests")
	if summary, known := insecure.Cached("requests"); !known || summary != "Python HTTP for Humans." {
		t.Errorf("Cached(requests) = %q, %v; want the summary over an unverified connection", summary, known)
	}
}
