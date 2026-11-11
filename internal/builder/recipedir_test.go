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
	recipeDir := t.TempDir()
	link := filepath.Join(t.TempDir(), "recipe")
	if err := os.Symlink(recipeDir, link); err != nil {
		t.Skipf("cannot make a symbolic link here: %v", err)
	}
	hold, err := HoldRecipeDir(getenv, recipeDir, "vendor")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := HoldRecipeDir(getenv, link, "build"); !errors.Is(err, ErrRecipeDirBusy) || !strings.Contains(err.Error(), "frostroot vendor") {
		t.Errorf("HoldRecipeDir through a link while held = %v, want ErrRecipeDirBusy naming vendor", err)
	}
	if other, err := HoldRecipeDir(getenv, t.TempDir(), "build"); err != nil {
		t.Errorf("another directory: %v", err)
	} else {
		other.Release()
	}
	hold.Release()
	again, err := HoldRecipeDir(getenv, link, "build")
	if err != nil {
		t.Fatalf("after Release: %v", err)
	}
	again.Release()
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
