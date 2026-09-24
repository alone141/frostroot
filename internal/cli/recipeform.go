package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"frostroot/internal/form"
	"frostroot/internal/pgp"
	"frostroot/internal/recipe"
	"frostroot/internal/sources"
	"frostroot/internal/textdiff"
	"frostroot/internal/tui"
)

// host is what the form may read from this machine.
func (a *App) host() form.Host { return form.Host{ReadFile: a.ReadFile, RecipeDir: a.RecipeDir} }

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
// fetched. intro is shown before the questions: what capture has to say,
// or nothing.
func (a *App) runRecipeForm(commandName string, initial form.Values, recipePath string, plainRequested bool, providedKeys map[string][]byte, intro []form.Field, indexes *packageIndexes) int {
	host := a.host()
	host.OpenIndex = indexes.Open
	host.OpenPythonIndex = indexes.OpenPython
	fields := slices.Concat(intro, form.Fields(host))
	fullScreen := a.useFullScreen(plainRequested)
	preview := recipePreview(recipePath, indexes)
	var values form.Values
	var problems []string
	var err error
	if fullScreen {
		values, err = tui.RunForm(context.Background(), fields, initial, preview, a.Stdin, a.Stdout)
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
		// The full-screen form confirmed on its summary page, with the
		// preview above the question; the plain interface shows the same
		// and confirms here.
		shown := preview(values)
		a.stdoutf("\n%s\n", form.Summary(values))
		if shown.Warning != "" {
			a.stdoutf("\n%s\n", shown.Warning)
		}
		a.stdoutf("\n%s\n", shown.Heading)
		if shown.Text != "" {
			a.stdoutf("\n%s\n", strings.TrimRight(shown.Text, "\n"))
		}
		question := "Write " + recipeFileName + "?"
		if shown.Unchanged {
			question = "Nothing changes. Write anyway?"
		}
		write, err := a.askYesNo(question, !shown.Unchanged)
		if err != nil {
			a.stderrf("frostroot %s: %v\n", commandName, err)
			return exitUserError
		}
		if !write {
			return a.declineToWrite(commandName, shown.Unchanged)
		}
	}

	if err := writeRecipe(recipePath, imageRecipe); err != nil {
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	a.stdoutf("\nWrote %s.\n", recipeFileName)
	if exitCode := a.fetchMissingKeys(commandName, imageRecipe.Sources, providedKeys, indexes.insecureTLS()); exitCode != exitSuccess {
		return exitCode
	}
	a.stdoutf("Next: frostroot validate, then frostroot build.\n")
	if slices.Contains(imageRecipe.Packages.Include, "python3-pip") && imageRecipe.Image.Release == "24.04" {
		a.stdoutf("Note: Ubuntu 24.04 enforces PEP 668, so pip install outside a virtual environment fails by design. Use: python3 -m venv .venv\n")
	}
	return exitSuccess
}

// declineToWrite reports that the recipe was not written. Declining to
// rewrite a file that would not change is not an error: nothing was asked
// for that did not happen.
func (a *App) declineToWrite(commandName string, unchanged bool) int {
	if unchanged {
		a.stdoutf("frostroot %s: nothing changes; nothing written\n", commandName)
		return exitSuccess
	}
	a.stderrf("frostroot %s: nothing written\n", commandName)
	return exitUserError
}

// recipePreview returns what the last page of the form shows for the
// answers so far: the recipe as it will be written when recipePath does
// not exist yet, and otherwise how the file there will change, because
// edit and init --force replace it — comments of the user's own included,
// which the preview says when it sees any.
func recipePreview(recipePath string, indexes *packageIndexes) tui.PreviewFunc {
	current, err := os.ReadFile(recipePath)
	replacing := err == nil
	describe := func(values form.Values) tui.Preview {
		rendered, err := renderRecipe(form.ToRecipe(values))
		if err != nil {
			return tui.Preview{Heading: err.Error()}
		}
		if !replacing {
			return tui.Preview{Heading: "This is what " + recipeFileName + " will say:", Text: rendered}
		}
		difference := textdiff.Lines(string(current), rendered)
		if difference.Changed() == 0 {
			return tui.Preview{Heading: "Nothing changes: " + recipeFileName + " already says this.", Text: rendered, Unchanged: true}
		}
		heading := fmt.Sprintf("%d lines change:", difference.Changed())
		if difference.Changed() == 1 {
			heading = "1 line changes:"
		}
		if hasOwnComments(string(current), rendered) {
			heading = strings.TrimSuffix(heading, ":") + "; your own comments in the file are replaced by the template's:"
		}
		return tui.Preview{Heading: heading, Text: difference.String()}
	}
	// Whatever the file will say, names no index has are
	// worth a word before it is written.
	return func(values form.Values) tui.Preview {
		preview := describe(values)
		preview.Warning = form.Warnings(values, indexes.Known(form.IndexRequestFor(values)), indexes.KnownPython())
		return preview
	}
}

// hasOwnComments reports whether current holds a comment line that the
// rendered recipe does not: something a person wrote, which the template
// will not write back.
func hasOwnComments(current, rendered string) bool {
	renderedLines := map[string]bool{}
	for _, line := range strings.Split(rendered, "\n") {
		renderedLines[strings.TrimSpace(line)] = true
	}
	for _, line := range strings.Split(current, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") && !renderedLines[trimmed] {
			return true
		}
	}
	return false
}

