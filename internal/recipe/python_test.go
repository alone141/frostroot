package recipe

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadRecipeWithPython(t *testing.T) {
	imageRecipe, err := Load(testdataPath("python.toml"))
	if err != nil {
		t.Fatal(err)
	}
	wantPackages := []string{"numpy", "pandas", "jupyterlab"}
	if !reflect.DeepEqual(imageRecipe.PythonPackages(), wantPackages) {
		t.Fatalf("PythonPackages() = %q, want %q", imageRecipe.PythonPackages(), wantPackages)
	}
	if problems := Validate(imageRecipe); len(problems) != 0 {
		t.Fatalf("Validate = %q, want no problems", problems)
	}

	// Round trip through Save keeps the table.
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := Save(path, imageRecipe); err != nil {
		t.Fatal(err)
	}
	reloaded, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, imageRecipe) {
		t.Fatalf("round trip =\n%+v\nwant\n%+v", reloaded, imageRecipe)
	}
}

func TestRecipeWithoutPythonAsksForNothing(t *testing.T) {
	imageRecipe := loadValidRecipe(t)
	if imageRecipe.Python != nil {
		t.Errorf("Python = %+v, want nil for a recipe without the table", imageRecipe.Python)
	}
	if packages := imageRecipe.PythonPackages(); packages != nil {
		t.Errorf("PythonPackages() = %q, want nil", packages)
	}
	if problems := Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("Validate = %q, want no problems", problems)
	}
}

func TestSaveRecipeWithoutPythonOmitsTheTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "frostroot.toml")
	if err := Save(path, loadValidRecipe(t)); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), "python") {
		t.Errorf("a recipe without python packages must not mention them:\n%s", content)
	}
}

func TestValidateAcceptsPyPINames(t *testing.T) {
	imageRecipe := loadValidRecipe(t)
	imageRecipe.Python = &Python{Include: []string{
		"numpy", "jupyterlab", "python-dateutil", "zope.interface",
		"Flask-SQLAlchemy", "typing_extensions", "ruamel.yaml.clib", "a",
	}}
	if problems := Validate(imageRecipe); len(problems) != 0 {
		t.Fatalf("Validate = %q, want no problems", problems)
	}
}

func TestValidateRejectsWhatIsNotAPyPIName(t *testing.T) {
	testCases := []struct {
		name        string
		packageName string
	}{
		{"version specifier", "numpy==2.1.3"},
		{"version range", "numpy>=2"},
		{"extra", "requests[socks]"},
		{"vcs url", "git+https://example.com/pkg.git"},
		{"local path", "./wheels/numpy.whl"},
		{"shell", "numpy; rm -rf /"},
		{"space", "two names"},
		{"empty", ""},
		{"leading dash", "-numpy"},
		{"trailing dot", "numpy."},
		{"path traversal", "../etc/passwd"},
		{"too long", strings.Repeat("a", maxPythonNameLength+1)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			imageRecipe := loadValidRecipe(t)
			imageRecipe.Python = &Python{Include: []string{testCase.packageName}}
			problems := Validate(imageRecipe)
			if !strings.Contains(strings.Join(problems, "\n"), "python package name") {
				t.Fatalf("Validate(%q) = %q, want a problem mentioning the python package name", testCase.packageName, problems)
			}
		})
	}
}

func TestValidateReportsAPythonPackageAskedForTwice(t *testing.T) {
	testCases := [][]string{
		{"numpy", "numpy"},
		{"numpy", "NumPy"},
		{"zope.interface", "zope-interface"},
		{"typing_extensions", "typing-extensions"},
	}
	for _, include := range testCases {
		t.Run(strings.Join(include, " and "), func(t *testing.T) {
			imageRecipe := loadValidRecipe(t)
			imageRecipe.Python = &Python{Include: include}
			problems := Validate(imageRecipe)
			if !strings.Contains(strings.Join(problems, "\n"), "twice") {
				t.Fatalf("Validate(%q) = %q, want a problem saying the package is named twice", include, problems)
			}
		})
	}
}

