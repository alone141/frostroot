package builder

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// pythonRecipe returns a recipe that asks for Python packages.
func pythonRecipe() recipe.Recipe {
	imageRecipe := sampleRecipe()
	imageRecipe.Python = &recipe.Python{Include: []string{"numpy", "jupyterlab"}}
	return imageRecipe
}

// renderPythonScript renders the Python script or fails the test.
func renderPythonScript(t *testing.T, imageRecipe recipe.Recipe, options PythonOptions) string {
	t.Helper()
	script, err := RenderPythonScript(imageRecipe, options)
	if err != nil {
		t.Fatal(err)
	}
	return script
}

// parseReport parses report, which stands in for pip's --report output.
func parseReport(t *testing.T, report string, requested ...string) (PythonResult, error) {
	t.Helper()
	return ParsePipReport(strings.NewReader(report), requested)
}

func TestParsePipReport(t *testing.T) {
	// The fixture is pip 24.0's own report, from the spike, cut down to
	// three of its 92 entries.
	file, err := os.Open(filepath.Join("testdata", "pip-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }() // read-only: closing cannot lose data

	result, err := ParsePipReport(file, []string{"numpy"})
	if err != nil {
		t.Fatal(err)
	}
	if result.PipVersion != "24.0" || result.Interpreter != "3.12.3" {
		t.Errorf("pip %q, interpreter %q, want 24.0 and 3.12.3", result.PipVersion, result.Interpreter)
	}
	// pip is there because the build installs the pinned resolver into the
	// same environment; the report itself names three packages.
	wantNames := []string{"numpy", "pip", "python-dateutil", "six"}
	if len(result.Wheels) != len(wantNames) {
		t.Fatalf("wheels = %d, want %d", len(result.Wheels), len(wantNames))
	}
	for index, wantName := range wantNames {
		if got := result.Wheels[index].Name; got != wantName {
			t.Errorf("wheel %d = %q, want %q (sorted by name)", index, got, wantName)
		}
	}
	numpy := result.Wheels[0]
	if numpy.Auto {
		t.Error("numpy is what the recipe asked for, so it must not be marked auto")
	}
	if pinned := result.Wheels[1]; pinned != PinnedPip {
		t.Errorf("pip entry = %+v, want the pin %+v", pinned, PinnedPip)
	}
	if !result.Wheels[2].Auto {
		t.Error("python-dateutil came in as a dependency, so it must be marked auto")
	}
	if numpy.Version != "2.5.3" {
		t.Errorf("numpy version = %q, want 2.5.3", numpy.Version)
	}
	if numpy.SHA256 != "b7e18c623bb5c95acb3b3328861272816ba199fb531921c5d6d0b675f1fde9e3" {
		t.Errorf("numpy sha256 = %q", numpy.SHA256)
	}
	wantFileName := "numpy-2.5.3-cp312-cp312-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl"
	if numpy.Filename != wantFileName {
		t.Errorf("numpy filename = %q, want %q", numpy.Filename, wantFileName)
	}
	if !strings.HasPrefix(numpy.URL, "https://files.pythonhosted.org/") {
		t.Errorf("numpy url = %q", numpy.URL)
	}
	// pip's report carries no file size, so the lock records none.
	if numpy.Size != 0 {
		t.Errorf("numpy size = %d, want 0: pip's report does not give one", numpy.Size)
	}
}

// reportWith returns a one-entry report with the given download info and
// metadata, for the refusal table.
func reportWith(downloadInfo, metadata string) string {
	return `{"version": "1", "pip_version": "24.0", "environment": {"python_full_version": "3.12.3"},
		"install": [{"requested": true, "download_info": ` + downloadInfo + `, "metadata": ` + metadata + `}]}`
}

const goodArchive = `{"url": "https://files.pythonhosted.org/packages/ab/numpy-2.5.3-cp312-cp312-manylinux_2_28_x86_64.whl",
	"archive_info": {"hashes": {"sha256": "aa"}}}`

const goodMetadata = `{"name": "numpy", "version": "2.5.3"}`

func TestParsePipReportRefusals(t *testing.T) {
	testCases := []struct {
		name        string
		report      string
		requested   []string
		wantErr     error
		wantMessage string
	}{
		{name: "not json", report: "{", wantErr: ErrBadPipReport},
		{
			name:   "unknown report version",
			report: `{"version": "2", "install": [], "environment": {}}`,
			// A pip that changed the shape must not be guessed at.
			wantErr: ErrBadPipReport, wantMessage: "version",
		},
		{
			name:    "installed nothing",
			report:  `{"version": "1", "install": [], "environment": {}}`,
			wantErr: ErrBadPipReport, wantMessage: "nothing",
		},
		{
			name:    "no checksum",
			report:  reportWith(`{"url": "https://files.pythonhosted.org/packages/ab/numpy.whl", "archive_info": {}}`, goodMetadata),
			wantErr: ErrBadPipReport, wantMessage: "sha256",
		},
		{
			name:    "source distribution",
			report:  reportWith(`{"url": "https://files.pythonhosted.org/packages/ab/numpy-2.5.3.tar.gz", "archive_info": {"hashes": {"sha256": "aa"}}}`, goodMetadata),
			wantErr: ErrNotAWheel, wantMessage: "numpy-2.5.3.tar.gz",
		},
		{
			name:    "not https",
			report:  reportWith(`{"url": "http://example.invalid/numpy.whl", "archive_info": {"hashes": {"sha256": "aa"}}}`, goodMetadata),
			wantErr: ErrBadPipReport, wantMessage: "https",
		},
		{
			name:    "no name",
			report:  reportWith(goodArchive, `{"version": "2.5.3"}`),
			wantErr: ErrBadPipReport, wantMessage: "name",
		},
		{
			name:      "a requested package is missing",
			report:    reportWith(goodArchive, goodMetadata),
			requested: []string{"numpy", "scipy"},
			wantErr:   ErrBadPipReport, wantMessage: "scipy",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := parseReport(t, testCase.report, testCase.requested...)
			if !errors.Is(err, testCase.wantErr) {
				t.Fatalf("ParsePipReport = %v, want %v", err, testCase.wantErr)
			}
			if testCase.wantMessage != "" && !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Errorf("ParsePipReport = %q, want it to mention %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestParsePipReportKeepsAResolvedPipOverThePin(t *testing.T) {
	// If the resolution decided a pip of its own, the report is the truth
	// about the image and the pin must not overwrite it.
	report := reportWith(`{"url": "https://files.pythonhosted.org/packages/ab/pip-25.0-py3-none-any.whl",
		"archive_info": {"hashes": {"sha256": "dd"}}}`, `{"name": "pip", "version": "25.0"}`)
	result, err := parseReport(t, report)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Wheels) != 1 || result.Wheels[0].Version != "25.0" {
		t.Fatalf("wheels = %+v, want only the reported pip 25.0", result.Wheels)
	}
}

func TestParsePipReportAcceptsPipsOlderSingleHash(t *testing.T) {
	report := reportWith(`{"url": "https://files.pythonhosted.org/packages/ab/numpy-2.5.3-py3-none-any.whl",
		"archive_info": {"hash": "sha256=bb"}}`, goodMetadata)
	result, err := parseReport(t, report, "numpy")
	if err != nil {
		t.Fatal(err)
	}
	if result.Wheels[0].SHA256 != "bb" {
		t.Errorf("sha256 = %q, want bb from the hash field", result.Wheels[0].SHA256)
	}
}

func TestRenderRequirements(t *testing.T) {
	lock := recipe.Lockfile{PyPI: []recipe.LockPyPI{
		{Name: "six", Version: "1.17.0", SHA256: "cc", Filename: "six.whl", URL: "https://example.invalid/six.whl", Auto: true},
		{Name: "numpy", Version: "2.5.3", SHA256: "aa", Filename: "numpy.whl", URL: "https://example.invalid/numpy.whl"},
	}}
	want := "# Generated by frostroot from frostroot.lock.\n" +
		"numpy==2.5.3 --hash=sha256:aa\n" +
		"six==1.17.0 --hash=sha256:cc\n"
	if got := RenderRequirements(lock); got != want {
		t.Errorf("RenderRequirements =\n%q\nwant\n%q", got, want)
	}
}

func TestRenderPythonScriptOnline(t *testing.T) {
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{SourceDateEpoch: 1789662022})
	for _, want := range []string{
		"python3 -m venv \"$venv\"",
		"--only-binary=:all:",
		"--report '/frostroot-pip-report.json'",
		"'numpy'",
		"'jupyterlab'",
		"SOURCE_DATE_EPOCH=1789662022",
		"export SOURCE_DATE_EPOCH",
		"/etc/profile.d/frostroot-python.sh",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "--no-index") {
		t.Errorf("an online install must not refuse the index:\n%s", script)
	}
}

func TestRenderPythonScriptOffline(t *testing.T) {
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{Offline: true, SourceDateEpoch: 1789662022})
	for _, want := range []string{
		"--no-index",
		"--find-links '/frostroot-wheels'",
		"--require-hashes",
		"--requirement '/frostroot-requirements.txt'",
		"rm -rf '/frostroot-wheels' '/frostroot-requirements.txt'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	// Offline the lock decides, so no recipe name and no resolution reach pip.
	for _, unwanted := range []string{"--report", "numpy", "jupyterlab", "--only-binary"} {
		if strings.Contains(script, unwanted) {
			t.Errorf("an offline install must not mention %q:\n%s", unwanted, script)
		}
	}
}

func TestRenderPythonScriptWithoutPythonPackages(t *testing.T) {
	script := renderPythonScript(t, sampleRecipe(), PythonOptions{})
	if script != "" {
		t.Errorf("RenderPythonScript = %q, want nothing for a recipe with no Python packages", script)
	}
}

func TestRenderPythonScriptWithoutAnEpoch(t *testing.T) {
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{})
	if strings.Contains(script, "SOURCE_DATE_EPOCH") {
		t.Errorf("without an instant to freeze at the script must not set one:\n%s", script)
	}
}

func TestParsePipList(t *testing.T) {
	installed, err := ParsePipList(strings.NewReader(`[{"name": "numpy", "version": "2.5.3"}, {"name": "pip", "version": "24.3.1"}]`))
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 2 || installed[0] != (PythonInstalled{Name: "numpy", Version: "2.5.3"}) {
		t.Fatalf("ParsePipList = %+v", installed)
	}
	if _, err := ParsePipList(strings.NewReader("not json")); err == nil {
		t.Error("ParsePipList accepted something that is not a list")
	}
}

func TestComparePythonWithLock(t *testing.T) {
	lock := recipe.Lockfile{PyPI: []recipe.LockPyPI{
		{Name: "numpy", Version: "2.5.3"},
		{Name: "pip", Version: "24.3.1"},
	}}
	asLocked := []PythonInstalled{{Name: "numpy", Version: "2.5.3"}, {Name: "pip", Version: "24.3.1"}}
	testCases := []struct {
		name        string
		installed   []PythonInstalled
		wantMessage string
	}{
		{name: "as locked", installed: asLocked},
		{
			name:      "the name spelled another way is the same package",
			installed: []PythonInstalled{{Name: "NumPy", Version: "2.5.3"}, {Name: "pip", Version: "24.3.1"}},
		},
		{
			// python3 -m venv seeds these; no lock names them.
			name:      "seeded packages are not a difference",
			installed: append(slices.Clone(asLocked), PythonInstalled{Name: "setuptools", Version: "68.1.2"}),
		},
		{
			name:        "another version",
			installed:   []PythonInstalled{{Name: "numpy", Version: "2.5.2"}, {Name: "pip", Version: "24.3.1"}},
			wantMessage: "numpy is 2.5.2 in the environment and 2.5.3 in the lock",
		},
		{
			name:        "missing",
			installed:   []PythonInstalled{{Name: "pip", Version: "24.3.1"}},
			wantMessage: "numpy 2.5.3 is in the lock but not in the environment",
		},
		{
			name:        "one the lock does not name",
			installed:   append(slices.Clone(asLocked), PythonInstalled{Name: "scipy", Version: "1.14.1"}),
			wantMessage: "scipy 1.14.1 is in the environment but not in the lock",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := ComparePythonWithLock(lock, testCase.installed)
			if testCase.wantMessage == "" {
				if err != nil {
					t.Fatalf("ComparePythonWithLock = %v, want no difference", err)
				}
				return
			}
			if !errors.Is(err, ErrImageDiffersFromLock) {
				t.Fatalf("ComparePythonWithLock = %v, want ErrImageDiffersFromLock", err)
			}
			if !strings.Contains(err.Error(), testCase.wantMessage) {
				t.Errorf("ComparePythonWithLock = %q, want it to say %q", err, testCase.wantMessage)
			}
		})
	}
}

func TestComparePythonWithLockChecksALockedSeedPackage(t *testing.T) {
	// setuptools is seeded by venv on 22.04 and 20.04 and by nothing on
	// 24.04, so a lock can perfectly well name it: pip installed that
	// version, and it is compared like any other package. Skipping it
	// outright made every offline rebuild of such a lock fail.
	lock := recipe.Lockfile{PyPI: []recipe.LockPyPI{
		{Name: "pip", Version: "24.3.1"},
		{Name: "setuptools", Version: "75.6.0"},
	}}
	asLocked := []PythonInstalled{{Name: "pip", Version: "24.3.1"}, {Name: "setuptools", Version: "75.6.0"}}
	if err := ComparePythonWithLock(lock, asLocked); err != nil {
		t.Errorf("ComparePythonWithLock = %v, want no difference: the environment is the lock", err)
	}

	drifted := []PythonInstalled{{Name: "pip", Version: "24.3.1"}, {Name: "setuptools", Version: "59.6.0"}}
	err := ComparePythonWithLock(lock, drifted)
	if !errors.Is(err, ErrImageDiffersFromLock) || !strings.Contains(err.Error(), "setuptools is 59.6.0 in the environment and 75.6.0 in the lock") {
		t.Errorf("ComparePythonWithLock = %v, want the version difference reported", err)
	}

	missing := []PythonInstalled{{Name: "pip", Version: "24.3.1"}}
	err = ComparePythonWithLock(lock, missing)
	if !errors.Is(err, ErrImageDiffersFromLock) || !strings.Contains(err.Error(), "setuptools 75.6.0 is in the lock but not in the environment") {
		t.Errorf("ComparePythonWithLock = %v, want the missing package reported", err)
	}
}

func TestLockedPip(t *testing.T) {
	lock := recipe.Lockfile{PyPI: []recipe.LockPyPI{
		{Name: "numpy", Version: "2.5.3"},
		{Name: "pip", Version: "25.0", SHA256: "dd", Filename: "pip-25.0-py3-none-any.whl", URL: "https://example.invalid/pip-25.0-py3-none-any.whl"},
	}}
	if got := LockedPip(lock); got.Version != "25.0" || got.SHA256 != "dd" {
		t.Errorf("LockedPip = %+v, want the lock's own pip", got)
	}
	if got := LockedPip(recipe.Lockfile{PyPI: []recipe.LockPyPI{{Name: "numpy", Version: "2.5.3"}}}); got.Version != "" {
		t.Errorf("LockedPip = %+v, want the zero value for a lock that names no pip", got)
	}
}

func TestRenderPythonScriptOfflineInstallsTheLocksPip(t *testing.T) {
	// The pin is a constant of this frostroot; the pool holds whatever the
	// online build resolved. An offline rebuild must ask for the lock's pip,
	// or --require-hashes --no-index cannot satisfy it.
	lockedPip := recipe.LockPyPI{Name: "pip", Version: "25.0", SHA256: "dd", Filename: "pip-25.0-py3-none-any.whl"}
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{Offline: true, Pip: lockedPip})
	if !strings.Contains(script, "'pip==%s --hash=sha256:%s\\n' '25.0' 'dd'") {
		t.Errorf("script does not install the lock's pip:\n%s", script)
	}
	if strings.Contains(script, PinnedPip.Version) {
		t.Errorf("script mentions this frostroot's pin instead of the lock's pip:\n%s", script)
	}
	if !strings.Contains(script, "wantPip='25.0'") {
		t.Errorf("script checks for the wrong pip:\n%s", script)
	}

	// A lock that names no pip has none to install: the environment's own
	// does the work, and there is nothing to check.
	withoutPip := renderPythonScript(t, pythonRecipe(), PythonOptions{Offline: true})
	for _, unwanted := range []string{"--hash=sha256:", PythonPinPath, "did not replace this release's pip"} {
		if strings.Contains(withoutPip, unwanted) {
			t.Errorf("a lock with no pip must leave out the pin step, but the script has %q:\n%s", unwanted, withoutPip)
		}
	}
	if !strings.Contains(withoutPip, "--require-hashes --requirement '"+PythonRequirementsPath+"'") {
		t.Errorf("the packages are still installed from the lock:\n%s", withoutPip)
	}
}

