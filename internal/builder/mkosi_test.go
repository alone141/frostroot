package builder

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeHost answers LookPath from a set of installed programs and runs
// programs from a table of canned results.
type fakeHost struct {
	installed map[string]bool
	results   map[string]fakeResult // by program name
	ran       []string
}

type fakeResult struct {
	stdout, stderr string
	err            error
}

func newFakeHost() *fakeHost {
	installed := map[string]bool{}
	for _, tool := range slices.Concat(fedoraHostTools, unprivilegedHostTools) {
		installed[tool.program] = true
	}
	return &fakeHost{
		installed: installed,
		results: map[string]fakeResult{
			"mkosi": {stdout: "mkosi 20.2\n"},
			"dnf":   {stdout: "4.14.0\n"},
		},
	}
}

func (h *fakeHost) lookPath(program string) (string, error) {
	if h.installed[program] {
		return "/usr/bin/" + program, nil
	}
	return "", exec.ErrNotFound
}

func (h *fakeHost) run(_ context.Context, program string, args, _ []string, stdout, stderr io.Writer) error {
	h.ran = append(h.ran, program+" "+strings.Join(args, " "))
	result := h.results[program]
	_, _ = io.WriteString(stdout, result.stdout)
	_, _ = io.WriteString(stderr, result.stderr)
	return result.err
}

func (h *fakeHost) mkosi(uid int) *Mkosi {
	return &Mkosi{RunCommand: h.run, LookPath: h.lookPath, CurrentUID: func() int { return uid }}
}

func TestMkosiPreflightAcceptsAReadyHost(t *testing.T) {
	// A work root not made yet, below a directory anyone may enter, as the
	// first build on a host has it.
	workRoot := filepath.Join(os.TempDir(), "frostroot-preflight-never-created", "frostroot")
	for _, uid := range []int{0, 1000} {
		host := newFakeHost()
		if err := host.mkosi(uid).Preflight(context.Background(), FedoraSpec{WorkDir: workRoot}); err != nil {
			t.Errorf("uid %d: Preflight = %v, want nil", uid, err)
		}
		if want := []string{"mkosi --version", "dnf --version"}; !slices.Equal(host.ran, want) {
			t.Errorf("uid %d ran %q, want %q", uid, host.ran, want)
		}
	}
}

// TestMkosiPreflightNamesEveryMissingProgram: one error that says all of
// it, and the one apt line that fixes it, rather than one program a run.
func TestMkosiPreflightNamesEveryMissingProgram(t *testing.T) {
	host := newFakeHost()
	for _, program := range []string{"mkosi", "createrepo_c", "newuidmap", "newgidmap"} {
		delete(host.installed, program)
	}
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{})
	if !errors.Is(err, ErrNoFedoraTool) {
		t.Fatalf("Preflight = %v, want ErrNoFedoraTool", err)
	}
	if want := "mkosi, createrepo_c, newuidmap, newgidmap; install with: sudo apt install mkosi createrepo-c uidmap"; !strings.Contains(err.Error(), want) {
		t.Errorf("Preflight = %v, want it to end %q", err, want)
	}
	if len(host.ran) != 0 {
		t.Errorf("ran %q before the missing programs were installed", host.ran)
	}
}

// TestMkosiPreflightAsRootNeedsNoIdMapping: root's mkosi maps no
// subordinate ids, so uidmap is not asked for.
func TestMkosiPreflightAsRootNeedsNoIdMapping(t *testing.T) {
	host := newFakeHost()
	delete(host.installed, "newuidmap")
	delete(host.installed, "newgidmap")
	if err := host.mkosi(0).Preflight(context.Background(), FedoraSpec{}); err != nil {
		t.Errorf("Preflight as root = %v, want nil", err)
	}
}

func TestMkosiPreflightRefusesAnUnknownMkosi(t *testing.T) {
	host := newFakeHost()
	host.results["mkosi"] = fakeResult{stdout: "mkosi 25.3\n"}
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{})
	if !errors.Is(err, ErrMkosiVersion) || !strings.Contains(err.Error(), `"25.3"`) || !strings.Contains(err.Error(), "20.2") {
		t.Errorf("Preflight = %v, want ErrMkosiVersion naming 25.3 and 20.2", err)
	}
}

