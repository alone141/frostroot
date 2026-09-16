package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

const defaultStatus = "Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\n\n" +
	"Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\n"

// fakeBoot produces the same two artifacts the real bootstrapper does: a
// tarball at TarPath and a dpkg status file wherever the download hook says.
type fakeBoot struct {
	err       error
	status    string // written to the status path; default sample
	noTar     bool
	emptyTar  bool // mmdebstrap creates the output file before it starts
	spec      BootstrapSpec
	calls     int
	stageSeen map[string]bool
}

func (f *fakeBoot) Run(ctx context.Context, spec BootstrapSpec) error {
	f.calls++
	f.spec = spec
	f.stageSeen = map[string]bool{}
	for _, name := range []string{"wsl.conf", "sudoers", "provision.sh"} {
		_, err := os.Stat(filepath.Join(spec.WorkDir, "stage", name))
		f.stageSeen[name] = err == nil
	}
	if f.err != nil {
		return f.err
	}
	switch {
	case f.emptyTar:
		if err := os.WriteFile(spec.TarPath, nil, 0o644); err != nil {
			return err
		}
	case !f.noTar:
		if err := os.WriteFile(spec.TarPath, []byte("tar-bytes"), 0o644); err != nil {
			return err
		}
	}
	body := f.status
	if body == "" {
		body = defaultStatus
	}
	return os.WriteFile(statusPathFromHooks(spec.Hooks), []byte(body), 0o644)
}

// statusPathFromHooks finds where the download hook writes the status file,
// the way mmdebstrap would.
func statusPathFromHooks(hooks []string) string {
	const prefix = "download /var/lib/dpkg/status "
	for _, h := range hooks {
		if strings.HasPrefix(h, prefix) {
			return strings.Trim(strings.TrimPrefix(h, prefix), "'")
		}
	}
	return ""
}

type preflightBoot struct {
	fakeBoot
	preflight error
}

func (p *preflightBoot) Preflight(spec BootstrapSpec) error { return p.preflight }

// testOptions keeps work directories inside the test's temp dir instead of
// /var/tmp/frostroot.
func testOptions(t *testing.T) (Options, string) {
	t.Helper()
	cache := t.TempDir()
	return Options{
		Dir:    t.TempDir(),
		GOOS:   "linux",
		Getenv: env(map[string]string{"XDG_CACHE_HOME": cache}),
	}, filepath.Join(cache, "frostroot")
}

func tarball(opts Options) string {
	return filepath.Join(opts.Dir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
}

func assertNoArtifacts(t *testing.T, opts Options) {
	t.Helper()
	for _, p := range []string{
		filepath.Join(opts.Dir, "frostroot.lock"),
		filepath.Join(opts.Dir, "frostroot.lock.tmp"),
		tarball(opts),
	} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("%s must not exist after a failed build: %v", p, err)
		}
	}
}

