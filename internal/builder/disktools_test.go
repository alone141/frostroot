package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// newFakeDiskHost returns a host with every program a disk build runs.
func newFakeDiskHost() *fakeHost {
	installed := map[string]bool{}
	for _, tool := range slices.Concat(diskHostTools, unprivilegedDiskHostTools) {
		installed[tool.program] = true
	}
	return &fakeHost{installed: installed, results: map[string]fakeResult{}}
}

func (h *fakeHost) bubblewrap(uid int) *Bubblewrap {
	return &Bubblewrap{RunCommand: h.run, LookPath: h.lookPath, CurrentUID: func() int { return uid }}
}

// TestBubblewrapCommandLine: the tree's /usr and /etc read-only, the work
// directory at /work, an environment of frostroot's alone, and the
// program's arguments after "--", every path an argument of its own
// however hostile.
func TestBubblewrapCommandLine(t *testing.T) {
	run := ToolRun{
		TreeDir:         "/var/tmp/frostroot 1000/build-1/disk-tools/tree",
		WorkDir:         "/var/tmp/frostroot 1000/build-1/it's --here",
		Tool:            "mke2fs",
		Args:            []string{"-d", "image.tar", "-E", "hash_seed=1 2", "--unshare-all"},
		SourceDateEpoch: 1758067200,
	}
	args := bubblewrapCommand(run)
	joined := strings.Join(args, "\x00")
	for _, want := range [][]string{
		{"--unshare-all"}, {"--die-with-parent"}, {"--new-session"},
		{"--ro-bind", run.TreeDir + "/usr", "/usr"},
		{"--ro-bind", run.TreeDir + "/etc", "/etc"},
		{"--symlink", "usr/bin", "/bin"}, {"--symlink", "usr/lib64", "/lib64"},
		{"--proc", "/proc"}, {"--dev", "/dev"}, {"--tmpfs", "/tmp"},
		{"--bind", run.WorkDir, "/work"},
		{"--chdir", "/work"},
		{"--clearenv"},
		{"--setenv", "PATH", "/usr/sbin:/usr/bin:/sbin:/bin"},
		{"--setenv", "LC_ALL", "C.UTF-8"},
		{"--setenv", "TZ", "UTC"},
		{"--setenv", "MTOOLS_SKIP_CHECK", "1"},
		{"--setenv", "SOURCE_DATE_EPOCH", "1758067200"},
		{"--", "mke2fs", "-d", "image.tar", "-E", "hash_seed=1 2", "--unshare-all"},
	} {
		if !strings.Contains("\x00"+joined+"\x00", "\x00"+strings.Join(want, "\x00")+"\x00") {
			t.Errorf("bwrap arguments lack %q:\n%q", want, args)
		}
	}
	// The program's own --unshare-all is its argument, not bwrap's: bwrap
	// reads nothing after "--".
	if separator := slices.Index(args, "--"); separator < 0 || args[separator+1] != "mke2fs" || slices.Index(args[:separator], "--clearenv") < 0 {
		t.Errorf("the program must follow \"--\", after bwrap's own options: %q", args)
	}
	if clearenv, setenv := slices.Index(args, "--clearenv"), slices.Index(args, "--setenv"); clearenv > setenv {
		t.Errorf("--clearenv must come before the first --setenv: %q", args)
	}
	var variables []string
	for index, arg := range args {
		if arg == "--setenv" {
			variables = append(variables, args[index+1])
		}
	}
	if want := []string{"PATH", "LC_ALL", "TZ", "MTOOLS_SKIP_CHECK", "SOURCE_DATE_EPOCH"}; !slices.Equal(variables, want) {
		t.Errorf("the environment is %q, want exactly %q", variables, want)
	}
	// Without an instant there is no SOURCE_DATE_EPOCH at all, rather than
	// a zero that every tool would read as 1970.
	run.SourceDateEpoch = 0
	if args := bubblewrapCommand(run); slices.Contains(args, "SOURCE_DATE_EPOCH") {
		t.Errorf("SOURCE_DATE_EPOCH set without an instant: %q", args)
	}
}

