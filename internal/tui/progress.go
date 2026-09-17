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

// phaseState is everything the screen knows about one phase.
type phaseState struct {
	status      phaseStatus
	startedAt   time.Time
	finishedAt  time.Time
	progress    builder.ProgressEvent // the last EventProgress
	hasProgress bool
}

// Layout constants, in cells and rows.
const (
	defaultWidth       = 80
	defaultHeight      = 24
	phaseTitleWidth    = 40
	barWidth           = 20
	collapsedLogHeight = 8
	minimumLogHeight   = 3
	maxLogLines        = 500
	clockInterval      = time.Second
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
	model := &progressModel{
		screen:    screen,
		events:    events,
		done:      done,
		cancel:    cancel,
		phases:    make([]phaseState, len(screen.Phases)),
		rows:      make(map[builder.Phase]int, len(screen.Phases)),
		spinner:   spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(runningStyle)),
		bar:       progress.New(progress.WithWidth(barWidth), progress.WithoutPercentage(), progress.WithDefaultGradient()),
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
	m.logView.SetContent(strings.Join(m.logLines, "\n"))
	if followTail {
		m.logView.GotoBottom()
	}
}

// resizeLog fits the log pane to the terminal.
func (m *progressModel) resizeLog() {
	m.logView.Width = max(10, m.width-4)
	fixedRows := 2 + len(m.phases) + 1 + 2 + 2 // header, phases, gap, box frame, footer
	free := m.height - fixedRows
	height := collapsedLogHeight
	if m.logGrown {
		height = free
	}
	m.logView.Height = max(minimumLogHeight, min(height, max(minimumLogHeight, free)))
	m.logView.SetContent(strings.Join(m.logLines, "\n"))
	m.logView.GotoBottom()
}

// Styles.
var (
	titleStyle   = lipgloss.NewStyle().Bold(true)
	dimStyle     = lipgloss.NewStyle().Faint(true)
	doneStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("2"))
	failedStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("1"))
	runningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("5"))
	warningStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("3"))
	paneStyle    = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("8")).Padding(0, 1)
)

// View implements tea.Model.
func (m *progressModel) View() string {
	var view strings.Builder
	fmt.Fprintf(&view, "%s   %s\n\n", titleStyle.Render(m.screen.Title), m.screen.Subtitle)
	for index, phase := range m.screen.Phases {
		view.WriteString(m.phaseLine(phase, m.phases[index]))
		view.WriteByte('\n')
	}
	view.WriteByte('\n')
	pane := paneStyle.Width(m.logView.Width).Render(m.logView.View())
	pane = strings.Replace(pane, "╭─", "╭─ "+dimStyle.Render(m.screen.LogTitle)+" ", 1)
	view.WriteString(pane)
	view.WriteByte('\n')
	view.WriteString(m.footer())
	return view.String()
}

// phaseLine renders one row of the phase table.
func (m *progressModel) phaseLine(phase builder.Phase, state phaseState) string {
	icon, title := dimStyle.Render("·"), dimStyle.Render(padRight(phase.Title(), phaseTitleWidth))
	detail, duration := "", ""
	switch state.status {
	case phasePending:
	case phaseRunning:
		icon, title = m.spinner.View(), padRight(phase.Title(), phaseTitleWidth)
		detail = m.progressDetail(state)
		duration = formatDuration(m.now.Sub(state.startedAt))
	case phaseDone:
		icon, title = doneStyle.Render("✓"), padRight(phase.Title(), phaseTitleWidth)
		detail = m.finishedDetail(state)
		duration = formatDuration(state.finishedAt.Sub(state.startedAt))
	case phaseFailed:
		icon, title = failedStyle.Render("✗"), padRight(phase.Title(), phaseTitleWidth)
		detail = failedStyle.Render("failed")
		duration = formatDuration(state.finishedAt.Sub(state.startedAt))
	}
	line := fmt.Sprintf("  %s  %s %s", icon, title, detail)
	if duration != "" {
		line = padRight(line, m.width-8) + dimStyle.Render(duration)
	}
	return line
}

// progressDetail renders the bar and the words for a running phase.
func (m *progressModel) progressDetail(state phaseState) string {
	if !state.hasProgress {
		return ""
	}
	percent, known := state.progress.Percent()
	if !known {
		return dimStyle.Render(state.progress.Summary())
	}
	return fmt.Sprintf("%s %3.0f%%  %s", m.bar.ViewAs(percent/100), percent, dimStyle.Render(truncate(state.progress.Summary(), m.width-phaseTitleWidth-barWidth-22)))
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

// footer renders the last line: elapsed time, log location and keys, or the
// interruption notice.
func (m *progressModel) footer() string {
	elapsed := formatDuration(m.now.Sub(m.startedAt))
	if m.interrupting && !m.finished() {
		return warningStyle.Render(fmt.Sprintf("  %s elapsed · interrupting: waiting for %s to stop (Ctrl-C again to stop waiting)", elapsed, m.screen.LogTitle))
	}
	parts := []string{elapsed + " elapsed"}
	if m.logPath != "" {
		parts = append(parts, "log: "+m.logPath)
	}
	parts = append(parts, "Ctrl-C interrupts", "l grows the log")
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

// padRight pads text with spaces to width cells, or truncates it.
func padRight(text string, width int) string {
	textWidth := lipgloss.Width(text)
	if textWidth >= width {
		return truncate(text, width)
	}
	return text + strings.Repeat(" ", width-textWidth)
}

// truncate cuts text to at most width cells, ending with an ellipsis.
func truncate(text string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(text) <= width {
		return text
	}
	runes := []rune(text)
	if width == 1 {
		return "…"
	}
	return string(runes[:min(len(runes), width-1)]) + "…"
}
