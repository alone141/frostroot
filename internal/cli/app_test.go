package cli

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/builder"
)

// testdataPath returns the path of a fixture in the repository's testdata
// directory.
func testdataPath(name string) string { return filepath.Join("..", "..", "testdata", name) }

// newRecipeDir returns a new temporary directory holding a copy of a fixture
// as frostroot.toml.
func newRecipeDir(t *testing.T, fixtureName string) string {
	t.Helper()
	fixture, err := os.ReadFile(testdataPath(fixtureName))
	if err != nil {
		t.Fatal(err)
	}
	recipeDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(recipeDir, "frostroot.toml"), fixture, 0o644); err != nil {
		t.Fatal(err)
	}
	return recipeDir
}

func TestRunRejectsMissingOrUnknownCommand(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}, {"--bogus"}} {
		var stderr bytes.Buffer
		app := App{Stdout: io.Discard, Stderr: &stderr, RecipeDir: t.TempDir()}
		if exitCode := app.Run(args); exitCode != exitUserError {
			t.Errorf("Run(%q) = %d, want %d", args, exitCode, exitUserError)
		}
		if !strings.Contains(stderr.String(), "usage") {
			t.Errorf("Run(%q) should print usage to stderr, got %q", args, stderr.String())
		}
	}
}

func TestRunHelp(t *testing.T) {
	for _, args := range [][]string{{"help"}, {"-h"}, {"--help"}} {
		var stdout bytes.Buffer
		app := App{Stdout: &stdout, Stderr: io.Discard, RecipeDir: t.TempDir()}
		if exitCode := app.Run(args); exitCode != exitSuccess {
			t.Errorf("Run(%q) = %d, want %d", args, exitCode, exitSuccess)
		}
		for _, command := range []string{"init", "edit", "capture", "validate", "build", "vendor", "version"} {
			if !strings.Contains(stdout.String(), command) {
				t.Errorf("Run(%q): usage should list %s: %q", args, command, stdout.String())
			}
		}
	}
}

func TestNewUsesRealMmdebstrap(t *testing.T) {
	app := New()
	if app.Builder == nil {
		t.Fatal("New().Builder is nil")
	}
	if _, isMmdebstrap := app.Builder.Bootstrapper.(*builder.Mmdebstrap); !isMmdebstrap {
		t.Errorf("New must build with mmdebstrap, got %T", app.Builder.Bootstrapper)
	}
	if app.Prompt == nil || app.WSLPath == nil || app.Getenv == nil || app.RecipeDir == "" || app.GOOS == "" {
		t.Errorf("New left defaults unset: %+v", app)
	}
}