func TestRenderPythonScriptQuotesAPipVersionFromTheLock(t *testing.T) {
	// The lock is written by frostroot, but it is also a text file anyone can
	// edit, and offline the pin's version comes from it.
	hostile := recipe.LockPyPI{Name: "pip", Version: `24.3.1) touch /pwned;; *`, SHA256: "dd", Filename: "pip.whl"}
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{Offline: true, Pip: hostile})
	if strings.Contains(script, "touch /pwned;; *)") {
		t.Errorf("the lock's version reached the script as shell:\n%s", script)
	}
	syntaxCheck := exec.Command("sh", "-n")
	syntaxCheck.Stdin = strings.NewReader(script)
	if output, err := syntaxCheck.CombinedOutput(); err != nil {
		t.Errorf("sh -n: %v: %s", err, output)
	}

	// Run the version check against a pip that answers with something else:
	// the hostile version must be compared, not executed.
	fakeVenv := t.TempDir()
	if err := os.MkdirAll(filepath.Join(fakeVenv, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	check := strings.ReplaceAll(`venv='VENV'
wantPip='24.3.1) touch /pwned;; *'
gotPip=$("$venv"/bin/python -m pip --version | cut -d' ' -f2)
if [ "$gotPip" != "$wantPip" ]; then
	echo "refused"
fi`, "VENV", fakeVenv)
	fakePython := filepath.Join(fakeVenv, "bin", "python")
	if err := os.WriteFile(fakePython, []byte("#!/bin/sh\necho 'pip 24.3.1 from /x (python 3.12)'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command("sh", "-c", check).CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if !strings.Contains(string(output), "refused") {
		t.Errorf("the check did not refuse a pip that is not the lock's: %s", output)
	}
	if _, err := os.Stat("/pwned"); err == nil {
		t.Fatal("the lock's version was executed as shell")
	}
}

func TestComparePythonWithLockExcusesTheEnvironmentsOwnPip(t *testing.T) {
	// A lock that names no pip is rebuilt by the environment's own, seeded by
	// ensurepip: it is a package nothing asked for, not a difference.
	lock := recipe.Lockfile{PyPI: []recipe.LockPyPI{{Name: "numpy", Version: "2.5.3"}}}
	installed := []PythonInstalled{{Name: "numpy", Version: "2.5.3"}, {Name: "pip", Version: "24.0"}}
	if err := ComparePythonWithLock(lock, installed); err != nil {
		t.Errorf("ComparePythonWithLock = %v, want no difference for the seeded pip", err)
	}

	// A lock that does name pip still compares it.
	locked := recipe.Lockfile{PyPI: []recipe.LockPyPI{{Name: "pip", Version: "24.3.1"}}}
	err := ComparePythonWithLock(locked, []PythonInstalled{{Name: "pip", Version: "24.0"}})
	if !errors.Is(err, ErrImageDiffersFromLock) || !strings.Contains(err.Error(), "pip is 24.0 in the environment and 24.3.1 in the lock") {
		t.Errorf("ComparePythonWithLock = %v, want the locked pip compared", err)
	}
}

func TestRenderPythonScriptIgnoresThePipEnvironmentOfTheBuildHost(t *testing.T) {
	// A customize hook inherits whoever started the build: one PIP_INDEX_URL
	// would resolve the recipe against another index and write its URLs into
	// the lock.
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{})
	for _, want := range []string{
		"unset \"$pipVariable\"", "PIP_CONFIG_FILE=/dev/null", "HOME=/root",
		// PIP_ does not cover what pip trusts: its vendored requests reads
		// REQUESTS_CA_BUNDLE and CURL_CA_BUNDLE, and Python's ssl reads
		// SSL_CERT_FILE and SSL_CERT_DIR. All four name build-host paths.
		"unset REQUESTS_CA_BUNDLE CURL_CA_BUNDLE SSL_CERT_FILE SSL_CERT_DIR",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("script lacks %q:\n%s", want, script)
		}
	}
	// Whatever the recipe trusts reaches pip as a flag on the install and
	// never as one of those variables, so nothing sets them back.
	withTrust := renderPythonScript(t, pythonRecipe(), PythonOptions{TrustImageCertificates: true, ExtraTrust: true})
	for _, unwanted := range []string{"REQUESTS_CA_BUNDLE=", "CURL_CA_BUNDLE=", "SSL_CERT_FILE=", "SSL_CERT_DIR="} {
		if strings.Contains(withTrust, unwanted) {
			t.Errorf("the script sets %q, where trust belongs to --cert:\n%s", unwanted, withTrust)
		}
	}
	if !strings.Contains(withTrust, `--cert "$pipCert"`) {
		t.Errorf("the recipe's authorities do not reach pip:\n%s", withTrust)
	}
}

