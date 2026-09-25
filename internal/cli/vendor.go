package cli

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"frostroot/internal/builder"
	"frostroot/internal/pki"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
	"frostroot/internal/tui"
)

const vendorUsageText = `usage: frostroot vendor [--mirror URL] [--ca-bundle FILE | --insecure] [--prune] [--plain]

Download every package frostroot.lock names into vendor/debs/, and every Python
wheel it names into vendor/wheels/, checked against the lock's checksums, so
that "frostroot build --offline" can rebuild the exact image without the archive
or PyPI. Files already there and correct are kept, so rerunning resumes an
interrupted download, and --prune removes files the lock does not name, a
download that was cut short included. Packages the archive has since dropped
are fetched from Launchpad, which keeps every file ever published. Both are
HTTPS, so on a network that inspects TLS, --ca-bundle names a PEM file of
certificate authorities to trust while fetching, and --insecure skips
certificate verification instead. Every file is still checked against the
lock either way.

A lock whose Python packages were resolved with "build --insecure" says so,
and vendor repeats it: those hashes are only as trustworthy as that network
was. Vendoring such a lock without --insecure, on a trusted network, checks
them: a wheel that is not what its server publishes is refused.

`

func (a *App) runVendor(args []string) int {
	flags := a.newFlagSet("vendor", vendorUsageText)
	mirrorURL := flags.String("mirror", "", "download from this archive base `URL` instead of the one recorded in the lock")
	caBundlePath := flags.String("ca-bundle", "", "PEM `FILE` of certificate authorities to trust while fetching, for a network that inspects TLS")
	insecure := flags.Bool("insecure", false, insecureFlagUsage)
	prune := flags.Bool("prune", false, "remove files in vendor/debs and vendor/wheels that the lock does not name")
	plain := flags.Bool("plain", false, "print progress as lines instead of showing the full-screen progress screen")
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	if *mirrorURL != "" {
		if err := validateMirrorURL(*mirrorURL); err != nil {
			a.stderrf("frostroot: %v\n", err)
			return exitUserError
		}
	}
	lockPath := filepath.Join(a.RecipeDir, builder.LockFileName)
	lock, err := recipe.LoadLock(lockPath)
	if errors.Is(err, os.ErrNotExist) {
		a.stderrf("frostroot: no %s in %s; run frostroot build first\n", builder.LockFileName, a.RecipeDir)
		return exitUserError
	}
	if err != nil {
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	entries, err := pool.Manifest(lock)
	if errors.Is(err, pool.ErrNoChecksums) {
		a.stderrf("frostroot: %s was written by frostroot %s and records no checksums; run frostroot build once with this version, then vendor\n", builder.LockFileName, lock.FrostrootVersion)
		return exitUserError
	}
	if err != nil {
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	source := lock.Mirror
	if *mirrorURL != "" {
		source = *mirrorURL
	}
	if len(lock.Repositories) > 0 {
		source += fmt.Sprintf(" and %d more", len(lock.Repositories))
	}
	wheelEntries, err := pool.WheelManifest(lock)
	if err != nil {
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	rootCAs, ok := a.vendorTrustPool(lock, *caBundlePath)
	if !ok {
		return exitUserError
	}
	if *insecure {
		a.warnInsecure(insecureVendorDetail)
	}
	a.warnIfUnverified(lock)
	run := &vendorRun{
		poolDir:      filepath.Join(a.RecipeDir, filepath.FromSlash(pool.DebsDirName)),
		entries:      entries,
		wheelPoolDir: filepath.Join(a.RecipeDir, filepath.FromSlash(pool.WheelsDirName)),
		wheelEntries: wheelEntries,
		mirrorURL:    *mirrorURL,
		source:       source,
		prune:        *prune,
		fallback:     a.VendorFallback,
		client:       a.VendorClient,
		rootCAs:      rootCAs,
		insecure:     *insecure,
		checksLock:   lock.PythonResolvedUnverified() && !*insecure,
	}
	if run.fallback == nil {
		run.fallback = pool.FallbackURL(lock)
	}

	ctx, stopSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignalHandling()
	screen := tui.Screen{
		Title:    "frostroot vendor",
		Subtitle: strings.Join(subtitleParts(run, source), " · "),
		Phases:   builder.VendorPhases(*prune),
		LogTitle: "downloads",
	}
	var outcome tui.Outcome
	if a.useFullScreen(*plain) {
		outcome = a.runVendorFullScreen(ctx, run, screen)
	} else {
		a.stderrf("frostroot: vendoring %s\n", screen.Subtitle)
		run.progress = newPlainProgress(a.Stderr)
		outcome = tui.Outcome{Err: run.do(ctx), Interrupted: ctx.Err() != nil}
	}
	switch {
	case outcome.Abandoned:
		// The fetch is still winding down as the process exits, so the
		// download it was in the middle of may not have removed its
		// temporary file yet; --prune counts that file among the extras.
		a.stderrf("frostroot: vendor interrupted; finished downloads are kept, and frostroot vendor --prune removes one that was cut short\n")
		return exitInterrupted
	case errors.Is(outcome.Err, context.Canceled) || outcome.Interrupted:
		a.stderrf("frostroot: vendor interrupted; finished downloads are kept, so rerunning resumes\n")
		return exitInterrupted
	case outcome.Err != nil:
		a.stderrf("frostroot vendor: %v\n", outcome.Err)
		if errors.Is(outcome.Err, pool.ErrNotFound) {
			a.stderrf("frostroot: the archive drops superseded packages after a while; vendor soon after building, or rebuild and vendor again\n")
		}
		return exitBuildFailed
	}
	a.reportVendorSuccess(run)
	return exitSuccess
}

// runVendorFullScreen runs the fetch behind the progress screen; see
// runBuildFullScreen for how Ctrl-C works there.
func (a *App) runVendorFullScreen(ctx context.Context, run *vendorRun, screen tui.Screen) tui.Outcome {
	fetchCtx, cancelFetch := context.WithCancel(ctx)
	defer cancelFetch()
	events := make(chan builder.ProgressEvent, progressEventBuffer)
	run.progress = builder.ProgressFunc(func(event builder.ProgressEvent) { events <- event })
	done := make(chan error, 1)
	var fetchErr error
	go func() {
		fetchErr = run.do(fetchCtx)
		// Closing events publishes fetchErr to whoever drains the channel;
		// done is the screen's, and a screen that failed still takes it.
		close(events)
		done <- fetchErr
	}()
	outcome, err := tui.RunProgress(screen, events, done, cancelFetch, a.Stdin, a.Stdout)
	if err != nil {
		a.stderrf("frostroot: %v; waiting for the downloads without it\n", err)
		a.followWithoutScreen(events)
		outcome = tui.Outcome{Err: fetchErr, Interrupted: ctx.Err() != nil}
	}
	return outcome
}

// reportVendorSuccess prints what each pool now holds and the next step.
func (a *App) reportVendorSuccess(run *vendorRun) {
	a.stdoutf("\nVendored %s into %s: %s.\n", describePool(packageCount(len(run.entries)), run.entries),
		pool.DebsDirName, fetchDetails(run.summaryByPool[pool.DebsDirName]))
	if len(run.wheelEntries) > 0 {
		a.stdoutf("Vendored %s into %s: %s.\n", describePool(wheelCount(len(run.wheelEntries)), run.wheelEntries),
			pool.WheelsDirName, fetchDetails(run.summaryByPool[pool.WheelsDirName]))
	}
	if len(run.pruned) > 0 {
		a.stdoutf("Removed %d file(s) the lock does not name: %s\n", len(run.pruned), strings.Join(run.pruned, ", "))
	} else {
		if extra := run.summaryByPool[pool.DebsDirName].Extra; len(extra) > 0 {
			a.stdoutf("%d file(s) in %s are not in the lock; remove them with: frostroot vendor --prune\n", len(extra), pool.DebsDirName)
		}
		if extra := run.summaryByPool[pool.WheelsDirName].Extra; len(extra) > 0 {
			a.stdoutf("%d file(s) in %s are not in the lock; remove them with: frostroot vendor --prune\n", len(extra), pool.WheelsDirName)
		}
	}
	if run.checksLock {
		a.reportUnverifiedLockChecked(run)
	}
	a.stdoutf("\nRebuild the exact image without the archive:\n  frostroot build --offline\n")
	a.stdoutf("%s", goModuleVendorNote(a.RecipeDir))
}

// reportUnverifiedLockChecked says what a verified vendor run has shown
// about a lock whose Python packages were resolved with --insecure. A wheel
// downloaded now came from its server over a verified connection and
// matched the lock's hash, so that hash is what the server publishes; a
// wheel already in the pool was compared with the lock and nothing else, so
// it proves nothing about the server. A real but older release passes
// either way, and the README says so.
func (a *App) reportUnverifiedLockChecked(run *vendorRun) {
	summary := run.summaryByPool[pool.WheelsDirName]
	switch {
	case len(run.wheelEntries) == 0:
	case summary.Present == 0:
		a.stdoutf("The lock's Python packages were resolved without verifying TLS; every wheel downloaded now matched the lock over a verified connection, so its hashes are what the servers publish.\n")
	default:
		a.stdoutf("The lock's Python packages were resolved without verifying TLS; %d of %d wheels matched the lock over a verified connection, and %d were already in %s and not downloaded again. Remove %s and run frostroot vendor again to check every one.\n",
			summary.Fetched, len(run.wheelEntries), summary.Present, pool.WheelsDirName, pool.WheelsDirName)
	}
}

// subtitleParts describes the run above the progress screen: how many files,
// how many bytes when the lock knows, and where they come from.
func subtitleParts(run *vendorRun, source string) []string {
	parts := []string{packageCount(len(run.allEntries()))}
	if size := poolSize(run.allEntries()); size != "" {
		parts = append(parts, size)
	}
	return append(parts, source)
}

// poolSize renders how many bytes a pool holds, or "" when the lock does not
// know. pip's installation report carries no file size, so a wheel pool knows
// only the pinned pip's; adding up the few sizes it has and calling that the
// total would be worse than saying nothing.
func poolSize(entries []pool.Entry) string {
	for _, entry := range entries {
		if entry.Size <= 0 {
			return ""
		}
	}
	return builder.FormatBytes(pool.TotalSize(entries))
}

// describePool renders "N packages (12 MB)", or "N packages" when the sizes
// are not known.
func describePool(count string, entries []pool.Entry) string {
	if size := poolSize(entries); size != "" {
		return count + " (" + size + ")"
	}
	return count
}

// fetchDetails describes one pool's fetch: what was downloaded, what was
// already there, and what had to be replaced.
func fetchDetails(summary pool.Summary) string {
	details := []string{fmt.Sprintf("%d downloaded", summary.Fetched)}
	if summary.Present > 0 {
		details = append(details, fmt.Sprintf("%d already there", summary.Present))
	}
	if summary.Replaced > 0 {
		details = append(details, fmt.Sprintf("%d replaced", summary.Replaced))
	}
	return strings.Join(details, ", ")
}

// vendorRun is one run of the vendor command: the fetch, the optional prune,
// and the progress they report.
type vendorRun struct {
	poolDir string
	entries []pool.Entry
	// wheelPoolDir and wheelEntries are the Python side, empty for a lock
	// with no Python packages. Wheels come from PyPI, each by its own whole
	// URL, so neither --mirror nor the Launchpad fallback touches them.
	wheelPoolDir string
	wheelEntries []pool.Entry
	mirrorURL    string // --mirror, or "" for the base URLs the lock records
	source       string // where the packages come from, for messages
	prune        bool
	fallback     func(pool.Entry) string
	client       *http.Client
	rootCAs      *x509.CertPool
	insecure     bool // --insecure: verify no certificate
	// checksLock means this verified run reads a lock whose Python packages
	// were resolved with --insecure, so what it downloads says something
	// about that lock, and the report says what.
	checksLock bool
	progress   builder.Progress

	// summaryByPool holds what each pool directory ended up with, so that a
	// message can report the packages and the wheels apart.
	summaryByPool map[string]pool.Summary
	pruned        []string
}

// allEntries returns both pools' entries, for the counts and sizes messages
// show.
func (r *vendorRun) allEntries() []pool.Entry {
	return slices.Concat(r.entries, r.wheelEntries)
}

// do fetches both pools and, if asked, prunes them, reporting phases as it
// goes.
func (r *vendorRun) do(ctx context.Context) error {
	report := func(event builder.ProgressEvent) { r.progress.Report(event) }
	report(builder.ProgressEvent{Phase: builder.PhaseVendorRead, Kind: builder.EventPhaseStarted})
	report(builder.ProgressEvent{Phase: builder.PhaseVendorRead, Kind: builder.EventLogLine, Line: fmt.Sprintf("%s: %s, %s, from %s", builder.LockFileName, packageCount(len(r.entries)), builder.FormatBytes(pool.TotalSize(r.entries)), r.source)})
	if len(r.wheelEntries) > 0 {
		report(builder.ProgressEvent{Phase: builder.PhaseVendorRead, Kind: builder.EventLogLine, Line: fmt.Sprintf("%s: %s from PyPI", builder.LockFileName, wheelCount(len(r.wheelEntries)))})
	}
	report(builder.ProgressEvent{Phase: builder.PhaseVendorRead, Kind: builder.EventPhaseFinished})
	report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventPhaseStarted})
	downloading := false
	r.summaryByPool = map[string]pool.Summary{}
	if err := r.fetchPool(ctx, report, &downloading, pool.DebsDirName, r.poolDir, r.entries, r.mirrorURL, r.fallback); err != nil {
		return err
	}
	if len(r.wheelEntries) > 0 {
		// A wheel carries its own whole URL and PyPI never drops a file, so
		// there is no mirror to substitute and no fallback to try.
		if err := r.fetchPool(ctx, report, &downloading, pool.WheelsDirName, r.wheelPoolDir, r.wheelEntries, "", nil); err != nil {
			return err
		}
	}
	if downloading {
		report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventPhaseFinished})
	}
	if !r.prune {
		return nil
	}
	report(builder.ProgressEvent{Phase: builder.PhaseVendorPrune, Kind: builder.EventPhaseStarted})
	for _, prunable := range []struct {
		dir     string
		entries []pool.Entry
		name    string
	}{
		{r.poolDir, r.entries, pool.DebsDirName},
		{r.wheelPoolDir, r.wheelEntries, pool.WheelsDirName},
	} {
		// A pool the lock no longer names is exactly the one that needs
		// pruning: drop [python] from a recipe and every wheel on disk is a
		// file the lock does not name. Prune is a no-op on a directory that
		// is not there.
		pruned, err := pool.Prune(prunable.dir, prunable.entries)
		if err != nil {
			return fmt.Errorf("pruning %s: %w", prunable.name, err)
		}
		r.pruned = append(r.pruned, pruned...)
		for _, removed := range pruned {
			report(builder.ProgressEvent{Phase: builder.PhaseVendorPrune, Kind: builder.EventLogLine, Line: "removed " + removed})
		}
	}
	report(builder.ProgressEvent{Phase: builder.PhaseVendorPrune, Kind: builder.EventPhaseFinished})
	return nil
}

