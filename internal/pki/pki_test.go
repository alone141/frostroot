package pki

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"strings"
	"testing"
	"time"
)

// makeCertificate returns one self-signed certificate as PEM, and its key.
func makeCertificate(t *testing.T, commonName string, isCA bool) ([]byte, *ecdsa.PrivateKey) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating a key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("creating a certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), key
}

// makePrivateKeyPEM returns a private key file, the thing nobody should have
// handed out.
func makePrivateKeyPEM(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshaling a key: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})
}

func TestParseCertificatesReadsEveryCertificateInOrder(t *testing.T) {
	first, _ := makeCertificate(t, "First Root CA", true)
	second, _ := makeCertificate(t, "Second Root CA", true)
	third, _ := makeCertificate(t, "Intermediate", false)

	certificates, err := ParseCertificates(bytes.Join([][]byte{first, second, third}, nil))
	if err != nil {
		t.Fatalf("ParseCertificates: %v", err)
	}
	if len(certificates) != 3 {
		t.Fatalf("read %d certificates, want 3", len(certificates))
	}
	wantSubjects := []string{"CN=First Root CA", "CN=Second Root CA", "CN=Intermediate"}
	for index, want := range wantSubjects {
		if certificates[index].Subject != want {
			t.Errorf("certificate %d subject is %q, want %q", index, certificates[index].Subject, want)
		}
	}
	if !certificates[0].IsCA {
		t.Error("the first certificate is a CA and was not reported as one")
	}
	if certificates[2].IsCA {
		t.Error("the third certificate is not a CA and was reported as one")
	}
	if certificates[0].NotAfter.IsZero() {
		t.Error("NotAfter was not read")
	}
}

func TestParseCertificatesReEncodesWhatItRead(t *testing.T) {
	certificate, _ := makeCertificate(t, "Corp Root CA", true)
	// A file as an editor on Windows might leave it: CRLF, a comment before
	// the block, and no final newline.
	messy := append([]byte("# the corporate root\r\n"), bytes.ReplaceAll(certificate, []byte("\n"), []byte("\r\n"))...)
	messy = bytes.TrimRight(messy, "\r\n")

	certificates, err := ParseCertificates(messy)
	if err != nil {
		t.Fatalf("ParseCertificates: %v", err)
	}
	if len(certificates) != 1 {
		t.Fatalf("read %d certificates, want 1", len(certificates))
	}
	if !bytes.Equal(certificates[0].PEM, certificate) {
		t.Error("the re-encoded certificate is not the canonical PEM of what was parsed")
	}
	if !bytes.HasSuffix(certificates[0].PEM, []byte("\n")) {
		t.Error("the re-encoded certificate does not end with a newline")
	}
}

func TestParseCertificatesRefusesWhatIsNotACertificate(t *testing.T) {
	certificate, key := makeCertificate(t, "Corp Root CA", true)
	privateKey := makePrivateKeyPEM(t, key)

	tests := []struct {
		name    string
		data    []byte
		wantErr error
	}{
		{name: "a private key", data: privateKey, wantErr: ErrPrivateKey},
		{name: "a private key beside a certificate", data: append(append([]byte(nil), certificate...), privateKey...), wantErr: ErrPrivateKey},
		{name: "an empty file", data: nil, wantErr: ErrNoCertificate},
		{name: "not PEM at all", data: []byte("hello, this is not a certificate\n"), wantErr: ErrNoCertificate},
		{name: "an OpenPGP key", data: []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----\nmDMEZ\n-----END PGP PUBLIC KEY BLOCK-----\n"), wantErr: ErrNoCertificate},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseCertificates(test.data)
			if !errors.Is(err, test.wantErr) {
				t.Errorf("ParseCertificates returned %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestParseCertificatesNamesTheCertificateThatIsBroken(t *testing.T) {
	good, _ := makeCertificate(t, "Corp Root CA", true)
	broken := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte("not a certificate")})

	_, err := ParseCertificates(append(append([]byte(nil), good...), broken...))
	if err == nil {
		t.Fatal("ParseCertificates accepted a broken certificate")
	}
	if !strings.Contains(err.Error(), "certificate 2") {
		t.Errorf("the error does not say which certificate is broken: %v", err)
	}
}