func TestBubblewrapRunsTheProgramAndRepeatsItsLastLineOnFailure(t *testing.T) {
	host := newFakeDiskHost()
	host.results["bwrap"] = fakeResult{stdout: "mke2fs 1.47.2 (1-Jan-2025)\n"}
	var stdout, stderr bytes.Buffer
	run := ToolRun{TreeDir: "/tree", WorkDir: "/work dir", Tool: "mke2fs", Args: []string{"-V"}, Stdout: &stdout, Stderr: &stderr}
	if err := host.bubblewrap(1000).Run(context.Background(), run); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if want := "bwrap " + strings.Join(bubblewrapCommand(run), " "); !slices.Equal(host.ran, []string{want}) {
		t.Errorf("ran %q, want %q", host.ran, want)
	}
	if stdout.String() != "mke2fs 1.47.2 (1-Jan-2025)\n" {
		t.Errorf("stdout = %q, want the program's", stdout.String())
	}

	host.results["bwrap"] = fakeResult{stderr: "mke2fs: Pathname can't be converted\nmke2fs: aborting\n", err: errors.New("exit status 1")}
	err := host.bubblewrap(1000).Run(context.Background(), ToolRun{TreeDir: "/tree", WorkDir: "/work", Tool: "mke2fs"})
	if err == nil || !strings.Contains(err.Error(), "mke2fs: exit status 1: mke2fs: aborting") {
		t.Errorf("Run = %v, want the program, the exit status and its last line", err)
	}
	// Discarded output is still read for the error.
	if err := host.bubblewrap(1000).Run(context.Background(), ToolRun{TreeDir: "/tree", WorkDir: "/work", Tool: "mke2fs", Stdout: io.Discard}); err == nil {
		t.Error("Run = nil, want the failure")
	}
}

// TestBubblewrapPreflightNamesEveryMissingProgram: one error that says all
// of it, and the one apt line that fixes it. Root maps no subordinate ids,
// so it is not asked for unshare's helpers.
func TestBubblewrapPreflightNamesEveryMissingProgram(t *testing.T) {
	host := newFakeDiskHost()
	for _, program := range []string{"bwrap", "unshare", "newuidmap", "newgidmap"} {
		delete(host.installed, program)
	}
	err := host.bubblewrap(1000).Preflight()
	if !errors.Is(err, ErrNoDiskTool) {
		t.Fatalf("Preflight = %v, want ErrNoDiskTool", err)
	}
	if want := "bwrap, unshare, newuidmap, newgidmap; install with: sudo apt install bubblewrap util-linux uidmap"; !strings.Contains(err.Error(), want) {
		t.Errorf("Preflight = %v, want it to end %q", err, want)
	}
	err = host.bubblewrap(0).Preflight()
	if !errors.Is(err, ErrNoDiskTool) || !strings.HasSuffix(err.Error(), "bwrap; install with: sudo apt install bubblewrap") {
		t.Errorf("Preflight as root = %v, want only bwrap named", err)
	}
	host.installed["bwrap"] = true
	if err := host.bubblewrap(0).Preflight(); err != nil {
		t.Errorf("Preflight as root with bwrap = %v, want nil", err)
	}
	if err := host.bubblewrap(1000).Preflight(); !errors.Is(err, ErrNoDiskTool) {
		t.Errorf("Preflight unprivileged without unshare's helpers = %v, want ErrNoDiskTool", err)
	}
	if err := newFakeDiskHost().bubblewrap(1000).Preflight(); err != nil {
		t.Errorf("Preflight on a ready host = %v, want nil", err)
	}
	if len(host.ran) != 0 {
		t.Errorf("Preflight ran %q; it only looks programs up", host.ran)
	}
}

