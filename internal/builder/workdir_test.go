package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestWorkRootPrefersXDGCache(t *testing.T) {
	cache := t.TempDir()
	got, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": cache}), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(cache, "frostroot") {
		t.Fatalf("got %q want under %q", got, cache)
	}
}

func TestWorkRootFallsBackToPerUserVarTmp(t *testing.T) {
	// /var/tmp is shared. One `sudo frostroot build` would leave a root-owned
	// /var/tmp/frostroot that blocks every later unprivileged build, so the
	// default is per user.
	got, err := WorkRoot(env(nil), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/var/tmp/frostroot-1000" {
		t.Fatalf("got %q", got)
	}
	if root, _ := WorkRoot(env(nil), 0); root != "/var/tmp/frostroot-0" {
		t.Fatalf("root: got %q", root)
	}
}

func TestWorkRootIgnoresRelativeXDGCache(t *testing.T) {
	// The XDG base directory spec says relative paths are invalid.
	got, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": "cache"}), 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got != "/var/tmp/frostroot-1000" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkRootRefusesMnt(t *testing.T) {
	// /mnt/<drive> under WSL is 9p: slow, and unreliable for chown and
	// device nodes. Never bootstrap there.
	for _, cache := range []string{"/mnt/c/Users/me/cache", "/mnt/d", "/mnt/../mnt/d/x"} {
		_, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": cache}), 1000)
		if err == nil {
			t.Fatalf("%s: expected refusal", cache)
		}
		if !strings.Contains(err.Error(), "XDG_CACHE_HOME") {
			t.Fatalf("%s: message should say how to fix it: %v", cache, err)
		}
		if !errors.Is(err, ErrBadWorkRoot) {
			t.Fatalf("%s: want ErrBadWorkRoot so the CLI can call it a user error: %v", cache, err)
		}
	}
}

func TestWorkRootAllowsMntLookalikes(t *testing.T) {
	got, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": "/mntdata/cache"}), 1000)
	if err != nil || got != "/mntdata/cache/frostroot" {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestPrepareWorkRootCreatesAndAccepts(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cache", "frostroot")
	if err := prepareWorkRoot(root, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if !fi.IsDir() || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode %v", fi.Mode())
	}
	// Idempotent: a second build reuses it.
	if err := prepareWorkRoot(root, os.Getuid()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareWorkRootRefusesSomeoneElsesDirectory(t *testing.T) {
	// The directory exists and is owned by another user: either a leftover
	// from their build, which we could not write into anyway, or a squat in
	// shared /var/tmp. Simulated by asking for a uid we are not.
	root := t.TempDir()
	err := prepareWorkRoot(root, os.Getuid()+1)
	if !errors.Is(err, ErrBadWorkRoot) {
		t.Fatalf("want ErrBadWorkRoot, got %v", err)
	}
	if !strings.Contains(err.Error(), "owned by") || !strings.Contains(err.Error(), "XDG_CACHE_HOME") {
		t.Fatalf("message should name the owner and the way out: %v", err)
	}
}

func TestPrepareWorkRootRefusesNonDirectory(t *testing.T) {
	root := filepath.Join(t.TempDir(), "frostroot")
	if err := os.WriteFile(root, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkRoot(root, os.Getuid()); !errors.Is(err, ErrBadWorkRoot) {
		t.Fatalf("want ErrBadWorkRoot, got %v", err)
	}
}

func TestPrepareWorkRootFollowsOwnSymlink(t *testing.T) {
	// A user may point the work root at a bigger disk. A link owned by
	// someone else is refused like any other foreign directory.
	target := t.TempDir()
	link := filepath.Join(t.TempDir(), "frostroot")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkRoot(link, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkRoot(link, os.Getuid()+1); !errors.Is(err, ErrBadWorkRoot) {
		t.Fatalf("want ErrBadWorkRoot, got %v", err)
	}
}

func TestCheckOutput(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	dir := t.TempDir()
	if err := checkOutput(dir); err != nil {
		t.Fatalf("writable directory without dist/: %v", err)
	}
	dist := filepath.Join(dir, "dist")
	if err := os.Mkdir(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkOutput(dir); err != nil {
		t.Fatalf("writable dist/: %v", err)
	}
	if entries, _ := os.ReadDir(dist); len(entries) != 0 {
		t.Fatalf("probe files left behind: %v", entries)
	}

	// dist/ left behind by `sudo frostroot build`: owned by root, so not
	// writable by us. Simulated with a read-only directory.
	if err := os.Chmod(dist, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(dist, 0o755) })
	err := checkOutput(dir)
	if !errors.Is(err, ErrUnwritableOutput) {
		t.Fatalf("want ErrUnwritableOutput, got %v", err)
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Fatalf("message should point at the usual cause: %v", err)
	}

	if err := os.Chmod(dist, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "frostroot.lock"), []byte("x"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := checkOutput(dir); err != nil {
		t.Fatalf("a read-only lock is replaced by rename, not rewritten: %v", err)
	}
}

func TestCheckOutputRejectsDistFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dist"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkOutput(dir); !errors.Is(err, ErrUnwritableOutput) {
		t.Fatalf("want ErrUnwritableOutput, got %v", err)
	}
}
