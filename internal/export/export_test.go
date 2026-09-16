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

func TestPlaceCreatesParentAndMoves(t *testing.T) {
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if err := Place(sourcePath, destinationPath, nil); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, destinationPath, "payload")
	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the source should be gone, Stat error = %v", err)
	}
	assertDirectoryHolds(t, filepath.Dir(destinationPath), "cpp-lab-ubuntu-22.04-amd64.tar.gz")
}

func TestPlaceOverwritesExisting(t *testing.T) {
	// build overwrites a matching tarball without asking.
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	writeExistingFile(t, destinationPath, "stale")
	if err := Place(writeSourceFile(t, "fresh"), destinationPath, nil); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, destinationPath, "fresh")
}

func TestPlaceCopiesAcrossFilesystems(t *testing.T) {
	setUmask(t, 0o022)
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	writeExistingFile(t, destinationPath, "stale")

	if err := place(sourcePath, destinationPath, renameAcrossFilesystems, nil); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, destinationPath, "payload")
	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the source should be gone after a cross-filesystem move, Stat error = %v", err)
	}
	destinationInfo, err := os.Stat(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := destinationInfo.Mode().Perm(); mode != 0o644 {
		t.Fatalf("mode = %v, want 0644 so the tarball is readable for wsl --import", mode)
	}
	assertDirectoryHolds(t, filepath.Dir(destinationPath), "out.tar.gz")
}

func TestPlaceReportsCopyProgress(t *testing.T) {
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	var reports [][2]int64
	onProgress := func(copiedBytes, totalBytes int64) { reports = append(reports, [2]int64{copiedBytes, totalBytes}) }
	if err := place(sourcePath, destinationPath, renameAcrossFilesystems, onProgress); err != nil {
		t.Fatal(err)
	}
	if len(reports) == 0 {
		t.Fatal("a copy across filesystems must report its progress")
	}
	if last := reports[len(reports)-1]; last != [2]int64{int64(len("payload")), int64(len("payload"))} {
		t.Errorf("last report = %v, want the whole file copied out of its size", last)
	}
	reports = nil
	if err := place(writeSourceFile(t, "payload"), filepath.Join(t.TempDir(), "renamed"), os.Rename, onProgress); err != nil {
		t.Fatal(err)
	}
	if len(reports) != 0 {
		t.Errorf("a rename is instant and reports nothing, got %v", reports)
	}
}

func TestPlaceAcrossFilesystemsRespectsUmaskWithoutChmod(t *testing.T) {
	// dist/ is routinely on a drvfs mount of a Windows drive, where chmod
	// fails with EPERM. The copy must not depend on chmod: the temporary file
	// is created with its final mode and the umask applies, as for any file a
	// user creates.
	setUmask(t, 0o027)
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	if err := place(writeSourceFile(t, "payload"), destinationPath, renameAcrossFilesystems, nil); err != nil {
		t.Fatal(err)
	}
	destinationInfo, err := os.Stat(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if mode := destinationInfo.Mode().Perm(); mode != 0o640 {
		t.Fatalf("mode = %v, want 0644 minus umask 027", mode)
	}
}

func TestPlaceAcrossFilesystemsLeavesOtherTemporaryFilesAlone(t *testing.T) {
	distDir := filepath.Join(t.TempDir(), "dist")
	// Another build's temporary file for the same destination.
	otherBuildsTemporary := filepath.Join(distDir, ".out.tar.gz.12345.tmp")
	writeExistingFile(t, otherBuildsTemporary, "theirs")

	if err := place(writeSourceFile(t, "ours"), filepath.Join(distDir, "out.tar.gz"), renameAcrossFilesystems, nil); err != nil {
		t.Fatal(err)
	}
	assertFileContent(t, otherBuildsTemporary, "theirs")
}

func TestPlaceAcrossFilesystemsWithMissingSource(t *testing.T) {
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")
	missingSource := filepath.Join(t.TempDir(), "absent.tar.gz")
	if err := place(missingSource, destinationPath, renameAcrossFilesystems, nil); err == nil {
		t.Fatal("place succeeded, want an error for the missing source")
	}
	assertDirectoryHolds(t, filepath.Dir(destinationPath))
}

func TestPlaceDoesNotCopyOnOtherRenameErrors(t *testing.T) {
	// Only EXDEV means "try copying". Anything else is a real failure, and the
	// source must stay where it is.
	renameDenied := func(oldPath, newPath string) error {
		return &os.LinkError{Op: "rename", Old: oldPath, New: newPath, Err: syscall.EACCES}
	}
	sourcePath := writeSourceFile(t, "payload")
	destinationPath := filepath.Join(t.TempDir(), "dist", "out.tar.gz")

	err := place(sourcePath, destinationPath, renameDenied, nil)
	if !errors.Is(err, syscall.EACCES) {
		t.Fatalf("place error = %v, want EACCES", err)
	}
	if _, err := os.Stat(destinationPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the destination must not exist, Stat error = %v", err)
	}
	assertFileContent(t, sourcePath, "payload")
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
