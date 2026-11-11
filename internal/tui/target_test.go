package tui

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
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
	want := []string{form.KeyTarget, form.KeyUserName, form.KeySudo, form.KeyTimezone, form.KeyLocale, form.KeyDiskSize, form.KeySources, form.KeyPPAs, form.KeyPackages, form.KeyOtherPackages, form.KeyPythonPackages, form.KeyCertificates}
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
		Disk:     &recipe.Disk{Size: "32G"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{"git"}},
	}
	d := newFormDriver(t, form.FromRecipe(original))
	d.pressEnterUntil(stageDone)
	if got := form.ToRecipe(d.model.binding.values()); !reflect.DeepEqual(got, original) {
		t.Errorf("an unchanged disk recipe came back as\n%+v\nwant\n%+v", got, original)
	}
}

// toTheRelease presses Shift-Tab from the target back up to the release.
func (d *formDriver) toTheRelease() {
	d.t.Helper()
	d.press(pressShiftTab)
	if focused := d.model.pages.GetFocusedField(); focused == nil || focused.GetKey() != form.KeyRelease {
		d.t.Fatalf("the release does not have the focus before the target:\n%s", d.model.View())
	}
}

// TestFormOffersADiskImageOnlyWhereOneIsBuilt: the target offers a disk
// image for Ubuntu 24.04 and 26.04, and for the other releases and Fedora
// offers WSL alone and says where a disk image is built.
func TestFormOffersADiskImageOnlyWhereOneIsBuilt(t *testing.T) {
	for _, test := range []struct {
		family  distro.Family
		release string
		want    string
	}{
		{distro.Ubuntu, "20.04", recipe.TargetWSL},
		{distro.Ubuntu, "22.04", recipe.TargetWSL},
		{distro.Ubuntu, "24.04", recipe.TargetDisk},
		{distro.Ubuntu, "26.04", recipe.TargetDisk},
		{distro.Fedora, "44", recipe.TargetWSL},
	} {
		t.Run(string(test.family)+"-"+test.release, func(t *testing.T) {
			initial := form.ForFamily(form.Defaults(noHost), test.family)
			initial[form.KeyRelease] = test.release
			d := newFormDriver(t, initial)
			d.toTheTarget()
			d.press(pressRight) // to a disk image, where there is one
			if got := form.Target(d.model.binding.values()); got != test.want {
				t.Errorf("Right on the target answered %s, want %s", got, test.want)
			}
			view := d.model.View()
			wantSaid := test.want == recipe.TargetWSL
			if said := strings.Contains(view, "A disk image is built for Ubuntu 24.04 or 26.04"); said != wantSaid {
				t.Errorf("saying where a disk image is built = %v, want %v:\n%s", said, wantSaid, view)
			}
		})
	}
}

// TestFormDropsADiskImageTheReleaseHasNot: a disk image answered on 26.04
// stays one on 24.04, is WSL on 22.04, and is a disk image again back on
// 24.04; and with a size typed on the Disk page, moving to 22.04 asks no
// Disk page and writes a WSL recipe.
func TestFormDropsADiskImageTheReleaseHasNot(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.toTheTarget()
	d.press(pressRight) // a disk image
	d.toTheRelease()
	d.press(pressUp) // 26.04 to 24.04
	if got := form.Target(d.model.binding.values()); got != recipe.TargetDisk {
		t.Fatalf("on 24.04 the target is %s, want the disk image kept", got)
	}
	d.press(pressUp) // 24.04 to 22.04
	if got := form.ToRecipe(d.model.binding.values()); got.Image.Release != "22.04" || got.Image.Target != "" || got.Disk != nil {
		t.Errorf("on 22.04 the recipe is %+v, want a WSL one", got.Image)
	}
	d.press(pressDown) // back to 24.04
	if got := form.Target(d.model.binding.values()); got != recipe.TargetDisk {
		t.Fatalf("back on 24.04 the target is %s, want the disk image answered before", got)
	}
	d.pressEnterUntilField(form.KeyDiskSize)
	d.press(typeText("16G"))
	for range 7 { // back up to the Image page's release
		d.press(pressShiftTab)
		if focused := d.model.pages.GetFocusedField(); focused != nil && focused.GetKey() == form.KeyRelease {
			break
		}
	}
	if focused := d.model.pages.GetFocusedField(); focused == nil || focused.GetKey() != form.KeyRelease {
		t.Fatalf("never got back to the release:\n%s", d.model.View())
	}
	d.press(pressUp) // 24.04 to 22.04
	if asked := d.focusedKeys(); slices.Contains(asked, form.KeyDiskSize) {
		t.Errorf("asked %q on 22.04, want no Disk page", asked)
	}
	if got := form.ToRecipe(d.model.binding.values()); got.Image.Target != "" || got.Disk != nil || !got.WSL.Systemd {
		t.Errorf("on 22.04 the recipe is %+v, want a WSL one", got)
	}
}

// TestFormDropsADiskImageFedoraHasNot: a disk image answered for Ubuntu is
// WSL once the family is Fedora.
func TestFormDropsADiskImageFedoraHasNot(t *testing.T) {
	d := newFormDriver(t, form.Defaults(noHost))
	d.toTheTarget()
	d.press(pressRight)    // a disk image
	d.press(pressShiftTab) // back to the release
	d.press(pressShiftTab) // and to the family
	if focused := d.model.pages.GetFocusedField(); focused == nil || focused.GetKey() != form.KeyDistro {
		t.Fatalf("the family does not have the focus:\n%s", d.model.View())
	}
	d.press(pressRight) // Ubuntu to Fedora
	d.pressEnterUntil(stageDone)
	if got := form.ToRecipe(d.model.binding.values()); got.Image.Distro != "fedora" || got.Image.Target != "" || got.Disk != nil {
		t.Errorf("the Fedora recipe is %+v, want a WSL one", got)
	}
}
