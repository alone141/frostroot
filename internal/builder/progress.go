package builder

import (
	"bytes"
	"regexp"
	"strconv"
	"strings"
)

// Phase is one step of a build, in the order a build runs them and a
// progress display lists them. The first seven are mmdebstrap's; the last two
// are frostroot's own.
type Phase int

// The phases of a build.
const (
	PhaseUpdateIndex Phase = iota
	PhaseDownload
	PhaseExtract
	PhaseInstallEssential
	PhaseInstallRequested
	PhaseProvision
	PhaseCreateTarball
	PhaseWriteLock
	PhasePlaceTarball
	phaseCount
)

var phaseTitles = [phaseCount]string{
	PhaseUpdateIndex:      "Update package index",
	PhaseDownload:         "Download packages",
	PhaseExtract:          "Extract archives",
	PhaseInstallEssential: "Install essential packages",
	PhaseInstallRequested: "Install requested packages",
	PhaseProvision:        "Provision user, locale and timezone",
	PhaseCreateTarball:    "Create tarball",
	PhaseWriteLock:        "Write frostroot.lock",
	PhasePlaceTarball:     "Place tarball",
}

// Title returns the phase's name as a progress display shows it.
func (p Phase) Title() string {
	if p < 0 || p >= phaseCount {
		return "Unknown phase"
	}
	return phaseTitles[p]
}

// String returns the phase's title.
func (p Phase) String() string { return p.Title() }

// Phases returns every phase in build order.
func Phases() []Phase {
	phases := make([]Phase, 0, phaseCount)
	for phase := Phase(0); phase < phaseCount; phase++ {
		phases = append(phases, phase)
	}
	return phases
}

// EventKind says what a ProgressEvent reports.
type EventKind int

// The kinds of progress event.
const (
	// EventPhaseStarted means the phase has begun.
	EventPhaseStarted EventKind = iota
	// EventProgress carries Done and Total for the running phase. Total is
	// zero while the amount of work is unknown.
	EventProgress
	// EventLogLine carries one line of the bootstrap's output, attributed to
	// the phase running when it appeared.
	EventLogLine
	// EventPhaseFinished means the phase completed. A phase that finishes
	// without having started was skipped; it still counts as done.
	EventPhaseFinished
)

// Unit is what Done and Total count.
type Unit int

// The units progress is measured in.
const (
	UnitNone  Unit = iota
	UnitBytes      // bytes downloaded or copied
	UnitFiles      // index files fetched; the total is never known
	UnitSteps      // dpkg steps: every package is unpacked, then set up
)

// ProgressEvent is one report from a running build.
type ProgressEvent struct {
	Phase Phase
	Kind  EventKind
	Done  int64 // EventProgress: work done so far
	Total int64 // EventProgress: total work, or zero when unknown
	Unit  Unit  // EventProgress: what Done and Total count
	// Line is the output line for EventLogLine, and for EventProgress a short
	// description of the current step, such as "Setting up passwd", when the
	// output offers one.
	Line string
}

// Percent returns how far the work has got, when the total is known.
func (event ProgressEvent) Percent() (percent float64, known bool) {
	if event.Kind != EventProgress || event.Total <= 0 {
		return 0, false
	}
	return min(100, 100*float64(event.Done)/float64(event.Total)), true
}

// Summary describes the progress in words: "12.6 MB / 28.1 MB", "9 files",
// "Setting up passwd".
func (event ProgressEvent) Summary() string {
	switch event.Unit {
	case UnitBytes:
		if event.Total > 0 {
			return FormatBytes(event.Done) + " / " + FormatBytes(event.Total)
		}
		return FormatBytes(event.Done)
	case UnitFiles:
		if event.Done == 1 {
			return "1 file"
		}
		return strconv.FormatInt(event.Done, 10) + " files"
	case UnitSteps:
		return event.Line
	case UnitNone:
	}
	return event.Line
}

// FormatBytes formats a size the way apt does: SI units, three significant
// digits ("3716 B", "51.0 kB", "28.1 MB", "1.20 GB").
func FormatBytes(count int64) string {
	value := float64(count)
	for _, unit := range []string{"B", "kB", "MB", "GB", "TB"} {
		if value < 1000 || unit == "TB" {
			switch {
			case unit == "B":
				return strconv.FormatInt(count, 10) + " B"
			case value < 10:
				return strconv.FormatFloat(value, 'f', 2, 64) + " " + unit
			case value < 100:
				return strconv.FormatFloat(value, 'f', 1, 64) + " " + unit
			default:
				return strconv.FormatFloat(value, 'f', 0, 64) + " " + unit
			}
		}
		value /= 1000
	}
	return strconv.FormatInt(count, 10) + " B" // unreachable: the loop returns at TB
}

