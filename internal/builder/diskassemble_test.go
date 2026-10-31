package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// sizedFixtureLayout is the fixture layout sized by the spike's tarball.
func sizedFixtureLayout(t *testing.T) diskLayout {
	t.Helper()
	layout := fixtureLayout(t)
	if err := layout.size(1_370_000_000, ""); err != nil {
		t.Fatal(err)
	}
	return layout
}

func TestRenderSfdiskScript(t *testing.T) {
	layout := sizedFixtureLayout(t)
	ids := layout.identifiers()
	want := "label: gpt\n" +
		"label-id: " + ids.disk + "\n" +
		"unit: sectors\n" +
		"first-lba: 2048\n" +
		"sector-size: 512\n\n" +
		"start=2048, size=1048576, type=c12a7328-f81f-11d2-ba4b-00a0c93ec93b, uuid=" + ids.espPartition + ", name=\"esp\"\n" +
		"start=1050624, size=" + itoa(layout.rootBytes/512) + ", type=4f68bce3-e8cd-4db1-96e7-fbcaf984b709, uuid=" + ids.rootPartition + ", name=\"root-x86-64\", attrs=\"GUID:59\"\n"
	if got := renderSfdiskScript(layout); got != want {
		t.Errorf("sfdisk script =\n%s\nwant\n%s", got, want)
	}
	// The root ends a MiB before the disk does, where the backup table goes.
	if end := (1050624 + layout.rootBytes/512) * 512; end != layout.diskBytes-diskMiB {
		t.Errorf("the root ends at %d, the disk at %d; want a MiB between", end, layout.diskBytes)
	}
}

func itoa(value int64) string { return strconv.FormatInt(value, 10) }

func fmtInt(value int64) string { return strconv.FormatInt(value, 10) }

func TestMke2fsConfPinsTheFeatures(t *testing.T) {
	for _, want := range []string{"features = has_journal,extent,huge_file,flex_bg,metadata_csum,64bit,dir_nlink,extra_isize\n", "blocksize = 4096\n", "inode_size = 256\n", "hash_alg = half_md4\n"} {
		if !strings.Contains(mke2fsConf, want) {
			t.Errorf("mke2fs.conf lacks %q", want)
		}
	}
	for _, unwanted := range []string{"orphan_file", "metadata_csum_seed", "small =", "floppy ="} {
		if strings.Contains(mke2fsConf, unwanted) {
			t.Errorf("mke2fs.conf holds %q, which GRUB or a size-dependent type would make trouble of", unwanted)
		}
	}
}

func TestESPFilesAreSortedAndComplete(t *testing.T) {
	files := espFiles("shimx64.efi.signed.latest")
	var paths []string
	for _, file := range files {
		paths = append(paths, file.path)
	}
	if !slices.IsSorted(paths) {
		t.Errorf("the ESP's files are copied in %q, which is not sorted", paths)
	}
	want := map[string]string{
		"EFI/BOOT/BOOTX64.EFI": "esp-files/usr/lib/shim/shimx64.efi.signed.latest",
		"EFI/BOOT/grubx64.efi": "esp-files/usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed",
		"EFI/BOOT/mmx64.efi":   "esp-files/usr/lib/shim/mmx64.efi",
		"EFI/BOOT/grub.cfg":    "esp-grub.cfg",
		"EFI/ubuntu/grub.cfg":  "esp-grub.cfg",
	}
	for _, file := range files {
		if want[file.path] != file.source {
			t.Errorf("%s comes from %q, want %q", file.path, file.source, want[file.path])
		}
	}
	if len(files) != len(want) {
		t.Errorf("%d files on the ESP, want %d", len(files), len(want))
	}
}

