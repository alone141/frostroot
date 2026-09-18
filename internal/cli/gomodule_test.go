package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoModuleVendorNote(t *testing.T) {
	// The note exists because frostroot's own checkout is a Go module, and
	// the README tells people to build it from source there.
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "go.mod"), []byte("module example\n\ngo 1.24\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	note := goModuleVendorNote(moduleDir)
	if note == "" {
		t.Fatal("no note beside a go.mod")
	}
	for _, want := range []string{"-mod=mod", "inconsistent vendoring", "vendor/"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note does not name %q:\n%s", want, note)
		}
	}
	if note := goModuleVendorNote(t.TempDir()); note != "" {
		t.Errorf("a directory that is not a Go module got a note: %q", note)
	}
}