func TestBuildSuccessWritesLockAndTarball(t *testing.T) {
	opts, root := testOptions(t)
	boot := &fakeBoot{}
	b := Builder{Bootstrap: boot}
	res, err := b.Build(context.Background(), testRecipe(), opts)
	if err != nil {
		t.Fatal(err)
	}
	spec := boot.spec
	if spec.Suite != "jammy" || spec.Arch != "amd64" || !spec.Recommends {
		t.Fatalf("spec %+v", spec)
	}
	if spec.Keyring != "/usr/share/keyrings/ubuntu-archive-keyring.gpg" {
		t.Fatalf("keyring %q", spec.Keyring)
	}
	if len(spec.Sources) != 3 || !strings.Contains(spec.Sources[1], "jammy-updates") || !strings.Contains(spec.Sources[2], "jammy-security") {
		t.Fatalf("expected three pockets, got %#v", spec.Sources)
	}
	for _, need := range []string{"git", "build-essential", "cmake", "systemd", "systemd-sysv", "sudo", "ca-certificates"} {
		if !contains(spec.Include, need) {
			t.Fatalf("include %v missing %s", spec.Include, need)
		}
	}
	if !strings.HasPrefix(spec.WorkDir, root+string(filepath.Separator)) || spec.TarPath != filepath.Join(spec.WorkDir, "image.tar.gz") {
		t.Fatalf("work %q tar %q root %q", spec.WorkDir, spec.TarPath, root)
	}
	if !boot.stageSeen["wsl.conf"] || !boot.stageSeen["sudoers"] || !boot.stageSeen["provision.sh"] {
		t.Fatalf("stage files must exist before the bootstrap runs: %v", boot.stageSeen)
	}
	if len(spec.Hooks) != len(CustomizeHooks(Stage{WSLConf: "w", Sudoers: "s", Provision: "p", StatusOut: "o"})) {
		t.Fatalf("hooks %#v", spec.Hooks)
	}

	lock, err := recipe.LoadLock(filepath.Join(opts.Dir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Version != 1 || lock.Distro != "ubuntu" || lock.Release != "22.04" || lock.Suite != "jammy" ||
		lock.Arch != "amd64" || lock.FrostrootVersion != Version || lock.Mirror != "http://archive.ubuntu.com/ubuntu" {
		t.Fatalf("lock %+v", lock)
	}
	if strings.Join(lock.Sources, "\n") != strings.Join(spec.Sources, "\n") {
		t.Fatalf("lock must record the sources used: %#v", lock.Sources)
	}
	if strings.Join(lock.Requested, ",") != "git,build-essential,cmake" {
		t.Fatalf("requested %#v", lock.Requested)
	}
	if len(lock.Packages) != 2 || lock.Packages[0].Name != "git" || lock.Packages[1].Name != "libc6" {
		t.Fatalf("packages must be every installed package, sorted: %#v", lock.Packages)
	}

	if body, err := os.ReadFile(tarball(opts)); err != nil || string(body) != "tar-bytes" {
		t.Fatalf("tarball: %q %v", body, err)
	}
	if res.TarballPath != tarball(opts) || res.LockPath != filepath.Join(opts.Dir, "frostroot.lock") {
		t.Fatalf("result %+v", res)
	}
	if res.WorkDir != "" {
		t.Fatalf("workdir should be removed on success: %q", res.WorkDir)
	}
	if _, err := os.Stat(spec.WorkDir); !os.IsNotExist(err) {
		t.Fatalf("workdir still exists: %v", err)
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, "frostroot.lock.tmp")); !os.IsNotExist(err) {
		t.Fatalf("tmp lock left behind: %v", err)
	}
}

func TestBuildRequestedExcludesEssentials(t *testing.T) {
	opts, _ := testOptions(t)
	b := Builder{Bootstrap: &fakeBoot{}}
	if _, err := b.Build(context.Background(), testRecipe(), opts); err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(filepath.Join(opts.Dir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if contains(lock.Requested, "systemd") {
		t.Fatalf("requested is the recipe list as written: %#v", lock.Requested)
	}
}

func TestBuildNoSudoStagesNoSudoers(t *testing.T) {
	opts, _ := testOptions(t)
	boot := &fakeBoot{}
	r := testRecipe()
	r.User.Sudo = false
	if _, err := (&Builder{Bootstrap: boot}).Build(context.Background(), r, opts); err != nil {
		t.Fatal(err)
	}
	if boot.stageSeen["sudoers"] || strings.Contains(strings.Join(boot.spec.Hooks, "\n"), "sudoers") {
		t.Fatalf("no sudo means no sudoers: %v %#v", boot.stageSeen, boot.spec.Hooks)
	}
}

func TestBuildOverwritesExistingArtifacts(t *testing.T) {
	opts, _ := testOptions(t)
	if err := os.MkdirAll(filepath.Dir(tarball(opts)), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{tarball(opts), filepath.Join(opts.Dir, "frostroot.lock")} {
		if err := os.WriteFile(p, []byte("stale"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := (&Builder{Bootstrap: &fakeBoot{}}).Build(context.Background(), testRecipe(), opts); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(tarball(opts)); string(body) != "tar-bytes" {
		t.Fatalf("tarball not replaced: %q", body)
	}
	if _, err := recipe.LoadLock(filepath.Join(opts.Dir, "frostroot.lock")); err != nil {
		t.Fatalf("lock not replaced: %v", err)
	}
}

func TestBuildKeepWork(t *testing.T) {
	opts, _ := testOptions(t)
	opts.KeepWork = true
	res, err := (&Builder{Bootstrap: &fakeBoot{}}).Build(context.Background(), testRecipe(), opts)
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkDir == "" {
		t.Fatal("expected workdir")
	}
	if _, err := os.Stat(filepath.Join(res.WorkDir, "stage", "dpkg-status")); err != nil {
		t.Fatalf("kept workdir should still hold the stage: %v", err)
	}
}

func TestBuildFailureKeepsWorkdirAndWritesNoArtifacts(t *testing.T) {
	opts, _ := testOptions(t)
	b := Builder{Bootstrap: &fakeBoot{err: errors.New("mmdebstrap exploded")}}
	res, err := b.Build(context.Background(), testRecipe(), opts)
	if err == nil || !strings.Contains(err.Error(), "mmdebstrap exploded") {
		t.Fatalf("got %v", err)
	}
	if res.WorkDir == "" {
		t.Fatal("expected kept workdir")
	}
	if _, err := os.Stat(res.WorkDir); err != nil {
		t.Fatalf("workdir must be kept for debugging: %v", err)
	}
	assertNoArtifacts(t, opts)
}

func TestBuildMissingTarballIsAnError(t *testing.T) {
	opts, _ := testOptions(t)
	b := Builder{Bootstrap: &fakeBoot{noTar: true}} // status written, tarball missing
	res, err := b.Build(context.Background(), testRecipe(), opts)
	if err == nil {
		t.Fatal("expected error when the tarball is missing")
	}
	if res.WorkDir == "" {
		t.Fatal("expected kept workdir")
	}
	assertNoArtifacts(t, opts)
}

func TestBuildEmptyTarballIsAnError(t *testing.T) {
	// mmdebstrap creates the output file at 0 bytes before it starts.
	opts, _ := testOptions(t)
	if _, err := (&Builder{Bootstrap: &fakeBoot{emptyTar: true}}).Build(context.Background(), testRecipe(), opts); err == nil {
		t.Fatal("expected error for an empty tarball")
	}
	assertNoArtifacts(t, opts)
}

func TestBuildEmptyStatusIsAnError(t *testing.T) {
	// A bootstrap that installs nothing must not produce a lock.
	opts, _ := testOptions(t)
	if _, err := (&Builder{Bootstrap: &fakeBoot{status: "\n"}}).Build(context.Background(), testRecipe(), opts); err == nil {
		t.Fatal("expected error for empty status")
	}
	assertNoArtifacts(t, opts)
}

func TestBuildMirrorOverrideReplacesAllPockets(t *testing.T) {
	opts, _ := testOptions(t)
	opts.Mirror = "http://mirror.example/ubuntu"
	boot := &fakeBoot{}
	if _, err := (&Builder{Bootstrap: boot}).Build(context.Background(), testRecipe(), opts); err != nil {
		t.Fatal(err)
	}
	for _, line := range boot.spec.Sources {
		if !strings.Contains(line, "mirror.example") {
			t.Fatalf("override missed: %q", line)
		}
	}
	lock, err := recipe.LoadLock(filepath.Join(opts.Dir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Mirror != "http://mirror.example/ubuntu" || !strings.Contains(lock.Sources[0], "mirror.example") {
		t.Fatalf("lock must record the mirror actually used: %+v", lock)
	}
}

func TestBuildNotLinux(t *testing.T) {
	opts, root := testOptions(t)
	opts.GOOS = "windows"
	boot := &fakeBoot{}
	_, err := (&Builder{Bootstrap: boot}).Build(context.Background(), testRecipe(), opts)
	if !errors.Is(err, ErrNotLinux) {
		t.Fatalf("got %v", err)
	}
	if boot.calls != 0 {
		t.Fatal("bootstrap must not run")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("no work directory should be created")
	}
}

func TestBuildPreflightRunsBeforeAnyWork(t *testing.T) {
	opts, root := testOptions(t)
	boot := &preflightBoot{preflight: ErrNoKeyring}
	res, err := (&Builder{Bootstrap: boot}).Build(context.Background(), testRecipe(), opts)
	if !errors.Is(err, ErrNoKeyring) {
		t.Fatalf("got %v", err)
	}
	if boot.calls != 0 || res.WorkDir != "" {
		t.Fatalf("bootstrap ran or workdir reported: calls=%d res=%+v", boot.calls, res)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("a failed preflight must not create a work directory")
	}
}

func TestBuildRefusesWorkRootOnMnt(t *testing.T) {
	opts, _ := testOptions(t)
	opts.Getenv = env(map[string]string{"XDG_CACHE_HOME": "/mnt/c/cache"})
	boot := &fakeBoot{}
	if _, err := (&Builder{Bootstrap: boot}).Build(context.Background(), testRecipe(), opts); err == nil {
		t.Fatal("expected refusal")
	}
	if boot.calls != 0 {
		t.Fatal("bootstrap must not run")
	}
}

func TestBuildFocalUsesArchivePockets(t *testing.T) {
	opts, _ := testOptions(t)
	boot := &fakeBoot{}
	r := testRecipe()
	r.Image.Release = "20.04"
	if _, err := (&Builder{Bootstrap: boot}).Build(context.Background(), r, opts); err != nil {
		t.Fatal(err)
	}
	if boot.spec.Suite != "focal" {
		t.Fatalf("suite %q", boot.spec.Suite)
	}
	// Task 0 spike: focal is not on old-releases; every pocket 404s there.
	for _, line := range boot.spec.Sources {
		if !strings.HasPrefix(line, "deb http://archive.ubuntu.com/ubuntu focal") {
			t.Fatalf("focal must use the archive: %q", line)
		}
	}
	if _, err := os.Stat(filepath.Join(opts.Dir, "dist", "cpp-lab-ubuntu-20.04-amd64.tar.gz")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildCancelledContextPropagates(t *testing.T) {
	opts, _ := testOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := (&Builder{Bootstrap: &fakeBoot{err: context.Canceled}}).Build(ctx, testRecipe(), opts)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if res.WorkDir == "" {
		t.Fatal("an interrupted build keeps its workdir like any other failure")
	}
	assertNoArtifacts(t, opts)
}

func TestBuildInvalidReleaseIsAnError(t *testing.T) {
	opts, _ := testOptions(t)
	r := testRecipe()
	r.Image.Release = "18.04"
	boot := &fakeBoot{}
	if _, err := (&Builder{Bootstrap: boot}).Build(context.Background(), r, opts); err == nil || boot.calls != 0 {
		t.Fatalf("err=%v calls=%d", err, boot.calls)
	}
}
