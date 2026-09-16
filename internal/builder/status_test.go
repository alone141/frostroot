package builder

import (
	"slices"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

// realisticDpkgStatus has a multi-line field that repeats a Status line, a
// removed package that left configuration files, and an epoch version.
const realisticDpkgStatus = `Package: bash
Essential: yes
Status: install ok installed
Priority: required
Architecture: amd64
Version: 5.2.21-2ubuntu4
Description: GNU Bourne Again SHell
 Bash is an sh-compatible command language interpreter.
 .
 Status: install ok installed
Homepage: http://tiswww.case.edu/php/chet/bash/bashtop.html

Package: gone
Status: deinstall ok config-files
Architecture: amd64
Version: 1.0-1
Conffiles:
 /etc/gone.conf 0123456789abcdef

Package: git
Status: install ok installed
Architecture: amd64
Version: 1:2.43.0-1ubuntu7.1
Multi-Arch: foreign

Package: libc6
Status: install ok installed
Architecture: amd64
Version: 2.39-0ubuntu8.3
`

// parseDpkgStatus parses status text, failing the test on error.
func parseDpkgStatus(t *testing.T, statusText string) []recipe.LockPackage {
	t.Helper()
	installedPackages, err := ParseDpkgStatus(strings.NewReader(statusText))
	if err != nil {
		t.Fatal(err)
	}
	return installedPackages
}

func TestParseDpkgStatusRealisticFile(t *testing.T) {
	got := parseDpkgStatus(t, realisticDpkgStatus)
	want := []recipe.LockPackage{
		// Continuation lines must not clobber fields, the removed package is
		// skipped, the epoch survives, and the result is sorted by name.
		{Name: "bash", Version: "5.2.21-2ubuntu4", Arch: "amd64"},
		{Name: "git", Version: "1:2.43.0-1ubuntu7.1", Arch: "amd64"},
		{Name: "libc6", Version: "2.39-0ubuntu8.3", Arch: "amd64"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ParseDpkgStatus =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseDpkgStatusSelectionStateDoesNotMatter(t *testing.T) {
	// "hold ok installed" and "deinstall ok installed" are both still on disk;
	// half-configured, unpacked and not-installed packages are not usable.
	statusText := `Package: held
Status: hold ok installed
Architecture: amd64
Version: 1

Package: marked
Status: deinstall ok installed
Architecture: all
Version: 2

Package: half
Status: install ok half-configured
Architecture: amd64
Version: 3

Package: unpacked
Status: install ok unpacked
Architecture: amd64
Version: 4

Package: purged
Status: purge ok not-installed
Architecture: amd64
`
	got := parseDpkgStatus(t, statusText)
	want := []recipe.LockPackage{
		{Name: "held", Version: "1", Arch: "amd64"},
		{Name: "marked", Version: "2", Arch: "all"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ParseDpkgStatus = %+v, want %+v", got, want)
	}
}

func TestParseDpkgStatusSortsSameNameByArch(t *testing.T) {
	statusText := "Package: libc6\nStatus: install ok installed\nArchitecture: i386\nVersion: 2\n\n" +
		"Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2\n"
	installedPackages := parseDpkgStatus(t, statusText)
	if len(installedPackages) != 2 || installedPackages[0].Arch != "amd64" || installedPackages[1].Arch != "i386" {
		t.Fatalf("ParseDpkgStatus = %+v, want amd64 before i386 so that the lock is deterministic", installedPackages)
	}
}

func TestParseDpkgStatusLayoutEdgeCases(t *testing.T) {
	testCases := []struct {
		name       string
		statusText string
		wantCount  int
	}{
		{
			name:       "last stanza without a trailing blank line",
			statusText: "Package: only\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1",
			wantCount:  1,
		},
		{
			name:       "extra blank lines",
			statusText: "\n\nPackage: a1\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1\n\n\n\nPackage: b1\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2\n\n",
			wantCount:  2,
		},
		{
			name:       "very long line",
			statusText: "Package: big\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1\nDepends: " + strings.Repeat("x", 200<<10) + "\n",
			wantCount:  1,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := parseDpkgStatus(t, testCase.statusText); len(got) != testCase.wantCount {
				t.Fatalf("ParseDpkgStatus = %+v, want %d packages", got, testCase.wantCount)
			}
		})
	}
}

func TestParseDpkgStatusInstalledWithoutVersionIsAnError(t *testing.T) {
	// The lock promises exact versions, so a malformed stanza must not become
	// a lock entry with an empty version.
	statusText := "Package: odd\nStatus: install ok installed\nArchitecture: amd64\n"
	_, err := ParseDpkgStatus(strings.NewReader(statusText))
	if err == nil || !strings.Contains(err.Error(), "odd") {
		t.Fatalf("ParseDpkgStatus error = %v, want one naming the package", err)
	}
}

func TestParseDpkgStatusWithNothingInstalledIsAnError(t *testing.T) {
	// An empty status file means the bootstrap produced nothing, and a lock
	// with no packages must never be written.
	for _, statusText := range []string{"", "\n\n", "Package: gone\nStatus: purge ok not-installed\n"} {
		if _, err := ParseDpkgStatus(strings.NewReader(statusText)); err == nil {
			t.Errorf("ParseDpkgStatus(%q) succeeded, want an error", statusText)
		}
	}
}
