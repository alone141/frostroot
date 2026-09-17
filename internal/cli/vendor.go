package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	run := &vendorRun{
		poolDir:   filepath.Join(a.RecipeDir, filepath.FromSlash(pool.DebsDirName)),
		entries:   entries,
		mirrorURL: *mirrorURL,
		source:    source,
		prune:     *prune,
		fallback:  a.VendorFallback,
	}
	if run.fallback == nil {
		run.fallback = pool.FallbackURL(lock)
	}

	ctx, stopSignalHandling := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	defer stopSignalHandling()
	screen := tui.Screen{
		Title:    "frostroot vendor",
		Subtitle: fmt.Sprintf("%s · %s · %s", packageCount(len(entries)), builder.FormatBytes(pool.TotalSize(entries)), source),
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
	switch {
	case len(run.pruned) > 0:
		a.stdoutf("Removed %d file(s) the lock does not name: %s\n", len(run.pruned), strings.Join(run.pruned, ", "))
	case len(summary.Extra) > 0:
		a.stdoutf("%d .deb file(s) in %s are not in the lock; remove them with: frostroot vendor --prune\n", len(summary.Extra), pool.DebsDirName)
	}
	a.stdoutf("\nRebuild the exact image without the archive:\n  frostroot build --offline\n")
}

// vendorRun is one run of the vendor command: the fetch, the optional prune,
// and the progress they report.
type vendorRun struct {
	poolDir   string
	entries   []pool.Entry
	mirrorURL string // --mirror, or "" for the base URLs the lock records
	source    string // where the packages come from, for messages
	prune     bool
	fallback  func(pool.Entry) string
	progress  builder.Progress

	summary pool.Summary
	pruned  []string
}

// do fetches and, if asked, prunes, reporting phases as it goes.
func (r *vendorRun) do(ctx context.Context) error {
	report := func(event builder.ProgressEvent) { r.progress.Report(event) }
	report(builder.ProgressEvent{Phase: builder.PhaseVendorRead, Kind: builder.EventPhaseStarted})
	report(builder.ProgressEvent{Phase: builder.PhaseVendorRead, Kind: builder.EventLogLine, Line: fmt.Sprintf("%s: %s, %s, from %s", builder.LockFileName, packageCount(len(r.entries)), builder.FormatBytes(pool.TotalSize(r.entries)), r.source)})
	report(builder.ProgressEvent{Phase: builder.PhaseVendorRead, Kind: builder.EventPhaseFinished})
	report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventPhaseStarted})
	downloading := false
	summary, err := pool.Fetch(ctx, pool.FetchOptions{
		Dir:       r.poolDir,
		Entries:   r.entries,
		MirrorURL: r.mirrorURL,
		Fallback:  r.fallback,
		UserAgent: "frostroot/" + builder.Version,
		OnChecked: func(checked, total int) {
			report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventProgress, Done: int64(checked), Total: int64(total), Unit: builder.UnitFiles})
		},
		OnVerified: func(status pool.Status) {
			report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventLogLine, Line: fmt.Sprintf("%d present, %d missing, %d corrupt, %d not in the lock", len(status.Present), len(status.Missing), len(status.Corrupt), len(status.Extra))})
			report(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventPhaseFinished})
			report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventPhaseStarted})
			downloading = true
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
	r.summary = summary
	if err != nil {
		return err
	}
	if downloading {
		report(builder.ProgressEvent{Phase: builder.PhaseVendorDownload, Kind: builder.EventPhaseFinished})
	}
	if !r.prune {
		return nil
	}
	report(builder.ProgressEvent{Phase: builder.PhaseVendorPrune, Kind: builder.EventPhaseStarted})
	pruned, err := pool.Prune(r.poolDir, r.entries)
	if err != nil {
		return fmt.Errorf("pruning %s: %w", pool.DebsDirName, err)
	}
	r.pruned = pruned
	for _, removed := range pruned {
		report(builder.ProgressEvent{Phase: builder.PhaseVendorPrune, Kind: builder.EventLogLine, Line: "removed " + removed})
	}
	report(builder.ProgressEvent{Phase: builder.PhaseVendorPrune, Kind: builder.EventPhaseFinished})
	return nil
}
