package cli

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"frostroot/internal/capture"
	"frostroot/internal/export"
	"frostroot/internal/form"
	"frostroot/internal/recipe"
)

// captureReportFileName is the report capture writes next to the recipe.
const captureReportFileName = "frostroot-capture.md"

const captureUsageText = `usage: frostroot capture [--root DIR] [--force] [--plain] [--mirror URL] [--python-index URL] [--ca-bundle FILE | --insecure] [--refresh-index]

Describe an installed Ubuntu system as a recipe: read what apt installed,
which third-party apt sources it uses and how the machine is set up, open
the form with those values, and write frostroot.toml plus
frostroot-capture.md, a report of everything a recipe cannot carry (pip and
npm installs, /opt, edits to /etc, dotfiles...). Reads package metadata and
a few configuration files; the only files it copies are the public signing
keys of apt sources; needs no root.

` + indexUsageText

func (a *App) runCapture(args []string) int {
	flags := a.newFlagSet("capture", captureUsageText)
	rootDir := flags.String("root", "/", "root of the system to describe, e.g. a mounted `DIR`")
	overwrite := flags.Bool("force", false, "overwrite an existing frostroot.toml")
	plain := flags.Bool("plain", false, "ask line by line instead of showing the full-screen form")
	indexOptions := addIndexFlags(flags)
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
	// The authorities the machine added are written before the form opens,
	// because the Trust page checks that every file it names is there and
	// holds a certificate. A form the person abandons leaves nothing behind
	// that was not already there: the files this run created go again
	// unless the recipe that names them was written, and a file that was
	// already there is never touched.
	writtenCertificates, err := a.writeCapturedCertificates(snapshot.Certificates)
	if err != nil {
		a.stderrf("frostroot capture: %v\n", err)
		// The ones written before the failure, not the whole set: the file
		// that failed was never created, and whatever was there before the
		// run is not in the list.
		removeCapturedCertificates(writtenCertificates)
		return exitUserError
	}

	// What was read, and what a recipe cannot carry, before the questions:
	// the packages page is answered knowing what is missing.
	intro := []form.Field{form.NoteField(form.PageCaptured, "What capture found", captureNoteText(snapshot))}
	indexes, ok := a.packageIndexes(indexOptions)
	if !ok {
		removeCapturedCertificates(writtenCertificates)
		return exitUserError
	}
	if exitCode := a.runRecipeForm("capture", form.FromRecipe(snapshot.Recipe()), recipePath, *plain, snapshot.Keys, intro, indexes); exitCode != exitSuccess {
		// The form can fail after the recipe is on disk: a key it then
		// fetches may not come. That recipe names these files, and a
		// certificate read off a machine cannot be fetched again the way a
		// key can, so they stay whenever the recipe does.
		if _, err := os.Stat(recipePath); err != nil {
			removeCapturedCertificates(writtenCertificates)
		}
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

// captureNoteText is the first page of the capture form: where the values
// came from, and every area a recipe cannot carry with its count, so that
// the user decides about packages with the facts. The details are in the
// report, written with the recipe.
func captureNoteText(snapshot capture.Snapshot) string {
	var text strings.Builder
	fmt.Fprintf(&text, "Read %s: Ubuntu %s, %d packages installed, %d asked for, %d third-party sources with keys",
		snapshot.Root, snapshot.Release, snapshot.InstalledCount, len(snapshot.Packages), len(snapshot.Sources))
	if count := len(snapshot.Certificates); count > 0 {
		fmt.Fprintf(&text, ", %d certificate authorities", count)
	}
	text.WriteString(".\n")
	if machine := snapshot.Finding(capture.AreaMachinePackages); machine.Count > 0 {
		fmt.Fprintf(&text, "\n%d packages belong to the machine, not the image (kernel, bootloader, firmware,\ndrivers). They are left out; the report lists every one.\n", machine.Count)
	}
	text.WriteString("\nA recipe cannot carry:\n")
	for _, line := range snapshot.Summary() {
		text.WriteString("  " + line + "\n")
	}
	text.WriteString("\nThe details go to " + captureReportFileName + ", written beside the recipe.")
	return text.String()
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

// writeCapturedCertificates saves the authorities capture read from the
// machine beside the recipe, and returns the files it created. A file that
// was already there is left alone, whatever it holds, and not reported as
// created: it is the person's, and neither the form's outcome nor the
// machine's bytes may decide its fate. The Trust page then checks it like
// any other file the recipe names, and says so if it is not a certificate.
// Whatever happens after this, the files the run created are the whole of
// what removeCapturedCertificates may undo.
func (a *App) writeCapturedCertificates(certificates map[string][]byte) (written []string, err error) {
	for _, certificatePath := range slices.Sorted(maps.Keys(certificates)) {
		fullPath := recipe.CertificatePath(a.RecipeDir, certificatePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			return written, fmt.Errorf("writing %s: %w", certificatePath, err)
		}
		// O_EXCL, so that the check and the write are one step: a file
		// that appears between them is left alone too.
		file, err := os.OpenFile(fullPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return written, fmt.Errorf("writing %s: %w", certificatePath, err)
		}
		// Created, so it is the run's to remove, even if writing it fails.
		written = append(written, fullPath)
		_, err = file.Write(certificates[certificatePath])
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			return written, fmt.Errorf("writing %s: %w", certificatePath, err)
		}
	}
	return written, nil
}

// removeCapturedCertificates undoes writeCapturedCertificates when the form
// was abandoned, so that declining a capture leaves the directory as it was.
// Best effort throughout: the recipe was not written either, and that is
// what the exit code already says.
func removeCapturedCertificates(written []string) {
	directories := map[string]bool{}
	for _, path := range written {
		_ = os.Remove(path)
		directories[filepath.Dir(path)] = true
	}
	for directory := range directories {
		// Fails harmlessly while anything else is still in there.
		_ = os.Remove(directory)
	}
}
