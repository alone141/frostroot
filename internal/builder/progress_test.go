package builder

import (
	"os"
	"path/filepath"
	"testing"
)

// recordingProgress keeps every event it receives.
type recordingProgress struct {
	events []ProgressEvent
}

func (r *recordingProgress) Report(event ProgressEvent) { r.events = append(r.events, event) }

// ofKind returns the recorded events of one kind.
func (r *recordingProgress) ofKind(kind EventKind) []ProgressEvent {
	var matching []ProgressEvent
	for _, event := range r.events {
		if event.Kind == kind {
			matching = append(matching, event)
		}
	}
	return matching
}

// lastProgressOf returns the last EventProgress reported for phase.
func (r *recordingProgress) lastProgressOf(t *testing.T, phase Phase) ProgressEvent {
	t.Helper()
	var last *ProgressEvent
	for index := range r.events {
		event := &r.events[index]
		if event.Kind == EventProgress && event.Phase == phase {
			last = event
		}
	}
	if last == nil {
		t.Fatalf("no progress event for %s", phase)
	}
	return *last
}

// parseFixture feeds testdata/mmdebstrap-verbose.log to a parser in chunks
// that split lines, the way a pipe delivers them.
func parseFixture(t *testing.T) *recordingProgress {
	t.Helper()
	stream, err := os.ReadFile(filepath.Join("testdata", "mmdebstrap-verbose.log"))
	if err != nil {
		t.Fatal(err)
	}
	recorder := &recordingProgress{}
	parser := newProgressParser(recorder)
	const chunkSize = 37 // deliberately not a line length
	for start := 0; start < len(stream); start += chunkSize {
		end := min(start+chunkSize, len(stream))
		if _, err := parser.Write(stream[start:end]); err != nil {
			t.Fatal(err)
		}
	}
	parser.flush()
	return recorder
}

func TestProgressParserPhasesInOrder(t *testing.T) {
	recorder := parseFixture(t)
	var started []Phase
	for _, event := range recorder.ofKind(EventPhaseStarted) {
		started = append(started, event.Phase)
	}
	wantStarted := []Phase{PhaseUpdateIndex, PhaseDownload, PhaseExtract, PhaseInstallEssential, PhaseInstallRequested, PhaseProvision, PhaseCreateTarball}
	if len(started) != len(wantStarted) {
		t.Fatalf("started phases = %v, want %v", started, wantStarted)
	}
	for index, phase := range wantStarted {
		if started[index] != phase {
			t.Errorf("started[%d] = %s, want %s", index, started[index], phase)
		}
	}
	var finished []Phase
	for _, event := range recorder.ofKind(EventPhaseFinished) {
		finished = append(finished, event.Phase)
	}
	if len(finished) != len(wantStarted) || finished[len(finished)-1] != PhaseCreateTarball {
		t.Errorf("finished phases = %v, want each started phase finished, ending with the tarball", finished)
	}
	// Every phase finishes before the next one starts.
	runningPhase, running := Phase(0), false
	for _, event := range recorder.events {
		switch event.Kind {
		case EventPhaseStarted:
			if running {
				t.Errorf("%s started while %s was running", event.Phase, runningPhase)
			}
			runningPhase, running = event.Phase, true
		case EventPhaseFinished:
			running = false
		case EventProgress, EventLogLine:
		}
	}
}

func TestProgressParserMeasuresDownloadsInBytes(t *testing.T) {
	recorder := parseFixture(t)
	download := recorder.lastProgressOf(t, PhaseDownload)
	// "Need to get 28.1 MB", then five Get: lines of 51.0 kB, 3264 kB,
	// 78.4 kB, 127 kB and 82.3 kB.
	if download.Unit != UnitBytes || download.Total != 28_100_000 || download.Done != 3_602_700 {
		t.Errorf("download progress = %+v, want 3602700 of 28100000 bytes", download)
	}
	index := recorder.lastProgressOf(t, PhaseUpdateIndex)
	if index.Unit != UnitFiles || index.Done != 9 || index.Total != 0 {
		t.Errorf("index progress = %+v, want 9 files of an unknown total", index)
	}
}

func TestProgressParserCountsInstallSteps(t *testing.T) {
	recorder := parseFixture(t)
	essential := recorder.lastProgressOf(t, PhaseInstallEssential)
	// apt announced 95 packages during the download; the excerpt unpacks five
	// and sets up one.
	if essential.Unit != UnitSteps || essential.Done != 6 || essential.Total != 190 {
		t.Errorf("essential progress = %+v, want 6 of 190 steps", essential)
	}
	if essential.Line != "Setting up bsdextrautils (2.39.3-9ubuntu6.6)" {
		t.Errorf("essential step = %q, want the last dpkg action", essential.Line)
	}
	requested := recorder.lastProgressOf(t, PhaseInstallRequested)
	// 163 packages announced; one unpacked and four set up in the excerpt.
	if requested.Unit != UnitSteps || requested.Done != 5 || requested.Total != 326 {
		t.Errorf("requested progress = %+v, want 5 of 326 steps", requested)
	}
	// The same phase downloaded first: 69.5 MB total, eight Get: lines.
	var requestedDownload *ProgressEvent
	for index := range recorder.events {
		event := &recorder.events[index]
		if event.Kind == EventProgress && event.Phase == PhaseInstallRequested && event.Unit == UnitBytes {
			requestedDownload = event
		}
	}
	if requestedDownload == nil || requestedDownload.Total != 69_500_000 || requestedDownload.Done != 1_177_600 {
		t.Errorf("requested download = %+v, want 1177600 of 69500000 bytes", requestedDownload)
	}
}

