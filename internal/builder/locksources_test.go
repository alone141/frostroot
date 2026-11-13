package builder

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// sourceLinesLock returns a lock of 22.04 with the source lines and the
// repositories a build of sampleSources from mirrorURL writes.
func sourceLinesLock(t *testing.T, mirrorURL string) (recipe.Lockfile, distro.Release) {
	t.Helper()
	release, err := distro.Lookup("22.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	return recipe.Lockfile{
		Mirror:       mirrorURL,
		Sources:      SourceLines(release, mirrorURL, sampleSources(), ImageKeyringDir),
		Repositories: lockRepositories(release, sampleSources(), nil),
	}, release
}

func TestCheckLockSourceLines(t *testing.T) {
	const archiveLine = "deb http://archive.ubuntu.com/ubuntu "
	dockerLine := "deb [signed-by=/etc/apt/keyrings/frostroot-docker.gpg] https://download.docker.com/linux/ubuntu jammy stable"
	testCases := []struct {
		name      string
		mirrorURL string
		edit      func(lock *recipe.Lockfile)
		wantText  string // "" when the lock must pass
	}{
		{name: "untouched lock", edit: func(*recipe.Lockfile) {}},
		{
			name: "older lock with main and universe",
			edit: func(lock *recipe.Lockfile) {
				for index, suite := range []string{"jammy", "jammy-updates", "jammy-security"} {
					lock.Sources[index] = archiveLine + suite + " main universe"
				}
			},
		},
		{name: "pockets in another order", edit: func(lock *recipe.Lockfile) { lock.Sources[0], lock.Sources[2] = lock.Sources[2], lock.Sources[0] }},
		{name: "mirror lock", mirrorURL: "http://mirror.example/ubuntu", edit: func(*recipe.Lockfile) {}},
		{name: "mirror lock with a trailing slash", mirrorURL: "http://mirror.example/ubuntu/", edit: func(*recipe.Lockfile) {}},
		{
			name:      "mirror lock with a trailing slash on its lines only",
			mirrorURL: "http://mirror.example/ubuntu",
			edit: func(lock *recipe.Lockfile) {
				lock.Sources[0] = "deb http://mirror.example/ubuntu/ jammy main restricted universe multiverse"
			},
		},
		{
			name:     "extra pocket",
			edit:     func(lock *recipe.Lockfile) { lock.Sources = append(lock.Sources, archiveLine+"jammy-proposed main") },
			wantText: "suite jammy-proposed is none of the archive's pockets",
		},
		{
			name:     "another release's pocket",
			edit:     func(lock *recipe.Lockfile) { lock.Sources[0] = archiveLine + "noble main" },
			wantText: "suite noble is none of the archive's pockets",
		},
		{
			name:     "pocket twice",
			edit:     func(lock *recipe.Lockfile) { lock.Sources = append(lock.Sources, archiveLine+"jammy-updates main") },
			wantText: "the jammy-updates pocket has a line already",
		},
		{
			name:     "security pocket missing",
			edit:     func(lock *recipe.Lockfile) { lock.Sources = slices.Delete(lock.Sources, 2, 3) },
			wantText: "no source line for the archive's jammy-security pocket",
		},
		{
			name:     "archive on another host than the archive",
			edit:     func(lock *recipe.Lockfile) { lock.Sources[2] = "deb http://evil.example/ubuntu jammy-security main" },
			wantText: "the archive is http://archive.ubuntu.com/ubuntu in the lock, not http://evil.example/ubuntu",
		},
		{
			name:      "archive on another host than the mirror",
			mirrorURL: "http://mirror.example/ubuntu",
			edit:      func(lock *recipe.Lockfile) { lock.Sources[1] = archiveLine + "jammy-updates main" },
			wantText:  "the archive is http://mirror.example/ubuntu in the lock, not http://archive.ubuntu.com/ubuntu",
		},
		{
			name:     "component the release does not have",
			edit:     func(lock *recipe.Lockfile) { lock.Sources[0] = archiveLine + "jammy main partner" },
			wantText: "component partner is none of the release's",
		},
		{
			name:     "component twice",
			edit:     func(lock *recipe.Lockfile) { lock.Sources[0] = archiveLine + "jammy main universe main" },
			wantText: "component main appears twice",
		},
		{
			name: "signed line of no repository",
			edit: func(lock *recipe.Lockfile) {
				lock.Sources = append(lock.Sources, "deb [signed-by=/etc/apt/keyrings/frostroot-evil.gpg] https://evil.example/ubuntu jammy main")
			},
			wantText: "keyring frostroot-evil.gpg belongs to no repository of the lock",
		},
		{
			name: "signed line with another URL",
			edit: func(lock *recipe.Lockfile) {
				lock.Sources[3] = strings.Replace(dockerLine, "download.docker.com", "evil.example", 1)
			},
			wantText: "are not those of repository docker",
		},
		{
			name:     "signed line with another suite",
			edit:     func(lock *recipe.Lockfile) { lock.Sources[3] = strings.Replace(dockerLine, " jammy ", " noble ", 1) },
			wantText: "are not those of repository docker",
		},
		{
			name:     "signed line with other components",
			edit:     func(lock *recipe.Lockfile) { lock.Sources[3] = dockerLine + " test" },
			wantText: "are not those of repository docker",
		},
		{
			name:     "repository with no line",
			edit:     func(lock *recipe.Lockfile) { lock.Sources = slices.Delete(lock.Sources, 3, 4) },
			wantText: "no source line for repository docker",
		},
		{
			name: "repository named twice, the second with another URL",
			edit: func(lock *recipe.Lockfile) {
				other := lock.Repositories[0]
				other.URL = "https://evil.example/ubuntu"
				lock.Repositories = append(lock.Repositories, other)
				lock.Sources[3] = strings.Replace(dockerLine, "download.docker.com/linux", "evil.example", 1)
			},
			wantText: "it names repository docker twice",
		},
		{
			name:     "repository line twice",
			edit:     func(lock *recipe.Lockfile) { lock.Sources = append(lock.Sources, dockerLine) },
			wantText: "repository docker has a line already",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			lock, release := sourceLinesLock(t, testCase.mirrorURL)
			if lock.Sources[3] != dockerLine {
				t.Fatalf("Sources[3] = %q, want %q", lock.Sources[3], dockerLine)
			}
			testCase.edit(&lock)
			err := checkLockSourceLines(lock, release)
			if testCase.wantText == "" {
				if err != nil {
					t.Errorf("checkLockSourceLines(%q) = %v, want nil", lock.Sources, err)
				}
				return
			}
			if !errors.Is(err, pool.ErrBadLock) || !strings.Contains(err.Error(), testCase.wantText) {
				t.Errorf("checkLockSourceLines(%q) = %v, want %v saying %q", lock.Sources, err, pool.ErrBadLock, testCase.wantText)
			}
		})
	}
}

