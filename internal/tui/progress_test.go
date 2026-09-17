package tui

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"frostroot/internal/builder"
)

var sampleScreen = BuildScreen("cpp-lab", "22.04", "jammy", "amd64", "http://archive.ubuntu.com/ubuntu", builder.Phases())

// newProgressDriver returns a driver around a progress model whose channels
// never deliver anything; tests send events and the outcome as messages.
func newProgressDriver(t *testing.T, screen Screen) (*driver, *progressModel, *int) {
	t.Helper()
	cancelCalls := 0
	model := newProgressModel(screen, make(chan builder.ProgressEvent), make(chan error), func() { cancelCalls++ })
	d := newDriver(t, model)
	d.press(tea.WindowSizeMsg{Width: 100, Height: 40})
	return d, model, &cancelCalls
}

func event(phase builder.Phase, kind builder.EventKind) eventMsg {
	return eventMsg(builder.ProgressEvent{Phase: phase, Kind: kind})
}

// statusOf returns the state of phase on the screen.
func statusOf(model *progressModel, phase builder.Phase) phaseStatus {
	return model.phases[model.rows[phase]].status
}

func TestProgressModelShowsPhasesAndProgress(t *testing.T) {
	d, model, _ := newProgressDriver(t, sampleScreen)
	d.press(eventMsg(builder.ProgressEvent{Kind: builder.EventLogFile, Line: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log"}))
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseStarted))
	d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseUpdateIndex, Kind: builder.EventProgress, Done: 9, Unit: builder.UnitFiles}))
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseFinished))
	d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
	d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventProgress, Done: 14_050_000, Total: 28_100_000, Unit: builder.UnitBytes}))
	d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventLogLine, Line: "Get:7 http://archive.ubuntu.com/ubuntu jammy/main amd64 libc6 amd64 2.35-0ubuntu3 [3264 kB]"}))

	view := model.View()
	for _, wantText := range []string{
		"frostroot build", "cpp-lab · Ubuntu 22.04 (jammy, amd64)",
		"Update package index", "9 files",
		"Download packages", " 50%", "14.1 MB / 28.1 MB",
		"Install requested packages", "Place tarball",
		"Get:7 http://archive.ubuntu.com", "mmdebstrap",
		"log: /var/tmp/frostroot-1000/build-1/mmdebstrap.log", "Ctrl-C interrupts",
	} {
		if !strings.Contains(view, wantText) {
			t.Errorf("view lacks %q:\n%s", wantText, view)
		}
	}
	if statusOf(model, builder.PhaseUpdateIndex) != phaseDone || statusOf(model, builder.PhaseDownload) != phaseRunning {
		t.Errorf("phase statuses = %v", model.phases)
	}
}

func TestProgressModelShowsOnlyItsOwnPhases(t *testing.T) {
	// The vendor screen lists the vendor phases; a build phase reported to
	// it (which cannot happen, but the screen must not crash) is ignored.
	screen := Screen{Title: "frostroot vendor", Subtitle: "351 packages · 308 MB", Phases: builder.VendorPhases(true), LogTitle: "downloads"}
	d, model, _ := newProgressDriver(t, screen)
	d.press(event(builder.PhaseVendorRead, builder.EventPhaseStarted))
	d.press(event(builder.PhaseVendorRead, builder.EventPhaseFinished))
	d.press(event(builder.PhaseVendorCheck, builder.EventPhaseStarted))
	d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseVendorCheck, Kind: builder.EventProgress, Done: 120, Total: 351, Unit: builder.UnitFiles}))
	d.press(event(builder.PhaseInstallEssential, builder.EventPhaseStarted))
	view := model.View()
	for _, wantText := range []string{"frostroot vendor", "351 packages · 308 MB", "Read frostroot.lock", "Check vendor/debs", "120 / 351 files", " 34%", "Download packages", "Remove packages not in the lock", "downloads"} {
		if !strings.Contains(view, wantText) {
			t.Errorf("view lacks %q:\n%s", wantText, view)
		}
	}
	if strings.Contains(view, "Install essential") || strings.Contains(view, "mmdebstrap") {
		t.Errorf("the vendor screen shows build phases:\n%s", view)
	}
	if len(model.phases) != 4 || statusOf(model, builder.PhaseVendorCheck) != phaseRunning {
		t.Errorf("phases = %v", model.phases)
	}
}

func TestProgressModelFinishes(t *testing.T) {
	d, model, _ := newProgressDriver(t, sampleScreen)
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseStarted))
	d.press(eventsClosedMsg{})
	if d.quit {
		t.Fatal("the screen must wait for the outcome after the events end")
	}
	d.press(doneMsg{})
	if !d.quit {
		t.Fatal("the screen should quit once the outcome and the last event are in")
	}
	if statusOf(model, builder.PhaseUpdateIndex) != phaseDone {
		t.Errorf("a phase still running at success is done, got %v", statusOf(model, builder.PhaseUpdateIndex))
	}
	if outcome := model.outcome(); outcome.Abandoned || outcome.Interrupted || outcome.Err != nil {
		t.Errorf("outcome = %+v", outcome)
	}
}

