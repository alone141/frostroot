package cli

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
)

// fakeUbuntuRoot builds the least an Ubuntu root needs for capture: a
// release, a hostname, a user, and a dpkg status with two packages asked for.
func fakeUbuntuRoot(t *testing.T, release string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"etc/os-release":      "ID=ubuntu\nVERSION_ID=\"" + release + "\"\n",
		"etc/hostname":        "lab-pc\n",
		"etc/passwd":          "root:x:0:0::/root:/bin/bash\nmelik:x:1000:1000::/home/melik:/bin/bash\n",
		"etc/group":           "sudo:x:27:melik\nmelik:x:1000:\n",
		"etc/timezone":        "Europe/Istanbul\n",
		"home/melik/.bashrc":  "",
		"home/melik/.ssh/key": "",
		"var/lib/dpkg/status": "Package: dpkg\nStatus: install ok installed\nPriority: required\nArchitecture: amd64\n\n" +
			"Package: git\nStatus: install ok installed\nPriority: optional\nArchitecture: amd64\n\n" +
			"Package: mytool\nStatus: install ok installed\nPriority: optional\nArchitecture: amd64\n\n",
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// runCaptureWithAnswers runs `frostroot capture --root root` in recipeDir with
// scripted answers.
func runCaptureWithAnswers(recipeDir, root string, answers []string, args ...string) (exitCode int, stdout, stderr string) {
	var stdoutBuffer, stderrBuffer bytes.Buffer
	app := App{Stdout: &stdoutBuffer, Stderr: &stderrBuffer, RecipeDir: recipeDir, Prompt: &scriptedPrompt{answers: answers}, ReadFile: noHostFile}
	exitCode = app.Run(append([]string{"capture", "--root", root}, args...))
	return exitCode, stdoutBuffer.String(), stderrBuffer.String()
}

func TestCaptureCarriesSourcesWithTheirKeys(t *testing.T) {
	root := fakeUbuntuRoot(t, "24.04")
	dockerKey := dockerKeyFixture(t)
	for relative, content := range map[string][]byte{
		"etc/apt/sources.list.d/docker.list": []byte("deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable\n"),
		"etc/apt/keyrings/docker.asc":        dockerKey,
		"etc/apt/sources.list.d/nokey.list":  []byte("deb https://nokey.example/ubuntu noble main\n"),
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	recipeDir := t.TempDir()
	exitCode, stdout, stderr := runCaptureWithAnswers(recipeDir, root, nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if len(imageRecipe.Sources) != 1 || imageRecipe.Sources[0].Name != "docker" || imageRecipe.Sources[0].Key != "keys/docker.asc" {
		t.Fatalf("Sources = %+v", imageRecipe.Sources)
	}
	written, err := os.ReadFile(filepath.Join(recipeDir, "keys", "docker.asc"))
	if err != nil {
		t.Fatal(err)
	}
	if key, err := pgp.ParsePublicKey(written); err != nil || key.Fingerprint != "9DC858229FC7DD38854AE2D88D81803C0EBFCD88" {
		t.Errorf("written key: %v, %+v", err, key)
	}
	for _, wantText := range []string{"1 third-party sources with keys", "Saved the signing key of docker from the machine into keys/docker.asc"} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout)
		}
	}
	if !regexp.MustCompile(`Third-party apt sources\s+1\n`).MatchString(stdout) {
		t.Errorf("the summary should count the source not carried:\n%s", stdout)
	}
	if exitCode, _, validateErr := runValidateIn(recipeDir); exitCode != exitSuccess {
		t.Errorf("validate: exit %d, stderr %s", exitCode, validateErr)
	}
	report, err := os.ReadFile(filepath.Join(recipeDir, captureReportFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(report), "nokey.list (https://nokey.example/ubuntu noble): no signed-by key") || !strings.Contains(string(report), `source "docker" (Docker) from /etc/apt/sources.list.d/docker.list, key /etc/apt/keyrings/docker.asc`) {
		t.Errorf("report:\n%s", report)
	}
}

func TestCaptureWritesRecipeAndReport(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, stdout, stderr := runCaptureWithAnswers(recipeDir, fakeUbuntuRoot(t, "22.04"), nil)
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	imageRecipe := loadWrittenRecipe(t, recipeDir)
	if imageRecipe.Image.Name != "lab-pc" || imageRecipe.Image.Release != "22.04" || imageRecipe.User.Name != "melik" || !imageRecipe.User.Sudo || imageRecipe.Locale.Timezone != "Europe/Istanbul" {
		t.Errorf("recipe = %+v, want the captured machine", imageRecipe)
	}
	if want := []string{"git", "mytool"}; !slices.Equal(imageRecipe.Packages.Include, want) {
		t.Errorf("packages = %q, want %q", imageRecipe.Packages.Include, want)
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("captured recipe does not validate: %v", problems)
	}
	report, err := os.ReadFile(filepath.Join(recipeDir, captureReportFileName))
	if err != nil {
		t.Fatal(err)
	}
	for _, wantText := range []string{"# frostroot capture report", "user \"melik\" from /etc/wsl.conf", ".ssh (secrets: never copy)"} {
		if wantText == "user \"melik\" from /etc/wsl.conf" {
			wantText = "user \"melik\": the first account" // no wsl.conf in this fixture
		}
		if !strings.Contains(string(report), wantText) {
			t.Errorf("report lacks %q:\n%s", wantText, report)
		}
	}
	for _, wantText := range []string{"Read ", "Ubuntu 22.04, 3 packages installed, 2 asked for", "Not captured (details in frostroot-capture.md)", "The user's home directory"} {
		if !strings.Contains(stdout, wantText) {
			t.Errorf("stdout lacks %q:\n%s", wantText, stdout)
		}
	}
}

func TestCaptureRefusesUnsupportedRelease(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, _, stderr := runCaptureWithAnswers(recipeDir, fakeUbuntuRoot(t, "23.10"), nil)
	if exitCode != exitUserError || !strings.Contains(stderr, "unsupported Ubuntu release") || !strings.Contains(stderr, "23.10") {
		t.Errorf("exit code = %d, stderr %q; want a refusal naming the release", exitCode, stderr)
	}
	if entries, _ := os.ReadDir(recipeDir); len(entries) != 0 {
		t.Errorf("nothing may be written, found %v", entries)
	}
}

func TestCaptureRefusesToOverwriteWithoutForce(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	root := fakeUbuntuRoot(t, "24.04")
	if exitCode, _, stderr := runCaptureWithAnswers(recipeDir, root, nil); exitCode != exitUserError || !strings.Contains(stderr, "--force") {
		t.Errorf("exit code = %d, stderr %q; want a refusal mentioning --force", exitCode, stderr)
	}
	if exitCode, _, stderr := runCaptureWithAnswers(recipeDir, root, nil, "--force"); exitCode != exitSuccess {
		t.Errorf("--force: exit code = %d, stderr %s", exitCode, stderr)
	}
	if got := loadWrittenRecipe(t, recipeDir); got.Image.Name != "lab-pc" {
		t.Errorf("--force should have replaced the recipe, got %+v", got)
	}
}

func TestCaptureDeclinedWritesNothing(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, _, _ := runCaptureWithAnswers(recipeDir, fakeUbuntuRoot(t, "24.04"), answersWith(map[int]string{answerWrite: "n"}))
	if exitCode != exitUserError {
		t.Errorf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if entries, _ := os.ReadDir(recipeDir); len(entries) != 0 {
		t.Errorf("declining must write neither file, found %v", entries)
	}
}

// corpCertificatePEM is a certificate fixture for the capture tests.
func corpCertificatePEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "corp-root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

// withLocalAuthority adds an organisation's certificate authority to a fake
// root, where update-ca-certificates reads them from.
func withLocalAuthority(t *testing.T, root string, pemBytes []byte) {
	t.Helper()
	path := filepath.Join(root, "usr", "local", "share", "ca-certificates", "corp-root.crt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pemBytes, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestCaptureCarriesCertificateAuthorities: a machine behind a proxy that
// inspects TLS needs its authorities in the image, and they live outside
// /etc so nothing else capture reads would find them.
func TestCaptureCarriesCertificateAuthorities(t *testing.T) {
	recipeDir := t.TempDir()
	root := fakeUbuntuRoot(t, "24.04")
	authority := corpCertificatePEM(t)
	withLocalAuthority(t, root, authority)

	exitCode, stdout, stderr := runCaptureWithAnswers(recipeDir, root, answersWith(nil))
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	written, err := os.ReadFile(filepath.Join(recipeDir, "certs", "corp-root.pem"))
	if err != nil {
		t.Fatalf("the authority was not written beside the recipe: %v", err)
	}
	if !bytes.Equal(written, authority) {
		t.Error("the written certificate is not the one the machine had")
	}
	imageRecipe, err := recipe.Load(filepath.Join(recipeDir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if got := imageRecipe.CertificatePaths(); !slices.Equal(got, []string{"certs/corp-root.pem"}) {
		t.Errorf("recipe certificates = %v", got)
	}
	// The recipe it wrote must be one the other commands accept.
	if problems := recipe.Validate(imageRecipe); len(problems) > 0 {
		t.Errorf("the recipe capture wrote does not validate: %v", problems)
	}
	if problems := recipe.CheckCertificateFiles(recipeDir, imageRecipe.CertificatePaths()); len(problems) > 0 {
		t.Errorf("the certificate files capture wrote do not check out: %v", problems)
	}
	if !strings.Contains(stdout, "certs/corp-root.pem") && !strings.Contains(stderr, "certs/corp-root.pem") {
		t.Log("stdout:", stdout)
	}
}

// Declining still writes nothing, including the certificates that had to be
// on disk before the form could check them.
func TestCaptureDeclinedWritesNoCertificates(t *testing.T) {
	recipeDir := t.TempDir()
	root := fakeUbuntuRoot(t, "24.04")
	withLocalAuthority(t, root, corpCertificatePEM(t))

	exitCode, _, _ := runCaptureWithAnswers(recipeDir, root, answersWith(map[int]string{answerWrite: "n"}))
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d", exitCode, exitUserError)
	}
	if entries, _ := os.ReadDir(recipeDir); len(entries) != 0 {
		t.Errorf("declining must leave the directory as it was, found %v", entries)
	}
}

// TestCaptureLeavesAnExistingCertificateAlone: a file the person put beside
// the recipe is theirs. A capture that is declined must leave it byte for
// byte as it was, and a capture that is written must not replace it with
// the machine's bytes either; the Trust page checks what it holds.
func TestCaptureLeavesAnExistingCertificateAlone(t *testing.T) {
	recipeDir := t.TempDir()
	root := fakeUbuntuRoot(t, "24.04")
	withLocalAuthority(t, root, corpCertificatePEM(t))
	own := []byte("# the person's own file, not a certificate at all\n")
	ownPath := filepath.Join(recipeDir, "certs", "corp-root.pem")
	if err := os.MkdirAll(filepath.Dir(ownPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ownPath, own, 0o644); err != nil {
		t.Fatal(err)
	}

	for _, answer := range []string{"n", "y"} {
		exitCode, _, stderr := runCaptureWithAnswers(recipeDir, root, answersWith(map[int]string{answerWrite: answer}), "--force")
		// Written or declined, the outcome is not the point; the file is.
		t.Logf("answer %q: exit %d, stderr %q", answer, exitCode, stderr)
		after, err := os.ReadFile(ownPath)
		if err != nil {
			t.Fatalf("after answering %q the file is gone: %v", answer, err)
		}
		if !bytes.Equal(after, own) {
			t.Errorf("after answering %q the file holds the machine's bytes, not the person's", answer)
		}
	}
}

// TestCaptureKeepsCertificatesTheWrittenRecipeNames: the form writes the
// recipe and can still fail afterwards, saving a source's key. The recipe
// on disk then names the certificate files, and a certificate read off a
// machine cannot be fetched again the way a key can, so they stay.
func TestCaptureKeepsCertificatesTheWrittenRecipeNames(t *testing.T) {
	recipeDir := t.TempDir()
	root := fakeUbuntuRoot(t, "24.04")
	withLocalAuthority(t, root, corpCertificatePEM(t))
	// A source with its key, which the form saves after writing the recipe,
	// and a stray file where the keys directory would go, so that the save
	// fails and nothing else does.
	for relative, content := range map[string][]byte{
		"etc/apt/sources.list.d/docker.list": []byte("deb [arch=amd64 signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu noble stable\n"),
		"etc/apt/keyrings/docker.asc":        dockerKeyFixture(t),
	} {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(recipeDir, "keys"), []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	exitCode, _, stderr := runCaptureWithAnswers(recipeDir, root, answersWith(nil))
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d for a key that could not be saved; stderr %s", exitCode, exitUserError, stderr)
	}
	if _, err := os.Stat(filepath.Join(recipeDir, "frostroot.toml")); err != nil {
		t.Fatalf("the recipe was not written before the key failed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recipeDir, "certs", "corp-root.pem")); err != nil {
		t.Errorf("the certificate the written recipe names was removed: %v", err)
	}
	if !strings.Contains(stderr, "frostroot edit fetches them again") {
		t.Errorf("stderr does not say how to get the key:\n%s", stderr)
	}
}

// TestCaptureRemovesWhatItWroteWhenAWriteFails: a certificate that cannot be
// written ends the capture before the form, and the ones written before it
// are not left behind.
func TestCaptureRemovesWhatItWroteWhenAWriteFails(t *testing.T) {
	recipeDir := t.TempDir()
	root := fakeUbuntuRoot(t, "24.04")
	authority := corpCertificatePEM(t)
	withLocalAuthority(t, root, authority)
	// A second authority whose target cannot be created: a directory stands
	// where the file would go, and sorts after corp-root.
	second := filepath.Join(root, "usr", "local", "share", "ca-certificates", "zz-second.crt")
	if err := os.WriteFile(second, authority, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(recipeDir, "certs", "zz-second.pem"), 0o755); err != nil {
		t.Fatal(err)
	}

	exitCode, _, stderr := runCaptureWithAnswers(recipeDir, root, answersWith(nil))
	if exitCode != exitUserError {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitUserError, stderr)
	}
	if !strings.Contains(stderr, "zz-second.pem") {
		t.Errorf("stderr does not name the file that could not be written:\n%s", stderr)
	}
	if _, err := os.Stat(filepath.Join(recipeDir, "certs", "corp-root.pem")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the certificate written before the failure was left behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recipeDir, "frostroot.toml")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a capture that failed before the form wrote a recipe: %v", err)
	}
}
