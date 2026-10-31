package builder

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

// newFakeDiskHost returns a host with every program a disk build runs.
func newFakeDiskHost() *fakeHost {
	installed := map[string]bool{}
	for _, tool := range slices.Concat(diskHostTools, unprivilegedDiskHostTools) {
		installed[tool.program] = true
	}
	return &fakeHost{installed: installed, results: map[string]fakeResult{}}
}

func (h *fakeHost) bubblewrap(uid int) *Bubblewrap {
	return &Bubblewrap{RunCommand: h.run, LookPath: h.lookPath, CurrentUID: func() int { return uid }}
}

// TestBubblewrapCommandLine: the tree's /usr and /etc read-only, the work
// directory at /work, an environment of frostroot's alone, and the
// program's arguments after "--", every path an argument of its own
// however hostile.
func TestBubblewrapCommandLine(t *testing.T) {
	run := ToolRun{
		TreeDir:         "/var/tmp/frostroot 1000/build-1/disk-tools/tree",
		WorkDir:         "/var/tmp/frostroot 1000/build-1/it's --here",
		Tool:            "mke2fs",
		Args:            []string{"-d", "image.tar", "-E", "hash_seed=1 2", "--unshare-all"},
		SourceDateEpoch: 1758067200,
	}
	args := bubblewrapCommand(run)
	joined := strings.Join(args, "\x00")
	for _, want := range [][]string{
		{"--unshare-all"}, {"--die-with-parent"}, {"--new-session"},
		{"--ro-bind", run.TreeDir + "/usr", "/usr"},
		{"--ro-bind", run.TreeDir + "/etc", "/etc"},
		{"--symlink", "usr/bin", "/bin"}, {"--symlink", "usr/lib64", "/lib64"},
		{"--proc", "/proc"}, {"--dev", "/dev"}, {"--tmpfs", "/tmp"},
		{"--bind", run.WorkDir, "/work"},
		{"--chdir", "/work"},
		{"--clearenv"},
		{"--setenv", "PATH", "/usr/sbin:/usr/bin:/sbin:/bin"},
		{"--setenv", "LC_ALL", "C.UTF-8"},
		{"--setenv", "TZ", "UTC"},
		{"--setenv", "MTOOLS_SKIP_CHECK", "1"},
		{"--setenv", "SOURCE_DATE_EPOCH", "1758067200"},
		{"--", "mke2fs", "-d", "image.tar", "-E", "hash_seed=1 2", "--unshare-all"},
	} {
		if !strings.Contains("\x00"+joined+"\x00", "\x00"+strings.Join(want, "\x00")+"\x00") {
			t.Errorf("bwrap arguments lack %q:\n%q", want, args)
		}
	}
	// The program's own --unshare-all is its argument, not bwrap's: bwrap
	// reads nothing after "--".
	if separator := slices.Index(args, "--"); separator < 0 || args[separator+1] != "mke2fs" || slices.Index(args[:separator], "--clearenv") < 0 {
		t.Errorf("the program must follow \"--\", after bwrap's own options: %q", args)
	}
	if clearenv, setenv := slices.Index(args, "--clearenv"), slices.Index(args, "--setenv"); clearenv > setenv {
		t.Errorf("--clearenv must come before the first --setenv: %q", args)
	}
	var variables []string
	for index, arg := range args {
		if arg == "--setenv" {
			variables = append(variables, args[index+1])
		}
	}
	if want := []string{"PATH", "LC_ALL", "TZ", "MTOOLS_SKIP_CHECK", "SOURCE_DATE_EPOCH"}; !slices.Equal(variables, want) {
		t.Errorf("the environment is %q, want exactly %q", variables, want)
	}
	// Without an instant there is no SOURCE_DATE_EPOCH at all, rather than
	// a zero that every tool would read as 1970.
	run.SourceDateEpoch = 0
	if args := bubblewrapCommand(run); slices.Contains(args, "SOURCE_DATE_EPOCH") {
		t.Errorf("SOURCE_DATE_EPOCH set without an instant: %q", args)
	}
}

