package cli

import (
	"bytes"
	"io"
	"net/http"
	"path/filepath"
	"reflect"
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

// runFullScreen runs a command in the full-screen form, typing keys into it
// a moment apart as a person would, then Enter until the form is done. The
// index client answers nothing, so that a picker that opens an index finds
// none rather than the network.
func runFullScreen(t *testing.T, recipeDir string, keys []string, args ...string) (exitCode int, stderr string) {
	t.Helper()
	for range 40 {
		keys = append(keys, keyEnter)
	}
	reader, writer := io.Pipe()
	go func() {
		for _, key := range keys {
			time.Sleep(30 * time.Millisecond)
			if _, err := io.WriteString(writer, key); err != nil {
				return // the form has stopped reading
			}
		}
		// Left open: a closed input would end the form early.
	}()
	var stderrBuffer bytes.Buffer
	app := App{
		Stdin: reader, Stdout: io.Discard, Stderr: &stderrBuffer, RecipeDir: recipeDir, ReadFile: noHostFile,
		IsTerminal:  terminalChecker(true),
		Getenv:      environmentWith(map[string]string{"TERM": "xterm-256color", "XDG_CACHE_HOME": t.TempDir()}),
		IndexClient: &http.Client{Transport: failingTransport{}},
	}
	return app.Run(args), stderrBuffer.String()
}

// TestTheFullScreenFormAsksTheFamilyWhereItShould: init and edit ask the
// family on the Image page, between the name and the release, and a switch
// there writes the other family's recipe; init --distro answers it, so the
// page asks none.
func TestTheFullScreenFormAsksTheFamilyWhereItShould(t *testing.T) {
	student := recipe.User{Name: "student", Sudo: true}
	wsl := recipe.WSL{Systemd: true, DefaultUser: "student"}
	t.Run("init", func(t *testing.T) {
		recipeDir := t.TempDir()
		if exitCode, stderr := runFullScreen(t, recipeDir, []string{keyEnter, keyRight}, "init"); exitCode != exitSuccess {
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
		if exitCode, stderr := runFullScreen(t, recipeDir, []string{keyEnter, keyLeft}, "init", "--distro", "fedora"); exitCode != exitSuccess {
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
		if exitCode, stderr := runFullScreen(t, recipeDir, []string{keyEnter, keyLeft}, "edit"); exitCode != exitSuccess {
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