// TestMkosiPreflightRefusesADnfThatDoesNotRun: dnf is Python, and a host
// whose python3 lacks the dnf module has the program but not the tool.
func TestMkosiPreflightRefusesADnfThatDoesNotRun(t *testing.T) {
	host := newFakeHost()
	host.results["dnf"] = fakeResult{
		stderr: "Traceback (most recent call last):\n  File \"/usr/bin/dnf\", line 61, in <module>\nModuleNotFoundError: No module named 'dnf'\n",
		err:    errors.New("exit status 1"),
	}
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{})
	if !errors.Is(err, ErrNoFedoraTool) || !strings.Contains(err.Error(), "No module named 'dnf'") {
		t.Errorf("Preflight = %v, want ErrNoFedoraTool with dnf's own last line", err)
	}
}

func TestMkosiPreflightRefusesAWorkRootTheNamespaceCannotEnter(t *testing.T) {
	unreachable := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(unreachable, 0o750); err != nil {
		t.Fatal(err)
	}
	host := newFakeHost()
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{WorkDir: filepath.Join(unreachable, "frostroot")})
	if !errors.Is(err, ErrBadWorkRoot) || !strings.Contains(err.Error(), "mkosi runs in a user namespace") {
		t.Errorf("Preflight = %v, want ErrBadWorkRoot naming mkosi", err)
	}
	// Root's mkosi is not in a user namespace of subordinate ids.
	if err := host.mkosi(0).Preflight(context.Background(), FedoraSpec{WorkDir: filepath.Join(unreachable, "frostroot")}); err != nil {
		t.Errorf("Preflight as root = %v, want nil", err)
	}
}

func TestMkosiVersion(t *testing.T) {
	for output, want := range map[string]string{"mkosi 20.2\n": "20.2", "mkosi 25.3": "25.3", "something else\n": "something else"} {
		if got := mkosiVersion(output); got != want {
			t.Errorf("mkosiVersion(%q) = %q, want %q", output, got, want)
		}
	}
}

