package builder

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// TestMmdebstrapSkipsVerificationThroughTheSetupHook: --insecure turns apt's
// verification off the way --ca-bundle turns a bundle on, in the file the
// setup hook writes and the cleanup hook removes, never as an --aptopt,
// which mmdebstrap would ship in the image. An image that never verified a
// mirror again would be a worse outcome than the network the flag is for.
func TestMmdebstrapSkipsVerificationThroughTheSetupHook(t *testing.T) {
	tests := []struct {
		name          string
		spec          BootstrapSpec
		wantSetupHook string
	}{
		{
			name:          "without a bundle",
			spec:          BootstrapSpec{Suite: "noble", TarballPath: "/t", WorkDir: "/w", Arch: "amd64", Insecure: true, CustomizeHooks: []string{"first"}},
			wantSetupHook: `mkdir -p "$1/etc/apt/apt.conf.d" && printf '%s\n' 'Acquire::https::Verify-Peer "false";' 'Acquire::https::Verify-Host "false";' > "$1/etc/apt/apt.conf.d/99frostroot-build-ca"`,
		},
		{
			name:          "with a bundle, which keeps the build's own trust readable in the same file",
			spec:          BootstrapSpec{Suite: "noble", TarballPath: "/t", WorkDir: "/w", Arch: "amd64", Insecure: true, CaInfoPath: "/w/apt-ca-bundle.pem", CustomizeHooks: []string{"first"}},
			wantSetupHook: `mkdir -p "$1/etc/apt/apt.conf.d" && printf '%s\n' 'Acquire::https::CaInfo "/w/apt-ca-bundle.pem";' 'Acquire::https::Verify-Peer "false";' 'Acquire::https::Verify-Host "false";' > "$1/etc/apt/apt.conf.d/99frostroot-build-ca"`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			args, _ := (&Mmdebstrap{CurrentUID: uidFunc(1000)}).commandLine(test.spec)
			var setupHooks, customizeHooks []string
			for _, arg := range args {
				if strings.HasPrefix(arg, "--aptopt=") && strings.Contains(arg, "Verify") {
					t.Errorf("%q is an --aptopt, which mmdebstrap ships in the image", arg)
				}
				if hook, isHook := strings.CutPrefix(arg, "--setup-hook="); isHook {
					setupHooks = append(setupHooks, hook)
				}
				if hook, isHook := strings.CutPrefix(arg, "--customize-hook="); isHook {
					customizeHooks = append(customizeHooks, hook)
				}
			}
			if !slices.Equal(setupHooks, []string{test.wantSetupHook}) {
				t.Errorf("setup hooks =\n%q\nwant\n%q", setupHooks, test.wantSetupHook)
			}
			if wantHooks := []string{"first", `rm -f "$1/etc/apt/apt.conf.d/99frostroot-build-ca"`}; !slices.Equal(customizeHooks, wantHooks) {
				t.Errorf("customize hooks = %q, want %q", customizeHooks, wantHooks)
			}
		})
	}
}

// TestAptTrustHooksWriteEverySettingOnItsOwnLine runs the hooks the way
// mmdebstrap does, against a directory standing in for the chroot: apt reads
// one setting per line, and printf with several arguments writes exactly
// that.
func TestAptTrustHooksWriteEverySettingOnItsOwnLine(t *testing.T) {
	tests := []struct {
		name       string
		caInfoPath string
		want       string
	}{
		{"verification off", "", "Acquire::https::Verify-Peer \"false\";\nAcquire::https::Verify-Host \"false\";\n"},
		{"a bundle and verification off", "/var/tmp/o'brien/apt-ca-bundle.pem", "Acquire::https::CaInfo \"/var/tmp/o'brien/apt-ca-bundle.pem\";\nAcquire::https::Verify-Peer \"false\";\nAcquire::https::Verify-Host \"false\";\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			runHook := func(hook string) {
				t.Helper()
				command := exec.Command("sh", "-c", hook, "exec", root)
				command.Dir = root
				if output, err := command.CombinedOutput(); err != nil {
					t.Fatalf("running %q: %v: %s", hook, err, output)
				}
			}
			runHook(aptTrustSetupHook(aptTrustSettings(test.caInfoPath, true)))
			confPath := filepath.Join(root, filepath.FromSlash(aptBuildTrustConfPath))
			written, err := os.ReadFile(confPath)
			if err != nil {
				t.Fatal(err)
			}
			if string(written) != test.want {
				t.Errorf("the setup hook wrote %q, want %q", written, test.want)
			}
			runHook(aptTrustCleanupHook)
			if _, err := os.Stat(confPath); err == nil {
				t.Errorf("%s is still there after the cleanup hook", confPath)
			}
		})
	}
	if settings := aptTrustSettings("", false); len(settings) != 0 {
		t.Errorf("a build that trusts the host's store and verifies gets no settings, got %q", settings)
	}
}

