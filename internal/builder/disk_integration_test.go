//go:build integration

package builder

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// diskIntegrationVariable is what runs the disk integration tests, which
// build a whole disk image with the kernel and its firmware, about 1.4 GB
// of it, and so never run in CI. The run is
// FROSTROOT_INTEGRATION_DISK=1 scripts/integration.sh -run 'TestIntegration.*Disk' ./internal/builder/.
const diskIntegrationVariable = "FROSTROOT_INTEGRATION_DISK"

// diskCheckTools are the host's programs the checks read the disk with.
var diskCheckTools = []string{"sfdisk", "e2fsck", "debugfs", "fsck.fat", "mdir"}

// skipUnlessDiskIntegrationAsked skips the disk integration tests unless
// asked for by their variable, and when the host lacks what makes or
// checks a disk.
func skipUnlessDiskIntegrationAsked(t *testing.T) {
	t.Helper()
	if os.Getenv(diskIntegrationVariable) == "" {
		t.Skipf("disk integration tests run only with %s=1: they build a disk with the kernel and its firmware", diskIntegrationVariable)
	}
	skipUnlessDiskToolsAvailable(t)
	for _, tool := range diskCheckTools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not installed, which the checks read the disk with", tool)
		}
	}
}

// resoluteDiskRecipe is a 26.04 disk of the essential packages and git.
func resoluteDiskRecipe(t *testing.T) recipe.Recipe {
	t.Helper()
	imageRecipe := recipe.Recipe{
		Image:    recipe.Image{Name: "tiny", Release: "26.04", Arch: "amd64", Target: recipe.TargetDisk},
		User:     recipe.User{Name: "student", Sudo: true},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Asia/Tokyo"},
		Packages: recipe.Packages{Include: []string{"git"}},
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Fatal(problems)
	}
	return imageRecipe
}

