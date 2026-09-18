package cli

import (
	"path/filepath"

	"frostroot/internal/form"
)

const editUsageText = `usage: frostroot edit [--plain] [--mirror URL] [--ca-bundle FILE] [--refresh-index]

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
	imageRecipe, ok := a.loadRecipe()
	if !ok {
		return exitUserError
	}
	indexes, ok := a.packageIndexes(indexOptions)
	if !ok {
		return exitUserError
	}
	return a.runRecipeForm("edit", form.FromRecipe(imageRecipe), filepath.Join(a.RecipeDir, recipeFileName), *plain, nil, nil, indexes)
}
