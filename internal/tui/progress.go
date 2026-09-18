package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"frostroot/internal/builder"
)

// Screen describes a progress screen: what a long-running command shows
// while it works. build and vendor each fill one in.
type Screen struct {
	Title    string          // "frostroot build"
	Subtitle string          // what is being built or fetched, and from where
	Phases   []builder.Phase // the rows of the checklist, in order
	LogTitle string          // label of the log pane: whose output it shows
}

// BuildScreen returns the screen of a build.
func BuildScreen(imageName, release, suite, arch, source string, phases []builder.Phase) Screen {
	return Screen{
		Title:    "frostroot build",
		Subtitle: fmt.Sprintf("%s · Ubuntu %s (%s, %s) · %s", imageName, release, suite, arch, source),
		Phases:   phases,
		LogTitle: "mmdebstrap",
	}
}

// Outcome is how the work behind a screen ended, as far as the screen saw.
type Outcome struct {
	Err error // what the work reported, or nil
	// Interrupted means the user pressed Ctrl-C and the work was canceled;
	// Err describes how it stopped.
	Interrupted bool
	// Abandoned means the user pressed Ctrl-C a second time and the screen
	// closed before the work finished, so Err is unknown.
	Abandoned bool
}

// RunProgress shows the screen until the work finishes. events carries the
// progress and is closed when the work is over; done then delivers its
// error, or nil. cancel is called on the first Ctrl-C; the screen stays up
// until the canceled work has finished cleaning up, unless Ctrl-C is pressed
// again.
func RunProgress(screen Screen, events <-chan builder.ProgressEvent, done <-chan error, cancel func(), input io.Reader, output io.Writer) (Outcome, error) {
	model := newProgressModel(screen, events, done, cancel)
	program := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return Outcome{}, fmt.Errorf("running the progress screen: %w", err)
	}
	finished, isProgressModel := finalModel.(*progressModel)
	if !isProgressModel {
		return Outcome{}, fmt.Errorf("running the progress screen: unexpected model %T", finalModel)
	}
	return finished.outcome(), nil
}

// phaseStatus is where one phase stands.
type phaseStatus int

const (
	phasePending phaseStatus = iota
	phaseRunning
	phaseDone
	phaseFailed
)

// progressSample is one measured point of a phase counted in bytes, for
// the rate.
type progressSample struct {
	at   time.Time
	done int64
}

// phaseState is everything the screen knows about one phase.
type phaseState struct {
	status      phaseStatus
	startedAt   time.Time
	finishedAt  time.Time
	progress    builder.ProgressEvent // the last EventProgress
	hasProgress bool
	samples     []progressSample // byte progress over at least the last rateWindow
}

// Layout constants, in cells and rows.
const (
	defaultWidth       = 80
	defaultHeight      = 24
	phaseTitleWidth    = 40 // at most; half the terminal when that is less
	barWidth           = 20
	durationWidth      = 7 // "1:02:03"
	collapsedLogHeight = 8
	minimumLogHeight   = 3
	minimumLogWidth    = 10
	maxLogLines        = 500
	clockInterval      = time.Second
	// rateWindow is how far back the rate of a byte phase looks at least,
	// and rateSettle how much has to have been measured before a rate is
	// shown: a number from two seconds of downloading is not worth
	// believing.
	rateWindow = 10 * time.Second
	rateSettle = 5 * time.Second
)

// Messages the model receives besides Bubble Tea's own.
type (
	eventMsg        builder.ProgressEvent
	eventsClosedMsg struct{}
	doneMsg         struct{ err error }
	clockMsg        time.Time
)

// progressModel is the progress screen.
type progressModel struct {
	screen Screen
	events <-chan builder.ProgressEvent
	done   <-chan error
	cancel func()
	glyphs glyphSet

	phases    []phaseState          // one per screen.Phases
	rows      map[builder.Phase]int // phase to its index in phases
	spinner   spinner.Model
	bar       progress.Model
	logLines  []string
	logView   viewport.Model
	logGrown  bool // l pressed: the log pane takes the free height
	logPath   string
	width     int
	height    int
	startedAt time.Time
	now       time.Time

	result       *error
	eventsClosed bool
	interrupting bool
	abandoned    bool
}

