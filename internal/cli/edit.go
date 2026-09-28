package cli

import (
	"path/filepath"

	"frostroot/internal/distro"
	"frostroot/internal/form"
)

const editUsageText = `usage: frostroot edit [--plain] [--mirror URL] [--python-index URL] [--ca-bundle FILE | --insecure] [--refresh-index]

Open frostroot.toml in the form with its current values and write it back.
The file is regenerated from frostroot's template, so its explanatory
comments come back and any comments you added do not.

` + indexUsageText

func (a *App) runEdit(args []string) int {
	flags := a.newFlagSet("edit", editUsageText)
	plain := flags.Bool("plain", false, "ask line by line instead of showing the full-screen form")
	indexOptions := addIndexFlags(flags)
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	imageRecipe, ok := a.loadValidatedRecipe()
	if !ok {
		return exitUserError
	}
	// The form offers Ubuntu's releases, packages and sources only, and
	// would give a Fedora recipe one of each.
	if family, err := distro.FamilyOf(imageRecipe.Image.Distro); err == nil && family == distro.Fedora {
		a.stderrf("frostroot: edit does not know Fedora recipes yet; change %s by hand\n", recipeFileName)
		return exitUserError
	}
	indexes, ok := a.packageIndexes(indexOptions)
	if !ok {
		return exitUserError
	}
	return a.runRecipeForm("edit", form.FromRecipe(imageRecipe), filepath.Join(a.RecipeDir, recipeFileName), true, *plain, nil, nil, indexes)
}
