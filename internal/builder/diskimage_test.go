package builder

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// diskStageOptionsFor returns the options a disk build of release writes
// its stage with, from the fixture seed.
func diskStageOptionsFor(t *testing.T, version string) DiskStageOptions {
	t.Helper()
	release, err := distro.Lookup(version, "amd64")
	if err != nil {
		t.Fatal(err)
	}
	return DiskStageOptions{identifiers: fixtureLayout(t).identifiers(), release: release}
}

// diskFileByPath finds the rendered file for one path in the image.
func diskFileByPath(t *testing.T, files []diskFile, imagePath string) diskFile {
	t.Helper()
	for _, file := range files {
		if file.imagePath == imagePath {
			return file
		}
	}
	t.Fatalf("no file for %s among %d", imagePath, len(files))
	return diskFile{}
}

func TestRenderDiskFiles(t *testing.T) {
	imageRecipe := sampleDiskRecipe()
	files, script, err := renderDiskFiles(imageRecipe, diskStageOptionsFor(t, "26.04"))
	if err != nil {
		t.Fatal(err)
	}
	ids := fixtureLayout(t).identifiers()
	wantPaths := []string{
		"/etc/fstab", "/usr/lib/repart.d/50-root.conf", "/boot/grub/grub.cfg", "/etc/netplan/50-frostroot.yaml",
		"/etc/hostname", "/etc/locale.conf", "/etc/vconsole.conf", "/usr/lib/frostroot/set-password",
		"/usr/lib/systemd/system/frostroot-set-password.service",
	}
	var paths []string
	for _, file := range files {
		paths = append(paths, file.imagePath)
	}
	if !slices.Equal(paths, wantPaths) {
		t.Errorf("files = %q, want %q", paths, wantPaths)
	}
	fstab := diskFileByPath(t, files, "/etc/fstab").content
	for _, want := range []string{
		"UUID=" + ids.rootFilesystem + " / ext4 defaults,x-systemd.growfs 0 1\n",
		"UUID=2FA0-9E74 /boot/efi vfat umask=0077,nofail 0 1\n",
	} {
		if !strings.Contains(fstab, want) {
			t.Errorf("fstab lacks %q:\n%s", want, fstab)
		}
	}
	grub := diskFileByPath(t, files, "/boot/grub/grub.cfg").content
	for _, want := range []string{
		"search --no-floppy --fs-uuid --set=root " + ids.rootFilesystem + "\n",
		"linux /boot/vmlinuz root=UUID=" + ids.rootFilesystem + " ro console=ttyS0 console=tty0\n",
		"initrd /boot/initrd.img\n",
	} {
		if !strings.Contains(grub, want) {
			t.Errorf("grub.cfg lacks %q:\n%s", want, grub)
		}
	}
	if stub := renderESPGrubConfig(ids); stub != "search.fs_uuid "+ids.rootFilesystem+" root\nset prefix=($root)'/boot/grub'\nconfigfile $prefix/grub.cfg\n" {
		t.Errorf("the ESP's grub.cfg = %q", stub)
	}
	if repart := diskFileByPath(t, files, "/usr/lib/repart.d/50-root.conf").content; !strings.Contains(repart, "[Partition]\nType=root\n") {
		t.Errorf("repart.d = %q", repart)
	}
	if netplan := diskFileByPath(t, files, "/etc/netplan/50-frostroot.yaml").content; !strings.Contains(netplan, "renderer: networkd") || !strings.Contains(netplan, "name: \"e*\"") || !strings.Contains(netplan, "dhcp4: true") {
		t.Errorf("netplan = %q", netplan)
	}
	if hostname := diskFileByPath(t, files, "/etc/hostname").content; hostname != "cpp-lab\n" {
		t.Errorf("hostname = %q, want the image's name", hostname)
	}
	if locale := diskFileByPath(t, files, "/etc/locale.conf").content; locale != "LANG=en_US.UTF-8\n" {
		t.Errorf("locale.conf = %q", locale)
	}
	if vconsole := diskFileByPath(t, files, "/etc/vconsole.conf").content; vconsole != "KEYMAP=us\n" {
		t.Errorf("vconsole.conf = %q", vconsole)
	}
	unit := diskFileByPath(t, files, "/usr/lib/systemd/system/frostroot-set-password.service").content
	for _, want := range []string{
		"ExecCondition=/usr/lib/frostroot/set-password check student\n",
		"ExecStart=/usr/lib/frostroot/set-password set student\n",
		"Wants=getty-pre.target\nBefore=getty-pre.target display-manager.service\n",
		"After=systemd-vconsole-setup.service plymouth-quit-wait.service\n",
		"ImportCredential=passwd.hashed-password.*\n",
		"TTYPath=/dev/console\nTTYReset=yes\n",
	} {
		if !strings.Contains(unit, want) {
			t.Errorf("the set-password unit lacks %q:\n%s", want, unit)
		}
	}
	if strings.Contains(unit, "ConditionFirstBoot") {
		t.Error("the unit must run while the password is unset, not on a first boot alone")
	}
	if diskFileByPath(t, files, "/usr/lib/frostroot/set-password").content != setPasswordScript {
		t.Error("the set-password program is not the script")
	}
	for _, want := range []string{"usermod -p '!unprovisioned' \"$user\"\n", "passwd -l root\n", "ln -sfn ../run/systemd/resolve/stub-resolv.conf /etc/resolv.conf\n", "printf '%s\\n' uninitialized > /etc/machine-id\n", "chmod 0600 /etc/netplan/50-frostroot.yaml\n", "ln -sfn ../frostroot-set-password.service /usr/lib/systemd/system/multi-user.target.wants/frostroot-set-password.service\n"} {
		if !strings.Contains(script, want) {
			t.Errorf("the disk script lacks %q:\n%s", want, script)
		}
	}
	for _, perMachine := range diskPerMachineFiles {
		if !strings.Contains(script, " "+perMachine+" ") {
			t.Errorf("the disk script does not remove %s", perMachine)
		}
	}
	if strings.Contains(script, "frostroot-ssh-keygen") {
		t.Error("26.04 makes its own host keys; the script links the unit anyway")
	}

	// 24.04 ships the unit that makes the SSH host keys, and links it.
	noble, nobleScript, err := renderDiskFiles(imageRecipe, diskStageOptionsFor(t, "24.04"))
	if err != nil {
		t.Fatal(err)
	}
	keygen := diskFileByPath(t, noble, "/usr/lib/systemd/system/frostroot-ssh-keygen.service").content
	if !strings.Contains(keygen, "ExecStart=/usr/bin/ssh-keygen -A\n") || !strings.Contains(keygen, "ConditionPathExists=!/etc/ssh/ssh_host_ed25519_key\n") || !strings.Contains(keygen, "Before=ssh.service\n") {
		t.Errorf("the ssh-keygen unit = %q", keygen)
	}
	if !strings.Contains(nobleScript, "ln -sfn ../frostroot-ssh-keygen.service /usr/lib/systemd/system/multi-user.target.wants/frostroot-ssh-keygen.service\n") {
		t.Errorf("24.04's disk script does not link the ssh-keygen unit:\n%s", nobleScript)
	}
}

