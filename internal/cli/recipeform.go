package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"frostroot/internal/distro"
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

// isCharacterDevice reports whether stream is an *os.File on a character
// device, which a terminal is and a pipe or a regular file is not, and
// which /dev/null is as well; isTerminal is the check.
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
// replace says whether a recipe already at recipePath may be replaced: edit
// rewrites the file it opened, and init and capture pass --force; without
// it, a recipe that appeared while the form was open is left alone.
// providedKeys are armored signing keys by source name that the caller
// already has (capture read them from the machine); other missing keys are
// fetched. intro is shown before the questions: what capture has to say,
// or nothing. fixed says which of the family and the target the full-screen
// form does not ask, because the command chose them already; what it does
// ask decides the questions after it. The plain interface asks neither,
// because people pipe answers into it, and asks initial's family's and
// target's questions. wrote says whether the recipe was written, which it
// can be when the exit code is a failure: a key fetched afterwards may not
// come. ctx ends the fetch of those keys.
func (a *App) runRecipeForm(ctx context.Context, commandName string, initial form.Values, recipePath string, replace, plainRequested bool, fixed form.Fixed, providedKeys map[string][]byte, intro []form.Field, indexes *packageIndexes) (exitCode int, wrote bool) {
	host := a.host()
	host.OpenIndex = indexes.Open
	host.OpenPythonIndex = indexes.OpenPython
	fullScreen := a.useFullScreen(plainRequested)
	if !fullScreen {
		fixed = form.Fixed{Family: true, Target: true}
	}
	// A recipe, or the machine capture reads, may hold a timezone or a
	// locale the form's lists lack; it is offered too, so that it survives.
	questions := form.KeepCurrentChoices(form.FieldsFor(form.Fields(host), form.VariantOf(initial), fixed), initial)
	fields := slices.Concat(intro, questions)
	preview := recipePreview(recipePath, indexes)
	var values form.Values
	var problems []string
	var err error
	if fullScreen {
		values, problems, err = a.runFullScreenForm(fields, initial, preview)
	} else {
		values, problems, err = a.askFieldsPlain(fields, initial)
	}
	if errors.Is(err, tui.ErrCanceled) {
		// Esc or Ctrl-C, which at a question of the plain interface ends
		// the process with SIGINT: interrupted either way.
		a.stderrf("frostroot %s: canceled; nothing written\n", commandName)
		return exitInterrupted, false
	}
	if errors.Is(err, tui.ErrDeclined) {
		// No at the summary page, as at the plain interface's last question.
		return a.declineToWrite(commandName, preview(values).Unchanged), false
	}
	if errors.Is(err, context.Canceled) {
		a.stderrf("frostroot %s: interrupted; nothing written\n", commandName)
		return exitInterrupted, false
	}
	if err != nil {
		a.stderrf("frostroot %s: %v\n", commandName, err)
		return exitUserError, false
	}

	imageRecipe := form.ToRecipe(values)
	problems = append(problems, recipe.Validate(imageRecipe)...)
	if len(problems) > 0 {
		for _, problem := range problems {
			a.stderrf("frostroot %s: %s\n", commandName, problem)
		}
		a.stderrf("frostroot %s: nothing written\n", commandName)
		return exitUserError, false
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
			return exitUserError, false
		}
		if !write {
			return a.declineToWrite(commandName, shown.Unchanged), false
		}
	}

	if err := writeRecipe(recipePath, imageRecipe, replace); err != nil {
		a.stderrf("frostroot %s: %v\n", commandName, err)
		return exitUserError, false
	}
	a.stdoutf("\nWrote %s.\n", recipeFileName)
	a.trustWrittenCertificates(imageRecipe.CertificatePaths(), indexes)
	if exitCode := a.fetchMissingKeys(ctx, commandName, imageRecipe.Sources, providedKeys, indexes.insecureTLS()); exitCode != exitSuccess {
		return exitCode, true
	}
	a.stdoutf("Next: frostroot validate, then frostroot build.\n")
	// The recipe was validated before it was written, so the release is
	// known; were it not, there would be nothing to say about its Python.
	release, err := distro.Lookup(imageRecipe.Image.Release, imageRecipe.Image.Arch)
	if err == nil && release.ExternallyManagedPython && slices.Contains(imageRecipe.Packages.Include, "python3-pip") {
		a.stdoutf("Note: Ubuntu %s enforces PEP 668, so pip install outside a virtual environment fails by design. Use: python3 -m venv .venv\n", imageRecipe.Image.Release)
	}
	return exitSuccess, true
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

