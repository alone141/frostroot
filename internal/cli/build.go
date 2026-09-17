package cli

import (
	"context"
	"errors"
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
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
	"frostroot/internal/tui"
)

// archiveUnreachableMessages are apt and mmdebstrap messages meaning the
// archive could not be reached, as opposed to, say, a package that does not
// exist.
var archiveUnreachableMessages = []string{
	"Failed to fetch", "Temporary failure resolving", "Could not resolve",
	"Could not connect", "Connection failed", "Connection timed out",
	"Unable to connect", "No route to host", "404  Not Found",
}

const buildUsageText = `usage: frostroot build [--mirror URL | --offline] [--keep-work] [--plain]

Build frostroot.lock and dist/<name>-ubuntu-<release>-amd64.tar.gz from frostroot.toml.
Needs Linux, mmdebstrap, network, and user namespaces or root. Never prompts.

With --offline, rebuild the image from frostroot.lock and vendor/debs (see
frostroot vendor) without the archive: the same packages at the same versions,
verified against the lock. The lock is read, not written.

`

// progressEventBuffer is how many progress events may wait for the progress
// screen before the work has to pause for it.
const progressEventBuffer = 256

// vendorDebsDisplayName is how the offline source is shown.
const vendorDebsDisplayName = "vendor/debs"

