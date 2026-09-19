package tui

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"frostroot/internal/builder"
	"frostroot/internal/form"
)

// Golden frames pin what the screens look like. Every substring test in
// this package says a view contains something; a frame says where it is,
// so a layout change, a Charm upgrade or a narrow-terminal fix is seen in
// review as the diff of a text file. Frames are recorded with
//
//	FROSTROOT_UPDATE_FRAMES=1 go test ./internal/tui
//
// and read back by every other run. Colors are stripped before comparing:
// the frames are about layout, and what escape sequences a run emits
// depends on the terminal go test happens to run under.

// updateFramesVariable is the environment variable that rewrites the frames.
const updateFramesVariable = "FROSTROOT_UPDATE_FRAMES"

// framesDir is where the frames live, relative to this package.
var framesDir = filepath.Join("testdata", "frames")

// frameSizes are the terminals every frame is rendered at: the classic
// terminal, a roomy one, and the narrowest the screens promise to draw
// without wrapping.
var frameSizes = []struct{ cols, rows int }{{80, 24}, {120, 40}, {60, 20}}

// ansiSequence matches CSI and OSC escape sequences, which is everything
// lipgloss emits around text.
var ansiSequence = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)

// frameOf renders a view as the text a frame file holds: no escape
// sequences, no trailing spaces, exactly one final newline.
func frameOf(view string) string {
	plain := ansiSequence.ReplaceAllString(view, "")
	lines := strings.Split(strings.ReplaceAll(plain, "\r\n", "\n"), "\n")
	for index, line := range lines {
		lines[index] = strings.TrimRight(line, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n") + "\n"
}

// assertFrame compares view with the recorded frame name, or records it
// when the environment says to.
func assertFrame(t *testing.T, name, view string) {
	t.Helper()
	got := frameOf(view)
	path := filepath.Join(framesDir, name+".txt")
	if os.Getenv(updateFramesVariable) != "" {
		if err := os.MkdirAll(framesDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("recorded %s", path)
		return
	}
	wantBytes, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no frame at %s; record it with %s=1 go test ./internal/tui", path, updateFramesVariable)
	}
	if err != nil {
		t.Fatal(err)
	}
	want := string(wantBytes)
	if got == want {
		return
	}
	t.Errorf("%s differs from %s (record a new one with %s=1 if the change is intended):\n%s", name, path, updateFramesVariable, describeFrameDifference(want, got))
}

// describeFrameDifference points at the first line that differs and shows
// the whole of both frames, which is what a reviewer wants to see.
func describeFrameDifference(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")
	var description strings.Builder
	for index := range max(len(wantLines), len(gotLines)) {
		wantLine, gotLine := "", ""
		if index < len(wantLines) {
			wantLine = wantLines[index]
		}
		if index < len(gotLines) {
			gotLine = gotLines[index]
		}
		if wantLine != gotLine {
			fmt.Fprintf(&description, "first difference at line %d:\n  want: %q\n  got:  %q\n", index+1, wantLine, gotLine)
			break
		}
	}
	fmt.Fprintf(&description, "--- recorded ---\n%s--- rendered ---\n%s", want, got)
	return description.String()
}

// frameName names a frame after its scenario and terminal size.
func frameName(scenario string, cols, rows int) string {
	return fmt.Sprintf("%s-%dx%d", scenario, cols, rows)
}

// A moment the progress frames are clocked from, so that durations are the
// same on every run; the frames advance it with clockMsg.
var frameEpoch = time.Date(2026, time.September, 18, 12, 0, 0, 0, time.UTC)

// progressScenarios are the states of the progress screen worth pinning.
// Each takes a freshly sized model and brings it to the state.
var progressScenarios = []struct {
	name string
	act  func(d *driver, model *progressModel)
}{
	{name: "progress-pending", act: func(_ *driver, _ *progressModel) {}},
	{name: "progress-running", act: func(d *driver, model *progressModel) {
		d.press(eventMsg(builder.ProgressEvent{Kind: builder.EventLogFile, Line: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log"}))
		d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseStarted))
		d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseUpdateIndex, Kind: builder.EventProgress, Done: 9, Unit: builder.UnitFiles}))
		d.press(clockMsg(model.startedAt.Add(4 * time.Second)))
		d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseFinished))
		d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
		d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventProgress, Done: 14_050_000, Total: 28_100_000, Unit: builder.UnitBytes}))
		for _, line := range []string{
			"Get:6 http://archive.ubuntu.com/ubuntu jammy/main amd64 libc6 amd64 2.35-0ubuntu3 [3264 kB]",
			"Get:7 http://archive.ubuntu.com/ubuntu jammy/main amd64 libgcc-s1 amd64 12.3.0-1ubuntu1~22.04 [53.9 kB]",
			"Get:8 http://archive.ubuntu.com/ubuntu jammy/main amd64 libcrypt1 amd64 1:4.4.27-1 [85.1 kB]",
		} {
			d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventLogLine, Line: line}))
		}
		// A second measurement far enough from the first for a rate.
		d.press(clockMsg(model.startedAt.Add(20 * time.Second)))
		d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventProgress, Done: 19_050_000, Total: 28_100_000, Unit: builder.UnitBytes}))
	}},
	{name: "progress-done", act: func(d *driver, model *progressModel) {
		d.press(eventMsg(builder.ProgressEvent{Kind: builder.EventLogFile, Line: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log"}))
		elapsed := time.Duration(0)
		for _, phase := range sampleScreen.Phases {
			d.press(event(phase, builder.EventPhaseStarted))
			elapsed += 7 * time.Second
			d.press(clockMsg(model.startedAt.Add(elapsed)))
			d.press(event(phase, builder.EventPhaseFinished))
		}
		d.press(eventMsg(builder.ProgressEvent{Kind: builder.EventLogLine, Line: "I: done"}))
		d.press(doneMsg{})
		d.press(eventsClosedMsg{})
	}},
	{name: "progress-failed", act: func(d *driver, model *progressModel) {
		d.press(eventMsg(builder.ProgressEvent{Kind: builder.EventLogFile, Line: "/var/tmp/frostroot-1000/build-1/mmdebstrap.log"}))
		d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseStarted))
		d.press(clockMsg(model.startedAt.Add(4 * time.Second)))
		d.press(event(builder.PhaseUpdateIndex, builder.EventPhaseFinished))
		d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
		d.press(eventMsg(builder.ProgressEvent{Kind: builder.EventLogLine, Line: "E: Unable to locate package ninja-buld"}))
		d.press(clockMsg(model.startedAt.Add(9 * time.Second)))
		d.press(doneMsg{err: errors.New("mmdebstrap failed: exit status 1")})
		d.press(eventsClosedMsg{})
	}},
	{name: "progress-interrupting", act: func(d *driver, model *progressModel) {
		d.press(event(builder.PhaseDownload, builder.EventPhaseStarted))
		d.press(eventMsg(builder.ProgressEvent{Phase: builder.PhaseDownload, Kind: builder.EventProgress, Done: 3_000_000, Total: 28_100_000, Unit: builder.UnitBytes}))
		d.press(clockMsg(model.startedAt.Add(12 * time.Second)))
		d.press(tea.KeyMsg{Type: tea.KeyCtrlC})
	}},
}