func TestDiskScriptQuotesTheUserAndIsValidShell(t *testing.T) {
	imageRecipe := sampleDiskRecipe()
	imageRecipe.User.Name = "it's"
	release, err := distro.Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	script, err := renderDiskScript(imageRecipe, release)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, `user='it'\''s'`) {
		t.Errorf("the user is not single-quoted:\n%s", script)
	}
	syntaxCheck := exec.Command("sh", "-n")
	syntaxCheck.Stdin = strings.NewReader(script)
	if output, err := syntaxCheck.CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v: %s", err, output)
	}
	bashCheck := exec.Command("bash", "-n")
	bashCheck.Stdin = strings.NewReader(setPasswordScript)
	if output, err := bashCheck.CombinedOutput(); err != nil {
		t.Errorf("bash -n on set-password: %v: %s", err, output)
	}
	everything := script + setPasswordScript + strings.Join(diskHooks(sampleDiskStage(t)), "\n")
	for _, failureSwallower := range []string{"|| true", "|| :", "set +e"} {
		if strings.Contains(everything, failureSwallower) {
			t.Errorf("the disk's scripts and hooks must fail closed, found %q", failureSwallower)
		}
	}
}

// TestSetPasswordCheckReadsTheShadowField: the unit's condition holds while
// the field is "!unprovisioned", and not once a hash is there.
func TestSetPasswordCheckReadsTheShadowField(t *testing.T) {
	binDir := t.TempDir()
	scriptPath := filepath.Join(binDir, "set-password")
	if err := os.WriteFile(scriptPath, []byte(setPasswordScript), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, testCase := range []struct {
		field    string
		wantExit int
	}{
		{"!unprovisioned", 0},
		{"$6$salt$hash", 1},
		{"!", 1},
	} {
		fakeGetent := "#!/bin/sh\nprintf '%s:" + testCase.field + ":20000:0:99999:7:::\\n' \"$2\"\n"
		if err := os.WriteFile(filepath.Join(binDir, "getent"), []byte(fakeGetent), 0o755); err != nil {
			t.Fatal(err)
		}
		command := exec.Command("bash", scriptPath, "check", "student")
		command.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin"}
		output, err := command.CombinedOutput()
		exitCode := 0
		if err != nil {
			exitCode = command.ProcessState.ExitCode()
		}
		if exitCode != testCase.wantExit {
			t.Errorf("field %q: check exited %d (%s), want %d", testCase.field, exitCode, output, testCase.wantExit)
		}
	}
	if err := exec.Command("bash", scriptPath).Run(); err == nil {
		t.Error("set-password without arguments exited 0")
	}
}

// sampleDiskStage writes a disk build's stage into a temporary directory.
func sampleDiskStage(t *testing.T) Stage {
	t.Helper()
	stage, err := WriteStage(filepath.Join(t.TempDir(), "stage"), sampleDiskRecipe(), StageOptions{RecordForLock: true, Disk: ptr(diskStageOptionsFor(t, "24.04"))})
	if err != nil {
		t.Fatal(err)
	}
	return stage
}

func ptr[T any](value T) *T { return &value }

func TestCustomizeHooksForADisk(t *testing.T) {
	stage := sampleDiskStage(t)
	if stage.WSLConfPath != "" || stage.DiskScriptPath == "" || len(stage.DiskFiles) != 10 {
		t.Fatalf("stage = %+v, want no wsl.conf, a disk script and ten files", stage)
	}
	hooks := CustomizeHooks(stage)
	joined := strings.Join(hooks, "\n")
	if strings.Contains(joined, "wsl.conf") || strings.Contains(joined, `rm -f "$1/etc/resolv.conf"`) {
		t.Errorf("a disk's hooks upload wsl.conf or remove the resolver:\n%s", joined)
	}
	for _, file := range stage.DiskFiles {
		upload := "upload " + shellQuote(file.hostPath) + " " + file.imagePath
		if !slices.Contains(hooks, upload) {
			t.Errorf("hooks lack %q", upload)
		}
		if !slices.Contains(hooks, `mkdir -p "$1`+filepath.Dir(file.imagePath)+`"`) {
			t.Errorf("hooks make no directory for %s", file.imagePath)
		}
		if content, err := os.ReadFile(file.hostPath); err != nil || len(content) == 0 {
			t.Errorf("%s was not written: %v", file.hostPath, err)
		}
	}
	scriptHook := `chroot "$1" /bin/sh -c "$(cat ` + shellQuote(stage.DiskScriptPath) + `)" frostroot-disk`
	scriptIndex := slices.Index(hooks, scriptHook)
	if scriptIndex < 0 {
		t.Fatalf("hooks lack the disk script:\n%s", joined)
	}
	provisionIndex := slices.IndexFunc(hooks, func(hook string) bool { return strings.Contains(hook, "frostroot-provision") })
	statusIndex := slices.IndexFunc(hooks, func(hook string) bool { return strings.HasPrefix(hook, "download /var/lib/dpkg/status ") })
	if provisionIndex > scriptIndex || scriptIndex > statusIndex || statusIndex != len(hooks)-1 {
		t.Errorf("the disk script must run after the provisioning and before the status download, which is last: provision %d, disk %d, status %d of %d", provisionIndex, scriptIndex, statusIndex, len(hooks))
	}
	for _, hook := range hooks[:scriptIndex] {
		if strings.HasPrefix(hook, "upload ") && strings.Contains(hook, "grub.cfg") && slices.Index(hooks, hook) < provisionIndex {
			t.Error("grub.cfg must land after every package's postinst, not before the provisioning")
		}
	}
}

// TestDiskBuildInstallsWhatADiskHolds: through Build, with fakes, for each
// release that builds disks, and nothing of it for a WSL recipe.
func TestDiskBuildInstallsWhatADiskHolds(t *testing.T) {
	for _, version := range []string{"24.04", "26.04"} {
		t.Run(version, func(t *testing.T) {
			options, _ := newTestOptions(t)
			imageRecipe := sampleDiskRecipe()
			imageRecipe.Image.Release = version
			bootstrapper := &fakeBootstrapper{}
			result, err := (&Builder{Bootstrapper: bootstrapper, Tools: &fakeToolRunner{}}).Build(context.Background(), imageRecipe, options)
			if err != nil {
				t.Fatalf("Build = %v", err)
			}
			release, err := distro.Lookup(version, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			spec := bootstrapper.lastSpec
			for _, want := range release.DiskPackages {
				if !slices.Contains(spec.Include, want) {
					t.Errorf("Include %q lacks %s", spec.Include, want)
				}
			}
			if slices.Contains(spec.Include, "systemd-repart") != (version == "26.04") {
				t.Errorf("%s: systemd-repart among %q", version, spec.Include)
			}
			if !spec.SkipReproducibleCleanup || filepath.Base(spec.TarballPath) != "image.tar" {
				t.Errorf("spec = skip %v, tarball %s; want the cleanup skipped and an uncompressed tarball", spec.SkipReproducibleCleanup, spec.TarballPath)
			}
			hooks := strings.Join(spec.CustomizeHooks, "\n")
			if !strings.Contains(hooks, " /boot/grub/grub.cfg\n") || !strings.Contains(hooks, "frostroot-disk") || strings.Contains(hooks, "wsl.conf") {
				t.Errorf("hooks:\n%s", hooks)
			}
			if strings.Contains(hooks, "frostroot-ssh-keygen.service") != (version == "24.04") {
				t.Errorf("%s: the ssh-keygen unit in the hooks:\n%s", version, hooks)
			}
			lock, err := recipe.LoadLock(result.LockPath)
			if err != nil {
				t.Fatal(err)
			}
			if lock.Disk == nil || lock.Disk.Shim != "shimx64.efi.signed.latest" || lock.Disk.ESPBytes != 512<<20 || lock.Disk.RootBytes <= 0 || lock.Disk.DiskBytes != 1<<20+lock.Disk.ESPBytes+lock.Disk.RootBytes+1<<20 {
				t.Errorf("lock [disk] = %+v", lock.Disk)
			}
			seed, err := parseUUID(lock.Disk.Seed)
			if err != nil {
				t.Fatalf("the lock's seed: %v", err)
			}
			// The fstab the image got names the file system the seed gives.
			if want := (diskLayout{seed: seed}).identifiers().rootFilesystem; !strings.Contains(bootstrapper.stagedFstab, "UUID="+want+" / ext4") {
				t.Errorf("the staged fstab %q does not mount the root the lock's seed names, %s", bootstrapper.stagedFstab, want)
			}
		})
	}
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	if _, err := (&Builder{Bootstrapper: bootstrapper, Tools: &fakeToolRunner{}}).Build(context.Background(), sampleRecipe(), options); err != nil {
		t.Fatal(err)
	}
	if spec := bootstrapper.lastSpec; spec.SkipReproducibleCleanup || filepath.Base(spec.TarballPath) != "image.tar.gz" || slices.Contains(spec.Include, "linux-image-generic") || strings.Contains(strings.Join(spec.CustomizeHooks, "\n"), "grub.cfg") {
		t.Errorf("a WSL build gained the disk's: %+v", spec)
	}
}

// TestDiskBuildWithASizeTooSmallFails: the recipe's size is checked once
// the tarball says how much the image needs, and the build fails as
// something the user can fix.
func TestDiskBuildWithASizeTooSmallFails(t *testing.T) {
	// A 900 MiB tarball needs a 1381 MiB root: more than 1G holds, less
	// than 2G.
	const tarballBytes = 900 << 20
	options, _ := newTestOptions(t)
	imageRecipe := sampleDiskRecipe()
	imageRecipe.Disk = &recipe.Disk{Size: "1G"}
	runner := &fakeToolRunner{}
	result, err := (&Builder{Bootstrapper: &fakeBootstrapper{tarballBytes: tarballBytes}, Tools: runner}).Build(context.Background(), imageRecipe, options)
	if !errors.Is(err, ErrDiskTooSmall) {
		t.Fatalf("Build = %v, want ErrDiskTooSmall", err)
	}
	if len(runner.removed) != 1 || result.WorkDir == "" {
		t.Errorf("removed %q, work directory %q; want the tree removed and the directory kept", runner.removed, result.WorkDir)
	}
	assertNoDiskBuildOutput(t, options)

	options, _ = newTestOptions(t)
	imageRecipe.Disk = &recipe.Disk{Size: "2G"}
	built, err := (&Builder{Bootstrapper: &fakeBootstrapper{tarballBytes: tarballBytes}, Tools: &fakeToolRunner{}}).Build(context.Background(), imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(built.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Disk == nil || lock.Disk.DiskBytes != 2<<30 {
		t.Errorf("lock [disk] = %+v, want the 2G asked for", lock.Disk)
	}
}
