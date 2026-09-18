package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/builder"
)

// explainingBootstrapper fails the way a real mmdebstrap does: it writes a
// log whose explaining line is far above the tail the error carries.
type explainingBootstrapper struct {
	fakeBootstrapper
	logLines []string
	tail     string
}

func (b *explainingBootstrapper) Run(_ context.Context, spec builder.BootstrapSpec) error {
	b.lastSpec = spec
	logPath := filepath.Join(spec.WorkDir, builder.LogFileName)
	if err := os.WriteFile(logPath, []byte(strings.Join(b.logLines, "\n")+"\n"), 0o644); err != nil {
		return err
	}
	return &builder.BootstrapError{Err: errors.New("exit status 1"), Tail: b.tail}
}

func TestBuildFailurePrintsTheLineThatExplainsItFirst(t *testing.T) {
	// 300 lines of dpkg cleanup after apt's E: line, more than the 4 KiB
	// tail holds, which is what a real failure looks like once mmdebstrap
	// has torn the chroot down.
	cleanup := make([]string, 300)
	for index := range cleanup {
		cleanup[index] = "Removing libfoo" + strings.Repeat("o", 40)
	}
	logLines := append([]string{"I: running apt-get update...", "E: Unable to locate package ninja-buld"}, cleanup...)
	tail := strings.Join(cleanup[len(cleanup)-20:], "\n")
	bootstrapper := &explainingBootstrapper{logLines: logLines, tail: tail}

	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, newRecipeDir(t, "valid.toml"), bootstrapper, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitBuildFailed {
		t.Fatalf("exit code = %d, want %d; stderr %s", exitCode, exitBuildFailed, stderr.String())
	}
	output := stderr.String()
	inOrder := []string{
		"frostroot: build failed: mmdebstrap failed: exit status 1",
		"the first error in mmdebstrap.log, line 2:",
		"E: Unable to locate package ninja-buld",
		"--- last lines of mmdebstrap output ---",
		"Removing libfoo",
		"work directory kept",
	}
	position := 0
	for _, wantText := range inOrder {
		index := strings.Index(output[position:], wantText)
		if index < 0 {
			t.Fatalf("stderr lacks %q after position %d:\n%s", wantText, position, output)
		}
		position += index + len(wantText)
	}
	if strings.Count(output, "E: Unable to locate") != 1 {
		t.Errorf("the explaining line is not in the tail, so it should appear exactly once:\n%s", output)
	}
}

func TestBuildFailureWithoutAnExplainingLineStillPrintsTheTail(t *testing.T) {
	bootstrapper := &explainingBootstrapper{logLines: []string{"I: running apt-get update...", "I: done"}, tail: "I: done"}
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, newRecipeDir(t, "valid.toml"), bootstrapper, &stdout, &stderr)
	if exitCode := app.Run([]string{"build"}); exitCode != exitBuildFailed {
		t.Fatalf("exit code = %d, want %d", exitCode, exitBuildFailed)
	}
	output := stderr.String()
	if strings.Contains(output, "the first error in") {
		t.Errorf("no line explains this failure, yet one was claimed:\n%s", output)
	}
	for _, wantText := range []string{"--- last lines of mmdebstrap output ---", "I: done", "work directory kept"} {
		if !strings.Contains(output, wantText) {
			t.Errorf("stderr lacks %q:\n%s", wantText, output)
		}
	}
}