// sampleWheels returns a resolved Python side of a build: one package asked
// for and one the resolver pulled in.
func sampleWheels() ([]LockPyPI, *LockPython) {
	wheels := []LockPyPI{
		{
			Name: "numpy", Version: "2.1.3",
			SHA256:   "c1ad7a8d9e6b4b1d3f2a0e5c6b7a8d9e0f1a2b3c4d5e6f7a8b9c0d1e2f3a4b5c",
			Size:     16000000,
			Filename: "numpy-2.1.3-cp312-cp312-manylinux_2_17_x86_64.manylinux2014_x86_64.whl",
			URL:      "https://files.pythonhosted.org/packages/ab/cd/numpy-2.1.3-cp312-cp312-manylinux_2_17_x86_64.manylinux2014_x86_64.whl",
		},
		{
			Name: "python-dateutil", Version: "2.9.0.post0", Auto: true,
			SHA256:   "d4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f4a5b6c7d8e9f0a1b2c3d4e5",
			Size:     229892,
			Filename: "python_dateutil-2.9.0.post0-py2.py3-none-any.whl",
			URL:      "https://files.pythonhosted.org/packages/ef/gh/python_dateutil-2.9.0.post0-py2.py3-none-any.whl",
		},
	}
	return wheels, &LockPython{
		Requested:   []string{"numpy"},
		Venv:        "/opt/frostroot/venv",
		Interpreter: "Python 3.12.3",
		PipVersion:  "24.0",
	}
}

func TestLockRoundTripWithPython(t *testing.T) {
	original := sampleLock()
	original.PyPI, original.Python = sampleWheels()
	path, content := saveAndRead(t, original)
	for _, want := range []string{"[python]", "[[pypi]]", "numpy", "/opt/frostroot/venv"} {
		if !strings.Contains(content, want) {
			t.Errorf("lock does not mention %q:\n%s", want, content)
		}
	}
	reloaded, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(reloaded, original) {
		t.Fatalf("round trip =\n%+v\nwant\n%+v", reloaded, original)
	}
}

func TestLockWithoutPythonOmitsBothTables(t *testing.T) {
	_, content := saveAndRead(t, sampleLock())
	for _, unwanted := range []string{"python", "pypi"} {
		if strings.Contains(content, unwanted) {
			t.Errorf("a lock without Python packages must not mention %q:\n%s", unwanted, content)
		}
	}
}

func TestLoadLockAcceptsALockWithoutPython(t *testing.T) {
	// What frostroot 0.6 wrote: no [python] table, no [[pypi]] entries.
	path := writeRecipeFile(t, `version = 1
distro = 'ubuntu'
release = '22.04'
suite = 'jammy'
arch = 'amd64'
mirror = 'http://archive.ubuntu.com/ubuntu'
sources = ['deb http://archive.ubuntu.com/ubuntu jammy main universe']
frostroot_version = '0.6.0'
requested = ['git']
source_date_epoch = 1789662022

[[packages]]
name = 'git'
version = '1:2.34.1-1ubuntu1.11'
arch = 'amd64'
sha256 = 'aa'
size = 1
filename = 'pool/main/g/git/git.deb'
`)
	lock, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Python != nil || lock.PyPI != nil {
		t.Errorf("Python = %+v, PyPI = %+v, want neither", lock.Python, lock.PyPI)
	}
	if !lock.HasWheelChecksums() {
		t.Error("HasWheelChecksums() = false, want true: a lock with no wheels has nothing missing")
	}
}

func TestHasWheelChecksums(t *testing.T) {
	complete, pythonSide := sampleWheels()
	testCases := []struct {
		name   string
		wheels []LockPyPI
		want   bool
	}{
		{"none", nil, true},
		{"complete", complete, true},
		{"no checksum", []LockPyPI{{Name: "numpy", Version: "2.1.3", Filename: "numpy.whl", URL: "https://example.invalid/numpy.whl"}}, false},
		{"no file name", []LockPyPI{{Name: "numpy", Version: "2.1.3", SHA256: "aa", URL: "https://example.invalid/numpy.whl"}}, false},
		{"no url", []LockPyPI{{Name: "numpy", Version: "2.1.3", SHA256: "aa", Filename: "numpy.whl"}}, false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			lock := sampleLock()
			lock.PyPI, lock.Python = testCase.wheels, pythonSide
			if got := lock.HasWheelChecksums(); got != testCase.want {
				t.Errorf("HasWheelChecksums() = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestNormalizePythonName(t *testing.T) {
	testCases := []struct{ name, want string }{
		{"numpy", "numpy"},
		{"NumPy", "numpy"},
		{"Flask_SQLAlchemy", "flask-sqlalchemy"},
		{"zope.interface", "zope-interface"},
		{"ruamel.yaml.clib", "ruamel-yaml-clib"},
		{"typing_extensions", "typing-extensions"},
		{"a.-_b", "a-b"},
	}
	for _, testCase := range testCases {
		if got := NormalizePythonName(testCase.name); got != testCase.want {
			t.Errorf("NormalizePythonName(%q) = %q, want %q", testCase.name, got, testCase.want)
		}
	}
}
