package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// sampleFedoraRecipe is a small valid Fedora recipe.
func sampleFedoraRecipe() recipe.Recipe {
	imageRecipe := sampleRecipe()
	imageRecipe.Image = recipe.Image{Name: "fedora-lab", Distro: "fedora", Release: "44", Arch: "amd64"}
	imageRecipe.Packages.Include = []string{"git", "gcc"}
	return imageRecipe
}

// fakeRPM is one package a fake Fedora build installs: where dnf found it
// and the file it downloaded.
type fakeRPM struct {
	nevra, reason, repository, sourceRPM string
	content                              string
}

func (p fakeRPM) fileName() string {
	installed, err := parseFullNEVRA(p.nevra)
	if err != nil {
		panic(err)
	}
	return installed.fileName()
}

// fakeFedoraBootstrapper writes what mkosi and frostroot's scripts would:
// the records of what the tools tree and the image hold, the files dnf kept
// in the package cache, and the tarball.
type fakeFedoraBootstrapper struct {
	image, tools  []fakeRPM
	tarball       string // what the tarball holds; "" for "fedora tarball"
	preflightErr  error
	runErr        error
	preflightSpec FedoraSpec
	runSpec       FedoraSpec
	ran           bool
}

func (f *fakeFedoraBootstrapper) Preflight(_ context.Context, spec FedoraSpec) error {
	f.preflightSpec = spec
	return f.preflightErr
}

func (f *fakeFedoraBootstrapper) Run(_ context.Context, spec FedoraSpec) error {
	f.ran, f.runSpec = true, spec
	if f.runErr != nil {
		return f.runErr
	}
	write := func(path, content string) error {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		return os.WriteFile(path, []byte(content), 0o644)
	}
	var installed, locations strings.Builder
	installed.WriteString("gpg-pubkey-0:36f612dcf27f7d1a48a835e4dbfcf71c6d9f90a6-6786af3b.noarch|User|@System|(none)\n")
	for _, rpm := range f.image {
		installed.WriteString(rpm.nevra + "|" + rpm.reason + "|" + rpm.repository + "|" + rpm.sourceRPM + "\n")
		locations.WriteString(rpm.nevra + "|" + rpm.repository + "|https://mirror.example/fedora/linux/updates/44/Everything/x86_64/" + fedoraLayoutPath(rpm.fileName()) + "\n")
		if err := write(filepath.Join(spec.CacheDir, "cache", "libdnf5", rpm.repository+"-0123abcd", "packages", rpm.fileName()), rpm.content); err != nil {
			return err
		}
	}
	var tools strings.Builder
	tools.WriteString("gpg-pubkey|0|6d9f90a6|6786af3b|(none)|(none)\n")
	for _, rpm := range f.tools {
		parsed, err := parseFullNEVRA(rpm.nevra)
		if err != nil {
			return err
		}
		version, release, _ := strings.Cut(parsed.version, "-")
		tools.WriteString(strings.Join([]string{parsed.name, parsed.epoch, version, release, parsed.arch, rpm.sourceRPM}, "|") + "\n")
		if err := write(filepath.Join(spec.CacheDir, "cache", "dnf", rpm.repository+"-4567cdef", "packages", rpm.fileName()), rpm.content); err != nil {
			return err
		}
	}
	tarball := f.tarball
	if tarball == "" {
		tarball = "fedora tarball"
	}
	for path, content := range map[string]string{
		filepath.Join(spec.ImageOutputDir, fedoraInstalledFile): installed.String(),
		filepath.Join(spec.ImageOutputDir, fedoraLocationsFile): locations.String(),
		spec.ToolsRecordPath: tools.String(),
		spec.TarballPath:     tarball,
	} {
		if err := write(path, content); err != nil {
			return err
		}
	}
	return nil
}

