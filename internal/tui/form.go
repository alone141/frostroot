// Package tui is frostroot's full-screen terminal interface: the recipe form
// that init and edit show, and the build screen. It is the only package that
// imports the Charm libraries; what it shows is computed elsewhere, in
// internal/form and internal/builder, without any terminal.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"frostroot/internal/distro"
	"frostroot/internal/form"
)

// ErrCanceled means the user left the form without finishing it: Esc,
// Ctrl-C, or declining to write on the summary page.
var ErrCanceled = errors.New("canceled")

// Heights of the scrolling lists, in rows.
const (
	selectListHeight      = 8
	multiSelectListHeight = 12
)

// inlineSelectOptions is the most options a select shows on one line,
// switched between with the left and right keys: the family, and Fedora's
// one release. A list of them would be two rows of choice under rows of
// padding.
const inlineSelectOptions = 2

// Sizes of the summary page, in rows and cells.
const (
	// previewMinimumHeight is the fewest rows the preview pane keeps on a
	// short terminal: the question below it has to stay on screen.
	previewMinimumHeight = 3
	// questionRows is what the write question takes with its help line.
	questionRows = 6
	// previewFrameRows is what surrounds the pane: the blank line above
	// it, the heading, the border, and the blank line below.
	previewFrameRows = 6
	// previewMinimumWidth keeps the pane readable on an absurd terminal.
	previewMinimumWidth = 10
)

// Preview is what the last page shows above the write question: the recipe
// as it will be written, or how the file will change.
type Preview struct {
	Heading   string // one line above the text
	Text      string // the rendered recipe, or a line diff of it; "" shows the summary alone
	Unchanged bool   // writing would leave the file exactly as it is
	Warning   string // what is worth knowing before writing, shown above the heading; "" says nothing
}

// PreviewFunc renders the preview for the answers so far. nil means the
// last page shows the summary alone, as it did before previews.
type PreviewFunc func(form.Values) Preview

// RunForm shows fields as a full-screen form, one page per form page,
// starting from initial, then a summary page with the preview that asks to
// write the recipe. It returns the answers, or ErrCanceled.
func RunForm(ctx context.Context, fields []form.Field, initial form.Values, preview PreviewFunc, input io.Reader, output io.Writer) (form.Values, error) {
	// The form's context ends with the form, so that a package index still
	// being fetched when the user finishes or leaves is not fetched further.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	model := newFormModel(ctx, fields, initial, preview)
	program := tea.NewProgram(model, tea.WithInput(input), tea.WithOutput(output), tea.WithContext(ctx))
	finalModel, err := program.Run()
	switch {
	case errors.Is(err, tea.ErrInterrupted):
		return nil, ErrCanceled
	case errors.Is(err, tea.ErrProgramKilled) && ctx.Err() != nil:
		return nil, fmt.Errorf("running the form: %w", ctx.Err())
	case err != nil:
		return nil, fmt.Errorf("running the form: %w", err)
	}
	finished, isFormModel := finalModel.(*formModel)
	if !isFormModel || finished.stage != stageDone || !finished.write {
		return nil, ErrCanceled
	}
	return finished.binding.values(), nil
}

// formStage is where the form is: answering the pages, confirming on the
// summary, or finished.
type formStage int

const (
	stagePages formStage = iota
	stageSummary
	stageDone
)

// formModel runs the question pages as one huh form, then the summary page,
// in one Bubble Tea program. One program, so one input stream, which also
// keeps scripted tests honest.
type formModel struct {
	binding *formBinding
	preview PreviewFunc
	glyphs  glyphSet
	pages   *huh.Form
	summary *summaryModel
	stage   formStage
	write   bool
	width   int
	height  int
}

func newFormModel(ctx context.Context, fields []form.Field, initial form.Values, preview PreviewFunc) *formModel {
	glyphs := glyphsForTerminal()
	binding := newFormBinding(ctx, fields, initial, glyphs)
	return &formModel{
		binding: binding,
		preview: preview,
		glyphs:  glyphs,
		pages:   huh.NewForm(binding.groups()...).WithTheme(formTheme(glyphs)),
		width:   defaultWidth,
		height:  defaultHeight,
	}
}