func TestProgressModelMarksTheFailedPhase(t *testing.T) {
	d, model, _ := newProgressDriver(t, sampleScreen)
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseStarted))
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseFinished))
	d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
	d.press(doneMsg{err: errors.New("mmdebstrap failed: exit status 1")})
	d.press(eventsClosedMsg{})
	if !d.quit {
		t.Fatal("the screen should quit after a failure too")
	}
	if statusOf(model, builder.PhaseDownload) != phaseFailed || statusOf(model, builder.PhaseUpdateIndex) != phaseDone {
		t.Errorf("phase statuses = %v, want the running phase failed and the finished one done", model.phases)
	}
	if view := model.View(); !strings.Contains(view, "failed") {
		t.Errorf("view should mark the failure:\n%s", view)
	}
	if outcome := model.outcome(); outcome.Err == nil {
		t.Errorf("outcome = %+v, want the error", outcome)
	}
}

func TestProgressModelInterrupts(t *testing.T) {
	d, model, cancelCalls := newProgressDriver(t, sampleScreen)
	d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
	d.press(tea.KeyMsg{Type: tea.KeyCtrlC})
	if *cancelCalls != 1 || d.quit {
		t.Fatalf("first Ctrl-C: cancel called %d times, quit %v; want the work canceled and the screen kept", *cancelCalls, d.quit)
	}
	if view := model.View(); !strings.Contains(view, "interrupting: waiting for mmdebstrap to stop") || !strings.Contains(view, "Ctrl-C again") {
		t.Errorf("view should explain the wait:\n%s", view)
	}
	d.press(doneMsg{err: context.Canceled})
	d.press(eventsClosedMsg{})
	if !d.quit {
		t.Fatal("the screen should quit when the interrupted work has finished")
	}
	if outcome := model.outcome(); !outcome.Interrupted || outcome.Abandoned || !errors.Is(outcome.Err, context.Canceled) {
		t.Errorf("outcome = %+v, want interrupted with the error", outcome)
	}
}

func TestProgressModelAbandonsOnSecondCtrlC(t *testing.T) {
	d, model, cancelCalls := newProgressDriver(t, sampleScreen)
	d.press(tea.KeyMsg{Type: tea.KeyCtrlC})
	d.press(tea.KeyMsg{Type: tea.KeyCtrlC})
	if *cancelCalls != 1 || !d.quit {
		t.Fatalf("second Ctrl-C: cancel called %d times, quit %v; want one cancel and an immediate quit", *cancelCalls, d.quit)
	}
	if outcome := model.outcome(); !outcome.Abandoned || !outcome.Interrupted {
		t.Errorf("outcome = %+v, want abandoned", outcome)
	}
}

func TestProgressModelLogPaneKeysAndResize(t *testing.T) {
	d, model, _ := newProgressDriver(t, sampleScreen)
	for lineNumber := range 30 {
		d.press(eventMsg(builder.ProgressEvent{Kind: builder.EventLogLine, Line: strings.Repeat("x", 10) + string(rune('a'+lineNumber%26))}))
	}
	if !strings.Contains(model.View(), "xxxxxxxxxxd") { // the 30th line (index 29 -> 'd')
		t.Error("the pane should follow the newest line")
	}
	collapsedHeight := model.logView.Height
	d.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if model.logView.Height <= collapsedHeight {
		t.Errorf("l should grow the pane: %d -> %d", collapsedHeight, model.logView.Height)
	}
	d.press(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("l")})
	if model.logView.Height != collapsedHeight {
		t.Errorf("l again should restore the pane: %d", model.logView.Height)
	}
	d.press(tea.WindowSizeMsg{Width: 40, Height: 12})
	view := model.View()
	if view == "" || model.logView.Height < minimumLogHeight {
		t.Errorf("a tiny terminal must still render something sane: height %d", model.logView.Height)
	}
}

func TestFormatDuration(t *testing.T) {
	testCases := map[time.Duration]string{0: "0s", 12 * time.Second: "12s", 62 * time.Second: "1:02", 3723 * time.Second: "1:02:03", -time.Second: "0s"}
	for duration, want := range testCases {
		if got := formatDuration(duration); got != want {
			t.Errorf("formatDuration(%v) = %q, want %q", duration, got, want)
		}
	}
}

func TestRunProgressEndToEnd(t *testing.T) {
	events := make(chan builder.ProgressEvent, 16)
	done := make(chan error, 1)
	events <- builder.ProgressEvent{Phase: builder.PhaseUpdateIndex, Kind: builder.EventPhaseStarted}
	events <- builder.ProgressEvent{Phase: builder.PhaseUpdateIndex, Kind: builder.EventPhaseFinished}
	close(events)
	done <- nil
	finished := make(chan struct{})
	var outcome Outcome
	var err error
	go func() {
		defer close(finished)
		// The input never delivers a key: the screen must end on its own.
		outcome, err = RunProgress(sampleScreen, events, done, func() {}, neverReader{}, io.Discard)
	}()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("RunProgress did not return after the work finished")
	}
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Abandoned || outcome.Err != nil {
		t.Errorf("outcome = %+v", outcome)
	}
}

// neverReader blocks forever, like a terminal nobody types on.
type neverReader struct{}

func (neverReader) Read([]byte) (int, error) { select {} }
