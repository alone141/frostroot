package pgp

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// FuzzParsePublicKey: a key file comes from a vendor's server, and every
// primary key in it is pinned, so the parser must neither panic nor name
// a fingerprint that is not one.
func FuzzParsePublicKey(f *testing.F) {
	for _, name := range []string{"docker.asc", "github-cli.gpg"} {
		data, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	f.Add([]byte(armorBegin + "\n\n" + armorEnd + "\n"))
	f.Add([]byte{0x99, 0x00, 0x01, 0x04})
	f.Fuzz(func(t *testing.T, data []byte) {
		key, err := ParsePublicKey(data)
		if err != nil {
			return
		}
		if len(key.Fingerprints) == 0 || key.Fingerprint != key.Fingerprints[0] {
			t.Fatalf("parsed with fingerprints %q and first %q", key.Fingerprints, key.Fingerprint)
		}
		for _, fingerprint := range key.Fingerprints {
			if (len(fingerprint) != 40 && len(fingerprint) != 64) || strings.Trim(fingerprint, "0123456789ABCDEF") != "" {
				t.Fatalf("fingerprint %q is not 40 or 64 uppercase hex digits", fingerprint)
			}
		}
	})
}