// Init implements tea.Model.
func (m *formModel) Init() tea.Cmd { return m.pages.Init() }

// Update implements tea.Model: it forwards to the running page and moves
// on when that page completes or aborts.
func (m *formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, isSize := msg.(tea.WindowSizeMsg); isSize {
		m.width, m.height = size.Width, size.Height
		if m.summary != nil {
			m.summary.resize(m.width, m.height)
		}
	}
	switch m.stage {
	case stagePages:
		updated, cmd := m.pages.Update(msg)
		m.pages = asHuhForm(updated, m.pages)
		switch m.pages.State {
		case huh.StateAborted:
			return m, tea.Interrupt
		case huh.StateCompleted:
			m.stage = stageSummary
			values := m.binding.values()
			var preview Preview
			if m.preview != nil {
				preview = m.preview(values)
			}
			m.summary = newSummaryModel(form.Summary(values), preview, m.glyphs, m.width, m.height, &m.write)
			return m, tea.Batch(cmd, m.summary.Init())
		case huh.StateNormal:
		}
		return m, cmd
	case stageSummary:
		// The summary's warning is computed from the package index as it
		// stood when the questions ended. On a cold cache or with
		// --refresh-index that fetch can still be in flight: names are
		// typed into a picker that is a plain list editor until it lands,
		// Enter reaches this page, and the warning about names no archive
		// has would be missing from the one screen that asks whether to
		// write. The index arriving is a reason to say it now.
		if _, indexLoaded := msg.(pickerLoadedMsg); indexLoaded && m.preview != nil {
			m.summary.setPreview(m.preview(m.binding.values()), m.width, m.height)
		}
		cmd, state := m.summary.Update(msg)
		switch state {
		case huh.StateAborted:
			return m, tea.Interrupt
		case huh.StateCompleted:
			m.stage = stageDone
			return m, tea.Quit
		case huh.StateNormal:
		}
		return m, cmd
	case stageDone:
	}
	return m, nil
}

// asHuhForm returns updated as the form it is. huh's Update always returns
// the form itself; current is kept should that ever change.
func asHuhForm(updated tea.Model, current *huh.Form) *huh.Form {
	if huhForm, isForm := updated.(*huh.Form); isForm {
		return huhForm
	}
	return current
}

// View implements tea.Model.
func (m *formModel) View() string {
	switch m.stage {
	case stagePages:
		return m.pages.View()
	case stageSummary:
		return m.summary.View()
	case stageDone:
	}
	return ""
}

// summaryModel is the last page: the summary of the answers, the preview
// of the file in a pane that scrolls, and the question whether to write.
// The question is a huh form of one confirm, so its keys, help line and
// theme are the ones the pages had; the pane takes the scrolling keys
// before the question sees anything.
type summaryModel struct {
	summary  string
	heading  string
	warning  string
	text     string // the preview, whole; the pane shows it cut to its width
	width    int    // the terminal's, or 0 before it has said
	glyphs   glyphSet
	pane     viewport.Model
	hasPane  bool
	question *huh.Form
}

// newSummaryModel builds the page. write starts as yes, unless writing
// would change nothing, in which case the question says so and starts as
// no.
func newSummaryModel(summary string, preview Preview, glyphs glyphSet, width, height int, write *bool) *summaryModel {
	*write = !preview.Unchanged
	title := "Write frostroot.toml?"
	if preview.Unchanged {
		title = "Nothing changes. Write anyway?"
	}
	page := &summaryModel{
		summary: summary,
		heading: preview.Heading,
		warning: strings.TrimRight(preview.Warning, "\n"),
		text:    strings.TrimRight(preview.Text, "\n"),
		glyphs:  glyphs,
		hasPane: preview.Text != "",
		question: huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title(title).Affirmative("Write").Negative("Cancel").Value(write),
		)).WithTheme(formTheme(glyphs)),
	}
	if page.hasPane {
		page.pane = viewport.New(previewMinimumWidth, previewMinimumHeight)
	}
	page.resize(width, height)
	return page
}

// Init starts the question.
func (s *summaryModel) Init() tea.Cmd { return s.question.Init() }

