package builder

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"

	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// newFedoraOfflineFixture builds sampleFedoraRecipe online with the fake,
// vendors every file its lock names, as frostroot vendor would, and returns
// options for an offline rebuild, the bootstrapper, and the lock's bytes.
func newFedoraOfflineFixture(t *testing.T) (Options, *fakeFedoraBootstrapper, []byte) {
	t.Helper()
	options, _ := newTestOptions(t)
	bootstrapper := newFakeFedoraBootstrapper()
	// The recipe's locale is en_US.UTF-8: an offline build needs its
	// langpack in the lock.
	bootstrapper.image = append(bootstrapper.image, fakeRPM{nevra: "glibc-langpack-en-0:2.43-2.fc44.x86_64", reason: "Dependency", repository: "updates",
		sourceRPM: "glibc-2.43-2.fc44.src.rpm", content: "langpack rpm"})
	if _, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options); err != nil {
		t.Fatal(err)
	}
	lockBytes, err := os.ReadFile(filepath.Join(options.RecipeDir, LockFileName))
	if err != nil {
		t.Fatal(err)
	}
	vendorDir := filepath.Join(options.RecipeDir, "vendor", "rpms")
	if err := os.MkdirAll(vendorDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, rpm := range slices.Concat(bootstrapper.image, bootstrapper.tools) {
		if err := os.WriteFile(filepath.Join(vendorDir, rpm.fileName()), []byte(rpm.content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	options.Offline = true
	return options, bootstrapper, lockBytes
}

// editLock loads the lock in recipeDir, changes it, and saves it.
func editLock(t *testing.T, recipeDir string, change func(*recipe.Lockfile)) {
	t.Helper()
	lockPath := filepath.Join(recipeDir, LockFileName)
	lock, err := recipe.LoadLock(lockPath)
	if err != nil {
		t.Fatal(err)
	}
	change(&lock)
	if err := recipe.SaveLock(lockPath, lock); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFedoraOfflineRebuildsFromTheLock(t *testing.T) {
	options, bootstrapper, lockBytes := newFedoraOfflineFixture(t)
	options.KeepWork = true
	result, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Offline || !result.Reproducible || result.InstalledPackageCount != 4 {
		t.Errorf("Result = %+v, want an offline, reproducible build of 4 packages", result)
	}
	if after, err := os.ReadFile(filepath.Join(options.RecipeDir, LockFileName)); err != nil || !bytes.Equal(after, lockBytes) {
		t.Errorf("an offline build changed the lock: %v", err)
	}
	if content, err := os.ReadFile(result.TarballPath); err != nil || string(content) != "fedora tarball" {
		t.Errorf("tarball = %q, %v", content, err)
	}
	spec := bootstrapper.runSpec
	// A cache of the build's own, so that nothing an earlier build left in
	// the shared one reaches it.
	if !spec.Offline || spec.CacheDir != filepath.Join(result.WorkDir, fedoraOfflineCacheName) || spec.LocalMirror != filepath.Join(result.WorkDir, fedoraLocalMirrorName) {
		t.Errorf("spec = %+v, want an offline build with its own cache and local mirror", spec)
	}
	// Each build's packages in local repositories of their own, under
	// their online names, which dnf5 records.
	for dir, want := range map[string][]string{
		"tools/fedora":  {"rpm-6.0.2-1.fc44.x86_64.rpm"},
		"tools/updates": {"dnf5-5.4.6.0-1.fc44.x86_64.rpm"},
		"image/fedora":  {"NetworkManager-libnm-1.54.0-1.fc44.x86_64.rpm", "bash-completion-2.17-2.fc44.noarch.rpm"},
		"image/updates": {"git-2.55.0-1.fc44.x86_64.rpm", "glibc-langpack-en-2.43-2.fc44.x86_64.rpm"},
	} {
		repositoryDir := filepath.Join(spec.LocalMirror, filepath.FromSlash(dir))
		entries, err := os.ReadDir(repositoryDir)
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if !slices.Equal(names, want) {
			t.Errorf("%s holds %q, want %q", dir, names, want)
		}
		if !slices.Contains(spec.LocalRepositories, repositoryDir) {
			t.Errorf("%s is not among the repositories to index: %q", dir, spec.LocalRepositories)
		}
	}
	// Exactly the locked packages, each by name, version and arch.
	for configDir, want := range map[string]string{
		spec.ImageConfigDir: "Packages=NetworkManager-libnm-1:1.54.0-1.fc44.x86_64\n         bash-completion-1:2.17-2.fc44.noarch\n         git-2.55.0-1.fc44.x86_64\n         glibc-langpack-en-2.43-2.fc44.x86_64\n",
		spec.ToolsConfigDir: "Packages=dnf5-5.4.6.0-1.fc44.x86_64\n         rpm-6.0.2-1.fc44.x86_64\n",
	} {
		conf, err := os.ReadFile(filepath.Join(configDir, "mkosi.conf"))
		if err != nil || !strings.HasSuffix(string(conf), want) {
			t.Errorf("%s/mkosi.conf = %q, %v; want it to end with %q", configDir, conf, err, want)
		}
	}
	for configDir, part := range map[string]string{spec.ToolsConfigDir: "tools", spec.ImageConfigDir: "image"} {
		repoFile, err := os.ReadFile(filepath.Join(configDir, fedoraPackageManagerTree, "etc", "yum.repos.d", "mkosi.repo"))
		if err != nil {
			t.Fatal(err)
		}
		for _, repository := range []string{"fedora", "updates"} {
			if want := "baseurl=file://" + filepath.Join(spec.LocalMirror, part, repository) + "\n"; !strings.Contains(string(repoFile), want) {
				t.Errorf("%s's repository file lacks %q:\n%s", part, want, repoFile)
			}
		}
		if strings.Contains(string(repoFile), "metalink=") {
			t.Errorf("%s's repository file reaches for the network:\n%s", part, repoFile)
		}
	}
	// dnf5's reasons as the lock has them, which the finalize script puts
	// back, and no query of what the mirrors have.
	reasons, err := os.ReadFile(filepath.Join(spec.ImageConfigDir, fedoraReasonsFile))
	wantReasons := "version = \"1.0\"\n\n[packages]\n" +
		"\"NetworkManager-libnm.x86_64\" = {reason = \"Dependency\"}\n" +
		"\"bash-completion.noarch\" = {reason = \"Weak Dependency\"}\n" +
		"\"git.x86_64\" = {reason = \"User\"}\n" +
		"\"glibc-langpack-en.x86_64\" = {reason = \"Dependency\"}\n\n"
	if err != nil || string(reasons) != wantReasons {
		t.Errorf("packages.toml = %q, %v; want %q", reasons, err, wantReasons)
	}
	finalize, err := os.ReadFile(filepath.Join(spec.ImageConfigDir, fedoraFinalizeName))
	if err != nil || !strings.Contains(string(finalize), `cat "$SRCDIR/packages.toml" >`) || strings.Contains(string(finalize), "--available") {
		t.Errorf("finalize = %s, %v; want the reasons put back and no query of the mirrors", finalize, err)
	}
}

// TestRenderFedoraReasonsSortsAsDnf5Does: dnf5 sorts by name.arch, byte by
// byte, so authselect-libs comes before authselect.
func TestRenderFedoraReasonsSortsAsDnf5Does(t *testing.T) {
	got := renderFedoraReasons([]recipe.LockPackage{
		{Name: "authselect", Arch: "x86_64", Reason: "Dependency"},
		{Name: "7zip", Arch: "x86_64", Reason: "Weak Dependency"},
		{Name: "authselect-libs", Arch: "x86_64", Reason: "Dependency"},
		{Name: "Xaw3d", Arch: "x86_64", Reason: "User"},
	})
	want := "version = \"1.0\"\n\n[packages]\n" +
		"\"7zip.x86_64\" = {reason = \"Weak Dependency\"}\n" +
		"\"Xaw3d.x86_64\" = {reason = \"User\"}\n" +
		"\"authselect-libs.x86_64\" = {reason = \"Dependency\"}\n" +
		"\"authselect.x86_64\" = {reason = \"Dependency\"}\n\n"
	if got != want {
		t.Errorf("renderFedoraReasons =\n%s\nwant\n%s", got, want)
	}
}

func TestBuildFedoraOfflineRefusals(t *testing.T) {
	testCases := map[string]struct {
		prepare   func(t *testing.T, options Options, imageRecipe *recipe.Recipe)
		wantError error
		wantText  string
	}{
		"no lock": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				if err := os.Remove(filepath.Join(options.RecipeDir, LockFileName)); err != nil {
					t.Fatal(err)
				}
			},
			wantError: ErrNoLock,
		},
		"an Ubuntu lock": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Distro = "ubuntu" })
			},
			wantError: ErrLockMismatch,
			wantText:  `distro "ubuntu" in the lock, "fedora" in the recipe`,
		},
		"another release": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Release = "43" })
			},
			wantError: ErrLockMismatch,
			wantText:  "release 43 in the lock, 44 in the recipe",
		},
		"a package added to the recipe": {
			prepare: func(_ *testing.T, _ Options, imageRecipe *recipe.Recipe) {
				imageRecipe.Packages.Include = append(imageRecipe.Packages.Include, "vim-enhanced")
			},
			wantError: ErrLockMismatch,
			wantText:  "packages added to the recipe: vim-enhanced",
		},
		// A locale is a package on Fedora.
		"a locale whose langpack is not locked": {
			prepare: func(_ *testing.T, _ Options, imageRecipe *recipe.Recipe) {
				imageRecipe.Locale.Lang = "tr_TR.UTF-8"
			},
			wantError: ErrLockMismatch,
			wantText:  "locale tr_TR.UTF-8 needs glibc-langpack-tr, which the lock does not hold",
		},
		"another key": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Repositories[1].KeySHA256 = strings.Repeat("0", 64) })
			},
			wantError: ErrLockMismatch,
			wantText:  "Fedora 44's key is not the one the lock recorded for repository updates",
		},
		"no key digest": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Repositories[0].KeySHA256 = "" })
			},
			wantError: ErrLockMismatch,
			wantText:  "records no key_sha256 for repository fedora",
		},
		"a vendored file missing": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				if err := os.Remove(filepath.Join(options.RecipeDir, "vendor", "rpms", "dnf5-5.4.6.0-1.fc44.x86_64.rpm")); err != nil {
					t.Fatal(err)
				}
			},
			wantError: ErrPoolIncomplete,
			wantText:  "dnf5-5.4.6.0-1.fc44.x86_64.rpm (missing) in vendor/rpms; run frostroot vendor",
		},
		"a repository Fedora 44 does not have": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) {
					lock.Repositories = append(lock.Repositories, recipe.LockRepository{Name: "rpmfusion", URL: "https://mirror.example/rpmfusion", KeySHA256: lock.Repositories[0].KeySHA256})
				})
			},
			wantError: pool.ErrBadLock,
			wantText:  `repository "rpmfusion" is not one of Fedora 44's`,
		},
		// What an offline build hands mkosi, one per line of mkosi.conf.
		"a version that is not an rpm's": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Packages[2].Version = "2.55.0-1.fc44\nPackages=evil" })
			},
			wantError: pool.ErrBadLock,
			wantText:  "which are not an rpm's",
		},
		"an arch that is not an rpm's": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Tools[0].Arch = "x86 64" })
			},
			wantError: pool.ErrBadLock,
			wantText:  "which are not an rpm's",
		},
		"a name that is not a package's": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Packages[2].Name = "git\n" })
			},
			wantError: pool.ErrBadLock,
		},
		"a package locked twice": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) {
					again := lock.Packages[2]
					again.Version = "2.54.0-1.fc44"
					again.Filename = "Packages/g/git-2.54.0-1.fc44.x86_64.rpm"
					lock.Packages = append(lock.Packages, again)
				})
			},
			wantError: pool.ErrBadLock,
			wantText:  "the image locks git.x86_64 twice",
		},
		"a reason dnf5 does not have": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Packages[0].Reason = "Clean" })
			},
			wantError: pool.ErrBadLock,
			wantText:  `reason "Clean"`,
		},
		"no tools tree": {
			prepare: func(t *testing.T, options Options, _ *recipe.Recipe) {
				t.Helper()
				editLock(t, options.RecipeDir, func(lock *recipe.Lockfile) { lock.Tools = nil })
			},
			wantError: pool.ErrBadLock,
			wantText:  "records no packages for the tools tree",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			options, _, _ := newFedoraOfflineFixture(t)
			imageRecipe := sampleFedoraRecipe()
			testCase.prepare(t, options, &imageRecipe)
			bootstrapper := newFakeFedoraBootstrapper()
			result, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), imageRecipe, options)
			if !errors.Is(err, testCase.wantError) {
				t.Fatalf("Build error = %v, want %v", err, testCase.wantError)
			}
			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Errorf("error = %v, want it to say %q", err, testCase.wantText)
			}
			if bootstrapper.ran || result.WorkDir != "" {
				t.Errorf("ran = %v, Result = %+v; want nothing done", bootstrapper.ran, result)
			}
		})
	}
}