func TestRenderPythonScriptUpgradesWhatTheRecipeAsksFor(t *testing.T) {
	// Without --upgrade, pip calls a package the environment already seeds
	// satisfied, leaves it out of the report, and the build fails on a
	// package it did install.
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{})
	if !strings.Contains(script, "--only-binary=:all: --upgrade --report") {
		t.Errorf("the online install does not upgrade what the recipe asks for:\n%s", script)
	}
}

func TestPythonEnvironmentIsNeutralInARealShell(t *testing.T) {
	// The sweeps are shell, not Go: prove they clear the variables and
	// survive an environment that has none.
	script := renderPythonScript(t, pythonRecipe(), PythonOptions{})
	sweep, _, found := strings.Cut(script, "venv=")
	if !found {
		t.Fatalf("the script has no venv assignment:\n%s", script)
	}
	check := sweep + "\nenv | grep -E '^(PIP_|PYTHON|REQUESTS_CA_BUNDLE|CURL_CA_BUNDLE|SSL_CERT_)' || echo 'nothing of the host left'\n"
	command := exec.Command("sh", "-c", check)
	command.Env = append(os.Environ(),
		"PIP_INDEX_URL=https://nexus.invalid/simple",
		"PIP_EXTRA_INDEX_URL=https://other.invalid/simple",
		"PIP_TRUSTED_HOST=nexus.invalid",
		// What Python itself reads: a cache prefix would put a directory
		// named after the build host in the image, in place of the caches
		// the script compiles into the environment, and a seed of the
		// host's would order set constants its way.
		"PYTHONPYCACHEPREFIX=/home/builder/.cache/host-bytecode",
		"PYTHONDONTWRITEBYTECODE=1",
		"PYTHONHASHSEED=42",
		// The build host's own trust, which pip reads by four other names.
		"REQUESTS_CA_BUNDLE=/etc/ssl/certs/host.pem",
		"CURL_CA_BUNDLE=/etc/ssl/certs/host.pem",
		"SSL_CERT_FILE=/etc/ssl/certs/host.pem",
		"SSL_CERT_DIR=/etc/ssl/certs",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, output)
	}
	if strings.Contains(string(output), "nexus.invalid") {
		t.Errorf("the build host's pip settings survived the script:\n%s", output)
	}
	if strings.Contains(string(output), "host.pem") || strings.Contains(string(output), "SSL_CERT_DIR") {
		t.Errorf("the build host's certificate paths survived the script:\n%s", output)
	}
	if !strings.Contains(string(output), "PIP_CONFIG_FILE=/dev/null") {
		t.Errorf("the script did not disable pip's configuration files:\n%s", output)
	}
	if strings.Contains(string(output), "host-bytecode") || strings.Contains(string(output), "PYTHONDONTWRITEBYTECODE") {
		t.Errorf("the build host's Python settings survived the script:\n%s", output)
	}
	if !strings.Contains(string(output), "PYTHONHASHSEED=0\n") {
		t.Errorf("the script's own seed did not survive its sweep:\n%s", output)
	}
}