// TestMkosiRunMakesToolsTreeThenImageThenCompresses: the order, the command
// lines, the umask, and gzip's output going into the tarball.
func TestMkosiRunMakesToolsTreeThenImageThenCompresses(t *testing.T) {
	workDir := t.TempDir()
	spec := FedoraSpec{
		WorkDir:         workDir,
		ToolsConfigDir:  filepath.Join(workDir, "mkosi-tools"),
		ImageConfigDir:  filepath.Join(workDir, "mkosi-image"),
		CacheDir:        filepath.Join(workDir, "cache"),
		ToolsOutputDir:  filepath.Join(workDir, "tools"),
		ToolsRecordPath: filepath.Join(workDir, "tools.installed"),
		ImageOutputDir:  filepath.Join(workDir, "image"),
		SourceDateEpoch: 1790600000,
		TarballPath:     filepath.Join(workDir, "image.tar.gz"),
	}
	var phases []string
	spec.Progress = ProgressFunc(func(event ProgressEvent) {
		if event.Kind == EventPhaseStarted {
			phases = append(phases, event.Phase.Title())
		}
	})
	var commands [][]string
	run := func(_ context.Context, program string, args, _ []string, stdout, _ io.Writer) error {
		commands = append(commands, append([]string{program}, args...))
		switch program {
		case "sh":
			// The tools tree's build prints markers of its own, which
			// must not move the image's phases.
			if slices.Contains(args, "--directory="+spec.ToolsConfigDir) && slices.Contains(args, "build") {
				_, _ = io.WriteString(stdout, "Running transaction\n‣  Creating tar archive /z…\n")
				// The finalize script's record, in mkosi's output directory.
				if err := os.MkdirAll(spec.ToolsOutputDir, 0o755); err != nil {
					return err
				}
				if err := os.WriteFile(filepath.Join(spec.ToolsOutputDir, "tools.installed"), []byte("dnf5|0|5.4.6.0|1.fc44|x86_64|x\n"), 0o644); err != nil {
					return err
				}
			}
			if slices.Contains(args, "--directory="+spec.ImageConfigDir) {
				_, _ = io.WriteString(stdout, "‣  Installing Fedora\nRunning transaction\n‣  Running postinstall script /x…\n‣  Creating tar archive /y…\n")
			}
		case "gzip":
			_, _ = io.WriteString(stdout, "compressed")
		}
		return nil
	}
	if err := (&Mkosi{RunCommand: run}).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 4 {
		t.Fatalf("ran %d commands, want mkosi twice, gzip, and mkosi's clean:\n%q", len(commands), commands)
	}
	wantClean := []string{"sh", "-c", `umask 022 && exec mkosi "$@"`, "mkosi", "--directory=" + spec.ToolsConfigDir, "--output-dir=" + spec.ToolsOutputDir, "-f", "clean"}
	if !slices.Equal(commands[3], wantClean) {
		t.Errorf("last command =\n%q\nwant mkosi removing the tools tree\n%q", commands[3], wantClean)
	}
	wantTools := []string{"sh", "-c", `umask 022 && exec mkosi "$@"`, "mkosi",
		"--directory=" + spec.ToolsConfigDir,
		"--package-manager-tree=" + filepath.Join(spec.ToolsConfigDir, fedoraPackageManagerTree),
		"--cache-dir=" + spec.CacheDir,
		"--output-dir=" + spec.ToolsOutputDir,
		"--finalize-script=" + filepath.Join(spec.ToolsConfigDir, fedoraFinalizeName),
		"--force", "build"}
	if !slices.Equal(commands[0], wantTools) {
		t.Errorf("tools tree command =\n%q\nwant\n%q", commands[0], wantTools)
	}
	for _, want := range []string{
		"--tools-tree=" + filepath.Join(spec.ToolsOutputDir, "tools"),
		"--source-date-epoch=1790600000",
		"--postinst-script=" + filepath.Join(spec.ImageConfigDir, fedoraProvisionName),
		"--finalize-script=" + filepath.Join(spec.ImageConfigDir, fedoraFinalizeName),
	} {
		if !slices.Contains(commands[1], want) {
			t.Errorf("image command lacks %q:\n%q", want, commands[1])
		}
	}
	if want := []string{"gzip", "-n", "-c", filepath.Join(spec.ImageOutputDir, "image.tar")}; !slices.Equal(commands[2], want) {
		t.Errorf("gzip command = %q, want %q: -n, so that the header carries no name and no time", commands[2], want)
	}
	if content, err := os.ReadFile(spec.TarballPath); err != nil || string(content) != "compressed" {
		t.Errorf("tarball = %q, %v; want gzip's output", content, err)
	}
	if want := []string{"Make the tools tree", "Download packages", "Install requested packages", "Provision user, locale and timezone", "Create tarball"}; !slices.Equal(phases, want) {
		t.Errorf("phases started = %q, want %q", phases, want)
	}
	if record, err := os.ReadFile(spec.ToolsRecordPath); err != nil || !strings.HasPrefix(string(record), "dnf5|") {
		t.Errorf("the tools record = %q, %v; want it moved out of what mkosi's clean removes", record, err)
	}
	if log, err := os.ReadFile(filepath.Join(workDir, MkosiLogFileName)); err != nil || !strings.Contains(string(log), "Running transaction") {
		t.Errorf("mkosi.log = %q, %v; want mkosi's output", log, err)
	}
}

func TestMkosiRunFailureCarriesMkosisOwnWords(t *testing.T) {
	workDir := t.TempDir()
	spec := FedoraSpec{WorkDir: workDir, CacheDir: filepath.Join(workDir, "c"), ToolsOutputDir: filepath.Join(workDir, "t"), ImageOutputDir: filepath.Join(workDir, "i"), TarballPath: filepath.Join(workDir, "image.tar.gz")}
	run := func(_ context.Context, program string, _, _ []string, stdout, _ io.Writer) error {
		_, _ = io.WriteString(stdout, "No match for argument: gti\nFailed to resolve the transaction:\n")
		return errors.New("exit status 1")
	}
	var cleaned bool
	failing := run
	run = func(ctx context.Context, program string, args, environment []string, stdout, stderr io.Writer) error {
		if slices.Contains(args, "clean") {
			cleaned = true
			return nil
		}
		return failing(ctx, program, args, environment, stdout, stderr)
	}
	err := (&Mkosi{RunCommand: run}).Run(context.Background(), spec)
	if !cleaned {
		t.Error("a failed build left the tools tree for the user, who cannot remove it")
	}
	var bootstrapErr *BootstrapError
	if !errors.As(err, &bootstrapErr) || bootstrapErr.Program != "mkosi" || bootstrapErr.LogFile != MkosiLogFileName || !strings.Contains(bootstrapErr.Tail, "No match for argument: gti") {
		t.Fatalf("Run = %#v, want a BootstrapError from mkosi carrying its output", err)
	}
	if !strings.Contains(err.Error(), "mkosi failed: mkosi could not make the tools tree") {
		t.Errorf("error = %v", err)
	}
	if line, found := FirstErrorLine(filepath.Join(workDir, MkosiLogFileName)); !found || line.Text != "No match for argument: gti" {
		t.Errorf("FirstErrorLine = %+v, %v; want dnf's own explanation", line, found)
	}
}

