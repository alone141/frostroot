// Package pgp reads OpenPGP public keys the way apt needs them: it turns an
// ASCII-armored key into its binary form and names every primary key the
// file holds by its fingerprint. It walks the packet headers and hashes each
// primary key; it verifies no signatures and knows no policy.
//
// Every key is named, not only the first, because a key file becomes an apt
// signed-by keyring whole, and apt accepts a Release signed by any key in
// one.
package pgp

import (
	"bytes"
	// SHA-1 is what OpenPGP version 4 fingerprints are defined as (RFC 4880
	// 12.2). It identifies a key here; nothing is protected by its strength.
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// Key is a parsed public key file.
type Key struct {
	Binary      []byte // the key material as apt's signed-by wants it
	Fingerprint string // the first primary key's fingerprint, uppercase hex
	// Fingerprints is every primary key the file holds, in the order they
	// appear; Fingerprint is the first of them. Whoever decides what to trust
	// has to see all of them: apt accepts a Release signed by any key in a
	// signed-by keyring, so a file is only as trustworthy as its least
	// expected key.
	Fingerprints []string
	Armored      bool // the input was ASCII-armored
}

// ErrNotPublicKey means the data is not an OpenPGP public key: not armored
// and not starting with a public-key packet.
var ErrNotPublicKey = errors.New("not an OpenPGP public key")

// Armor markers.
const (
	armorBegin = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
	armorEnd   = "-----END PGP PUBLIC KEY BLOCK-----"
)

// OpenPGP packet constants (RFC 4880 and RFC 9580).
const (
	packetTagPublicKey = 6
	keyVersion4        = 4
	keyVersion6        = 6
	fingerprintV4Lead  = 0x99
	fingerprintV6Lead  = 0x9B
)

// ParsePublicKey parses data, armored or binary. A file may hold several
// keys, as some vendors' keyrings do: Fingerprints names every one of them,
// and Fingerprint is the first.
func ParsePublicKey(data []byte) (Key, error) {
	key := Key{Binary: data}
	if trimmed := bytes.TrimSpace(data); bytes.HasPrefix(trimmed, []byte(armorBegin)) {
		binaryKey, err := dearmor(string(trimmed))
		if err != nil {
			return Key{}, err
		}
		key.Binary, key.Armored = binaryKey, true
	}
	fingerprints, err := primaryFingerprints(key.Binary)
	if err != nil {
		return Key{}, err
	}
	key.Fingerprints, key.Fingerprint = fingerprints, fingerprints[0]
	return key, nil
}

// dearmor decodes an ASCII-armored block: the header lines up to the first
// blank line are skipped, the body is base64, and the optional "=XXXX" line
// is a CRC-24 of the decoded body, checked when present.
func dearmor(text string) ([]byte, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != armorBegin {
		return nil, fmt.Errorf("%w: malformed armor", ErrNotPublicKey)
	}
	lineIndex := 1
	for lineIndex < len(lines) && strings.TrimSpace(lines[lineIndex]) != "" {
		if !strings.Contains(lines[lineIndex], ":") {
			break // no headers at all: the body starts right away
		}
		lineIndex++
	}
	var body strings.Builder
	var checksumLine string
	ended := false
	for ; lineIndex < len(lines); lineIndex++ {
		line := strings.TrimSpace(lines[lineIndex])
		switch {
		case line == "":
		case line == armorEnd:
			ended = true
		case strings.HasPrefix(line, "="):
			checksumLine = line[1:]
		default:
			body.WriteString(line)
		}
		if ended {
			break
		}
	}
	if !ended {
		return nil, fmt.Errorf("%w: armor has no END line", ErrNotPublicKey)
	}
	decoded, err := base64.StdEncoding.DecodeString(body.String())
	if err != nil {
		return nil, fmt.Errorf("%w: armor body is not base64: %w", ErrNotPublicKey, err)
	}
	if checksumLine != "" {
		want, err := base64.StdEncoding.DecodeString(checksumLine)
		if err != nil || len(want) != 3 {
			return nil, fmt.Errorf("%w: malformed armor checksum", ErrNotPublicKey)
		}
		if got := crc24(decoded); got != uint32(want[0])<<16|uint32(want[1])<<8|uint32(want[2]) {
			return nil, fmt.Errorf("%w: armor checksum does not match; the file is damaged", ErrNotPublicKey)
		}
	}
	return decoded, nil
}

// CRC-24 as OpenPGP armor uses it (RFC 4880 6.1).
const (
	crc24Init       = 0xB704CE
	crc24Polynomial = 0x1864CFB
)

func crc24(data []byte) uint32 {
	crc := uint32(crc24Init)
	for _, octet := range data {
		crc ^= uint32(octet) << 16
		for range 8 {
			crc <<= 1
			if crc&0x1000000 != 0 {
				crc ^= crc24Polynomial
			}
		}
	}
	return crc & 0xFFFFFF
}

// primaryFingerprints walks every packet of binary key material and returns
// the fingerprint of each primary key, in the order they appear. The first
// packet must be a public key, which is how every keyring apt accepts
// begins. Subkeys (tag 14) are not returned: a subkey is already covered by
// the primary key that certifies it, and it is the primary keys that decide
// what a signed-by keyring trusts.
//
// Reading only the first packet would be enough to name a file, and not
// enough to trust one: whoever serves the key could append a second primary
// key after the expected one and have it accepted with it.
func primaryFingerprints(data []byte) ([]string, error) {
	var fingerprints []string
	for offset := 0; offset < len(data); {
		tag, body, next, err := packetAt(data, offset)
		if err != nil {
			return nil, err
		}
		if offset == 0 && tag != packetTagPublicKey {
			return nil, fmt.Errorf("%w: the first packet is tag %d, not a public key", ErrNotPublicKey, tag)
		}
		if tag == packetTagPublicKey {
			fingerprint, err := fingerprintOfKeyPacket(body)
			if err != nil {
				return nil, err
			}
			fingerprints = append(fingerprints, fingerprint)
		}
		offset = next
	}
	if len(fingerprints) == 0 {
		return nil, fmt.Errorf("%w: no public-key packet", ErrNotPublicKey)
	}
	return fingerprints, nil
}

// fingerprintOfKeyPacket returns the fingerprint of one public-key packet's
// body.
func fingerprintOfKeyPacket(body []byte) (string, error) {
	if len(body) == 0 {
		return "", fmt.Errorf("%w: empty key packet", ErrNotPublicKey)
	}
	switch version := body[0]; version {
	case keyVersion4:
		digest := sha1.New()
		digest.Write([]byte{fingerprintV4Lead, byte(len(body) >> 8), byte(len(body))})
		digest.Write(body)
		return strings.ToUpper(hex.EncodeToString(digest.Sum(nil))), nil
	case keyVersion6:
		digest := sha256.New()
		lengthBytes := make([]byte, 4)
		binary.BigEndian.PutUint32(lengthBytes, uint32(len(body)))
		digest.Write([]byte{fingerprintV6Lead})
		digest.Write(lengthBytes)
		digest.Write(body)
		return strings.ToUpper(hex.EncodeToString(digest.Sum(nil))), nil
	default:
		return "", fmt.Errorf("unsupported OpenPGP key version %d", version)
	}
}

// packetAt decodes the header of the OpenPGP packet starting at offset and
// returns its tag, its body, and where the packet after it starts. Both the
// old and the new header format are understood; partial-length bodies are
// not, since key packets never use them.
//
// Every header format consumes at least two bytes, so next is always past
// offset and a walk over a file always terminates.
func packetAt(data []byte, offset int) (tag int, body []byte, next int, err error) {
	packet := data[offset:]
	if len(packet) < 2 || packet[0]&0x80 == 0 {
		return 0, nil, 0, fmt.Errorf("%w: not an OpenPGP packet", ErrNotPublicKey)
	}
	header := packet[0]
	var headerLength, bodyLength int
	if header&0x40 != 0 {
		// New format: six tag bits, then a variable-length length.
		tag = int(header & 0x3F)
		first := int(packet[1])
		switch {
		case first < 192:
			headerLength, bodyLength = 2, first
		case first < 224:
			if len(packet) < 3 {
				return 0, nil, 0, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 3, (first-192)<<8+int(packet[2])+192
		case first == 255:
			if len(packet) < 6 {
				return 0, nil, 0, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 6, int(binary.BigEndian.Uint32(packet[2:6]))
		default:
			return 0, nil, 0, fmt.Errorf("%w: partial-length packet", ErrNotPublicKey)
		}
	} else {
		// Old format: four tag bits and a two-bit length type.
		tag = int(header>>2) & 0x0F
		switch header & 0x03 {
		case 0:
			headerLength, bodyLength = 2, int(packet[1])
		case 1:
			if len(packet) < 3 {
				return 0, nil, 0, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 3, int(binary.BigEndian.Uint16(packet[1:3]))
		case 2:
			if len(packet) < 5 {
				return 0, nil, 0, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 5, int(binary.BigEndian.Uint32(packet[1:5]))
		default:
			return 0, nil, 0, fmt.Errorf("%w: indeterminate packet length", ErrNotPublicKey)
		}
	}
	// A four-octet length that overflows int on a 32-bit build reads as
	// negative. The remaining comparison is written as a subtraction rather
	// than headerLength+bodyLength so that it cannot overflow in its turn;
	// every branch above has already required len(packet) >= headerLength.
	if bodyLength < 0 || bodyLength > len(packet)-headerLength {
		return 0, nil, 0, fmt.Errorf("%w: truncated packet", ErrNotPublicKey)
	}
	return tag, packet[headerLength : headerLength+bodyLength], offset + headerLength + bodyLength, nil
}

// armorLineLength is how many base64 characters an armor line holds.
const armorLineLength = 64

// Armor renders binary key material as an ASCII-armored public key block,
// the text form people commit and diff.
func Armor(binaryKey []byte) []byte {
	encoded := base64.StdEncoding.EncodeToString(binaryKey)
	var armored strings.Builder
	armored.WriteString(armorBegin + "\n\n")
	for start := 0; start < len(encoded); start += armorLineLength {
		armored.WriteString(encoded[start:min(start+armorLineLength, len(encoded))])
		armored.WriteByte('\n')
	}
	checksum := crc24(binaryKey)
	armored.WriteString("=" + base64.StdEncoding.EncodeToString([]byte{byte(checksum >> 16), byte(checksum >> 8), byte(checksum)}) + "\n")
	armored.WriteString(armorEnd + "\n")
	return []byte(armored.String())
}

// FormatFingerprint groups a fingerprint in fours for display:
// "9DC8 5822 9FC7 DD38 854A E2D8 8D81 803C 0EBF CD88".
func FormatFingerprint(fingerprint string) string {
	var groups []string
	for start := 0; start < len(fingerprint); start += 4 {
		groups = append(groups, fingerprint[start:min(start+4, len(fingerprint))])
	}
	return strings.Join(groups, " ")
}

// FormatFingerprints groups every fingerprint of a set for display and joins
// them with "and". A message about a key file names all of them: the file
// becomes a signed-by keyring whole, so naming only the first would say less
// than what is trusted.
func FormatFingerprints(fingerprints []string) string {
	grouped := make([]string, 0, len(fingerprints))
	for _, fingerprint := range fingerprints {
		grouped = append(grouped, FormatFingerprint(fingerprint))
	}
	return strings.Join(grouped, " and ")
}