func (a *App) runBuild(args []string) int {
	flags := a.newFlagSet("build", buildUsageText)
	mirrorURL := flags.String("mirror", "", "archive base `URL` to use for all three pockets instead of http://archive.ubuntu.com/ubuntu")
	offline := flags.Bool("offline", false, "rebuild from frostroot.lock and vendor/debs, without the archive")
	keepWork := flags.Bool("keep-work", false, "keep the work directory after a successful build")
	plain := flags.Bool("plain", false, "print progress as lines instead of showing the full-screen build screen")
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	if *mirrorURL != "" && *offline {
		a.stderrf("frostroot: --mirror and --offline exclude each other: an offline build installs from %s\n", vendorDebsDisplayName)
		return exitUserError
	}
	if *mirrorURL != "" {
		if err := validateMirrorURL(*mirrorURL); err != nil {
			a.stderrf("frostroot: %v\n", err)
			return exitUserError
		}
	}
	if a.GOOS != "linux" {
		a.stderrf("frostroot: build requires Linux; on Windows, run frostroot inside WSL\n")
		return exitUserError
	}
	imageRecipe, ok := a.loadRecipe()
	if !ok {
		return exitUserError
	}
	release, err := distro.Lookup(imageRecipe.Image.Release, imageRecipe.Image.Arch)
	if err != nil { // unreachable after validation, but never ignore an error
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	archiveURL := release.ArchiveURL
	if *mirrorURL != "" {
		archiveURL = *mirrorURL
	}
	if *offline {
		archiveURL = vendorDebsDisplayName
	}
	if release.EndOfLife {
		a.stderrf("warning: Ubuntu %s is past the end of standard support. The image will contain packages\n"+
			"with known, unfixed security vulnerabilities: security fixes are only published to Ubuntu Pro.\n"+
			"Prefer 22.04 or 24.04 unless you specifically need %s.\n", imageRecipe.Image.Release, imageRecipe.Image.Release)
	}

	// SIGHUP too: mmdebstrap runs in its own process group, so a closed
	// terminal no longer reaches it directly and frostroot must pass the
	// interrupt on instead of leaving an orphan bootstrapping for minutes.
	ctx, stopSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignalHandling()
	options := builder.Options{
		RecipeDir: a.RecipeDir,
		MirrorURL: *mirrorURL,
		KeepWork:  *keepWork,
		GOOS:      a.GOOS,
		Getenv:    a.Getenv,
		Offline:   *offline,
	}
	phases := builder.Phases()
	if *offline {
		phases = builder.OfflinePhases()
	}
	screen := tui.BuildScreen(imageRecipe.Image.Name, imageRecipe.Image.Release, release.Suite, imageRecipe.Image.Arch, archiveURL, phases)
	if a.useFullScreen(*plain) {
		return a.runBuildFullScreen(ctx, imageRecipe, options, screen, archiveURL)
	}
	return a.runBuildPlain(ctx, stopSignalHandling, imageRecipe, options, screen, archiveURL)
}

// runBuildPlain runs the build with progress as lines on standard error.
func (a *App) runBuildPlain(ctx context.Context, stopSignalHandling func(), imageRecipe recipe.Recipe, options builder.Options, screen tui.Screen, archiveURL string) int {
	a.stderrf("frostroot: building %s\n", screen.Subtitle)
	var buildFinished atomic.Bool
	go func() {
		<-ctx.Done()
		if buildFinished.Load() {
			return
		}
		// Restore default signal handling, so that a second Ctrl-C ends
		// frostroot at once. mmdebstrap runs in its own process group and
		// finishes its cleanup either way; it must never be killed in the
		// middle of it.
		stopSignalHandling()
		a.stderrf("\nfrostroot: interrupted; waiting for mmdebstrap to clean up (Ctrl-C again to stop waiting)\n")
	}()
	options.Progress = newPlainProgress(a.Stderr)
	result, err := a.Builder.Build(ctx, imageRecipe, options)
	interrupted := ctx.Err() != nil
	buildFinished.Store(true)

	if err != nil {
		return a.reportBuildFailure(err, interrupted, archiveURL, result.WorkDir)
	}
	a.reportBuildSuccess(imageRecipe, result)
	return exitSuccess
}

// runBuildFullScreen runs the build behind the progress screen. In the raw
// terminal Ctrl-C is a key, not a signal: the screen cancels the build on the
// first press and stays up until mmdebstrap has cleaned up; a second press
// closes the screen and leaves mmdebstrap to finish on its own. SIGTERM and
// SIGHUP still cancel the build through ctx.
func (a *App) runBuildFullScreen(ctx context.Context, imageRecipe recipe.Recipe, options builder.Options, screen tui.Screen, archiveURL string) int {
	buildCtx, cancelBuild := context.WithCancel(ctx)
	defer cancelBuild()
	events := make(chan builder.ProgressEvent, progressEventBuffer)
	options.Progress = builder.ProgressFunc(func(event builder.ProgressEvent) { events <- event })
	done := make(chan error, 1)
	var result builder.Result
	go func() {
		var err error
		result, err = a.Builder.Build(buildCtx, imageRecipe, options)
		close(events)
		done <- err
	}()

	outcome, err := tui.RunProgress(screen, events, done, cancelBuild, a.Stdin, a.Stdout)
	if err != nil {
		// The screen failed; the build did not. Wait for it and report as
		// the plain interface would.
		a.stderrf("frostroot: %v; waiting for the build without it\n", err)
		outcome = tui.Outcome{Err: <-done}
	}
	if outcome.Abandoned {
		a.stderrf("frostroot: build interrupted; mmdebstrap is finishing its cleanup on its own, and the work directory is kept\n")
		return exitInterrupted
	}
	// done has delivered, so result is complete and no longer written to.
	if outcome.Err != nil {
		return a.reportBuildFailure(outcome.Err, outcome.Interrupted || ctx.Err() != nil, archiveURL, result.WorkDir)
	}
	a.reportBuildSuccess(imageRecipe, result)
	return exitSuccess
}

// reportBuildFailure explains a failed build and returns its exit code.
func (a *App) reportBuildFailure(err error, interrupted bool, archiveURL, keptWorkDir string) int {
	switch {
	case errors.Is(err, context.Canceled) || interrupted:
		a.stderrf("frostroot: build interrupted; no lock or tarball was written\n")
		a.reportKeptWorkDir(keptWorkDir)
		return exitInterrupted
	case errors.Is(err, builder.ErrNotLinux), errors.Is(err, builder.ErrNoMmdebstrap),
		errors.Is(err, builder.ErrNoKeyring), errors.Is(err, builder.ErrBadWorkRoot),
		errors.Is(err, builder.ErrUnwritableOutput), errors.Is(err, builder.ErrNoLock),
		errors.Is(err, builder.ErrLockMismatch), errors.Is(err, builder.ErrPoolIncomplete),
		errors.Is(err, pool.ErrNoChecksums), errors.Is(err, pool.ErrBadLock):
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	default:
		a.stderrf("frostroot: build failed: %v\n", err)
		if archiveURL != vendorDebsDisplayName && containsAny(err.Error(), archiveUnreachableMessages) {
			a.stderrf("frostroot: could not fetch from %s; check the network, or retry with --mirror URL\n", archiveURL)
		}
		a.reportKeptWorkDir(keptWorkDir)
		return exitBuildFailed
	}
}

// megabyte is the unit tarball sizes are reported in.
const megabyte = 1 << 20

// reportBuildSuccess prints what was written and how to import it.
func (a *App) reportBuildSuccess(imageRecipe recipe.Recipe, result builder.Result) {
	imageName := imageRecipe.Image.Name
	relativeTarballPath := filepath.ToSlash(export.TarballRelPath(imageName, imageRecipe.Image.Release, imageRecipe.Image.Arch))
	sizeSuffix := ""
	if tarballInfo, err := os.Stat(result.TarballPath); err == nil {
		sizeSuffix = " (" + formatMegabytes(tarballInfo.Size()) + ")"
	}
	if result.Offline {
		a.stdoutf("\nWrote %s%s, rebuilt from frostroot.lock: %s, every one as locked.\n", relativeTarballPath, sizeSuffix, packageCount(result.InstalledPackageCount))
	} else {
		a.stdoutf("\nWrote %s%s\nWrote frostroot.lock (%s)\n", relativeTarballPath, sizeSuffix, packageCount(result.InstalledPackageCount))
	}
	a.stdoutf("\nImport it on Windows:\n  wsl --import %s <install-dir> %s\n", imageName, relativeTarballPath)
	if windowsPath, err := a.WSLPath(result.TarballPath); err == nil && windowsPath != "" {
		// PowerShell single quotes are literal; an embedded quote is doubled.
		a.stdoutf("or from any directory in PowerShell:\n  wsl --import %s <install-dir> '%s'\n",
			imageName, strings.ReplaceAll(windowsPath, "'", "''"))
	}
	if !result.Offline {
		a.stdoutf("\nTo be able to rebuild this exact image later, offline:\n  frostroot vendor\n")
	}
	if result.CleanupErr != nil {
		a.stderrf("warning: could not remove the work directory: %v\n", result.CleanupErr)
	}
	a.reportKeptWorkDir(result.WorkDir)
}

// formatMegabytes formats a size in bytes as whole megabytes, rounded.
func formatMegabytes(sizeInBytes int64) string {
	return fmt.Sprintf("%d MB", (sizeInBytes+megabyte/2)/megabyte)
}

// reportKeptWorkDir tells the user where a kept work directory and its
// mmdebstrap log are, if any.
func (a *App) reportKeptWorkDir(workDir string) {
	if workDir != "" {
		a.stderrf("frostroot: work directory kept at %s (mmdebstrap output in %s)\n", workDir, builder.LogFileName)
	}
}

// validateMirrorURL accepts http and https URLs with a host. The URL becomes
// part of apt source lines, so whitespace would split it.
func validateMirrorURL(mirrorURL string) error {
	parsed, err := url.Parse(mirrorURL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || strings.ContainsAny(mirrorURL, " \t\r\n") {
		return fmt.Errorf("--mirror %q: expected an http or https URL such as http://mirror.example.com/ubuntu", mirrorURL)
	}
	return nil
}

// containsAny reports whether text contains any of substrings.
func containsAny(text string, substrings []string) bool {
	for _, substring := range substrings {
		if strings.Contains(text, substring) {
			return true
		}
	}
	return false
}

// windowsPathOf returns the Windows form of a Linux path when running under
// WSL, using wslpath.
func windowsPathOf(linuxPath string) (string, error) {
	if _, err := exec.LookPath("wslpath"); err != nil {
		return "", err
	}
	output, err := exec.Command("wslpath", "-w", linuxPath).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}