// setPreview replaces what the page shows above the question when the
// preview is worth computing again, such as a package index that finished
// loading after the questions did. The question itself is left alone: it
// holds the answer and the focus, and the values it is about have not
// changed.
func (s *summaryModel) setPreview(preview Preview, width, height int) {
	s.heading = preview.Heading
	s.warning = strings.TrimRight(preview.Warning, "\n")
	s.text = strings.TrimRight(preview.Text, "\n")
	s.hasPane = preview.Text != ""
	if s.hasPane && s.pane.Width == 0 {
		s.pane = viewport.New(previewMinimumWidth, previewMinimumHeight)
	}
	s.resize(width, height)
}

// resize fits the pane to the terminal, leaving the summary above and the
// question below their rows, and cuts the lines to the pane's width so that
// none wraps. The summary and the warning are cut when they are drawn, to
// the width kept here: capture answers with hundreds of packages on one
// line, and a line that wrapped would take rows this arithmetic did not
// count, until the heading, the answers and the question had scrolled off
// the one screen that asks whether to write.
func (s *summaryModel) resize(width, height int) {
	s.width = width
	if !s.hasPane {
		return
	}
	summaryRows := strings.Count(s.summary, "\n") + 2 // its lines and the title
	minimumHeight := previewMinimumHeight
	if s.warning != "" {
		summaryRows += strings.Count(s.warning, "\n") + 2 // its lines and the blank one above
		// On a 24-row terminal the warning and a three-row pane do not both
		// fit, and what scrolls off is the top: the warning. The pane gives
		// way; it still scrolls.
		minimumHeight = 1
	}
	s.pane.Width = max(previewMinimumWidth, width-4)
	s.pane.Height = max(minimumHeight, height-summaryRows-previewFrameRows-questionRows)
	s.pane.SetContent(fitLines(s.text, s.pane.Width, s.glyphs.ellipsis))
}

// Update routes a key to the pane when it scrolls, and everything else to
// the question. It returns the question's state, which is the page's.
func (s *summaryModel) Update(msg tea.Msg) (tea.Cmd, huh.FormState) {
	if key, isKey := msg.(tea.KeyMsg); isKey && s.hasPane && isScrollKey(key) {
		var cmd tea.Cmd
		s.pane, cmd = s.pane.Update(msg)
		return cmd, huh.StateNormal
	}
	updated, cmd := s.question.Update(msg)
	s.question = asHuhForm(updated, s.question)
	return cmd, s.question.State
}

// isScrollKey reports whether key belongs to the pane: the arrows and page
// keys, and j and k. Left, right, y, n, Enter and Tab belong to the
// question.
func isScrollKey(key tea.KeyMsg) bool {
	switch key.Type {
	case tea.KeyUp, tea.KeyDown, tea.KeyPgUp, tea.KeyPgDown, tea.KeyHome, tea.KeyEnd:
		return true
	default:
		return key.String() == "j" || key.String() == "k"
	}
}

// fitted cuts every line of text to the terminal's width, with an ellipsis,
// the way the pane cuts the preview; before the terminal has said its width
// nothing is cut.
func (s *summaryModel) fitted(text string) string {
	if s.width <= 0 {
		return text
	}
	return fitLines(text, s.width, s.glyphs.ellipsis)
}

// View renders the page.
func (s *summaryModel) View() string {
	var view strings.Builder
	view.WriteString(titleStyle.Render("Summary"))
	view.WriteByte('\n')
	view.WriteString(s.fitted(s.summary))
	view.WriteByte('\n')
	if s.warning != "" {
		view.WriteByte('\n')
		view.WriteString(warningStyle.Render(s.fitted(s.warning)))
		view.WriteByte('\n')
	}
	if s.heading != "" {
		view.WriteByte('\n')
		view.WriteString(s.fitted(s.heading))
		view.WriteByte('\n')
	}
	if s.hasPane {
		view.WriteString(paneStyleWith(s.glyphs.border).Width(s.pane.Width + 2).Render(s.pane.View()))
		view.WriteByte('\n')
		if s.pane.TotalLineCount() > s.pane.Height {
			view.WriteString(dimStyle.Render(fmt.Sprintf("  %s scroll · %d more lines", s.glyphs.scrollHint, s.pane.TotalLineCount()-s.pane.Height)))
			view.WriteByte('\n')
		}
	}
	view.WriteByte('\n')
	view.WriteString(s.question.View())
	return view.String()
}