func newFakeFedoraBootstrapper() *fakeFedoraBootstrapper {
	return &fakeFedoraBootstrapper{
		image: []fakeRPM{
			{nevra: "git-0:2.55.0-1.fc44.x86_64", reason: "User", repository: "updates", sourceRPM: "git-2.55.0-1.fc44.src.rpm", content: "git rpm"},
			{nevra: "bash-completion-1:2.17-2.fc44.noarch", reason: "Weak Dependency", repository: "fedora", sourceRPM: "bash-completion-2.17-2.fc44.src.rpm", content: "completion rpm"},
			{nevra: "NetworkManager-libnm-1:1.54.0-1.fc44.x86_64", reason: "Dependency", repository: "fedora", sourceRPM: "NetworkManager-1.54.0-1.fc44.src.rpm", content: "libnm rpm"},
		},
		tools: []fakeRPM{
			{nevra: "dnf5-0:5.4.6.0-1.fc44.x86_64", repository: "updates", sourceRPM: "dnf5-5.4.6.0-1.fc44.src.rpm", content: "dnf5 rpm"},
			{nevra: "rpm-0:6.0.2-1.fc44.x86_64", repository: "fedora", sourceRPM: "rpm-6.0.2-1.fc44.src.rpm", content: "rpm rpm"},
		},
	}
}

func sha256Hex(content string) string {
	digest := sha256.Sum256([]byte(content))
	return hex.EncodeToString(digest[:])
}

