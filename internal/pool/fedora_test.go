package pool

import (
	"errors"
	"slices"
	"testing"

	"frostroot/internal/recipe"
)

// fedoraLock is a Fedora lock whose image and tools tree share glibc.
func fedoraLock() recipe.Lockfile {
	glibc := recipe.LockPackage{Name: "glibc", Version: "2.43-2.fc44", Arch: "x86_64", Reason: "Dependency", SHA256: "aa", Size: 10,
		Filename: "Packages/g/glibc-2.43-2.fc44.x86_64.rpm", Source: "updates", SourceRPM: "glibc-2.43-2.fc44.src.rpm"}
	git := recipe.LockPackage{Name: "git", Version: "2.55.0-1.fc44", Arch: "x86_64", Reason: "User", SHA256: "bb", Size: 20,
		Filename: "Packages/g/git-2.55.0-1.fc44.x86_64.rpm", Source: "updates", SourceRPM: "git-2.55.0-1.fc44.src.rpm"}
	dnf5 := recipe.LockPackage{Name: "dnf5", Version: "5.4.6.0-1.fc44", Arch: "x86_64", SHA256: "cc", Size: 30,
		Filename: "Packages/d/dnf5-5.4.6.0-1.fc44.x86_64.rpm", Source: "fedora", SourceRPM: "dnf5-5.4.6.0-1.fc44.src.rpm"}
	return recipe.Lockfile{
		Version: 1, Distro: "fedora", Release: "44", Arch: "amd64",
		Repositories: []recipe.LockRepository{
			{Name: "fedora", URL: "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Everything/x86_64/os", KeySHA256: "k"},
			{Name: "updates", URL: "https://dl.fedoraproject.org/pub/fedora/linux/updates/44/Everything/x86_64", KeySHA256: "k"},
		},
		Packages: []recipe.LockPackage{git, glibc},
		Tools:    []recipe.LockPackage{dnf5, glibc},
	}
}

func TestFedoraManifest(t *testing.T) {
	lock := fedoraLock()
	if DirName(lock) != RPMsDirName || DirName(lockFor(entryFor("curl", "1"))) != DebsDirName {
		t.Errorf("DirName = %q for Fedora, %q for Ubuntu", DirName(lock), DirName(lockFor(entryFor("curl", "1"))))
	}
	entries, err := Manifest(lock)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		names = append(names, entry.FileName)
	}
	// The image's packages, then the tools tree's, glibc once.
	if want := []string{"git-2.55.0-1.fc44.x86_64.rpm", "glibc-2.43-2.fc44.x86_64.rpm", "dnf5-5.4.6.0-1.fc44.x86_64.rpm"}; !slices.Equal(names, want) {
		t.Errorf("manifest = %q, want %q", names, want)
	}
	if got := entries[2].DownloadURL(); got != "https://dl.fedoraproject.org/pub/fedora/linux/releases/44/Everything/x86_64/os/Packages/d/dnf5-5.4.6.0-1.fc44.x86_64.rpm" {
		t.Errorf("dnf5's URL = %q", got)
	}
	// An update the repository has dropped is still in Fedora's build
	// system, signed with the release's key.
	if got := FallbackURL(lock)(entries[0]); got != "https://kojipkgs.fedoraproject.org/packages/git/2.55.0/1.fc44/data/signed/6d9f90a6/x86_64/git-2.55.0-1.fc44.x86_64.rpm" {
		t.Errorf("git's fallback = %q", got)
	}
}

func TestFedoraManifestRefusesAHostileLock(t *testing.T) {
	testCases := map[string]struct {
		breakLock func(*recipe.Lockfile)
		want      error
	}{
		"a repository over http":        {func(l *recipe.Lockfile) { l.Repositories[0].URL = "http://dl.fedoraproject.org/x" }, ErrBadLock},
		"a repository with credentials": {func(l *recipe.Lockfile) { l.Repositories[1].URL = "https://user:secret@dl.fedoraproject.org/x" }, ErrBadLock},
		"a package from nowhere":        {func(l *recipe.Lockfile) { l.Packages[0].Source = "rpmfusion" }, ErrBadLock},
		"a path out of the pool":        {func(l *recipe.Lockfile) { l.Packages[0].Filename = "../../etc/passwd.rpm" }, ErrBadLock},
		"not an rpm":                    {func(l *recipe.Lockfile) { l.Packages[0].Filename = "Packages/g/git.deb" }, ErrBadLock},
		"one file, two checksums":       {func(l *recipe.Lockfile) { l.Tools[1].SHA256 = "dd" }, ErrBadLock},
		"no checksum":                   {func(l *recipe.Lockfile) { l.Tools[0].SHA256 = "" }, ErrNoChecksums},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			lock := fedoraLock()
			testCase.breakLock(&lock)
			if _, err := Manifest(lock); !errors.Is(err, testCase.want) {
				t.Errorf("Manifest = %v, want %v", err, testCase.want)
			}
		})
	}
}

// TestKojiURLRefusesWhatWouldLeaveThePath: the source rpm and the arch come
// from the lock, and reach a URL.
func TestKojiURLRefusesWhatWouldLeaveThePath(t *testing.T) {
	for _, entry := range []Entry{
		{Arch: "x86_64", FileName: "x.rpm", SourceRPM: "git-2.55.0-1.fc44.src"},
		{Arch: "x86_64", FileName: "x.rpm", SourceRPM: "git/../../evil-1-1.src.rpm"},
		{Arch: "x86_64", FileName: "x.rpm", SourceRPM: "..-1-1.src.rpm"},
		{Arch: "../x", FileName: "x.rpm", SourceRPM: "git-2.55.0-1.fc44.src.rpm"},
		{Arch: "x86_64", FileName: "x.rpm", SourceRPM: "nodashes.src.rpm"},
	} {
		if got := kojiURL(entry, "6d9f90a6"); got != "" {
			t.Errorf("kojiURL(%+v) = %q, want none", entry, got)
		}
	}
}