// TestRenderPythonScriptTrustsTheIndexHostsWhenInsecure: pip has no flag
// that verifies nothing, only hosts it may trust unverified, so --insecure
// names the index's host and the one PyPI serves files from, on the pinned
// pip's install and on the resolve alike. Offline there is no network and
// nothing is named.
func TestRenderPythonScriptTrustsTheIndexHostsWhenInsecure(t *testing.T) {
	withIndex := pythonRecipe()
	withIndex.Python.IndexURL = "https://nexus.example.com:8443/repository/pypi/simple"
	tests := []struct {
		name        string
		imageRecipe recipe.Recipe
		options     PythonOptions
		wantHosts   string // the flags, in order, or "" for none
	}{
		{"PyPI", pythonRecipe(), PythonOptions{Insecure: true}, "--trusted-host 'pypi.org' --trusted-host 'files.pythonhosted.org' "},
		{"an index of the recipe's, port included", withIndex, PythonOptions{Insecure: true}, "--trusted-host 'nexus.example.com:8443' --trusted-host 'files.pythonhosted.org' "},
		{"verified", pythonRecipe(), PythonOptions{}, ""},
		{"offline", pythonRecipe(), PythonOptions{Insecure: true, Offline: true, Pip: PinnedPip}, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := renderPythonScript(t, test.imageRecipe, test.options)
			if test.wantHosts == "" {
				if strings.Contains(script, "--trusted-host") {
					t.Errorf("the script trusts a host unverified:\n%s", script)
				}
				return
			}
			if got := strings.Count(script, test.wantHosts); got != 2 {
				t.Errorf("want %q on the pinned pip and on the resolve, found it %d times:\n%s", test.wantHosts, got, script)
			}
		})
	}
	if hosts := pipTrustedHosts("https://files.pythonhosted.org/simple"); !slices.Equal(hosts, []string{"files.pythonhosted.org"}) {
		t.Errorf("pipTrustedHosts names a host twice: %q", hosts)
	}
}

// TestBuildRecordsAnUnverifiedResolveInTheLock: the one thing --insecure
// leaves unprotected is what pip resolves, and the lock then pins it by
// hash for every rebuild. So the lock says how it was resolved, and a
// verified build says nothing, which is what every lock before this said.
func TestBuildRecordsAnUnverifiedResolveInTheLock(t *testing.T) {
	report, err := os.ReadFile(filepath.Join("testdata", "pip-report.json"))
	if err != nil {
		t.Fatal(err)
	}
	imageRecipe := sampleRecipe()
	imageRecipe.Python = &recipe.Python{Include: []string{"numpy"}}
	for _, insecure := range []bool{false, true} {
		options, _ := newTestOptions(t)
		options.Insecure = insecure
		bootstrapper := &fakeBootstrapper{pipReport: string(report)}
		if _, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), imageRecipe, options); err != nil {
			t.Fatalf("Build with Insecure=%v: %v", insecure, err)
		}
		if bootstrapper.lastSpec.Insecure != insecure {
			t.Errorf("Insecure=%v: the bootstrap spec says %v", insecure, bootstrapper.lastSpec.Insecure)
		}
		lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, LockFileName))
		if err != nil {
			t.Fatal(err)
		}
		if lock.PythonResolvedUnverified() != insecure {
			t.Errorf("Insecure=%v: the lock's [python] says transport %q", insecure, lock.Python.Transport)
		}
	}
}

// TestOfflineBuildIgnoresInsecure: an offline build fetches nothing, so there
// is nothing to verify or to skip verifying; the flag reaches neither apt nor
// pip, and the lock is read, not written.
func TestOfflineBuildIgnoresInsecure(t *testing.T) {
	options, _ := newTestOptions(t)
	writeVendoredLock(t, options)
	options.Insecure = true
	options.Offline = true
	bootstrapper := &fakeBootstrapper{}
	if _, err := buildWith(bootstrapper, options); err != nil {
		t.Fatalf("offline build with Insecure: %v", err)
	}
	if bootstrapper.lastSpec.Insecure {
		t.Error("an offline bootstrap was told to skip verification")
	}
	if joined := strings.Join(bootstrapper.lastSpec.CustomizeHooks, "\n"); strings.Contains(joined, "Verify-Peer") {
		t.Errorf("an offline build got apt's verification setting:\n%s", joined)
	}
}
