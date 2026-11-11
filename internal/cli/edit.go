package cli

import (
	"context"
	"path/filepath"

	"frostroot/internal/form"
)

const editUsageText = `usage: frostroot edit [--plain] [--mirror URL] [--python-index URL] [--ca-bundle FILE] [--insecure] [--refresh-index]

Open frostroot.toml in the form with its current values and write it back.
The file is regenerated from frostroot's template, so its explanatory
comments come back and any comments you added do not. The full-screen form
can move the recipe to the other distribution, or to the other target, a
WSL tarball or a disk image; --plain keeps its own.

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
	indexOptions.pythonIndex = pythonIndexFor(indexOptions.pythonIndex, imageRecipe)
	indexOptions.certificates = imageRecipe.CertificatePaths()
	indexes, ok := a.packageIndexes(indexOptions)
	if !ok {
		return exitUserError
	}
	exitCode, _ := a.runRecipeForm(context.Background(), "edit", form.FromRecipe(imageRecipe), filepath.Join(a.RecipeDir, recipeFileName), true, *plain, form.Fixed{}, nil, nil, indexes)
	return exitCode
}
