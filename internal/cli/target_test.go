package cli

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"frostroot/internal/form"
	"frostroot/internal/recipe"
)

// TestADiskRecipeIsWrittenAsADisksAndReadsBack: no [wsl], a [disk] that
// says where its settings go, comments that speak of a disk rather than of
// WSL, and a file that parses back to the recipe, which writeRecipe also
// proves before it renames the file into place.
func TestADiskRecipeIsWrittenAsADisksAndReadsBack(t *testing.T) {
	for name, disk := range map[string]*recipe.Disk{
		"defaults":         {},
		"a size and qcow2": {Size: "16G", Formats: []string{recipe.DiskFormatQcow2}},
	} {
		t.Run(name, func(t *testing.T) {
			values := form.Defaults(form.Host{ReadFile: noHostFile})
			values[form.KeyTarget] = recipe.TargetDisk
			values[form.KeyDiskSize] = disk.Size
			values[form.KeyDiskQcow2] = len(disk.Formats) > 0
			imageRecipe := form.ToRecipe(values)
			recipePath := filepath.Join(t.TempDir(), recipeFileName)
			if err := writeRecipe(recipePath, imageRecipe, false); err != nil {
				t.Fatal(err)
			}
			reloaded, err := recipe.Load(recipePath)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(reloaded, imageRecipe) || !reflect.DeepEqual(reloaded.Disk, disk) {
				t.Errorf("read back\n%+v\nwant\n%+v with [disk] %+v", reloaded, imageRecipe, disk)
			}
			rendered, err := renderRecipe(imageRecipe)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{`target = "disk"`, "[disk]", "# file name of the disk image", "its password is set at the first boot", "the root grows to fill the disk"} {
				if !strings.Contains(rendered, want) {
					t.Errorf("the recipe lacks %q:\n%s", want, rendered)
				}
			}
			for _, unwanted := range []string{"[wsl]", "WSL", "wsl.conf"} {
				if strings.Contains(rendered, unwanted) {
					t.Errorf("a disk recipe mentions %q:\n%s", unwanted, rendered)
				}
			}
		})
	}
}

// TestInitWithTargetDisk: --target answers the target, and the plain
// questions of a disk recipe are a WSL recipe's without systemd, with the
// disk's size and qcow2 after the locale.
func TestInitWithTargetDisk(t *testing.T) {
	recipeDir := t.TempDir()
	answers := []string{"pc-lab", "", "", "", "", "", "16G", "y", "", "", "", "", "", "", ""}
	exitCode, stdout, stderr := runInitWithAnswers(recipeDir, answers, "--target", "disk")
	if exitCode != exitSuccess {
		t.Fatalf("exit code = %d\n%s\n%s", exitCode, stdout, stderr)
	}
	prompt := &scriptedPrompt{answers: answers}
	app := App{Stdout: io.Discard, Stderr: io.Discard, RecipeDir: t.TempDir(), Prompt: prompt, ReadFile: noHostFile}
	if exitCode := app.Run([]string{"init", "--target", "disk"}); exitCode != exitSuccess {
		t.Fatalf("second init: exit code = %d", exitCode)
	}
	wantTopics := []string{"image name", "release", "user name", "sudo", "timezone", "locale", "disk size", "qcow2", "apt sources", "other ppas", "packages", "other packages", "python packages", "certificate authorities", "write frostroot.toml"}
	if len(prompt.questionsAsked) != len(wantTopics) {
		t.Fatalf("questions asked = %q, want one about each of %q", prompt.questionsAsked, wantTopics)
	}
	for i, wantTopic := range wantTopics {
		if !strings.Contains(strings.ToLower(prompt.questionsAsked[i]), wantTopic) {
			t.Errorf("question %d %q should be about %s", i, prompt.questionsAsked[i], wantTopic)
		}
	}
	want := recipe.Recipe{
		Image:    recipe.Image{Name: "pc-lab", Release: "26.04", Arch: "amd64", Target: recipe.TargetDisk},
		User:     recipe.User{Name: "student", Sudo: true},
		Disk:     &recipe.Disk{Size: "16G", Formats: []string{recipe.DiskFormatQcow2}},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{}},
	}
	if got := loadWrittenRecipe(t, recipeDir); !reflect.DeepEqual(got, want) {
		t.Errorf("recipe:\n got %+v\nwant %+v", got, want)
	}
	if !strings.Contains(stdout, "Disk      16G, raw and qcow2") {
		t.Errorf("the summary does not describe the disk:\n%s", stdout)
	}
	exitCode, validated, note := runValidateIn(recipeDir)
	if exitCode != exitSuccess || !strings.Contains(validated, "Ubuntu 26.04 amd64, disk image,") || strings.TrimSpace(note) != "" {
		t.Errorf("validate: exit %d, stdout %q, stderr %q; want the summary and no note", exitCode, validated, note)
	}
}