func newProgressModel(screen Screen, events <-chan builder.ProgressEvent, done <-chan error, cancel func()) *progressModel {
	now := time.Now()
	glyphs := glyphsForTerminal()
	bar := progress.New(progress.WithWidth(barWidth), progress.WithoutPercentage(), progress.WithDefaultGradient())
	bar.Full, bar.Empty = glyphs.barFull, glyphs.barEmpty
	model := &progressModel{
		screen:    screen,
		events:    events,
		done:      done,
		cancel:    cancel,
		glyphs:    glyphs,
		phases:    make([]phaseState, len(screen.Phases)),
		rows:      make(map[builder.Phase]int, len(screen.Phases)),
		spinner:   spinner.New(spinner.WithSpinner(glyphs.spinner), spinner.WithStyle(runningStyle)),
		bar:       bar,
		logView:   viewport.New(defaultWidth-4, collapsedLogHeight),
		width:     defaultWidth,
		height:    defaultHeight,
		startedAt: now,
		now:       now,
	}
	for index, phase := range screen.Phases {
		model.rows[phase] = index
	}
	return model
}

// outcome returns how the work ended, as far as the screen saw it.
func (m *progressModel) outcome() Outcome {
	if m.result == nil {
		return Outcome{Abandoned: true, Interrupted: m.interrupting}
	}
	return Outcome{Err: *m.result, Interrupted: m.interrupting}
}

// Init implements tea.Model.
func (m *progressModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.waitForEvent(), m.waitForDone(), tickClock())
}

// waitForEvent delivers the next progress event, or says the channel closed.
func (m *progressModel) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		event, open := <-m.events
		if !open {
			return eventsClosedMsg{}
		}
		return eventMsg(event)
	}
}

// waitForDone delivers the work's outcome.
func (m *progressModel) waitForDone() tea.Cmd {
	return func() tea.Msg { return doneMsg{err: <-m.done} }
}

func tickClock() tea.Cmd {
	return tea.Tick(clockInterval, func(now time.Time) tea.Msg { return clockMsg(now) })
}

// Update implements tea.Model.
func (m *progressModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeLog()
	case tea.KeyMsg:
		return m.handleKey(msg)
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case clockMsg:
		m.now = time.Time(msg)
		if m.finished() {
			return m, nil
		}
		return m, tickClock()
	case eventMsg:
		m.apply(builder.ProgressEvent(msg))
		return m, m.waitForEvent()
	case eventsClosedMsg:
		m.eventsClosed = true
		return m, m.quitIfFinished()
	case doneMsg:
		err := msg.err
		m.result = &err
		m.closePhases(err != nil)
		return m, m.quitIfFinished()
	}
	return m, nil
}

// finished reports whether the work is over and every event has been seen.
func (m *progressModel) finished() bool { return m.result != nil && m.eventsClosed }

func (m *progressModel) quitIfFinished() tea.Cmd {
	if m.finished() {
		return tea.Quit
	}
	return nil
}

func (m *progressModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		if m.interrupting || m.finished() {
			m.abandoned = true
			return m, tea.Quit
		}
		m.interrupting = true
		m.cancel()
		return m, nil
	case msg.String() == "l":
		m.logGrown = !m.logGrown
		m.resizeLog()
		return m, nil
	}
	var cmd tea.Cmd
	m.logView, cmd = m.logView.Update(msg)
	return m, cmd
}

// apply updates the phase table and the log from one event.
func (m *progressModel) apply(event builder.ProgressEvent) {
	switch event.Kind {
	case builder.EventLogFile:
		m.logPath = event.Line
		return
	case builder.EventLogLine:
		m.appendLog(event.Line)
		return
	case builder.EventPhaseStarted, builder.EventProgress, builder.EventPhaseFinished:
	}
	row, shown := m.rows[event.Phase]
	if !shown {
		return // a phase this screen does not list; the log still shows it
	}
	state := &m.phases[row]
	switch event.Kind {
	case builder.EventPhaseStarted:
		state.status = phaseRunning
		state.startedAt = m.now
	case builder.EventProgress:
		state.progress = event
		state.hasProgress = true
		if event.Unit == builder.UnitBytes {
			state.samples = recentSamples(append(state.samples, progressSample{at: m.now, done: event.Done}), m.now)
		}
	case builder.EventPhaseFinished:
		if state.status != phaseFailed {
			state.status = phaseDone
			state.finishedAt = m.now
			if state.startedAt.IsZero() {
				state.startedAt = m.now
			}
		}
	case builder.EventLogLine, builder.EventLogFile:
	}
}

// recentSamples drops the oldest samples while the ones left still reach
// back at least rateWindow, so that the rate is measured over that long
// even when progress arrives seldom.
func recentSamples(samples []progressSample, now time.Time) []progressSample {
	first := 0
	for first+1 < len(samples) && now.Sub(samples[first+1].at) > rateWindow {
		first++
	}
	return samples[first:]
}

