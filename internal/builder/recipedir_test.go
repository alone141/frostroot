package builder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// heldBootstrapper is a fakeBootstrapper whose image bootstrap says that it
// started, then waits for release.
type heldBootstrapper struct {
	fakeBootstrapper
	started chan struct{}
	release chan struct{}
}

func (h *heldBootstrapper) Run(ctx context.Context, spec BootstrapSpec) error {
	if spec.TreeDir == "" {
		close(h.started)
		<-h.release
	}
	return h.fakeBootstrapper.Run(ctx, spec)
}

func TestBuildRefusesASecondRunInTheSameRecipeDir(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	options, _ := newTestOptions(t)
	first := &heldBootstrapper{started: make(chan struct{}), release: make(chan struct{})}
	firstDone := make(chan error, 1)
	go func() {
		_, err := buildWith(first, options)
		firstDone <- err
	}()
	select {
	case <-first.started:
	case err := <-firstDone:
		t.Fatalf("the first build ended before its bootstrap: %v", err)
	}

	second := &fakeBootstrapper{}
	_, secondErr := buildWith(second, options)
	close(first.release)
	if err := <-firstDone; err != nil {
		t.Fatalf("the first build failed: %v", err)
	}
	if !errors.Is(secondErr, ErrRecipeDirBusy) {
		t.Fatalf("second Build error = %v, want ErrRecipeDirBusy", secondErr)
	}
	if holder := fmt.Sprintf("frostroot build, process %d", os.Getpid()); !strings.Contains(secondErr.Error(), holder) {
		t.Errorf("error %q does not name the run holding the directory, %q", secondErr, holder)
	}
	if second.runCount != 0 {
		t.Error("the second build bootstrapped")
	}
	if _, err := os.Stat(expectedTarballPath(options)); err != nil {
		t.Errorf("the first build's tarball: %v", err)
	}
	assertNoTemporaryFiles(t, options.RecipeDir)

	// Its hold went with it.
	if _, err := buildWith(&fakeBootstrapper{}, options); err != nil {
		t.Errorf("a build after the first: %v", err)
	}
}

func TestHoldRecipeDirSharesTheHoldAcrossPathsToOneDirectory(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	cacheHome := t.TempDir()
	getenv := fakeEnvironment(map[string]string{"XDG_CACHE_HOME": cacheHome})
	holdRoot := t.TempDir()
	recipeDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "recipe")
	if err := os.Symlink(recipeDir, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	hold, err := HoldRecipeDir(getenv, holdRoot, recipeDir, "vendor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := HoldRecipeDir(getenv, holdRoot, link, "build"); !errors.Is(err, ErrRecipeDirBusy) || !strings.Contains(err.Error(), "frostroot vendor") {
		t.Errorf("HoldRecipeDir through a link while held = %v, want ErrRecipeDirBusy naming vendor", err)
	}
	if other, err := HoldRecipeDir(getenv, holdRoot, t.TempDir(), "build"); err != nil {
		t.Errorf("another directory: %v", err)
	} else {
		other.Release()
	}
	hold.Release()
	again, err := HoldRecipeDir(getenv, holdRoot, link, "build")
	if err != nil {
		t.Fatalf("after Release: %v", err)
	}
	again.Release()
}

// On a case-insensitive Windows drive two paths that differ in case are one
// directory, and only the lock named for its device and inode numbers is
// shared. A run that finds it busy lets go of the one named for its path.
func TestHoldRecipeDirRefusesWhenTheDirectoryIsHeldByAnotherPath(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	holdRoot := t.TempDir()
	recipeDir := t.TempDir()
	holdNames := holdFileNames(resolveRecipeDir(recipeDir))
	if len(holdNames) != 2 {
		t.Fatalf("holdFileNames(%s) = %q, want a path's and a device and inode's", recipeDir, holdNames)
	}
	holdDir := filepath.Join(holdRoot, "holds")
	if err := os.Mkdir(holdDir, 0o700); err != nil {
		t.Fatal(err)
	}
	identityPath := filepath.Join(holdDir, holdNames[1])
	other, held, err := lockExclusive(identityPath)
	if err != nil || !held {
		t.Fatalf("locking %s by hand: held %v, %v", identityPath, held, err)
	}
	if _, err := other.WriteString("frostroot build, process 1"); err != nil {
		t.Fatal(err)
	}

	_, err = holdRecipeDir(holdRoot, recipeDir, "vendor")
	if !errors.Is(err, ErrRecipeDirBusy) || !strings.Contains(err.Error(), "frostroot build, process 1") {
		t.Errorf("holdRecipeDir while the device and inode lock is held = %v, want ErrRecipeDirBusy naming its holder", err)
	}
	pathPath := filepath.Join(holdDir, holdNames[0])
	pathFile, held, err := lockExclusive(pathPath)
	if err != nil || !held {
		t.Errorf("the refused hold kept the path's lock: held %v, %v", held, err)
	} else {
		_ = pathFile.Close() // a lock only
	}

	_ = other.Close() // a lock only
	hold, err := holdRecipeDir(holdRoot, recipeDir, "vendor")
	if err != nil {
		t.Fatalf("holdRecipeDir once the hand lock is closed: %v", err)
	}
	hold.Release()
}