// Progress receives the events of a build as they happen. Implementations
// must not block for long: they are called from the goroutine reading the
// bootstrap's output.
type Progress interface {
	Report(event ProgressEvent)
}

// ProgressFunc adapts a function to the Progress interface.
type ProgressFunc func(event ProgressEvent)

// Report calls the function.
func (report ProgressFunc) Report(event ProgressEvent) { report(event) }

// discardProgress ignores every event.
type discardProgress struct{}

func (discardProgress) Report(ProgressEvent) {}

// progressOrDiscard returns progress, or a Progress that ignores events when
// progress is nil.
func progressOrDiscard(progress Progress) Progress {
	if progress == nil {
		return discardProgress{}
	}
	return progress
}

// Patterns for the apt lines that carry measurable progress. Sizes are as apt
// prints them, in SI units: "[3264 kB]", "[15.0 MB]", "[3716 B]".
var (
	aptFetchSizePattern      = regexp.MustCompile(`\[([0-9][0-9.,]*) ([kMG]?B)\]$`)
	aptNeedToGetPattern      = regexp.MustCompile(`^Need to get ([0-9][0-9.,]*) ([kMG]?B)`)
	aptNewlyInstalledPattern = regexp.MustCompile(`([0-9]+) newly installed`)
)

// bytesPerAptUnit maps apt's size suffixes to bytes.
var bytesPerAptUnit = map[string]float64{"B": 1, "kB": 1e3, "MB": 1e6, "GB": 1e9}

// progressParser turns mmdebstrap's --verbose output into ProgressEvents. It
// is an io.Writer so it can sit beside the log file in an io.MultiWriter; it
// buffers partial lines between writes.
//
// The recognizers are mmdebstrap's "I:" phase messages and apt's and dpkg's
// ordinary output. When a future version changes a message the parser
// degrades: a phase never announced is marked done when a later one starts,
// and a total never seen leaves the bar indeterminate. It never fails a build.
type progressParser struct {
	progress    Progress
	partialLine []byte

	current Phase // the running phase, valid when running is true
	running bool

	indexFiles         int64 // PhaseUpdateIndex: "Get:" lines seen
	downloadedBytes    int64 // PhaseDownload and PhaseInstallRequested: bytes fetched so far
	downloadTotalBytes int64 // ... out of this many, from "Need to get"
	essentialPackages  int64 // packages apt announced in PhaseDownload; the total of PhaseInstallEssential
	installSteps       int64 // dpkg steps done in the running install phase
	installTotalSteps  int64 // two per package, once apt has announced the count
}

func newProgressParser(progress Progress) *progressParser {
	return &progressParser{progress: progressOrDiscard(progress)}
}

// Write consumes bootstrap output. It never returns an error, so a parsing
// problem can never stop the build.
func (parser *progressParser) Write(data []byte) (int, error) {
	parser.partialLine = append(parser.partialLine, data...)
	for {
		newline := bytes.IndexByte(parser.partialLine, '\n')
		if newline < 0 {
			return len(data), nil
		}
		line := string(parser.partialLine[:newline])
		parser.partialLine = parser.partialLine[newline+1:]
		parser.handleLine(strings.TrimSuffix(line, "\r"))
	}
}

// flush handles a final line that had no newline.
func (parser *progressParser) flush() {
	if len(parser.partialLine) > 0 {
		line := string(parser.partialLine)
		parser.partialLine = nil
		parser.handleLine(strings.TrimSuffix(line, "\r"))
	}
}

func (parser *progressParser) handleLine(line string) {
	parser.progress.Report(ProgressEvent{Phase: parser.current, Kind: EventLogLine, Line: line})
	if message, isInfo := strings.CutPrefix(line, "I: "); isInfo {
		parser.handleInfoMessage(message)
		return
	}
	if !parser.running {
		return
	}
	switch parser.current {
	case PhaseUpdateIndex:
		if strings.HasPrefix(line, "Get:") {
			parser.indexFiles++
			parser.reportProgress(parser.indexFiles, 0, UnitFiles, "")
		}
	case PhaseDownload:
		if count, found := newlyInstalledCount(line); found {
			parser.essentialPackages = count
		}
		parser.handleAptLine(line)
	case PhaseInstallEssential:
		parser.handleDpkgLine(line)
	case PhaseInstallRequested:
		if count, found := newlyInstalledCount(line); found {
			parser.installTotalSteps = 2 * count
		}
		parser.handleAptLine(line)
		parser.handleDpkgLine(line)
	case PhaseExtract, PhaseProvision, PhaseCreateTarball, PhaseWriteLock, PhasePlaceTarball, phaseCount:
		// Nothing measurable in the output of these phases.
	}
}

