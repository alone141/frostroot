package pgp

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Fingerprints of the fixture keys, computed with gpg on the build host.
// GitHub's keyring holds two primary keys; both are listed here, and the
// subkeys gpg also reports are not, because a subkey is not what a signed-by
// keyring is trusted by.
const (
	dockerFingerprint          = "9DC858229FC7DD38854AE2D88D81803C0EBFCD88"
	githubCLIFingerprint       = "2C6106201985B60E6C7AC87323F3D4EA75716059"
	githubCLISecondFingerprint = "7F38BBB59D064DBCB3D84D725612B36462313325"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseArmoredKey(t *testing.T) {
	// Docker's key as its website serves it: armored, one primary key.
	key, err := ParsePublicKey(readFixture(t, "docker.asc"))
	if err != nil {
		t.Fatal(err)
	}
	if key.Fingerprint != dockerFingerprint || !key.Armored {
		t.Errorf("Fingerprint = %s, Armored = %v", key.Fingerprint, key.Armored)
	}
	if len(key.Binary) == 0 || key.Binary[0] != 0x99 {
		t.Errorf("Binary should start with an old-format public-key packet, got %x", key.Binary[:min(4, len(key.Binary))])
	}
	// The binary form parses to the same key.
	again, err := ParsePublicKey(key.Binary)
	if err != nil {
		t.Fatal(err)
	}
	if again.Fingerprint != dockerFingerprint || again.Armored || !bytes.Equal(again.Binary, key.Binary) {
		t.Errorf("binary round trip = %+v", again)
	}
}

func TestParseBinaryKeyringWithTwoKeys(t *testing.T) {
	// GitHub's keyring holds two primary keys; the first one is the identity.
	key, err := ParsePublicKey(readFixture(t, "github-cli.gpg"))
	if err != nil {
		t.Fatal(err)
	}
	if key.Fingerprint != githubCLIFingerprint || key.Armored {
		t.Errorf("Fingerprint = %s, Armored = %v", key.Fingerprint, key.Armored)
	}
	// Both primary keys are named, and neither subkey is.
	want := []string{githubCLIFingerprint, githubCLISecondFingerprint}
	if !slices.Equal(key.Fingerprints, want) {
		t.Errorf("Fingerprints = %v, want %v", key.Fingerprints, want)
	}
}

func TestFingerprintsNamesEveryKeyInTheFile(t *testing.T) {
	// One primary key with one subkey is one fingerprint: a subkey is
	// certified by its primary and is not separately trusted.
	docker, err := ParsePublicKey(readFixture(t, "docker.asc"))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(docker.Fingerprints, []string{dockerFingerprint}) {
		t.Errorf("docker Fingerprints = %v, want only its primary key", docker.Fingerprints)
	}

	// A key appended after another is seen. This is the shape that defeated
	// the pin while only the first packet was read: the file still leads
	// with the expected key, so naming it by its first fingerprint says
	// nothing about what else it carries.
	appended := append(append([]byte(nil), docker.Binary...), readFixture(t, "github-cli.gpg")...)
	key, err := ParsePublicKey(appended)
	if err != nil {
		t.Fatal(err)
	}
	if key.Fingerprint != dockerFingerprint {
		t.Errorf("Fingerprint = %s, want the first key to be unchanged", key.Fingerprint)
	}
	want := []string{dockerFingerprint, githubCLIFingerprint, githubCLISecondFingerprint}
	if !slices.Equal(key.Fingerprints, want) {
		t.Errorf("Fingerprints = %v, want %v", key.Fingerprints, want)
	}
}

func TestParseArmoredKeyVariants(t *testing.T) {
	armored := string(readFixture(t, "docker.asc"))
	withoutChecksum := strings.Join(func() []string {
		var kept []string
		for _, line := range strings.Split(armored, "\n") {
			if !strings.HasPrefix(line, "=") {
				kept = append(kept, line)
			}
		}
		return kept
	}(), "\n")
	testCases := map[string]string{
		"CRLF line endings, as a Windows editor saves": strings.ReplaceAll(armored, "\n", "\r\n"),
		"surrounding whitespace":                       "\n\n  " + armored + "\n\n",
		"no checksum line":                             withoutChecksum,
		"header lines":                                 strings.Replace(armored, armorBegin+"\n", armorBegin+"\nVersion: GnuPG v2\nComment: test\n", 1),
	}
	for name, variant := range testCases {
		t.Run(name, func(t *testing.T) {
			key, err := ParsePublicKey([]byte(variant))
			if err != nil {
				t.Fatal(err)
			}
			if key.Fingerprint != dockerFingerprint {
				t.Errorf("Fingerprint = %s", key.Fingerprint)
			}
		})
	}
}

func TestParseRejectsNonKeys(t *testing.T) {
	armored := string(readFixture(t, "docker.asc"))
	damaged := strings.Replace(armored, "\n=", "\n=AAAA\n#", 1) // wrong checksum
	testCases := map[string][]byte{
		"HTML error page":      []byte("<!DOCTYPE html><html><body>404 Not Found</body></html>"),
		"empty":                nil,
		"whitespace":           []byte("  \n"),
		"armor without end":    []byte(armorBegin + "\n\nmQINBFit2io\n"),
		"armor bad base64":     []byte(armorBegin + "\n\n!!!not base64!!!\n" + armorEnd + "\n"),
		"armor bad checksum":   []byte(damaged),
		"signature packet":     {0x89, 0x00, 0x02, 0x04, 0x00}, // old format, tag 2
		"truncated packet":     {0x99, 0x01, 0x00, 0x04},
		"indeterminate length": {0x9B, 0x04},
	}
	for name, data := range testCases {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePublicKey(data)
			if !errors.Is(err, ErrNotPublicKey) {
				t.Errorf("err = %v, want ErrNotPublicKey", err)
			}
		})
	}
	// A key packet of a version nobody uses is a different error.
	_, err := ParsePublicKey([]byte{0x99, 0x00, 0x03, 0x03, 0x00, 0x00})
	if err == nil || errors.Is(err, ErrNotPublicKey) || !strings.Contains(err.Error(), "version 3") {
		t.Errorf("err = %v, want an unsupported-version error", err)
	}
}

