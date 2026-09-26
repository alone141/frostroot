package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"

	"frostroot/internal/builder"
	"frostroot/internal/form"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
)

// Sizes of the picker.
const (
	// pickerListHeight is how many rows the list shows when the form gives
	// the field no height of its own: what fits a 24-row terminal with the
	// page title, the field's own lines and the help line around it.
	pickerListHeight = 8
	// pickerMinimumListHeight is what a short terminal still gets.
	pickerMinimumListHeight = 3
	// pickerChromeRows is what the field shows besides the list: title,
	// description, query, status, chosen, hint.
	pickerChromeRows = 6
	// pickerMatchLimit is how many matches the list holds. Nobody scrolls
	// past two hundred; the status line says how many more there are.
	pickerMatchLimit = 200
	// pickerNameWidth and pickerVersionWidth bound the first two columns;
	// nine names in ten are forty characters or fewer (the spike), and the
	// version gives way entirely on a narrow terminal.
	pickerNameWidth      = 28
	pickerVersionWidth   = 16
	pickerNarrowWidth    = 72
	pickerBarWidth       = 16
	pickerTickInterval   = 150 * time.Millisecond
	pickerNearestOffered = 3
)

// pickerSummaryDelay is how long the cursor rests on a row before its
// summary is asked for. Holding Down through twenty rows must not be twenty
// requests. Tests shorten it, so that the debounce they exercise is the one
// that ships rather than a stand-in.
var pickerSummaryDelay = 250 * time.Millisecond

// pickerField is a question answered with a list of package names, found by
// searching an index and added with Space, or typed. "Other packages"
// searches the release's apt archive and "Python packages" searches PyPI. It is
// a huh.Field, so both sit on the Packages page and move on with the form's
// own keys.
//
// The index is help, not a requirement. While it loads, when it cannot be
// had, and when the form was given none, the field is a list editor: type a
// name, Space adds it.
type pickerField struct {
	key, title, description string
	value                   *string                  // the answer: names separated by spaces
	validate                func(string) error       // nil accepts anything
	openIndex               form.IndexOpener         // nil means there is no index
	hasSummaries            bool                     // the index looks summaries up one at a time
	request                 func() form.IndexRequest // the release and sources answered by now
	inCatalog               func() []string          // the names chosen in the catalog above
	ctx                     context.Context          // ends with the form; stops a fetch

	glyphs  glyphSet
	theme   *huh.Theme
	keymap  pickerKeyMap
	width   int
	height  int
	focused bool
	err     error

	input   textinput.Model
	chosen  []string
	rows    []pickerRow
	total   int // how many the index has for the query; len(rows) may be fewer
	cursor  int
	offset  int
	section string
	hint    string
	// nudged is set once Enter was held back because a typed name was
	// not added; the second Enter moves on.
	nudged bool

	choosingSection bool
	sections        []form.SectionCount
	sectionCursor   int
	sectionOffset   int

	load *indexLoad
	tick int

	// summaryTick numbers the debounce, so that only the newest row the
	// cursor rested on is ever asked about.
	summaryTick int
	// summaryPending is the name being looked up, for the line that says so.
	summaryPending string
	// hasSections is whether the loaded index has any, asked once.
	sectionsChecked bool
	hasSections     bool
}

// pickerRow is one line of the list.
type pickerRow struct {
	name        string
	version     string
	origin      string // the source it comes from, or "" for the release's archive
	description string
	asTyped     bool // the query itself, offered because the index lacks it
	unknown     bool // a chosen name the index lacks
}

// pickerKeyMap is what the field answers to besides typing.
type pickerKeyMap struct {
	Up, Down, PageUp, PageDown key.Binding
	Toggle, Section, Clear     key.Binding
	Next, Prev                 key.Binding
	PickSection, CloseSections key.Binding // shown while the section list is open
}

func newPickerKeyMap(glyphs glyphSet) pickerKeyMap {
	arrows := "↑/↓"
	if glyphs.ascii {
		arrows = "up/down"
	}
	return pickerKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "ctrl+p"), key.WithHelp(arrows, "move")),
		Down:     key.NewBinding(key.WithKeys("down", "ctrl+n")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		Toggle:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "add/remove")),
		Section:  key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "section")),
		Clear:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "clear")),
		Next:     key.NewBinding(key.WithKeys("enter", "tab"), key.WithHelp("enter", "next")),
		Prev:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "back")),

		PickSection:   key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "pick")),
		CloseSections: key.NewBinding(key.WithKeys("esc", "/"), key.WithHelp("esc", "close")),
	}
}

