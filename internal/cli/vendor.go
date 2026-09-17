package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"

	"frostroot/internal/builder"
	"frostroot/internal/pool"
	"frostroot/internal/recipe"
	"frostroot/internal/tui"
)

const vendorUsageText = `usage: frostroot vendor [--mirror URL] [--prune] [--plain]

Download every package frostroot.lock names into vendor/debs/, checked against
the lock's checksums, so that "frostroot build --offline" can rebuild the exact
image without the archive. Files already there and correct are kept, so
rerunning resumes an interrupted download. Packages the archive has since
dropped are fetched from Launchpad, which keeps every file ever published.

`

func (a *App) runVendor(args []string) int {
	flags := a.newFlagSet("vendor", vendorUsageText)
	mirrorURL := flags.String("mirror", "", "download from this archive base `URL` instead of the one recorded in the lock")
	prune := flags.Bool("prune", false, "remove .deb files in vendor/debs that the lock does not name")
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
	run := &vendorRun{
		poolDir:      filepath.Join(a.RecipeDir, filepath.FromSlash(pool.DebsDirName)),
		entries:      entries,
		wheelPoolDir: filepath.Join(a.RecipeDir, filepath.FromSlash(pool.WheelsDirName)),
		wheelEntries: wheelEntries,
		mirrorURL:    *mirrorURL,
		source:       source,
		prune:        *prune,
		fallback:     a.VendorFallback,
	}
	if run.fallback == nil {
		run.fallback = pool.FallbackURL(lock)
	}

	ctx, stopSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignalHandling()
	screen := tui.Screen{
		Title:    "frostroot vendor",
		Subtitle: fmt.Sprintf("%s · %s · %s", packageCount(len(run.allEntries())), builder.FormatBytes(pool.TotalSize(run.allEntries())), source),
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
		a.stderrf("frostroot: vendor interrupted; finished downloads are kept, the rest is removed\n")
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
	go func() {
		err := run.do(fetchCtx)
		close(events)
		done <- err
	}()
	outcome, err := tui.RunProgress(screen, events, done, cancelFetch, a.Stdin, a.Stdout)
	if err != nil {
		a.stderrf("frostroot: %v; waiting for the downloads without it\n", err)
		outcome = tui.Outcome{Err: <-done}
	}
	return outcome
}

// reportVendorSuccess prints what the pool now holds and the next step.
func (a *App) reportVendorSuccess(run *vendorRun) {
	summary := run.summary
	details := []string{fmt.Sprintf("%d downloaded", summary.Fetched)}
	if summary.Present > 0 {
		details = append(details, fmt.Sprintf("%d already there", summary.Present))
	}
	if summary.Replaced > 0 {
		details = append(details, fmt.Sprintf("%d replaced", summary.Replaced))
	}
	a.stdoutf("\nVendored %s (%s) into %s: %s.\n", packageCount(len(run.entries)), builder.FormatBytes(pool.TotalSize(run.entries)), pool.DebsDirName, strings.Join(details, ", "))
	if len(run.wheelEntries) > 0 {
		a.stdoutf("Vendored %s into %s.\n", wheelCount(len(run.wheelEntries)), pool.WheelsDirName)
	}
	if len(run.pruned) > 0 {
		a.stdoutf("Removed %d file(s) the lock does not name: %s\n", len(run.pruned), strings.Join(run.pruned, ", "))
	} else {
		if extra := run.extraByPool[pool.DebsDirName]; len(extra) > 0 {
			a.stdoutf("%d .deb file(s) in %s are not in the lock; remove them with: frostroot vendor --prune\n", len(extra), pool.DebsDirName)
		}
		if extra := run.extraByPool[pool.WheelsDirName]; len(extra) > 0 {
			a.stdoutf("%d wheel file(s) in %s are not in the lock; remove them with: frostroot vendor --prune\n", len(extra), pool.WheelsDirName)
		}
	}
	a.stdoutf("\nRebuild the exact image without the archive:\n  frostroot build --offline\n")
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
	progress     builder.Progress

	summary pool.Summary
	// extraByPool holds, per pool directory name, the files there that the
	// lock does not name, so a message can say where they are.
	extraByPool map[string][]string
	pruned      []string
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
	r.extraByPool = map[string][]string{}
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
		if len(prunable.entries) == 0 {
			continue
		}
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
	summary, err := pool.Fetch(ctx, pool.FetchOptions{
		Dir:       dir,
		Entries:   entries,
		MirrorURL: mirrorURL,
		Fallback:  fallback,
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
			report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventProgress, Done: doneBytes, Total: totalBytes, Unit: builder.UnitBytes})
		},
		OnFileDone: func(entry pool.Entry, sourceURL string) {
			report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventLogLine, Line: fmt.Sprintf("%s (%s) from %s", entry.FileName, builder.FormatBytes(entry.Size), sourceURL)})
		},
	})
	r.summary.Present += summary.Present
	r.summary.Fetched += summary.Fetched
	r.summary.Replaced += summary.Replaced
	r.summary.FetchedBytes += summary.FetchedBytes
	r.summary.Extra = append(r.summary.Extra, summary.Extra...)
	if len(summary.Extra) > 0 {
		r.extraByPool[poolName] = summary.Extra
	}
	return err
}
