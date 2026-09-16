package tui

import (
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// driver feeds messages to a model the way a Bubble Tea program does,
// running every returned command at once, so tests need no timing.
type driver struct {
	t           *testing.T
	model       tea.Model
	quit        bool
	interrupted bool
}

// commandPatience is how long settle waits for a command. The forms' own
// commands (move to the next field, and so on) return at once; a cursor
// blink, a spinner tick or a wait on an empty channel sleeps first, and
// following those would loop or block forever, so they are dropped.
const commandPatience = 50 * time.Millisecond

func newDriver(t *testing.T, model tea.Model) *driver {
	t.Helper()
	d := &driver{t: t, model: model}
	d.settle(d.model.Init())
	return d
}

// press sends one message and settles what follows from it.
func (d *driver) press(msg tea.Msg) {
	updated, cmd := d.model.Update(msg)
	d.model = updated
	d.settle(cmd)
}

// settle runs cmd and feeds its results back until nothing is pending.
func (d *driver) settle(cmd tea.Cmd) {
	if cmd == nil || d.quit || d.interrupted {
		return
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	var msg tea.Msg
	select {
	case msg = <-result:
	case <-time.After(commandPatience):
		return // a timer or a wait; not part of the model's logic
	}
	switch msg := msg.(type) {
	case nil:
	case tea.QuitMsg:
		d.quit = true
	case tea.InterruptMsg:
		d.interrupted = true
	case tea.BatchMsg:
		for _, batched := range msg {
			d.settle(batched)
		}
	default:
		d.press(msg)
	}
}