// indexLoad is one opening of an index, shared between the field and the
// command that does it. The command may finish while another page is on
// screen, where its message reaches other fields and not this one, so the
// result lives here and the message only asks for a redraw.
type indexLoad struct {
	mutex sync.Mutex
	// key identifies the request this load answered, so that a field
	// returned to opens the index again only when the answers it is made of
	// have changed.
	key         string
	index       form.PackageIndex
	err         error
	finished    bool
	done, total int64
	cancel      context.CancelFunc
}

// incomplete reports whether a finished load left a repository out: one
// that could not be read. Coming back to the field tries it again, because
// a vendor that was down for a moment should not be missing from the search
// for the rest of the form. A load still running is not incomplete.
func (l *indexLoad) incomplete() bool {
	l.mutex.Lock()
	defer l.mutex.Unlock()
	if !l.finished {
		return false
	}
	if l.err != nil {
		return true
	}
	repositories, canSay := l.index.(form.PackageRepositories)
	if !canSay {
		return false
	}
	_, missing := repositories.Sources()
	return len(missing) > 0
}

// pickerLoadedMsg says an index load ended. pickerTickMsg redraws the
// progress of one that has not.
type (
	pickerLoadedMsg struct{}
	pickerTickMsg   struct{ tick int }
	// pickerSummaryDueMsg says the cursor has rested on name long enough to
	// ask about it; tick tells a stale rest from the current one.
	pickerSummaryDueMsg struct {
		tick int
		name string
	}
	// pickerSummaryMsg says a lookup ended, one way or the other.
	pickerSummaryMsg struct{}
)

func newPickerField(ctx context.Context, field form.Field, value *string, request func() form.IndexRequest, inCatalog func() []string, glyphs glyphSet) *pickerField {
	input := textinput.New()
	input.Prompt = "> "
	input.Placeholder = "type to search, or a name"
	picker := &pickerField{
		key: field.Key, title: field.Title, description: field.Description,
		value: value, validate: field.Validate, openIndex: field.OpenIndex,
		hasSummaries: field.Summaries,
		request:      request, inCatalog: inCatalog, ctx: ctx,
		glyphs: glyphs, keymap: newPickerKeyMap(glyphs), input: input,
		chosen: splitNames(*value),
	}
	picker.refresh()
	return picker
}

// splitNames splits an answer the way the form does.
func splitNames(text string) []string {
	return strings.FieldsFunc(text, func(character rune) bool {
		return character == ',' || character == ' ' || character == '\t' || character == '\n' || character == '\r'
	})
}

// isNameList reports whether typed or pasted text is more than part of one
// name: a comma typed after a name, or a pasted list.
func isNameList(text string) bool { return strings.ContainsAny(text, ", \t\n\r") }

// Init implements huh.Field.
func (p *pickerField) Init() tea.Cmd { return nil }

// Focus implements huh.Field. The index is opened the first time the field
// is reached, and again when the answers it is made of have changed since:
// the release, asked pages earlier, and the sources, asked on the page
// before this one.
func (p *pickerField) Focus() tea.Cmd {
	p.focused = true
	p.nudged = false
	cmds := []tea.Cmd{p.input.Focus()}
	if p.openIndex != nil {
		request := p.request()
		if asked := request.Key(); p.load == nil || p.load.key != asked || p.load.incomplete() {
			if p.load != nil && p.load.cancel != nil {
				p.load.cancel()
			}
			cmds = append(cmds, p.startLoad(request))
		}
		if _, loading := p.loading(); loading {
			cmds = append(cmds, p.nextTick())
		}
	}
	p.refresh()
	cmds = append(cmds, p.scheduleSummary())
	return tea.Batch(cmds...)
}

// startLoad opens the index a request asks for in a command.
func (p *pickerField) startLoad(request form.IndexRequest) tea.Cmd {
	ctx, cancel := context.WithCancel(p.ctx)
	load := &indexLoad{key: request.Key(), cancel: cancel}
	p.load = load
	openIndex := p.openIndex
	return func() tea.Msg {
		index, err := openIndex(ctx, request, func(done, total int64) {
			load.mutex.Lock()
			load.done, load.total = done, total
			load.mutex.Unlock()
		})
		load.mutex.Lock()
		load.index, load.err, load.finished = index, err, true
		load.mutex.Unlock()
		return pickerLoadedMsg{}
	}
}