// A source line whose URL holds a password with "?" or "#" in it, which
// net/url does not read as a userinfo while apt does, is refused as
// carrying credentials, and the refusal does not quote the password.
func TestCheckUbuntuLockEntriesRefusesCredentialsNetURLDoesNotSee(t *testing.T) {
	for _, sourceURL := range []string{
		"https://deploy:12?s3cret@apt.example/ubuntu",
		"https://deploy:pa?s3cret@apt.example/ubuntu",
		"https://deploy:pa#s3cret@apt.example/ubuntu",
		"user:s3cret@mirror.example.com/ubuntu",
		"https:/user:s3cret@mirror.example.com/ubuntu",
	} {
		line := "deb " + sourceURL + " jammy main universe"
		err := checkUbuntuLockEntries(recipe.Lockfile{Sources: []string{line}})
		if !errors.Is(err, pool.ErrBadLock) {
			t.Errorf("checkUbuntuLockEntries(%q) = %v, want %v", line, err, pool.ErrBadLock)
			continue
		}
		if strings.Contains(err.Error(), "s3cret") {
			t.Errorf("the refusal of %q quotes the secret: %v", line, err)
		}
		if !strings.Contains(err.Error(), "REDACTED@") {
			t.Errorf("the refusal of %q should name the line, redacted: %v", line, err)
		}
		if !strings.Contains(err.Error(), "credentials") {
			t.Errorf("the refusal of %q should say it carries credentials: %v", line, err)
		}
	}
}

