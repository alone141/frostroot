package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// cancelingProgress cancels the build when a phase starts, and records what
// it was told.
type cancelingProgress struct {
	recordingProgress
	phase  Phase
	cancel context.CancelFunc
}

func (c *cancelingProgress) Report(event ProgressEvent) {
	c.recordingProgress.Report(event)
	if event.Phase == c.phase && event.Kind == EventPhaseStarted {
		c.cancel()
	}
}

// TestBuildCanceledWhilePlacingWritesNothing: Ctrl-C during "Place tarball"
// used to be ignored. The build went on, wrote the lock and the tarball and
// exited 0, while the interface said it was cleaning up.
func TestBuildCanceledWhilePlacingWritesNothing(t *testing.T) {
	options, _ := newTestOptions(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options.Progress = &cancelingProgress{phase: PhasePlaceTarball, cancel: cancel}
	result, err := (&Builder{Bootstrapper: &fakeBootstrapper{}}).Build(ctx, sampleRecipe(), options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build error = %v, want context.Canceled", err)
	}
	assertNoBuildOutput(t, options)
	if result.WorkDir == "" {
		t.Fatal("a canceled build keeps its work directory")
	}
	// The image went back to the work directory, where it was built.
	if _, err := os.Stat(filepath.Join(result.WorkDir, "image.tar.gz")); err != nil {
		t.Errorf("the built tarball is not back in the work directory: %v", err)
	}
}

// TestBuildCanceledWhileCheckingThePoolStopsThere: the vendored files of an
// offline build were all hashed before a Ctrl-C was looked at.
func TestBuildCanceledWhileCheckingThePoolStopsThere(t *testing.T) {
	options, workRoot := newTestOptions(t)
	writeVendoredLock(t, options)
	options.Offline = true
	options.Getenv = fakeEnvironment(map[string]string{"XDG_CACHE_HOME": filepath.Dir(workRoot)})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	options.Progress = &cancelingProgress{phase: PhaseVerifyVendored, cancel: cancel}
	bootstrapper := &offlineFakeBootstrapper{}
	_, err := (&Builder{Bootstrapper: bootstrapper}).Build(ctx, sampleRecipe(), options)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Build error = %v, want context.Canceled", err)
	}
	if bootstrapper.runCount != 0 {
		t.Error("a build canceled while checking its pool bootstrapped")
	}
}

// TestBuildRemovesWhatAKilledBuildLeft: a build killed while it copied its
// tarball onto a Windows drive left gigabytes in dist/ under a temporary
// name, and nothing removed them. The next build in the directory does, and
// only those.
func TestBuildRemovesWhatAKilledBuildLeft(t *testing.T) {
	options, _ := newTestOptions(t)
	distDir := filepath.Join(options.RecipeDir, "dist")
	if err := os.Mkdir(distDir, 0o755); err != nil {
		t.Fatal(err)
	}
	leftovers := []string{
		filepath.Join(distDir, ".cpp-lab-ubuntu-22.04-amd64.tar.gz.3810987762.tmp"),
		filepath.Join(distDir, ".cpp-lab-ubuntu-22.04-amd64.img.42.tmp"),
		filepath.Join(options.RecipeDir, ".frostroot.lock.1234.tmp"),
	}
	others := []string{
		filepath.Join(distDir, ".other-lab-ubuntu-22.04-amd64.tar.gz.42.tmp"), // another recipe's name
		filepath.Join(distDir, ".cpp-lab-ubuntu-22.04-amd64.tar.gz.notes.tmp"),
		filepath.Join(distDir, "cpp-lab-ubuntu-22.04-amd64.tar.gz.42.tmp"),
		filepath.Join(options.RecipeDir, "frostroot.toml"),
	}
	for _, path := range append(append([]string(nil), leftovers...), others...) {
		if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// It fails, so that nothing it placed is confused with what it removed.
	_, err := buildWith(&fakeBootstrapper{runErr: errors.New("mmdebstrap failed")}, options)
	if err == nil {
		t.Fatal("Build succeeded with a failing bootstrapper")
	}
	for _, path := range leftovers {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there, Stat error = %v", path, err)
		}
	}
	for _, path := range others {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("%s was removed: %v", path, err)
		}
	}
}