// nextTick asks for a redraw shortly. The focused field of a huh group is
// handed every message twice, so a tick carries a number and only the
// current one is answered; the other would double the ticks each round.
func (p *pickerField) nextTick() tea.Cmd {
	p.tick++
	tick := p.tick
	return tea.Tick(pickerTickInterval, func(time.Time) tea.Msg { return pickerTickMsg{tick: tick} })
}

// summaries returns the loaded index's summary source, or nil when it has
// none: the apt index carries its descriptions already.
func (p *pickerField) summaries() form.PackageSummaries {
	source, hasSummaries := p.index().(form.PackageSummaries)
	if !hasSummaries {
		return nil
	}
	return source
}

// highlighted is the name the cursor is on, or "".
func (p *pickerField) highlighted() string {
	if p.choosingSection || p.cursor < 0 || p.cursor >= len(p.rows) {
		return ""
	}
	return p.rows[p.cursor].name
}

// scheduleSummary asks about the highlighted row once the cursor has rested
// on it, unless it is already known. Every call supersedes the last, so
// moving through rows asks about the one that is stopped on.
func (p *pickerField) scheduleSummary() tea.Cmd {
	source := p.summaries()
	name := p.highlighted()
	if source == nil || name == "" || !p.focused {
		return nil
	}
	if _, known := source.Summary(name); known {
		p.summaryPending = ""
		return nil
	}
	p.summaryTick++
	tick := p.summaryTick
	return tea.Tick(pickerSummaryDelay, func(time.Time) tea.Msg {
		return pickerSummaryDueMsg{tick: tick, name: name}
	})
}

// fetchSummary looks one name up, away from the drawing.
func (p *pickerField) fetchSummary(name string) tea.Cmd {
	source := p.summaries()
	if source == nil {
		return nil
	}
	ctx := p.ctx
	return func() tea.Msg {
		source.FetchSummary(ctx, name)
		return pickerSummaryMsg{}
	}
}

// index returns the loaded index, or nil.
func (p *pickerField) index() form.PackageIndex {
	if p.load == nil {
		return nil
	}
	p.load.mutex.Lock()
	defer p.load.mutex.Unlock()
	if p.load.err != nil {
		return nil
	}
	return p.load.index
}

// loading returns the progress of a load that has not ended.
func (p *pickerField) loading() (progress [2]int64, isLoading bool) {
	if p.load == nil {
		return progress, false
	}
	p.load.mutex.Lock()
	defer p.load.mutex.Unlock()
	return [2]int64{p.load.done, p.load.total}, !p.load.finished
}

// Blur implements huh.Field.
func (p *pickerField) Blur() tea.Cmd {
	p.focused = false
	p.choosingSection = false
	p.input.Blur()
	p.err = p.check()
	return nil
}

// check validates the answer.
func (p *pickerField) check() error {
	if p.validate == nil {
		return nil
	}
	return p.validate(*p.value)
}

// Update implements huh.Field.
func (p *pickerField) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case pickerLoadedMsg:
		p.refresh()
		return p, p.scheduleSummary()
	case pickerSummaryDueMsg:
		if msg.tick != p.summaryTick || !p.focused || p.highlighted() != msg.name {
			return p, nil // the cursor moved on; that row is not wanted now
		}
		p.summaryPending = msg.name
		return p, p.fetchSummary(msg.name)
	case pickerSummaryMsg:
		p.summaryPending = ""
		return p, nil
	case pickerTickMsg:
		if msg.tick != p.tick || !p.focused {
			return p, nil
		}
		if _, loading := p.loading(); loading {
			return p, p.nextTick()
		}
		p.refresh()
		return p, nil
	case tea.KeyMsg:
		// Whatever the key did, the cursor may now be on another row, and
		// that row is the one worth asking about.
		return p, tea.Batch(p.handleKey(msg), p.scheduleSummary())
	}
	return p, nil
}

func (p *pickerField) handleKey(msg tea.KeyMsg) tea.Cmd {
	p.err = nil
	if p.choosingSection {
		p.handleSectionKey(msg)
		return nil
	}
	p.hint = ""
	switch {
	case key.Matches(msg, p.keymap.Prev):
		return huh.PrevField
	case key.Matches(msg, p.keymap.Next):
		return p.moveOn()
	case key.Matches(msg, p.keymap.Up):
		p.moveCursor(-1)
	case key.Matches(msg, p.keymap.Down):
		p.moveCursor(1)
	case key.Matches(msg, p.keymap.PageUp):
		p.moveCursor(-p.listHeight())
	case key.Matches(msg, p.keymap.PageDown):
		p.moveCursor(p.listHeight())
	case key.Matches(msg, p.keymap.Toggle):
		p.toggle()
	case key.Matches(msg, p.keymap.Section):
		p.openSections()
	case key.Matches(msg, p.keymap.Clear):
		if p.input.Value() == "" {
			p.section = ""
		}
		p.input.SetValue("")
		p.refresh()
	case msg.Type == tea.KeyRunes && isNameList(string(msg.Runes)):
		p.addSeveral(p.input.Value() + string(msg.Runes))
	default:
		before := p.input.Value()
		var cmd tea.Cmd
		p.input, cmd = p.input.Update(msg)
		if p.input.Value() != before {
			p.nudged = false
			p.refresh()
		}
		return cmd
	}
	return nil
}

