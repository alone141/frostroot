// Package pki reads the PEM certificate files a recipe names. It splits a
// file into one certificate each, because Debian's update-ca-certificates
// hashes the first certificate of a file and ignores the rest, and it
// re-encodes what it parsed so that what the image trusts is what frostroot
// understood. It verifies no chains and knows no policy.
package pki

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"
)

// Certificate is one certificate read from a recipe's file.
type Certificate struct {
	PEM      []byte    // the certificate re-encoded, one block, newline-terminated
	Subject  string    // as x509 renders it, for messages
	NotAfter time.Time // when it stops being valid
	IsCA     bool      // whether it is a certificate authority
}

// Errors of certificate reading. Compare with errors.Is.
var (
	// ErrNoCertificate means the file holds no PEM CERTIFICATE block.
	ErrNoCertificate = errors.New("no certificate in this file")
	// ErrPrivateKey means the file holds a private key. Saying so is worth
	// more than "no certificate": it is the mistake people make, and the
	// file should not have been handed out at all.
	ErrPrivateKey = errors.New("this is a private key, not a certificate")
)

// certificateBlockType is the PEM type of a certificate. "X509 CERTIFICATE"
// is an old spelling of the same thing and some tools still write it.
const (
	certificateBlockType    = "CERTIFICATE"
	oldCertificateBlockType = "X509 CERTIFICATE"
)

// ParseCertificates returns every certificate in a PEM file, in the order
// the file lists them. Anything that is not a certificate block is skipped,
// except a private key, which is an error: a file that holds one was not
// meant to be shared. A file with no certificate at all is ErrNoCertificate.
func ParseCertificates(data []byte) ([]Certificate, error) {
	var certificates []Certificate
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch block.Type {
		case certificateBlockType, oldCertificateBlockType:
			parsed, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, fmt.Errorf("certificate %d: %w", len(certificates)+1, err)
			}
			certificates = append(certificates, Certificate{
				// Re-encoded rather than copied out of the file, so that
				// trailing text, CRLF and an odd line width cannot change
				// what lands in the image.
				PEM:      pem.EncodeToMemory(&pem.Block{Type: certificateBlockType, Bytes: parsed.Raw}),
				Subject:  parsed.Subject.String(),
				NotAfter: parsed.NotAfter,
				IsCA:     parsed.IsCA,
			})
		default:
			if isPrivateKeyBlock(block.Type) {
				return nil, ErrPrivateKey
			}
		}
	}
	if len(certificates) == 0 {
		return nil, ErrNoCertificate
	}
	return certificates, nil
}

// ReadBundle reads a PEM file of certificate authorities and returns them
// re-encoded, one block each. It is what --ca-bundle names: a file the build
// trusts while it fetches, and nothing else. Failing here rather than at the
// first fetch is the point — a bundle that is a private key, or not a
// certificate at all, is worth saying before a build starts.
func ReadBundle(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	certificates, err := ParseCertificates(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return Concatenate(certificates), nil
}

// Concatenate joins certificates into one PEM file, in order.
func Concatenate(certificates []Certificate) []byte {
	var bundle []byte
	for _, certificate := range certificates {
		bundle = append(bundle, certificate.PEM...)
	}
	return bundle
}

// SystemPoolWith returns the host's root certificates with extra appended.
// Empty extra returns the system pool alone. The extra certificates are
// added to the public roots rather than used instead of them, so a network
// that inspects only some hosts still verifies the rest.
func SystemPoolWith(extraPEM []byte) (*x509.CertPool, error) {
	pool, err := x509.SystemCertPool()
	if err != nil {
		return nil, fmt.Errorf("reading the host's root certificates: %w", err)
	}
	if len(extraPEM) == 0 {
		return pool, nil
	}
	if !pool.AppendCertsFromPEM(extraPEM) {
		return nil, ErrNoCertificate
	}
	return pool, nil
}

// Transport returns a transport that verifies against pool, keeping
// everything else the default one does, the proxy environment above all. A
// nil pool returns a plain clone, which verifies against the host's roots.
func Transport(pool *x509.CertPool) *http.Transport {
	transport, isTransport := http.DefaultTransport.(*http.Transport)
	if !isTransport {
		return &http.Transport{Proxy: http.ProxyFromEnvironment, TLSClientConfig: tlsConfig(pool)}
	}
	clone := transport.Clone()
	if pool != nil {
		clone.TLSClientConfig = tlsConfig(pool)
	}
	return clone
}

// tlsConfig is the client configuration for pool.
func tlsConfig(pool *x509.CertPool) *tls.Config {
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
}

// isPrivateKeyBlock reports whether a PEM block type names a private key.
// The spellings are openssl's, across the formats it writes.
func isPrivateKeyBlock(blockType string) bool {
	switch blockType {
	case "PRIVATE KEY", "RSA PRIVATE KEY", "EC PRIVATE KEY", "DSA PRIVATE KEY", "ENCRYPTED PRIVATE KEY", "OPENSSH PRIVATE KEY":
		return true
	default:
		return false
	}
}
