package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// createKeyringFile creates a stand-in keyring so that these tests need no
// ubuntu-keyring package on the host.
func createKeyringFile(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "keyring.gpg")
	if err := os.WriteFile(path, []byte("keys"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// createReachableDir returns a temporary directory that mmdebstrap's user
// namespace could enter. t.TempDir makes its parent 0700, so the parents under
// the system temporary directory are opened up.
func createReachableDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	systemTempDir := filepath.Clean(os.TempDir())
	for ancestor := directory; strings.HasPrefix(ancestor, systemTempDir+string(filepath.Separator)); ancestor = filepath.Dir(ancestor) {
		if err := os.Chmod(ancestor, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return directory
}

// createUnreachableDir returns a directory that mmdebstrap's user namespace
// cannot enter, like a 0750 home directory.
func createUnreachableDir(t *testing.T) string {
	t.Helper()
	directory := filepath.Join(createReachableDir(t), "closed")
	if err := os.Mkdir(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	return directory
}

// runnableSpec returns a spec that Mmdebstrap.Run accepts: a reachable work
// directory and an existing keyring.
func runnableSpec(t *testing.T) BootstrapSpec {
	t.Helper()
	workDir := createReachableDir(t)
	return BootstrapSpec{
		Suite:       "noble",
		TarballPath: filepath.Join(workDir, "image.tar.gz"),
		WorkDir:     workDir,
		Arch:        "amd64",
		KeyringPath: createKeyringFile(t),
	}
}

// commandRecord captures what a recordingRunner was asked to run.
type commandRecord struct {
	runCount    int
	program     string
	args        []string
	environment []string
}

// recordingRunner returns a CommandRunner that records its arguments into
// record instead of running anything.
func recordingRunner(record *commandRecord) CommandRunner {
	return func(_ context.Context, program string, args, environment []string, _, _ io.Writer) error {
		record.runCount++
		record.program = program
		record.args = slices.Clone(args)
		record.environment = slices.Clone(environment)
		return nil
	}
}

// uidFunc returns a CurrentUID function that always reports uid.
func uidFunc(uid int) func() int { return func() int { return uid } }

func TestMmdebstrapCommandLine(t *testing.T) {
	bootstrapper := Mmdebstrap{CurrentUID: uidFunc(1000)}
	args, environment := bootstrapper.commandLine(BootstrapSpec{
		Suite:             "jammy",
		SourceLines:       []string{"deb http://a jammy main universe", "deb http://a jammy-updates main universe", "deb http://a jammy-security main universe"},
		Include:           []string{"git", "systemd"},
		CustomizeHooks:    []string{"upload '/w/wsl.conf' /etc/wsl.conf", "download /var/lib/dpkg/status '/w/dpkg-status'"},
		TarballPath:       "/w/image.tar.gz",
		WorkDir:           "/w",
		Arch:              "amd64",
		InstallRecommends: true,
		KeyringPath:       "/k.gpg",
		SourceDateEpoch:   1758067200,
	})
	wantArgs := []string{
		"--verbose",
		"--mode=unshare",
		"--variant=important",
		"--architectures=amd64",
		"--keyring=/k.gpg",
		`--aptopt=Apt::Install-Recommends "true"`,
		"--include=git,systemd",
		"--customize-hook=upload '/w/wsl.conf' /etc/wsl.conf",
		"--customize-hook=download /var/lib/dpkg/status '/w/dpkg-status'",
		// Positional order matters: suite, target, then every source line.
		"jammy",
		"/w/image.tar.gz",
		"deb http://a jammy main universe",
		"deb http://a jammy-updates main universe",
		"deb http://a jammy-security main universe",
	}
	if !slices.Equal(args, wantArgs) {
		t.Errorf("args =\n%s\nwant\n%s", strings.Join(args, "\n"), strings.Join(wantArgs, "\n"))
	}
	if wantEnvironment := []string{"TMPDIR=/w/tmp", "SOURCE_DATE_EPOCH=1758067200"}; !slices.Equal(environment, wantEnvironment) {
		t.Errorf("environment = %q, want TMPDIR inside the work directory and the epoch", environment)
	}
}

func TestMmdebstrapCommandLineDefaultsAndOptionalFlags(t *testing.T) {
	minimalSpec := BootstrapSpec{Suite: "noble", TarballPath: "/t", WorkDir: "/w", Arch: "amd64"}
	args, environment := (&Mmdebstrap{CurrentUID: uidFunc(1000)}).commandLine(minimalSpec)
	if !slices.Contains(args, "--keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg") {
		t.Errorf("args = %q, want the Ubuntu archive keyring by default", args)
	}
	joinedArgs := strings.Join(args, " ")
	if strings.Contains(joinedArgs, "Install-Recommends") {
		t.Errorf("args = %q, want no Recommends option when it is off", joinedArgs)
	}
	if strings.Contains(joinedArgs, "--include") {
		t.Errorf("args = %q, want no --include when there is nothing to include", joinedArgs)
	}
	if wantEnvironment := []string{"TMPDIR=/w/tmp"}; !slices.Equal(environment, wantEnvironment) {
		t.Errorf("environment = %q, want no SOURCE_DATE_EPOCH when the spec sets none", environment)
	}
}

func TestMmdebstrapPassesHooksInOrder(t *testing.T) {
	spec := BootstrapSpec{Suite: "noble", TarballPath: "/t", WorkDir: "/w", Arch: "amd64", CustomizeHooks: []string{"first", "second", "third"}}
	args, _ := (&Mmdebstrap{CurrentUID: uidFunc(1000)}).commandLine(spec)
	var passedHooks []string
	for _, arg := range args {
		if hook, isHook := strings.CutPrefix(arg, "--customize-hook="); isHook {
			passedHooks = append(passedHooks, hook)
		}
	}
	if !slices.Equal(passedHooks, spec.CustomizeHooks) {
		t.Fatalf("hooks passed = %q, want %q", passedHooks, spec.CustomizeHooks)
	}
}

func TestMmdebstrapMode(t *testing.T) {
	testCases := []struct {
		configuredMode string
		currentUID     int
		wantModeArg    string
	}{
		{configuredMode: "", currentUID: 1000, wantModeArg: "--mode=unshare"},
		{configuredMode: "", currentUID: 0, wantModeArg: "--mode=root"},
		{configuredMode: "unshare", currentUID: 0, wantModeArg: "--mode=unshare"},
		{configuredMode: "root", currentUID: 1000, wantModeArg: "--mode=root"},
	}
	for _, testCase := range testCases {
		bootstrapper := Mmdebstrap{Mode: testCase.configuredMode, CurrentUID: uidFunc(testCase.currentUID)}
		args, _ := bootstrapper.commandLine(BootstrapSpec{Suite: "noble", TarballPath: "/t", WorkDir: "/w", Arch: "amd64"})
		if !slices.Contains(args, testCase.wantModeArg) {
			t.Errorf("Mode %q, uid %d: arguments %q lack %q", testCase.configuredMode, testCase.currentUID, args, testCase.wantModeArg)
		}
	}
}

func TestMmdebstrapRunPreparesTMPDIR(t *testing.T) {
	// mmdebstrap(1): in unshare mode TMPDIR must be world-writable and all its
	// ancestors world-executable, because the namespace's root is a
	// subordinate uid and "other" to the user's files.
	var record commandRecord
	spec := runnableSpec(t)
	bootstrapper := Mmdebstrap{CurrentUID: uidFunc(1000), RunCommand: recordingRunner(&record)}
	if err := bootstrapper.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if record.program != "mmdebstrap" || record.runCount != 1 {
		t.Fatalf("recorded %+v, want mmdebstrap to run once", record)
	}
	temporaryDir := filepath.Join(spec.WorkDir, "tmp")
	temporaryDirInfo, err := os.Stat(temporaryDir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := temporaryDirInfo.Mode(); mode.Perm() != 0o777 || mode&os.ModeSticky == 0 {
		t.Errorf("TMPDIR mode = %v, want sticky and world-writable", mode)
	}
	if !slices.Contains(record.environment, "TMPDIR="+temporaryDir) {
		t.Errorf("environment = %q, want TMPDIR=%s", record.environment, temporaryDir)
	}
}

func TestMmdebstrapRunRefusesUnreachableWorkDir(t *testing.T) {
	spec := runnableSpec(t)
	spec.WorkDir = filepath.Join(createUnreachableDir(t), "work")
	if err := os.Mkdir(spec.WorkDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var record commandRecord
	unprivileged := Mmdebstrap{CurrentUID: uidFunc(1000), RunCommand: recordingRunner(&record)}
	err := unprivileged.Run(context.Background(), spec)
	if !errors.Is(err, ErrBadWorkRoot) || !strings.Contains(err.Error(), "XDG_CACHE_HOME") {
		t.Fatalf("Run error = %v, want ErrBadWorkRoot saying how to fix it", err)
	}
	if record.runCount != 0 {
		t.Fatal("mmdebstrap must not run")
	}

	// Root mode has no user namespace, so there is nothing to reach.
	asRoot := Mmdebstrap{CurrentUID: uidFunc(0), RunCommand: recordingRunner(&record)}
	if err := asRoot.Run(context.Background(), spec); err != nil {
		t.Fatalf("Run in root mode: %v", err)
	}
}

func TestCheckReachableFromUserNamespace(t *testing.T) {
	notCreatedYet := filepath.Join(createReachableDir(t), "not", "created", "yet")
	if err := checkReachableFromUserNamespace(notCreatedYet); err != nil {
		t.Errorf("missing components are created 0755 later, so they are fine: %v", err)
	}
	unreachableDir := createUnreachableDir(t)
	err := checkReachableFromUserNamespace(filepath.Join(unreachableDir, "frostroot"))
	if !errors.Is(err, ErrBadWorkRoot) || !strings.Contains(err.Error(), unreachableDir) {
		t.Errorf("error = %v, want ErrBadWorkRoot naming %s", err, unreachableDir)
	}
}

func TestMmdebstrapRunLogsParsesAndKeepsTail(t *testing.T) {
	recorder := &recordingProgress{}
	bootstrapper := Mmdebstrap{
		CurrentUID: uidFunc(1000),
		RunCommand: func(_ context.Context, _ string, _, _ []string, _, stderr io.Writer) error {
			_, _ = fmt.Fprintln(stderr, "I: running apt-get update...")
			for lineNumber := range 400 {
				_, _ = fmt.Fprintf(stderr, "I: line %03d of chatter that fills the buffer\n", lineNumber)
			}
			_, _ = fmt.Fprintln(stderr, "E: Unable to locate package nosuchpkg")
			return errors.New("exit status 1")
		},
	}
	spec := runnableSpec(t)
	spec.Progress = recorder
	err := bootstrapper.Run(context.Background(), spec)
	if err == nil {
		t.Fatal("Run succeeded, want the command's failure")
	}
	logContent, readErr := os.ReadFile(filepath.Join(spec.WorkDir, LogFileName))
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !strings.HasPrefix(string(logContent), "I: running apt-get update...\n") || !strings.HasSuffix(string(logContent), "nosuchpkg\n") {
		t.Errorf("the log file must hold the whole output; got %d bytes starting %q", len(logContent), logContent[:min(40, len(logContent))])
	}
	if started := recorder.ofKind(EventPhaseStarted); len(started) != 1 || started[0].Phase != PhaseUpdateIndex {
		t.Errorf("progress events = %+v, want the update phase started", started)
	}
	if lines := recorder.ofKind(EventLogLine); len(lines) != 402 {
		t.Errorf("log line events = %d, want every line (402)", len(lines))
	}
	message := err.Error()
	if !strings.Contains(message, "nosuchpkg") || !strings.Contains(message, "exit status 1") {
		t.Errorf("error must carry the exit status and the end of the output: %v", err)
	}
	if len(message) > errorTailBytes+200 {
		t.Errorf("error is %d bytes, want the tail bounded to about %d", len(message), errorTailBytes)
	}
	if strings.Contains(message, "line 000") {
		t.Error("the tail should drop the oldest output")
	}
}

func TestMmdebstrapRunInterruptedReportsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	bootstrapper := Mmdebstrap{
		CurrentUID: uidFunc(1000),
		RunCommand: func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
			cancel()
			return errors.New("signal: interrupt")
		},
	}
	if err := bootstrapper.Run(ctx, runnableSpec(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context.Canceled", err)
	}
}

func TestMmdebstrapTrustedSpecNeedsNoKeyring(t *testing.T) {
	// An offline build's local repository carries [trusted=yes]; frostroot
	// verified its files against the lock, and no keyring is involved.
	var record commandRecord
	bootstrapper := Mmdebstrap{CurrentUID: uidFunc(1000), RunCommand: recordingRunner(&record), LookPath: func(string) (string, error) { return "/usr/bin/mmdebstrap", nil }}
	spec := runnableSpec(t)
	spec.Trusted = true
	spec.KeyringPath = ""
	spec.SourceLines = []string{"deb [trusted=yes] copy:///w/pool ./"}
	if err := bootstrapper.Preflight(spec); err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if err := bootstrapper.Run(context.Background(), spec); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, arg := range record.args {
		if strings.HasPrefix(arg, "--keyring") {
			t.Errorf("args = %q, want no --keyring for a trusted source", record.args)
		}
	}
	if !slices.Contains(record.args, "deb [trusted=yes] copy:///w/pool ./") {
		t.Errorf("args = %q, want the local repository line", record.args)
	}
}

func TestMmdebstrapRunWithoutKeyring(t *testing.T) {
	var record commandRecord
	bootstrapper := Mmdebstrap{CurrentUID: uidFunc(1000), RunCommand: recordingRunner(&record)}
	spec := runnableSpec(t)
	spec.KeyringPath = filepath.Join(t.TempDir(), "absent.gpg")
	err := bootstrapper.Run(context.Background(), spec)
	if !errors.Is(err, ErrNoKeyring) || !strings.Contains(err.Error(), "ubuntu-keyring") {
		t.Fatalf("Run error = %v, want ErrNoKeyring with an install hint", err)
	}
	if record.runCount != 0 {
		t.Fatal("mmdebstrap must not run without a keyring")
	}
}

func TestMmdebstrapPreflight(t *testing.T) {
	mmdebstrapFound := func(string) (string, error) { return "/usr/bin/mmdebstrap", nil }
	mmdebstrapMissing := func(string) (string, error) { return "", errors.New("not found") }
	reachableWorkRoot := filepath.Join(createReachableDir(t), "frostroot") // not created yet

	testCases := []struct {
		name        string
		lookPath    func(string) (string, error)
		spec        BootstrapSpec
		wantError   error
		wantInError string
	}{
		{
			name:        "mmdebstrap missing",
			lookPath:    mmdebstrapMissing,
			spec:        BootstrapSpec{KeyringPath: createKeyringFile(t), WorkDir: reachableWorkRoot},
			wantError:   ErrNoMmdebstrap,
			wantInError: "sudo apt install mmdebstrap",
		},
		{
			name:      "keyring missing",
			lookPath:  mmdebstrapFound,
			spec:      BootstrapSpec{KeyringPath: filepath.Join(t.TempDir(), "absent.gpg"), WorkDir: reachableWorkRoot},
			wantError: ErrNoKeyring,
		},
		{
			name:      "work root unreachable",
			lookPath:  mmdebstrapFound,
			spec:      BootstrapSpec{KeyringPath: createKeyringFile(t), WorkDir: filepath.Join(createUnreachableDir(t), "frostroot")},
			wantError: ErrBadWorkRoot,
		},
		{
			name:     "all fine",
			lookPath: mmdebstrapFound,
			spec:     BootstrapSpec{KeyringPath: createKeyringFile(t), WorkDir: reachableWorkRoot},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			bootstrapper := Mmdebstrap{LookPath: testCase.lookPath, CurrentUID: uidFunc(1000)}
			err := bootstrapper.Preflight(testCase.spec)
			if testCase.wantError == nil {
				if err != nil {
					t.Fatalf("Preflight error = %v, want none", err)
				}
				return
			}
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Preflight error = %v, want %v", err, testCase.wantError)
			}
			if !strings.Contains(err.Error(), testCase.wantInError) {
				t.Errorf("Preflight error = %v, want it to mention %q", err, testCase.wantInError)
			}
		})
	}
}

// synchronizedBuffer lets a test read output while a child process is still
// writing it.
type synchronizedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
}

func (b *synchronizedBuffer) Write(data []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(data)
}

func (b *synchronizedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

func TestRunInterruptiblySignalsTheWholeProcessGroup(t *testing.T) {
	// mmdebstrap's main process reacts to SIGINT by waiting for its worker,
	// relying on a terminal to deliver Ctrl-C to the whole process group. So
	// canceling must signal the group, or a build stopped with kill, timeout
	// or a service manager runs to the end anyway. And never SIGKILL: in root
	// mode mmdebstrap has proc, sys and dev mounted inside the chroot.
	// TestHelperProcess has the same shape: a main process that notes the
	// signal and waits, and a worker that cleans up. (It is not a shell
	// script: sh starts background jobs with SIGINT ignored, which no trap can
	// undo.)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output synchronizedBuffer
	runFinished := make(chan error, 1)
	go func() {
		runFinished <- runInterruptibly(ctx, os.Args[0], []string{"-test.run=^TestHelperProcess$"}, []string{"FROSTROOT_HELPER=main"}, &output, &output)
	}()

	startDeadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(output.String(), "ready") {
		if time.Now().After(startDeadline) {
			t.Fatal("the worker never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-runFinished:
		if err == nil {
			t.Fatal("the helper exited 0, want a non-zero exit")
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("still running 10s after cancel, so only the main process was signaled: %q", output.String())
	}
	for _, wantLine := range []string{"main got INT", "worker cleaned up", "main done"} {
		if !strings.Contains(output.String(), wantLine) {
			t.Errorf("output lacks %q; every process must get a catchable SIGINT: %q", wantLine, output.String())
		}
	}
}

// TestHelperProcess is not a real test. TestRunInterruptiblySignalsTheWholeProcessGroup
// runs the test binary as these two processes, selected by FROSTROOT_HELPER.
func TestHelperProcess(*testing.T) {
	interrupts := make(chan os.Signal, 1)
	switch os.Getenv("FROSTROOT_HELPER") {
	case "main":
		signal.Notify(interrupts, os.Interrupt)
		worker := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
		worker.Env = append(os.Environ(), "FROSTROOT_HELPER=worker")
		worker.Stdout, worker.Stderr = os.Stdout, os.Stderr
		if err := worker.Start(); err != nil {
			os.Exit(10)
		}
		workerExited := make(chan struct{})
		go func() {
			_ = worker.Wait() // its exit status is not what the parent test checks
			close(workerExited)
		}()
		for {
			select {
			case <-interrupts:
				fmt.Println("main got INT, waiting for worker")
			case <-workerExited:
				fmt.Println("main done")
				os.Exit(1)
			}
		}
	case "worker":
		signal.Notify(interrupts, os.Interrupt)
		fmt.Println("ready")
		<-interrupts
		time.Sleep(200 * time.Millisecond)
		fmt.Println("worker cleaned up")
		os.Exit(3)
	}
}

func TestRunInterruptiblyPassesEnvironment(t *testing.T) {
	var output bytes.Buffer
	err := runInterruptibly(context.Background(), "sh", []string{"-c", `printf %s "$TMPDIR"`}, []string{"TMPDIR=/work/dir"}, &output, &output)
	if err != nil {
		t.Fatal(err)
	}
	if output.String() != "/work/dir" {
		t.Fatalf("the child saw TMPDIR=%q, want /work/dir", output.String())
	}
}

func TestTailBuffer(t *testing.T) {
	testCases := []struct {
		name     string
		capacity int
		writes   []string
		want     string
	}{
		{name: "keeps the last bytes", capacity: 10, writes: []string{"abc", "defgh", "ijklmnop", "q"}, want: "hijklmnopq"},
		{name: "truncated output starts at a line boundary", capacity: 8, writes: []string{"one\ntwo\nthree\n"}, want: "three\n"},
		{name: "short output is kept whole", capacity: 100, writes: []string{"a\nb\n"}, want: "a\nb\n"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			tail := newTailBuffer(testCase.capacity)
			for _, data := range testCase.writes {
				if _, err := tail.Write([]byte(data)); err != nil {
					t.Fatal(err)
				}
			}
			if got := tail.String(); got != testCase.want {
				t.Fatalf("String() = %q, want %q", got, testCase.want)
			}
		})
	}
}
