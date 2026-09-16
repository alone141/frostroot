package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeEnvironment returns a Getenv function that looks names up in variables
// and returns "" for anything else.
func fakeEnvironment(variables map[string]string) func(string) string {
	return func(name string) string { return variables[name] }
}

func TestWorkRoot(t *testing.T) {
	testCases := []struct {
		name         string
		cacheHome    string // XDG_CACHE_HOME; empty means unset
		uid          int
		wantWorkRoot string
	}{
		{name: "XDG_CACHE_HOME set", cacheHome: "/home/me/.cache", uid: 1000, wantWorkRoot: "/home/me/.cache/frostroot"},
		// /var/tmp is shared. One `sudo frostroot build` would leave a
		// root-owned /var/tmp/frostroot that blocks every later unprivileged
		// build, so the default is per user.
		{name: "default for a user", uid: 1000, wantWorkRoot: "/var/tmp/frostroot-1000"},
		{name: "default for root", uid: 0, wantWorkRoot: "/var/tmp/frostroot-0"},
		// The XDG base directory specification says relative paths are invalid.
		{name: "relative XDG_CACHE_HOME ignored", cacheHome: "cache", uid: 1000, wantWorkRoot: "/var/tmp/frostroot-1000"},
		{name: "a path that only looks like /mnt", cacheHome: "/mntdata/cache", uid: 1000, wantWorkRoot: "/mntdata/cache/frostroot"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			got, err := WorkRoot(fakeEnvironment(map[string]string{"XDG_CACHE_HOME": testCase.cacheHome}), testCase.uid)
			if err != nil {
				t.Fatal(err)
			}
			if got != testCase.wantWorkRoot {
				t.Fatalf("WorkRoot = %q, want %q", got, testCase.wantWorkRoot)
			}
		})
	}
}

func TestWorkRootRefusesMnt(t *testing.T) {
	// /mnt/<drive> under WSL is 9p: slow, and unreliable for chown and device
	// nodes. Never bootstrap there.
	for _, cacheHome := range []string{"/mnt/c/Users/me/cache", "/mnt/d", "/mnt/../mnt/d/x"} {
		_, err := WorkRoot(fakeEnvironment(map[string]string{"XDG_CACHE_HOME": cacheHome}), 1000)
		if !errors.Is(err, ErrBadWorkRoot) {
			t.Errorf("WorkRoot with XDG_CACHE_HOME=%s: error = %v, want ErrBadWorkRoot so the CLI reports a user error", cacheHome, err)
			continue
		}
		if !strings.Contains(err.Error(), "XDG_CACHE_HOME") {
			t.Errorf("error should say how to fix it: %v", err)
		}
	}
}

func TestPrepareWorkRootCreatesItOnce(t *testing.T) {
	workRoot := filepath.Join(t.TempDir(), "cache", "frostroot")
	if err := prepareWorkRoot(workRoot, os.Getuid()); err != nil {
		t.Fatal(err)
	}
	workRootInfo, err := os.Stat(workRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !workRootInfo.IsDir() || workRootInfo.Mode().Perm() != 0o755 {
		t.Fatalf("work root mode = %v, want a 0755 directory", workRootInfo.Mode())
	}
	// A second build reuses it.
	if err := prepareWorkRoot(workRoot, os.Getuid()); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareWorkRootRefusesSomeoneElsesDirectory(t *testing.T) {
	// A work root owned by another user is either left over from their build,
	// which could not be written into anyway, or planted in shared /var/tmp.
	// Simulated by passing a uid the test does not run as.
	err := prepareWorkRoot(t.TempDir(), os.Getuid()+1)
	if !errors.Is(err, ErrBadWorkRoot) {
		t.Fatalf("prepareWorkRoot error = %v, want ErrBadWorkRoot", err)
	}
	for _, wantText := range []string{"owned by", "XDG_CACHE_HOME"} {
		if !strings.Contains(err.Error(), wantText) {
			t.Errorf("error should mention %q: %v", wantText, err)
		}
	}
}

func TestPrepareWorkRootRefusesFile(t *testing.T) {
	workRoot := filepath.Join(t.TempDir(), "frostroot")
	if err := os.WriteFile(workRoot, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkRoot(workRoot, os.Getuid()); !errors.Is(err, ErrBadWorkRoot) {
		t.Fatalf("prepareWorkRoot error = %v, want ErrBadWorkRoot", err)
	}
}

func TestPrepareWorkRootSymbolicLinks(t *testing.T) {
	// A user may point the work root at a bigger disk. A link owned by someone
	// else is refused like any other foreign directory.
	linkPath := filepath.Join(t.TempDir(), "frostroot")
	if err := os.Symlink(t.TempDir(), linkPath); err != nil {
		t.Fatal(err)
	}
	if err := prepareWorkRoot(linkPath, os.Getuid()); err != nil {
		t.Errorf("own symbolic link: %v", err)
	}
	if err := prepareWorkRoot(linkPath, os.Getuid()+1); !errors.Is(err, ErrBadWorkRoot) {
		t.Errorf("someone else's symbolic link: error = %v, want ErrBadWorkRoot", err)
	}
}

func TestCheckOutputWritable(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root can write anywhere")
	}
	recipeDir := t.TempDir()
	if err := checkOutputWritable(recipeDir); err != nil {
		t.Fatalf("writable directory without dist/: %v", err)
	}
	distDir := filepath.Join(recipeDir, "dist")
	if err := os.Mkdir(distDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := checkOutputWritable(recipeDir); err != nil {
		t.Fatalf("writable dist/: %v", err)
	}
	if probeLeftovers, _ := os.ReadDir(distDir); len(probeLeftovers) != 0 {
		t.Fatalf("probe files left behind: %v", probeLeftovers)
	}

	// dist/ left behind by `sudo frostroot build` is owned by root, so this
	// user cannot write to it. Simulated with a read-only directory.
	if err := os.Chmod(distDir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(distDir, 0o755) })
	err := checkOutputWritable(recipeDir)
	if !errors.Is(err, ErrUnwritableOutput) {
		t.Fatalf("read-only dist/: error = %v, want ErrUnwritableOutput", err)
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("error should point at the usual cause: %v", err)
	}

	if err := os.Chmod(distDir, 0o755); err != nil {
		t.Fatal(err)
	}
	readOnlyLock := filepath.Join(recipeDir, "frostroot.lock")
	if err := os.WriteFile(readOnlyLock, []byte("old lock"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := checkOutputWritable(recipeDir); err != nil {
		t.Errorf("a read-only lock is replaced by rename, not rewritten, so it is fine: %v", err)
	}
}

func TestCheckOutputWritableRejectsDistFile(t *testing.T) {
	recipeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipeDir, "dist"), []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := checkOutputWritable(recipeDir); !errors.Is(err, ErrUnwritableOutput) {
		t.Fatalf("checkOutputWritable error = %v, want ErrUnwritableOutput", err)
	}
}
