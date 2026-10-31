package builder

import (
	"errors"
	"regexp"
	"strings"
	"testing"

	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// fixtureSeed is the seed the recipe tests' lock fixture names.
const fixtureSeed = "6f9619ff-8b86-4011-b42d-00c04fc964ff"

// uuidV4Pattern is a dashed, lowercase, version 4 UUID.
var uuidV4Pattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func fixtureLayout(t *testing.T) diskLayout {
	t.Helper()
	seed, err := parseUUID(fixtureSeed)
	if err != nil {
		t.Fatal(err)
	}
	return diskLayout{seed: seed, shim: diskShim, espBytes: diskESPBytes}
}

// TestDiskIdentifiersMatchSystemdRepart: the GPT's id and both partitions'
// are what systemd-repart 255 (Ubuntu 24.04's) wrote into a disk it made
// from this seed with an esp and a root definition, read back with sfdisk
// --dump; the file systems' and the serial follow the same derivation from
// the partitions'.
func TestDiskIdentifiersMatchSystemdRepart(t *testing.T) {
	got := fixtureLayout(t).identifiers()
	want := diskIdentifiers{
		disk:           "0255db97-81f5-4242-95ce-207b5ae03e7c",
		espPartition:   "40f2809d-0da3-486c-8efa-4a54ad550c18",
		rootPartition:  "705b4bde-f25a-45f8-88a6-d6c1aaef8c39",
		espFilesystem:  "2fa09e74-c973-4709-9e8e-078e7def93fb",
		rootFilesystem: "d2b89045-90dd-48ac-b2fd-7a386d7eef7b",
		rootHashSeed:   "8310f374-aed3-4775-a9c5-cb1321e48533",
		espSerial:      "2fa09e74",
	}
	if got != want {
		t.Errorf("identifiers =\n%+v\nwant\n%+v", got, want)
	}
	for name, id := range map[string]string{"disk": got.disk, "esp": got.espPartition, "root": got.rootPartition, "esp fs": got.espFilesystem, "root fs": got.rootFilesystem, "hash seed": got.rootHashSeed} {
		if !uuidV4Pattern.MatchString(id) {
			t.Errorf("%s = %q is not a lowercase version 4 UUID", name, id)
		}
	}
	if got.espSerial != got.espFilesystem[:8] {
		t.Errorf("serial = %q, want the first eight digits of the file system's UUID %q", got.espSerial, got.espFilesystem)
	}
	// Another seed, other identifiers: nothing is a constant.
	other := fixtureLayout(t)
	other.seed[15]++
	if other.identifiers() == got {
		t.Error("two seeds gave the same identifiers")
	}
}

func TestNewDiskLayoutDrawsASeed(t *testing.T) {
	first, err := newDiskLayout()
	if err != nil {
		t.Fatal(err)
	}
	second, err := newDiskLayout()
	if err != nil {
		t.Fatal(err)
	}
	if !uuidV4Pattern.MatchString(uuidText(first.seed)) || first.seed == second.seed {
		t.Errorf("seeds %s and %s; want two version 4 UUIDs", uuidText(first.seed), uuidText(second.seed))
	}
	if first.fromLock || first.shim != diskShim || first.espBytes != diskESPBytes || first.diskBytes != 0 || first.rootBytes != 0 {
		t.Errorf("a new layout is %+v; want the shim and the ESP's size known, the rest not yet", first)
	}
}

func TestDiskSizes(t *testing.T) {
	const tarball = 1_370_000_000 // the spike's 26.04 image, uncompressed
	layout := fixtureLayout(t)
	if err := layout.size(tarball, ""); err != nil {
		t.Fatal(err)
	}
	wantRoot := roundUpToMiB(tarball + tarball/4 + 256*diskMiB)
	if layout.rootBytes != wantRoot || layout.rootBytes%diskMiB != 0 {
		t.Errorf("root = %d, want the tarball, a quarter more and 256 MiB rounded up, %d", layout.rootBytes, wantRoot)
	}
	if layout.diskBytes != diskMiB+diskESPBytes+wantRoot+diskMiB {
		t.Errorf("disk = %d, want the partitions and a MiB of tables at each end", layout.diskBytes)
	}
	if err := layout.checkSizes(); err != nil {
		t.Errorf("the sizes an online build decides fail the lock's check: %v", err)
	}

	asked := fixtureLayout(t)
	if err := asked.size(tarball, "16G"); err != nil {
		t.Fatal(err)
	}
	if asked.diskBytes != 16<<30 || asked.rootBytes != 16<<30-diskMiB-diskESPBytes-diskMiB {
		t.Errorf("with 16G: disk = %d, root = %d; want the disk as asked and the root filling it", asked.diskBytes, asked.rootBytes)
	}

	small := fixtureLayout(t)
	err := small.size(tarball, "1G")
	if !errors.Is(err, ErrDiskTooSmall) || !strings.Contains(err.Error(), "1G holds less than the image needs, ") || !strings.Contains(err.Error(), "M; raise disk.size or leave it out") {
		t.Errorf("with 1G: size = %v, want ErrDiskTooSmall naming what the image needs", err)
	}
	if err := small.size(tarball, "16X"); err == nil {
		t.Error("a size validate would refuse was accepted")
	}
}

func TestDiskLayoutRoundTripsThroughTheLock(t *testing.T) {
	layout := fixtureLayout(t)
	if err := layout.size(1_370_000_000, ""); err != nil {
		t.Fatal(err)
	}
	locked := layout.lock()
	if locked.Seed != fixtureSeed || locked.Shim != diskShim || locked.ESPBytes != diskESPBytes || locked.DiskBytes != layout.diskBytes || locked.RootBytes != layout.rootBytes {
		t.Errorf("lock() = %+v", locked)
	}
	again, err := diskLayoutFromLock(recipe.Lockfile{Target: recipe.TargetDisk, Disk: &locked})
	if err != nil {
		t.Fatal(err)
	}
	if !again.fromLock || again.seed != layout.seed || again.shim != layout.shim || again.diskBytes != layout.diskBytes || again.espBytes != layout.espBytes || again.rootBytes != layout.rootBytes {
		t.Errorf("from the lock = %+v, want the layout it came from, marked as the lock's", again)
	}
	if again.identifiers() != layout.identifiers() {
		t.Error("the identifiers changed through the lock")
	}
	// The seed is accepted in either case, as systemd-repart takes it.
	upper := locked
	upper.Seed = strings.ToUpper(upper.Seed)
	if fromUpper, err := diskLayoutFromLock(recipe.Lockfile{Disk: &upper}); err != nil || fromUpper.seed != layout.seed {
		t.Errorf("an uppercase seed: %v, %+v", err, fromUpper)
	}
}

// TestDiskLayoutFromLockRefusesWhatItCannotLayOut: the lock is a text file
// anyone can edit, and every value here reaches a tool.
func TestDiskLayoutFromLockRefusesWhatItCannotLayOut(t *testing.T) {
	good := fixtureLayout(t)
	if err := good.size(1_370_000_000, ""); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		name string
		edit func(disk *recipe.LockDisk)
		want string
	}{
		{"no seed", func(disk *recipe.LockDisk) { disk.Seed = "" }, "seed"},
		{"a seed that is not a UUID", func(disk *recipe.LockDisk) { disk.Seed = "6f9619ff-8b86-4011-b42d-00c04fc964zz" }, "not a UUID"},
		{"a seed with a shell in it", func(disk *recipe.LockDisk) { disk.Seed = "$(id)-8b86-4011-b42d-00c04fc964ff" }, "not a UUID"},
		{"an unknown shim", func(disk *recipe.LockDisk) { disk.Shim = "../../evil.efi" }, "shim"},
		{"a root that is not whole MiBs", func(disk *recipe.LockDisk) { disk.RootBytes++; disk.DiskBytes++ }, "whole number of MiB"},
		{"a disk that is not its partitions", func(disk *recipe.LockDisk) { disk.DiskBytes += diskMiB }, "not the partitions"},
		{"an ESP too small", func(disk *recipe.LockDisk) { disk.ESPBytes = 8 * diskMiB; disk.DiskBytes -= diskESPBytes - 8*diskMiB }, "esp_bytes"},
		{"a disk beyond 64T", func(disk *recipe.LockDisk) {
			disk.RootBytes = diskMaxBytes
			disk.DiskBytes = diskMiB + disk.ESPBytes + disk.RootBytes + diskMiB
		}, "64T"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			locked := good.lock()
			testCase.edit(&locked)
			_, err := diskLayoutFromLock(recipe.Lockfile{Disk: &locked})
			if !errors.Is(err, pool.ErrBadLock) || !strings.Contains(err.Error(), testCase.want) {
				t.Errorf("diskLayoutFromLock = %v, want ErrBadLock mentioning %q", err, testCase.want)
			}
		})
	}
	if _, err := diskLayoutFromLock(recipe.Lockfile{Target: recipe.TargetDisk}); !errors.Is(err, pool.ErrBadLock) || !strings.Contains(err.Error(), "records no [disk]") {
		t.Errorf("a lock without [disk] = %v, want ErrBadLock saying so", err)
	}
}

func TestUUIDText(t *testing.T) {
	for _, text := range []string{fixtureSeed, "c12a7328-f81f-11d2-ba4b-00a0c93ec93b", "00000000-0000-0000-0000-000000000000"} {
		id, err := parseUUID(text)
		if err != nil {
			t.Errorf("parseUUID(%q) = %v", text, err)
		}
		if uuidText(id) != text {
			t.Errorf("uuidText(parseUUID(%q)) = %q", text, uuidText(id))
		}
	}
	for _, bad := range []string{"", "6f9619ff8b864011b42d00c04fc964ff", "6f9619ff-8b86-4011-b42d-00c04fc964f", "6f9619ff-8b86-4011-b42d-00c04fc964ffa", "6f9619ff_8b86_4011_b42d_00c04fc964ff"} {
		if _, err := parseUUID(bad); err == nil {
			t.Errorf("parseUUID(%q) accepted it", bad)
		}
	}
}
