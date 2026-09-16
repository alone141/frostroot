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

// RunForm shows fields as a full-screen form, one page per form page,
// starting from initial, then a summary page that asks to write the recipe.
// It returns the answers, or ErrCanceled.
func RunForm(ctx context.Context, fields []form.Field, initial form.Values, input io.Reader, output io.Writer) (form.Values, error) {
	model := newFormModel(fields, initial)
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

// formModel runs two huh forms in one Bubble Tea program: the question pages,
// then a summary built from the answers with the question whether to write.
// One program, so one input stream, which also keeps scripted tests honest.
type formModel struct {
	binding *formBinding
	pages   *huh.Form
	summary *huh.Form
	stage   formStage
	write   bool
}

func newFormModel(fields []form.Field, initial form.Values) *formModel {
	binding := newFormBinding(fields, initial)
	return &formModel{
		binding: binding,
		pages:   huh.NewForm(binding.groups()...).WithTheme(formTheme()),
	}
}

// Init implements tea.Model.
func (m *formModel) Init() tea.Cmd { return m.pages.Init() }

// Update implements tea.Model: it forwards to the running huh form and moves
// on when that form completes or aborts.
func (m *formModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch m.stage {
	case stagePages:
		updated, cmd := m.pages.Update(msg)
		m.pages = asHuhForm(updated, m.pages)
		switch m.pages.State {
		case huh.StateAborted:
			return m, tea.Interrupt
		case huh.StateCompleted:
			m.stage = stageSummary
			m.summary = newSummaryForm(form.Summary(m.binding.values()), &m.write).WithTheme(formTheme())
			return m, tea.Batch(cmd, m.summary.Init())
		case huh.StateNormal:
		}
		return m, cmd
	case stageSummary:
		updated, cmd := m.summary.Update(msg)
		m.summary = asHuhForm(updated, m.summary)
		switch m.summary.State {
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

// newSummaryForm builds the last page: the summary and the write question.
func newSummaryForm(summary string, write *bool) *huh.Form {
	*write = true
	return huh.NewForm(huh.NewGroup(
		huh.NewNote().Title("Summary").Description(summary),
		huh.NewConfirm().Title("Write frostroot.toml?").Affirmative("Write").Negative("Cancel").Value(write),
	))
}

// formTheme is the look of every form. Base16 uses the terminal's own
// sixteen colors, so it follows the user's palette on light and dark
// terminals alike, and it degrades to plain text where colors are off.
func formTheme() *huh.Theme { return huh.ThemeBase16() }

// formBinding holds the variables huh writes answers into, one per field, and
// the initial values everything else is copied from.
type formBinding struct {
	fields  []form.Field
	initial form.Values
	texts   map[string]*string
	flags   map[string]*bool
	lists   map[string]*[]string
}

func newFormBinding(fields []form.Field, initial form.Values) *formBinding {
	binding := &formBinding{
		fields:  fields,
		initial: initial,
		texts:   map[string]*string{},
		flags:   map[string]*bool{},
		lists:   map[string]*[]string{},
	}
	for _, field := range fields {
		switch field.Kind {
		case form.KindInput, form.KindSelect:
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
	}
	return huh.NewNote().Title(field.Title).Description("unsupported field kind")
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
