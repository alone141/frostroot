package distro

import (
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestFamilyOf(t *testing.T) {
	testCases := []struct {
		name       string
		field      string
		wantFamily Family
		wantError  bool
	}{
		// Every recipe and lock written before there was a second family.
		{name: "no distro is Ubuntu", field: "", wantFamily: Ubuntu},
		{name: "ubuntu", field: "ubuntu", wantFamily: Ubuntu},
		{name: "the field is lowercase", field: "Ubuntu", wantError: true},
		{name: "a family frostroot does not build", field: "debian", wantError: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			family, err := FamilyOf(testCase.field)
			if testCase.wantError {
				if !errors.Is(err, ErrUnknownFamily) || !strings.Contains(err.Error(), "known: ubuntu") {
					t.Fatalf("FamilyOf(%q) = %q, %v; want ErrUnknownFamily naming the known families", testCase.field, family, err)
				}
				return
			}
			if err != nil || family != testCase.wantFamily {
				t.Fatalf("FamilyOf(%q) = %q, %v; want %q", testCase.field, family, err, testCase.wantFamily)
			}
		})
	}
}

func TestFamiliesStartWithUbuntu(t *testing.T) {
	if got := Families(); len(got) == 0 || got[0] != Ubuntu {
		t.Fatalf("Families() = %q, want Ubuntu first: the form offers it first", got)
	}
}

func TestCheck(t *testing.T) {
	if err := Check(Ubuntu, "24.04", "amd64"); err != nil {
		t.Errorf("Check(Ubuntu, 24.04, amd64) = %v, want nil", err)
	}
	// Ubuntu's problems are Lookup's, both of them at once.
	err := Check(Ubuntu, "18.04", "arm64")
	if !errors.Is(err, ErrUnknownRelease) || !errors.Is(err, ErrUnsupportedArch) {
		t.Errorf("Check(Ubuntu, 18.04, arm64) = %v, want ErrUnknownRelease and ErrUnsupportedArch", err)
	}
	if err := Check("debian", "12", "amd64"); !errors.Is(err, ErrUnknownFamily) {
		t.Errorf("Check(debian, 12, amd64) = %v, want ErrUnknownFamily", err)
	}
}

func TestReadersOfAnUnknownFamily(t *testing.T) {
	if got := SupportedVersions("debian"); got != nil {
		t.Errorf("SupportedVersions(debian) = %q, want none", got)
	}
	if got := NewestVersion("debian"); got != "" {
		t.Errorf("NewestVersion(debian) = %q, want none", got)
	}
	if got := VersionsInStandardSupport("debian"); got != nil {
		t.Errorf("VersionsInStandardSupport(debian) = %q, want none", got)
	}
	if got := SupportedVersions(Ubuntu); !slices.Contains(got, NewestVersion(Ubuntu)) {
		t.Errorf("NewestVersion(Ubuntu) = %q is not among %q", NewestVersion(Ubuntu), got)
	}
}
