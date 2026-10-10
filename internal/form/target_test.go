package form

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// diskAnswers returns the defaults as a disk recipe's, with a size and a
// qcow2 asked for.
func diskAnswers() Values {
	values := Defaults(noHost)
	values[KeyTarget] = recipe.TargetDisk
	values[KeyDiskSize] = "16G"
	values[KeyDiskQcow2] = true
	return values
}

// TestTheTargetIsAskedAfterTheRelease: a select every family asks, of WSL
// and a disk image, each saying what it makes.
func TestTheTargetIsAskedAfterTheRelease(t *testing.T) {
	var target Field
	for _, field := range Fields(noHost) {
		if field.Key == KeyTarget {
			target = field
		}
	}
	if target.Page != PageImage || target.Kind != KindSelect || len(target.Families) != 0 || len(target.Targets) != 0 {
		t.Fatalf("the target question = %+v, want a select on the Image page that every variant asks", target)
	}
	var labels []string
	for _, option := range target.Options {
		labels = append(labels, option.Value+"="+option.DisplayLabel())
	}
	if want := []string{"wsl=WSL", "disk=Disk image"}; !slices.Equal(labels, want) {
		t.Errorf("the targets offered are %q, want %q", labels, want)
	}
}

// TestADiskRecipesPlainQuestions: a disk recipe asks no systemd question,
// which is wsl.conf's, and asks the disk's size and qcow2 after the locale;
// its name and user are described as a disk's.
func TestADiskRecipesPlainQuestions(t *testing.T) {
	plain := Fixed{Family: true, Target: true}
	ubuntu := FieldsFor(Fields(noHost), Variant{Family: distro.Ubuntu, Target: recipe.TargetDisk}, plain)
	want := []string{KeyImageName, KeyRelease, KeyUserName, KeySudo, KeyTimezone, KeyLocale, KeyDiskSize, KeyDiskQcow2, KeySources, KeyPPAs, KeyPackages, KeyOtherPackages, KeyPythonPackages, KeyCertificates}
	if got := keysOf(ubuntu); !slices.Equal(got, want) {
		t.Errorf("an Ubuntu disk recipe asks %q, want %q", got, want)
	}
	fedora := FieldsFor(Fields(noHost), Variant{Family: distro.Fedora, Target: recipe.TargetDisk}, plain)
	want = []string{KeyImageName, KeyRelease, KeyUserName, KeySudo, KeyTimezone, KeyLocale, KeyDiskSize, KeyDiskQcow2, KeyPackages, KeyOtherPackages}
	if got := keysOf(fedora); !slices.Equal(got, want) {
		t.Errorf("a Fedora disk recipe asks %q, want %q", got, want)
	}
	for _, field := range ubuntu {
		if (field.Key == KeyImageName || field.Key == KeyUserName) && strings.Contains(field.Description, "WSL") {
			t.Errorf("a disk recipe's %s is described as WSL's: %q", field.Key, field.Description)
		}
	}
}

// TestFieldsForWithTheTargetAsked: init --distro fixes the family alone,
// so the target is still asked, with the questions of both targets.
func TestFieldsForWithTheTargetAsked(t *testing.T) {
	asked := FieldsFor(Fields(noHost), Variant{Family: distro.Fedora, Target: recipe.TargetWSL}, Fixed{Family: true})
	keys := keysOf(asked)
	if slices.Contains(keys, KeyDistro) || !slices.Contains(keys, KeyTarget) {
		t.Errorf("asks %q; want the target and not the family", keys)
	}
	for _, key := range []string{KeySystemd, KeyDiskSize, KeyDiskQcow2} {
		if !slices.Contains(keys, key) {
			t.Errorf("asks no %s; both targets' questions are wanted", key)
		}
	}
	if slices.Contains(keys, KeySources) {
		t.Errorf("asks %s, which is Ubuntu's", KeySources)
	}
}

