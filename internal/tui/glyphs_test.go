package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// withLocale runs the rest of the test with only the given locale variables
// set, the way glyphsForTerminal sees them.
func withLocale(t *testing.T, variables map[string]string) {
	t.Helper()
	previous := lookupEnvironment
	lookupEnvironment = func(name string) (string, bool) {
		value, isSet := variables[name]
		return value, isSet
	}
	t.Cleanup(func() { lookupEnvironment = previous })
}

func TestGlyphsFollowTheLocale(t *testing.T) {
	tests := []struct {
		name      string
		variables map[string]string
		wantASCII bool
	}{
		{name: "nothing set: a modern terminal", variables: nil},
		{name: "LANG UTF-8", variables: map[string]string{"LANG": "en_US.UTF-8"}},
		{name: "LANG utf8 spelled the other way", variables: map[string]string{"LANG": "C.utf8"}},
		{name: "LANG C", variables: map[string]string{"LANG": "C"}, wantASCII: true},
		{name: "LANG POSIX", variables: map[string]string{"LANG": "POSIX"}, wantASCII: true},
		{name: "LC_ALL wins over LANG", variables: map[string]string{"LC_ALL": "C", "LANG": "en_US.UTF-8"}, wantASCII: true},
		{name: "LC_CTYPE wins over LANG", variables: map[string]string{"LC_CTYPE": "en_GB.UTF-8", "LANG": "C"}},
		{name: "an empty LC_ALL does not count", variables: map[string]string{"LC_ALL": "", "LANG": "C"}, wantASCII: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			withLocale(t, test.variables)
			if got := glyphsForTerminal(); got.ascii != test.wantASCII {
				t.Errorf("ascii = %v, want %v", got.ascii, test.wantASCII)
			}
		})
	}
}

func TestTruncateWith(t *testing.T) {
	tests := []struct {
		text     string
		width    int
		ellipsis string
		want     string
	}{
		{text: "short", width: 10, ellipsis: "…", want: "short"},
		{text: "exactly ten", width: 11, ellipsis: "…", want: "exactly ten"},
		{text: "this is too long", width: 8, ellipsis: "…", want: "this is…"},
		{text: "this is too long", width: 8, ellipsis: "...", want: "this ..."},
		{text: "anything", width: 0, ellipsis: "…", want: ""},
		{text: "anything", width: 2, ellipsis: "...", want: ".."},
		{text: "日本語のテキスト", width: 7, ellipsis: "…", want: "日本語…"},
	}
	for _, test := range tests {
		if got := truncateWith(test.text, test.width, test.ellipsis); got != test.want {
			t.Errorf("truncateWith(%q, %d, %q) = %q, want %q", test.text, test.width, test.ellipsis, got, test.want)
		}
	}
}

func TestShortenLeftKeepsTheEnd(t *testing.T) {
	tests := []struct {
		text     string
		width    int
		ellipsis string
		want     string
	}{
		{text: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log", width: 60, ellipsis: "…", want: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log"},
		{text: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log", width: 24, ellipsis: "…", want: "…/build-1/mmdebstrap.log"},
		{text: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log", width: 24, ellipsis: "...", want: "...uild-1/mmdebstrap.log"},
		{text: "anything", width: 0, ellipsis: "…", want: ""},
		{text: "anything", width: 1, ellipsis: "...", want: "."},
	}
	for _, test := range tests {
		if got := shortenLeft(test.text, test.width, test.ellipsis); got != test.want {
			t.Errorf("shortenLeft(%q, %d, %q) = %q, want %q", test.text, test.width, test.ellipsis, got, test.want)
		}
	}
}

func TestFitLinesCutsInsteadOfWrapping(t *testing.T) {
	text := "a short line\n" + strings.Repeat("x", 100) + "\nlast\n"
	fitted := fitLines(text, 20, "…")
	lines := strings.Split(fitted, "\n")
	if len(lines) != 3 {
		t.Fatalf("fitLines gave %d lines, want 3:\n%s", len(lines), fitted)
	}
	if lines[0] != "a short line" || lines[2] != "last" {
		t.Errorf("short lines changed: %q", lines)
	}
	if got := lines[1]; got != strings.Repeat("x", 19)+"…" {
		t.Errorf("the long line became %q", got)
	}
}

func TestTitledPaneKeepsItsEdgeTheRightLength(t *testing.T) {
	for _, glyphs := range []glyphSet{unicodeGlyphs, asciiGlyphs} {
		pane := frameOf(titledPane(glyphs, "mmdebstrap", 40, "one line\ntwo"))
		lines := strings.Split(strings.TrimRight(pane, "\n"), "\n")
		top, bottom := lines[0], lines[len(lines)-1]
		if lipgloss.Width(top) != lipgloss.Width(bottom) {
			t.Errorf("the top edge is %d cells and the bottom %d:\n%s", lipgloss.Width(top), lipgloss.Width(bottom), pane)
		}
		if !strings.Contains(top, " mmdebstrap ") {
			t.Errorf("the title is not in the top edge:\n%s", pane)
		}
	}
}

func TestThroughput(t *testing.T) {
	start := time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)
	sample := func(after time.Duration, done int64) progressSample {
		return progressSample{at: start.Add(after), done: done}
	}
	tests := []struct {
		name    string
		samples []progressSample
		want    float64
		wantOK  bool
	}{
		{name: "one sample", samples: []progressSample{sample(0, 0)}},
		{name: "too soon to say", samples: []progressSample{sample(0, 0), sample(2*time.Second, 4_000_000)}},
		{name: "settled", samples: []progressSample{sample(0, 0), sample(5*time.Second, 10_000_000)}, want: 2_000_000, wantOK: true},
		{name: "no progress", samples: []progressSample{sample(0, 5), sample(8*time.Second, 5)}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := throughput(test.samples)
			if ok != test.wantOK || got != test.want {
				t.Errorf("throughput = %v, %v; want %v, %v", got, ok, test.want, test.wantOK)
			}
		})
	}
	// The kept samples always reach back at least the window: the oldest
	// goes only once the next one is itself older than the window, so
	// seldom progress still gets a rate.
	kept := recentSamples([]progressSample{sample(0, 0), sample(1*time.Second, 1), sample(12*time.Second, 2)}, start.Add(12*time.Second))
	if len(kept) != 2 || kept[0].done != 1 {
		t.Errorf("recentSamples kept %+v, want the last two", kept)
	}
	sparse := recentSamples([]progressSample{sample(0, 0), sample(16*time.Second, 5)}, start.Add(16*time.Second))
	if len(sparse) != 2 {
		t.Errorf("recentSamples kept %+v, want both of two sparse samples", sparse)
	}
}
