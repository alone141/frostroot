package recipe

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// vendorSources is one source whose key the recipe keeps at keys/vendor.asc.
var vendorSources = []Source{{Name: "vendor", URL: "https://x", Key: "keys/vendor.asc"}}

// vendorCertificates is the one certificate file the recipe names.
var vendorCertificates = []string{"certs/corp.pem"}

// symlinkOrSkip makes a symbolic link, or skips the test where the system
// cannot.
func symlinkOrSkip(t *testing.T, target, link string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
}

// checkRecipeFiles runs both checks of the files a recipe names, and returns
// the problems each reported.
func checkRecipeFiles(recipeDir string) (keyProblems, certificateProblems []string) {
	return CheckSourceKeys(recipeDir, vendorSources), CheckCertificateFiles(recipeDir, vendorCertificates)
}

// wantOneProblem fails unless problems is one problem naming path as the
// recipe wrote it and saying wantText.
func wantOneProblem(t *testing.T, what string, problems []string, path, wantText string) {
	t.Helper()
	if len(problems) != 1 || !strings.Contains(problems[0], path) || !strings.Contains(problems[0], wantText) {
		t.Errorf("%s: problems = %q, want one naming %s and saying %q", what, problems, path, wantText)
	}
}

func TestRecipeFilesRefuseALinkOutOfTheRecipeDirectory(t *testing.T) {
	outside := t.TempDir()
	writeKey(t, outside, "vendor.asc")
	writeCertificate(t, filepath.Join(outside, "corp.pem"))
	recipeDir := t.TempDir()
	symlinkOrSkip(t, filepath.Join(outside, "vendor.asc"), filepath.Join(recipeDir, "keys", "vendor.asc"))
	symlinkOrSkip(t, filepath.Join(outside, "corp.pem"), filepath.Join(recipeDir, "certs", "corp.pem"))

	keyProblems, certificateProblems := checkRecipeFiles(recipeDir)
	wantOneProblem(t, "the key", keyProblems, "keys/vendor.asc", "outside the recipe directory")
	wantOneProblem(t, "the certificate", certificateProblems, "certs/corp.pem", "outside the recipe directory")
}

func TestRecipeFilesFollowALinkInsideTheRecipeDirectory(t *testing.T) {
	recipeDir := t.TempDir()
	writeKey(t, recipeDir, "shared/vendor.asc")
	writeCertificate(t, filepath.Join(recipeDir, "shared", "corp.pem"))
	symlinkOrSkip(t, filepath.Join("..", "shared", "vendor.asc"), filepath.Join(recipeDir, "keys", "vendor.asc"))
	symlinkOrSkip(t, filepath.Join("..", "shared", "corp.pem"), filepath.Join(recipeDir, "certs", "corp.pem"))

	keyProblems, certificateProblems := checkRecipeFiles(recipeDir)
	if len(keyProblems) != 0 || len(certificateProblems) != 0 {
		t.Errorf("a link inside the recipe directory was refused: %q %q", keyProblems, certificateProblems)
	}

	// A recipe directory reached through a link is read as it is.
	linkedDir := filepath.Join(t.TempDir(), "linked")
	symlinkOrSkip(t, recipeDir, linkedDir)
	keyProblems, certificateProblems = checkRecipeFiles(linkedDir)
	if len(keyProblems) != 0 || len(certificateProblems) != 0 {
		t.Errorf("a recipe directory reached through a link was refused: %q %q", keyProblems, certificateProblems)
	}
}

