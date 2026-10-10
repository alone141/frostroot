package cli

import (
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
