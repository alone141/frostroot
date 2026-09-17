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

var sampleScreen = BuildScreen{ImageName: "cpp-lab", Release: "22.04", Suite: "jammy", Arch: "amd64", ArchiveURL: "http://archive.ubuntu.com/ubuntu"}

// newBuildDriver returns a driver around a build model whose channels never
// deliver anything; tests send events and the outcome as messages.
func newBuildDriver(t *testing.T) (*driver, *buildModel, *int) {
	t.Helper()
	cancelCalls := 0
	model := newBuildModel(sampleScreen, make(chan builder.ProgressEvent), make(chan BuildOutcome), func() { cancelCalls++ })
	d := newDriver(t, model)
	d.press(tea.WindowSizeMsg{Width: 100, Height: 40})
	return d, model, &cancelCalls
}

func event(phase builder.Phase, kind builder.EventKind) eventMsg {
	return eventMsg(builder.ProgressEvent{Phase: phase, Kind: kind})
}

func TestBuildModelShowsPhasesAndProgress(t *testing.T) {
	d, model, _ := newBuildDriver(t)
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
	if model.phases[builder.PhaseUpdateIndex].status != phaseDone || model.phases[builder.PhaseDownload].status != phaseRunning {
		t.Errorf("phase statuses = %v", model.phases)
	}
}

func TestBuildModelFinishes(t *testing.T) {
	d, model, _ := newBuildDriver(t)
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseStarted))
	d.press(eventsClosedMsg{})
	if d.quit {
		t.Fatal("the screen must wait for the outcome after the events end")
	}
	d.press(doneMsg(BuildOutcome{Result: builder.Result{InstalledPackageCount: 3}}))
	if !d.quit {
		t.Fatal("the screen should quit once the outcome and the last event are in")
	}
	if model.phases[builder.PhaseUpdateIndex].status != phaseDone {
		t.Errorf("a phase still running at success is done, got %v", model.phases[builder.PhaseUpdateIndex].status)
	}
	if outcome := model.outcome(); outcome.Abandoned || outcome.Interrupted || outcome.Result.InstalledPackageCount != 3 {
		t.Errorf("outcome = %+v", outcome)
	}
}

func TestBuildModelMarksTheFailedPhase(t *testing.T) {
	d, model, _ := newBuildDriver(t)
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseStarted))
	d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseFinished))
	d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
	d.press(doneMsg(BuildOutcome{Err: errors.New("mmdebstrap failed: exit status 1")}))
	d.press(eventsClosedMsg{})
	if !d.quit {
		t.Fatal("the screen should quit after a failure too")
	}
	if model.phases[builder.PhaseDownload].status != phaseFailed || model.phases[builder.PhaseUpdateIndex].status != phaseDone {
		t.Errorf("phase statuses = %v, want the running phase failed and the finished one done", model.phases)
	}
	if view := model.View(); !strings.Contains(view, "failed") {
		t.Errorf("view should mark the failure:\n%s", view)
	}
}

func TestBuildModelInterrupts(t *testing.T) {
	d, model, cancelCalls := newBuildDriver(t)
	d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
	d.press(tea.KeyMsg{Type: tea.KeyCtrlC})
	if *cancelCalls != 1 || d.quit {
		t.Fatalf("first Ctrl-C: cancel called %d times, quit %v; want the build canceled and the screen kept", *cancelCalls, d.quit)
	}
	if view := model.View(); !strings.Contains(view, "interrupting") || !strings.Contains(view, "Ctrl-C again") {
		t.Errorf("view should explain the wait:\n%s", view)
	}
	d.press(doneMsg(BuildOutcome{Err: context.Canceled, Result: builder.Result{WorkDir: "/w"}}))
	d.press(eventsClosedMsg{})
	if !d.quit {
		t.Fatal("the screen should quit when the interrupted build has finished")
	}
	if outcome := model.outcome(); !outcome.Interrupted || outcome.Abandoned || outcome.Result.WorkDir != "/w" {
		t.Errorf("outcome = %+v, want interrupted with the kept work directory", outcome)
	}
}

func TestBuildModelAbandonsOnSecondCtrlC(t *testing.T) {
	d, model, cancelCalls := newBuildDriver(t)
	d.press(tea.KeyMsg{Type: tea.KeyCtrlC})
	d.press(tea.KeyMsg{Type: tea.KeyCtrlC})
	if *cancelCalls != 1 || !d.quit {
		t.Fatalf("second Ctrl-C: cancel called %d times, quit %v; want one cancel and an immediate quit", *cancelCalls, d.quit)
	}
	if outcome := model.outcome(); !outcome.Abandoned || !outcome.Interrupted {
		t.Errorf("outcome = %+v, want abandoned", outcome)
	}
}

func TestBuildModelLogPaneKeysAndResize(t *testing.T) {
	d, model, _ := newBuildDriver(t)
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

func TestRunBuildEndToEnd(t *testing.T) {
	events := make(chan builder.ProgressEvent, 16)
	done := make(chan BuildOutcome, 1)
	events <- builder.ProgressEvent{Phase: builder.PhaseUpdateIndex, Kind: builder.EventPhaseStarted}
	events <- builder.ProgressEvent{Phase: builder.PhaseUpdateIndex, Kind: builder.EventPhaseFinished}
	close(events)
	done <- BuildOutcome{Result: builder.Result{InstalledPackageCount: 5}}
	finished := make(chan struct{})
	var outcome BuildOutcome
	var err error
	go func() {
		defer close(finished)
		// The input never delivers a key: the screen must end on its own.
		outcome, err = RunBuild(sampleScreen, events, done, func() {}, neverReader{}, io.Discard)
	}()
	select {
	case <-finished:
	case <-time.After(20 * time.Second):
		t.Fatal("RunBuild did not return after the build finished")
	}
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Abandoned || outcome.Result.InstalledPackageCount != 5 {
		t.Errorf("outcome = %+v", outcome)
	}
}

// neverReader blocks forever, like a terminal nobody types on.
type neverReader struct{}

func (neverReader) Read([]byte) (int, error) { select {} }