func TestParseRejectsAMalformedPacketAfterTheFirst(t *testing.T) {
	// The walk reads the whole file, so a file that begins with a good key
	// and goes wrong later is refused rather than half-read. The reader this
	// replaced stopped after the first packet and accepted every one of
	// these, which is the same blind spot that let an appended key through.
	docker, err := ParsePublicKey(readFixture(t, "docker.asc"))
	if err != nil {
		t.Fatal(err)
	}
	testCases := map[string][]byte{
		"a key packet claiming more bytes than follow": {0x99, 0x01, 0x00, 0x04},
		"bytes that are not a packet at all":           []byte("not a packet"),
		"a partial-length packet":                      {0xC6, 0xE0},
	}
	for name, trailer := range testCases {
		t.Run(name, func(t *testing.T) {
			data := append(append([]byte(nil), docker.Binary...), trailer...)
			if _, err := ParsePublicKey(data); !errors.Is(err, ErrNotPublicKey) {
				t.Errorf("err = %v, want ErrNotPublicKey", err)
			}
		})
	}
}

func TestParseNewFormatHeaderAndVersion6(t *testing.T) {
	// A new-format header (0xC6 = tag 6) with a two-octet length, wrapping a
	// version 6 key body: the fingerprint is SHA-256 over 0x9B, a four-byte
	// length and the body.
	body := append([]byte{keyVersion6}, bytes.Repeat([]byte{0x01}, 199)...)
	data := append([]byte{0xC6, 192, byte(len(body) - 192)}, body...)
	key, err := ParsePublicKey(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(key.Fingerprint) != 64 {
		t.Errorf("v6 fingerprint = %q, want 64 hex digits", key.Fingerprint)
	}
}

func TestArmorRoundTrip(t *testing.T) {
	// GitHub's binary keyring, armored, parses back to the same bytes.
	binaryKey := readFixture(t, "github-cli.gpg")
	armored := Armor(binaryKey)
	if !bytes.HasPrefix(armored, []byte(armorBegin+"\n\n")) || !bytes.HasSuffix(armored, []byte(armorEnd+"\n")) {
		t.Errorf("armor markers missing:\n%s", armored)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(armored)), "\n") {
		if len(line) > armorLineLength {
			t.Errorf("line longer than %d: %q", armorLineLength, line)
		}
	}
	key, err := ParsePublicKey(armored)
	if err != nil {
		t.Fatal(err)
	}
	if !key.Armored || !bytes.Equal(key.Binary, binaryKey) || key.Fingerprint != githubCLIFingerprint {
		t.Errorf("round trip lost something: armored %v, %d bytes, %s", key.Armored, len(key.Binary), key.Fingerprint)
	}
}

func TestFormatFingerprint(t *testing.T) {
	if got := FormatFingerprint(dockerFingerprint); got != "9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88" {
		t.Errorf("FormatFingerprint = %q", got)
	}
}

func TestCRC24MatchesTheArmorChecksum(t *testing.T) {
	// The Docker key's armor carries a checksum line; dearmor verified it in
	// TestParseArmoredKey. Here the same body with one flipped byte must fail.
	armored := readFixture(t, "docker.asc")
	flipped := bytes.Replace(armored, []byte("mQINBFit2ioBEADh"), []byte("mQINBFit2ioBEADi"), 1)
	if bytes.Equal(flipped, armored) {
		t.Fatal("fixture changed; update the test")
	}
	if _, err := ParsePublicKey(flipped); err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Errorf("a flipped byte must fail the armor checksum, got %v", err)
	}
}