// TestAssembleDiskRunsTheToolsInOrder: each tool with its arguments,
// relative to /work, in the order the disk is made in, every one dated by
// the instant, mke2fs under frostroot's own configuration.
func TestAssembleDiskRunsTheToolsInOrder(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "build with space")
	if err := os.Mkdir(workDir, 0o755); err != nil {
		t.Fatal(err)
	}
	layout := sizedFixtureLayout(t)
	ids := layout.identifiers()
	runner := &fakeToolRunner{}
	recorder := &recordingProgress{}
	assembly := &diskAssembly{runner: runner, treeDir: "/tree", workDir: workDir, tarball: "image.tar", layout: layout, epoch: 1758067200, progress: recorder}
	imagePath, err := assembly.assemble(context.Background())
	if err != nil {
		t.Fatalf("assemble = %v", err)
	}
	if imagePath != filepath.Join(workDir, "disk.img") {
		t.Errorf("image at %q", imagePath)
	}
	var ran []string
	for _, run := range runner.runs {
		ran = append(ran, run.Tool+" "+strings.Join(run.Args, " "))
		if run.TreeDir != "/tree" || run.WorkDir != workDir || run.SourceDateEpoch != 1758067200 {
			t.Errorf("%s ran in tree %q, work %q, instant %d", run.Tool, run.TreeDir, run.WorkDir, run.SourceDateEpoch)
		}
		if (run.Tool == "mke2fs") != (len(run.Variables) == 1 && run.Variables[0] == [2]string{"MKE2FS_CONFIG", "/work/mke2fs.conf"}) {
			t.Errorf("%s got the variables %q", run.Tool, run.Variables)
		}
	}
	want := []string{
		"truncate -s " + fmtInt(layout.rootBytes) + " root.ext4",
		"mke2fs -q -F -t ext4 -d image.tar -U " + ids.rootFilesystem + " -E nodiscard,lazy_itable_init=1,hash_seed=" + ids.rootHashSeed + " root.ext4",
		"truncate -s 536870912 esp.vfat",
		"mkfs.fat -F 32 --invariant -i " + ids.espSerial + " esp.vfat",
		"tar -xf image.tar -C esp-files ./usr/lib/shim/shimx64.efi.signed.latest ./usr/lib/shim/mmx64.efi ./usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed",
		"mmd -i esp.vfat ::/EFI ::/EFI/BOOT ::/EFI/ubuntu",
		"mcopy -i esp.vfat esp-files/usr/lib/shim/shimx64.efi.signed.latest ::/EFI/BOOT/BOOTX64.EFI",
		"mcopy -i esp.vfat esp-grub.cfg ::/EFI/BOOT/grub.cfg",
		"mcopy -i esp.vfat esp-files/usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed ::/EFI/BOOT/grubx64.efi",
		"mcopy -i esp.vfat esp-files/usr/lib/shim/mmx64.efi ::/EFI/BOOT/mmx64.efi",
		"mcopy -i esp.vfat esp-grub.cfg ::/EFI/ubuntu/grub.cfg",
		"truncate -s " + fmtInt(layout.diskBytes) + " disk.img",
		`sh -c exec sfdisk "$@" < gpt.sfdisk sh --no-reread --no-tell-kernel disk.img`,
		"dd if=esp.vfat of=disk.img bs=1M seek=1 conv=notrunc,sparse status=none",
		"dd if=root.ext4 of=disk.img bs=1M seek=513 conv=notrunc,sparse status=none",
	}
	if !slices.Equal(ran, want) {
		t.Errorf("ran:\n%s\nwant:\n%s", strings.Join(ran, "\n"), strings.Join(want, "\n"))
	}
	for name, want := range map[string]string{"mke2fs.conf": mke2fsConf, "gpt.sfdisk": renderSfdiskScript(layout), "esp-grub.cfg": renderESPGrubConfig(ids)} {
		content, err := os.ReadFile(filepath.Join(workDir, name))
		if err != nil || string(content) != want {
			t.Errorf("%s in the work directory: %v, %q", name, err, content)
		}
	}
	if info, err := os.Stat(filepath.Join(workDir, "esp-files")); err != nil || !info.IsDir() {
		t.Errorf("esp-files was not made for tar: %v", err)
	}
	var started, finished []Phase
	for _, event := range recorder.ofKind(EventPhaseStarted) {
		started = append(started, event.Phase)
	}
	for _, event := range recorder.ofKind(EventPhaseFinished) {
		finished = append(finished, event.Phase)
	}
	if !slices.Equal(started, []Phase{PhaseAssembleDisk}) || !slices.Equal(finished, []Phase{PhaseAssembleDisk}) {
		t.Errorf("phases started %v, finished %v; want the assembly alone", started, finished)
	}
	var lines []string
	for _, event := range recorder.ofKind(EventLogLine) {
		lines = append(lines, event.Line)
	}
	if !slices.Contains(lines, "$ mkfs.fat -F 32 --invariant -i "+ids.espSerial+" esp.vfat") || !slices.Contains(lines, "mkfs.fat ran") {
		t.Errorf("the log lacks the command and its output:\n%s", strings.Join(lines, "\n"))
	}
}