// TestBubblewrapRemovesTheTreeInTheNamespaceThatMadeIt: unprivileged, the
// tree's files belong to the subordinate ids mmdebstrap mapped, and rm
// runs in a namespace that maps them again; root's rm needs none.
func TestBubblewrapRemovesTheTreeInTheNamespaceThatMadeIt(t *testing.T) {
	tree := "/var/tmp/frostroot-1000/build-1/disk tools/tree"
	for _, testCase := range []struct {
		uid  int
		want string
	}{
		{0, "rm -rf -- " + tree},
		{1000, "unshare --map-root-user --map-auto -- rm -rf -- " + tree},
	} {
		host := newFakeDiskHost()
		if err := host.bubblewrap(testCase.uid).RemoveTree(context.Background(), tree); err != nil {
			t.Errorf("uid %d: RemoveTree = %v, want nil", testCase.uid, err)
		}
		if !slices.Equal(host.ran, []string{testCase.want}) {
			t.Errorf("uid %d ran %q, want %q", testCase.uid, host.ran, testCase.want)
		}
	}

	host := newFakeDiskHost()
	host.results["rm"] = fakeResult{stderr: "rm: cannot remove 'tree/usr': Permission denied\n", err: errors.New("exit status 1")}
	err := host.bubblewrap(0).RemoveTree(context.Background(), tree)
	if err == nil || !strings.Contains(err.Error(), "removing "+tree+": exit status 1: rm: cannot remove") {
		t.Errorf("RemoveTree = %v, want rm's own words", err)
	}

	// Nothing frostroot did not make under a work directory: a relative
	// path or the root is a bug, and no rm runs for it.
	for _, bad := range []string{"", "tree", "/", "//", "/../"} {
		host := newFakeDiskHost()
		if err := host.bubblewrap(0).RemoveTree(context.Background(), bad); err == nil || len(host.ran) != 0 {
			t.Errorf("RemoveTree(%q) = %v and ran %q, want a refusal and no command", bad, err, host.ran)
		}
	}
}

// sampleToolsDpkgStatus lists two packages of a disk tools tree.
const sampleToolsDpkgStatus = "Package: mtools\nStatus: install ok installed\nArchitecture: amd64\nVersion: 4.0.49-1\n\n" +
	"Package: e2fsprogs\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1.47.2-3ubuntu1\n"

// Checksums the sample tree's apt index records for its packages.
const (
	sampleE2fsprogsSHA256 = "1111111111111111111111111111111111111111111111111111111111111111"
	sampleMtoolsSHA256    = "2222222222222222222222222222222222222222222222222222222222222222"
)

// sampleToolsAptLists returns the Packages index the tree's bootstrap leaves
// in /var/lib/apt/lists, named for jammy as sampleAptLists is, and renamed
// by listsForSpec to the tree's own suite.
func sampleToolsAptLists() map[string]string {
	return map[string]string{
		"archive.ubuntu.com_ubuntu_dists_jammy_main_binary-amd64_Packages": "Package: e2fsprogs\nArchitecture: amd64\nVersion: 1.47.2-3ubuntu1\n" +
			"Filename: pool/main/e/e2fsprogs/e2fsprogs_1.47.2-3ubuntu1_amd64.deb\nSize: 600000\nSHA256: " + sampleE2fsprogsSHA256 + "\n\n" +
			"Package: mtools\nArchitecture: amd64\nVersion: 4.0.49-1\n" +
			"Filename: pool/universe/m/mtools/mtools_4.0.49-1_amd64.deb\nSize: 200000\nSHA256: " + sampleMtoolsSHA256 + "\n",
	}
}

// sampleDiskRecipe is sampleRecipe as a disk recipe: no [wsl], a target.
func sampleDiskRecipe() recipe.Recipe {
	return recipe.Recipe{
		Image:    recipe.Image{Name: "cpp-lab", Release: "24.04", Arch: "amd64", Target: recipe.TargetDisk},
		User:     recipe.User{Name: "student", Sudo: true},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Europe/Istanbul"},
		Packages: recipe.Packages{Include: []string{"git", "build-essential", "cmake"}},
	}
}

