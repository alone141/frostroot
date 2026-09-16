package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/builder"
)

const stubStatus = "Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:1\n"

// stubBoot writes the two artifacts a real bootstrap produces.
type stubBoot struct {
	err       error
	preflight error
	spec      builder.BootstrapSpec
}

func (s *stubBoot) Preflight(spec builder.BootstrapSpec) error { return s.preflight }

func (s *stubBoot) Run(ctx context.Context, spec builder.BootstrapSpec) error {
	s.spec = spec
	if s.err != nil {
		return s.err
	}
	if err := os.WriteFile(spec.TarPath, []byte("tar"), 0o644); err != nil {
		return err
	}
	for _, h := range spec.Hooks {
		if p, ok := strings.CutPrefix(h, "download /var/lib/dpkg/status "); ok {
			return os.WriteFile(strings.Trim(p, "'"), []byte(stubStatus), 0o644)
		}
	}
	return errors.New("no download hook")
}

func newApp(t *testing.T, dir string, boot *stubBoot, out, errb *bytes.Buffer) *App {
	t.Helper()
	cache := t.TempDir()
	return &App{
		Stdout: out, Stderr: errb, Dir: dir, GOOS: "linux",
		Getenv: func(k string) string {
			if k == "XDG_CACHE_HOME" {
				return cache
			}
			return ""
		},
		WSLPath: func(string) (string, error) { return "", errors.New("not WSL") },
		Builder: &builder.Builder{Bootstrap: boot},
	}
}

