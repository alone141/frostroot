package cli

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"frostroot/internal/recipe"
)

// Keys as a terminal sends them.
const (
	keyEnter = "\r"
	keyRight = "\x1b[C"
	keyLeft  = "\x1b[D"
)

// What the form shows once Enter has moved it past its first question: the
// way back, which the first question does not offer.
const pastTheFirstQuestion = "shift+tab back"

// keystroke is a key a test types into the form, and what the screen shows
// once the form has taken it. huh moves focus with a command: Enter on a
// question returns one, and the message it makes reaches the program after
// the key did, from another goroutine. A key read before that message goes
// to the question Enter left, so a key meant for the next question waits
// until the screen shows it. On a slow machine, under -race, a pause of
// 30ms was not enough: the arrow meant for the family moved the cursor in
// the image name.
type keystroke struct {
	key  string
	then string // what the screen shows after key; "" types the next key a moment later
}

// escapeSequence matches what a terminal draws with rather than shows: the
// CSI and OSC sequences and the two-byte escapes.
var escapeSequence = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)|\x1b[@-Z\\-_]`)

// screenPatience is how long a keystroke waits for the screen to show what
// it names.
const screenPatience = 30 * time.Second

// formScreen keeps everything the form draws, so that a key is typed only
// once the form shows the question it is meant for. The renderer draws only
// the lines that changed, so what a keystroke waits for is text that
// appears only in the state it waits for.
type formScreen struct {
	mutex   sync.Mutex
	written []byte
	missed  string
	wrote   chan struct{}
}

// Write implements io.Writer.
func (s *formScreen) Write(p []byte) (int, error) {
	s.mutex.Lock()
	s.written = append(s.written, p...)
	s.mutex.Unlock()
	select {
	case s.wrote <- struct{}{}:
	default: // a wake-up is already pending
	}
	return len(p), nil
}

// length is how much has been drawn, the mark shows reads from.
func (s *formScreen) length() int {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return len(s.written)
}

// shows reports whether text was drawn after mark.
func (s *formScreen) shows(mark int, text string) bool {
	s.mutex.Lock()
	defer s.mutex.Unlock()
	return strings.Contains(escapeSequence.ReplaceAllString(string(s.written[mark:]), ""), text)
}

// waitFor waits until text is drawn after mark, for at most screenPatience.
func (s *formScreen) waitFor(mark int, text string) bool {
	deadline := time.After(screenPatience)
	for !s.shows(mark, text) {
		select {
		case <-s.wrote:
		case <-deadline:
			return false
		}
	}
	return true
}

// typeKeys types each keystroke into writer as its own write, a moment
// after the one before, and once a keystroke names what the screen then
// shows, only after it shows it. A screen that never does gets Ctrl-C,
// which ends the form, and missed says what it lacked.
func (s *formScreen) typeKeys(writer io.Writer, strokes []keystroke) {
	for _, stroke := range strokes {
		time.Sleep(30 * time.Millisecond)
		mark := s.length()
		if _, err := io.WriteString(writer, stroke.key); err != nil {
			return // the form has stopped reading
		}
		if stroke.then != "" && !s.waitFor(mark, stroke.then) {
			s.mutex.Lock()
			s.missed = stroke.then
			s.mutex.Unlock()
			_, _ = io.WriteString(writer, "\x03")
			return
		}
	}
	// Left open: a closed input would end the form early.
}

// runFullScreen runs a command in the full-screen form, typing keystrokes
// into it, then Enter until the form is done. The index client answers
// nothing, so that a picker that opens an index finds none rather than the
// network.
func runFullScreen(t *testing.T, recipeDir string, strokes []keystroke, args ...string) (exitCode int, stderr string) {
	t.Helper()
	for range 40 {
		strokes = append(strokes, keystroke{key: keyEnter})
	}
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }() // ends the typing goroutine; a pipe has nothing to lose
	screen := &formScreen{wrote: make(chan struct{}, 1)}
	go screen.typeKeys(writer, strokes)
	var stderrBuffer bytes.Buffer
	app := App{
		Stdin: reader, Stdout: screen, Stderr: &stderrBuffer, RecipeDir: recipeDir, ReadFile: noHostFile,
		IsTerminal:  terminalChecker(true),
		Getenv:      environmentWith(map[string]string{"TERM": "xterm-256color", "XDG_CACHE_HOME": t.TempDir()}),
		IndexClient: &http.Client{Transport: failingTransport{}},
	}
	exitCode = app.Run(args)
	screen.mutex.Lock()
	missed := screen.missed
	screen.mutex.Unlock()
	if missed != "" {
		t.Fatalf("the form never showed %q; stderr %s", missed, stderrBuffer.String())
	}
	return exitCode, stderrBuffer.String()
}

// TestCtrlCInTheFullScreenFormIsAnInterrupt: Ctrl-C at a question of the
// plain interface ends the process with SIGINT, 130; the same key in the
// full-screen form exited 1.
func TestCtrlCInTheFullScreenFormIsAnInterrupt(t *testing.T) {
	recipeDir := t.TempDir()
	exitCode, stderr := runFullScreen(t, recipeDir, []keystroke{{key: "\x03"}}, "init")
	if exitCode != exitInterrupted || !strings.Contains(stderr, "canceled; nothing written") {
		t.Fatalf("exit code = %d, stderr %q; want %d and nothing written", exitCode, stderr, exitInterrupted)
	}
	if _, err := os.Stat(filepath.Join(recipeDir, recipeFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a canceled form wrote %s: Stat error = %v", recipeFileName, err)
	}
}

// TestTheFullScreenFormAsksTheFamilyWhereItShould:init and edit ask the
// family on the Image page, between the name and the release, and a switch
// there writes the other family's recipe; init --distro answers it, so the
// page asks none.
func TestTheFullScreenFormAsksTheFamilyWhereItShould(t *testing.T) {
	student := recipe.User{Name: "student", Sudo: true}
	wsl := recipe.WSL{Systemd: true, DefaultUser: "student"}
	t.Run("init", func(t *testing.T) {
		recipeDir := t.TempDir()
		// Right waits for the family question, and Enter for Fedora's
		// releases in the question below it.
		strokes := []keystroke{{key: keyEnter, then: pastTheFirstQuestion}, {key: keyRight, then: "Fedora 44"}}
		if exitCode, stderr := runFullScreen(t, recipeDir, strokes, "init"); exitCode != exitSuccess {
			t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
		}
		want := recipe.Image{Name: "lab", Distro: "fedora", Release: "44", Arch: "amd64"}
		if got := loadWrittenRecipe(t, recipeDir).Image; got != want {
			t.Errorf("image = %+v, want %+v", got, want)
		}
	})
	t.Run("init --distro", func(t *testing.T) {
		// Left would move a family question back to Ubuntu; there is none,
		// and the release beside the name has Fedora's one option.
		recipeDir := t.TempDir()
		strokes := []keystroke{{key: keyEnter, then: pastTheFirstQuestion}, {key: keyLeft}}
		if exitCode, stderr := runFullScreen(t, recipeDir, strokes, "init", "--distro", "fedora"); exitCode != exitSuccess {
			t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
		}
		if got := loadWrittenRecipe(t, recipeDir).Image.Distro; got != "fedora" {
			t.Errorf("distro = %q, want fedora", got)
		}
	})
	t.Run("edit", func(t *testing.T) {
		recipeDir := t.TempDir()
		fedora := recipe.Recipe{
			Image:    recipe.Image{Name: "lab", Distro: "fedora", Release: "44", Arch: "amd64"},
			User:     student,
			WSL:      wsl,
			Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
			Packages: recipe.Packages{Include: []string{"git", "jq"}},
		}
		if err := writeRecipe(filepath.Join(recipeDir, recipeFileName), fedora, false); err != nil {
			t.Fatal(err)
		}
		strokes := []keystroke{{key: keyEnter, then: pastTheFirstQuestion}, {key: keyLeft, then: "Ubuntu 26.04 LTS"}}
		if exitCode, stderr := runFullScreen(t, recipeDir, strokes, "edit"); exitCode != exitSuccess {
			t.Fatalf("exit code = %d, stderr %s", exitCode, stderr)
		}
		// Ubuntu's newest release; git is in both catalogs and jq was typed;
		// the recipe named its family, and goes on naming it.
		want := fedora
		want.Image = recipe.Image{Name: "lab", Distro: "ubuntu", Release: "26.04", Arch: "amd64"}
		if got := loadWrittenRecipe(t, recipeDir); !reflect.DeepEqual(got, want) {
			t.Errorf("edit switched to Ubuntu:\n got %+v\nwant %+v", got, want)
		}
	})
}