func TestADiskRecipeFromTheAnswers(t *testing.T) {
	imageRecipe := ToRecipe(diskAnswers())
	if imageRecipe.Image.Target != recipe.TargetDisk || imageRecipe.WSL != (recipe.WSL{}) {
		t.Errorf("target %q, [wsl] %+v; want disk, and no [wsl]", imageRecipe.Image.Target, imageRecipe.WSL)
	}
	if want := (&recipe.Disk{Size: "16G", Formats: []string{recipe.DiskFormatQcow2}}); !reflect.DeepEqual(imageRecipe.Disk, want) {
		t.Errorf("[disk] = %+v, want %+v", imageRecipe.Disk, want)
	}
	if problems := recipe.Validate(imageRecipe); len(problems) != 0 {
		t.Errorf("Validate = %q", problems)
	}
	if back := FromRecipe(imageRecipe); !reflect.DeepEqual(ToRecipe(back), imageRecipe) {
		t.Errorf("FromRecipe then ToRecipe = %+v, want %+v", ToRecipe(back), imageRecipe)
	}
	// Taking every default still writes [disk], so the recipe shows where
	// the disk's settings go.
	values := diskAnswers()
	values[KeyDiskSize], values[KeyDiskQcow2] = "", false
	if got := ToRecipe(values).Disk; !reflect.DeepEqual(got, &recipe.Disk{}) {
		t.Errorf("[disk] of the defaults = %+v, want an empty table", got)
	}
}

// TestSwitchingTheTargetCarriesTheAnswers: an edit that switches the
// target keeps every answer, and the recipe it writes holds only what its
// target uses.
func TestSwitchingTheTargetCarriesTheAnswers(t *testing.T) {
	values := diskAnswers()
	values[KeyTarget] = recipe.TargetWSL
	imageRecipe := ToRecipe(values)
	if imageRecipe.Image.Target != "" || imageRecipe.Disk != nil {
		t.Errorf("target %q, [disk] %+v; want a WSL recipe that names no target and has no [disk]", imageRecipe.Image.Target, imageRecipe.Disk)
	}
	if want := (recipe.WSL{Systemd: true, DefaultUser: "student"}); imageRecipe.WSL != want {
		t.Errorf("[wsl] = %+v, want %+v", imageRecipe.WSL, want)
	}
	values[KeyTarget] = recipe.TargetDisk
	if got := ToRecipe(values).Disk; got == nil || got.Size != "16G" {
		t.Errorf("switched back, [disk] = %+v; want the size answered before", got)
	}
}

// TestTheRecipeNamesItsTargetOnlyWhenItMust: a WSL recipe is the bytes it
// always was, unless it named its target, which it then keeps doing.
func TestTheRecipeNamesItsTargetOnlyWhenItMust(t *testing.T) {
	if got := ToRecipe(Defaults(noHost)).Image.Target; got != "" {
		t.Errorf("init's WSL recipe names target %q, want none", got)
	}
	named := recipe.Recipe{Image: recipe.Image{Name: "lab", Release: "26.04", Arch: "amd64", Target: recipe.TargetWSL}}
	if got := ToRecipe(FromRecipe(named)).Image.Target; got != recipe.TargetWSL {
		t.Errorf("a recipe that said target = \"wsl\" says %q after edit", got)
	}
}

func TestSummaryOfADiskRecipe(t *testing.T) {
	summary := Summary(diskAnswers())
	for _, want := range []string{"Image     lab, Ubuntu 26.04 amd64, disk image", "System    UTC, en_US.UTF-8\n", "Disk      16G, raw and qcow2; the root grows to fill its disk"} {
		if !strings.Contains(summary+"\n", want) {
			t.Errorf("summary lacks %q:\n%s", want, summary)
		}
	}
	if strings.Contains(summary, "systemd") {
		t.Errorf("a disk recipe's summary speaks of systemd, which is wsl.conf's:\n%s", summary)
	}
	values := diskAnswers()
	values[KeyDiskSize], values[KeyDiskQcow2] = "", false
	if summary := Summary(values); !strings.Contains(summary, "Disk      sized to fit, raw;") {
		t.Errorf("summary of the disk defaults:\n%s", summary)
	}
	if summary := Summary(Defaults(noHost)); strings.Contains(summary, "Disk") || strings.Contains(summary, "disk image") {
		t.Errorf("a WSL recipe's summary speaks of a disk:\n%s", summary)
	}
}

func TestTheDiskSizeAnswer(t *testing.T) {
	for _, answer := range []string{"", "16G", "4096M"} {
		if err := checkDiskSizeAnswer(answer); err != nil {
			t.Errorf("%q: %v", answer, err)
		}
	}
	for _, answer := range []string{"16GB", "16", "512M"} {
		if err := checkDiskSizeAnswer(answer); err == nil {
			t.Errorf("%q accepted", answer)
		}
	}
}