func TestProgressFrames(t *testing.T) {
	for _, scenario := range progressScenarios {
		for _, size := range frameSizes {
			t.Run(frameName(scenario.name, size.cols, size.rows), func(t *testing.T) {
				model := newProgressModel(sampleScreen, make(chan builder.ProgressEvent), make(chan error), func() {})
				model.startedAt, model.now = frameEpoch, frameEpoch
				d := newDriver(t, model)
				d.press(tea.WindowSizeMsg{Width: size.cols, Height: size.rows})
				scenario.act(d, model)
				assertFrame(t, frameName(scenario.name, size.cols, size.rows), model.View())
			})
		}
	}
}

// sampleRecipeText stands in for a rendered recipe on the summary page; the
// real one comes from the command line, which the form never imports.
const sampleRecipeText = `# frostroot recipe: the image you want. Edit it, then run: frostroot build
#
# Versions do not belong here. frostroot build writes the exact version of
# every installed package to frostroot.lock; commit both files.

[image]
name = "lab"  # file name of the tarball and name of the WSL distro
release = "24.04"
arch = "amd64"  # the only architecture in v1

[user]
name = "student"
sudo = true  # passwordless sudo: this is a lab image, not a hardened server

[wsl]
systemd = true
default_user = "student"

[locale]
lang = "en_US.UTF-8"
timezone = "UTC"  # kept under WSL instead of following Windows

[packages]
include = ["git", "build-essential"]
`