// throughput returns the bytes per second the samples show, or false when
// they do not span rateSettle yet or show no progress.
func throughput(samples []progressSample) (float64, bool) {
	if len(samples) < 2 {
		return 0, false
	}
	first, last := samples[0], samples[len(samples)-1]
	elapsed := last.at.Sub(first.at)
	if elapsed < rateSettle || last.done <= first.done {
		return 0, false
	}
	return float64(last.done-first.done) / elapsed.Seconds(), true
}

// closePhases settles the running phase when the work ends: failed when the
// work did, done otherwise.
func (m *progressModel) closePhases(failed bool) {
	for index := range m.phases {
		state := &m.phases[index]
		if state.status != phaseRunning {
			continue
		}
		state.finishedAt = m.now
		if failed {
			state.status = phaseFailed
		} else {
			state.status = phaseDone
		}
	}
}

// appendLog adds a line to the pane, keeping the view at the bottom when it
// was there.
func (m *progressModel) appendLog(line string) {
	followTail := m.logView.AtBottom()
	m.logLines = append(m.logLines, line)
	if len(m.logLines) > maxLogLines {
		m.logLines = m.logLines[len(m.logLines)-maxLogLines:]
	}
	m.setLogContent()
	if followTail {
		m.logView.GotoBottom()
	}
}

// setLogContent gives the pane its lines, each cut to the pane's width so
// that none wraps.
func (m *progressModel) setLogContent() {
	m.logView.SetContent(fitLines(strings.Join(m.logLines, "\n"), m.logView.Width, m.glyphs.ellipsis))
}

// resizeLog fits the log pane to the terminal.
func (m *progressModel) resizeLog() {
	m.logView.Width = max(minimumLogWidth, m.width-4)
	fixedRows := 2 + len(m.phases) + 1 + 2 + 2 // header, phases, gap, box frame, footer
	free := m.height - fixedRows
	height := collapsedLogHeight
	if m.logGrown {
		height = free
	}
	m.logView.Height = max(minimumLogHeight, min(height, max(minimumLogHeight, free)))
	m.setLogContent()
	m.logView.GotoBottom()
}

// titleWidth is the width of the phase column: the usual, or half the
// terminal when that is narrower, so the detail keeps some room.
func (m *progressModel) titleWidth() int {
	return min(phaseTitleWidth, m.width/2)
}

// Styles.
var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle     = lipgloss.NewStyle().Faint(true)
	doneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	failedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	warningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
)

// View implements tea.Model.
func (m *progressModel) View() string {
	var view strings.Builder
	header := titleStyle.Render(m.screen.Title) + "   " + m.screen.Subtitle
	view.WriteString(truncateWith(header, m.width, m.glyphs.ellipsis))
	view.WriteString("\n\n")
	for index, phase := range m.screen.Phases {
		view.WriteString(m.phaseLine(phase, m.phases[index]))
		view.WriteByte('\n')
	}
	view.WriteByte('\n')
	view.WriteString(titledPane(m.glyphs, m.screen.LogTitle, m.logView.Width, m.logView.View()))
	view.WriteByte('\n')
	view.WriteString(truncateWith(m.footer(), m.width, m.glyphs.ellipsis))
	return view.String()
}

// phaseLine renders one row of the phase table: icon, title, detail, and
// the duration in a column at the right edge.
func (m *progressModel) phaseLine(phase builder.Phase, state phaseState) string {
	titleWidth := m.titleWidth()
	// What the detail may take: the row, less the icon and title columns
	// and the duration column with its gap.
	detailWidth := m.width - 2 - 1 - 2 - titleWidth - 1 - durationWidth - 1
	icon, title := dimStyle.Render(m.glyphs.pending), dimStyle.Render(m.padRight(phase.Title(), titleWidth))
	detail, duration := "", ""
	switch state.status {
	case phasePending:
	case phaseRunning:
		// The Dot spinner's frames carry a trailing space; the column
		// does not want it.
		icon, title = strings.TrimRight(m.spinner.View(), " "), m.padRight(phase.Title(), titleWidth)
		detail = m.progressDetail(state, detailWidth)
		duration = formatDuration(m.now.Sub(state.startedAt))
	case phaseDone:
		icon, title = doneStyle.Render(m.glyphs.done), m.padRight(phase.Title(), titleWidth)
		detail = m.finishedDetail(state)
		duration = formatDuration(state.finishedAt.Sub(state.startedAt))
	case phaseFailed:
		icon, title = failedStyle.Render(m.glyphs.failed), m.padRight(phase.Title(), titleWidth)
		detail = failedStyle.Render("failed")
		duration = formatDuration(state.finishedAt.Sub(state.startedAt))
	}
	line := fmt.Sprintf("  %s  %s %s", icon, title, detail)
	if duration != "" {
		// A space always separates the detail from the duration, even when
		// the detail was cut to fit.
		line = m.padRight(line, m.width-durationWidth-1) + " " + dimStyle.Render(padLeft(duration, durationWidth))
	}
	return line
}