// TestBuildFedoraOfflineFailsWhenTheRebuildDiffers: the image, or the tools
// tree that made it, holds another package than the lock, or took one from
// another repository; the build fails and places nothing.
func TestBuildFedoraOfflineFailsWhenTheRebuildDiffers(t *testing.T) {
	testCases := map[string]struct {
		change   func(*fakeFedoraBootstrapper)
		wantText string
	}{
		"another version in the image": {
			change:   func(f *fakeFedoraBootstrapper) { f.image[0].nevra = "git-0:2.55.1-1.fc44.x86_64" },
			wantText: "in the image but not in the lock: git 2.55.1-1.fc44 x86_64",
		},
		"a package from another repository": {
			change:   func(f *fakeFedoraBootstrapper) { f.image[0].repository = "fedora" },
			wantText: "installed from another repository: git 2.55.0-1.fc44 x86_64 from fedora, locked from updates",
		},
		"another tools tree": {
			change:   func(f *fakeFedoraBootstrapper) { f.tools[0].nevra = "dnf5-0:5.4.7.0-1.fc44.x86_64" },
			wantText: "in the lock but not in the tools tree: dnf5 5.4.6.0-1.fc44 x86_64",
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			options, bootstrapper, lockBytes := newFedoraOfflineFixture(t)
			testCase.change(bootstrapper)
			bootstrapper.tarball = "rebuilt tarball"
			result, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options)
			if !errors.Is(err, ErrImageDiffersFromLock) {
				t.Fatalf("Build error = %v, want ErrImageDiffersFromLock", err)
			}
			if !strings.Contains(err.Error(), testCase.wantText) {
				t.Errorf("error = %v, want it to say %q", err, testCase.wantText)
			}
			if result.WorkDir == "" {
				t.Error("a failed build keeps its work directory")
			}
			if after, err := os.ReadFile(filepath.Join(options.RecipeDir, LockFileName)); err != nil || !bytes.Equal(after, lockBytes) {
				t.Errorf("a failed offline build changed the lock: %v", err)
			}
			if content, err := os.ReadFile(filepath.Join(options.RecipeDir, "dist", "fedora-lab-fedora-44-amd64.tar.gz")); err != nil || string(content) != "fedora tarball" {
				t.Errorf("dist's tarball = %q, %v; want the previous one, untouched", content, err)
			}
		})
	}
}

// TestStagedRepositoriesAreReadableWhateverTheUmask: an unprivileged mkosi
// reads the local repositories as a subordinate uid, to which the user's
// files are anyone's.
func TestStagedRepositoriesAreReadableWhateverTheUmask(t *testing.T) {
	options, bootstrapper, _ := newFedoraOfflineFixture(t)
	options.KeepWork = true
	previous := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(previous) })
	if _, err := (&Builder{Fedora: bootstrapper}).Build(context.Background(), sampleFedoraRecipe(), options); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(bootstrapper.runSpec.LocalMirror, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		want := fs.FileMode(0o444)
		if entry.IsDir() {
			want = 0o555
		}
		if info.Mode().Perm()&want != want {
			t.Errorf("%s has mode %v, which not everyone can read", path, info.Mode().Perm())
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
