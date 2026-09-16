package builder

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func env(kv map[string]string) func(string) string {
	return func(k string) string { return kv[k] }
}

func TestWorkRootPrefersXDGCache(t *testing.T) {
	cache := t.TempDir()
	got, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": cache}))
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(cache, "frostroot") {
		t.Fatalf("got %q want under %q", got, cache)
	}
}

func TestWorkRootFallsBackToVarTmp(t *testing.T) {
	got, err := WorkRoot(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if got != "/var/tmp/frostroot" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkRootIgnoresRelativeXDGCache(t *testing.T) {
	// The XDG base directory spec says relative paths are invalid.
	got, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": "cache"}))
	if err != nil {
		t.Fatal(err)
	}
	if got != "/var/tmp/frostroot" {
		t.Fatalf("got %q", got)
	}
}

func TestWorkRootRefusesMnt(t *testing.T) {
	// /mnt/<drive> under WSL is 9p: slow, and unreliable for chown and
	// device nodes. Never bootstrap there.
	for _, cache := range []string{"/mnt/c/Users/me/cache", "/mnt/d", "/mnt/../mnt/d/x"} {
		_, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": cache}))
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
	got, err := WorkRoot(env(map[string]string{"XDG_CACHE_HOME": "/mntdata/cache"}))
	if err != nil || got != "/mntdata/cache/frostroot" {
		t.Fatalf("got %q, %v", got, err)
	}
}