func TestPythonHooksOnline(t *testing.T) {
	stage, err := WriteStage(t.TempDir(), pythonRecipe(), StageOptions{
		RecordForLock: true,
		Python:        PythonOptions{SourceDateEpoch: 1789662022},
	})
	if err != nil {
		t.Fatal(err)
	}
	hooks := strings.Join(CustomizeHooks(stage), "\n")
	provisionIndex := strings.Index(hooks, "frostroot-provision")
	pythonIndex := strings.Index(hooks, "frostroot-python")
	downloadIndex := strings.Index(hooks, "download "+PythonReportPath)
	if provisionIndex < 0 || pythonIndex < provisionIndex {
		t.Errorf("the Python step must run after provisioning:\n%s", hooks)
	}
	if downloadIndex < pythonIndex {
		t.Errorf("the report is downloaded after the step that writes it:\n%s", hooks)
	}
	if !strings.Contains(hooks, `rm -f "$1`+PythonReportPath+`"`) {
		t.Errorf("the report must not stay in the image:\n%s", hooks)
	}
	for _, unwanted := range []string{"copy-in", PythonRequirementsPath} {
		if strings.Contains(hooks, unwanted) {
			t.Errorf("an online build has no vendored wheels to bring in, but hooks mention %q:\n%s", unwanted, hooks)
		}
	}
}

