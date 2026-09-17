package builder

import (
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// realExtendedStates is an excerpt of the extended_states of a real noble
// image (the v0.6 spike): apt writes the native architecture even for a
// package of architecture "all", such as git-man, and keeps a paragraph with
// Auto-Installed: 0 for a mark it has taken back.
const realExtendedStates = "Package: libnghttp2-14\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
	"Package: git-man\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
	"Package: curl\nArchitecture: amd64\nAuto-Installed: 0\n\n" +
	"Package: libssh-4\nArchitecture: amd64\nAuto-Installed: 1\n\n"

func TestParseExtendedStates(t *testing.T) {
	marks, err := ParseExtendedStates(strings.NewReader(realExtendedStates))
	if err != nil {
		t.Fatal(err)
	}
	wantMarks := []AutoMark{{Name: "libnghttp2-14", Arch: "amd64"}, {Name: "git-man", Arch: "amd64"}, {Name: "libssh-4", Arch: "amd64"}}
	if !slices.Equal(marks, wantMarks) {
		t.Errorf("marks = %+v, want %+v (in file order, without the mark taken back)", marks, wantMarks)
	}
	if marks, err := ParseExtendedStates(strings.NewReader("")); err != nil || len(marks) != 0 {
		t.Errorf("an empty file (the image had nothing to mark) = %+v, %v; want no marks and no error", marks, err)
	}
	if _, err := ParseExtendedStates(strings.NewReader("Auto-Installed: 1\n")); err == nil || !strings.Contains(err.Error(), "Package") {
		t.Errorf("a mark without a package name should be an error, got %v", err)
	}
}

func TestRenderExtendedStatesRoundTrip(t *testing.T) {
	// The offline build renders the lock's marks the way apt wrote them, so
	// that apt reads the same marks back.
	installed := []recipe.LockPackage{
		{Name: "curl", Version: "8.5.0-2ubuntu10.6", Arch: "amd64"},
		{Name: "git-man", Version: "1:2.43.0-1ubuntu7.3", Arch: "all"},
		{Name: "libnghttp2-14", Version: "1.59.0-1ubuntu0.2", Arch: "amd64"},
		{Name: "libssh-4", Version: "0.10.6-2ubuntu0.1", Arch: "amd64"},
	}
	marks, err := ParseExtendedStates(strings.NewReader(realExtendedStates))
	if err != nil {
		t.Fatal(err)
	}
	markAutoInstalled(installed, marks, "amd64")
	var autoNames []string
	for _, locked := range installed {
		if locked.Auto {
			autoNames = append(autoNames, locked.Name)
		}
	}
	if wantNames := []string{"git-man", "libnghttp2-14", "libssh-4"}; !slices.Equal(autoNames, wantNames) {
		t.Errorf("auto = %q, want %q: git-man is arch all, which apt records as the native arch", autoNames, wantNames)
	}

	rendered := RenderExtendedStates(installed, "amd64")
	wantRendered := "Package: git-man\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
		"Package: libnghttp2-14\nArchitecture: amd64\nAuto-Installed: 1\n\n" +
		"Package: libssh-4\nArchitecture: amd64\nAuto-Installed: 1\n\n"
	if rendered != wantRendered {
		t.Errorf("RenderExtendedStates =\n%s\nwant\n%s", rendered, wantRendered)
	}
	reparsed, err := ParseExtendedStates(strings.NewReader(rendered))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reparsed, []AutoMark{{Name: "git-man", Arch: "amd64"}, {Name: "libnghttp2-14", Arch: "amd64"}, {Name: "libssh-4", Arch: "amd64"}}) {
		t.Errorf("the rendered file parses back to %+v", reparsed)
	}
	if RenderExtendedStates([]recipe.LockPackage{{Name: "curl", Arch: "amd64"}}, "amd64") != "" {
		t.Error("no marks should render as an empty file, which is what apt writes when it has nothing to say")
	}
}

func TestMarkAutoInstalledIgnoresUnknownMarks(t *testing.T) {
	installed := []recipe.LockPackage{{Name: "git", Version: "1", Arch: "amd64", Auto: true}}
	markAutoInstalled(installed, []AutoMark{{Name: "removed-since", Arch: "amd64"}, {Name: "git", Arch: "i386"}}, "amd64")
	if installed[0].Auto {
		t.Error("a mark for another architecture or a removed package must not mark git, and a stale Auto must be cleared")
	}
}
