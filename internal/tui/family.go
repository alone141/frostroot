package tui

import (
	"io"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"frostroot/internal/distro"
)

// familyField is one question that each family asks its own way on a page
// every family shares, such as the release on the Image page: a widget per
// family, of which the answered family's is the one shown, focused and
// given the keys. huh can hide a whole group but not one field of it, and
// a select whose options followed the family through huh's OptionsFunc
// would come back to Ubuntu from Fedora with its cursor on 20.04, the first
// option, rather than where it was. Each widget here keeps its own cursor
// and its own variable, so switching the family and back finds the answer
// where it was left.
type familyField struct {
	key      string
	families []distro.Family // the order the widgets are kept in
	widgets  map[distro.Family]huh.Field
	answered func() distro.Family
}

// current is the widget of the family answered so far. A family the field
// has no widget for cannot be answered on a page every family shares; the
// first widget stands in, so that the field is never without one.
func (f *familyField) current() huh.Field {
	if widget, found := f.widgets[f.answered()]; found {
		return widget
	}
	return f.widgets[f.families[0]]
}

// each calls do with every widget, in family order, and keeps what it
// returns.
func (f *familyField) each(do func(huh.Field) huh.Field) {
	for _, family := range f.families {
		f.widgets[family] = do(f.widgets[family])
	}
}

// Init implements huh.Field.
func (f *familyField) Init() tea.Cmd {
	var cmds []tea.Cmd
	for _, family := range f.families {
		cmds = append(cmds, f.widgets[family].Init())
	}
	return tea.Batch(cmds...)
}

// Update gives a key to the answered family's widget alone, and anything
// else to every widget, so that one shown after a switch is as up to date
// as the one it replaces.
func (f *familyField) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	answered := f.answered()
	_, isKey := msg.(tea.KeyMsg)
	var cmds []tea.Cmd
	for _, family := range f.families {
		if isKey && family != answered {
			continue
		}
		model, cmd := f.widgets[family].Update(msg)
		if widget, isField := model.(huh.Field); isField {
			f.widgets[family] = widget
		}
		cmds = append(cmds, cmd)
	}
	return f, tea.Batch(cmds...)
}

// View implements huh.Field.
func (f *familyField) View() string { return f.current().View() }

// Blur implements huh.Field.
func (f *familyField) Blur() tea.Cmd { return f.current().Blur() }

// Focus implements huh.Field.
func (f *familyField) Focus() tea.Cmd { return f.current().Focus() }

// Error implements huh.Field.
func (f *familyField) Error() error { return f.current().Error() }

// Run implements huh.Field.
func (f *familyField) Run() error { return f.current().Run() }

// RunAccessible implements huh.Field.
func (f *familyField) RunAccessible(w io.Writer, r io.Reader) error {
	return f.current().RunAccessible(w, r)
}

// Skip implements huh.Field.
func (f *familyField) Skip() bool { return f.current().Skip() }

// Zoom implements huh.Field.
func (f *familyField) Zoom() bool { return f.current().Zoom() }

// KeyBinds implements huh.Field.
func (f *familyField) KeyBinds() []key.Binding { return f.current().KeyBinds() }

// WithTheme implements huh.Field.
func (f *familyField) WithTheme(theme *huh.Theme) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithTheme(theme) })
	return f
}

// WithAccessible implements huh.Field. huh's accessible mode is not used:
// the plain interface is frostroot's line-by-line one.
func (f *familyField) WithAccessible(bool) huh.Field { return f }

// WithKeyMap implements huh.Field.
func (f *familyField) WithKeyMap(keyMap *huh.KeyMap) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithKeyMap(keyMap) })
	return f
}

// WithWidth implements huh.Field.
func (f *familyField) WithWidth(width int) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithWidth(width) })
	return f
}

// WithHeight implements huh.Field.
func (f *familyField) WithHeight(height int) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithHeight(height) })
	return f
}

// WithPosition implements huh.Field.
func (f *familyField) WithPosition(position huh.FieldPosition) huh.Field {
	f.each(func(widget huh.Field) huh.Field { return widget.WithPosition(position) })
	return f
}

// GetKey implements huh.Field.
func (f *familyField) GetKey() string { return f.key }

// GetValue implements huh.Field.
func (f *familyField) GetValue() any { return f.current().GetValue() }