// TestIntegrationResoluteDisk builds a real 26.04 disk image with
// mmdebstrap and the tools tree, and reads it back: the GPT with sfdisk,
// the root with e2fsck and debugfs, the EFI system partition with fsck.fat
// and mtools. It is the only automated check that a disk frostroot makes
// is one a firmware and a kernel can read.
//
// Needs what TestIntegrationDiskToolsTree needs, the host's e2fsprogs,
// util-linux, dosfstools and mtools, about 5 GB of disk, and the variable
// above. Takes five to ten minutes and about 1.5 GB of downloads.
func TestIntegrationResoluteDisk(t *testing.T) {
	skipUnlessDiskIntegrationAsked(t)
	workRoot := newIntegrationWorkRoot(t)
	imageRecipe := resoluteDiskRecipe(t)
	builder := Builder{Bootstrapper: &Mmdebstrap{}, Tools: &Bubblewrap{}}
	progress := &testLogProgress{t: t}
	result, err := builder.Build(context.Background(), imageRecipe, Options{
		RecipeDir: t.TempDir(),
		GOOS:      "linux",
		Getenv:    fakeEnvironment(map[string]string{"XDG_CACHE_HOME": workRoot}),
		Progress:  progress,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("progress", func(t *testing.T) {
		if want := DiskPhases(false, false); !slices.Equal(progress.started, want) {
			t.Errorf("phases started = %v, want %v", progress.started, want)
		}
	})
	if result.WorkDir != "" || result.TarballPath != "" || result.ImagePath == "" {
		t.Errorf("Result = %+v, want the work directory removed, an image and no tarball", result)
	}
	lock, err := recipe.LoadLock(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("lock", func(t *testing.T) {
		if lock.Target != recipe.TargetDisk || lock.Disk == nil || len(lock.DiskTools) < 50 {
			t.Fatalf("lock: target %q, disk %+v, %d tools", lock.Target, lock.Disk, len(lock.DiskTools))
		}
		if _, err := diskLayoutFromLock(lock); err != nil {
			t.Errorf("the lock's [disk] does not pass its own check: %v", err)
		}
		names := map[string]bool{}
		for _, locked := range lock.Packages {
			names[locked.Name] = true
		}
		for _, want := range []string{"linux-image-generic", "shim-signed", "grub-efi-amd64-signed", "systemd-repart", "netplan.io", "git"} {
			if !names[want] {
				t.Errorf("the lock has no %s", want)
			}
		}
	})

	disk := readDisk(t, result.ImagePath, *lock.Disk)
	ids, _ := diskLayoutFromLock(lock)
	identifiers := ids.identifiers()

	t.Run("gpt", func(t *testing.T) {
		dump := hostRead(t, "sfdisk", "--dump", result.ImagePath)
		for _, want := range []string{
			"label: gpt",
			"label-id: " + strings.ToUpper(identifiers.disk),
			"start=        2048, size=     1048576, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B, uuid=" + strings.ToUpper(identifiers.espPartition) + `, name="esp"`,
			"start=     1050624, size=" + leftPad(strconv.FormatInt(lock.Disk.RootBytes/512, 10), 12) + ", type=4F68BCE3-E8CD-4DB1-96E7-FBCAF984B709, uuid=" + strings.ToUpper(identifiers.rootPartition) + `, name="root-x86-64", attrs="GUID:59"`,
		} {
			if !strings.Contains(dump, want) {
				t.Errorf("sfdisk --dump lacks %q:\n%s", want, dump)
			}
		}
		if info, err := os.Stat(result.ImagePath); err != nil || info.Size() != lock.Disk.DiskBytes {
			t.Errorf("the image is %d bytes, the lock says %d", info.Size(), lock.Disk.DiskBytes)
		}
		if allocated := allocatedBytes(t, result.ImagePath); allocated >= lock.Disk.DiskBytes {
			t.Errorf("the image takes %d bytes on disk of %d; it is not sparse", allocated, lock.Disk.DiskBytes)
		}
	})

	t.Run("root", func(t *testing.T) {
		if output, err := exec.Command("e2fsck", "-fn", disk.root).CombinedOutput(); err != nil {
			t.Errorf("e2fsck -fn: %v\n%s", err, output)
		}
		if uuid := hostRead(t, "debugfs", "-R", "stats", disk.root); !strings.Contains(uuid, "Filesystem UUID:          "+identifiers.rootFilesystem) {
			t.Errorf("the root's UUID is not the lock's:\n%s", firstLines(uuid, 8))
		} else if !strings.Contains(uuid, "Directory Hash Seed:      "+identifiers.rootHashSeed) {
			t.Errorf("the root's hash seed is not the lock's:\n%s", firstLines(uuid, 30))
		}
		// Ownership is real and the user's home is the user's; file
		// capabilities and hardlinks came through tar into the file system.
		if home := hostRead(t, "debugfs", "-R", "stat home/student", disk.root); !strings.Contains(home, "User:  1000   Group:  1000") {
			t.Errorf("home/student:\n%s", firstLines(home, 6))
		}
		if ping := hostRead(t, "debugfs", "-R", "ea_list usr/bin/ping", disk.root); !strings.Contains(ping, "security.capability") {
			t.Errorf("usr/bin/ping lost its capability:\n%s", ping)
		}
		if perl := hostRead(t, "debugfs", "-R", "stat usr/bin/perl", disk.root); !regexp.MustCompile(`Links: [2-9]`).MatchString(perl) {
			t.Errorf("usr/bin/perl has one link (perl5.* is a hardlink to perl):\n%s", firstLines(perl, 6))
		}
		for path, want := range map[string]string{
			"etc/machine-id":                "uninitialized\n",
			"etc/hostname":                  "tiny\n",
			"etc/fstab":                     "UUID=" + identifiers.rootFilesystem + " / ext4 defaults,x-systemd.growfs 0 1\n",
			"boot/grub/grub.cfg":            "root=UUID=" + identifiers.rootFilesystem + " ro console=ttyS0 console=tty0\n",
			"usr/lib/repart.d/50-root.conf": "Type=root\n",
			"usr/lib/systemd/system/multi-user.target.wants/frostroot-set-password.service": "",
		} {
			content := hostRead(t, "debugfs", "-R", "cat "+path, disk.root)
			if !strings.Contains(content, want) {
				t.Errorf("%s = %q, want it to hold %q", path, firstLines(content, 4), want)
			}
		}
		if shadow := hostRead(t, "debugfs", "-R", "cat etc/shadow", disk.root); !strings.Contains(shadow, "student:!unprovisioned:") || !regexp.MustCompile(`(?m)^root:[!*]`).MatchString(shadow) {
			t.Errorf("etc/shadow: the user's field is not !unprovisioned, or root is not locked")
		}
		for _, absent := range []string{"etc/ssh/ssh_host_ed25519_key", "var/lib/systemd/random-seed", "etc/wsl.conf", "etc/resolv.conf.frostroot", "var/log/dpkg.log"} {
			if listing := hostRead(t, "debugfs", "-R", "stat "+absent, disk.root); !strings.Contains(listing, "File not found") {
				t.Errorf("%s is in the image:\n%s", absent, firstLines(listing, 3))
			}
		}
		if link := hostRead(t, "debugfs", "-R", "stat etc/resolv.conf", disk.root); !strings.Contains(link, "../run/systemd/resolve/stub-resolv.conf") {
			t.Errorf("etc/resolv.conf is not systemd-resolved's link:\n%s", firstLines(link, 6))
		}
		if kernel := hostRead(t, "debugfs", "-R", "ls -l boot", disk.root); !strings.Contains(kernel, "vmlinuz") || !strings.Contains(kernel, "initrd.img") {
			t.Errorf("boot/ holds no kernel or initramfs:\n%s", kernel)
		}
	})

	t.Run("esp", func(t *testing.T) {
		if output, err := exec.Command("fsck.fat", "-n", disk.esp).CombinedOutput(); err != nil {
			t.Errorf("fsck.fat -n: %v\n%s", err, output)
		}
		// mdir prints the names that fit 8.3 as two columns, name and
		// extension, and every one here fits.
		listing := hostRead(t, "mdir", "-i", disk.esp, "-/", "::/")
		for _, want := range []string{`BOOTX64 +EFI +966768`, `grubx64 +efi`, `mmx64 +efi`, `grub +cfg +116`, "Directory for ::/EFI/ubuntu"} {
			if !regexp.MustCompile(want).MatchString(listing) {
				t.Errorf("the ESP's listing lacks %q:\n%s", want, listing)
			}
		}
		if !strings.Contains(listing, "Volume Serial Number is "+strings.ToUpper(identifiers.espSerial[:4])+"-"+strings.ToUpper(identifiers.espSerial[4:])) {
			t.Errorf("the ESP's serial is not the lock's %s:\n%s", identifiers.espSerial, firstLines(listing, 4))
		}
		stub := hostRead(t, "mtype", "-i", disk.esp, "::/EFI/ubuntu/grub.cfg")
		if stub != renderESPGrubConfig(identifiers) {
			t.Errorf("EFI/ubuntu/grub.cfg = %q", stub)
		}
		// The shim is the one the lock names: its bytes are the package's.
		shim := hostRead(t, "mtype", "-i", disk.esp, "::/EFI/BOOT/BOOTX64.EFI")
		if len(shim) < 500<<10 || !strings.HasPrefix(shim, "MZ") {
			t.Errorf("BOOTX64.EFI is %d bytes and starts %q; want a PE binary of about a megabyte", len(shim), firstLines(shim, 1)[:min(4, len(shim))])
		}
	})
}

// TestIntegrationDiskOfflineRebuild builds a 26.04 disk online, vendors
// every package the lock names, the tools tree's included, rebuilds the
// disk from the pool alone twice, and checks that the two rebuilds are one
// SHA-256 and that the lock stayed as it was. It is the only automated
// check of the promise a disk image is made for.
//
// Needs what TestIntegrationResoluteDisk needs. Takes fifteen minutes or
// so and about 2.5 GB of downloads: the packages once through mmdebstrap,
// once into the pool.
func TestIntegrationDiskOfflineRebuild(t *testing.T) {
	skipUnlessDiskIntegrationAsked(t)
	workRoot := newIntegrationWorkRoot(t)
	recipeDir := t.TempDir()
	imageRecipe := resoluteDiskRecipe(t)
	options := Options{RecipeDir: recipeDir, GOOS: "linux", Getenv: fakeEnvironment(map[string]string{"XDG_CACHE_HOME": workRoot})}
	builder := Builder{Bootstrapper: &Mmdebstrap{}, Tools: &Bubblewrap{}}

	t.Log("online build")
	online, err := builder.Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(online.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := pool.Manifest(lock)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) <= len(lock.Packages) {
		t.Errorf("the manifest lists %d files for %d image packages; want the tree's too", len(entries), len(lock.Packages))
	}

	t.Log("vendor")
	poolDir := filepath.Join(recipeDir, "vendor", "debs")
	summary, err := pool.Fetch(context.Background(), pool.FetchOptions{Dir: poolDir, Entries: entries, Fallback: pool.FallbackURL(lock)})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Fetched != len(entries) {
		t.Errorf("fetched %d of %d", summary.Fetched, len(entries))
	}

	t.Log("offline build")
	if err := os.Remove(online.ImagePath); err != nil {
		t.Fatal(err)
	}
	lockBefore, err := os.ReadFile(online.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	options.Offline = true
	imageRecipe.User.Name = "teacher" // provisioning may change; packages may not
	offline, err := builder.Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	if !offline.Reproducible || offline.SourceDateEpoch != lock.SourceDateEpoch || offline.ImagePath != online.ImagePath {
		t.Errorf("Result = %+v, want a reproducible build frozen at the lock's instant, placed where the online one was", offline)
	}
	firstImage := filepath.Join(recipeDir, "first.img")
	if err := os.Rename(offline.ImagePath, firstImage); err != nil {
		t.Fatal(err)
	}

	t.Log("second offline build")
	second, err := builder.Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	if firstSum, secondSum := sha256Of(t, firstImage), sha256Of(t, second.ImagePath); firstSum != secondSum {
		t.Errorf("two offline builds of one lock differ: %s and %s", firstSum, secondSum)
	}
	lockAfter, err := os.ReadFile(online.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(lockAfter, lockBefore) {
		t.Error("an offline build rewrote the lock")
	}
	// The changed provisioning reached the rebuilt disk.
	disk := readDisk(t, second.ImagePath, *lock.Disk)
	if shadow := hostRead(t, "debugfs", "-R", "cat etc/shadow", disk.root); !strings.Contains(shadow, "teacher:!unprovisioned:") {
		t.Error("the offline build did not provision the changed user")
	}
}

// diskParts are the two partitions of a disk image, each copied out into
// a file of its own for the host's tools to read.
type diskParts struct {
	esp, root string
}

// readDisk copies each partition out of the image by the lock's layout.
func readDisk(t *testing.T, imagePath string, layout recipe.LockDisk) diskParts {
	t.Helper()
	image, err := os.Open(imagePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = image.Close() }() // read-only: closing cannot lose data
	dir := t.TempDir()
	parts := diskParts{esp: filepath.Join(dir, "esp.vfat"), root: filepath.Join(dir, "root.ext4")}
	for _, part := range []struct {
		path          string
		offset, bytes int64
	}{
		{parts.esp, diskFirstPartition, layout.ESPBytes},
		{parts.root, diskFirstPartition + layout.ESPBytes, layout.RootBytes},
	} {
		file, err := os.Create(part.path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(file, io.NewSectionReader(image, part.offset, part.bytes)); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	return parts
}

// hostRead runs one of the host's programs and returns its output, both
// streams, failing the test when it cannot run at all. debugfs prints its
// answers and its "File not found" alike, so the caller reads them.
func hostRead(t *testing.T, program string, args ...string) string {
	t.Helper()
	output, err := exec.Command(program, args...).CombinedOutput()
	var exitErr *exec.ExitError
	if err != nil && !errors.As(err, &exitErr) {
		t.Fatalf("%s %s: %v", program, strings.Join(args, " "), err)
	}
	return string(output)
}

// firstLines returns the first count lines of text.
func firstLines(text string, count int) string {
	lines := strings.SplitN(text, "\n", count+1)
	if len(lines) > count {
		lines = lines[:count]
	}
	return strings.Join(lines, "\n")
}

// leftPad pads text with spaces to width, as sfdisk --dump aligns sizes.
func leftPad(text string, width int) string {
	return strings.Repeat(" ", max(0, width-len(text))) + text
}
