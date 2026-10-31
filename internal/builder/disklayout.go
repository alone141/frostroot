package builder

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"

	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// ErrDiskTooSmall means the recipe's [disk] size is less than the image
// needs. Compare with errors.Is; the user can fix it.
var ErrDiskTooSmall = errors.New("disk.size is too small for the image")

// The disk's geometry, in bytes: the GPT header and table before the first
// partition, which starts on the first MiB; the EFI system partition; the
// root after it, with room to spare, which systemd-repart grows to the end
// of the disk at the first boot; and the backup table after the last one.
const (
	diskMiB            = int64(1) << 20
	diskFirstPartition = diskMiB
	diskESPBytes       = 512 * diskMiB
	diskGPTTail        = diskMiB
	diskRootHeadroom   = 256 * diskMiB
	diskSectorBytes    = 512
	// diskMaxBytes is what [disk] size may name at most, as validate says.
	diskMaxBytes = 64 * 1024 * 1024 * diskMiB
)

// The partition types, from the discoverable partitions specification, so
// that systemd finds the root without fstab too, and the names
// systemd-repart gives such partitions.
const (
	espPartitionType  = "c12a7328-f81f-11d2-ba4b-00a0c93ec93b"
	rootPartitionType = "4f68bce3-e8cd-4db1-96e7-fbcaf984b709" // Linux root, x86-64
	espPartitionName  = "esp"
	rootPartitionName = "root-x86-64"
)

// diskShim is the file of /usr/lib/shim the EFI system partition boots
// from, as BOOTX64.EFI: shim 15.8, signed by Microsoft's 2011 UEFI CA,
// which boots older firmware too, while the archive has it (decision 4 of
// the bare-metal spec). The lock records which file a disk was made with.
const diskShim = "shimx64.efi.signed.latest"

// knownShims are the files of /usr/lib/shim a lock may name: what
// shim-signed ships today, and what its next version calls them.
var knownShims = []string{"shimx64.efi.signed.latest", "shimx64.efi.signed.previous", "shimx64.efi.signed.2011", "shimx64.efi.signed.2023"}

// diskLayout is what a disk is laid out from: the seed every identifier on
// it derives from, the sizes, and the shim. An online build draws the seed
// and sizes the disk once the tarball exists; an offline build takes all of
// it from the lock, so that two offline builds make the same bytes.
type diskLayout struct {
	seed     [16]byte
	shim     string
	fromLock bool
	// The sizes: 0 until size has run, online.
	diskBytes int64
	espBytes  int64
	rootBytes int64
}

// newDiskLayout draws a seed for an online build.
func newDiskLayout() (diskLayout, error) {
	seed, err := randomUUID()
	if err != nil {
		return diskLayout{}, fmt.Errorf("drawing the disk's seed: %w", err)
	}
	return diskLayout{seed: seed, shim: diskShim, espBytes: diskESPBytes}, nil
}

// diskLayoutFromLock reads an offline build's layout out of the lock, which
// is hostile input: the seed must be a UUID, the shim a file frostroot
// knows, and the sizes a layout frostroot makes.
func diskLayoutFromLock(lock recipe.Lockfile) (diskLayout, error) {
	if lock.Disk == nil {
		return diskLayout{}, fmt.Errorf("%w: it records no [disk]; run frostroot build online, then frostroot vendor", pool.ErrBadLock)
	}
	seed, err := parseUUID(lock.Disk.Seed)
	if err != nil {
		return diskLayout{}, fmt.Errorf("%w: [disk] seed: %w", pool.ErrBadLock, err)
	}
	if !slices.Contains(knownShims, lock.Disk.Shim) {
		return diskLayout{}, fmt.Errorf("%w: [disk] shim %q is not a file frostroot copies (%s)", pool.ErrBadLock, lock.Disk.Shim, strings.Join(knownShims, ", "))
	}
	layout := diskLayout{seed: seed, shim: lock.Disk.Shim, fromLock: true, diskBytes: lock.Disk.DiskBytes, espBytes: lock.Disk.ESPBytes, rootBytes: lock.Disk.RootBytes}
	if err := layout.checkSizes(); err != nil {
		return diskLayout{}, fmt.Errorf("%w: [disk] sizes: %w", pool.ErrBadLock, err)
	}
	return layout, nil
}

// checkSizes refuses sizes that are not a layout frostroot makes: whole
// MiBs, an EFI system partition and a root of some size, the disk exactly
// their sum with the tables around them, and no larger than validate lets a
// recipe ask for.
func (l diskLayout) checkSizes() error {
	for name, size := range map[string]int64{"esp_bytes": l.espBytes, "root_bytes": l.rootBytes, "disk_bytes": l.diskBytes} {
		if size <= 0 || size%diskMiB != 0 {
			return fmt.Errorf("%s = %d is not a positive whole number of MiB", name, size)
		}
	}
	if l.espBytes < 32*diskMiB || l.espBytes > 1024*diskMiB {
		return fmt.Errorf("esp_bytes = %d is not between 32 MiB and 1 GiB", l.espBytes)
	}
	if l.diskBytes != diskFirstPartition+l.espBytes+l.rootBytes+diskGPTTail {
		return fmt.Errorf("disk_bytes = %d is not the partitions and the tables around them, %d", l.diskBytes, diskFirstPartition+l.espBytes+l.rootBytes+diskGPTTail)
	}
	if l.diskBytes > diskMaxBytes {
		return fmt.Errorf("disk_bytes = %d is larger than the 64T a recipe may ask for", l.diskBytes)
	}
	return nil
}