// moveOn leaves the field, unless a name was typed and never added: Enter
// kept what the old free-text field held, and here it would silently drop
// it. The first Enter says so; the second means it.
func (p *pickerField) moveOn() tea.Cmd {
	query := strings.TrimSpace(p.input.Value())
	if query != "" && !p.nudged && !slices.Contains(p.chosen, query) && !slices.Contains(p.inCatalog(), query) {
		p.nudged = true
		p.hint = fmt.Sprintf("%q is not added: Space adds it, Enter again leaves it out", query)
		return nil
	}
	if p.err = p.check(); p.err != nil {
		return nil
	}
	return huh.NextField
}

// toggle adds or removes the highlighted row.
func (p *pickerField) toggle() {
	if p.cursor < 0 || p.cursor >= len(p.rows) {
		return
	}
	row := p.rows[p.cursor]
	if slices.Contains(p.inCatalog(), row.name) {
		p.hint = row.name + " is chosen in the catalog above"
		return
	}
	if position := slices.Index(p.chosen, row.name); position >= 0 {
		p.chosen = slices.Delete(p.chosen, position, position+1)
	} else {
		p.chosen = append(p.chosen, row.name)
		if row.asTyped {
			p.hint = p.unknownHint(row.name)
		}
	}
	p.store()
	// A name typed in full and added is done with; a name picked out of a
	// search may have neighbors worth adding too, so that query stays.
	if strings.EqualFold(strings.TrimSpace(p.input.Value()), row.name) {
		p.input.SetValue("")
		p.refresh()
		return
	}
	if strings.TrimSpace(p.input.Value()) == "" {
		// With no query the list is the chosen names themselves, so a name
		// just removed has to leave it. Left there, its row would show an
		// empty box that no longer matches any answer, and Space on it
		// would put the name back, at the end of the list rather than where
		// it was. A search list is not rebuilt: its rows come from the
		// index and do not change, and refreshing would move the cursor off
		// the row the person is working through.
		wasAt := p.cursor
		p.refresh()
		p.moveCursor(wasAt)
	}
}

// unknownHint says what the index has instead of a name it lacks.
func (p *pickerField) unknownHint(name string) string {
	index := p.index()
	if index == nil {
		return ""
	}
	if nearest := index.Nearest(name, pickerNearestOffered); len(nearest) > 0 {
		return fmt.Sprintf("%s is not in the index; nearest: %s", name, strings.Join(nearest, ", "))
	}
	return name + " is not in the index; it may still come from somewhere else"
}

// checkName says whether one name is one the field's answer may hold: by
// the field's own validator, which is the PEP 503 rule for the Python field
// and apt's for the package fields, and by apt's rule for a field without
// one. The rule used to be apt's for every field, so on the Python field
// Flask, PyYAML and flask_sqlalchemy were offered no row, Space did nothing,
// Enter said that Space would add them, and a pasted list kept only the
// names that happened to be apt names too.
func (p *pickerField) checkName(name string) error {
	if p.validate != nil {
		return p.validate(name)
	}
	return recipe.CheckPackageName(name)
}

// addSeveral adds every name of a pasted or comma-separated list.
func (p *pickerField) addSeveral(text string) {
	var refused []string
	for _, name := range splitNames(text) {
		switch {
		case p.checkName(name) != nil:
			refused = append(refused, name)
		case slices.Contains(p.chosen, name) || slices.Contains(p.inCatalog(), name):
		default:
			p.chosen = append(p.chosen, name)
		}
	}
	p.store()
	p.input.SetValue("")
	p.refresh()
	if len(refused) > 0 {
		p.hint = "not package names, left out: " + strings.Join(refused, " ")
	}
}

// store writes the answer.
func (p *pickerField) store() { *p.value = strings.Join(p.chosen, " ") }