// runFullScreenForm runs the full-screen form until its answers make a
// recipe that validates, or it is left. Answers that do not validate send
// the person back to the form with every answer kept and what is wrong at
// the top of the Image page, rather than out of it with none: an Ubuntu
// 22.04 disk, or a Fedora one, was found only after the summary's Write,
// and every answer of the session went with the exit. The summary page
// says so before that Write.
func (a *App) runFullScreenForm(fields []form.Field, initial form.Values, preview tui.PreviewFunc) (form.Values, []string, error) {
	run := a.runForm
	if run == nil {
		run = tui.RunForm
	}
	warnedPreview := func(values form.Values) tui.Preview {
		shown := preview(values)
		if problems := recipe.Validate(form.ToRecipe(values)); len(problems) > 0 {
			warning := "These answers cannot be written yet; Write takes you back to them:\n  " + strings.Join(problems, "\n  ")
			if shown.Warning != "" {
				warning += "\n\n" + shown.Warning
			}
			shown.Warning = warning
		}
		return shown
	}
	asked := fields
	for {
		values, err := run(context.Background(), asked, initial, warnedPreview, a.Stdin, a.Stdout)
		if err != nil {
			return values, nil, err
		}
		problems := recipe.Validate(form.ToRecipe(values))
		if len(problems) == 0 {
			return values, nil, nil
		}
		initial = values
		note := form.NoteField(form.PageImage, "Not written yet", "Change these, then write again:\n  "+strings.Join(problems, "\n  "))
		asked = slices.Concat([]form.Field{note}, fields)
	}
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
	// Whatever the file will say, names no index has are worth a word
	// before it is written. An index is read only when the recipe has names
	// for it to judge: this runs as the last page opens, and reading PyPI's
	// cache for a Fedora recipe, which has no Python packages, held that
	// page up for seconds.
	return func(values form.Values) tui.Preview {
		preview := describe(values)
		imageRecipe := form.ToRecipe(values)
		var apt, python form.PackageIndex
		if len(imageRecipe.Packages.Include) > 0 {
			apt = indexes.Known(form.IndexRequestFor(values))
		}
		if len(imageRecipe.PythonPackages()) > 0 {
			python = indexes.KnownPython()
		}
		preview.Warning = form.Warnings(values, apt, python)
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
// connection is saved with a word on where to check it. When ctx ends, the
// fetch in flight stops and no other key is fetched: the result is
// exitInterrupted, with the keys still missing named.
func (a *App) fetchMissingKeys(ctx context.Context, commandName string, recipeSources []recipe.Source, providedKeys map[string][]byte, insecure bool) int {
	failed := false
	var missing []string
	for _, source := range recipeSources {
		keyPath := recipe.KeyPath(a.RecipeDir, source)
		if _, err := os.Stat(keyPath); err == nil {
			continue
		}
		armored, provided := providedKeys[source.Name]
		origin := "the machine"
		discovered := false
		if !provided {
			if ctx.Err() != nil {
				missing = append(missing, source.Name)
				continue
			}
			fetched, err := sources.FetchKey(ctx, a.KeyClient, source)
			if err != nil {
				missing = append(missing, source.Name)
				// A fetch the interrupt ended did not fail on its own; the
				// line after the loop says what happened.
				if ctx.Err() == nil {
					a.stderrf("frostroot %s: %v\n", commandName, err)
					failed = true
				}
				continue
			}
			armored, origin, discovered = fetched.Armored, fetched.SourceURL, fetched.Discovered
		}
		key, err := pgp.ParsePublicKey(armored)
		if err != nil {
			a.stderrf("frostroot %s: key of %s: %v\n", commandName, source.Name, err)
			missing = append(missing, source.Name)
			failed = true
			continue
		}
		// A keys/ that is a link out of the recipe directory refuses the
		// key rather than saving it wherever the link leads.
		writePath, err := recipe.PrepareFilePath(a.RecipeDir, source.Key)
		if err != nil {
			a.stderrf("frostroot %s: writing %s: %v\n", commandName, source.Key, err)
			missing = append(missing, source.Name)
			failed = true
			continue
		}
		if err := writeFileAtomically(writePath, string(armored)); err != nil {
			a.stderrf("frostroot %s: writing %s: %v\n", commandName, source.Key, err)
			missing = append(missing, source.Name)
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
	if ctx.Err() != nil && len(missing) > 0 {
		a.stderrf("frostroot %s: interrupted; %s is written without the keys of %s, so frostroot validate will refuse it until every key is in place; frostroot edit fetches them again\n", commandName, recipeFileName, strings.Join(missing, ", "))
		return exitInterrupted
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
