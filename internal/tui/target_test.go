package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/form"
	"frostroot/internal/recipe"
)

// toTheTarget presses past the name, the family and the release, to the
// target.
func (d *formDriver) toTheTarget() {
	d.t.Helper()
	d.press(pressEnter) // past the name
	d.press(pressEnter) // Ubuntu stays
	d.press(pressEnter) // 26.04 stays
	if focused := d.model.pages.GetFocusedField(); focused == nil || focused.GetKey() != form.KeyTarget {
		d.t.Fatalf("the target does not have the focus after the release:\n%s", d.model.View())
	}
}

// TestFormAsksADiskImagesQuestions: a disk image chosen on the Image page
// leads past the systemd question, which is wsl.conf's, to the Disk page,
// and to a disk recipe.
func TestFormAsksADiskImagesQuestions(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.toTheTarget()
	d.press(pressRight) // WSL to a disk image
	asked := d.focusedKeys()
	want := []string{form.KeyTarget, form.KeyUserName, form.KeySudo, form.KeyTimezone, form.KeyLocale, form.KeyDiskSize, form.KeyDiskQcow2, form.KeySources, form.KeyPPAs, form.KeyPackages, form.KeyOtherPackages, form.KeyPythonPackages, form.KeyCertificates}
	if !slices.Equal(asked, want) {
		t.Errorf("asked %q, want %q", asked, want)
	}
	got := form.ToRecipe(d.model.binding.values())
	wantRecipe := recipe.Recipe{
		Image:    recipe.Image{Name: "lab", Release: "26.04", Arch: "amd64", Target: recipe.TargetDisk},
		User:     recipe.User{Name: "student", Sudo: true},
		Disk:     &recipe.Disk{},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{}},
	}
	if !reflect.DeepEqual(got, wantRecipe) {
		t.Errorf("recipe:\n got %+v\nwant %+v", got, wantRecipe)
	}
}

// TestFormDescribesTheUserForTheTarget: the same question, described for
// the target answered, as a variantField switches it.
func TestFormDescribesTheUserForTheTarget(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.toTheTarget()
	d.press(pressRight) // a disk image
	d.press(pressEnter) // to the User page
	if view := d.model.View(); !strings.Contains(view, "password is set at the first boot") || strings.Contains(view, "WSL logs in as") {
		t.Errorf("a disk image's user is not described as a disk's:\n%s", view)
	}
}

// TestFormKeepsEachTargetsAnswers: a size typed for a disk survives going
// to WSL and back, and the WSL recipe in between has no [disk].
func TestFormKeepsEachTargetsAnswers(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.toTheTarget()
	d.press(pressRight) // a disk image
	d.pressEnterUntilField(form.KeyDiskSize)
	d.press(typeText("16G"))
	for range 7 { // back up to the Image page's target
		d.press(pressShiftTab)
		if focused := d.model.pages.GetFocusedField(); focused != nil && focused.GetKey() == form.KeyTarget {
			break
		}
	}
	if focused := d.model.pages.GetFocusedField(); focused == nil || focused.GetKey() != form.KeyTarget {
		t.Fatalf("never got back to the target:\n%s", d.model.View())
	}
	d.press(pressLeft) // WSL
	if wsl := form.ToRecipe(d.model.binding.values()); wsl.Disk != nil || wsl.WSL.Systemd != true {
		t.Errorf("the WSL recipe = %+v; want [wsl] and no [disk]", wsl)
	}
	d.press(pressRight) // a disk image again
	d.pressEnterUntil(stageDone)
	if got := form.ToRecipe(d.model.binding.values()).Disk; got == nil || got.Size != "16G" {
		t.Errorf("[disk] = %+v, want the size typed before the switch", got)
	}
}

// TestFormEditsADiskRecipe: edit starts on the recipe's target, and a
// recipe nobody changes is written back as it was.
func TestFormEditsADiskRecipe(t *testing.T) {
	original := recipe.Recipe{
		Image:    recipe.Image{Name: "pc-lab", Release: "26.04", Arch: "amd64", Target: recipe.TargetDisk},
		User:     recipe.User{Name: "student", Sudo: true},
		Disk:     &recipe.Disk{Size: "32G", Formats: []string{recipe.DiskFormatQcow2}},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{"git"}},
	}
	d := newFormDriver(t, form.FromRecipe(original))
	d.pressEnterUntil(stageDone)
	if got := form.ToRecipe(d.model.binding.values()); !reflect.DeepEqual(got, original) {
		t.Errorf("an unchanged disk recipe came back as\n%+v\nwant\n%+v", got, original)
	}
}
