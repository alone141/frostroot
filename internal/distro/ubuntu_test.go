package distro

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestLookupSupportedReleases(t *testing.T) {
	testCases := []struct {
		version       string
		wantSuite     string
		wantEndOfLife bool
	}{
		// The Task 0 spike found every focal pocket returning 404 on
		// old-releases: an LTS release under ESM stays on the archive.
		{version: "20.04", wantSuite: "focal", wantEndOfLife: true},
		{version: "22.04", wantSuite: "jammy", wantEndOfLife: false},
		{version: "24.04", wantSuite: "noble", wantEndOfLife: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.version, func(t *testing.T) {
			release, err := Lookup(testCase.version, "amd64")
			if err != nil {
				t.Fatal(err)
			}
			if release.Suite != testCase.wantSuite {
				t.Errorf("Suite = %q, want %q", release.Suite, testCase.wantSuite)
			}
			if release.ArchiveURL != "http://archive.ubuntu.com/ubuntu" {
				t.Errorf("ArchiveURL = %q, want the Ubuntu archive", release.ArchiveURL)
			}
			if release.EndOfLife != testCase.wantEndOfLife {
				t.Errorf("EndOfLife = %v, want %v", release.EndOfLife, testCase.wantEndOfLife)
			}
			if !slices.Equal(release.Components, []string{"main", "universe"}) {
				t.Errorf("Components = %q, want main and universe", release.Components)
			}
		})
	}
}

func TestSourceLinesHasThreePockets(t *testing.T) {
	release, err := Lookup("22.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	got := release.SourceLines("")
	want := []string{
		"deb http://archive.ubuntu.com/ubuntu jammy main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SourceLines(\"\") =\n%q\nwant\n%q", got, want)
	}
}

func TestSourceLinesMirrorReplacesArchiveInAllPockets(t *testing.T) {
	release, err := Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	got := release.SourceLines("http://mirror.example/ubuntu")
	want := []string{
		"deb http://mirror.example/ubuntu noble main universe",
		"deb http://mirror.example/ubuntu noble-updates main universe",
		"deb http://mirror.example/ubuntu noble-security main universe",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("SourceLines(mirror) =\n%q\nwant\n%q", got, want)
	}
}

func TestLookupReturnsACopyOfComponents(t *testing.T) {
	first, err := Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	first.Components[0] = "restricted"
	second, err := Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if second.Components[0] != "main" {
		t.Fatalf("a caller modified the shared release table: %q", second.Components)
	}
}

func TestLookupRejects(t *testing.T) {
	testCases := []struct {
		name         string
		version      string
		arch         string
		wantErrors   []error
		wantInOutput []string
	}{
		{
			name:       "unknown release",
			version:    "18.04",
			arch:       "amd64",
			wantErrors: []error{ErrUnknownRelease},
		},
		{
			name:       "unsupported arch",
			version:    "24.04",
			arch:       "arm64",
			wantErrors: []error{ErrUnsupportedArch},
		},
		{
			name:         "empty arch names the supported one",
			version:      "24.04",
			arch:         "",
			wantErrors:   []error{ErrUnsupportedArch},
			wantInOutput: []string{"amd64"},
		},
		{
			// A validator prints every problem, so one Lookup call must not
			// hide the release problem behind the arch problem.
			name:         "release and arch reported together",
			version:      "18.04",
			arch:         "arm64",
			wantErrors:   []error{ErrUnknownRelease, ErrUnsupportedArch},
			wantInOutput: []string{`"18.04"`, `"arm64"`},
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := Lookup(testCase.version, testCase.arch)
			for _, wantError := range testCase.wantErrors {
				if !errors.Is(err, wantError) {
					t.Errorf("Lookup(%q, %q) error = %v, want it to wrap %v", testCase.version, testCase.arch, err, wantError)
				}
			}
			for _, wantText := range testCase.wantInOutput {
				if err == nil || !strings.Contains(err.Error(), wantText) {
					t.Errorf("error %v should mention %s", err, wantText)
				}
			}
		})
	}
}

func TestLookupReportsReleaseBeforeArch(t *testing.T) {
	_, err := Lookup("18.04", "arm64")
	if err == nil {
		t.Fatal("want an error")
	}
	message := err.Error()
	if strings.Index(message, "release") > strings.Index(message, "arch") {
		t.Fatalf("the release problem should come first, in recipe order: %q", message)
	}
}

func TestSupportedVersions(t *testing.T) {
	want := []string{"20.04", "22.04", "24.04"}
	if got := SupportedVersions(); !slices.Equal(got, want) {
		t.Fatalf("SupportedVersions() = %q, want %q", got, want)
	}
}
