package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeLog writes lines as a log file and returns its path.
func writeLog(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), LogFileName)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFirstErrorLineFindsTheLineThatExplainsAFailure(t *testing.T) {
	filler := make([]string, 300)
	for index := range filler {
		filler[index] = "Get:" + strings.Repeat("x", 60)
	}
	tests := []struct {
		name       string
		lines      []string
		wantNumber int
		wantText   string
	}{
		{
			name:       "apt, above a long tail",
			lines:      append([]string{"I: running apt-get update...", "E: Unable to locate package ninja-buld"}, filler...),
			wantNumber: 2,
			wantText:   "E: Unable to locate package ninja-buld",
		},
		{
			name:       "the provision script",
			lines:      []string{"I: running special hook", "Generating locales...", "frostroot: timezone Mars/Olympus does not exist in this image"},
			wantNumber: 3,
			wantText:   "frostroot: timezone Mars/Olympus does not exist in this image",
		},
		{
			name:       "pip, followed by its traceback",
			lines:      append([]string{"Collecting requests", "ERROR: Could not find a version that satisfies the requirement reqests", "Traceback (most recent call last):"}, filler...),
			wantNumber: 2,
			wantText:   "ERROR: Could not find a version that satisfies the requirement reqests",
		},
		{
			name:       "dpkg",
			lines:      []string{"Setting up foo (1.0) ...", "dpkg: error processing package foo (--configure):"},
			wantNumber: 2,
			wantText:   "dpkg: error processing package foo (--configure):",
		},
		{
			name:       "the first of several, and a warning is not one",
			lines:      []string{"W: some index files failed to download", "E: first", "E: second"},
			wantNumber: 2,
			wantText:   "E: first",
		},
		{
			name:       "a Windows line ending is trimmed",
			lines:      []string{"E: with a carriage return\r"},
			wantNumber: 1,
			wantText:   "E: with a carriage return",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			line, found := FirstErrorLine(writeLog(t, test.lines...))
			if !found {
				t.Fatal("FirstErrorLine found nothing")
			}
			if line.Number != test.wantNumber || line.Text != test.wantText {
				t.Errorf("FirstErrorLine = line %d %q, want line %d %q", line.Number, line.Text, test.wantNumber, test.wantText)
			}
		})
	}
}

func TestFirstErrorLineSaysNothingRatherThanTheWrongThing(t *testing.T) {
	t.Run("a log with no error line", func(t *testing.T) {
		if line, found := FirstErrorLine(writeLog(t, "I: done", "W: not an error")); found {
			t.Errorf("FirstErrorLine = %+v, want nothing", line)
		}
	})
	t.Run("a log that is not there", func(t *testing.T) {
		if line, found := FirstErrorLine(filepath.Join(t.TempDir(), "missing.log")); found {
			t.Errorf("FirstErrorLine = %+v, want nothing", line)
		}
	})
	t.Run("a line too long to be an explanation", func(t *testing.T) {
		// The scanner gives up on it; the lines after it are not reached,
		// and that is still nothing rather than a crash.
		if line, found := FirstErrorLine(writeLog(t, "E: "+strings.Repeat("x", maxExplanationLineBytes+1), "E: after")); found {
			t.Errorf("FirstErrorLine = %+v, want nothing", line)
		}
	})
}

func TestBootstrapErrorRendersTheTailAndUnwraps(t *testing.T) {
	cause := os.ErrPermission
	err := &BootstrapError{Err: cause, Tail: "E: apt-get update failed\nI: cleaning up"}
	want := "mmdebstrap failed: permission denied\n--- last lines of mmdebstrap output ---\nE: apt-get update failed\nI: cleaning up"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
	if !errors.Is(err, cause) {
		t.Error("the process error is not reachable through Unwrap")
	}
}
