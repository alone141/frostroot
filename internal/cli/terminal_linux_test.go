//go:build linux

package cli

import (
	"io"
	"os"
	"testing"
)

// TestIsTerminalIsNotFooledByDevNull: the check was "a character device",
// and /dev/null is one, so `frostroot init </dev/null` from a terminal
// opened the full-screen form on an input that had already ended.
func TestIsTerminalIsNotFooledByDevNull(t *testing.T) {
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = devNull.Close() }() // read-only
	if isTerminal(devNull) {
		t.Error("/dev/null is not a terminal")
	}
	regularFile, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regularFile.Close() }() // temporary, read back by nothing
	if isTerminal(regularFile) || isTerminal(io.Discard) {
		t.Error("a regular file, or a writer that is no file, is not a terminal")
	}
}
