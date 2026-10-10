package cli

import (
	"fmt"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

func (a *App) runValidate(args []string) int {
	flags := a.newFlagSet("validate", "usage: frostroot validate\n\nCheck frostroot.toml in the current directory. No network, no root.\n")
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	imageRecipe, ok := a.loadRecipe()
	if !ok {
		return exitUserError
	}
	sourcesNote := ""
	if count := len(imageRecipe.Sources); count == 1 {
		sourcesNote = ", 1 extra source"
	} else if count > 1 {
		sourcesNote = fmt.Sprintf(", %d extra sources", count)
	}
	pythonNote := ""
	if count := len(imageRecipe.PythonPackages()); count > 0 {
		pythonNote = fmt.Sprintf(", %d from PyPI", count)
	}
	family, err := distro.FamilyOf(imageRecipe.Image.Distro)
	if err != nil { // unreachable after validation, but never ignore an error
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	targetNote := ""
	if imageRecipe.Target() == recipe.TargetDisk {
		targetNote = ", disk image"
	}
	a.stdoutf("%s: ok (%s, %s %s %s%s, %s requested%s%s)\n", recipeFileName,
		imageRecipe.Image.Name, family.Name(), imageRecipe.Image.Release, imageRecipe.Image.Arch, targetNote, packageCount(len(imageRecipe.Packages.Include)), pythonNote, sourcesNote)
	if imageRecipe.Target() == recipe.TargetDisk {
		a.stderrf("note: %s\n", diskNotBuiltYet)
	}
	return exitSuccess
}

// diskNotBuiltYet says what this version does with a disk recipe.
const diskNotBuiltYet = "this version of frostroot writes and checks disk recipes, and builds WSL tarballs only; building a disk image comes in a later version"

// packageCount returns "1 package" or "N packages".
func packageCount(count int) string {
	if count == 1 {
		return "1 package"
	}
	return fmt.Sprintf("%d packages", count)
}

// wheelCount renders a count of Python packages as "1 wheel" or "N wheels".
func wheelCount(count int) string {
	if count == 1 {
		return "1 wheel"
	}
	return fmt.Sprintf("%d wheels", count)
}
