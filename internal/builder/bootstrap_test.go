package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// keyringFile gives Run a keyring that exists, so these tests need no
// ubuntu-keyring on the host.
func keyringFile(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "keyring.gpg")
	if err := os.WriteFile(p, []byte("keys"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

type recorded struct {
	name string
	args []string
	env  []string
}

func recorder(rec *recorded) Runner {
	return func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
		rec.name, rec.args, rec.env = name, append([]string{}, args...), append([]string{}, env...)
		return nil
	}
}

func TestMmdebstrapCommand(t *testing.T) {
	var rec recorded
	keyring := keyringFile(t)
	m := Mmdebstrap{Uid: func() int { return 1000 }, RunCmd: recorder(&rec)}
	spec := BootstrapSpec{
		Suite:      "jammy",
		Sources:    []string{"deb http://a jammy main universe", "deb http://a jammy-updates main universe", "deb http://a jammy-security main universe"},
		Include:    []string{"git", "systemd"},
		Hooks:      []string{"upload '/w/wsl.conf' /etc/wsl.conf", "download /var/lib/dpkg/status '/w/dpkg-status'"},
		TarPath:    "/w/image.tar.gz",
		WorkDir:    "/w",
		Arch:       "amd64",
		Recommends: true,
		Keyring:    keyring,
	}
	if err := m.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if rec.name != "mmdebstrap" {
		t.Fatalf("name %q", rec.name)
	}
	want := []string{
		"--mode=unshare",
		"--variant=important",
		"--architectures=amd64",
		"--keyring=" + keyring,
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
	if strings.Join(rec.args, "\n") != strings.Join(want, "\n") {
		t.Fatalf("args:\n%s\nwant:\n%s", strings.Join(rec.args, "\n"), strings.Join(want, "\n"))
	}
	if !contains(rec.env, "TMPDIR=/w") {
		t.Fatalf("TMPDIR must point at the work dir: %v", rec.env)
	}
}

func TestMmdebstrapDefaultKeyring(t *testing.T) {
	args, _ := (&Mmdebstrap{Uid: func() int { return 1000 }}).command(BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64"})
	if !contains(args, "--keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg") {
		t.Fatalf("args %v", args)
	}
}

func TestMmdebstrapHooksPassedInOrder(t *testing.T) {
	var rec recorded
	m := Mmdebstrap{Uid: func() int { return 1000 }, RunCmd: recorder(&rec)}
	hooks := []string{"first", "second", "third"}
	if err := m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64", Hooks: hooks, Keyring: keyringFile(t)}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, a := range rec.args {
		if h, ok := strings.CutPrefix(a, "--customize-hook="); ok {
			seen = append(seen, h)
		}
	}
	if strings.Join(seen, ",") != "first,second,third" {
		t.Fatalf("hook order: %v", seen)
	}
}

func TestMmdebstrapModes(t *testing.T) {
	for _, tc := range []struct {
		mode string
		uid  int
		want string
	}{
		{"", 1000, "--mode=unshare"},
		{"", 0, "--mode=root"},
		{"unshare", 0, "--mode=unshare"},
		{"root", 1000, "--mode=root"},
	} {
		args, _ := (&Mmdebstrap{Mode: tc.mode, Uid: func() int { return tc.uid }}).command(BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64"})
		if args[0] != tc.want {
			t.Fatalf("mode %q uid %d: got %v", tc.mode, tc.uid, args)
		}
	}
}

func TestMmdebstrapOptionalFlags(t *testing.T) {
	args, _ := (&Mmdebstrap{Uid: func() int { return 1000 }}).command(BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64"})
	joined := strings.Join(args, " ")
	if strings.Contains(joined, "Install-Recommends") {
		t.Fatalf("no Recommends flag when off: %q", joined)
	}
	if strings.Contains(joined, "--include") {
		t.Fatalf("no --include when there is nothing to include: %q", joined)
	}
}

func TestMmdebstrapStreamsProgressAndKeepsTail(t *testing.T) {
	var progress bytes.Buffer
	m := Mmdebstrap{
		Uid:    func() int { return 1000 },
		Stderr: &progress,
		RunCmd: func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			for i := 0; i < 400; i++ {
				fmt.Fprintf(stderr, "I: line %03d of chatter that fills the buffer\n", i)
			}
			fmt.Fprintln(stderr, "E: Unable to locate package nosuchpkg")
			return errors.New("exit status 1")
		},
	}
	err := m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64", Keyring: keyringFile(t)})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nosuchpkg") || !strings.Contains(err.Error(), "exit status 1") {
		t.Fatalf("error must carry the exit status and the stderr tail: %v", err)
	}
	if len(err.Error()) > 4096+200 {
		t.Fatalf("tail should be bounded to about 4 KiB, got %d bytes", len(err.Error()))
	}
	if strings.Contains(err.Error(), "line 000") {
		t.Fatal("tail should drop the oldest output")
	}
	if !strings.Contains(progress.String(), "line 000") || !strings.Contains(progress.String(), "nosuchpkg") {
		t.Fatal("everything must be streamed to the terminal as it happens")
	}
}

func TestMmdebstrapInterruptedIsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	m := Mmdebstrap{
		Uid: func() int { return 1000 },
		RunCmd: func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			cancel()
			return errors.New("signal: interrupt")
		},
	}
	err := m.Run(ctx, BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64", Keyring: keyringFile(t)})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("an interrupted run must report context.Canceled, got %v", err)
	}
}

