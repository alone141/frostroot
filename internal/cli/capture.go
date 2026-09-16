package cli

import (
	"os"
	"path/filepath"

	"frostroot/internal/capture"
	"frostroot/internal/export"
	"frostroot/internal/form"
)

// captureReportFileName is the report capture writes next to the recipe.
const captureReportFileName = "frostroot-capture.md"

const captureUsageText = `usage: frostroot capture [--root DIR] [--force] [--plain]

Describe an installed Ubuntu system as a recipe: read what apt installed and
how the machine is set up, open the form with those values, and write
frostroot.toml plus frostroot-capture.md, a report of everything a recipe
cannot carry (pip and npm installs, /opt, edits to /etc, dotfiles...).
Reads package metadata and a few configuration files; copies nothing and
needs no root.

`

func (a *App) runCapture(args []string) int {
	flags := a.newFlagSet("capture", captureUsageText)
	rootDir := flags.String("root", "/", "root of the system to describe, e.g. a mounted `DIR`")
	overwrite := flags.Bool("force", false, "overwrite an existing frostroot.toml")
	plain := flags.Bool("plain", false, "ask line by line instead of showing the full-screen form")
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	recipePath := filepath.Join(a.RecipeDir, recipeFileName)
	if _, err := os.Stat(recipePath); err == nil && !*overwrite {
		a.stderrf("frostroot: %s already exists; use --force to overwrite it\n", recipePath)
		return exitUserError
	}

	snapshot, err := capture.Read(*rootDir)
	if err != nil {
		a.stderrf("frostroot capture: %v\n", err)
		return exitUserError
	}
	a.stdoutf("Read %s: Ubuntu %s, %d packages installed, %d asked for.\n", snapshot.Root, snapshot.Release, snapshot.InstalledCount, len(snapshot.Packages))

	if exitCode := a.runRecipeForm("capture", form.FromRecipe(snapshot.Recipe()), recipePath, *plain); exitCode != exitSuccess {
		return exitCode
	}

	reportPath := filepath.Join(a.RecipeDir, captureReportFileName)
	if err := writeFileAtomically(reportPath, snapshot.Report()); err != nil {
		a.stderrf("frostroot capture: writing the report: %v\n", err)
		return exitUserError
	}
	a.stdoutf("\nNot captured (details in %s):\n", captureReportFileName)
	for _, line := range snapshot.Summary() {
		a.stdoutf("  %s\n", line)
	}
	return exitSuccess
}

// writeFileAtomically writes content to path through a temporary file in the
// same directory, so a reader never sees a half-written file.
func writeFileAtomically(path, content string) (err error) {
	temporary, err := export.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			// Best effort: the write has already failed, and that error is
			// the one returned.
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}
	}()
	if _, err = temporary.WriteString(content); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporary.Name(), path)
}