// refresh recomputes the list for the query, the section and the index as
// they are now.
func (p *pickerField) refresh() {
	p.cursor, p.offset = 0, 0
	query := strings.TrimSpace(p.input.Value())
	index := p.index()
	p.rows, p.total = nil, 0
	if query == "" && p.section == "" {
		// Nothing asked: the list is what has been chosen, so that it can
		// be looked over and thinned.
		for _, name := range p.chosen {
			p.rows = append(p.rows, p.chosenRow(name, index))
		}
		return
	}
	if index != nil {
		matches, total := index.Search(query, p.section, pickerMatchLimit)
		p.total = total
		for _, match := range matches {
			p.rows = append(p.rows, rowOf(match))
		}
	}
	exact := len(p.rows) > 0 && p.rows[0].name == query
	if !exact && len(p.rows) > 0 && index != nil {
		// The PyPI index compares names as PEP 503 does and answers with
		// the published spelling, so flask and Flask are one project. Typed
		// above its own row, the query would be a twin: two rows for one
		// project, a count that does not match them, and a Space on the
		// second undoing the first.
		if match, isThere := index.Lookup(query); isThere && match.Name == p.rows[0].name {
			exact = true
		}
	}
	if query == "" || exact || p.checkName(query) != nil {
		return
	}
	// What was typed comes first, so that Space after a whole name adds that
	// name and never a neighbor the search ranked first. The index may
	// well have it: a section filter hides a package of another section
	// from the search, and that is no reason to call it missing.
	first := pickerRow{name: query, asTyped: true}
	if index != nil {
		if match, isThere := index.Lookup(query); isThere {
			first = rowOf(match)
		}
	}
	p.rows = append([]pickerRow{first}, p.rows...)
}

// chosenRow describes a chosen name for the list.
func (p *pickerField) chosenRow(name string, index form.PackageIndex) pickerRow {
	if index == nil {
		return pickerRow{name: name}
	}
	// Lookup, not Search: capture's recipe has hundreds of names, and a scan
	// of the index for each would be seconds of a frozen screen.
	if match, isThere := index.Lookup(name); isThere {
		// The name stays as it was chosen, never as the index spells it:
		// PyPI compares Flask_SQLAlchemy and flask-sqlalchemy as one name,
		// and a row whose name is not the chosen one is a row that does not
		// know it is chosen.
		row := rowOf(match)
		row.name = name
		return row
	}
	return pickerRow{name: name, unknown: true}
}

// rowOf is the line a match is shown as.
func rowOf(match form.Match) pickerRow {
	return pickerRow{name: match.Name, version: match.Version, origin: match.Origin, description: match.Description}
}

// describe is what a row says of the package after its name and version:
// the source it comes from, when it is not the release's own archive, and
// then the one-line description. The source goes first because a line too
// long to fit loses its end, and which repository a package comes from is
// what a person is deciding about. It is shortened, because a row is the
// one place the name competes for width: the recipe's "ppa-deadsnakes-ppa"
// is "deadsnakes" here, and the line above the results still names every
// repository in full.
func (r pickerRow) describe() string {
	origin := sources.ShortName(r.origin)
	if origin == "" {
		return r.description
	}
	if r.description == "" {
		return origin
	}
	return origin + " · " + r.description
}

func (p *pickerField) moveCursor(delta int) {
	if len(p.rows) == 0 {
		return
	}
	p.cursor = min(max(p.cursor+delta, 0), len(p.rows)-1)
	p.offset = scrolledTo(p.offset, p.cursor, p.listHeight())
}

// scrolledTo returns the offset that keeps cursor inside a window of height
// rows starting at offset.
func scrolledTo(offset, cursor, height int) int {
	switch {
	case cursor < offset:
		return cursor
	case cursor >= offset+height:
		return cursor - height + 1
	}
	return offset
}

// openSections shows the sections that hold matches in place of the list.
func (p *pickerField) openSections() {
	index := p.index()
	if index == nil {
		p.hint = "sections need the package index"
		return
	}
	sections := index.SectionsMatching(strings.TrimSpace(p.input.Value()))
	if len(sections) == 0 {
		// PyPI publishes no sections, so there is nothing to narrow to and
		// an "all sections" list of one would be a menu that does nothing.
		p.hint = "this index has no sections"
		return
	}
	p.sections = append([]form.SectionCount{{Name: ""}}, sections...)
	p.choosingSection = true
	p.sectionCursor, p.sectionOffset = 0, 0
	for position, section := range p.sections {
		if section.Name == p.section {
			p.sectionCursor = position
			p.sectionOffset = scrolledTo(0, position, p.listHeight())
		}
	}
}

