package cli

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// pythonRecipeText is a recipe that asks for the one package the pip report
// fixture says was requested, so that a fake build can write a lock with a
// Python side.
const pythonRecipeText = `[image]
name = "python-lab"
release = "24.04"
arch = "amd64"

[user]
name = "student"
sudo = true

[wsl]
systemd = true
default_user = "student"

[locale]
lang = "en_US.UTF-8"
timezone = "UTC"

[packages]
include = ["git"]

[python]
include = ["numpy"]
`

// pipReportFixture is pip's report from the builder's spike, cut down.
func pipReportFixture(t *testing.T) string {
	t.Helper()
	report, err := os.ReadFile(filepath.Join("..", "builder", "testdata", "pip-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	return string(report)
}

func TestBuildInsecureWarnsAndReachesTheBootstrap(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	bootstrapper := &fakeBootstrapper{}
	var stdout, stderr bytes.Buffer
	if exitCode := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr).Run([]string{"build", "--insecure"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !bootstrapper.lastSpec.Insecure {
		t.Error("the bootstrap was not told to skip verification")
	}
	for _, wantText := range []string{insecureWarning, insecureBuildDetail} {
		if !strings.Contains(stderr.String(), wantText) {
			t.Errorf("stderr lacks %q:\n%s", wantText, stderr.String())
		}
	}
	// A recipe with no [python] resolves nothing unverified, and the
	// warning does not claim otherwise.
	if strings.Contains(stderr.String(), insecurePythonDetail) {
		t.Errorf("stderr warns about Python packages the recipe does not ask for:\n%s", stderr.String())
	}
	lock, err := recipe.LoadLock(filepath.Join(recipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.PythonResolvedUnverified() {
		t.Error("a lock with no Python side says its resolve was unverified")
	}
}

func TestBuildInsecureWithPythonWarnsAndMarksTheLock(t *testing.T) {
	recipeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipeDir, "frostroot.toml"), []byte(pythonRecipeText), 0o644); err != nil {
		t.Fatal(err)
	}
	bootstrapper := &fakeBootstrapper{pipReport: pipReportFixture(t)}
	var stdout, stderr bytes.Buffer
	if exitCode := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr).Run([]string{"build", "--insecure"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), insecurePythonDetail) {
		t.Errorf("stderr does not say the Python packages are resolved unverified:\n%s", stderr.String())
	}
	lock, err := recipe.LoadLock(filepath.Join(recipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if !lock.PythonResolvedUnverified() {
		t.Errorf("the lock does not record the unverified resolve: %+v", lock.Python)
	}

	// Built again with verification, the lock says nothing: the mark
	// belongs to the resolve that made the lock, not to the recipe.
	stderr.Reset()
	if exitCode := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr).Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("verified build: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if strings.Contains(stderr.String(), "warning") {
		t.Errorf("a verified build warns:\n%s", stderr.String())
	}
	if lock, err = recipe.LoadLock(filepath.Join(recipeDir, "frostroot.lock")); err != nil || lock.PythonResolvedUnverified() {
		t.Errorf("after a verified build the lock still says unverified (%v): %+v", err, lock.Python)
	}
}

func TestBuildOfflineInsecureChangesNothing(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	if exitCode := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr).Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("online build: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	vendorFakePool(t, recipeDir)

	stderr.Reset()
	bootstrapper := &fakeBootstrapper{}
	if exitCode := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr).Run([]string{"build", "--offline", "--insecure"}); exitCode != exitSuccess {
		t.Fatalf("offline build: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if bootstrapper.lastSpec.Insecure {
		t.Error("an offline bootstrap was told to skip verification")
	}
	if !strings.Contains(stderr.String(), "note: an offline build fetches nothing, so --insecure changes nothing\n") {
		t.Errorf("stderr lacks the note:\n%s", stderr.String())
	}
	if strings.Contains(stderr.String(), "warning") {
		t.Errorf("a flag that changes nothing deserves no warning:\n%s", stderr.String())
	}
}

func TestBuildOfflineWarnsAboutAnUnverifiedLock(t *testing.T) {
	recipeDir := newRecipeDir(t, "valid.toml")
	var stdout, stderr bytes.Buffer
	if exitCode := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr).Run([]string{"build"}); exitCode != exitSuccess {
		t.Fatalf("online build: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	lock := vendorFakePool(t, recipeDir)
	// A lock edited to carry the mark and no wheels: the rebuild has no
	// Python step to run, and the warning is about the lock, not the step.
	lock.Python = &recipe.LockPython{Transport: recipe.TransportUnverified}
	if err := recipe.SaveLock(filepath.Join(recipeDir, "frostroot.lock"), lock); err != nil {
		t.Fatal(err)
	}

	stderr.Reset()
	if exitCode := newBuildApp(t, recipeDir, &fakeBootstrapper{}, &stdout, &stderr).Run([]string{"build", "--offline"}); exitCode != exitSuccess {
		t.Fatalf("offline build: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), unverifiedLockWarning) {
		t.Errorf("stderr lacks the lock's warning:\n%s", stderr.String())
	}
}

func TestVendorInsecureAcceptsAnyCertificate(t *testing.T) {
	fixture := newVendorFixture(t)
	fixture.addWheels(t, "numpy")
	var stdout, stderr bytes.Buffer
	app := newVendorApp(fixture, &stdout, &stderr)
	app.VendorClient = nil // the real trust path: the server's certificate is one no root signed
	if exitCode := app.Run([]string{"vendor", "--insecure"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	for _, wantText := range []string{insecureWarning, insecureVendorDetail} {
		if !strings.Contains(stderr.String(), wantText) {
			t.Errorf("stderr lacks %q:\n%s", wantText, stderr.String())
		}
	}
	for _, path := range []string{filepath.Join(fixture.poolDir(), "curl_1_amd64.deb"), filepath.Join(fixture.wheelPoolDir(), "numpy-1.0-py3-none-any.whl")} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was not vendored: %v", path, err)
		}
	}
	// The lock was resolved verified, so there is nothing to say about it.
	if strings.Contains(stderr.String(), unverifiedLockWarning) || strings.Contains(stdout.String(), "resolved without verifying") {
		t.Errorf("a verified lock is reported as unverified:\n%s\n%s", stderr.String(), stdout.String())
	}
}

func TestVendorReportsWhatItCheckedOfAnUnverifiedLock(t *testing.T) {
	fixture := newVendorFixture(t)
	fixture.addWheels(t, "numpy", "six")
	fixture.lock.Python.Transport = recipe.TransportUnverified
	fixture.saveLock(t)

	// A verified run downloads every wheel over a verified connection and
	// compares it with the lock, which is the check the README describes.
	var stdout, stderr bytes.Buffer
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), unverifiedLockWarning) {
		t.Errorf("stderr lacks the lock's warning:\n%s", stderr.String())
	}
	if !strings.Contains(stdout.String(), "every wheel downloaded now matched the lock over a verified connection") {
		t.Errorf("stdout does not report the check:\n%s", stdout.String())
	}

	// Run again, the wheels are already there: they were compared with the
	// lock and nothing else, and the report says so rather than vouching.
	stdout.Reset()
	stderr.Reset()
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor"}); exitCode != exitSuccess {
		t.Fatalf("second run: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stdout.String(), "0 of 2 wheels matched the lock over a verified connection, and 2 were already in vendor/wheels") {
		t.Errorf("stdout does not say what was not checked:\n%s", stdout.String())
	}

	// An insecure run of such a lock checks nothing about it and says so
	// only by warning; the check line would be a false comfort.
	stdout.Reset()
	stderr.Reset()
	if exitCode := newVendorApp(fixture, &stdout, &stderr).Run([]string{"vendor", "--insecure"}); exitCode != exitSuccess {
		t.Fatalf("insecure run: exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if !strings.Contains(stderr.String(), unverifiedLockWarning) || strings.Contains(stdout.String(), "resolved without verifying") {
		t.Errorf("an insecure run must warn and not vouch:\n%s\n%s", stderr.String(), stdout.String())
	}
}

func TestEditInsecureSaysWhereToCheckAPPAKey(t *testing.T) {
	recipeDir := newRecipeDir(t, "sources.toml")
	docker, _ := sources.Lookup("docker")
	dockerKey := dockerKeyFixture(t)
	client := &fakeKeyClient{answers: map[string][]byte{
		docker.KeyURL: dockerKey,
		"https://api.launchpad.net/1.0/~deadsnakes/+archive/ubuntu/ppa":                                []byte(`{"signing_key_fingerprint": "` + docker.Fingerprints[0] + `"}`),
		"https://keyserver.ubuntu.com/pks/lookup?op=get&options=mr&search=0x" + docker.Fingerprints[0]: dockerKey,
	}}
	exitCode, stdout, stderr := runEditWithKeyClient(recipeDir, answersWith(map[int]string{answerWrite: "y"}), client, "--insecure")
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
	}
	for _, wantText := range []string{insecureWarning, insecureFormDetail,
		"warning: the fingerprint of ppa-deadsnakes-ppa came from Launchpad over an unverified connection, and so did the key; compare it with https://launchpad.net/~deadsnakes/+archive/ubuntu/ppa before building.\n"} {
		if !strings.Contains(stderr, wantText) {
			t.Errorf("stderr lacks %q:\n%s", wantText, stderr)
		}
	}
	// Docker's fingerprint is compiled into frostroot: the key was checked
	// against it whatever the connection, and there is nothing to compare.
	if strings.Contains(stderr, "fingerprint of docker") {
		t.Errorf("a catalog key is reported as discovered:\n%s", stderr)
	}
	if !strings.Contains(stdout, "Saved the signing key of ppa-deadsnakes-ppa") {
		t.Errorf("stdout lacks the saved-key line:\n%s", stdout)
	}
}

func TestKeyClientInsecureAcceptsAnyCertificate(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte("ok"))
	}))
	t.Cleanup(server.Close)

	var stderr bytes.Buffer
	app := App{Stderr: &stderr, RecipeDir: t.TempDir(), ReadFile: noHostFile}
	app.withDefaults()
	indexes, ok := app.packageIndexes(&indexFlags{insecure: true})
	if !ok {
		t.Fatalf("packageIndexes: %s", stderr.String())
	}
	if !indexes.insecureTLS() || !indexes.options.Insecure || !indexes.pypiOptions.Insecure {
		t.Error("the indexes were not told to skip verification")
	}
	body, err := app.KeyClient.Get(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("KeyClient with --insecure refused the certificate: %v", err)
	}
	if string(body) != "ok" {
		t.Errorf("body = %q, want ok", body)
	}
	if !strings.Contains(stderr.String(), insecureWarning) {
		t.Errorf("stderr lacks the warning:\n%s", stderr.String())
	}
}
