package cli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"

	"frostroot/internal/builder"
	"frostroot/internal/distro"
	"frostroot/internal/export"
)

// fetchTrouble are apt and mmdebstrap messages that mean the archive could not
// be reached, as opposed to, say, a package that does not exist.
var fetchTrouble = []string{
	"Failed to fetch", "Temporary failure resolving", "Could not resolve",
	"Could not connect", "Connection failed", "Connection timed out",
	"Unable to connect", "No route to host", "404  Not Found",
}

func (a *App) cmdBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	mirror := fs.String("mirror", "", "archive base `URL` to use for all three pockets instead of http://archive.ubuntu.com/ubuntu")
	keepWork := fs.Bool("keep-work", false, "keep the work directory after a successful build")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: frostroot build [--mirror URL] [--keep-work]\n\nBuild frostroot.lock and dist/<name>-ubuntu-<release>-amd64.tar.gz from frostroot.toml.\nNeeds Linux, mmdebstrap, network, and user namespaces or root. Never prompts.\n\n")
		fs.PrintDefaults()
	}
	if code, done := a.parseFlags(fs, args); done {
		return code
	}
	if *mirror != "" {
		if err := checkMirror(*mirror); err != nil {
			fmt.Fprintf(a.Stderr, "frostroot: %v\n", err)
			return 1
		}
	}
	if a.GOOS != "linux" {
		fmt.Fprintln(a.Stderr, "frostroot: build requires Linux; on Windows, run frostroot inside WSL")
		return 1
	}
	r, ok := a.loadRecipe()
	if !ok {
		return 1
	}
	info, err := distro.Lookup(r.Image.Release, r.Image.Arch)
	if err != nil { // unreachable after validation
		fmt.Fprintf(a.Stderr, "frostroot: %v\n", err)
		return 1
	}
	base := info.Base
	if *mirror != "" {
		base = *mirror
	}
	if info.EOL {
		fmt.Fprintf(a.Stderr, "warning: Ubuntu %s is past the end of standard support. The image will contain packages\n"+
			"with known, unfixed security vulnerabilities: security fixes are only published to Ubuntu Pro.\n"+
			"Prefer 22.04 or 24.04 unless you specifically need %s.\n", r.Image.Release, r.Image.Release)
	}
	fmt.Fprintf(a.Stderr, "frostroot: building %s from Ubuntu %s (%s, %s) using %s\n",
		r.Image.Name, r.Image.Release, info.Suite, r.Image.Arch, base)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var finished atomic.Bool
	go func() {
		<-ctx.Done()
		if finished.Load() {
			return
		}
		stop() // restore default handling: a second Ctrl-C ends frostroot at once
		fmt.Fprintln(a.Stderr, "\nfrostroot: interrupted; waiting for mmdebstrap to clean up (Ctrl-C again to abort)")
	}()
	res, err := a.Builder.Build(ctx, r, builder.Options{
		Dir:      a.Dir,
		Mirror:   *mirror,
		KeepWork: *keepWork,
		GOOS:     a.GOOS,
		Getenv:   a.Getenv,
	})
	interrupted := ctx.Err() != nil
	finished.Store(true)

	if err != nil {
		switch {
		case errors.Is(err, context.Canceled) || interrupted:
			fmt.Fprintln(a.Stderr, "frostroot: build interrupted; no lock or tarball was written")
			a.printKept(res.WorkDir)
			return 130
		case errors.Is(err, builder.ErrNotLinux), errors.Is(err, builder.ErrNoMmdebstrap),
			errors.Is(err, builder.ErrNoKeyring), errors.Is(err, builder.ErrBadWorkRoot):
			fmt.Fprintf(a.Stderr, "frostroot: %v\n", err)
			return 1
		default:
			fmt.Fprintf(a.Stderr, "frostroot: build failed: %v\n", err)
			if mentionsAny(err.Error(), fetchTrouble) {
				fmt.Fprintf(a.Stderr, "frostroot: could not fetch from %s; check the network, or retry with --mirror URL\n", base)
			}
			a.printKept(res.WorkDir)
			return 2
		}
	}

	rel := filepath.ToSlash(export.TarballRelPath(r.Image.Name, r.Image.Release, r.Image.Arch))
	size := ""
	if fi, err := os.Stat(res.TarballPath); err == nil {
		size = fmt.Sprintf(" (%d MB)", (fi.Size()+1<<19)>>20)
	}
	fmt.Fprintf(a.Stdout, "\nWrote %s%s\nWrote frostroot.lock (%d packages)\n", rel, size, res.Packages)
	fmt.Fprintf(a.Stdout, "\nImport it on Windows:\n  wsl --import %s <install-dir> %s\n", r.Image.Name, rel)
	if win, err := a.WSLPath(res.TarballPath); err == nil && win != "" {
		fmt.Fprintf(a.Stdout, "or from any directory in PowerShell:\n  wsl --import %s <install-dir> '%s'\n",
			r.Image.Name, strings.ReplaceAll(win, "'", "''"))
	}
	if res.CleanupErr != nil {
		fmt.Fprintf(a.Stderr, "warning: could not remove the work directory: %v\n", res.CleanupErr)
	}
	if res.WorkDir != "" {
		a.printKept(res.WorkDir)
	}
	return 0
}

func (a *App) printKept(workDir string) {
	if workDir != "" {
		fmt.Fprintf(a.Stderr, "frostroot: work directory kept at %s\n", workDir)
	}
}

// checkMirror accepts http and https URLs with a host. The URL becomes part of
// apt source lines, so whitespace would split it.
func checkMirror(s string) error {
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(s, " \t\r\n") {
		return fmt.Errorf("--mirror %q: expected an http or https URL such as http://mirror.example.com/ubuntu", s)
	}
	return nil
}

func mentionsAny(s string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(s, n) {
			return true
		}
	}
	return false
}

// wslpath returns the Windows form of a Linux path when running under WSL.
func wslpath(p string) (string, error) {
	if _, err := exec.LookPath("wslpath"); err != nil {
		return "", err
	}
	out, err := exec.Command("wslpath", "-w", p).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
