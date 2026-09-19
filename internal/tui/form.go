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
// none wraps.
func (s *summaryModel) resize(width, height int) {
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

// View renders the page.
func (s *summaryModel) View() string {
	var view strings.Builder
	view.WriteString(titleStyle.Render("Summary"))
	view.WriteByte('\n')
	view.WriteString(s.summary)
	view.WriteByte('\n')
	if s.warning != "" {
		view.WriteByte('\n')
		view.WriteString(warningStyle.Render(s.warning))
		view.WriteByte('\n')
	}
	if s.heading != "" {
		view.WriteByte('\n')
		view.WriteString(s.heading)
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
// the initial values everything else is copied from.
type formBinding struct {
	fields  []form.Field
	initial form.Values
	texts   map[string]*string
	flags   map[string]*bool
	lists   map[string]*[]string
	ctx     context.Context // ends with the form
	glyphs  glyphSet
}

func newFormBinding(ctx context.Context, fields []form.Field, initial form.Values, glyphs glyphSet) *formBinding {
	binding := &formBinding{
		fields:  fields,
		initial: initial,
		texts:   map[string]*string{},
		flags:   map[string]*bool{},
		lists:   map[string]*[]string{},
		ctx:     ctx,
		glyphs:  glyphs,
	}
	for _, field := range fields {
		switch field.Kind {
		case form.KindInput, form.KindSelect, form.KindSearch:
			text := initial.String(field.Key)
			binding.texts[field.Key] = &text
		case form.KindConfirm:
			flag := initial.Bool(field.Key)
			binding.flags[field.Key] = &flag
		case form.KindMultiSelect:
			list := slices.Clone(initial.Strings(field.Key))
			binding.lists[field.Key] = &list
		}
	}
	return binding
}

// groups returns one huh group per form page, in page order.
func (b *formBinding) groups() []*huh.Group {
	var groups []*huh.Group
	for _, page := range form.Pages() {
		var huhFields []huh.Field
		for _, field := range b.fields {
			if field.Page == page {
				huhFields = append(huhFields, b.huhField(field))
			}
		}
		if len(huhFields) > 0 {
			groups = append(groups, huh.NewGroup(huhFields...).Title(page))
		}
	}
	return groups
}

// huhField renders one form field as the matching huh widget.
func (b *formBinding) huhField(field form.Field) huh.Field {
	switch field.Kind {
	case form.KindInput:
		input := huh.NewInput().Key(field.Key).Title(field.Title).Description(field.Description).
			Placeholder(field.Placeholder).Value(b.texts[field.Key])
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
			Options(options...).Height(selectListHeight).Value(b.texts[field.Key])
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
			Options(options...).Filterable(field.Filterable).Height(multiSelectListHeight).Value(b.lists[field.Key])
	case form.KindConfirm:
		return huh.NewConfirm().Key(field.Key).Title(field.Title).Description(field.Description).
			Affirmative("Yes").Negative("No").Value(b.flags[field.Key])
	case form.KindSearch:
		return newPickerField(b.ctx, field, b.texts[field.Key], b.answeredRelease, b.chosenInCatalog, b.glyphs)
	case form.KindNote:
		// The text is escaped: a note renders its own markup, and what
		// capture found is full of underscores. A button to move on makes
		// the page a page rather than something huh skips over.
		return huh.NewNote().Title(field.Title).Description(noteText(field.Description)).
			Next(true).NextLabel("Continue")
	}
	return huh.NewNote().Title(field.Title).Description("unsupported field kind")
}

// answeredRelease is the Ubuntu release as answered so far: what the
// package picker opens the index of.
func (b *formBinding) answeredRelease() string {
	if release, isAsked := b.texts[form.KeyRelease]; isAsked {
		return *release
	}
	return b.initial.String(form.KeyRelease)
}

// chosenInCatalog is the catalog's answer so far. huh writes it on every
// toggle, so the picker can mark those names as chosen; it cannot change
// them, because the catalog keeps its own state and would write it back.
func (b *formBinding) chosenInCatalog() []string {
	if chosen, isAsked := b.lists[form.KeyPackages]; isAsked {
		return *chosen
	}
	return b.initial.Strings(form.KeyPackages)
}

// values returns the answers: everything in initial, overwritten by what
// the widgets collected.
func (b *formBinding) values() form.Values {
	values := b.initial.Clone()
	for key, text := range b.texts {
		values[key] = *text
	}
	for key, flag := range b.flags {
		values[key] = *flag
	}
	for key, list := range b.lists {
		values[key] = slices.Clone(*list)
	}
	return values
}