func TestMmdebstrapMissingKeyringIsClear(t *testing.T) {
	called := false
	m := Mmdebstrap{
		Uid: func() int { return 1000 },
		RunCmd: func(context.Context, string, []string, []string, io.Writer, io.Writer) error {
			called = true
			return nil
		},
	}
	err := m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64", Keyring: filepath.Join(t.TempDir(), "absent.gpg")})
	if !errors.Is(err, ErrNoKeyring) || !strings.Contains(err.Error(), "ubuntu-keyring") {
		t.Fatalf("want a keyring error with an install hint, got %v", err)
	}
	if called {
		t.Fatal("mmdebstrap must not run without a keyring")
	}
}

func TestMmdebstrapPreflight(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/mmdebstrap", nil }
	missing := func(string) (string, error) { return "", errors.New("not found") }

	m := Mmdebstrap{LookPath: missing}
	if err := m.Preflight(BootstrapSpec{Keyring: keyringFile(t)}); !errors.Is(err, ErrNoMmdebstrap) || !strings.Contains(err.Error(), "sudo apt install mmdebstrap") {
		t.Fatalf("got %v", err)
	}
	m = Mmdebstrap{LookPath: found}
	if err := m.Preflight(BootstrapSpec{Keyring: filepath.Join(t.TempDir(), "absent.gpg")}); !errors.Is(err, ErrNoKeyring) {
		t.Fatalf("got %v", err)
	}
	if err := m.Preflight(BootstrapSpec{Keyring: keyringFile(t)}); err != nil {
		t.Fatalf("got %v", err)
	}
}

// syncBuffer lets the test read output while the child is still writing it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

func TestExecRunInterruptsWithSIGINT(t *testing.T) {
	// Never SIGKILL mmdebstrap: in root mode it has proc, sys and dev mounted
	// inside the chroot and must be allowed to clean up. Prove cancellation
	// delivers a catchable SIGINT and waits for the child to finish.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	script := `trap 'sleep 0.3; echo "cleaned up after INT"; exit 3' INT; echo ready; while :; do sleep 0.05; done`
	done := make(chan error, 1)
	go func() { done <- execRun(ctx, "sh", []string{"-c", script}, nil, &out, &out) }()

	deadline := time.Now().Add(10 * time.Second)
	for !strings.Contains(out.String(), "ready") {
		if time.Now().After(deadline) {
			t.Fatal("child never started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected the child's non-zero exit")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("child did not exit after SIGINT")
	}
	if !strings.Contains(out.String(), "cleaned up after INT") {
		t.Fatalf("child was not allowed to handle SIGINT: %q", out.String())
	}
}

func TestExecRunPassesEnvironment(t *testing.T) {
	var out bytes.Buffer
	if err := execRun(context.Background(), "sh", []string{"-c", `printf %s "$TMPDIR"`}, []string{"TMPDIR=/work/dir"}, &out, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != "/work/dir" {
		t.Fatalf("got %q", out.String())
	}
}

func TestTailBuffer(t *testing.T) {
	tb := newTailBuffer(10)
	for _, s := range []string{"abc", "defgh", "ijklmnop", "q"} {
		tb.Write([]byte(s))
	}
	if got := string(tb.buf); got != "hijklmnopq" {
		t.Fatalf("want the last 10 bytes, got %q", got)
	}

	lines := newTailBuffer(8)
	lines.Write([]byte("one\ntwo\nthree\n"))
	if got := lines.String(); got != "three\n" {
		t.Fatalf("a truncated tail should start at a line boundary, got %q", got)
	}

	whole := newTailBuffer(100)
	whole.Write([]byte("a\nb\n"))
	if got := whole.String(); got != "a\nb\n" {
		t.Fatalf("got %q", got)
	}
}