func TestProgressParserAttributesLogLines(t *testing.T) {
	recorder := parseFixture(t)
	lines := recorder.ofKind(EventLogLine)
	if len(lines) != 138 {
		t.Fatalf("log lines = %d, want every line of the fixture (138)", len(lines))
	}
	var localeLine *ProgressEvent
	for index := range lines {
		if lines[index].Line == "Generation complete." {
			localeLine = &lines[index]
		}
	}
	if localeLine == nil || localeLine.Phase != PhaseProvision {
		t.Errorf("locale generation line = %+v, want attributed to provisioning", localeLine)
	}
}

func TestProgressParserSkippedPhaseIsFinished(t *testing.T) {
	recorder := &recordingProgress{}
	parser := newProgressParser(recorder)
	_, _ = parser.Write([]byte("I: running apt-get update...\nI: installing essential packages...\n"))
	var kinds []string
	for _, event := range recorder.events {
		if event.Kind != EventLogLine {
			kinds = append(kinds, event.Phase.Title()+":"+kindName(event.Kind))
		}
	}
	want := []string{
		"Update package index:started", "Update package index:finished",
		"Download packages:finished", "Extract archives:finished",
		"Install essential packages:started",
	}
	if len(kinds) != len(want) {
		t.Fatalf("events = %q, want %q", kinds, want)
	}
	for index := range want {
		if kinds[index] != want[index] {
			t.Errorf("event %d = %q, want %q", index, kinds[index], want[index])
		}
	}
}

func TestProgressParserToleratesUnknownOutput(t *testing.T) {
	recorder := &recordingProgress{}
	parser := newProgressParser(recorder)
	_, _ = parser.Write([]byte("I: something new and unexpected...\nGet:1 http://x noble InRelease\nNeed to get lots\n"))
	if started := recorder.ofKind(EventPhaseStarted); len(started) != 0 {
		t.Errorf("unknown messages must not start a phase, got %+v", started)
	}
	if progress := recorder.ofKind(EventProgress); len(progress) != 0 {
		t.Errorf("no phase is running, so nothing may be measured, got %+v", progress)
	}
	if lines := recorder.ofKind(EventLogLine); len(lines) != 3 {
		t.Errorf("log lines = %d, want 3", len(lines))
	}
}

func TestProgressParserFlushesPartialLine(t *testing.T) {
	recorder := &recordingProgress{}
	parser := newProgressParser(recorder)
	_, _ = parser.Write([]byte("I: running apt-get update"))
	if len(recorder.events) != 0 {
		t.Fatalf("a partial line must wait for its newline, got %+v", recorder.events)
	}
	_, _ = parser.Write([]byte("...\r\nGet:1 http://x noble InRelease [256 kB]"))
	parser.flush()
	if progress := recorder.ofKind(EventProgress); len(progress) != 1 || progress[0].Done != 1 {
		t.Errorf("progress = %+v, want one index file after the flush", progress)
	}
	if lines := recorder.ofKind(EventLogLine); len(lines) != 2 || lines[0].Line != "I: running apt-get update..." {
		t.Errorf("log lines = %+v, want two, without the carriage return", lines)
	}
}

func TestParseAptSize(t *testing.T) {
	testCases := []struct {
		amount, unit string
		want         int64
		ok           bool
	}{
		{amount: "3716", unit: "B", want: 3716, ok: true},
		{amount: "51.0", unit: "kB", want: 51_000, ok: true},
		{amount: "15.0", unit: "MB", want: 15_000_000, ok: true},
		{amount: "1,234", unit: "kB", want: 1_234_000, ok: true},
		{amount: "2", unit: "GB", want: 2_000_000_000, ok: true},
		{amount: "x", unit: "kB", ok: false},
		{amount: "1", unit: "TB", ok: false},
	}
	for _, testCase := range testCases {
		got, ok := parseAptSize(testCase.amount, testCase.unit)
		if got != testCase.want || ok != testCase.ok {
			t.Errorf("parseAptSize(%q, %q) = %d, %v; want %d, %v", testCase.amount, testCase.unit, got, ok, testCase.want, testCase.ok)
		}
	}
}

func TestPhaseTitles(t *testing.T) {
	phases := Phases()
	if len(phases) != int(phaseCount) || phases[0] != PhaseUpdateIndex || phases[len(phases)-1] != PhasePlaceTarball {
		t.Errorf("Phases() = %v", phases)
	}
	for _, phase := range phases {
		if phase.Title() == "" || phase.Title() == "Unknown phase" {
			t.Errorf("%d has no title", int(phase))
		}
	}
	if got := Phase(99).Title(); got != "Unknown phase" {
		t.Errorf("Phase(99).Title() = %q", got)
	}
}

// kindName names an EventKind for test messages.
func kindName(kind EventKind) string {
	switch kind {
	case EventPhaseStarted:
		return "started"
	case EventProgress:
		return "progress"
	case EventLogLine:
		return "log"
	case EventPhaseFinished:
		return "finished"
	}
	return "unknown"
}