func TestBuildFedoraWritesLockAndTarball(t *testing.T) {
	options, workRoot := newTestOptions(t)
	bootstrapper := newFakeFedoraBootstrapper()
	result, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapper.preflightSpec.WorkDir != workRoot {
		t.Errorf("Preflight got work root %q, want %q", bootstrapper.preflightSpec.WorkDir, workRoot)
	}
	if want := filepath.Join(workRoot, fedoraPackageCacheName); bootstrapper.runSpec.CacheDir != want {
		t.Errorf("the package cache is %q, want it kept under the work root at %q", bootstrapper.runSpec.CacheDir, want)
	}
	wantTarball := filepath.Join(options.RecipeDir, "dist", "fedora-lab-fedora-44-amd64.tar.gz")
	if result.TarballPath != wantTarball {
		t.Errorf("TarballPath = %q, want %q", result.TarballPath, wantTarball)
	}
	if content, err := os.ReadFile(wantTarball); err != nil || string(content) != "fedora tarball" {
		t.Errorf("tarball = %q, %v", content, err)
	}
	lock, err := recipe.LoadLock(filepath.Join(options.RecipeDir, LockFileName))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Distro != "fedora" || lock.Release != "44" || lock.Arch != "amd64" || lock.Suite != "" || lock.Mirror != "" || len(lock.Sources) != 0 {
		t.Errorf("lock head = %+v", lock)
	}
	if !slices.Equal(lock.Requested, []string{"git", "gcc"}) || lock.SourceDateEpoch <= 0 || lock.FrostrootVersion != Version {
		t.Errorf("requested %q, frozen at %d, frostroot %s", lock.Requested, lock.SourceDateEpoch, lock.FrostrootVersion)
	}
	release, err := distro.LookupFedora("44", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	keyDigest := sha256.Sum256(release.Key)
	wantRepositories := []recipe.LockRepository{
		{Name: "fedora", URL: release.Repositories[0].BaseURL, KeySHA256: hex.EncodeToString(keyDigest[:])},
		{Name: "updates", URL: release.Repositories[1].BaseURL, KeySHA256: hex.EncodeToString(keyDigest[:])},
	}
	if !slices.EqualFunc(lock.Repositories, wantRepositories, func(a, b recipe.LockRepository) bool {
		return a.Name == b.Name && a.URL == b.URL && a.KeySHA256 == b.KeySHA256 && a.Suite == "" && len(a.Components) == 0
	}) {
		t.Errorf("repositories = %+v, want %+v", lock.Repositories, wantRepositories)
	}
	// Sorted by name, the key rpm imported left out, the epoch kept where
	// there is one, and each package's file as dnf downloaded it.
	wantPackages := []recipe.LockPackage{
		{Name: "NetworkManager-libnm", Version: "1:1.54.0-1.fc44", Arch: "x86_64", Reason: "Dependency", Source: "fedora", SourceRPM: "NetworkManager-1.54.0-1.fc44.src.rpm",
			Filename: "Packages/n/NetworkManager-libnm-1.54.0-1.fc44.x86_64.rpm", SHA256: sha256Hex("libnm rpm"), Size: int64(len("libnm rpm"))},
		{Name: "bash-completion", Version: "1:2.17-2.fc44", Arch: "noarch", Reason: "Weak Dependency", Source: "fedora", SourceRPM: "bash-completion-2.17-2.fc44.src.rpm",
			Filename: "Packages/b/bash-completion-2.17-2.fc44.noarch.rpm", SHA256: sha256Hex("completion rpm"), Size: int64(len("completion rpm"))},
		{Name: "git", Version: "2.55.0-1.fc44", Arch: "x86_64", Reason: "User", Source: "updates", SourceRPM: "git-2.55.0-1.fc44.src.rpm",
			Filename: "Packages/g/git-2.55.0-1.fc44.x86_64.rpm", SHA256: sha256Hex("git rpm"), Size: int64(len("git rpm"))},
	}
	if !slices.Equal(lock.Packages, wantPackages) {
		t.Errorf("packages =\n%+v\nwant\n%+v", lock.Packages, wantPackages)
	}
	wantTools := []recipe.LockPackage{
		{Name: "dnf5", Version: "5.4.6.0-1.fc44", Arch: "x86_64", Source: "updates", SourceRPM: "dnf5-5.4.6.0-1.fc44.src.rpm",
			Filename: "Packages/d/dnf5-5.4.6.0-1.fc44.x86_64.rpm", SHA256: sha256Hex("dnf5 rpm"), Size: int64(len("dnf5 rpm"))},
		{Name: "rpm", Version: "6.0.2-1.fc44", Arch: "x86_64", Source: "fedora", SourceRPM: "rpm-6.0.2-1.fc44.src.rpm",
			Filename: "Packages/r/rpm-6.0.2-1.fc44.x86_64.rpm", SHA256: sha256Hex("rpm rpm"), Size: int64(len("rpm rpm"))},
	}
	if !slices.Equal(lock.Tools, wantTools) {
		t.Errorf("tools =\n%+v\nwant\n%+v", lock.Tools, wantTools)
	}
	if result.InstalledPackageCount != 3 {
		t.Errorf("InstalledPackageCount = %d, want 3", result.InstalledPackageCount)
	}
}

// TestBuildFedoraWritesMkosiItsConfiguration: what mkosi reads, where the
// Mkosi bootstrapper expects it, and the repository file under the name that
// keeps mkosi from writing its own.
func TestBuildFedoraWritesMkosiItsConfiguration(t *testing.T) {
	options, _ := newTestOptions(t)
	options.KeepWork = true
	bootstrapper := newFakeFedoraBootstrapper()
	if _, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options); err != nil {
		t.Fatal(err)
	}
	spec := bootstrapper.runSpec
	for _, path := range []string{
		filepath.Join(spec.ToolsConfigDir, "mkosi.conf"),
		filepath.Join(spec.ToolsConfigDir, fedoraFinalizeName),
		filepath.Join(spec.ToolsConfigDir, fedoraPackageManagerTree, "etc", "yum.repos.d", "mkosi.repo"),
		filepath.Join(spec.ToolsConfigDir, fedoraPackageManagerTree, "etc", "frostroot-keys", "RPM-GPG-KEY-fedora-44-primary"),
		filepath.Join(spec.ImageConfigDir, "mkosi.conf"),
		filepath.Join(spec.ImageConfigDir, fedoraProvisionName),
		filepath.Join(spec.ImageConfigDir, fedoraFinalizeName),
		filepath.Join(spec.ImageConfigDir, "wsl.conf"),
		filepath.Join(spec.ImageConfigDir, "sudoers"),
		filepath.Join(spec.ImageConfigDir, fedoraPackageManagerTree, "etc", "dnf", "dnf.conf"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("mkosi's configuration lacks %s: %v", path, err)
		}
	}
	conf, err := os.ReadFile(filepath.Join(spec.ImageConfigDir, "mkosi.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Format=tar\nCompressOutput=no\n", "WithRecommends=yes\n", "CleanPackageMetadata=no\n", "Packages=git\n         gcc\n         glibc-langpack-en\n         systemd\n"} {
		if !strings.Contains(string(conf), want) {
			t.Errorf("the image's mkosi.conf lacks %q:\n%s", want, conf)
		}
	}
	// No --ca-bundle: dnf keeps its own store, which the tools tree's
	// ca-certificates fills.
	if _, err := os.Stat(filepath.Join(spec.ImageConfigDir, fedoraPackageManagerTree, "etc", "frostroot-trust")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a build without --ca-bundle staged a trust bundle: %v", err)
	}
}

func TestBuildFedoraGivesDnfTheCABundle(t *testing.T) {
	options, _ := newTestOptions(t)
	options.KeepWork = true
	options.ExtraTrustPEM = []byte("-----BEGIN CERTIFICATE-----\nproxy\n-----END CERTIFICATE-----\n")
	options.Insecure = false
	bootstrapper := newFakeFedoraBootstrapper()
	if _, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options); err != nil {
		t.Fatal(err)
	}
	for _, configDir := range []string{bootstrapper.runSpec.ToolsConfigDir, bootstrapper.runSpec.ImageConfigDir} {
		tree := filepath.Join(configDir, fedoraPackageManagerTree)
		bundle, err := os.ReadFile(filepath.Join(tree, "etc", "frostroot-trust", "ca-bundle.pem"))
		if err != nil || !strings.HasSuffix(string(bundle), string(options.ExtraTrustPEM)) {
			t.Errorf("%s: bundle = %q, %v; want it to end with the extra authority", tree, bundle, err)
		}
		repoFile, err := os.ReadFile(filepath.Join(tree, "etc", "yum.repos.d", "mkosi.repo"))
		if err != nil || strings.Count(string(repoFile), "sslcacert=/etc/frostroot-trust/ca-bundle.pem\n") != 2 {
			t.Errorf("%s: every repository should verify with the bundle:\n%s", tree, repoFile)
		}
	}
}

