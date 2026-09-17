// Package pgp reads OpenPGP public keys the way apt needs them: it turns an
// ASCII-armored key into its binary form and computes the primary key's
// fingerprint. It parses one packet header and one hash; it verifies no
// signatures and knows no policy.
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
	Armored     bool   // the input was ASCII-armored
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
// keys, as some vendors' keyrings do; the fingerprint is the first one's.
func ParsePublicKey(data []byte) (Key, error) {
	key := Key{Binary: data}
	if trimmed := bytes.TrimSpace(data); bytes.HasPrefix(trimmed, []byte(armorBegin)) {
		binaryKey, err := dearmor(string(trimmed))
		if err != nil {
			return Key{}, err
		}
		key.Binary, key.Armored = binaryKey, true
	}
	fingerprint, err := fingerprintOf(key.Binary)
	if err != nil {
		return Key{}, err
	}
	key.Fingerprint = fingerprint
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

// fingerprintOf reads the first packet of binary key material, which must
// be a public-key packet, and returns its fingerprint.
func fingerprintOf(data []byte) (string, error) {
	tag, body, err := firstPacket(data)
	if err != nil {
		return "", err
	}
	if tag != packetTagPublicKey {
		return "", fmt.Errorf("%w: the first packet is tag %d, not a public key", ErrNotPublicKey, tag)
	}
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

// firstPacket decodes the header of the first OpenPGP packet and returns its
// tag and body. Both the old and the new header format are understood;
// partial-length bodies are not, since key packets never use them.
func firstPacket(data []byte) (tag int, body []byte, err error) {
	if len(data) < 2 || data[0]&0x80 == 0 {
		return 0, nil, fmt.Errorf("%w: not an OpenPGP packet", ErrNotPublicKey)
	}
	header := data[0]
	var headerLength, bodyLength int
	if header&0x40 != 0 {
		// New format: six tag bits, then a variable-length length.
		tag = int(header & 0x3F)
		first := int(data[1])
		switch {
		case first < 192:
			headerLength, bodyLength = 2, first
		case first < 224:
			if len(data) < 3 {
				return 0, nil, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 3, (first-192)<<8+int(data[2])+192
		case first == 255:
			if len(data) < 6 {
				return 0, nil, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 6, int(binary.BigEndian.Uint32(data[2:6]))
		default:
			return 0, nil, fmt.Errorf("%w: partial-length packet", ErrNotPublicKey)
		}
	} else {
		// Old format: four tag bits and a two-bit length type.
		tag = int(header>>2) & 0x0F
		switch header & 0x03 {
		case 0:
			headerLength, bodyLength = 2, int(data[1])
		case 1:
			if len(data) < 3 {
				return 0, nil, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 3, int(binary.BigEndian.Uint16(data[1:3]))
		case 2:
			if len(data) < 5 {
				return 0, nil, fmt.Errorf("%w: truncated packet header", ErrNotPublicKey)
			}
			headerLength, bodyLength = 5, int(binary.BigEndian.Uint32(data[1:5]))
		default:
			return 0, nil, fmt.Errorf("%w: indeterminate packet length", ErrNotPublicKey)
		}
	}
	if len(data) < headerLength+bodyLength {
		return 0, nil, fmt.Errorf("%w: truncated packet", ErrNotPublicKey)
	}
	return tag, data[headerLength : headerLength+bodyLength], nil
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
