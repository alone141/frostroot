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

// BuildScreen is what the build screen shows in its header.
type BuildScreen struct {
	ImageName  string
	Release    string // "22.04"
	Suite      string // "jammy"
	Arch       string
	ArchiveURL string
}

// BuildOutcome is how a build ended.
type BuildOutcome struct {
	Result builder.Result
	Err    error
	// Interrupted means the user pressed Ctrl-C and the build was canceled;
	// Result and Err describe how it stopped.
	Interrupted bool
	// Abandoned means the user pressed Ctrl-C a second time and the screen
	// closed before the build finished, so Result and Err are unknown.
	Abandoned bool
}

// RunBuild shows the build screen until the build finishes. events carries the
// builder's progress and is closed when the build is over; done then delivers
// the outcome. cancelBuild is called on the first Ctrl-C; the screen stays up
// until the canceled build has finished cleaning up, unless Ctrl-C is pressed
// again.
func RunBuild(screen BuildScreen, events <-chan builder.ProgressEvent, done <-chan BuildOutcome, cancelBuild func(), input io.Reader, output io.Writer) (BuildOutcome, error) {
	model := newBuildModel(screen, events, done, cancelBuild)
	program := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(output), tea.WithAltScreen())
	finalModel, err := program.Run()
	if err != nil {
		return BuildOutcome{}, fmt.Errorf("running the build screen: %w", err)
	}
	finished, isBuildModel := finalModel.(*buildModel)
	if !isBuildModel {
		return BuildOutcome{}, fmt.Errorf("running the build screen: unexpected model %T", finalModel)
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
	phaseTitleWidth    = 36
	barWidth           = 20
	collapsedLogHeight = 8
	minimumLogHeight   = 3
	maxLogLines        = 500
	clockInterval      = time.Second
)

// Messages the build model receives besides Bubble Tea's own.
type (
	eventMsg        builder.ProgressEvent
	eventsClosedMsg struct{}
	doneMsg         BuildOutcome
	clockMsg        time.Time
)

// buildModel is the build screen.
type buildModel struct {
	screen      BuildScreen
	events      <-chan builder.ProgressEvent
	done        <-chan BuildOutcome
	cancelBuild func()

	phases    []phaseState
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

	result       *BuildOutcome
	eventsClosed bool
	interrupting bool
	abandoned    bool
}

func newBuildModel(screen BuildScreen, events <-chan builder.ProgressEvent, done <-chan BuildOutcome, cancelBuild func()) *buildModel {
	now := time.Now()
	model := &buildModel{
		screen:      screen,
		events:      events,
		done:        done,
		cancelBuild: cancelBuild,
		phases:      make([]phaseState, len(builder.Phases())),
		spinner:     spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(runningStyle)),
		bar:         progress.New(progress.WithWidth(barWidth), progress.WithoutPercentage(), progress.WithDefaultGradient()),
		logView:     viewport.New(defaultWidth-4, collapsedLogHeight),
		width:       defaultWidth,
		height:      defaultHeight,
		startedAt:   now,
		now:         now,
	}
	return model
}

// outcome returns how the build ended, as far as the screen saw it.
func (m *buildModel) outcome() BuildOutcome {
	if m.result == nil {
		return BuildOutcome{Abandoned: true, Interrupted: m.interrupting}
	}
	result := *m.result
	result.Interrupted = m.interrupting
	return result
}

// Init implements tea.Model.
func (m *buildModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.waitForEvent(), m.waitForDone(), tickClock())
}

// waitForEvent delivers the next progress event, or says the channel closed.
func (m *buildModel) waitForEvent() tea.Cmd {
	return func() tea.Msg {
		event, open := <-m.events
		if !open {
			return eventsClosedMsg{}
		}
		return eventMsg(event)
	}
}

// waitForDone delivers the build's outcome.
func (m *buildModel) waitForDone() tea.Cmd {
	return func() tea.Msg { return doneMsg(<-m.done) }
}

func tickClock() tea.Cmd {
	return tea.Tick(clockInterval, func(now time.Time) tea.Msg { return clockMsg(now) })
}

// Update implements tea.Model.
func (m *buildModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		outcome := BuildOutcome(msg)
		m.result = &outcome
		m.closePhases(outcome.Err != nil)
		return m, m.quitIfFinished()
	}
	return m, nil
}

// finished reports whether the build is over and every event has been seen.
func (m *buildModel) finished() bool { return m.result != nil && m.eventsClosed }

func (m *buildModel) quitIfFinished() tea.Cmd {
	if m.finished() {
		return tea.Quit
	}
	return nil
}

func (m *buildModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case msg.Type == tea.KeyCtrlC:
		if m.interrupting || m.finished() {
			m.abandoned = true
			return m, tea.Quit
		}
		m.interrupting = true
		m.cancelBuild()
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
func (m *buildModel) apply(event builder.ProgressEvent) {
	switch event.Kind {
	case builder.EventLogFile:
		m.logPath = event.Line
		return
	case builder.EventLogLine:
		m.appendLog(event.Line)
		return
	case builder.EventPhaseStarted, builder.EventProgress, builder.EventPhaseFinished:
	}
	if int(event.Phase) >= len(m.phases) {
		return // a phase this screen does not know; the log still shows it
	}
	state := &m.phases[event.Phase]
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

// closePhases settles the running phase when the build ends: failed when the
// build did, done otherwise.
func (m *buildModel) closePhases(failed bool) {
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
func (m *buildModel) appendLog(line string) {
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
func (m *buildModel) resizeLog() {
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
func (m *buildModel) View() string {
	var view strings.Builder
	fmt.Fprintf(&view, "%s   %s · Ubuntu %s (%s, %s) · %s\n\n", titleStyle.Render("frostroot build"),
		m.screen.ImageName, m.screen.Release, m.screen.Suite, m.screen.Arch, m.screen.ArchiveURL)
	for index, phase := range builder.Phases() {
		view.WriteString(m.phaseLine(phase, m.phases[index]))
		view.WriteByte('\n')
	}
	view.WriteByte('\n')
	pane := paneStyle.Width(m.logView.Width).Render(m.logView.View())
	pane = strings.Replace(pane, "╭─", "╭─ "+dimStyle.Render("mmdebstrap")+" ", 1)
	view.WriteString(pane)
	view.WriteByte('\n')
	view.WriteString(m.footer())
	return view.String()
}

// phaseLine renders one row of the phase table.
func (m *buildModel) phaseLine(phase builder.Phase, state phaseState) string {
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
func (m *buildModel) progressDetail(state phaseState) string {
	if !state.hasProgress {
		return ""
	}
	percent, known := state.progress.Percent()
	if !known {
		return dimStyle.Render(state.progress.Summary())
	}
	return fmt.Sprintf("%s %3.0f%%  %s", m.bar.ViewAs(percent/100), percent, dimStyle.Render(truncate(state.progress.Summary(), m.width-phaseTitleWidth-barWidth-22)))
}

// finishedDetail says what a finished measured phase did: "28.1 MB", "326 steps".
func (m *buildModel) finishedDetail(state phaseState) string {
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
func (m *buildModel) footer() string {
	elapsed := formatDuration(m.now.Sub(m.startedAt))
	if m.interrupting && !m.finished() {
		return warningStyle.Render(fmt.Sprintf("  %s elapsed · interrupting: waiting for mmdebstrap to clean up (Ctrl-C again to stop waiting)", elapsed))
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
