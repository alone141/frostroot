package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"frostroot/internal/form"
	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
	"frostroot/internal/tui"
)

// host is what the form may read from this machine.
func (a *App) host() form.Host { return form.Host{ReadFile: a.ReadFile} }

// useFullScreen reports whether a command should show the full-screen
// interface: only when the user asked for nothing else and both standard
// input and standard output are terminals that can draw it.
func (a *App) useFullScreen(plainRequested bool) bool {
	if plainRequested || a.Getenv("TERM") == "dumb" {
		return false
	}
	return a.IsTerminal(a.Stdin) && a.IsTerminal(a.Stdout)
}

// isCharacterDevice is the default terminal check: an *os.File on a character
// device, which is what a terminal is and a pipe or a regular file is not.
func isCharacterDevice(stream any) bool {
	file, isFile := stream.(*os.File)
	if !isFile {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// runRecipeForm asks the recipe questions starting from initial, in the
// full-screen or the plain interface, and writes the recipe to recipePath.
// Nothing is written unless every answer validates and the user confirms.
// providedKeys are armored signing keys by source name that the caller
// already has (capture read them from the machine); other missing keys are
// fetched.
func (a *App) runRecipeForm(commandName string, initial form.Values, recipePath string, plainRequested bool, providedKeys map[string][]byte) int {
	fields := form.Fields(a.host())
	fullScreen := a.useFullScreen(plainRequested)
	var values form.Values
	var problems []string
	var err error
	if fullScreen {
		values, err = tui.RunForm(context.Background(), fields, initial, a.Stdin, a.Stdout)
	} else {
		values, problems, err = a.askFieldsPlain(fields, initial)
	}
	if errors.Is(err, tui.ErrCanceled) {
		a.stderrf("frostroot %s: canceled; nothing written\n", commandName)
		return exitUserError
	}
	if err != nil {
		a.stderrf("frostroot %s: %v\n", commandName, err)
		return exitUserError
	}

	imageRecipe := form.ToRecipe(values)
	problems = append(problems, recipe.Validate(imageRecipe)...)
	if len(problems) > 0 {
		for _, problem := range problems {
			a.stderrf("frostroot %s: %s\n", commandName, problem)
		}
		a.stderrf("frostroot %s: nothing written\n", commandName)
		return exitUserError
	}
	if !fullScreen {
		// The full-screen form confirmed on its summary page; the plain
		// interface confirms here.
		a.stdoutf("\n%s\n\n", form.Summary(values))
		write, err := a.askYesNo("Write "+recipeFileName+"?", true)
		if err != nil {
			a.stderrf("frostroot %s: %v\n", commandName, err)
			return exitUserError
		}
		if !write {
			a.stderrf("frostroot %s: nothing written\n", commandName)
			return exitUserError
		}
	}

	if err := writeRecipe(recipePath, imageRecipe); err != nil {
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	a.stdoutf("\nWrote %s.\n", recipeFileName)
	if exitCode := a.fetchMissingKeys(commandName, imageRecipe.Sources, providedKeys); exitCode != exitSuccess {
		return exitCode
	}
	a.stdoutf("Next: frostroot validate, then frostroot build.\n")
	if slices.Contains(imageRecipe.Packages.Include, "python3-pip") && imageRecipe.Image.Release == "24.04" {
		a.stdoutf("Note: Ubuntu 24.04 enforces PEP 668, so pip install outside a virtual environment fails by design. Use: python3 -m venv .venv\n")
	}
	return exitSuccess
}

// fetchMissingKeys writes the signing key of every source whose key file is
// not there yet: from providedKeys when the caller has it, otherwise fetched
// and checked. Keys already present are left alone. Every source is tried;
// any failure ends in exitUserError with the recipe already written, so
// that fixing the network or saving a key by hand is all that is left to do.
func (a *App) fetchMissingKeys(commandName string, recipeSources []recipe.Source, providedKeys map[string][]byte) int {
	failed := false
	for _, source := range recipeSources {
		keyPath := recipe.KeyPath(a.RecipeDir, source)
		if _, err := os.Stat(keyPath); err == nil {
			continue
		}
		armored, provided := providedKeys[source.Name]
		origin := "the machine"
		if !provided {
			fetched, err := sources.FetchKey(context.Background(), a.KeyClient, source)
			if err != nil {
				a.stderrf("frostroot %s: %v\n", commandName, err)
				failed = true
				continue
			}
			armored, origin = fetched.Armored, fetched.SourceURL
		}
		key, err := pgp.ParsePublicKey(armored)
		if err != nil {
			a.stderrf("frostroot %s: key of %s: %v\n", commandName, source.Name, err)
			failed = true
			continue
		}
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
			a.stderrf("frostroot %s: %v\n", commandName, err)
			failed = true
			continue
		}
		if err := writeFileAtomically(keyPath, string(armored)); err != nil {
			a.stderrf("frostroot %s: writing %s: %v\n", commandName, source.Key, err)
			failed = true
			continue
		}
		a.stdoutf("Saved the signing key of %s from %s into %s (fingerprint %s)\n", source.Name, origin, source.Key, pgp.FormatFingerprint(key.Fingerprint))
	}
	if failed {
		a.stderrf("frostroot %s: %s is written, but frostroot validate will refuse it until every key is in place; frostroot edit fetches them again\n", commandName, recipeFileName)
		return exitUserError
	}
	return exitSuccess
}