func (p *pickerField) handleSectionKey(msg tea.KeyMsg) {
	switch {
	case key.Matches(msg, p.keymap.Up):
		p.sectionCursor = max(p.sectionCursor-1, 0)
	case key.Matches(msg, p.keymap.Down):
		p.sectionCursor = min(p.sectionCursor+1, len(p.sections)-1)
	case key.Matches(msg, p.keymap.PageUp):
		p.sectionCursor = max(p.sectionCursor-p.listHeight(), 0)
	case key.Matches(msg, p.keymap.PageDown):
		p.sectionCursor = min(p.sectionCursor+p.listHeight(), len(p.sections)-1)
	case key.Matches(msg, p.keymap.PickSection):
		p.section = p.sections[p.sectionCursor].Name
		p.choosingSection = false
		p.refresh()
		return
	case key.Matches(msg, p.keymap.CloseSections):
		p.choosingSection = false
		return
	}
	p.sectionOffset = scrolledTo(p.sectionOffset, p.sectionCursor, p.listHeight())
}

// chromeRows is what the field draws besides the list. An index that has
// summaries adds the line they go on, and it is drawn whether or not there
// is one to show, so that the page does not jump as they arrive.
func (p *pickerField) chromeRows() int {
	if p.openIndex != nil && p.hasSummaries {
		return pickerChromeRows + 1
	}
	return pickerChromeRows
}

// listHeight is how many rows the list has.
func (p *pickerField) listHeight() int {
	if p.height <= 0 {
		return pickerListHeight
	}
	return max(pickerMinimumListHeight, p.height-p.chromeRows())
}

// summaryLine is what goes under the list: the highlighted package's
// summary, or that it is being looked up. Empty when there is nothing to
// say, which keeps the row without filling it.
func (p *pickerField) summaryLine() string {
	source := p.summaries()
	if source == nil || p.choosingSection {
		return ""
	}
	name := p.highlighted()
	if name == "" {
		return ""
	}
	if summary, known := source.Summary(name); known {
		return summary
	}
	if p.summaryPending == name {
		return "looking up " + name + p.glyphs.ellipsis
	}
	return ""
}

// styles returns the theme's styles for the field's state.
func (p *pickerField) styles() *huh.FieldStyles {
	theme := p.theme
	if theme == nil {
		theme = formTheme(p.glyphs)
	}
	if p.focused {
		return &theme.Focused
	}
	return &theme.Blurred
}

// View implements huh.Field.
func (p *pickerField) View() string {
	styles := p.styles()
	width := max(p.width-styles.Base.GetHorizontalFrameSize(), previewMinimumWidth)
	p.input.PlaceholderStyle = styles.TextInput.Placeholder
	p.input.PromptStyle = styles.TextInput.Prompt
	p.input.Cursor.Style = styles.TextInput.Cursor
	p.input.TextStyle = styles.TextInput.Text
	p.input.Width = max(width-lipgloss.Width(p.input.Prompt)-1, 1)

	var view strings.Builder
	view.WriteString(styles.Title.Render(p.title))
	if p.err != nil {
		view.WriteString(styles.ErrorIndicator.String())
	}
	view.WriteByte('\n')
	view.WriteString(styles.Description.Render(truncateWith(p.description, width, p.glyphs.ellipsis)))
	view.WriteByte('\n')
	view.WriteString(p.input.View())
	view.WriteByte('\n')
	view.WriteString(dimStyle.Render(truncateWith(p.status(), width, p.glyphs.ellipsis)))
	view.WriteByte('\n')
	if p.choosingSection {
		view.WriteString(p.sectionLines(styles, width))
	} else {
		view.WriteString(p.rowLines(styles, width))
	}
	view.WriteByte('\n')
	if p.openIndex != nil && p.hasSummaries {
		view.WriteString(dimStyle.Render(truncateWith(p.summaryLine(), width, p.glyphs.ellipsis)))
		view.WriteByte('\n')
	}
	view.WriteString(dimStyle.Render(truncateWith(p.chosenLine(), width, p.glyphs.ellipsis)))
	view.WriteByte('\n')
	switch {
	case p.err != nil:
		view.WriteString(styles.ErrorMessage.Render(truncateWith(p.err.Error(), width, p.glyphs.ellipsis)))
	case p.hint != "":
		view.WriteString(warningStyle.Render(truncateWith(p.hint, width, p.glyphs.ellipsis)))
	}
	return styles.Base.Width(p.width).Render(view.String())
}