func TestRecipeDirHoldReleaseFreesEveryFile(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	holdRoot := t.TempDir()
	recipeDir := t.TempDir()
	hold, err := holdRecipeDir(holdRoot, recipeDir, "build")
	if err != nil {
		t.Fatal(err)
	}
	holdNames := holdFileNames(resolveRecipeDir(recipeDir))
	for _, holdName := range holdNames {
		holdPath := filepath.Join(holdRoot, "holds", holdName)
		if holder := readHolder(holdPath); !strings.Contains(holder, "frostroot build") {
			t.Errorf("hold file %s names %q, want the build", holdName, holder)
		}
		if _, held, err := lockExclusive(holdPath); err != nil || held {
			t.Errorf("hold file %s could be locked while held: held %v, %v", holdName, held, err)
		}
	}
	hold.Release()
	for _, holdName := range holdNames {
		file, held, err := lockExclusive(filepath.Join(holdRoot, "holds", holdName))
		if err != nil || !held {
			t.Errorf("hold file %s after Release: held %v, %v", holdName, held, err)
			continue
		}
		_ = file.Close() // a lock only
	}
}

// Two runs of one user started from different environments, a snap's
// terminal and a plain one, name different work roots and must still hold
// one file: otherwise each removes the other's staged tarball as a leftover.
func TestHoldRecipeDirIgnoresXDGCacheHome(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	holdRoot := t.TempDir()
	recipeDir := t.TempDir()
	hold, err := HoldRecipeDir(fakeEnvironment(map[string]string{"XDG_CACHE_HOME": t.TempDir()}), holdRoot, recipeDir, "vendor")
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	if _, err := HoldRecipeDir(fakeEnvironment(map[string]string{"XDG_CACHE_HOME": t.TempDir()}), holdRoot, recipeDir, "build"); !errors.Is(err, ErrRecipeDirBusy) {
		t.Errorf("HoldRecipeDir with another XDG_CACHE_HOME while held = %v, want ErrRecipeDirBusy", err)
	}
}

func TestBuildRefusesARunWithAnotherXDGCacheHome(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	options, _ := newTestOptions(t)
	hold, err := HoldRecipeDir(fakeEnvironment(map[string]string{"XDG_CACHE_HOME": t.TempDir()}), options.HoldRoot, options.RecipeDir, "vendor")
	if err != nil {
		t.Fatal(err)
	}
	defer hold.Release()
	bootstrapper := &fakeBootstrapper{}
	if _, err := buildWith(bootstrapper, options); !errors.Is(err, ErrRecipeDirBusy) {
		t.Fatalf("Build error = %v, want ErrRecipeDirBusy", err)
	}
	if bootstrapper.runCount != 0 {
		t.Error("a build in a held recipe directory bootstrapped")
	}
}

