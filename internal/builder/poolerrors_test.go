package builder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// noProgress drops every event.
var noProgress = ProgressFunc(func(ProgressEvent) {})

// poolErrorPlan returns an offline plan of one package for family, whose
// pool directory is poolDir. The file itself is left to the test.
func poolErrorPlan(family, poolDir string) *offlinePlan {
	content := []byte("a package")
	digest := sha256.Sum256(content)
	urlPath := "pool/main/c/curl/curl_1_amd64.deb"
	locked := recipe.LockPackage{Name: "curl", Version: "1", Size: int64(len(content)), SHA256: hex.EncodeToString(digest[:])}
	if family == "fedora" {
		urlPath, locked.Source = "Packages/c/curl-1-1.fc44.x86_64.rpm", "updates"
	}
	locked.Filename = urlPath
	return &offlinePlan{
		lock:    recipe.Lockfile{Version: 1, Distro: family, Suite: "noble", Arch: "amd64", Packages: []recipe.LockPackage{locked}},
		poolDir: poolDir,
		entries: []pool.Entry{{Package: "curl", Version: "1", FileName: path.Base(urlPath), URLPath: urlPath, Size: locked.Size, SHA256: locked.SHA256}},
	}
}

// TestVerifyPoolThatCannotBeReadIsTheUsers: a pool the check could not read
// exited 2, as a failed build does, though the person can fix it. A pool
// directory that is a file stands in for a file left readable by root only,
// which a test cannot make as root; on Windows looking a file up in it does
// not fail.
func TestVerifyPoolThatCannotBeReadIsTheUsers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("a pool directory that is a file reads as an empty pool on Windows")
	}
	notADirectory := filepath.Join(t.TempDir(), "debs")
	if err := os.WriteFile(notADirectory, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	offline := poolErrorPlan("ubuntu", notADirectory)
	if err := verifyPool(context.Background(), offline, noProgress); !errors.Is(err, pool.ErrPoolRead) {
		t.Errorf("verifyPool error = %v, want pool.ErrPoolRead", err)
	}
	// Ctrl-C stays an interruption.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := verifyPool(ctx, offline, noProgress); !errors.Is(err, context.Canceled) || errors.Is(err, pool.ErrPoolRead) {
		t.Errorf("verifyPool of a canceled build = %v, want context.Canceled alone", err)
	}
}

// TestStagingAPoolFileGoneOrUnreadable: a pool file removed, or made
// unreadable, between the check and the staging exited 2. A file gone is
// the incomplete pool vendor fixes; one there but unreadable says so.
func TestStagingAPoolFileGoneOrUnreadable(t *testing.T) {
	stagers := map[string]struct {
		family string
		stage  func(offline *offlinePlan, workDir string) error
	}{
		"ubuntu": {"ubuntu", func(offline *offlinePlan, workDir string) error {
			return stageRepository(offline, workDir, noProgress)
		}},
		"fedora": {"fedora", func(offline *offlinePlan, workDir string) error {
			release := distro.FedoraRelease{Version: "44", Repositories: []distro.FedoraRepository{{ID: "updates"}}}
			_, _, _, err := stageFedoraRepositories(offline, release, workDir, noProgress)
			return err
		}},
	}
	for name, stager := range stagers {
		t.Run(name+", gone", func(t *testing.T) {
			offline := poolErrorPlan(stager.family, t.TempDir())
			err := stager.stage(offline, t.TempDir())
			if !errors.Is(err, ErrPoolIncomplete) || !strings.Contains(err.Error(), "was removed while the build ran") {
				t.Errorf("staging error = %v, want ErrPoolIncomplete saying the file was removed", err)
			}
		})
		// A directory where the file belongs cannot be read as one, on
		// Linux and on Windows alike.
		t.Run(name+", unreadable", func(t *testing.T) {
			offline := poolErrorPlan(stager.family, t.TempDir())
			if err := os.Mkdir(filepath.Join(offline.poolDir, offline.entries[0].FileName), 0o755); err != nil {
				t.Fatal(err)
			}
			err := stager.stage(offline, t.TempDir())
			if !errors.Is(err, pool.ErrPoolRead) || errors.Is(err, ErrPoolIncomplete) {
				t.Errorf("staging error = %v, want pool.ErrPoolRead", err)
			}
		})
	}
}