// fetchPool fills one pool directory, adding what it did to the run's
// summary. downloading says whether the download phase has been announced
// yet, so that two pools still report one phase.
func (r *vendorRun) fetchPool(ctx context.Context, report func(builder.ProgressEvent), downloading *bool, poolName, dir string, entries []pool.Entry, mirrorURL string, fallback func(pool.Entry) string) error {
	sizesUnknown := poolSize(entries) == ""
	summary, err := pool.Fetch(ctx, pool.FetchOptions{
		Dir:       dir,
		Entries:   entries,
		MirrorURL: mirrorURL,
		Fallback:  fallback,
		Client:    r.client,
		RootCAs:   r.rootCAs,
		Insecure:  r.insecure,
		UserAgent: "frostroot/" + builder.Version,
		OnChecked: func(checked, total int) {
			report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventProgress, Done: int64(checked), Total: int64(total), Unit: builder.UnitFiles})
		},
		OnVerified: func(status pool.Status) {
			report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventLogLine, Line: fmt.Sprintf("%d present, %d missing, %d corrupt, %d not in the lock", len(status.Present), len(status.Missing), len(status.Corrupt), len(status.Extra))})
			if !*downloading {
				report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventPhaseFinished})
				report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventPhaseStarted})
				*downloading = true
			}
		},
		OnReplaced: func(entry pool.Entry) {
			report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventLogLine, Line: "replacing corrupt " + entry.FileName})
		},
		OnDownloaded: func(doneBytes, totalBytes int64) {
			if sizesUnknown {
				// A total the lock cannot know would make the bar read
				// "19 MB / 1.7 MB"; without one the display counts bytes.
				totalBytes = 0
			}
			report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventProgress, Done: doneBytes, Total: totalBytes, Unit: builder.UnitBytes})
		},
		OnFileDone: func(entry pool.Entry, sourceURL string) {
			line := entry.FileName
			if entry.Size > 0 {
				line += " (" + builder.FormatBytes(entry.Size) + ")"
			}
			report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventLogLine, Line: line + " from " + sourceURL})
		},
	})
	r.summaryByPool[poolName] = summary
	return err
}

