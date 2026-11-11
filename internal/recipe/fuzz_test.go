package recipe

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzLoadAndValidate: a recipe is typed by hand and a lock is hostile
// input; loading either, and validating the recipe, refuses what it cannot
// use without a panic.
func FuzzLoadAndValidate(f *testing.F) {
	for _, name := range []string{"valid.toml", "disk.toml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(data)
	}
	lockPath := filepath.Join(f.TempDir(), "frostroot.lock")
	if err := SaveLock(lockPath, sampleLockWithChecksums()); err != nil {
		f.Fatal(err)
	}
	lockData, err := os.ReadFile(lockPath)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(lockData)
	f.Add([]byte("[image]\nname = \"\u0000\"\n[disk]\nsize = \"99999999999999999999G\"\n"))
	f.Fuzz(func(t *testing.T, data []byte) {
		path := filepath.Join(t.TempDir(), "frostroot.toml")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		if imageRecipe, err := Load(path); err == nil {
			_ = Validate(imageRecipe)
		}
		_, _ = LoadLock(path)
	})
}