func TestBuildFedoraRefusesAMirror(t *testing.T) {
	options, _ := newTestOptions(t)
	options.MirrorURL = "http://mirror.example/ubuntu"
	bootstrapper := newFakeFedoraBootstrapper()
	result, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options)
	if !errors.Is(err, ErrFedoraOption) {
		t.Fatalf("Build error = %v, want ErrFedoraOption", err)
	}
	if bootstrapper.ran || result.WorkDir != "" {
		t.Errorf("ran = %v, Result = %+v; want nothing done", bootstrapper.ran, result)
	}
}

func TestBuildFedoraFailuresKeepWorkDirAndWriteNothing(t *testing.T) {
	testCases := map[string]func() FedoraBootstrapper{
		"mkosi fails": func() FedoraBootstrapper {
			bootstrapper := newFakeFedoraBootstrapper()
			bootstrapper.runErr = errors.New("mkosi exploded")
			return bootstrapper
		},
		// A lock is fact: a package whose file is not where dnf downloaded
		// it cannot be recorded.
		"a package's file is missing": func() FedoraBootstrapper {
			return &missingFileBootstrapper{fakeFedoraBootstrapper: newFakeFedoraBootstrapper()}
		},
		"dnf5 names a reason it has not": func() FedoraBootstrapper {
			bootstrapper := newFakeFedoraBootstrapper()
			bootstrapper.image[0].reason = "Clean"
			return bootstrapper
		},
	}
	for name, newBootstrapper := range testCases {
		t.Run(name, func(t *testing.T) {
			options, _ := newTestOptions(t)
			result, err := (&Builder{Fedora: newBootstrapper()}).Build(context.Background(), sampleFedoraRecipe(), options)
			if err == nil {
				t.Fatal("Build succeeded, want a failure")
			}
			if result.WorkDir == "" {
				t.Error("a failed build keeps its work directory")
			}
			for _, path := range []string{filepath.Join(options.RecipeDir, LockFileName), filepath.Join(options.RecipeDir, "dist", "fedora-lab-fedora-44-amd64.tar.gz")} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Errorf("%s exists after a failed build: %v", path, err)
				}
			}
		})
	}
}

// missingFileBootstrapper removes the last image package's downloaded file
// after the fake build, as if dnf had not kept it.
type missingFileBootstrapper struct {
	*fakeFedoraBootstrapper
}