// status is the line under the query: what is being searched, or why
// nothing is.
func (p *pickerField) status() string {
	if p.openIndex == nil {
		return "no package index: names are added as typed and checked by build"
	}
	if progress, loading := p.loading(); loading {
		if progress[1] <= 0 {
			// Nothing downloaded yet: a cached index opens in a blink, and
			// "fetching" would be a claim about the network.
			return "opening the package index" + p.glyphs.ellipsis
		}
		filled := int(float64(pickerBarWidth) * float64(progress[0]) / float64(progress[1]))
		filled = min(max(filled, 0), pickerBarWidth)
		bar := strings.Repeat(string(p.glyphs.barFull), filled) + strings.Repeat(string(p.glyphs.barEmpty), pickerBarWidth-filled)
		return fmt.Sprintf("fetching the package index %s %s / %s", bar, builder.FormatBytes(progress[0]), builder.FormatBytes(progress[1]))
	}
	index := p.index()
	if index == nil {
		return "package index not available: names are added as typed and checked by build"
	}
	// An index without sections is not "all sections": PyPI has none, and
	// saying so would describe a thing that is not there.
	section := ""
	switch {
	case p.section != "":
		section = "section " + p.section
	case p.sectionsAvailable():
		section = "all sections"
	}
	var parts []string
	switch shown := p.shownMatches(); {
	case p.total > shown:
		parts = append(parts, fmt.Sprintf("%d of %d shown, keep typing", shown, p.total), section)
	case strings.TrimSpace(p.input.Value()) != "" || p.section != "":
		parts = append(parts, fmt.Sprintf("%d found", p.total), section, index.Describe())
	default:
		parts = append(parts, section, index.Describe())
	}
	return strings.Join(slices.DeleteFunc(parts, func(part string) bool { return part == "" }), " · ")
}

// sectionsAvailable reports whether the loaded index has sections to narrow
// by. It is asked once an index and remembered, because for the apt one the
// answer is a copy of its whole section table.
func (p *pickerField) sectionsAvailable() bool {
	if p.sectionsChecked {
		return p.hasSections
	}
	index := p.index()
	if index == nil {
		return false
	}
	p.sectionsChecked, p.hasSections = true, len(index.SectionsMatching("")) > 0
	return p.hasSections
}

// shownMatches is how many rows came from the index.
func (p *pickerField) shownMatches() int {
	shown := 0
	for _, row := range p.rows {
		if !row.asTyped {
			shown++
		}
	}
	return shown
}