// vendorTrustPool returns the authorities vendor fetches with: the host's
// store, the certificates the lock names, and --ca-bundle. A build fetches
// with the recipe's [certificates] as well as the flag (apt's CaInfo holds
// both), so a recipe whose only authority for an inspecting proxy lives in
// [certificates] could build and then fail TLS on the very URLs the lock had
// just recorded. Nothing is installed here: vendor only downloads.
func (a *App) vendorTrustPool(lock recipe.Lockfile, caBundlePath string) (*x509.CertPool, bool) {
	bundle, ok := a.readCABundle(caBundlePath)
	if !ok {
		return nil, false
	}
	var paths []string
	for _, certificate := range lock.Certificates {
		paths = append(paths, certificate.Path)
	}
	// The same reader the build used, so the two trust the same bytes.
	certificates, err := builder.ReadCertificates(a.RecipeDir, paths)
	if err != nil {
		a.stderrf("frostroot: %v\n", err)
		return nil, false
	}
	extraPEM := append(append([]byte(nil), certificates.PEM...), bundle...)
	if len(extraPEM) == 0 {
		return nil, true // the host's own store, which is what a fetch uses anyway
	}
	trusted, err := pki.SystemPoolWith(extraPEM)
	if err != nil {
		a.stderrf("frostroot: %v\n", err)
		return nil, false
	}
	return trusted, true
}