func TestAssembleDiskStopsAtTheFirstFailure(t *testing.T) {
	workDir := t.TempDir()
	runner := &fakeToolRunner{failOn: "mkfs.fat", runErr: errors.New("mkfs.fat: exit status 1: mkfs.fat: unable to open esp.vfat")}
	recorder := &recordingProgress{}
	assembly := &diskAssembly{runner: runner, treeDir: "/tree", workDir: workDir, tarball: "image.tar", layout: sizedFixtureLayout(t), progress: recorder}
	_, err := assembly.assemble(context.Background())
	if err == nil || !strings.Contains(err.Error(), "assembling the disk: mkfs.fat: exit status 1: mkfs.fat: unable to open esp.vfat") {
		t.Fatalf("assemble = %v, want the tool's failure", err)
	}
	if last := runner.runs[len(runner.runs)-1]; last.Tool != "mkfs.fat" || len(runner.runs) != 4 {
		t.Errorf("%d tools ran, the last %s; want the four up to mkfs.fat", len(runner.runs), last.Tool)
	}
	if len(recorder.ofKind(EventPhaseFinished)) != 0 {
		t.Error("the assembly phase finished after a failure")
	}
}

// TestDiskBuildAssemblesAndPlacesTheImage: through Build, with fakes, the
// disk is assembled after the image, the tree removed after the last tool,
// and the raw image placed in dist/ with the lock, the tarball staying a
// work file.
func TestDiskBuildAssemblesAndPlacesTheImage(t *testing.T) {
	options, _ := newTestOptions(t)
	recorder := &recordingProgress{}
	options.Progress = recorder
	bootstrapper := &fakeBootstrapper{}
	runner := &fakeToolRunner{lockPath: filepath.Join(options.RecipeDir, LockFileName)}
	result, err := (&Builder{Bootstrapper: bootstrapper, Tools: runner}).Build(context.Background(), sampleDiskRecipe(), options)
	if err != nil {
		t.Fatalf("Build = %v", err)
	}
	if result.ImagePath != expectedDiskImagePath(options) || result.TarballPath != "" {
		t.Errorf("Result = %+v, want the image placed and no tarball", result)
	}
	lock, err := recipe.LoadLock(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(result.ImagePath); err != nil || info.Size() != lock.Disk.DiskBytes {
		t.Errorf("the image: %v, %d bytes; want the lock's %d", err, info.Size(), lock.Disk.DiskBytes)
	}
	entries, err := os.ReadDir(filepath.Dir(result.ImagePath))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("dist/ holds %d entries, want the image alone", len(entries))
	}
	var tools []string
	for _, run := range runner.runs {
		tools = append(tools, run.Tool)
	}
	if tools[0] != "truncate" || tools[len(tools)-1] != "dd" || !slices.Contains(tools, "mke2fs") || !slices.Contains(tools, "sh") {
		t.Errorf("tools ran: %q", tools)
	}
	for _, run := range runner.runs {
		if run.TreeDir != bootstrapper.treeSpec.TreeDir || run.WorkDir != filepath.Dir(bootstrapper.treeSpec.WorkDir) || run.SourceDateEpoch != result.SourceDateEpoch {
			t.Errorf("%s ran in tree %q, work %q, at %d", run.Tool, run.TreeDir, run.WorkDir, run.SourceDateEpoch)
		}
		if run.Tool == "mke2fs" && !slices.Contains(run.Args, "image.tar") {
			t.Errorf("mke2fs read %q, want the uncompressed tarball", run.Args)
		}
	}
	if runner.runsAtRemoval != len(runner.runs) || runner.lockPresentAtRemoval {
		t.Error("the tree must go after the last tool and before the lock lands")
	}
	var started []Phase
	for _, event := range recorder.ofKind(EventPhaseStarted) {
		started = append(started, event.Phase)
	}
	if want := []Phase{PhaseMakeToolsTree, PhaseAssembleDisk, PhaseWriteLock, PhasePlaceImage}; !slices.Equal(started, want) {
		t.Errorf("phases started = %v, want %v", started, want)
	}
	if result.WorkDir != "" {
		t.Error("the work directory, the tarball in it, was kept after success")
	}
}

func TestDiskBuildFailsWhenTheAssemblyFails(t *testing.T) {
	options, _ := newTestOptions(t)
	runner := &fakeToolRunner{failOn: "mke2fs", runErr: errors.New("mke2fs: exit status 1: mke2fs: Could not open image.tar")}
	result, err := (&Builder{Bootstrapper: &fakeBootstrapper{}, Tools: runner}).Build(context.Background(), sampleDiskRecipe(), options)
	if err == nil || !strings.Contains(err.Error(), "assembling the disk: mke2fs: exit status 1") {
		t.Fatalf("Build = %v, want the assembly's failure", err)
	}
	if len(runner.removed) != 1 || result.WorkDir == "" {
		t.Errorf("removed %q, work directory %q; want the tree removed and the directory kept", runner.removed, result.WorkDir)
	}
	assertNoDiskBuildOutput(t, options)
}
