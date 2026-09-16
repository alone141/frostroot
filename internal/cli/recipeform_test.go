package cli

import (
	"io"
	"os"
	"testing"
)

// terminalChecker returns an IsTerminal that answers the same for every
// stream.
func terminalChecker(isTerminal bool) func(any) bool {
	return func(any) bool { return isTerminal }
}

func TestUseFullScreen(t *testing.T) {
	testCases := []struct {
		name           string
		isTerminal     bool
		term           string
		plainRequested bool
		want           bool
	}{
		{name: "terminal", isTerminal: true, term: "xterm-256color", want: true},
		{name: "pipe", isTerminal: false, term: "xterm-256color", want: false},
		{name: "--plain", isTerminal: true, term: "xterm-256color", plainRequested: true, want: false},
		{name: "dumb terminal", isTerminal: true, term: "dumb", want: false},
		{name: "no TERM", isTerminal: true, term: "", want: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			app := (&App{
				Stdout:     io.Discard,
				Stderr:     io.Discard,
				IsTerminal: terminalChecker(testCase.isTerminal),
				Getenv:     func(string) string { return testCase.term },
			}).withDefaults()
			if got := app.useFullScreen(testCase.plainRequested); got != testCase.want {
				t.Errorf("useFullScreen = %v, want %v", got, testCase.want)
			}
		})
	}
}

func TestIsCharacterDevice(t *testing.T) {
	if isCharacterDevice(io.Discard) {
		t.Error("a non-file writer is not a terminal")
	}
	regularFile, err := os.CreateTemp(t.TempDir(), "file")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = regularFile.Close() }() // temporary, read back by nothing
	if isCharacterDevice(regularFile) {
		t.Error("a regular file is not a terminal")
	}
	if devNull, err := os.Open(os.DevNull); err == nil {
		defer func() { _ = devNull.Close() }() // read-only
		if !isCharacterDevice(devNull) {
			t.Error("/dev/null is a character device; the check should say so")
		}
	}
}

func TestParseYesNo(t *testing.T) {
	for answer, want := range map[string]bool{"y": true, "Yes": true, "TRUE": true, "n": false, "no": false, "False": false} {
		got, understood := parseYesNo(answer)
		if !understood || got != want {
			t.Errorf("parseYesNo(%q) = %v, %v; want %v, understood", answer, got, understood, want)
		}
	}
	if _, understood := parseYesNo("maybe"); understood {
		t.Error("parseYesNo(maybe) should not be understood")
	}
}