func TestInitRefusesAnUnknownTarget(t *testing.T) {
	recipeDir := t.TempDir()
	prompt := &scriptedPrompt{}
	var stderr bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &stderr, RecipeDir: recipeDir, Prompt: prompt, ReadFile: noHostFile}
	if exitCode := app.Run([]string{"init", "--target", "iso"}); exitCode != exitUserError {
		t.Errorf("exit code = %d, want a user error", exitCode)
	}
	if !strings.Contains(stderr.String(), "--target") || !strings.Contains(stderr.String(), "wsl, disk") || len(prompt.questionsAsked) != 0 {
		t.Errorf("stderr %q after %d questions; want the flag and the targets named, and nothing asked", stderr.String(), len(prompt.questionsAsked))
	}
	if _, err := os.Stat(filepath.Join(recipeDir, recipeFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a recipe was written: %v", err)
	}
}

// TestBuildMakesADiskImage: a disk recipe builds a raw image in dist/ with
// the lock, and the report says how to use it: no wsl --import, the
// password at the first boot.
func TestBuildMakesADiskImage(t *testing.T) {
	recipeDir := newRecipeDir(t, "disk.toml")
	bootstrapper := &fakeBootstrapper{}
	var stdout, stderr bytes.Buffer
	app := newBuildApp(t, recipeDir, bootstrapper, &stdout, &stderr)
	if exitCode := app.Run([]string{"build", "--plain"}); exitCode != exitSuccess {
		t.Fatalf("exit code = %d, want success\n%s", exitCode, stderr.String())
	}
	imagePath := filepath.Join(recipeDir, "dist", "pc-lab-ubuntu-26.04-amd64.img")
	if _, err := os.Stat(imagePath); err != nil {
		t.Errorf("the image was not placed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(recipeDir, "dist", "pc-lab-ubuntu-26.04-amd64.tar.gz")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a tarball was placed for a disk recipe: %v", err)
	}
	for _, want := range []string{
		"Wrote dist/pc-lab-ubuntu-26.04-amd64.img (", ", sparse)", "Wrote frostroot.lock (",
		"sudo dd if=dist/pc-lab-ubuntu-26.04-amd64.img of=/dev/sdX", "The first boot asks for the password of student on the screen",
		"frostroot vendor",
	} {
		if !strings.Contains(stdout.String(), want) {
			t.Errorf("stdout lacks %q:\n%s", want, stdout.String())
		}
	}
	if strings.Contains(stdout.String(), "wsl --import") {
		t.Errorf("a disk image is not imported into WSL:\n%s", stdout.String())
	}
	for _, want := range []string{"frostroot: Make the tools tree", "frostroot: Assemble the disk image", "frostroot: Write frostroot.lock", "frostroot: Place the disk image"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks the phase %q:\n%s", want, stderr.String())
		}
	}
	lock, err := recipe.LoadLock(filepath.Join(recipeDir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Target != recipe.TargetDisk || lock.Disk == nil || lock.Disk.DiskBytes != 16<<30 || len(lock.DiskTools) == 0 {
		t.Errorf("lock: target %q, disk %+v, %d tools; want the disk target, the 16G asked for, the tree's packages", lock.Target, lock.Disk, len(lock.DiskTools))
	}
	if spec := bootstrapper.lastSpec; spec.Suite != "resolute" || !strings.Contains(strings.Join(spec.Include, " "), "linux-image-generic") {
		t.Errorf("the image was built as %+v", spec)
	}
}