// rowLines renders the list, always listHeight lines high so that the page
// does not jump as the matches come and go.
func (p *pickerField) rowLines(styles *huh.FieldStyles, width int) string {
	height := p.listHeight()
	lines := make([]string, 0, height)
	if len(p.rows) == 0 {
		lines = append(lines, dimStyle.Render(truncateWith(p.emptyText(), width, p.glyphs.ellipsis)))
	}
	nameWidth := 0
	for position := p.offset; position < min(p.offset+height, len(p.rows)); position++ {
		nameWidth = max(nameWidth, lipgloss.Width(p.rows[position].name))
	}
	nameWidth = min(nameWidth, pickerNameWidth)
	inCatalog := p.inCatalog()
	for position := p.offset; position < min(p.offset+height, len(p.rows)); position++ {
		lines = append(lines, p.rowLine(styles, p.rows[position], position == p.cursor, nameWidth, width, inCatalog))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// emptyText says why the list is empty.
func (p *pickerField) emptyText() string {
	if strings.TrimSpace(p.input.Value()) == "" {
		return "nothing chosen yet"
	}
	return "nothing found, and what is typed is not a package name"
}

func (p *pickerField) rowLine(styles *huh.FieldStyles, row pickerRow, highlighted bool, nameWidth, width int, inCatalog []string) string {
	selector := strings.Repeat(" ", lipgloss.Width(styles.MultiSelectSelector.String()))
	if highlighted && p.focused {
		selector = styles.MultiSelectSelector.String()
	}
	isChosen := slices.Contains(p.chosen, row.name) || slices.Contains(inCatalog, row.name)
	prefix, textStyle := styles.UnselectedPrefix.String(), styles.UnselectedOption
	if isChosen {
		prefix, textStyle = styles.SelectedPrefix.String(), styles.SelectedOption
	}
	remaining := width - lipgloss.Width(selector) - lipgloss.Width(prefix)
	var text string
	switch {
	case row.asTyped:
		text = fmt.Sprintf("add %q as typed", row.name)
		if p.index() != nil {
			text += " · not in the index"
		}
	case row.unknown:
		text = padCells(row.name, nameWidth) + "  ? not in the index"
	case width < pickerNarrowWidth || row.version == "":
		text = padCells(row.name, nameWidth) + "  " + row.describe()
	default:
		text = padCells(row.name, nameWidth) + "  " + padCells(truncateWith(row.version, pickerVersionWidth, p.glyphs.ellipsis), pickerVersionWidth) + "  " + row.describe()
	}
	return selector + prefix + textStyle.Render(truncateWith(strings.TrimRight(text, " "), remaining, p.glyphs.ellipsis))
}

// padCells pads text with spaces to width cells; longer text is left whole,
// for the caller to cut with the line.
func padCells(text string, width int) string {
	if missing := width - lipgloss.Width(text); missing > 0 {
		return text + strings.Repeat(" ", missing)
	}
	return text
}

func (p *pickerField) sectionLines(styles *huh.FieldStyles, width int) string {
	height := p.listHeight()
	lines := make([]string, 0, height)
	for position := p.sectionOffset; position < min(p.sectionOffset+height, len(p.sections)); position++ {
		section := p.sections[position]
		text := fmt.Sprintf("%-16s %d", section.Name, section.Count)
		if section.Name == "" {
			text = "all sections"
		}
		selector := strings.Repeat(" ", lipgloss.Width(styles.SelectSelector.String()))
		textStyle := styles.Option
		if position == p.sectionCursor {
			selector, textStyle = styles.SelectSelector.String(), styles.SelectedOption
		}
		lines = append(lines, selector+textStyle.Render(truncateWith(text, width-lipgloss.Width(selector), p.glyphs.ellipsis)))
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n")
}

// chosenLine sums up the answer under the list.
func (p *pickerField) chosenLine() string {
	if p.choosingSection {
		kept := p.section
		if kept == "" {
			kept = "all sections"
		}
		return "enter picks the section · esc keeps " + kept
	}
	if len(p.chosen) == 0 {
		return "chosen: none"
	}
	return fmt.Sprintf("chosen (%d): %s", len(p.chosen), strings.Join(p.chosen, " "))
}

// Error implements huh.Field.
func (p *pickerField) Error() error { return p.err }

// Skip implements huh.Field.
func (p *pickerField) Skip() bool { return false }

// Zoom implements huh.Field. The field keeps its place on the page: the
// catalog above it is what its marks refer to.
func (p *pickerField) Zoom() bool { return false }

// KeyBinds implements huh.Field: the help line under the page.
func (p *pickerField) KeyBinds() []key.Binding {
	if p.choosingSection {
		return []key.Binding{p.keymap.Up, p.keymap.PickSection, p.keymap.CloseSections}
	}
	bindings := []key.Binding{p.keymap.Up, p.keymap.Toggle}
	if p.sectionsAvailable() {
		bindings = append(bindings, p.keymap.Section)
	}
	return append(bindings, p.keymap.Prev, p.keymap.Next)
}

// WithTheme implements huh.Field.
func (p *pickerField) WithTheme(theme *huh.Theme) huh.Field {
	if p.theme == nil {
		p.theme = theme
	}
	return p
}

// WithAccessible implements huh.Field.
func (p *pickerField) WithAccessible(bool) huh.Field { return p }

// WithKeyMap implements huh.Field. The field's keys are its own.
func (p *pickerField) WithKeyMap(*huh.KeyMap) huh.Field { return p }

// WithWidth implements huh.Field.
func (p *pickerField) WithWidth(width int) huh.Field {
	p.width = width
	return p
}

// WithHeight implements huh.Field.
func (p *pickerField) WithHeight(height int) huh.Field {
	p.height = height
	return p
}

// WithPosition implements huh.Field.
func (p *pickerField) WithPosition(position huh.FieldPosition) huh.Field {
	p.keymap.Prev.SetEnabled(!position.IsFirst())
	return p
}

// GetKey implements huh.Field.
func (p *pickerField) GetKey() string { return p.key }

// GetValue implements huh.Field.
func (p *pickerField) GetValue() any { return *p.value }

// Run implements huh.Field. The field lives in the recipe form only.
func (p *pickerField) Run() error {
	return errors.New("the package picker runs inside the recipe form")
}

// RunAccessible implements huh.Field; the plain interface asks this
// question as a line of text instead.
func (p *pickerField) RunAccessible(io.Writer, io.Reader) error { return p.Run() }