func TestPythonHooksOffline(t *testing.T) {
	wheelsDir := filepath.Join(t.TempDir(), PythonWheelsDirName)
	stage, err := WriteStage(t.TempDir(), pythonRecipe(), StageOptions{
		Python:       PythonOptions{Offline: true, SourceDateEpoch: 1789662022},
		Requirements: "numpy==2.5.3 --hash=sha256:aa\n",
		WheelsDir:    wheelsDir,
	})
	if err != nil {
		t.Fatal(err)
	}
	requirements, err := os.ReadFile(stage.RequirementsPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(requirements) != "numpy==2.5.3 --hash=sha256:aa\n" {
		t.Errorf("staged requirements = %q", requirements)
	}
	hooks := CustomizeHooks(stage)
	joined := strings.Join(hooks, "\n")
	uploadIndex := strings.Index(joined, "upload "+shellQuote(stage.RequirementsPath))
	copyIndex := strings.Index(joined, "copy-in "+shellQuote(wheelsDir)+" /")
	pythonIndex := strings.Index(joined, "frostroot-python")
	listIndex := strings.Index(joined, "download "+PythonListPath)
	if uploadIndex < 0 || copyIndex < 0 || pythonIndex < 0 || listIndex < 0 {
		t.Fatalf("hooks lack one of upload, copy-in, the script or the list download:\n%s", joined)
	}
	if uploadIndex > pythonIndex || copyIndex > pythonIndex || listIndex < pythonIndex {
		t.Errorf("hooks are out of order:\n%s", joined)
	}
	if strings.Contains(joined, "download "+PythonReportPath) {
		t.Errorf("an offline build resolves nothing, so it writes no report:\n%s", joined)
	}
}

func TestStageWithoutPythonHasNoPythonStep(t *testing.T) {
	stage, err := WriteStage(t.TempDir(), sampleRecipe(), StageOptions{RecordForLock: true})
	if err != nil {
		t.Fatal(err)
	}
	if stage.PythonScriptPath != "" || stage.PipReportPath != "" {
		t.Errorf("stage = %+v, want no Python paths for a recipe without Python packages", stage)
	}
	if hooks := strings.Join(CustomizeHooks(stage), "\n"); strings.Contains(hooks, "frostroot-python") {
		t.Errorf("hooks mention the Python step:\n%s", hooks)
	}
}

func TestRenderPythonScriptIsValidShell(t *testing.T) {
	for _, options := range []PythonOptions{
		{},
		{SourceDateEpoch: 1789662022},
		{Offline: true, SourceDateEpoch: 1789662022},
		{TrustImageCertificates: true},
		{ExtraTrust: true},
		{TrustImageCertificates: true, ExtraTrust: true, SourceDateEpoch: 1789662022},
	} {
		imageRecipe := pythonRecipe()
		// Validation rejects a name like this; the quoting is the second
		// line of defense, and it is proved with a real shell.
		imageRecipe.Python = &recipe.Python{Include: []string{"numpy", "x'; touch /tmp/pwned #"}}
		syntaxCheck := exec.Command("sh", "-n")
		syntaxCheck.Stdin = strings.NewReader(renderPythonScript(t, imageRecipe, options))
		if output, err := syntaxCheck.CombinedOutput(); err != nil {
			t.Errorf("sh -n with %+v: %v: %s", options, err, output)
		}
	}
}

// TestRenderPythonScriptWithAnInternalIndex: the networks that inspect TLS
// are very often the ones that block pypi.org and mandate an internal
// mirror. The resolve goes through it, and so does the pinned pip, whose
// direct files.pythonhosted.org URL is exactly what such a network blocks.
func TestRenderPythonScriptWithAnInternalIndex(t *testing.T) {
	const index = "https://nexus.example.com/repository/pypi/simple"
	imageRecipe := pythonRecipe()
	imageRecipe.Python.IndexURL = index
	script := renderPythonScript(t, imageRecipe, PythonOptions{})

	for _, want := range []string{
		"--index-url '" + index + "'",
		// pip by version, not by URL: the direct URL is what is blocked.
		"printf 'pip==%s --hash=sha256:%s\\n' '" + PinnedPip.Version + "'",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("the script lacks %q:\n%s", want, script)
		}
	}
	// The hash still decides which file is accepted.
	if !strings.Contains(script, PinnedPip.SHA256) {
		t.Error("the pinned pip lost its hash, so any wheel the index offers would do")
	}
	if strings.Contains(script, PinnedPip.URL) {
		t.Error("the direct pypi URL is still there, which the network blocks")
	}
	// Without an index the direct URL is still how pip is pinned.
	plain := renderPythonScript(t, pythonRecipe(), PythonOptions{})
	if !strings.Contains(plain, PinnedPip.URL) || strings.Contains(plain, "--index-url") {
		t.Error("a recipe naming no index must resolve from PyPI exactly as before")
	}
}

