package cli

import "fmt"

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
	a.stdoutf("%s: ok (%s, Ubuntu %s %s, %s requested%s)\n", recipeFileName,
		imageRecipe.Image.Name, imageRecipe.Image.Release, imageRecipe.Image.Arch, packageCount(len(imageRecipe.Packages.Include)), sourcesNote)
	return exitSuccess
}

// packageCount returns "1 package" or "N packages".
func packageCount(count int) string {
	if count == 1 {
		return "1 package"
	}
	return fmt.Sprintf("%d packages", count)
}
