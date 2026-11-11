package cli

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"frostroot/internal/form"
	"frostroot/internal/recipe"
	"frostroot/internal/tui"
)

// formRun is one time the full-screen form was shown: what it asked, the
// answers it started from, and what its summary page warned of.
type formRun struct {
	fields  []form.Field
	initial form.Values
	warning string
}

// TestTheFullScreenFormReturnsToAnswersThatCannotBeWritten: the form never
// validated, the summary's Write did, and answers it then refused, such as
// a disk image of Ubuntu 22.04, ended the command with every answer of the
// session lost. The form says so on its summary page now, and Write goes
// back to the answers with what is wrong with them.
func TestTheFullScreenFormReturnsToAnswersThatCannotBeWritten(t *testing.T) {
	recipeDir := t.TempDir()
	var stderr bytes.Buffer
	var runs []formRun
	app := App{
		Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: &stderr, RecipeDir: recipeDir, ReadFile: noHostFile,
		IsTerminal:  terminalChecker(true),
		Getenv:      environmentWith(map[string]string{"TERM": "xterm-256color", "XDG_CACHE_HOME": t.TempDir()}),
		IndexClient: &http.Client{Transport: failingTransport{}},
	}
	app.runForm = func(_ context.Context, fields []form.Field, initial form.Values, preview tui.PreviewFunc, _ io.Reader, _ io.Writer) (form.Values, error) {
		answers := maps.Clone(initial)
		if len(runs) == 0 {
			answers[form.KeyImageName] = "pc-lab"
			answers[form.KeyTarget] = recipe.TargetDisk
			answers[form.KeyRelease] = "22.04"
		} else {
			answers[form.KeyRelease] = "24.04"
		}
		runs = append(runs, formRun{fields: fields, initial: initial, warning: preview(answers).Warning})
		return answers, nil
	}
	if exitCode := app.Run([]string{"init"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, stderr %s", exitCode, stderr.String())
	}
	if len(runs) != 2 {
		t.Fatalf("the form was shown %d times, want twice: once refused, once fixed", len(runs))
	}
	problem := "a disk image is built for Ubuntu 24.04 or 26.04, not 22.04"
	if !strings.Contains(runs[0].warning, problem) || !strings.Contains(runs[0].warning, "Write takes you back") {
		t.Errorf("the first summary warned %q, want the problem and what Write does", runs[0].warning)
	}
	note := runs[1].fields[0]
	if note.Kind != form.KindNote || note.Page != form.PageImage || !strings.Contains(note.Description, problem) {
		t.Errorf("the form came back with %+v first, want a note on the Image page naming the problem", note)
	}
	if runs[1].initial.String(form.KeyImageName) != "pc-lab" || runs[1].initial.String(form.KeyTarget) != recipe.TargetDisk {
		t.Errorf("the form came back with %v, want the answers given", runs[1].initial)
	}
	if runs[1].warning != "" {
		t.Errorf("the fixed answers' summary warned %q", runs[1].warning)
	}
	written := loadWrittenRecipe(t, recipeDir)
	if written.Image.Name != "pc-lab" || written.Image.Release != "24.04" || written.Image.Target != recipe.TargetDisk {
		t.Errorf("wrote %+v, want the answers as fixed", written.Image)
	}
}