// formTheme is the look of every form. Base16 uses the terminal's own
// sixteen colors, so it follows the user's palette on light and dark
// terminals alike, and it degrades to plain text where colors are off. On
// a terminal whose locale is not UTF-8 the theme's box drawing and marks
// become ASCII; the colors stay, since they have nothing to do with it.
func formTheme(glyphs glyphSet) *huh.Theme {
	theme := huh.ThemeBase16()
	if !glyphs.ascii {
		return theme
	}
	for _, styles := range []*huh.FieldStyles{&theme.Focused, &theme.Blurred} {
		styles.Base = styles.Base.BorderStyle(glyphs.border)
		styles.SelectedPrefix = styles.SelectedPrefix.SetString("[x] ")
		styles.UnselectedPrefix = styles.UnselectedPrefix.SetString("[ ] ")
		styles.NextIndicator = styles.NextIndicator.SetString(">")
		styles.PrevIndicator = styles.PrevIndicator.SetString("<")
	}
	// The help line renders its own " • " through this style, so the bullet
	// is replaced rather than joined by a second separator.
	asciiSeparator := func(string) string { return " | " }
	theme.Help.ShortSeparator = theme.Help.ShortSeparator.Transform(asciiSeparator)
	theme.Help.FullSeparator = theme.Help.FullSeparator.Transform(asciiSeparator)
	return theme
}