// handleInfoMessage handles the text after "I: ".
func (parser *progressParser) handleInfoMessage(message string) {
	if message == "done" {
		parser.finishCurrent()
		return
	}
	if phase, isPhase := phaseOfInfoMessage(message); isPhase {
		parser.startPhase(phase)
	}
}

// phaseOfInfoMessage maps mmdebstrap's phase announcements to phases.
// Cleaning the apt cache is the first step of packing the tarball, so it
// counts as PhaseCreateTarball.
func phaseOfInfoMessage(message string) (Phase, bool) {
	switch {
	case message == "running apt-get update...":
		return PhaseUpdateIndex, true
	case message == "downloading packages with apt...":
		return PhaseDownload, true
	case message == "extracting archives...":
		return PhaseExtract, true
	case message == "installing essential packages...":
		return PhaseInstallEssential, true
	case message == "installing remaining packages inside the chroot...":
		return PhaseInstallRequested, true
	case strings.HasPrefix(message, "running special hook: "), strings.HasPrefix(message, "running --customize-hook"):
		return PhaseProvision, true
	case message == "cleaning package lists and apt cache...", message == "creating tarball...":
		return PhaseCreateTarball, true
	}
	return 0, false
}

// startPhase begins phase, finishing the running one first. Phases between
// them that were never announced are reported finished, so a display can
// mark them done rather than leave them pending forever. Announcements of a
// phase already passed are ignored.
func (parser *progressParser) startPhase(phase Phase) {
	if parser.running && phase <= parser.current {
		return
	}
	nextPhase := Phase(0)
	if parser.running {
		nextPhase = parser.current + 1
		parser.finishCurrent()
	}
	for skipped := nextPhase; skipped < phase; skipped++ {
		parser.progress.Report(ProgressEvent{Phase: skipped, Kind: EventPhaseFinished})
	}
	parser.current = phase
	parser.running = true
	parser.downloadedBytes, parser.downloadTotalBytes = 0, 0
	parser.installSteps = 0
	switch phase {
	case PhaseInstallEssential:
		parser.installTotalSteps = 2 * parser.essentialPackages
	default:
		parser.installTotalSteps = 0
	}
	parser.progress.Report(ProgressEvent{Phase: phase, Kind: EventPhaseStarted})
}

func (parser *progressParser) finishCurrent() {
	if !parser.running {
		return
	}
	parser.running = false
	parser.progress.Report(ProgressEvent{Phase: parser.current, Kind: EventPhaseFinished})
}

// handleAptLine tracks a download: "Need to get" announces the total, every
// "Get:" line adds what it fetched.
func (parser *progressParser) handleAptLine(line string) {
	if match := aptNeedToGetPattern.FindStringSubmatch(line); match != nil {
		if size, ok := parseAptSize(match[1], match[2]); ok {
			parser.downloadTotalBytes += size
			parser.reportProgress(parser.downloadedBytes, parser.downloadTotalBytes, UnitBytes, "")
		}
		return
	}
	if !strings.HasPrefix(line, "Get:") {
		return
	}
	match := aptFetchSizePattern.FindStringSubmatch(line)
	if match == nil {
		return
	}
	if size, ok := parseAptSize(match[1], match[2]); ok {
		parser.downloadedBytes += size
		parser.reportProgress(parser.downloadedBytes, parser.downloadTotalBytes, UnitBytes, "")
	}
}

// handleDpkgLine counts dpkg's two steps per package.
func (parser *progressParser) handleDpkgLine(line string) {
	if !strings.HasPrefix(line, "Unpacking ") && !strings.HasPrefix(line, "Setting up ") {
		return
	}
	parser.installSteps++
	parser.reportProgress(parser.installSteps, parser.installTotalSteps, UnitSteps, strings.TrimSuffix(line, " ..."))
}

func (parser *progressParser) reportProgress(done, total int64, unit Unit, step string) {
	parser.progress.Report(ProgressEvent{Phase: parser.current, Kind: EventProgress, Done: done, Total: total, Unit: unit, Line: step})
}

// newlyInstalledCount reads N from apt's "0 upgraded, N newly installed, ..."
// summary.
func newlyInstalledCount(line string) (int64, bool) {
	match := aptNewlyInstalledPattern.FindStringSubmatch(line)
	if match == nil {
		return 0, false
	}
	count, err := strconv.ParseInt(match[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return count, true
}

// parseAptSize converts apt's "28.1" + "MB" into bytes. apt prints
// thousands separators in some locales, so commas are ignored.
func parseAptSize(amount, unit string) (int64, bool) {
	value, err := strconv.ParseFloat(strings.ReplaceAll(amount, ",", ""), 64)
	if err != nil {
		return 0, false
	}
	multiplier, known := bytesPerAptUnit[unit]
	if !known {
		return 0, false
	}
	return int64(value * multiplier), true
}