// fakeToolRunner records what a build asks of the runner, and removes the
// tree as rm would.
type fakeToolRunner struct {
	preflightErr error
	removeErr    error
	// lockPath, when set, is looked for as the tree is removed: a build
	// removes the tree before it places anything.
	lockPath string

	preflighted          bool
	runs                 []ToolRun
	removed              []string
	lockPresentAtRemoval bool
}

func (f *fakeToolRunner) Preflight() error {
	f.preflighted = true
	return f.preflightErr
}

func (f *fakeToolRunner) Run(_ context.Context, run ToolRun) error {
	f.runs = append(f.runs, run)
	return nil
}

func (f *fakeToolRunner) RemoveTree(_ context.Context, dir string) error {
	f.removed = append(f.removed, dir)
	if _, err := os.Stat(f.lockPath); f.lockPath != "" && err == nil {
		f.lockPresentAtRemoval = true
	}
	if f.removeErr != nil {
		return f.removeErr
	}
	return os.RemoveAll(dir)
}

// buildDiskWith builds sampleDiskRecipe with bootstrapper, runner and
// options.
func buildDiskWith(bootstrapper Bootstrapper, runner ToolRunner, options Options) (Result, error) {
	return (&Builder{Bootstrapper: bootstrapper, Tools: runner}).Build(context.Background(), sampleDiskRecipe(), options)
}