func TestBubblewrapRunsTheProgramAndRepeatsItsLastLineOnFailure(t *testing.T) {
	host := newFakeDiskHost()
	host.results["bwrap"] = fakeResult{stdout: "mke2fs 1.47.2 (1-Jan-2025)\n"}
	var stdout, stderr bytes.Buffer
	run := ToolRun{TreeDir: "/tree", WorkDir: "/work dir", Tool: "mke2fs", Args: []string{"-V"}, Stdout: &stdout, Stderr: &stderr}
	if err := host.bubblewrap(1000).Run(context.Background(), run); err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
	if want := "bwrap " + strings.Join(bubblewrapCommand(run), " "); !slices.Equal(host.ran, []string{want}) {
		t.Errorf("ran %q, want %q", host.ran, want)
	}
	if stdout.String() != "mke2fs 1.47.2 (1-Jan-2025)\n" {
		t.Errorf("stdout = %q, want the program's", stdout.String())
	}

	host.results["bwrap"] = fakeResult{stderr: "mke2fs: Pathname can't be converted\nmke2fs: aborting\n", err: errors.New("exit status 1")}
	err := host.bubblewrap(1000).Run(context.Background(), ToolRun{TreeDir: "/tree", WorkDir: "/work", Tool: "mke2fs"})
	if err == nil || !strings.Contains(err.Error(), "mke2fs: exit status 1: mke2fs: aborting") {
		t.Errorf("Run = %v, want the program, the exit status and its last line", err)
	}
	// Discarded output is still read for the error.
	if err := host.bubblewrap(1000).Run(context.Background(), ToolRun{TreeDir: "/tree", WorkDir: "/work", Tool: "mke2fs", Stdout: io.Discard}); err == nil {
		t.Error("Run = nil, want the failure")
	}
}

// TestBubblewrapPreflightNamesEveryMissingProgram: one error that says all
// of it, and the one apt line that fixes it. Root maps no subordinate ids,
// so it is not asked for unshare's helpers.
func TestBubblewrapPreflightNamesEveryMissingProgram(t *testing.T) {
	host := newFakeDiskHost()
	for _, program := range []string{"bwrap", "unshare", "newuidmap", "newgidmap"} {
		delete(host.installed, program)
	}
	err := host.bubblewrap(1000).Preflight()
	if !errors.Is(err, ErrNoDiskTool) {
		t.Fatalf("Preflight = %v, want ErrNoDiskTool", err)
	}
	if want := "bwrap, unshare, newuidmap, newgidmap; install with: sudo apt install bubblewrap util-linux uidmap"; !strings.Contains(err.Error(), want) {
		t.Errorf("Preflight = %v, want it to end %q", err, want)
	}
	err = host.bubblewrap(0).Preflight()
	if !errors.Is(err, ErrNoDiskTool) || !strings.HasSuffix(err.Error(), "bwrap; install with: sudo apt install bubblewrap") {
		t.Errorf("Preflight as root = %v, want only bwrap named", err)
	}
	host.installed["bwrap"] = true
	if err := host.bubblewrap(0).Preflight(); err != nil {
		t.Errorf("Preflight as root with bwrap = %v, want nil", err)
	}
	if err := host.bubblewrap(1000).Preflight(); !errors.Is(err, ErrNoDiskTool) {
		t.Errorf("Preflight unprivileged without unshare's helpers = %v, want ErrNoDiskTool", err)
	}
	if err := newFakeDiskHost().bubblewrap(1000).Preflight(); err != nil {
		t.Errorf("Preflight on a ready host = %v, want nil", err)
	}
	if len(host.ran) != 0 {
		t.Errorf("Preflight ran %q; it only looks programs up", host.ran)
	}
}

// TestBubblewrapRemovesTheTreeInTheNamespaceThatMadeIt: unprivileged, the
// tree's files belong to the subordinate ids mmdebstrap mapped, and rm
// runs in a namespace that maps them again; root's rm needs none.
func TestBubblewrapRemovesTheTreeInTheNamespaceThatMadeIt(t *testing.T) {
	tree := "/var/tmp/frostroot-1000/build-1/disk tools/tree"
	for _, testCase := range []struct {
		uid  int
		want string
	}{
		{0, "rm -rf -- " + tree},
		{1000, "unshare --map-root-user --map-auto -- rm -rf -- " + tree},
	} {
		host := newFakeDiskHost()
		if err := host.bubblewrap(testCase.uid).RemoveTree(context.Background(), tree); err != nil {
			t.Errorf("uid %d: RemoveTree = %v, want nil", testCase.uid, err)
		}
		if !slices.Equal(host.ran, []string{testCase.want}) {
			t.Errorf("uid %d ran %q, want %q", testCase.uid, host.ran, testCase.want)
		}
	}

	host := newFakeDiskHost()
	host.results["rm"] = fakeResult{stderr: "rm: cannot remove 'tree/usr': Permission denied\n", err: errors.New("exit status 1")}
	err := host.bubblewrap(0).RemoveTree(context.Background(), tree)
	if err == nil || !strings.Contains(err.Error(), "removing "+tree+": exit status 1: rm: cannot remove") {
		t.Errorf("RemoveTree = %v, want rm's own words", err)
	}

	// Nothing frostroot did not make under a work directory: a relative
	// path or the root is a bug, and no rm runs for it.
	for _, bad := range []string{"", "tree", "/", "//", "/../"} {
		host := newFakeDiskHost()
		if err := host.bubblewrap(0).RemoveTree(context.Background(), bad); err == nil || len(host.ran) != 0 {
			t.Errorf("RemoveTree(%q) = %v and ran %q, want a refusal and no command", bad, err, host.ran)
		}
	}
}