func TestBuildOfflineRefusesSourceLinesNoBuildWrites(t *testing.T) {
	testCases := map[string]func(lock *recipe.Lockfile){
		"proposed pocket added": func(lock *recipe.Lockfile) {
			lock.Sources = append(lock.Sources, "deb http://archive.ubuntu.com/ubuntu jammy-proposed main")
		},
		"security pocket dropped": func(lock *recipe.Lockfile) {
			lock.Sources = slices.DeleteFunc(lock.Sources, func(line string) bool { return strings.Contains(line, "-security") })
		},
		"archive on another host": func(lock *recipe.Lockfile) {
			lock.Sources[0] = "deb http://evil.example/ubuntu jammy main universe"
		},
	}
	for name, edit := range testCases {
		t.Run(name, func(t *testing.T) {
			options, _ := newTestOptions(t)
			editVendoredLock(edit)(t, options)
			lockPath := filepath.Join(options.RecipeDir, LockFileName)
			lockBefore, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			options.Offline = true
			bootstrapper := &offlineFakeBootstrapper{}
			result, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleRecipe(), options)
			if !errors.Is(err, pool.ErrBadLock) || !strings.Contains(err.Error(), "source line") {
				t.Fatalf("Build error = %v, want %v naming the source line", err, pool.ErrBadLock)
			}
			if bootstrapper.runCount != 0 || result.WorkDir != "" {
				t.Errorf("runs = %d, Result = %+v; want no bootstrap and no work directory", bootstrapper.runCount, result)
			}
			if _, err := os.Stat(expectedTarballPath(options)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("%s must not exist after a refused build, Stat error = %v", expectedTarballPath(options), err)
			}
			assertNoTemporaryFiles(t, options.RecipeDir)
			if lockAfter, err := os.ReadFile(lockPath); err != nil || !bytes.Equal(lockAfter, lockBefore) {
				t.Errorf("the lock changed in a refused build (read error %v)", err)
			}
		})
	}
}

// A source line whose URL is refused for something before its query, or
// for its fragment, was quoted whole, a token after "?" or "#" with it: by
// recipe.CheckSourceURL's message and by the line the refusal names.
func TestSourceLineRefusalsQuoteNothingOfAQueryOrFragment(t *testing.T) {
	for _, sourceURL := range []string{
		"https://host.example/x?token=s3cret#frag",
		"https://host.example/x?token=s3 cret",
		"https://host.example/x?token=[s3cret]",
		"ftp://host.example/x?token=s3cret",
		"https:///x?token=s3cret",
		"https://host.example/%zz?token=s3cret",
		"host.example/x?token=s3cret",
		"https://host.example/x#s3cret",
	} {
		line := "deb " + sourceURL + " jammy main universe"
		if err := checkLockSourceLine(line); err == nil || strings.Contains(err.Error(), "s3") || strings.Contains(err.Error(), "frag") {
			t.Errorf("checkLockSourceLine(%q) = %v, want a refusal quoting nothing after the ? or #", line, err)
		}
		err := checkUbuntuLockEntries(recipe.Lockfile{Sources: []string{line}})
		if !errors.Is(err, pool.ErrBadLock) || strings.Contains(err.Error(), "s3") || strings.Contains(err.Error(), "frag") {
			t.Errorf("checkUbuntuLockEntries(%q) = %v, want %v quoting nothing after the ? or #", line, err, pool.ErrBadLock)
		}
	}
	if got, want := redactSourceLine("deb https://u:p@host.example/x?token=s3 cret jammy main"), "deb https://REDACTED@host.example/x?REDACTED"; got != want {
		t.Errorf("redactSourceLine = %q, want %q", got, want)
	}
}