func TestDiskBuildMakesTheToolsTreeAndRecordsItInTheLock(t *testing.T) {
	options, workRoot := newTestOptions(t)
	recorder := &recordingProgress{}
	options.Progress = recorder
	bootstrapper := &fakeBootstrapper{}
	runner := &fakeToolRunner{lockPath: filepath.Join(options.RecipeDir, LockFileName)}
	result, err := buildDiskWith(bootstrapper, runner, options)
	if err != nil {
		t.Fatalf("Build = %v", err)
	}
	if !bootstrapper.treeRan || bootstrapper.runCount != 1 {
		t.Fatalf("the tree ran: %v, the image ran %d times; want both once", bootstrapper.treeRan, bootstrapper.runCount)
	}

	tree := bootstrapper.treeSpec
	resolute, err := distro.Lookup("26.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if tree.Suite != "resolute" || !slices.Equal(tree.SourceLines, resolute.SourceLines("")) {
		t.Errorf("the tree is made from %s %q, want 26.04's three pockets", tree.Suite, tree.SourceLines)
	}
	if tree.Variant != "essential" || tree.InstallRecommends || !slices.Equal(tree.Include, DiskToolsPackages) {
		t.Errorf("the tree is variant %q, recommends %v, with %q; want essential, no recommends, the disk tools", tree.Variant, tree.InstallRecommends, tree.Include)
	}
	if tree.KeyringPath != UbuntuArchiveKeyring || tree.Trusted || tree.Arch != "amd64" {
		t.Errorf("the tree spec is %+v, want the archive keyring, untrusted, amd64", tree)
	}
	if tree.SourceDateEpoch <= 0 || tree.SourceDateEpoch != result.SourceDateEpoch {
		t.Errorf("the tree is frozen at %d, the build at %d", tree.SourceDateEpoch, result.SourceDateEpoch)
	}
	if tree.CaInfoPath != "" || tree.Insecure {
		t.Errorf("the tree's apt trusts %q, insecure %v; want the host's store alone", tree.CaInfoPath, tree.Insecure)
	}
	if want := filepath.Join(workRoot, "build-*", "disk-tools"); !matchesGlob(t, want, tree.WorkDir) || tree.TreeDir != filepath.Join(tree.WorkDir, "tree") || tree.TarballPath != "" {
		t.Errorf("the tree's work directory is %q and the tree %q, want %s and tree below it, with no tarball", tree.WorkDir, tree.TreeDir, want)
	}
	if want := diskToolsHooks(filepath.Join(tree.WorkDir, "stage"), filepath.Join(tree.WorkDir, "stage", "dpkg-status")); !slices.Equal(tree.CustomizeHooks, want) {
		t.Errorf("the tree's hooks are %q, want %q", tree.CustomizeHooks, want)
	}
	// The image is built as it was, after the tree, for the recipe's own
	// release.
	if image := bootstrapper.lastSpec; image.Suite != "noble" || image.TreeDir != "" || image.Variant != "" || image.TarballPath == "" {
		t.Errorf("the image spec is %+v, want noble, a tarball, the default variant", image)
	}

	lock, err := recipe.LoadLock(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Target != recipe.TargetDisk {
		t.Errorf("lock target = %q, want disk", lock.Target)
	}
	wantTools := []recipe.LockPackage{
		{Name: "e2fsprogs", Version: "1.47.2-3ubuntu1", Arch: "amd64", SHA256: sampleE2fsprogsSHA256, Size: 600000, Filename: "pool/main/e/e2fsprogs/e2fsprogs_1.47.2-3ubuntu1_amd64.deb"},
		{Name: "mtools", Version: "4.0.49-1", Arch: "amd64", SHA256: sampleMtoolsSHA256, Size: 200000, Filename: "pool/universe/m/mtools/mtools_4.0.49-1_amd64.deb"},
	}
	if !slices.Equal(lock.DiskTools, wantTools) {
		t.Errorf("disk_tools = %+v, want %+v", lock.DiskTools, wantTools)
	}
	if len(lock.Packages) != 2 || lock.Packages[0].Name != "git" {
		t.Errorf("packages = %+v, want the image's own", lock.Packages)
	}

	if !runner.preflighted || !slices.Equal(runner.removed, []string{tree.TreeDir}) || len(runner.runs) != 0 {
		t.Errorf("the runner preflighted %v, removed %q, ran %d tools; want preflighted, the tree removed once, nothing run", runner.preflighted, runner.removed, len(runner.runs))
	}
	if runner.lockPresentAtRemoval {
		t.Error("the tree was removed after the lock was placed; it goes before anything lands")
	}
	if result.WorkDir != "" {
		t.Errorf("Result = %+v, want the work directory removed", result)
	}
	var started []Phase
	for _, event := range recorder.ofKind(EventPhaseStarted) {
		started = append(started, event.Phase)
	}
	if want := []Phase{PhaseMakeToolsTree, PhaseWriteLock, PhasePlaceTarball}; !slices.Equal(started, want) {
		t.Errorf("phases started = %v, want %v", started, want)
	}
}

// matchesGlob reports whether path matches pattern.
func matchesGlob(t *testing.T, pattern, path string) bool {
	t.Helper()
	matched, err := filepath.Match(pattern, path)
	if err != nil {
		t.Fatal(err)
	}
	return matched
}

func TestWSLBuildMakesNoToolsTree(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	runner := &fakeToolRunner{}
	result, err := (&Builder{Bootstrapper: bootstrapper, Tools: runner}).Build(context.Background(), sampleRecipe(), options)
	if err != nil {
		t.Fatalf("Build = %v", err)
	}
	if bootstrapper.treeRan || runner.preflighted || len(runner.removed) != 0 {
		t.Errorf("a WSL build touched the tree: ran %v, preflighted %v, removed %q", bootstrapper.treeRan, runner.preflighted, runner.removed)
	}
	lock, err := recipe.LoadLock(result.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if lock.Target != "" || lock.DiskTools != nil {
		t.Errorf("a WSL lock names target %q and disk tools %+v; want neither", lock.Target, lock.DiskTools)
	}
}

// TestDiskBuildTrustsWhatTheImageBuildTrusts: apt fetches the tree on the
// host too, so the bundle --ca-bundle and [certificates] give the image's
// apt reaches the tree's, and --insecure as well.
func TestDiskBuildTrustsWhatTheImageBuildTrusts(t *testing.T) {
	options, _ := newTestOptions(t)
	options.ExtraTrustPEM = certificatePEM(t, "Build-only Proxy CA")
	options.Insecure = true
	bootstrapper := &fakeBootstrapper{}
	if _, err := buildDiskWith(bootstrapper, &fakeToolRunner{}, options); err != nil {
		t.Fatalf("Build = %v", err)
	}
	tree := bootstrapper.treeSpec
	if tree.CaInfoPath != filepath.Join(tree.WorkDir, "apt-ca-bundle.pem") || !strings.Contains(bootstrapper.treeCaInfo, string(options.ExtraTrustPEM)) {
		t.Errorf("the tree's apt was pointed at %q holding %d bytes; want the bundle in the tree's work directory, with the extra authority", tree.CaInfoPath, len(bootstrapper.treeCaInfo))
	}
	if !tree.Insecure {
		t.Error("--insecure did not reach the tree's apt")
	}
}

func TestDiskBuildRemovesTheToolsTreeWhenTheImageFails(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{runErr: errors.New("mmdebstrap exploded")}
	runner := &fakeToolRunner{}
	result, err := buildDiskWith(bootstrapper, runner, options)
	if err == nil || !strings.Contains(err.Error(), "mmdebstrap exploded") {
		t.Fatalf("Build = %v, want the image's failure", err)
	}
	if !slices.Equal(runner.removed, []string{bootstrapper.treeSpec.TreeDir}) {
		t.Errorf("removed %q, want the tree", runner.removed)
	}
	if result.WorkDir == "" {
		t.Fatal("a failed build keeps its work directory")
	}
	if _, err := os.Stat(bootstrapper.treeSpec.TreeDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the tree is still there (Stat error %v); only the log stays", err)
	}
	assertNoDiskBuildOutput(t, options)
}

// assertNoDiskBuildOutput fails if a failed disk build left a lock, a
// tarball or a temporary file.
func assertNoDiskBuildOutput(t *testing.T, options Options) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(options.RecipeDir, LockFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a lock was written, or cannot be looked for: %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(options.RecipeDir, "dist")); len(entries) != 0 {
		t.Errorf("dist/ holds %v, want nothing", entries)
	}
	assertNoTemporaryFiles(t, options.RecipeDir)
}

func TestDiskBuildFailsWhenTheTreeCannotBeMade(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{treeRunErr: &BootstrapError{Err: errors.New("exit status 1"), Tail: "E: Unable to locate package libarchive13t64"}}
	runner := &fakeToolRunner{}
	result, err := buildDiskWith(bootstrapper, runner, options)
	if err == nil || !strings.Contains(err.Error(), "making the disk tools tree: mmdebstrap failed: exit status 1") {
		t.Fatalf("Build = %v, want the tree's failure with mmdebstrap's words", err)
	}
	var bootstrapErr *BootstrapError
	if !errors.As(err, &bootstrapErr) || bootstrapErr.LogFile != "disk-tools/mmdebstrap.log" || bootstrapErr.Program != "mmdebstrap" {
		t.Errorf("Build = %#v, want it to name the tree's own log", err)
	}
	if bootstrapper.runCount != 0 {
		t.Error("the image was built after the tree failed")
	}
	// The partial tree mmdebstrap left goes too; the work directory stays.
	if !slices.Equal(runner.removed, []string{bootstrapper.treeSpec.TreeDir}) || result.WorkDir == "" {
		t.Errorf("removed %q, work directory %q; want the tree removed and the directory kept", runner.removed, result.WorkDir)
	}
	assertNoDiskBuildOutput(t, options)
}

func TestDiskBuildFailsWhenTheTreeCannotBeRemoved(t *testing.T) {
	options, _ := newTestOptions(t)
	runner := &fakeToolRunner{removeErr: errors.New("rm: cannot remove 'tree/usr': Permission denied")}
	result, err := buildDiskWith(&fakeBootstrapper{}, runner, options)
	if err == nil || !strings.Contains(err.Error(), "removing the disk tools tree: rm: cannot remove") {
		t.Fatalf("Build = %v, want the removal's failure", err)
	}
	if result.WorkDir == "" {
		t.Error("a failed build keeps its work directory")
	}
	assertNoDiskBuildOutput(t, options)
}

func TestDiskBuildPreflightsTheRunnerBeforeAnyWork(t *testing.T) {
	options, workRoot := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	runner := &fakeToolRunner{preflightErr: fmt.Errorf("%w: bwrap; install with: sudo apt install bubblewrap", ErrNoDiskTool)}
	result, err := buildDiskWith(bootstrapper, runner, options)
	if !errors.Is(err, ErrNoDiskTool) {
		t.Fatalf("Build = %v, want ErrNoDiskTool", err)
	}
	if bootstrapper.treeRan || bootstrapper.runCount != 0 || result.WorkDir != "" {
		t.Errorf("work was done before the preflight failed: tree %v, image %d, work directory %q", bootstrapper.treeRan, bootstrapper.runCount, result.WorkDir)
	}
	assertNotCreated(t, workRoot)
}

func TestDiskBuildRefusesWhatCannotBuildItYet(t *testing.T) {
	options, _ := newTestOptions(t)
	bootstrapper := &fakeBootstrapper{}
	fedoraDisk := sampleDiskRecipe()
	fedoraDisk.Image.Distro, fedoraDisk.Image.Release = "fedora", "44"
	_, err := (&Builder{Bootstrapper: bootstrapper, Fedora: newFakeFedoraBootstrapper(), Tools: &fakeToolRunner{}}).Build(context.Background(), fedoraDisk, options)
	if !errors.Is(err, ErrFedoraOption) || !strings.Contains(err.Error(), "later version") {
		t.Errorf("a Fedora disk build = %v, want ErrFedoraOption saying it comes later", err)
	}
	_, err = (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleDiskRecipe(), options)
	if err == nil || !strings.Contains(err.Error(), "disk tool runner") {
		t.Errorf("a disk build without a runner = %v, want a refusal naming it", err)
	}
	if bootstrapper.treeRan || bootstrapper.runCount != 0 {
		t.Error("a refused build reached the bootstrapper")
	}
}

func TestToolsTreeProgressRelabelsTheBootstrapsOutput(t *testing.T) {
	recorder := &recordingProgress{}
	progress := toolsTreeProgress{recorder}
	progress.Report(ProgressEvent{Phase: PhaseDownload, Kind: EventPhaseStarted})
	progress.Report(ProgressEvent{Phase: PhaseDownload, Kind: EventProgress, Done: 1, Total: 2, Unit: UnitBytes})
	progress.Report(ProgressEvent{Phase: PhaseDownload, Kind: EventLogLine, Line: "Get:1 http://archive.ubuntu.com/ubuntu resolute InRelease"})
	progress.Report(ProgressEvent{Kind: EventLogFile, Line: "/w/disk-tools/mmdebstrap.log"})
	progress.Report(ProgressEvent{Phase: PhaseDownload, Kind: EventPhaseFinished})
	want := []ProgressEvent{
		{Phase: PhaseMakeToolsTree, Kind: EventLogLine, Line: "Get:1 http://archive.ubuntu.com/ubuntu resolute InRelease"},
		{Kind: EventLogFile, Line: "/w/disk-tools/mmdebstrap.log"},
	}
	if !slices.Equal(recorder.events, want) {
		t.Errorf("events = %+v, want %+v", recorder.events, want)
	}
}
