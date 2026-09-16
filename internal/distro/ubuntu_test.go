package distro

import (
	"errors"
	"strings"
	"testing"
)

func TestLookupFocalIsEOLOnArchive(t *testing.T) {
	info, err := Lookup("20.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if info.Suite != "focal" {
		t.Fatalf("suite: got %q", info.Suite)
	}
	// The Task 0 spike found every focal pocket 404ing on old-releases: an LTS
	// release under ESM stays on the archive.
	if info.Base != "http://archive.ubuntu.com/ubuntu" {
		t.Fatalf("base: got %q", info.Base)
	}
	if !info.EOL {
		t.Fatal("20.04 must be flagged EOL so build can warn")
	}
	if len(info.Components) != 2 || info.Components[0] != "main" || info.Components[1] != "universe" {
		t.Fatalf("components: got %#v", info.Components)
	}
}

func TestLookupJammyAndNobleArchive(t *testing.T) {
	for _, tc := range []struct{ release, suite string }{{"22.04", "jammy"}, {"24.04", "noble"}} {
		info, err := Lookup(tc.release, "amd64")
		if err != nil {
			t.Fatalf("%s: %v", tc.release, err)
		}
		if info.Suite != tc.suite {
			t.Fatalf("%s suite: got %q", tc.release, info.Suite)
		}
		if info.Base != "http://archive.ubuntu.com/ubuntu" {
			t.Fatalf("%s base: got %q", tc.release, info.Base)
		}
		if info.EOL {
			t.Fatalf("%s must not be flagged EOL", tc.release)
		}
	}
}

func TestSourcesHasThreePockets(t *testing.T) {
	info, err := Lookup("22.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	got := info.Sources("")
	want := []string{
		"deb http://archive.ubuntu.com/ubuntu jammy main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
	}
	if len(got) != 3 {
		t.Fatalf("want 3 pockets, got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestSourcesMirrorOverrideReplacesAllThree(t *testing.T) {
	info, err := Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	got := info.Sources("http://mirror.example/ubuntu")
	if len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	for _, line := range got {
		if !strings.Contains(line, "http://mirror.example/ubuntu") {
			t.Fatalf("override missed: %q", line)
		}
		if strings.Contains(line, "archive.ubuntu.com") {
			t.Fatalf("default leaked: %q", line)
		}
	}
	if !strings.Contains(got[1], "noble-updates") || !strings.Contains(got[2], "noble-security") {
		t.Fatalf("pockets wrong: %#v", got)
	}
}

func TestLookupReturnsACopyOfComponents(t *testing.T) {
	a, err := Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	a.Components[0] = "restricted"
	b, err := Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if b.Components[0] != "main" {
		t.Fatalf("caller mutated the shared table: %#v", b.Components)
	}
}

func TestLookupUnknownRelease(t *testing.T) {
	if _, err := Lookup("18.04", "amd64"); !errors.Is(err, ErrUnknownRelease) {
		t.Fatalf("got %v", err)
	}
}

func TestLookupUnsupportedArch(t *testing.T) {
	if _, err := Lookup("24.04", "arm64"); !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("got %v", err)
	}
}

func TestLookupEmptyArch(t *testing.T) {
	_, err := Lookup("24.04", "")
	if !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "amd64") {
		t.Fatalf("message should name the supported arch: %v", err)
	}
}

func TestLookupReportsReleaseAndArchTogether(t *testing.T) {
	// validate prints every problem; one Lookup call must not hide the
	// release problem behind the arch problem.
	_, err := Lookup("18.04", "arm64")
	if !errors.Is(err, ErrUnknownRelease) || !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("want both errors, got %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, `"18.04"`) || !strings.Contains(msg, `"arm64"`) {
		t.Fatalf("message should name both values: %q", msg)
	}
	if strings.Index(msg, "release") > strings.Index(msg, "arch") {
		t.Fatalf("release should be reported first, in recipe order: %q", msg)
	}
}

func TestKnownReleases(t *testing.T) {
	got := KnownReleases()
	want := []string{"20.04", "22.04", "24.04"}
	if len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v", got)
		}
	}
}