// TestMkosiRunOfflineIndexesTheLocalRepositoriesFirst: the host's
// createrepo_c indexes each local repository before any tools tree exists,
// both of mkosi's builds bind the local mirror, nothing is downloaded, and
// the build's own package cache goes with the tools tree.
func TestMkosiRunOfflineIndexesTheLocalRepositoriesFirst(t *testing.T) {
	workDir := t.TempDir()
	spec := FedoraSpec{
		WorkDir:           workDir,
		ToolsConfigDir:    filepath.Join(workDir, "mkosi-tools"),
		ImageConfigDir:    filepath.Join(workDir, "mkosi-image"),
		CacheDir:          filepath.Join(workDir, "package-cache"),
		ToolsOutputDir:    filepath.Join(workDir, "tools"),
		ToolsRecordPath:   filepath.Join(workDir, "tools.installed"),
		ImageOutputDir:    filepath.Join(workDir, "image"),
		SourceDateEpoch:   1790600000,
		TarballPath:       filepath.Join(workDir, "image.tar.gz"),
		Offline:           true,
		LocalMirror:       filepath.Join(workDir, "repos"),
		LocalRepositories: []string{filepath.Join(workDir, "repos", "tools", "fedora"), filepath.Join(workDir, "repos", "image", "fedora")},
	}
	var phases []string
	spec.Progress = ProgressFunc(func(event ProgressEvent) {
		if event.Kind == EventPhaseStarted {
			phases = append(phases, event.Phase.Title())
		}
	})
	var commands [][]string
	run := func(_ context.Context, program string, args, _ []string, stdout, _ io.Writer) error {
		commands = append(commands, append([]string{program}, args...))
		if program == "sh" && slices.Contains(args, "--directory="+spec.ToolsConfigDir) && slices.Contains(args, "build") {
			if err := os.MkdirAll(spec.ToolsOutputDir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(spec.ToolsOutputDir, "tools.installed"), []byte("dnf5|0|5.4.6.0|1.fc44|x86_64|x\n"), 0o644)
		}
		if program == "sh" && slices.Contains(args, "--directory="+spec.ImageConfigDir) {
			_, _ = io.WriteString(stdout, "Running transaction\n‣  Running postinstall script /x…\n‣  Creating tar archive /y…\n")
		}
		return nil
	}
	if err := (&Mkosi{RunCommand: run}).Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 6 {
		t.Fatalf("ran %d commands, want createrepo_c twice, mkosi twice, gzip, and mkosi's clean:\n%q", len(commands), commands)
	}
	for index, dir := range spec.LocalRepositories {
		want := []string{"sh", "-c", `umask 022 && exec createrepo_c "$@"`, "createrepo_c", "--quiet", "--", dir}
		if !slices.Equal(commands[index], want) {
			t.Errorf("command %d =\n%q\nwant\n%q", index, commands[index], want)
		}
	}
	for _, build := range [][]string{commands[2], commands[3]} {
		if !slices.Contains(build, "--local-mirror="+spec.LocalMirror) || !slices.Contains(build, "--cache-dir="+spec.CacheDir) {
			t.Errorf("mkosi build = %q, want the local mirror bound and the build's own cache", build)
		}
	}
	wantClean := []string{"sh", "-c", `umask 022 && exec mkosi "$@"`, "mkosi", "--directory=" + spec.ToolsConfigDir, "--output-dir=" + spec.ToolsOutputDir, "--cache-dir=" + spec.CacheDir, "-ff", "clean"}
	if !slices.Equal(commands[5], wantClean) {
		t.Errorf("last command =\n%q\nwant mkosi removing the tools tree and the build's cache\n%q", commands[5], wantClean)
	}
	if want := []string{"Make the tools tree", "Install requested packages", "Provision user, locale and timezone", "Create tarball"}; !slices.Equal(phases, want) {
		t.Errorf("phases started = %q, want %q", phases, want)
	}
}
