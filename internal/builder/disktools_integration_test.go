//go:build integration

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
	"time"

	"frostroot/internal/pool"
	"frostroot/internal/recipe"
)

// skipUnlessDiskToolsAvailable skips the test on hosts that cannot make
// the disk tools tree or run what is in it.
func skipUnlessDiskToolsAvailable(t *testing.T) {
	t.Helper()
	skipUnlessMmdebstrapAvailable(t)
	if err := (&Bubblewrap{}).Preflight(); err != nil {
		t.Skipf("bubblewrap not installed: %v", err)
	}
}

// TestIntegrationDiskToolsTree makes the disk tools tree for real with
// mmdebstrap, runs each of its tools through bubblewrap, vendors the tree's
// packages from the archive, makes a tree from the pool alone, and checks
// that it holds the same packages. It is the only automated check that the
// tree a disk is made with can be made on a host, run as any user, and
// rebuilt without the archive.
//
// Needs what TestIntegrationNobleTiny needs, plus bubblewrap, and
// unprivileged, uidmap. Takes a minute or two and about 330 MB of
// downloads, the tree's packages once through mmdebstrap and once into the
// pool.
func TestIntegrationDiskToolsTree(t *testing.T) {
	skipUnlessDiskToolsAvailable(t)
	ctx := context.Background()
	workRoot := newIntegrationWorkRoot(t)
	epoch := time.Now().Unix()
	runner := &Bubblewrap{}
	progress := &testLogProgress{t: t}

	t.Log("the tree, from the archive")
	onlineDir := filepath.Join(workRoot, "online")
	if err := os.Mkdir(onlineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tree, err := newDiskToolsTree(&Mmdebstrap{}, runner, Options{}, nil, epoch, nil, progress)
	if err != nil {
		t.Fatal(err)
	}
	if err := tree.make(ctx, onlineDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tree.remove(ctx) })
	if !slices.Equal(progress.started, []Phase{PhaseMakeToolsTree}) {
		t.Errorf("phases started = %v, want the tools tree phase alone", progress.started)
	}
	if _, err := os.Stat(filepath.Join(onlineDir, diskToolsDirName, LogFileName)); err != nil {
		t.Errorf("the tree's build left no log of its own: %v", err)
	}

	t.Run("packages", func(t *testing.T) {
		if len(tree.packages) < 50 {
			t.Errorf("the tree holds %d packages; the essential set with the tools is a hundred odd", len(tree.packages))
		}
		versions := map[string]string{}
		for _, locked := range tree.packages {
			versions[locked.Name] = locked.Version
			if len(locked.SHA256) != 64 || locked.Size <= 0 || !strings.HasPrefix(locked.Filename, "pool/") || locked.Source != "" {
				t.Errorf("odd lock entry %+v", locked)
			}
		}
		for _, name := range DiskToolsPackages {
			if versions[name] == "" {
				t.Errorf("the tree has no %s", name)
			}
		}
		// What the tree is for: 24.04's e2fsprogs is 1.47.0, which copies
		// the host's timestamps into the filesystem.
		if version := versions["e2fsprogs"]; !strings.HasPrefix(version, "1.47.") || strings.HasPrefix(version, "1.47.0") {
			t.Errorf("e2fsprogs is %s, want 1.47.1 or newer", version)
		}
	})

	t.Run("tools", func(t *testing.T) {
		run := func(tool string, args ...string) (string, error) {
			t.Helper()
			var output bytes.Buffer
			err := runner.Run(ctx, ToolRun{TreeDir: tree.dir, WorkDir: onlineDir, Tool: tool, Args: args, SourceDateEpoch: epoch, Stdout: &output, Stderr: &output})
			t.Logf("%s %s: %s", tool, strings.Join(args, " "), strings.TrimSpace(output.String()))
			return output.String(), err
		}
		for _, testCase := range []struct {
			tool string
			args []string
			want string
		}{
			{"mke2fs", []string{"-V"}, "mke2fs 1.47."},
			{"mcopy", []string{"-V"}, "GNU mtools"},
			{"sfdisk", []string{"--version"}, "util-linux"},
			{"qemu-img", []string{"--version"}, "qemu-img version"},
			{"tar", []string{"--version"}, "tar"},
			{"dd", []string{"--version"}, "dd"},
		} {
			if output, err := run(testCase.tool, testCase.args...); err != nil || !strings.Contains(output, testCase.want) {
				t.Errorf("%s %s = %v, %q; want %q", testCase.tool, strings.Join(testCase.args, " "), err, output, testCase.want)
			}
		}
		// A file written under /work lands in the work directory, which is
		// how a disk will be assembled; the FAT is mkfs.fat's answer.
		if _, err := run("truncate", "-s", "2M", "esp.img"); err != nil {
			t.Fatal(err)
		}
		if output, err := run("mkfs.fat", "--invariant", "esp.img"); err != nil || !strings.Contains(output, "mkfs.fat 4.") {
			t.Errorf("mkfs.fat = %v, %q; want its version line", err, output)
		}
		if info, err := os.Stat(filepath.Join(onlineDir, "esp.img")); err != nil || info.Size() != 2<<20 {
			t.Errorf("esp.img in the work directory: %v, %v; want the 2 MiB file the tools wrote", info, err)
		}
		// Nothing of the host's environment reaches a tool: frostroot's
		// five variables, and the PWD bwrap sets for --chdir.
		output, err := run("env")
		if err != nil {
			t.Fatal(err)
		}
		var variables []string
		for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
			variables = append(variables, strings.SplitN(line, "=", 2)[0])
		}
		slices.Sort(variables)
		if want := []string{"LC_ALL", "MTOOLS_SKIP_CHECK", "PATH", "PWD", "SOURCE_DATE_EPOCH", "TZ"}; !slices.Equal(variables, want) {
			t.Errorf("a tool's environment is %q, want exactly %q", variables, want)
		}
	})

	t.Log("vendor the tree's packages")
	// A lock of the tree alone: Manifest wants an image, so the tree stands
	// in for one, and names each file once.
	lock := recipe.Lockfile{
		Version: 1, Distro: "ubuntu", Release: "24.04", Suite: "noble", Arch: "amd64", Target: recipe.TargetDisk,
		Mirror: tree.archiveURL(), FrostrootVersion: Version, Requested: []string{}, SourceDateEpoch: epoch,
		Packages: tree.packages, DiskTools: tree.packages,
	}
	entries, err := pool.Manifest(lock)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(tree.packages) {
		t.Errorf("the manifest lists %d files for %d packages named twice; want each once", len(entries), len(tree.packages))
	}
	poolDir := filepath.Join(workRoot, "vendor", "debs")
	summary, err := pool.Fetch(ctx, pool.FetchOptions{Dir: poolDir, Entries: entries, Fallback: pool.FallbackURL(lock)})
	if err != nil {
		t.Fatal(err)
	}
	if summary.Fetched != len(entries) {
		t.Errorf("fetched %d of %d", summary.Fetched, len(entries))
	}

	t.Log("the tree, from the pool")
	offlineDir := filepath.Join(workRoot, "offline")
	if err := os.Mkdir(offlineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	offline := &offlinePlan{lock: lock, entries: entries, poolDir: poolDir}
	offlineTree, err := newDiskToolsTree(&Mmdebstrap{}, runner, Options{}, nil, epoch, offline, progress)
	if err != nil {
		t.Fatal(err)
	}
	// make compares the tree with the lock itself, and fails on a difference.
	if err := offlineTree.make(ctx, offlineDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = offlineTree.remove(ctx) })
	if !slices.EqualFunc(offlineTree.packages, tree.packages, func(a, b recipe.LockPackage) bool { return keyOf(a) == keyOf(b) }) {
		t.Errorf("the offline tree holds %d packages, the online one %d, or in another order", len(offlineTree.packages), len(tree.packages))
	}
	if output, err := func() (string, error) {
		var output bytes.Buffer
		err := runner.Run(ctx, ToolRun{TreeDir: offlineTree.dir, WorkDir: offlineDir, Tool: "mke2fs", Args: []string{"-V"}, Stdout: &output, Stderr: &output})
		return output.String(), err
	}(); err != nil || !strings.Contains(output, "mke2fs 1.47.") {
		t.Errorf("mke2fs -V in the offline tree = %v, %q", err, output)
	}

	t.Log("remove both trees")
	for _, made := range []*diskToolsTree{tree, offlineTree} {
		dir := made.dir
		if err := made.remove(ctx); err != nil {
			t.Errorf("remove = %v", err)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there after remove (Stat error %v)", dir, err)
		}
		if made.dir != "" {
			t.Error("remove left the tree named, so it would be removed twice")
		}
	}
}
