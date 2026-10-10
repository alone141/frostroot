package recipe

import (
	"reflect"
	"strings"
	"testing"
)

// diskRecipe returns valid.toml as a disk recipe: the target named, no
// [wsl], and a [disk] that asks for a size and a qcow2.
func diskRecipe(t *testing.T) Recipe {
	t.Helper()
	imageRecipe := loadValidRecipe(t)
	imageRecipe.Image.Target = TargetDisk
	imageRecipe.WSL = WSL{}
	imageRecipe.Disk = &Disk{Size: "16G", Formats: []string{DiskFormatQcow2}}
	return imageRecipe
}

// TestARecipeThatNamesNoTargetIsWSLs: every recipe written before there was
// a second target means what it meant, and saying "wsl" is the same.
func TestARecipeThatNamesNoTargetIsWSLs(t *testing.T) {
	imageRecipe := loadValidRecipe(t)
	if imageRecipe.Image.Target != "" || imageRecipe.Target() != TargetWSL {
		t.Fatalf("valid.toml: target %q, Target() %q; want none, and wsl", imageRecipe.Image.Target, imageRecipe.Target())
	}
	if imageRecipe.Disk != nil || imageRecipe.DiskSize() != "" || imageRecipe.DiskFormats() != nil {
		t.Errorf("valid.toml has a [disk]: %+v", imageRecipe.Disk)
	}
	imageRecipe.Image.Target = TargetWSL
	if problems := Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("target = \"wsl\": %q", problems)
	}
}

func TestADiskRecipeLoadsAndValidates(t *testing.T) {
	content := `[image]
name = "lab"
release = "26.04"
arch = "amd64"
target = "disk"

[user]
name = "student"
sudo = true

[disk]
size = "16G"
formats = ["qcow2"]

[locale]
lang = "en_US.UTF-8"
timezone = "UTC"

[packages]
include = ["git"]
`
	imageRecipe, err := Load(writeRecipeFile(t, content))
	if err != nil {
		t.Fatal(err)
	}
	want := Recipe{
		Image:    Image{Name: "lab", Release: "26.04", Arch: "amd64", Target: TargetDisk},
		User:     User{Name: "student", Sudo: true},
		Disk:     &Disk{Size: "16G", Formats: []string{DiskFormatQcow2}},
		Locale:   Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: Packages{Include: []string{"git"}},
	}
	if !reflect.DeepEqual(imageRecipe, want) {
		t.Fatalf("Load =\n%+v\nwant\n%+v", imageRecipe, want)
	}
	if problems := Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("Validate = %q", problems)
	}
	// [disk] may be left out, and Fedora builds disks too.
	imageRecipe.Disk = nil
	imageRecipe.Image.Distro, imageRecipe.Image.Release, imageRecipe.Packages.Include = "fedora", "44", []string{"gcc"}
	if problems := Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("a Fedora disk recipe without [disk]: %q", problems)
	}
}