// pinReportJSON is pip's report for the pinned resolver's own install, as an
// index other than PyPI answers it.
const pinReportJSON = `{"version":"1","pip_version":"24.3.1","install":[{"requested":true,
"metadata":{"name":"pip","version":"24.3.1"},
"download_info":{"url":"https://nexus.example.com/repository/pypi/pip-24.3.1-py3-none-any.whl",
"archive_info":{"hashes":{"sha256":"3790624780082365f47549d032f3770eeb2b1e8bd1f7b2e02dace1afa361b4ed"}}}}]}`

// TestPinnedPipIsRecordedFromTheIndexThatServedIt: with an index named by the
// recipe, pip comes from there, but the main resolve leaves an
// already-satisfied pin out of its report — so without the pinned step's own
// report the lock would name files.pythonhosted.org, the one host such a
// network blocks, and vendor would fail on the file the build had installed.
func TestPinnedPipIsRecordedFromTheIndexThatServedIt(t *testing.T) {
	pin, err := ParsePinReport(strings.NewReader(pinReportJSON))
	if err != nil {
		t.Fatal(err)
	}
	if pin.Name != "pip" || pin.Version != "24.3.1" {
		t.Fatalf("pin = %+v", pin)
	}
	if pin.URL != "https://nexus.example.com/repository/pypi/pip-24.3.1-py3-none-any.whl" {
		t.Errorf("url = %q, want the index's", pin.URL)
	}
	if pin.SHA256 != PinnedPip.SHA256 {
		t.Errorf("sha256 = %q, want the pinned one: the checksum still decides which file is accepted", pin.SHA256)
	}
	if !pin.Auto {
		t.Error("the resolver is nobody's request, so it is recorded as automatic")
	}

	// It replaces the entry withPinnedPip added from the constant, rather
	// than being added beside it.
	wheels := withPinnedPip([]recipe.LockPyPI{{Name: "requests", Version: "2.32.3"}})
	replaced := withResolvedPin(wheels, pin)
	pipEntries := 0
	for _, wheel := range replaced {
		if wheel.Name == "pip" {
			pipEntries++
			if wheel.URL != pin.URL {
				t.Errorf("pip url = %q, want the index's", wheel.URL)
			}
		}
	}
	if pipEntries != 1 {
		t.Errorf("%d pip entries, want exactly one", pipEntries)
	}
}

func TestParsePinReportRefusesAReportWithoutPip(t *testing.T) {
	noPip := `{"version":"1","pip_version":"24.3.1","install":[{"metadata":{"name":"requests","version":"2.32.3"},
"download_info":{"url":"https://x/requests.whl","archive_info":{"hashes":{"sha256":"aa"}}}}]}`
	if _, err := ParsePinReport(strings.NewReader(noPip)); err == nil {
		t.Error("a pin report that installed no pip must be refused")
	}
	if _, err := ParsePinReport(strings.NewReader(`{"version":"9"}`)); err == nil {
		t.Error("an unknown report version must be refused")
	}
}