// size decides an online build's sizes: the root holds the tarball, a
// quarter more and 256 MiB, rounded up to a MiB, and the disk is the
// partitions with the tables around them; or the recipe's [disk] size,
// with the root filling what the EFI system partition and the tables leave,
// which must be at least that much.
func (l *diskLayout) size(tarballBytes int64, requested string) error {
	root := roundUpToMiB(tarballBytes + tarballBytes/4 + diskRootHeadroom)
	needed := diskFirstPartition + l.espBytes + root + diskGPTTail
	if requested == "" {
		l.rootBytes, l.diskBytes = root, needed
		return nil
	}
	total, err := recipe.DiskSizeBytes(requested)
	if err != nil {
		return err
	}
	if total < needed {
		return fmt.Errorf("%w: %s holds less than the image needs, %dM; raise disk.size or leave it out", ErrDiskTooSmall, requested, needed/diskMiB)
	}
	l.rootBytes, l.diskBytes = total-diskFirstPartition-l.espBytes-diskGPTTail, total
	return nil
}

// roundUpToMiB rounds size up to a whole number of MiB.
func roundUpToMiB(size int64) int64 {
	return (size + diskMiB - 1) / diskMiB * diskMiB
}

// lock returns the layout as the lock records it.
func (l diskLayout) lock() recipe.LockDisk {
	return recipe.LockDisk{Seed: uuidText(l.seed), DiskBytes: l.diskBytes, ESPBytes: l.espBytes, RootBytes: l.rootBytes, Shim: l.shim}
}

// diskIdentifiers are every identifier on the disk, as UUID strings, and
// the EFI system partition's serial number.
type diskIdentifiers struct {
	disk           string // the GPT's own
	espPartition   string
	rootPartition  string
	espFilesystem  string
	rootFilesystem string
	rootHashSeed   string // what ext4 hashes directory entries with
	espSerial      string // 8 hex digits: what FAT has instead of a UUID
}

// identifiers derives every identifier from the seed the way systemd-repart
// (v255) does, so that a disk it would make from the same seed has the
// same ones: the GPT's from the seed with "disk-uuid", each partition's from
// the seed with its type, each file system's from its partition with
// "file-system-uuid", and the serial from the first four bytes of the file
// system's. The hash seed is frostroot's own, from the root partition with
// "hash-seed": repart passes mke2fs none.
func (l diskLayout) identifiers() diskIdentifiers {
	espType, rootType := mustParseUUID(espPartitionType), mustParseUUID(rootPartitionType)
	espPartition := deriveUUID(l.seed, espType[:])
	rootPartition := deriveUUID(l.seed, rootType[:])
	espFilesystem := deriveUUID(espPartition, []byte("file-system-uuid"))
	return diskIdentifiers{
		disk:           uuidText(deriveUUID(l.seed, []byte("disk-uuid"))),
		espPartition:   uuidText(espPartition),
		rootPartition:  uuidText(rootPartition),
		espFilesystem:  uuidText(espFilesystem),
		rootFilesystem: uuidText(deriveUUID(rootPartition, []byte("file-system-uuid"))),
		rootHashSeed:   uuidText(deriveUUID(rootPartition, []byte("hash-seed"))),
		espSerial:      hex.EncodeToString(espFilesystem[:4]),
	}
}

// deriveUUID is systemd's derive_uuid: the first half of the HMAC-SHA256 of
// message keyed by key, marked as a version 4 UUID.
func deriveUUID(key [16]byte, message []byte) [16]byte {
	mac := hmac.New(sha256.New, key[:])
	mac.Write(message)
	var derived [16]byte
	copy(derived[:], mac.Sum(nil)[:16])
	return markV4(derived)
}

// markV4 sets the version and variant bits of a UUID, as systemd's
// id128_make_v4_uuid does.
func markV4(id [16]byte) [16]byte {
	id[6] = (id[6] & 0x0f) | 0x40
	id[8] = (id[8] & 0x3f) | 0x80
	return id
}

// randomUUID draws a version 4 UUID.
func randomUUID() ([16]byte, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return [16]byte{}, err
	}
	return markV4(id), nil
}

// uuidText renders a UUID as systemd does: lowercase, 8-4-4-4-12.
func uuidText(id [16]byte) string {
	text := hex.EncodeToString(id[:])
	return text[:8] + "-" + text[8:12] + "-" + text[12:16] + "-" + text[16:20] + "-" + text[20:]
}

// parseUUID reads a dashed UUID, in either case.
func parseUUID(text string) ([16]byte, error) {
	var id [16]byte
	if len(text) != 36 || text[8] != '-' || text[13] != '-' || text[18] != '-' || text[23] != '-' {
		return id, fmt.Errorf("%q is not a UUID", text)
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(text, "-", ""))
	if err != nil || len(decoded) != 16 {
		return id, fmt.Errorf("%q is not a UUID", text)
	}
	copy(id[:], decoded)
	return id, nil
}

// mustParseUUID parses one of frostroot's own constants.
func mustParseUUID(text string) [16]byte {
	id, err := parseUUID(text)
	if err != nil {
		panic(err) // a constant of this file, checked by its tests
	}
	return id
}
