package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

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

const buildUsageText = `usage: frostroot build [--mirror URL | --offline] [--ca-bundle FILE | --insecure] [--keep-work] [--plain]

Build frostroot.lock and dist/<name>-ubuntu-<release>-amd64.tar.gz from frostroot.toml.
Needs Linux, mmdebstrap, network, and user namespaces or root. Never prompts.
The image is frozen at the instant the build starts, or at SOURCE_DATE_EPOCH
when that is set: no file in it is dated later, and the lock records the instant.

On a network that inspects TLS, --ca-bundle names a PEM file of certificate
authorities to trust while fetching. It is not installed in the image and not
recorded in the lock, so a build with it produces the same bytes as a build
without it. To have the image trust them too, name the file in [certificates]
in the recipe instead.

--insecure skips certificate verification on every fetch instead, for a
network whose authority you do not have: apt's on the build host and pip's in
the image being built. The packages apt installs are still verified against
their signatures. The Python packages are not: whoever is on the network path
decides what is resolved, and the lock records that it was resolved
unverified. Nothing of the flag reaches the image; apt inside it verifies as
before.

With --offline, rebuild the image from frostroot.lock and vendor/debs (see
frostroot vendor) without the archive: the same packages at the same versions,
verified against the lock, frozen at the lock's instant, so that every offline
build of one lock produces the same bytes. The lock is read, not written.

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
	caBundlePath := flags.String("ca-bundle", "", "PEM `FILE` of certificate authorities to trust while fetching, for a network that inspects TLS")
	insecure := flags.Bool("insecure", false, insecureFlagUsage)
	keepWork := flags.Bool("keep-work", false, "keep the work directory after a successful build")
	plain := flags.Bool("plain", false, "print progress as lines instead of showing the full-screen build screen")
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	if *mirrorURL != "" && *offline {
		a.stderrf("frostroot: --mirror and --offline exclude each other: an offline build installs from %s\n", vendorDebsDisplayName)
		return exitUserError
	}
	if *insecure && *offline {
		// Not an error: a script that always passes the flag should still
		// rebuild offline. There is simply no fetch to skip verifying.
		a.stderrf("note: an offline build fetches nothing, so --insecure changes nothing\n")
		*insecure = false
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
	hasPython := len(imageRecipe.PythonPackages()) > 0
	switch {
	case *insecure && hasPython:
		a.warnInsecure(insecureBuildDetail, insecurePythonDetail)
	case *insecure:
		a.warnInsecure(insecureBuildDetail)
	case *offline:
		// The lock is about to be rebuilt from; what it says about how it
		// was resolved is worth reading before the minutes the build takes.
		a.warnIfLockUnverified()
	}

	// SIGHUP too: mmdebstrap runs in its own process group, so a closed
	// terminal no longer reaches it directly and frostroot must pass the
	// interrupt on instead of leaving an orphan bootstrapping for minutes.
	ctx, stopSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignalHandling()
	extraTrust, ok := a.readCABundle(*caBundlePath)
	if !ok {
		return exitUserError
	}
	options := builder.Options{
		RecipeDir:     a.RecipeDir,
		MirrorURL:     *mirrorURL,
		KeepWork:      *keepWork,
		GOOS:          a.GOOS,
		Getenv:        a.Getenv,
		Offline:       *offline,
		ExtraTrustPEM: extraTrust,
		Insecure:      *insecure,
	}
	phases := builder.Phases(hasPython)
	if *offline {
		phases = builder.OfflinePhases(hasPython)
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
	var buildErr error
	go func() {
		result, buildErr = a.Builder.Build(buildCtx, imageRecipe, options)
		// Closing events is what publishes result and buildErr to whoever
		// drains the channel; done is the screen's.
		close(events)
		done <- buildErr
	}()

	outcome, err := tui.RunProgress(screen, events, done, cancelBuild, a.Stdin, a.Stdout)
	if err != nil {
		// The screen failed; the build did not. Follow it as the plain
		// interface would, and report as it would. Not by reading done: a
		// screen that failed leaves its own goroutine waiting on done,
		// which takes the one result the build sends.
		a.stderrf("frostroot: %v; waiting for the build without it\n", err)
		a.followWithoutScreen(events)
		outcome = tui.Outcome{Err: buildErr, Interrupted: ctx.Err() != nil}
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

// followWithoutScreen stands in for a progress screen that failed: it
// prints the events the work goes on sending as plain lines, the way
// --plain would, until the work closes the channel, which it does once its
// result is written. Waiting for the result alone hung frostroot for good:
// the work filled the channel's buffer, blocked in its progress callback,
// never reached its result, and never saw a signal, so only SIGKILL ended
// it, with mmdebstrap still running.
func (a *App) followWithoutScreen(events <-chan builder.ProgressEvent) {
	plain := newPlainProgress(a.Stderr)
	for event := range events {
		plain.Report(event)
	}
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
		errors.Is(err, builder.ErrSourceKey), errors.Is(err, builder.ErrBadSourceDateEpoch),
		errors.Is(err, pool.ErrNoChecksums), errors.Is(err, pool.ErrBadLock):
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	default:
		a.reportBuildError(err, keptWorkDir)
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
	frozenAt := formatInstant(result.SourceDateEpoch)
	switch {
	case result.Offline && result.Reproducible:
		a.stdoutf("\nWrote %s%s, rebuilt from frostroot.lock: %s, every one as locked.\n", relativeTarballPath, sizeSuffix, packageCount(result.InstalledPackageCount))
		a.stdoutf("Frozen at %s: every offline build of this lock produces this tarball, byte for byte.\n", frozenAt)
	case result.Offline:
		a.stdoutf("\nWrote %s%s, rebuilt from frostroot.lock: %s, every one as locked.\n", relativeTarballPath, sizeSuffix, packageCount(result.InstalledPackageCount))
		a.stderrf("warning: frostroot.lock was written before frostroot 0.6 and records no instant to freeze at, so this tarball\n" +
			"is not byte-identical with other builds. Run frostroot build online once more, then frostroot vendor, and it will be.\n")
	default:
		a.stdoutf("\nWrote %s%s\nWrote frostroot.lock (%s), frozen at %s\n", relativeTarballPath, sizeSuffix, packageCount(result.InstalledPackageCount), frozenAt)
	}
	if result.PythonPackageCount > 0 {
		a.stdoutf("The image's virtual environment holds %s, on PATH for every login shell: %s\n",
			packageCount(result.PythonPackageCount), builder.PythonVenvPath)
	}
	if result.Offline && a.Getenv(builder.SourceDateEpochVariable) != "" {
		a.stderrf("note: %s is set, but an offline build freezes at the lock's instant and ignores it\n", builder.SourceDateEpochVariable)
	}
	a.stdoutf("\nImport it on Windows:\n  wsl --import %s <install-dir> %s\n", imageName, relativeTarballPath)
	if windowsPath, err := a.WSLPath(result.TarballPath); err == nil && windowsPath != "" {
		// PowerShell single quotes are literal; an embedded quote is doubled.
		a.stdoutf("or from any directory in PowerShell:\n  wsl --import %s <install-dir> '%s'\n",
			imageName, strings.ReplaceAll(windowsPath, "'", "''"))
	}
	if !result.Offline {
		a.stdoutf("\nTo be able to rebuild this exact image later, offline:\n  frostroot vendor\n")
	} else {
		// An offline build read vendor/, so the pools are there to trip a Go
		// module in the same directory.
		a.stdoutf("%s", goModuleVendorNote(a.RecipeDir))
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

// formatInstant formats seconds since 1970 as a UTC date and time, the way
// the reproducible-builds convention reads.
func formatInstant(epoch int64) string {
	return time.Unix(epoch, 0).UTC().Format("2006-01-02 15:04:05 UTC")
}

// reportBuildError prints why the build failed. When mmdebstrap failed, the
// line of its log that explains it comes first — the whole log is searched,
// because the explanation is often followed by more output than the tail
// holds, pip's traceback or apt's cleanup — and the tail of its output
// after, as before. Any other error is printed as it is.
func (a *App) reportBuildError(err error, keptWorkDir string) {
	var bootstrapErr *builder.BootstrapError
	if !errors.As(err, &bootstrapErr) {
		a.stderrf("frostroot: build failed: %v\n", err)
		return
	}
	a.stderrf("frostroot: build failed: mmdebstrap failed: %v\n", bootstrapErr.Err)
	if keptWorkDir != "" {
		if line, found := builder.FirstErrorLine(filepath.Join(keptWorkDir, builder.LogFileName)); found {
			a.stderrf("frostroot: the first error in %s, line %d:\n  %s\n", builder.LogFileName, line.Number, line.Text)
		}
	}
	a.stderrf("--- last lines of mmdebstrap output ---\n%s\n", bootstrapErr.Tail)
}

// reportKeptWorkDir tells the user where a kept work directory and its
// mmdebstrap log are, if any.
func (a *App) reportKeptWorkDir(workDir string) {
	if workDir != "" {
		a.stderrf("frostroot: work directory kept at %s (mmdebstrap output in %s)\n", workDir, builder.LogFileName)
	}
}

// validateMirrorURL applies the recipe's source URL rule to --mirror. The
// URL becomes part of the deb lines the image keeps, where a space splits
// the line, a bracket is read as an option, "#" comments out the suite and
// the components, and credentials ship in the image. One rule, in one
// place: a copy of it here once drifted to refusing whitespace alone.
func validateMirrorURL(mirrorURL string) error {
	if err := recipe.CheckSourceURL(mirrorURL); err != nil {
		return fmt.Errorf("--mirror: %w", err)
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