func TestValidateRefusesWhatATargetCannotUse(t *testing.T) {
	testCases := []struct {
		name        string
		breakRecipe func(*Recipe)
		wantMessage string
	}{
		{"unknown target", func(r *Recipe) { r.Image.Target = "iso" }, `invalid image.target "iso" ("wsl" or "disk")`},
		{"target in capitals", func(r *Recipe) { r.Image.Target = "Disk" }, "invalid image.target"},
		{"systemd in a disk recipe", func(r *Recipe) { r.WSL.Systemd = true }, "[wsl] is for WSL recipes"},
		{"default_user in a disk recipe", func(r *Recipe) { r.WSL.DefaultUser = r.User.Name }, "[wsl] is for WSL recipes"},
		{"[disk] in a WSL recipe", func(r *Recipe) { r.Image.Target, r.WSL = "", WSL{Systemd: true} }, `[disk] is for disk recipes; set target = "disk"`},
		{"a size without a unit", func(r *Recipe) { r.Disk.Size = "16" }, "invalid disk.size"},
		{"a size in GB", func(r *Recipe) { r.Disk.Size = "16GB" }, "invalid disk.size"},
		{"a size in lowercase", func(r *Recipe) { r.Disk.Size = "16g" }, "invalid disk.size"},
		{"a fractional size", func(r *Recipe) { r.Disk.Size = "1.5G" }, "invalid disk.size"},
		{"a negative size", func(r *Recipe) { r.Disk.Size = "-1G" }, "invalid disk.size"},
		{"a size of zero", func(r *Recipe) { r.Disk.Size = "0G" }, "invalid disk.size"},
		{"a size with too many digits", func(r *Recipe) { r.Disk.Size = "12345678G" }, "invalid disk.size"},
		{"a size smaller than the ESP allows", func(r *Recipe) { r.Disk.Size = "512M" }, "out of range"},
		{"a size larger than 64T", func(r *Recipe) { r.Disk.Size = "65537G" }, "out of range"},
		{"vhdx", func(r *Recipe) { r.Disk.Formats = []string{"vhdx"} }, "vhdx comes in a later version"},
		{"raw named", func(r *Recipe) { r.Disk.Formats = []string{"raw"} }, "the raw image is always written"},
		{"qcow2 twice", func(r *Recipe) { r.Disk.Formats = []string{"qcow2", "qcow2"} }, "disk.formats names qcow2 twice"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			imageRecipe := diskRecipe(t)
			testCase.breakRecipe(&imageRecipe)
			problems := Validate(imageRecipe)
			if !strings.Contains(strings.Join(problems, "\n"), testCase.wantMessage) {
				t.Fatalf("Validate = %q, want a problem mentioning %q", problems, testCase.wantMessage)
			}
		})
	}
}

// TestADiskRecipeIsNotAskedForADefaultUser: the rule that [wsl]
// default_user equals the user is WSL's; a disk recipe has no [wsl] to
// break it with, and is told about the table rather than the field.
func TestADiskRecipeIsNotAskedForADefaultUser(t *testing.T) {
	imageRecipe := diskRecipe(t)
	imageRecipe.WSL.DefaultUser = "someone"
	problems := Validate(imageRecipe)
	if len(problems) != 1 || !strings.Contains(problems[0], "[wsl] is for WSL recipes") {
		t.Fatalf("Validate = %q, want the one problem with the table", problems)
	}
}

func TestDiskSizeBytes(t *testing.T) {
	for size, want := range map[string]int64{
		"1G":     1 << 30,
		"1024M":  1 << 30,
		"16G":    16 << 30,
		"4096M":  4 << 30,
		"65536G": 64 << 40,
	} {
		got, err := DiskSizeBytes(size)
		if err != nil || got != want {
			t.Errorf("DiskSizeBytes(%q) = %d, %v; want %d", size, got, err, want)
		}
	}
	for _, size := range []string{"", "1023M", "65537G", "G", "16T", "16 G", " 16G"} {
		if _, err := DiskSizeBytes(size); err == nil {
			t.Errorf("DiskSizeBytes(%q) succeeded", size)
		}
	}
}

func TestLockRoundTripWithADisk(t *testing.T) {
	original := sampleLock()
	original.Target = TargetDisk
	original.Disk = &LockDisk{
		Seed:      "6f9619ff-8b86-4011-b42d-00c04fc964ff",
		DiskBytes: 2522873856, ESPBytes: 536870912, RootBytes: 1983905792,
		Shim: "shimx64.efi.signed.latest",
	}
	original.DiskTools = []LockPackage{{Name: "e2fsprogs", Version: "1.47.2-3ubuntu4", Arch: "amd64"}}
	path, content := saveAndRead(t, original)
	reloaded, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, original) {
		t.Fatalf("round trip =\n%+v\nwant\n%+v\n%s", reloaded, original, content)
	}
	for _, want := range []string{`target = 'disk'`, "[disk]", "seed = '6f9619ff-8b86-4011-b42d-00c04fc964ff'", "[[disk_tools]]"} {
		if !strings.Contains(content, want) {
			t.Errorf("lock lacks %q:\n%s", want, content)
		}
	}
}

// TestALockOfWSLSaysNothingOfDisks: a tarball's lock is the bytes it was
// before there were disks, so every lock written so far rebuilds as it did.
func TestALockOfWSLSaysNothingOfDisks(t *testing.T) {
	_, content := saveAndRead(t, sampleLock())
	for _, unwanted := range []string{"target", "[disk]", "disk_tools", "seed"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("a WSL lock mentions %q:\n%s", unwanted, content)
		}
	}
}
