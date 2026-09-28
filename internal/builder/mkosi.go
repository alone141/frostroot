package builder

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
)

// Errors for a host that cannot build a Fedora image, which the user can fix.
// Compare with errors.Is.
var (
	// ErrNoFedoraTool means a program a Fedora build runs on the host is
	// missing, or is installed and does not run.
	ErrNoFedoraTool = errors.New("this host lacks a program Fedora builds run")
	// ErrMkosiVersion means the installed mkosi is not a version frostroot
	// knows how to drive: its settings and command line change between
	// versions, and so do the bytes it writes.
	ErrMkosiVersion = errors.New("mkosi version frostroot does not know")
)

// KnownMkosiVersions are the mkosi versions frostroot drives, oldest first:
// Ubuntu 24.04's, which the Fedora spike measured.
var KnownMkosiVersions = []string{"20.2"}

// hostTool is a program a build runs on the host, and the Ubuntu package
// that installs it.
type hostTool struct {
	program, aptPackage string
}

// fedoraHostTools are what a Fedora build runs on the host, whoever runs it.
var fedoraHostTools = []hostTool{
	{"mkosi", "mkosi"},
	// dnf makes the tools tree, which then makes the image with dnf5.
	{"dnf", "dnf"},
	// mkosi 20.2 runs rpm itself for any rpm-based image, and Ubuntu's dnf
	// does not pull it in.
	{"rpm", "rpm"},
	// Offline, the vendored packages need their index before any tools tree
	// exists to make it.
	{"createrepo_c", "createrepo-c"},
	{"bwrap", "bubblewrap"},
	// mkosi's own gzip dates what it writes; the tarball is compressed
	// afterwards with gzip -n.
	{"gzip", "gzip"},
}

// unprivilegedHostTools are what mkosi needs besides, run by anyone but root:
// the helpers that map the subordinate ids into its user namespace.
var unprivilegedHostTools = []hostTool{
	{"newuidmap", "uidmap"},
	{"newgidmap", "uidmap"},
}

// Mkosi builds Fedora images. mkosi makes the tools tree with the host's
// dnf, then the image with the tools tree's dnf5, and writes the tar from
// inside its user namespace, which is what keeps ownership, hardlinks and
// file capabilities correct.
type Mkosi struct {
	RunCommand CommandRunner                // defaults to runInterruptibly
	CurrentUID func() int                   // defaults to os.Getuid
	LookPath   func(string) (string, error) // defaults to exec.LookPath
}

// FedoraSpec is everything the Mkosi bootstrapper needs to build one image.
type FedoraSpec struct {
	WorkDir string // the build directory; during Preflight, the work root
}

// Preflight checks the host before any work is done: every program a
// Fedora build runs, named all at once with the line that installs them,
// the mkosi version, a dnf that runs, and a work root that mkosi's user
// namespace can enter. spec.WorkDir need not exist yet.
func (m *Mkosi) Preflight(ctx context.Context, spec FedoraSpec) error {
	lookPath := m.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	tools := fedoraHostTools
	if !m.privileged() {
		tools = slices.Concat(fedoraHostTools, unprivilegedHostTools)
	}
	var missing, aptPackages []string
	for _, tool := range tools {
		if _, err := lookPath(tool.program); err != nil {
			missing = append(missing, tool.program)
			if !slices.Contains(aptPackages, tool.aptPackage) {
				aptPackages = append(aptPackages, tool.aptPackage)
			}
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("%w: %s; install with: sudo apt install %s", ErrNoFedoraTool, strings.Join(missing, ", "), strings.Join(aptPackages, " "))
	}
	versionOutput, err := m.output(ctx, "mkosi", "--version")
	if err != nil {
		return fmt.Errorf("%w: mkosi --version: %w", ErrNoFedoraTool, err)
	}
	if version := mkosiVersion(versionOutput); !slices.Contains(KnownMkosiVersions, version) {
		return fmt.Errorf("%w: this host has mkosi %q, and frostroot builds Fedora with %s; Ubuntu 24.04's mkosi is 20.2",
			ErrMkosiVersion, version, strings.Join(KnownMkosiVersions, " or "))
	}
	// dnf is a Python program: installed is not the same as working.
	if _, err := m.output(ctx, "dnf", "--version"); err != nil {
		return fmt.Errorf("%w: dnf is installed but does not run: %w", ErrNoFedoraTool, err)
	}
	if !m.privileged() && spec.WorkDir != "" {
		return checkReachableFromUserNamespace("mkosi", spec.WorkDir)
	}
	return nil
}

// privileged reports whether mkosi runs as root, which needs no subordinate
// ids.
func (m *Mkosi) privileged() bool {
	currentUID := os.Getuid
	if m.CurrentUID != nil {
		currentUID = m.CurrentUID
	}
	return currentUID() == 0
}

// output runs a quick host program and returns what it printed; on failure
// the error carries the end of its standard error.
func (m *Mkosi) output(ctx context.Context, program string, args ...string) (string, error) {
	runCommand := m.RunCommand
	if runCommand == nil {
		runCommand = runInterruptibly
	}
	var stdout bytes.Buffer
	stderr := newTailBuffer(errorTailBytes)
	if err := runCommand(ctx, program, args, nil, &stdout, stderr); err != nil {
		if tail := strings.TrimSpace(stderr.String()); tail != "" {
			return "", fmt.Errorf("%w: %s", err, lastLine(tail))
		}
		return "", err
	}
	return stdout.String(), nil
}

// mkosiVersion returns the version in `mkosi --version`'s output, "mkosi
// 20.2", or the output itself, trimmed, when it is not of that form.
func mkosiVersion(output string) string {
	fields := strings.Fields(output)
	if len(fields) == 2 && fields[0] == "mkosi" {
		return fields[1]
	}
	return strings.TrimSpace(output)
}

// lastLine returns the last line of text.
func lastLine(text string) string {
	return text[strings.LastIndexByte(text, '\n')+1:]
}