// A host whose /var/tmp cannot hold the directory still holds the recipe
// directory, under the work root XDG_CACHE_HOME names.
func TestHoldRecipeDirFallsBackToTheWorkRoot(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	holdRoot := filepath.Join(t.TempDir(), "frostroot-1000")
	if err := os.WriteFile(holdRoot, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	cacheHome := t.TempDir()
	getenv := fakeEnvironment(map[string]string{"XDG_CACHE_HOME": cacheHome})
	recipeDir := t.TempDir()
	hold, err := HoldRecipeDir(getenv, holdRoot, recipeDir, "vendor")
	if err != nil {
		t.Fatalf("HoldRecipeDir with the hold root unusable: %v", err)
	}
	defer hold.Release()
	holdFiles, err := filepath.Glob(filepath.Join(cacheHome, "frostroot", "holds", "*.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if len(holdFiles) == 0 {
		t.Error("no hold file under the work root")
	}
	if _, err := HoldRecipeDir(getenv, holdRoot, recipeDir, "build"); !errors.Is(err, ErrRecipeDirBusy) {
		t.Errorf("a second hold under the work root = %v, want ErrRecipeDirBusy", err)
	}
}

func TestHoldRecipeDirRefusesWhenNeitherRootIsUsable(t *testing.T) {
	if !recipeDirLocking {
		t.Skip("recipe directories are held on Linux only")
	}
	tests := []struct {
		name      string
		cacheHome func(t *testing.T, holdRoot string) string
	}{
		{
			name: "a work root that is not a directory",
			cacheHome: func(t *testing.T, _ string) string {
				t.Helper()
				cacheHome := t.TempDir()
				if err := os.WriteFile(filepath.Join(cacheHome, "frostroot"), nil, 0o600); err != nil {
					t.Fatal(err)
				}
				return cacheHome
			},
		},
		{
			name:      "a work root under /mnt",
			cacheHome: func(*testing.T, string) string { return "/mnt/c/cache" },
		},
		{
			name: "the work root is the hold root",
			cacheHome: func(t *testing.T, holdRoot string) string {
				t.Helper()
				return filepath.Dir(holdRoot)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			holdRoot := filepath.Join(t.TempDir(), "frostroot")
			if err := os.WriteFile(holdRoot, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			getenv := fakeEnvironment(map[string]string{"XDG_CACHE_HOME": test.cacheHome(t, holdRoot)})
			_, err := HoldRecipeDir(getenv, holdRoot, t.TempDir(), "vendor")
			if !errors.Is(err, ErrBadWorkRoot) {
				t.Fatalf("HoldRecipeDir = %v, want ErrBadWorkRoot", err)
			}
			if !strings.Contains(err.Error(), holdRoot) {
				t.Errorf("error %q does not name the hold root %s", err, holdRoot)
			}
			if strings.Contains(err.Error(), "refusing to build") {
				t.Errorf("error %q is about building, which vendor does not", err)
			}
		})
	}
}

// An offline build reads the lock before it holds the recipe directory, so
// that a recipe it cannot build is refused without touching the work root.
// A lock another build places in that window is not the one it read.
func TestBuildOfflineRefusesALockReplacedBeforeTheHold(t *testing.T) {
	options, workRoot := newTestOptions(t)
	writeVendoredLock(t, options)
	options.Offline = true
	options.Getenv = fakeEnvironment(map[string]string{"XDG_CACHE_HOME": filepath.Dir(workRoot)})
	lockPath := filepath.Join(options.RecipeDir, LockFileName)
	bootstrapper := &lockReplacingBootstrapper{lockPath: lockPath}
	_, err := (&Builder{Bootstrapper: bootstrapper}).Build(context.Background(), sampleRecipe(), options)
	if !errors.Is(err, ErrRecipeDirBusy) || !strings.Contains(err.Error(), "changed while this build was starting") {
		t.Fatalf("Build error = %v, want ErrRecipeDirBusy saying the lock changed", err)
	}
	if !bootstrapper.replaced {
		t.Fatal("the preflight did not run")
	}
	if bootstrapper.runCount != 0 {
		t.Error("a build from a lock that is no longer there bootstrapped")
	}
}

// lockReplacingBootstrapper puts a copy of the lock in its place, as another
// build's rename would, when the build preflights it: after the plan read
// the lock and before the build holds the recipe directory.
type lockReplacingBootstrapper struct {
	fakeBootstrapper
	lockPath string
	replaced bool
}

func (l *lockReplacingBootstrapper) Preflight(BootstrapSpec) error {
	content, err := os.ReadFile(l.lockPath)
	if err != nil {
		return err
	}
	temporaryPath := l.lockPath + ".new"
	if err := os.WriteFile(temporaryPath, content, 0o644); err != nil {
		return err
	}
	l.replaced = true
	return os.Rename(temporaryPath, l.lockPath)
}