func TestRecipeFilesRefuseADirectory(t *testing.T) {
	recipeDir := t.TempDir()
	for _, path := range []string{"keys/vendor.asc", "certs/corp.pem"} {
		if err := os.MkdirAll(filepath.Join(recipeDir, filepath.FromSlash(path)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	keyProblems, certificateProblems := checkRecipeFiles(recipeDir)
	wantOneProblem(t, "the key", keyProblems, "keys/vendor.asc", "a directory, not a regular file")
	wantOneProblem(t, "the certificate", certificateProblems, "certs/corp.pem", "a directory, not a regular file")
}

func TestRecipeFilesRefuseAFileLargerThanTheCap(t *testing.T) {
	recipeDir := t.TempDir()
	for path, size := range map[string]int{"keys/vendor.asc": MaxKeyFileBytes + 1, "certs/corp.pem": MaxCertificateFileBytes + 1} {
		full := filepath.Join(recipeDir, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	keyProblems, certificateProblems := checkRecipeFiles(recipeDir)
	wantOneProblem(t, "the key", keyProblems, "keys/vendor.asc", "larger than 1024 KiB")
	wantOneProblem(t, "the certificate", certificateProblems, "certs/corp.pem", "larger than 4096 KiB")
}

func TestReadInsideRecipeDirectoryReadsAFileOfExactlyTheCap(t *testing.T) {
	recipeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipeDir, "file"), []byte("12345678"), 0o644); err != nil {
		t.Fatal(err)
	}
	if data, err := readInsideRecipeDirectory(recipeDir, "file", 8); err != nil || string(data) != "12345678" {
		t.Errorf("a file of the cap read as %q, %v; want all of it", data, err)
	}
	if _, err := readInsideRecipeDirectory(recipeDir, "file", 7); err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("a file a byte over the cap read with error %v, want it refused", err)
	}
}

func TestRecipeFilesNameADanglingLinkAndWhereItLeads(t *testing.T) {
	recipeDir := t.TempDir()
	nowhere := filepath.Join(t.TempDir(), "gone")
	symlinkOrSkip(t, nowhere, filepath.Join(recipeDir, "keys", "vendor.asc"))
	symlinkOrSkip(t, nowhere, filepath.Join(recipeDir, "certs"))

	keyProblems, certificateProblems := checkRecipeFiles(recipeDir)
	wantOneProblem(t, "the key", keyProblems, "keys/vendor.asc", "it is a symbolic link to "+nowhere+", which does not exist")
	wantOneProblem(t, "the certificate", certificateProblems, "certs/corp.pem", "certs is a symbolic link to "+nowhere+", which does not exist")
	if strings.Contains(strings.Join(append(keyProblems, certificateProblems...), " "), "file does not exist") {
		t.Errorf("a dangling link is said not to exist twice: %q %q", keyProblems, certificateProblems)
	}
}

func TestOpenCheckedFileRefusesAFileOtherThanTheOneChecked(t *testing.T) {
	dir := t.TempDir()
	checkedPath := filepath.Join(dir, "checked")
	swappedPath := filepath.Join(dir, "swapped")
	for _, path := range []string{checkedPath, swappedPath} {
		if err := os.WriteFile(path, []byte(path), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	checked, err := os.Stat(checkedPath)
	if err != nil {
		t.Fatal(err)
	}

	file, err := openCheckedFile(checkedPath, checked)
	if err != nil {
		t.Fatalf("the file that was checked: %v", err)
	}
	_ = file.Close() // read only

	if file, err := openCheckedFile(swappedPath, checked); err == nil || !strings.Contains(err.Error(), "changed while it was read") {
		if file != nil {
			_ = file.Close() // read only
		}
		t.Errorf("a file other than the one checked opened with error %v, want it refused", err)
	}
}

// emptyDirectory fails unless directory holds nothing.
func emptyDirectory(t *testing.T, directory string) {
	t.Helper()
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("%s holds %v, want nothing", directory, entries)
	}
}

// TestPrepareFilePathRefusesALinkOutOfTheRecipeDirectory: edit and capture
// saved a key or a certificate through a keys/ or certs/ that a cloned
// recipe made a link to anywhere, so the file landed there. Neither the file
// nor a directory on its way may be made outside.
func TestPrepareFilePathRefusesALinkOutOfTheRecipeDirectory(t *testing.T) {
	outside := t.TempDir()
	recipeDir := t.TempDir()
	symlinkOrSkip(t, outside, filepath.Join(recipeDir, "keys"))
	for _, relativePath := range []string{"keys/vendor.asc", "keys/deeper/vendor.asc"} {
		_, err := PrepareFilePath(recipeDir, relativePath)
		if err == nil || !strings.Contains(err.Error(), "outside the recipe directory") {
			t.Errorf("%s: err = %v, want a refusal", relativePath, err)
		}
	}
	emptyDirectory(t, outside)
}

func TestPrepareFilePathFollowsALinkInsideTheRecipeDirectory(t *testing.T) {
	recipeDir := t.TempDir()
	if err := os.Mkdir(filepath.Join(recipeDir, "shared"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkOrSkip(t, "shared", filepath.Join(recipeDir, "keys"))
	writePath, err := PrepareFilePath(recipeDir, "keys/deeper/vendor.asc")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(writePath, fakeKeyPacket, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadKeyFile(recipeDir, "keys/deeper/vendor.asc"); err != nil {
		t.Errorf("the file written is not the one the recipe names: %v", err)
	}

	// A recipe directory reached through a link, and a file directly in it.
	linkedDir := filepath.Join(t.TempDir(), "linked")
	symlinkOrSkip(t, recipeDir, linkedDir)
	for _, relativePath := range []string{"keys/vendor.asc", "vendor.asc", "certs/new/corp.pem"} {
		if _, err := PrepareFilePath(linkedDir, relativePath); err != nil {
			t.Errorf("%s in a linked recipe directory: %v", relativePath, err)
		}
	}
	if info, err := os.Stat(filepath.Join(recipeDir, "certs", "new")); err != nil || !info.IsDir() {
		t.Errorf("the directories on the way were not made: %v", err)
	}
}

func TestPrepareFilePathNamesADanglingLink(t *testing.T) {
	recipeDir := t.TempDir()
	symlinkOrSkip(t, "gone", filepath.Join(recipeDir, "keys"))
	_, err := PrepareFilePath(recipeDir, "keys/vendor.asc")
	if err == nil || !strings.Contains(err.Error(), "keys is a symbolic link to gone, which does not exist") {
		t.Errorf("err = %v", err)
	}
}
