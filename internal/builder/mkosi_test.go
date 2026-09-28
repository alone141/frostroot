package builder

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeHost answers LookPath from a set of installed programs and runs
// programs from a table of canned results.
type fakeHost struct {
	installed map[string]bool
	results   map[string]fakeResult // by program name
	ran       []string
}

type fakeResult struct {
	stdout, stderr string
	err            error
}

func newFakeHost() *fakeHost {
	installed := map[string]bool{}
	for _, tool := range slices.Concat(fedoraHostTools, unprivilegedHostTools) {
		installed[tool.program] = true
	}
	return &fakeHost{
		installed: installed,
		results: map[string]fakeResult{
			"mkosi": {stdout: "mkosi 20.2\n"},
			"dnf":   {stdout: "4.14.0\n"},
		},
	}
}

func (h *fakeHost) lookPath(program string) (string, error) {
	if h.installed[program] {
		return "/usr/bin/" + program, nil
	}
	return "", exec.ErrNotFound
}

func (h *fakeHost) run(_ context.Context, program string, args, _ []string, stdout, stderr io.Writer) error {
	h.ran = append(h.ran, program+" "+strings.Join(args, " "))
	result := h.results[program]
	_, _ = io.WriteString(stdout, result.stdout)
	_, _ = io.WriteString(stderr, result.stderr)
	return result.err
}

func (h *fakeHost) mkosi(uid int) *Mkosi {
	return &Mkosi{RunCommand: h.run, LookPath: h.lookPath, CurrentUID: func() int { return uid }}
}

func TestMkosiPreflightAcceptsAReadyHost(t *testing.T) {
	// A work root not made yet, below a directory anyone may enter, as the
	// first build on a host has it.
	workRoot := filepath.Join(os.TempDir(), "frostroot-preflight-never-created", "frostroot")
	for _, uid := range []int{0, 1000} {
		host := newFakeHost()
		if err := host.mkosi(uid).Preflight(context.Background(), FedoraSpec{WorkDir: workRoot}); err != nil {
			t.Errorf("uid %d: Preflight = %v, want nil", uid, err)
		}
		if want := []string{"mkosi --version", "dnf --version"}; !slices.Equal(host.ran, want) {
			t.Errorf("uid %d ran %q, want %q", uid, host.ran, want)
		}
	}
}

// TestMkosiPreflightNamesEveryMissingProgram: one error that says all of
// it, and the one apt line that fixes it, rather than one program a run.
func TestMkosiPreflightNamesEveryMissingProgram(t *testing.T) {
	host := newFakeHost()
	for _, program := range []string{"mkosi", "createrepo_c", "newuidmap", "newgidmap"} {
		delete(host.installed, program)
	}
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{})
	if !errors.Is(err, ErrNoFedoraTool) {
		t.Fatalf("Preflight = %v, want ErrNoFedoraTool", err)
	}
	if want := "mkosi, createrepo_c, newuidmap, newgidmap; install with: sudo apt install mkosi createrepo-c uidmap"; !strings.Contains(err.Error(), want) {
		t.Errorf("Preflight = %v, want it to end %q", err, want)
	}
	if len(host.ran) != 0 {
		t.Errorf("ran %q before the missing programs were installed", host.ran)
	}
}

// TestMkosiPreflightAsRootNeedsNoIdMapping: root's mkosi maps no
// subordinate ids, so uidmap is not asked for.
func TestMkosiPreflightAsRootNeedsNoIdMapping(t *testing.T) {
	host := newFakeHost()
	delete(host.installed, "newuidmap")
	delete(host.installed, "newgidmap")
	if err := host.mkosi(0).Preflight(context.Background(), FedoraSpec{}); err != nil {
		t.Errorf("Preflight as root = %v, want nil", err)
	}
}

func TestMkosiPreflightRefusesAnUnknownMkosi(t *testing.T) {
	host := newFakeHost()
	host.results["mkosi"] = fakeResult{stdout: "mkosi 25.3\n"}
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{})
	if !errors.Is(err, ErrMkosiVersion) || !strings.Contains(err.Error(), `"25.3"`) || !strings.Contains(err.Error(), "20.2") {
		t.Errorf("Preflight = %v, want ErrMkosiVersion naming 25.3 and 20.2", err)
	}
}

// TestMkosiPreflightRefusesADnfThatDoesNotRun: dnf is Python, and a host
// whose python3 lacks the dnf module has the program but not the tool.
func TestMkosiPreflightRefusesADnfThatDoesNotRun(t *testing.T) {
	host := newFakeHost()
	host.results["dnf"] = fakeResult{
		stderr: "Traceback (most recent call last):\n  File \"/usr/bin/dnf\", line 61, in <module>\nModuleNotFoundError: No module named 'dnf'\n",
		err:    errors.New("exit status 1"),
	}
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{})
	if !errors.Is(err, ErrNoFedoraTool) || !strings.Contains(err.Error(), "No module named 'dnf'") {
		t.Errorf("Preflight = %v, want ErrNoFedoraTool with dnf's own last line", err)
	}
}

func TestMkosiPreflightRefusesAWorkRootTheNamespaceCannotEnter(t *testing.T) {
	unreachable := filepath.Join(t.TempDir(), "home")
	if err := os.Mkdir(unreachable, 0o750); err != nil {
		t.Fatal(err)
	}
	host := newFakeHost()
	err := host.mkosi(1000).Preflight(context.Background(), FedoraSpec{WorkDir: filepath.Join(unreachable, "frostroot")})
	if !errors.Is(err, ErrBadWorkRoot) || !strings.Contains(err.Error(), "mkosi runs in a user namespace") {
		t.Errorf("Preflight = %v, want ErrBadWorkRoot naming mkosi", err)
	}
	// Root's mkosi is not in a user namespace of subordinate ids.
	if err := host.mkosi(0).Preflight(context.Background(), FedoraSpec{WorkDir: filepath.Join(unreachable, "frostroot")}); err != nil {
		t.Errorf("Preflight as root = %v, want nil", err)
	}
}

func TestMkosiVersion(t *testing.T) {
	for output, want := range map[string]string{"mkosi 20.2\n": "20.2", "mkosi 25.3": "25.3", "something else\n": "something else"} {
		if got := mkosiVersion(output); got != want {
			t.Errorf("mkosiVersion(%q) = %q, want %q", output, got, want)
		}
	}
}
