package cli

import (
	"os"
	"path/filepath"
)

// goModuleVendorNote is what vendor and an offline build say when the recipe
// directory is also a Go module.
//
// frostroot's pools live under vendor/, which is the one directory name go
// build reads as a module's vendored dependencies. With it there, go build
// switches to vendoring mode and stops with "inconsistent vendoring" — an
// error that names go.mod and modules.txt, so it reads as a broken Go setup
// rather than as the recipe sitting beside them. frostroot's own checkout is
// such a directory, and the README tells people to build it from source, so a
// contributor who tries vendor in it meets this first.
//
// The note says what happened and names the flag that ignores the directory.
// It is empty when there is no go.mod beside the recipe.
func goModuleVendorNote(recipeDir string) string {
	if _, err := os.Stat(filepath.Join(recipeDir, "go.mod")); err != nil {
		return ""
	}
	return "\nNote: this directory is a Go module, and go build switches to vendoring mode when\n" +
		"vendor/ exists, so it will stop with \"inconsistent vendoring\". Build that module with\n" +
		"go build -mod=mod, or keep the recipe in a directory of its own.\n"
}