func setRelease(t *testing.T, path, release string) {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	updated := strings.Replace(string(body), `release = "22.04"`, fmt.Sprintf("release = %q", release), 1)
	if updated == string(body) {
		t.Fatal("fixture has no release line to replace")
	}
	if err := os.WriteFile(path, []byte(updated), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildSuccess(t *testing.T) {
	dir := recipeDir(t, "valid.toml")
	var out, errb bytes.Buffer
	app := newApp(t, dir, &stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.lock")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wsl --import cpp-lab <install-dir> dist/cpp-lab-ubuntu-22.04-amd64.tar.gz") {
		t.Fatalf("stdout %s", out.String())
	}
	if !strings.Contains(out.String(), "frostroot.lock (1 package)") {
		t.Fatalf("stdout should summarise the lock: %s", out.String())
	}
	if strings.Contains(out.String(), "PowerShell") {
		t.Fatalf("no Windows path without wslpath: %s", out.String())
	}
}

func TestBuildPrintsWindowsPathUnderWSL(t *testing.T) {
	dir := recipeDir(t, "valid.toml")
	var out, errb bytes.Buffer
	app := newApp(t, dir, &stubBoot{}, &out, &errb)
	var asked string
	app.WSLPath = func(p string) (string, error) {
		asked = p
		return `\\wsl.localhost\Ubuntu\home\o'neil\lab\dist\cpp-lab-ubuntu-22.04-amd64.tar.gz`, nil
	}
	if code := app.Run([]string{"build"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if asked != filepath.Join(dir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz") {
		t.Fatalf("wslpath asked about %q", asked)
	}
	// Single-quoted for PowerShell, with the embedded quote doubled.
	want := `wsl --import cpp-lab <install-dir> '\\wsl.localhost\Ubuntu\home\o''neil\lab\dist\cpp-lab-ubuntu-22.04-amd64.tar.gz'`
	if !strings.Contains(out.String(), want) {
		t.Fatalf("stdout %s", out.String())
	}
}

func TestBuildWarnsOnEOLRelease(t *testing.T) {
	dir := recipeDir(t, "valid.toml")
	setRelease(t, filepath.Join(dir, "frostroot.toml"), "20.04")
	var out, errb bytes.Buffer
	app := newApp(t, dir, &stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	low := strings.ToLower(errb.String())
	for _, want := range []string{"20.04", "security", "ubuntu pro"} {
		if !strings.Contains(low, want) {
			t.Fatalf("EOL warning should mention %q: %s", want, errb.String())
		}
	}
}

func TestBuildDoesNotWarnOnSupportedRelease(t *testing.T) {
	var out, errb bytes.Buffer
	app := newApp(t, recipeDir(t, "valid.toml"), &stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if strings.Contains(strings.ToLower(errb.String()), "warning") {
		t.Fatalf("no warning expected for 22.04: %s", errb.String())
	}
}

func TestBuildNotLinuxExit1(t *testing.T) {
	var out, errb bytes.Buffer
	boot := &stubBoot{}
	app := newApp(t, recipeDir(t, "valid.toml"), boot, &out, &errb)
	app.GOOS = "windows"
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "WSL") {
		t.Fatalf("message should point Windows users at WSL: %s", errb.String())
	}
	if boot.spec.Suite != "" {
		t.Fatal("bootstrap must not run")
	}
}

func TestBuildUnwritableOutputExit1(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := recipeDir(t, "valid.toml")
	dist := filepath.Join(dir, "dist")
	if err := os.Mkdir(dist, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dist, 0o755) })
	var out, errb bytes.Buffer
	boot := &stubBoot{}
	app := newApp(t, dir, boot, &out, &errb)
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "sudo") || boot.spec.Suite != "" {
		t.Fatalf("should explain before bootstrapping: %s", errb.String())
	}
}

func TestBuildPreflightFailuresExit1(t *testing.T) {
	for _, err := range []error{
		fmt.Errorf("%w; install it with: sudo apt install mmdebstrap", builder.ErrNoMmdebstrap),
		fmt.Errorf("%w at /usr/share/keyrings/ubuntu-archive-keyring.gpg", builder.ErrNoKeyring),
	} {
		dir := recipeDir(t, "valid.toml")
		var out, errb bytes.Buffer
		app := newApp(t, dir, &stubBoot{preflight: err}, &out, &errb)
		if code := app.Run([]string{"build"}); code != 1 {
			t.Fatalf("%v: code %d", err, code)
		}
		if !strings.Contains(errb.String(), err.Error()) {
			t.Fatalf("stderr should carry the explanation: %s", errb.String())
		}
	}
}

func TestBuildWorkRootOnMntExit1(t *testing.T) {
	var out, errb bytes.Buffer
	app := newApp(t, recipeDir(t, "valid.toml"), &stubBoot{}, &out, &errb)
	app.Getenv = func(k string) string {
		if k == "XDG_CACHE_HOME" {
			return "/mnt/c/cache"
		}
		return ""
	}
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d: %s", code, errb.String())
	}
}

func TestBuildBootstrapFailureExit2(t *testing.T) {
	dir := recipeDir(t, "valid.toml")
	var out, errb bytes.Buffer
	app := newApp(t, dir, &stubBoot{err: errors.New("mmdebstrap exploded")}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 2 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "mmdebstrap exploded") {
		t.Fatalf("stderr %s", errb.String())
	}
	if !strings.Contains(errb.String(), "work directory kept") {
		t.Fatalf("failure must print the kept work directory: %s", errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.lock")); !os.IsNotExist(err) {
		t.Fatal("failure must not leave a lock")
	}
}

func TestBuildMirrorTroubleSuggestsMirrorFlag(t *testing.T) {
	var out, errb bytes.Buffer
	boot := &stubBoot{err: errors.New("mmdebstrap failed: exit status 1\nE: Failed to fetch http://archive.ubuntu.com/ubuntu/dists/jammy/InRelease  Temporary failure resolving 'archive.ubuntu.com'")}
	app := newApp(t, recipeDir(t, "valid.toml"), boot, &out, &errb)
	if code := app.Run([]string{"build"}); code != 2 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "--mirror") || !strings.Contains(errb.String(), "http://archive.ubuntu.com/ubuntu") {
		t.Fatalf("should name the mirror and suggest --mirror: %s", errb.String())
	}
}

func TestBuildInterruptedExit130(t *testing.T) {
	dir := recipeDir(t, "valid.toml")
	var out, errb bytes.Buffer
	app := newApp(t, dir, &stubBoot{err: fmt.Errorf("mmdebstrap: %w", context.Canceled)}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 130 {
		t.Fatalf("code %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.lock")); !os.IsNotExist(err) {
		t.Fatal("interrupt must not leave a lock")
	}
	if !strings.Contains(errb.String(), "work directory kept") {
		t.Fatalf("interrupt keeps the work directory like any failure: %s", errb.String())
	}
}

func TestBuildInvalidRecipeExit1(t *testing.T) {
	var out, errb bytes.Buffer
	boot := &stubBoot{}
	app := newApp(t, recipeDir(t, "bad-user.toml"), boot, &out, &errb)
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if boot.spec.Suite != "" {
		t.Fatal("an invalid recipe must never reach the bootstrapper")
	}
}

func TestBuildMissingRecipeExit1(t *testing.T) {
	var out, errb bytes.Buffer
	app := newApp(t, t.TempDir(), &stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestBuildMirrorFlagReachesBuilder(t *testing.T) {
	for _, args := range [][]string{
		{"build", "--mirror", "http://mirror.example/ubuntu"},
		{"build", "--mirror=https://mirror.example/ubuntu"},
	} {
		boot := &stubBoot{}
		var out, errb bytes.Buffer
		app := newApp(t, recipeDir(t, "valid.toml"), boot, &out, &errb)
		if code := app.Run(args); code != 0 {
			t.Fatalf("%v: code %d stderr %s", args, code, errb.String())
		}
		if len(boot.spec.Sources) != 3 {
			t.Fatalf("sources %#v", boot.spec.Sources)
		}
		for _, line := range boot.spec.Sources {
			if !strings.Contains(line, "mirror.example") {
				t.Fatalf("%v: mirror not applied: %#v", args, boot.spec.Sources)
			}
		}
	}
}

func TestBuildRejectsBadMirror(t *testing.T) {
	for _, m := range []string{"ftp://mirror.example/ubuntu", "mirror.example/ubuntu", "http://", "http://mirror.example/ubu ntu"} {
		boot := &stubBoot{}
		var out, errb bytes.Buffer
		app := newApp(t, recipeDir(t, "valid.toml"), boot, &out, &errb)
		if code := app.Run([]string{"build", "--mirror", m}); code != 1 {
			t.Fatalf("%q: code %d", m, code)
		}
		if boot.spec.Suite != "" {
			t.Fatalf("%q: bootstrap must not run", m)
		}
	}
}

func TestBuildKeepWorkPrintsWorkDir(t *testing.T) {
	var out, errb bytes.Buffer
	app := newApp(t, recipeDir(t, "valid.toml"), &stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build", "--keep-work"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(out.String()+errb.String(), "work directory kept") {
		t.Fatalf("--keep-work should say where: %s %s", out.String(), errb.String())
	}
}

func TestBuildBadFlagsExit1(t *testing.T) {
	for _, args := range [][]string{{"build", "--bogus"}, {"build", "extra"}} {
		var out, errb bytes.Buffer
		app := newApp(t, recipeDir(t, "valid.toml"), &stubBoot{}, &out, &errb)
		if code := app.Run(args); code != 1 {
			t.Fatalf("%v: code %d", args, code)
		}
	}
}