func (m *missingFileBootstrapper) Run(ctx context.Context, spec FedoraSpec) error {
	if err := m.fakeFedoraBootstrapper.Run(ctx, spec); err != nil {
		return err
	}
	last := m.image[len(m.image)-1]
	return os.Remove(filepath.Join(spec.CacheDir, "cache", "libdnf5", last.repository+"-0123abcd", "packages", last.fileName()))
}

func TestParseFullNEVRA(t *testing.T) {
	testCases := []struct {
		nevra                               string
		name, epoch, version, arch, fileRPM string
	}{
		{"git-0:2.55.0-1.fc44.x86_64", "git", "0", "2.55.0-1.fc44", "x86_64", "git-2.55.0-1.fc44.x86_64.rpm"},
		{"bash-completion-1:2.17-2.fc44.noarch", "bash-completion", "1", "2.17-2.fc44", "noarch", "bash-completion-2.17-2.fc44.noarch.rpm"},
		{"python3-libs-3.14.4-1.fc44.x86_64", "python3-libs", "0", "3.14.4-1.fc44", "x86_64", "python3-libs-3.14.4-1.fc44.x86_64.rpm"},
	}
	for _, testCase := range testCases {
		got, err := parseFullNEVRA(testCase.nevra)
		if err != nil {
			t.Errorf("parseFullNEVRA(%q) = %v", testCase.nevra, err)
			continue
		}
		if got.name != testCase.name || got.epoch != testCase.epoch || got.version != testCase.version || got.arch != testCase.arch || got.fileName() != testCase.fileRPM {
			t.Errorf("parseFullNEVRA(%q) = %+v, file %q", testCase.nevra, got, got.fileName())
		}
	}
	for _, bad := range []string{"", "git", "git.x86_64", "git-2.55.0.x86_64", "-0:1-1.x86_64"} {
		if _, err := parseFullNEVRA(bad); err == nil {
			t.Errorf("parseFullNEVRA(%q) succeeded", bad)
		}
	}
}

func TestFedoraFilename(t *testing.T) {
	for location, want := range map[string]string{
		"https://mirror.example/pub/fedora/linux/updates/44/Everything/x86_64/Packages/g/git-2.55.0-1.fc44.x86_64.rpm":     "Packages/g/git-2.55.0-1.fc44.x86_64.rpm",
		"http://mirror.example/fedora/releases/44/Everything/x86_64/os/Packages/n/NetworkManager-1.54.0-1.fc44.x86_64.rpm": "Packages/n/NetworkManager-1.54.0-1.fc44.x86_64.rpm",
		"https://mirror.example/fedora/Packages/7/7zip-26.02-1.fc44.x86_64.rpm":                                            "Packages/7/7zip-26.02-1.fc44.x86_64.rpm",
	} {
		if got, err := fedoraFilename(location); err != nil || got != want {
			t.Errorf("fedoraFilename(%q) = %q, %v; want %q", location, got, err, want)
		}
	}
	for _, bad := range []string{
		"https://mirror.example/fedora/git-2.55.0-1.fc44.x86_64.rpm",             // not below Packages/
		"https://mirror.example/fedora/Packages/x/git-2.55.0-1.fc44.x86_64.rpm",  // not below its letter
		"https://mirror.example/fedora/Packages/g/git-2.55.0-1.fc44.x86_64.drpm", // not an rpm
		"https://mirror.example/fedora/Packages/g/",                              // no file
	} {
		if got, err := fedoraFilename(bad); err == nil {
			t.Errorf("fedoraFilename(%q) = %q, want an error", bad, got)
		}
	}
}

func TestReadFedoraInstalledRefusesWhatIsNotARecord(t *testing.T) {
	for name, content := range map[string]string{
		"no packages":       "gpg-pubkey-0:36f612dc-6786af3b.noarch|User|@System|(none)\n",
		"too few fields":    "git-0:2.55.0-1.fc44.x86_64|User|updates\n",
		"an unknown reason": "git-0:2.55.0-1.fc44.x86_64|Clean|updates|git-2.55.0-1.fc44.src.rpm\n",
		"not a nevra":       "git|User|updates|git-2.55.0-1.fc44.src.rpm\n",
	} {
		path := filepath.Join(t.TempDir(), fedoraInstalledFile)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := readFedoraInstalled(path); err == nil {
			t.Errorf("%s: readFedoraInstalled succeeded", name)
		}
	}
}