// sampleDiffText stands in for the diff edit shows.
const sampleDiffText = `  [image]
- name = "lab"  # file name of the tarball and name of the WSL distro
+ name = "cpp-lab"  # file name of the tarball and name of the WSL distro
  release = "24.04"
  arch = "amd64"  # the only architecture in v1

  [packages]
- # my own note about these
- include = ["git"]
+ include = ["git", "build-essential"]
`

// summaryScenarios are the last pages worth pinning: with no preview, with
// a new recipe, with a diff, and with nothing to change.
var summaryScenarios = []struct {
	name    string
	preview PreviewFunc
}{
	{name: "form-summary", preview: nil},
	{name: "form-summary-new", preview: func(form.Values) Preview {
		return Preview{Heading: "This is what frostroot.toml will say:", Text: sampleRecipeText}
	}},
	{name: "form-summary-diff", preview: func(form.Values) Preview {
		return Preview{Heading: "4 lines change; your own comments in the file are replaced by the template's:", Text: sampleDiffText}
	}},
	{name: "form-summary-unchanged", preview: func(form.Values) Preview {
		return Preview{Heading: "Nothing changes: frostroot.toml already says this.", Text: sampleRecipeText, Unchanged: true}
	}},
	{name: "form-summary-warning", preview: func(values form.Values) Preview {
		unknown := []form.UnknownPackage{{Name: "ninja-buld", Nearest: []string{"ninja-build"}}, {Name: "docker-ce"}}
		// nil: this frame pins the layout of a warning, and the index that
		// says which repositories were searched is what the wording is
		// tested against, in internal/form.
		return Preview{Heading: "This is what frostroot.toml will say:", Text: sampleRecipeText, Warning: form.UnknownPackagesWarning(unknown, values, nil)}
	}},
}

// TestASCIIFrames pins the screens on a terminal whose locale is not UTF-8:
// the same layouts, drawn without box drawing or symbols.
func TestASCIIFrames(t *testing.T) {
	withLocale(t, map[string]string{"LANG": "C"})
	for _, scenario := range progressScenarios {
		if scenario.name != "progress-running" && scenario.name != "progress-failed" {
			continue
		}
		t.Run(frameName(scenario.name+"-ascii", 80, 24), func(t *testing.T) {
			model := newProgressModel(sampleScreen, make(chan builder.ProgressEvent), make(chan error), func() {})
			model.startedAt, model.now = frameEpoch, frameEpoch
			d := newDriver(t, model)
			d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
			scenario.act(d, model)
			assertFrame(t, frameName(scenario.name+"-ascii", 80, 24), model.View())
		})
	}
	t.Run(frameName("form-first-page-ascii", 80, 24), func(t *testing.T) {
		d := newFormDriver(t, form.Defaults(noHost))
		d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
		assertFrame(t, frameName("form-first-page-ascii", 80, 24), d.model.View())
	})
	t.Run(frameName("form-summary-new-ascii", 80, 24), func(t *testing.T) {
		d := newFormDriverWithPreview(t, form.Defaults(noHost), summaryScenarios[1].preview)
		d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
		d.pressEnterUntil(stageSummary)
		assertFrame(t, frameName("form-summary-new-ascii", 80, 24), d.model.View())
	})
}

func TestFormFrames(t *testing.T) {
	for _, size := range frameSizes {
		t.Run(frameName("form-first-page", size.cols, size.rows), func(t *testing.T) {
			d := newFormDriver(t, form.Defaults(noHost))
			d.press(tea.WindowSizeMsg{Width: size.cols, Height: size.rows})
			assertFrame(t, frameName("form-first-page", size.cols, size.rows), d.model.View())
		})
		for _, scenario := range summaryScenarios {
			t.Run(frameName(scenario.name, size.cols, size.rows), func(t *testing.T) {
				d := newFormDriverWithPreview(t, form.Defaults(noHost), scenario.preview)
				d.press(tea.WindowSizeMsg{Width: size.cols, Height: size.rows})
				d.pressEnterUntil(stageSummary)
				assertFrame(t, frameName(scenario.name, size.cols, size.rows), d.model.View())
			})
		}
	}
}

func TestFrameOfStripsWhatIsNotLayout(t *testing.T) {
	view := "\x1b[1mfrostroot build\x1b[0m   lab \x1b]8;;http://x\x1b\\link\x1b]8;;\x1b\\  \r\n  \x1b[32m✓\x1b[0m done   \n\n"
	if got, want := frameOf(view), "frostroot build   lab link\n  ✓ done\n"; got != want {
		t.Errorf("frameOf = %q, want %q", got, want)
	}
}