// noteMarkup escapes what a huh note reads as markup. Its description is
// rendered with a small markup language in which "_" and "*" toggle italic
// and bold and "`" opens a code span, so a note showing en_US.UTF-8 would
// show enUS.UTF-8 with everything after it in italics. A backslash makes
// the next character literal.
var noteMarkup = strings.NewReplacer(`\`, `\\`, "_", `\_`, "*", `\*`, "`", "\\`")

// noteText returns text as a huh note shows it verbatim.
func noteText(text string) string { return noteMarkup.Replace(text) }

// formBinding holds the variables huh writes answers into, one per field, and
// the initial values everything else is copied from. Fields of different
// families may share a key, the release's or the packages', and each has a
// variable of its own, so that switching the family and back finds every
// answer where it was left.
type formBinding struct {
	fields  []form.Field
	initial form.Values
	texts   map[bindingKey]*string
	flags   map[bindingKey]*bool
	lists   map[bindingKey]*[]string
	ctx     context.Context // ends with the form
	glyphs  glyphSet
}

// bindingKey names a field's variable: its key, and the families that ask
// it, which are none for a field every family asks.
type bindingKey struct{ key, families string }

func bindingKeyOf(field form.Field) bindingKey {
	families := make([]string, len(field.Families))
	for position, family := range field.Families {
		families[position] = string(family)
	}
	return bindingKey{key: field.Key, families: strings.Join(families, ",")}
}

// familyKey is the variable of the question that chooses the family.
var familyKey = bindingKey{key: form.KeyDistro}

func newFormBinding(ctx context.Context, fields []form.Field, initial form.Values, glyphs glyphSet) *formBinding {
	binding := &formBinding{
		fields:  fields,
		initial: initial,
		texts:   map[bindingKey]*string{},
		flags:   map[bindingKey]*bool{},
		lists:   map[bindingKey]*[]string{},
		ctx:     ctx,
		glyphs:  glyphs,
	}
	initialFamily := form.Family(initial)
	for _, field := range fields {
		// A field of another family starts where switching to it puts the
		// answers: on its newest release, with the selections its catalog
		// has.
		start := initial
		if !field.AskedFor(initialFamily) {
			start = form.ForFamily(initial, field.Families[0])
		}
		key := bindingKeyOf(field)
		switch field.Kind {
		case form.KindInput, form.KindSelect, form.KindSearch:
			text := start.String(field.Key)
			binding.texts[key] = &text
		case form.KindConfirm:
			flag := start.Bool(field.Key)
			binding.flags[key] = &flag
		case form.KindMultiSelect:
			list := slices.Clone(start.Strings(field.Key))
			binding.lists[key] = &list
		}
	}
	return binding
}

// families are the families the form can be answered for: every one when it
// asks the family, and otherwise the one it started on.
func (b *formBinding) families() []distro.Family {
	if _, isAsked := b.texts[familyKey]; isAsked {
		return distro.Families()
	}
	return []distro.Family{form.Family(b.initial)}
}

// answeredFamily is the family as answered so far.
func (b *formBinding) answeredFamily() distro.Family {
	if answer, isAsked := b.texts[familyKey]; isAsked {
		return form.Family(form.Values{form.KeyDistro: *answer})
	}
	return form.Family(b.initial)
}

// groups returns the form's pages as huh groups, in page order. A page
// every family asks alike is one group, where a question each family asks
// its own way, the release, is one widget showing the answered family's. A
// page that some family asks otherwise, or not at all, is a group per
// family that has questions on it, hidden while another family is
// answered: Ubuntu's Sources, Packages and Trust, and Fedora's Packages.
// The family is asked on the first page, so by the time huh decides
// whether to show a group the answer that decides it is given.
func (b *formBinding) groups() []*huh.Group {
	families := b.families()
	var groups []*huh.Group
	for _, page := range form.Pages() {
		var onPage []form.Field
		for _, field := range b.fields {
			if field.Page == page {
				onPage = append(onPage, field)
			}
		}
		if len(onPage) == 0 {
			continue
		}
		if widgets, shared := b.sharedPage(onPage, families); shared {
			groups = append(groups, huh.NewGroup(widgets...).Title(page))
			continue
		}
		for _, family := range families {
			var widgets []huh.Field
			for _, field := range onPage {
				if field.AskedFor(family) {
					widgets = append(widgets, b.huhField(field))
				}
			}
			if len(widgets) > 0 {
				groups = append(groups, huh.NewGroup(widgets...).Title(page).WithHideFunc(func() bool { return b.answeredFamily() != family }))
			}
		}
	}
	return groups
}

// sharedPage returns the widgets of a page when every family asks each of
// its keys: a field that every family asks as one widget, and a key that
// each family asks with a field of its own as a familyField. false when a
// family asks nothing in the place of another family's field.
func (b *formBinding) sharedPage(onPage []form.Field, families []distro.Family) ([]huh.Field, bool) {
	var widgets []huh.Field
	placed := map[string]bool{}
	for _, field := range onPage {
		if placed[field.Key] {
			continue
		}
		placed[field.Key] = true
		fieldOf := map[distro.Family]form.Field{}
		var distinct []form.Field
		for _, candidate := range onPage {
			if candidate.Key != field.Key {
				continue
			}
			for _, family := range families {
				if candidate.AskedFor(family) {
					fieldOf[family] = candidate
				}
			}
			distinct = append(distinct, candidate)
		}
		if len(fieldOf) < len(families) {
			return nil, false
		}
		if len(distinct) == 1 {
			widgets = append(widgets, b.huhField(field))
			continue
		}
		widgetOf := map[bindingKey]huh.Field{}
		for _, candidate := range distinct {
			widgetOf[bindingKeyOf(candidate)] = b.huhField(candidate)
		}
		variants := &familyField{key: field.Key, families: families, widgets: map[distro.Family]huh.Field{}, answered: b.answeredFamily}
		for family, candidate := range fieldOf {
			variants.widgets[family] = widgetOf[bindingKeyOf(candidate)]
		}
		widgets = append(widgets, variants)
	}
	return widgets, true
}

// huhField renders one form field as the matching huh widget.
func (b *formBinding) huhField(field form.Field) huh.Field {
	key := bindingKeyOf(field)
	switch field.Kind {
	case form.KindInput:
		input := huh.NewInput().Key(field.Key).Title(field.Title).Description(field.Description).
			Placeholder(field.Placeholder).Value(b.texts[key])
		if field.Validate != nil {
			input.Validate(field.Validate)
		}
		return input
	case form.KindSelect:
		options := make([]huh.Option[string], 0, len(field.Options))
		for _, option := range field.Options {
			label := option.DisplayLabel()
			if option.Description != "" {
				label += "  (" + option.Description + ")"
			}
			options = append(options, huh.NewOption(label, option.Value))
		}
		selectField := huh.NewSelect[string]().Key(field.Key).Title(field.Title).Description(field.Description).
			Options(options...).Value(b.texts[key])
		switch {
		case len(options) <= inlineSelectOptions:
			selectField.Inline(true)
		case field.Filterable || len(options) > selectListHeight:
			selectField.Height(selectListHeight)
		}
		if field.Filterable {
			selectField.Filtering(true)
		}
		return selectField
	case form.KindMultiSelect:
		options := make([]huh.Option[string], 0, len(field.Options))
		for _, option := range field.Options {
			options = append(options, huh.NewOption(option.DisplayLabel(), option.Value))
		}
		return huh.NewMultiSelect[string]().Key(field.Key).Title(field.Title).Description(field.Description).
			Options(options...).Filterable(field.Filterable).Height(multiSelectListHeight).Value(b.lists[key])
	case form.KindConfirm:
		return huh.NewConfirm().Key(field.Key).Title(field.Title).Description(field.Description).
			Affirmative("Yes").Negative("No").Value(b.flags[key])
	case form.KindSearch:
		return newPickerField(b.ctx, field, b.texts[key], b.requestFor(field), b.chosenInCatalog(field), b.glyphs)
	case form.KindNote:
		// The text is escaped: a note renders its own markup, and what
		// capture found is full of underscores. A button to move on makes
		// the page a page rather than something huh skips over.
		return huh.NewNote().Title(field.Title).Description(noteText(field.Description)).
			Next(true).NextLabel("Continue")
	}
	return huh.NewNote().Title(field.Title).Description("unsupported field kind")
}

// requestFor is what a search field opens its index of: the family and
// release and, for a field whose index covers them, the sources as answered
// so far. Those pages come before the picker's, so a source chosen a moment
// ago is a source it searches. A field whose index is PyPI is not told
// about them, or ticking a source would abandon a download of it and start
// again, and Fedora's has none to be told of.
func (b *formBinding) requestFor(field form.Field) func() form.IndexRequest {
	if !field.Sourced {
		return func() form.IndexRequest {
			return form.IndexRequest{Family: b.answeredFamily(), Release: b.answeredRelease()}
		}
	}
	return func() form.IndexRequest { return form.IndexRequestFor(b.values()) }
}

// answeredRelease is the answered family's release as answered so far.
func (b *formBinding) answeredRelease() string {
	family := b.answeredFamily()
	for _, field := range b.fields {
		if field.Key == form.KeyRelease && field.AskedFor(family) {
			return *b.texts[bindingKeyOf(field)]
		}
	}
	return b.initial.String(form.KeyRelease)
}

// chosenInCatalog returns the answer so far of the catalog a search field's
// family offers beside it. huh writes it on every toggle, so the picker can
// mark those names as chosen; it cannot change them, because the catalog
// keeps its own state and would write it back.
func (b *formBinding) chosenInCatalog(picker form.Field) func() []string {
	return func() []string {
		family := b.answeredFamily()
		if len(picker.Families) > 0 {
			family = picker.Families[0]
		}
		for _, field := range b.fields {
			if field.Key == form.KeyPackages && field.AskedFor(family) {
				return *b.lists[bindingKeyOf(field)]
			}
		}
		return b.initial.Strings(form.KeyPackages)
	}
}

// values returns the answers: everything in initial, overwritten by what
// the widgets of the answered family's questions collected. An answer to a
// question the family does not ask stays as it started, and ToRecipe leaves
// it out.
func (b *formBinding) values() form.Values {
	values := b.initial.Clone()
	family := b.answeredFamily()
	for _, field := range b.fields {
		if !field.AskedFor(family) {
			continue
		}
		key := bindingKeyOf(field)
		switch field.Kind {
		case form.KindInput, form.KindSelect, form.KindSearch:
			values[field.Key] = *b.texts[key]
		case form.KindConfirm:
			values[field.Key] = *b.flags[key]
		case form.KindMultiSelect:
			values[field.Key] = slices.Clone(*b.lists[key])
		case form.KindNote:
		}
	}
	return values
}
