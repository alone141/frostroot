package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"frostroot/internal/distro"
	"frostroot/internal/form"
	"frostroot/internal/recipe"
)

var pressShiftTab = tea.KeyMsg{Type: tea.KeyShiftTab}

// focusedKeys presses Enter until the form is done, and returns the key of
// every field that had the focus on the way, once each, in order.
func (d *formDriver) focusedKeys() []string {
	d.t.Helper()
	var keys []string
	for presses := 0; d.model.stage < stageSummary && presses < maxKeyPresses; presses++ {
		if focused := d.model.pages.GetFocusedField(); focused != nil && !slices.Contains(keys, focused.GetKey()) {
			keys = append(keys, focused.GetKey())
		}
		d.press(pressEnter)
	}
	d.pressEnterUntil(stageDone)
	return keys
}

// TestFormAsksFedorasQuestions: Fedora chosen on the Image page leads to
// Fedora's own questions, past Ubuntu's Sources and Trust pages and its
// Python field, and to a Fedora recipe.
func TestFormAsksFedorasQuestions(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.press(pressEnter) // past the name
	d.press(pressRight) // Ubuntu to Fedora
	asked := d.focusedKeys()
	want := []string{form.KeyDistro, form.KeyRelease, form.KeyUserName, form.KeySudo, form.KeyTimezone, form.KeyLocale, form.KeySystemd, form.KeyPackages, form.KeyOtherPackages}
	if !slices.Equal(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
	got := form.ToRecipe(d.model.binding.values())
	wantRecipe := recipe.Recipe{
		Image:    recipe.Image{Name: "lab", Distro: "fedora", Release: "44", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{}},
	}
	if !reflect.DeepEqual(got, wantRecipe) {
		t.Errorf("recipe:\n got %+v\nwant %+v", got, wantRecipe)
	}
}

// TestFormKeepsEachFamilysAnswers: going to Fedora and back finds Ubuntu's
// release where it was left, not on 20.04 and not on Fedora's 44.
func TestFormKeepsEachFamilysAnswers(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.press(pressEnter)    // past the name
	d.press(pressEnter)    // Ubuntu stays, for now
	d.press(pressUp)       // 26.04 to 24.04
	d.press(pressShiftTab) // back to the family
	d.press(pressRight)    // Fedora
	if !strings.Contains(d.model.View(), "Fedora 44") {
		t.Errorf("the release did not follow the family:\n%s", d.model.View())
	}
	d.press(pressLeft) // Ubuntu again
	if view := d.model.View(); !strings.Contains(view, "> Ubuntu 24.04") {
		t.Errorf("Ubuntu's release is not where it was left:\n%s", view)
	}
	d.pressEnterUntil(stageDone)
	values := d.model.binding.values()
	if form.Family(values) != distro.Ubuntu || values.String(form.KeyRelease) != "24.04" {
		t.Errorf("answers: %s %s, want ubuntu 24.04", values.String(form.KeyDistro), values.String(form.KeyRelease))
	}
	if distroLine := form.ToRecipe(values).Image.Distro; distroLine != "" {
		t.Errorf("an Ubuntu recipe init writes names its family %q; it never did", distroLine)
	}
}

// TestFormEditsAFedoraRecipe: edit starts on the recipe's family, and a
// recipe nobody changes is written back as it was.
func TestFormEditsAFedoraRecipe(t *testing.T) {
	original := recipe.Recipe{
		Image:    recipe.Image{Name: "fedora-lab", Distro: "fedora", Release: "44", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "tr_TR.UTF-8", Timezone: "Europe/Istanbul"},
		Packages: recipe.Packages{Include: []string{"git", "NetworkManager-tui", "gcc"}},
	}
	d := newFormDriver(t, form.FromRecipe(original))
	d.press(tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := d.model.View(); !strings.Contains(view, "Fedora release") || !strings.Contains(view, "Fedora 44") {
		t.Errorf("the Image page does not start on Fedora:\n%s", view)
	}
	d.pressEnterUntil(stageDone)
	if got := form.ToRecipe(d.model.binding.values()); !reflect.DeepEqual(got, original) {
		t.Errorf("edit changed a recipe nobody touched:\n got %+v\nwant %+v", got, original)
	}
}
