package export

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

func TestTarballRelPath(t *testing.T) {
	got := TarballRelPath("cpp-lab", "22.04", "amd64")
	want := filepath.Join("dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if got != want {
		t.Fatalf("TarballRelPath = %q, want %q", got, want)
	}
}

// writeSourceFile creates a file with content in a new temporary directory.
func writeSourceFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.tar.gz")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeExistingFile creates path, and its directory, with content.
func writeExistingFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// assertFileContent fails unless the file at path holds want.
func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("%s holds %q, want %q", path, got, want)
	}
}

// assertGone fails if path still exists.
func assertGone(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s should be gone, Lstat error = %v", path, err)
	}
}

// assertDirectoryHolds fails unless directory contains exactly the named
// entries.
func assertDirectoryHolds(t *testing.T, directory string, wantNames ...string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	var gotNames []string
	for _, entry := range entries {
		gotNames = append(gotNames, entry.Name())
	}
	if !slices.Equal(gotNames, wantNames) {
		t.Fatalf("%s holds %q, want %q", directory, gotNames, wantNames)
	}
}

// assertStagedBeside fails unless stagedPath is a temporary name for
// destinationPath in its directory: hidden, named after it, ending in .tmp.
func assertStagedBeside(t *testing.T, stagedPath, destinationPath string) {
	t.Helper()
	if filepath.Dir(stagedPath) != filepath.Dir(destinationPath) {
		t.Fatalf("staged at %s, want it beside %s", stagedPath, destinationPath)
	}
	name := filepath.Base(stagedPath)
	if !strings.HasPrefix(name, "."+filepath.Base(destinationPath)+".") || !strings.HasSuffix(name, ".tmp") {
		t.Fatalf("staged name %q does not follow the pattern for %s", name, filepath.Base(destinationPath))
	}
}

// setUmask sets the process umask for the rest of the test.
func setUmask(t *testing.T, mask int) {
	t.Helper()
	previous := syscall.Umask(mask)
	t.Cleanup(func() { syscall.Umask(previous) })
}

// renameAcrossFilesystems fails the way os.Rename does when the work directory
// and dist/ are on different filesystems, the normal case under WSL.
func renameAcrossFilesystems(oldPath, newPath string) error {
	return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EXDEV}
}

func TestStageCreatesParentAndMoves(t *testing.T) {
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	stagedPath, err := Stage(sourcePath, destinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertStagedBeside(t, stagedPath, destinationPath)
	assertFileContent(t, stagedPath, "payload")
	assertGone(t, sourcePath)
	assertDirectoryHolds(t, filepath.Dir(destinationPath), filepath.Base(stagedPath))
	// The one rename that is left to the caller.
	if err := os.Rename(stagedPath, destinationPath); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, destinationPath, "payload")
}

func TestStageCopiesAcrossFilesystems(t *testing.T) {
	setUmask(t, 0o022)
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	writeExistingFile(t, destinationPath, "previous")

	stagedPath, err := stage(sourcePath, destinationPath, renameAcrossFilesystems, nil)
	if err != nil {
		t.Fatal(err)
	}
	assertStagedBeside(t, stagedPath, destinationPath)
	assertFileContent(t, stagedPath, "payload")
	// A copy leaves the source in the work directory, and the destination
	// as it was until the caller renames.
	assertFileContent(t, sourcePath, "payload")
	assertFileContent(t, destinationPath, "previous")
	stagedInfo, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := stagedInfo.Mode().Perm(); mode != 0o644 {
		t.Fatalf("mode = %v, want 0644 so the tarball is readable for wsl --import", mode)
	}
	assertDirectoryHolds(t, filepath.Dir(destinationPath), filepath.Base(stagedPath), "out.tar.gz")
}

func TestStageReportsCopyProgress(t *testing.T) {
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	var reports [][2]int64
	onProgress := func(copiedBytes, totalBytes int64) { reports = append(reports, [2]int64{copiedBytes, totalBytes}) }
	if _, err := stage(sourcePath, destinationPath, renameAcrossFilesystems, onProgress); err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 {
		t.Fatal("a copy across filesystems must report its progress")
	}
	if last := reports[len(reports)-1]; last != [2]int64{int64(len("payload")), int64(len("payload"))} {
		t.Errorf("last report = %v, want the whole file copied out of its size", last)
	}
	reports = nil
	if _, err := stage(writeSourceFile(t, "payload"), filepath.Join(t.TempDir(), "renamed"), os.Rename, onProgress); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Errorf("a rename is instant and reports nothing, got %v", reports)
	}
}

