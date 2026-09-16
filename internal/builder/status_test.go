package builder

import (
	"strings"
	"testing"
)

const sampleStatus = `Package: bash
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

func TestParseDpkgStatusKeepsOnlyInstalled(t *testing.T) {
	pkgs, err := ParseDpkgStatus(strings.NewReader(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("want 3 installed packages, got %#v", pkgs)
	}
	for _, p := range pkgs {
		if p.Name == "gone" {
			t.Fatal("deinstalled package must not be in the lock")
		}
	}
}

func TestParseDpkgStatusSortedByName(t *testing.T) {
	pkgs, err := ParseDpkgStatus(strings.NewReader(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bash", "git", "libc6"}
	for i, w := range want {
		if pkgs[i].Name != w {
			t.Fatalf("order: got %#v", pkgs)
		}
	}
}

func TestParseDpkgStatusFields(t *testing.T) {
	pkgs, err := ParseDpkgStatus(strings.NewReader(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	if pkgs[1].Name != "git" || pkgs[1].Version != "1:2.43.0-1ubuntu7.1" || pkgs[1].Arch != "amd64" {
		t.Fatalf("git entry (epoch must survive): %#v", pkgs[1])
	}
	if pkgs[0].Version != "5.2.21-2ubuntu4" {
		t.Fatalf("continuation lines must not clobber fields: %#v", pkgs[0])
	}
}

func TestParseDpkgStatusWantStateDoesNotMatter(t *testing.T) {
	// "hold ok installed" and "deinstall ok installed" are both still on disk;
	// half-configured, unpacked and not-installed packages are not usable.
	in := `Package: held
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
	pkgs, err := ParseDpkgStatus(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 || pkgs[0].Name != "held" || pkgs[1].Name != "marked" || pkgs[1].Arch != "all" {
		t.Fatalf("got %#v", pkgs)
	}
}

func TestParseDpkgStatusSameNameSortsByArch(t *testing.T) {
	in := "Package: libc6\nStatus: install ok installed\nArchitecture: i386\nVersion: 2\n\n" +
		"Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2\n"
	pkgs, err := ParseDpkgStatus(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 || pkgs[0].Arch != "amd64" || pkgs[1].Arch != "i386" {
		t.Fatalf("order must be total so the lock is deterministic: %#v", pkgs)
	}
}

func TestParseDpkgStatusHandlesTrailingStanzaWithoutBlankLine(t *testing.T) {
	in := "Package: only\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1"
	pkgs, err := ParseDpkgStatus(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].Name != "only" {
		t.Fatalf("got %#v", pkgs)
	}
}

func TestParseDpkgStatusToleratesExtraBlankLines(t *testing.T) {
	in := "\n\nPackage: a1\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1\n\n\n\nPackage: b1\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2\n\n"
	pkgs, err := ParseDpkgStatus(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %#v", pkgs)
	}
}

func TestParseDpkgStatusLongLines(t *testing.T) {
	long := strings.Repeat("x", 200*1024)
	in := "Package: big\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1\nDepends: " + long + "\n"
	pkgs, err := ParseDpkgStatus(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 {
		t.Fatalf("got %#v", pkgs)
	}
}

func TestParseDpkgStatusInstalledWithoutVersionIsError(t *testing.T) {
	// The lock promises exact versions; a malformed stanza must not become a
	// lock entry with an empty version.
	in := "Package: odd\nStatus: install ok installed\nArchitecture: amd64\n"
	if _, err := ParseDpkgStatus(strings.NewReader(in)); err == nil || !strings.Contains(err.Error(), "odd") {
		t.Fatalf("want an error naming the package, got %v", err)
	}
}

func TestParseDpkgStatusEmptyIsError(t *testing.T) {
	// An empty status file means the bootstrap produced nothing; a lock with
	// zero packages must never be written.
	for _, in := range []string{"", "\n\n", "Package: gone\nStatus: purge ok not-installed\n"} {
		if _, err := ParseDpkgStatus(strings.NewReader(in)); err == nil {
			t.Fatalf("expected error for %q", in)
		}
	}
}