// fetchMissingKeys writes the signing key of every source whose key file is
// not there yet: from providedKeys when the caller has it, otherwise fetched
// and checked. Keys already present are left alone. Every source is tried;
// any failure ends in exitUserError with the recipe already written, so
// that fixing the network or saving a key by hand is all that is left to do.
// insecure is --insecure: a key whose fingerprint was discovered over such a
// connection is saved with a word on where to check it.
func (a *App) fetchMissingKeys(commandName string, recipeSources []recipe.Source, providedKeys map[string][]byte, insecure bool) int {
	failed := false
	for _, source := range recipeSources {
		keyPath := recipe.KeyPath(a.RecipeDir, source)
		if _, err := os.Stat(keyPath); err == nil {
			continue
		}
		armored, provided := providedKeys[source.Name]
		origin := "the machine"
		discovered := false
		if !provided {
			fetched, err := sources.FetchKey(context.Background(), a.KeyClient, source)
			if err != nil {
				a.stderrf("frostroot %s: %v\n", commandName, err)
				failed = true
				continue
			}
			armored, origin, discovered = fetched.Armored, fetched.SourceURL, fetched.Discovered
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
		a.stdoutf("Saved the signing key of %s from %s into %s (%s)\n", source.Name, origin, source.Key, describeFingerprints(key.Fingerprints))
		// A catalog key is checked against a fingerprint compiled into
		// frostroot, whatever the connection. A PPA's fingerprint is
		// Launchpad's answer over that same connection, so with --insecure
		// the key and the check of it came from the same unverified place,
		// and the file is trusted by every build from now on.
		if insecure && discovered {
			if owner, name, isPPA := sources.PPAOf(source.URL); isPPA {
				a.stderrf("warning: the fingerprint of %s came from Launchpad over an unverified connection, and so did the key; compare it with %s before building.\n", source.Name, sources.PPAPageURL(owner, name))
			}
		}
	}
	if failed {
		a.stderrf("frostroot %s: %s is written, but frostroot validate will refuse it until every key is in place; frostroot edit fetches them again\n", commandName, recipeFileName)
		return exitUserError
	}
	return exitSuccess
}

// describeFingerprints names the primary keys of a saved key file, for the
// line that tells the user what was saved. A key file may hold more than one
// primary key — GitHub's keyring holds two — and it is installed whole as the
// source's signed-by keyring, where apt accepts a Release signed by any key
// in it. Naming only the first would under-report what is being trusted.
func describeFingerprints(fingerprints []string) string {
	label := "fingerprint"
	if len(fingerprints) > 1 {
		label = "fingerprints"
	}
	return label + " " + pgp.FormatFingerprints(fingerprints)
}