func TestStageAcrossFilesystemsRespectsUmaskWithoutChmod(t *testing.T) {
	// dist/ is routinely on a drvfs mount of a Windows drive, where chmod
	// fails with EPERM. The copy must not depend on chmod: the temporary file
	// is created with its final mode and the umask applies, as for any file a
	// user creates.
	setUmask(t, 0o027)
	stagedPath, err := stage(writeSourceFile(t, "payload"), filepath.Join(t.TempDir(), "dist", "out.tar.gz"), renameAcrossFilesystems, nil)
	if err != nil {
		t.Fatal(err)
	}
	stagedInfo, err := os.Stat(stagedPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := stagedInfo.Mode().Perm(); mode != 0o640 {
		t.Fatalf("mode = %v, want 0644 minus umask 027", mode)
	}
}

func TestStageLeavesOtherTemporaryFilesAlone(t *testing.T) {
	distDir := filepath.Join(t.TempDir(), "dist")
	// Another build's temporary file for the same destination.
	otherBuildsTemporary := filepath.Join(distDir, ".out.tar.gz.12345.tmp")
	writeExistingFile(t, otherBuildsTemporary, "theirs")

	stagedPath, err := stage(writeSourceFile(t, "ours"), filepath.Join(distDir, "out.tar.gz"), renameAcrossFilesystems, nil)
	if err != nil {
		t.Fatal(err)
	}
	if stagedPath == otherBuildsTemporary {
		t.Fatal("staged over another build's temporary file")
	}
	assertFileContent(t, otherBuildsTemporary, "theirs")
	assertFileContent(t, stagedPath, "ours")
}

func TestStageAcrossFilesystemsWithMissingSource(t *testing.T) {
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	missingSource := filepath.Join(t.TempDir(), "absent.tar.gz")
	if _, err := stage(missingSource, destinationPath, renameAcrossFilesystems, nil); err == nil {
		t.Fatal("stage succeeded, want an error for the missing source")
	}
	assertDirectoryHolds(t, filepath.Dir(destinationPath))
}

func TestStageDoesNotCopyOnOtherRenameErrors(t *testing.T) {
	// Only EXDEV means "try copying". Anything else is a real failure, the
	// source must stay where it is, and the name that was claimed goes.
	renameDenied := func(oldPath, newPath string) error {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EACCES}
	}
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")

	_, err := stage(sourcePath, destinationPath, renameDenied, nil)
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("stage error = %v, want EACCES", err)
	}
	assertDirectoryHolds(t, filepath.Dir(destinationPath))
	assertFileContent(t, sourcePath, "payload")
}

func TestUnstagePutsAMovedFileBack(t *testing.T) {
	// The builder's failure path: the lock could not be renamed into place,
	// and the image goes back to the work directory the failed build keeps.
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	stagedPath, err := Stage(sourcePath, destinationPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	Unstage(stagedPath, sourcePath)
	assertFileContent(t, sourcePath, "payload")
	assertDirectoryHolds(t, filepath.Dir(destinationPath))
}

func TestUnstageRemovesACopy(t *testing.T) {
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	stagedPath, err := stage(sourcePath, destinationPath, renameAcrossFilesystems, nil)
	if err != nil {
		t.Fatal(err)
	}
	Unstage(stagedPath, sourcePath)
	assertFileContent(t, sourcePath, "payload")
	assertDirectoryHolds(t, filepath.Dir(destinationPath))
}

func TestCreateTemp(t *testing.T) {
	directory := t.TempDir()
	first, err := CreateTemp(directory, ".frostroot.lock.*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := CreateTemp(directory, ".frostroot.lock.*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })

	if first.Name() == second.Name() {
		t.Fatalf("two temporary files share the name %s", first.Name())
	}
	for _, file := range []*os.File{first, second} {
		name := filepath.Base(file.Name())
		if !strings.HasPrefix(name, ".frostroot.lock.") || !strings.HasSuffix(name, ".tmp") {
			t.Errorf("name %q does not follow the pattern", name)
		}
	}
}