// progressDetail renders the bar and the words for a running phase, and the
// rate and the time left once a byte phase has been measured for long
// enough to say. The words give way first when width is short: the bar and
// the percentage are the part worth keeping.
func (m *progressModel) progressDetail(state phaseState, width int) string {
	if !state.hasProgress {
		return ""
	}
	percent, known := state.progress.Percent()
	if !known {
		return dimStyle.Render(truncateWith(state.progress.Summary(), width, m.glyphs.ellipsis))
	}
	words := state.progress.Summary()
	if state.progress.Unit == builder.UnitBytes {
		if perSecond, measured := throughput(state.samples); measured {
			left := time.Duration(float64(state.progress.Total-state.progress.Done)/perSecond) * time.Second
			words += fmt.Sprintf(" · %s/s · %s left", builder.FormatBytes(int64(perSecond)), formatDuration(left))
		}
	}
	// The percentage is four cells (" 68%"); with the bar and its gaps the
	// measure is 26, which is exactly what an 80-column terminal leaves.
	measure := fmt.Sprintf("%s %3.0f%% ", m.bar.ViewAs(percent/100), percent)
	if lipgloss.Width(measure) > width {
		// Too narrow for the bar: the number alone still says it.
		measure = fmt.Sprintf("%3.0f%% ", percent)
	}
	words = truncateWith(words, width-lipgloss.Width(measure), m.glyphs.ellipsis)
	return measure + dimStyle.Render(words)
}

// finishedDetail says what a finished measured phase did: "28.1 MB", "326 files".
func (m *progressModel) finishedDetail(state phaseState) string {
	if !state.hasProgress {
		return ""
	}
	switch state.progress.Unit {
	case builder.UnitBytes:
		return dimStyle.Render(builder.FormatBytes(max(state.progress.Done, state.progress.Total)))
	case builder.UnitFiles:
		return dimStyle.Render(state.progress.Summary())
	case builder.UnitSteps, builder.UnitNone:
	}
	return ""
}

// footer renders the last line: elapsed time, the keys, and where the log
// is, in that order so that a narrow terminal loses the path rather than
// the keys; or the interruption notice.
func (m *progressModel) footer() string {
	elapsed := formatDuration(m.now.Sub(m.startedAt))
	if m.interrupting && !m.finished() {
		return warningStyle.Render(fmt.Sprintf("  %s elapsed · interrupting: waiting for %s to stop (Ctrl-C again to stop waiting)", elapsed, m.screen.LogTitle))
	}
	parts := []string{elapsed + " elapsed", "Ctrl-C interrupts", "l grows the log"}
	if m.logPath != "" {
		// The path gives way from the left, so that its file name and the
		// keys before it are always there; when not even the file name
		// fits, the log is left out rather than shown as "…g".
		room := m.width - lipgloss.Width("  "+strings.Join(parts, " · ")+" · log: ")
		fileName := m.logPath[strings.LastIndex(m.logPath, "/")+1:]
		if room >= lipgloss.Width(m.glyphs.ellipsis)+lipgloss.Width(fileName) {
			parts = append(parts, "log: "+shortenLeft(m.logPath, room, m.glyphs.ellipsis))
		}
	}
	return dimStyle.Render("  " + strings.Join(parts, " · "))
}

// formatDuration renders a duration as "12s", "1:02" or "1:02:03".
func formatDuration(duration time.Duration) string {
	seconds := int(duration.Round(time.Second).Seconds())
	if seconds < 0 {
		seconds = 0
	}
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
	default:
		return fmt.Sprintf("%d:%02d:%02d", seconds/3600, seconds%3600/60, seconds%60)
	}
}

// padRight pads text with spaces to width cells, or cuts it with the
// screen's ellipsis.
func (m *progressModel) padRight(text string, width int) string {
	textWidth := lipgloss.Width(text)
	if textWidth > width {
		return truncateWith(text, width, m.glyphs.ellipsis)
	}
	return text + strings.Repeat(" ", width-textWidth)
}

// padLeft pads text with spaces on the left to width cells.
func padLeft(text string, width int) string {
	textWidth := lipgloss.Width(text)
	if textWidth > width {
		return text
	}
	return strings.Repeat(" ", width-textWidth) + text
}
