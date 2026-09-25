package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"

	"frostroot/internal/export"
	"frostroot/internal/form"
	"frostroot/internal/recipe"
)

// recipeTemplate renders the recipe init and edit write. The recipe is read
// and edited by people, so it is written from a commented template rather
// than with toml.Marshal, which cannot emit comments.
var recipeTemplate = template.Must(template.New("recipe").Funcs(template.FuncMap{
	"tomlQuote":     tomlQuote,
	"tomlQuoteList": tomlQuoteList,
}).Parse(`# frostroot recipe: the image you want. Edit it, then run: frostroot build
#
# Versions do not belong here. frostroot build writes the exact version of
# every installed package to frostroot.lock; commit both files.

[image]
name = {{tomlQuote .Image.Name}}  # file name of the tarball and name of the WSL distro
# 20.04 | 22.04 | 24.04. 20.04 is past standard support: its packages carry
# known security vulnerabilities that only Ubuntu Pro fixes.
release = {{tomlQuote .Image.Release}}
arch = {{tomlQuote .Image.Arch}}  # the only architecture in v1

[user]
name = {{tomlQuote .User.Name}}
sudo = {{.User.Sudo}}  # passwordless sudo: this is a lab image, not a hardened server

[wsl]
systemd = {{.WSL.Systemd}}
default_user = {{tomlQuote .WSL.DefaultUser}}

[locale]
lang = {{tomlQuote .Locale.Lang}}
timezone = {{tomlQuote .Locale.Timezone}}  # kept under WSL instead of following Windows

[packages]
# apt package names, exactly as you would pass them to apt install.
# Dependencies and Recommends come along automatically.
include = {{tomlQuoteList .Packages.Include}}
{{if .Python}}
[python]
# PyPI names, installed into the image's virtual environment, which every
# login shell finds on PATH. Versions belong in frostroot.lock, as above.
include = {{tomlQuoteList .Python.Include}}
{{if .Python.IndexURL}}# The index they resolve from instead of PyPI, for a network that mandates an
# internal mirror. https only, and no credentials: this file is committed.
index_url = {{tomlQuote .Python.IndexURL}}
{{end}}{{end}}{{if .Certificates}}
[certificates]
# PEM certificate authorities, as files next to this recipe. The image trusts
# them and so does the build, which is what a network that inspects TLS needs.
include = {{tomlQuoteList .Certificates.Include}}
{{end}}{{if .Sources}}
# Extra apt sources (PPAs, vendor repositories). Each needs its signing key
# as a file next to this recipe; frostroot init and edit fetch the keys of
# the sources they know. suite defaults to the release's code name,
# components to ["main"].
{{range .Sources}}
[[sources]]
name = {{tomlQuote .Name}}
url = {{tomlQuote .URL}}
{{if .Suite}}suite = {{tomlQuote .Suite}}
{{end}}{{if .Components}}components = {{tomlQuoteList .Components}}
{{end}}key = {{tomlQuote .Key}}
{{end}}{{end}}`))

const initUsageText = `usage: frostroot init [--force] [--plain] [--mirror URL] [--python-index URL] [--ca-bundle FILE | --insecure] [--refresh-index]

Answer a few questions and write frostroot.toml in the current directory.

` + indexUsageText

func (a *App) runInit(args []string) int {
	flags := a.newFlagSet("init", initUsageText)
	overwrite := flags.Bool("force", false, "overwrite an existing frostroot.toml")
	plain := flags.Bool("plain", false, "ask line by line instead of showing the full-screen form")
	indexOptions := addIndexFlags(flags)
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	recipePath := filepath.Join(a.RecipeDir, recipeFileName)
	if _, err := os.Stat(recipePath); err == nil && !*overwrite {
		a.stderrf("frostroot: %s already exists; use --force to overwrite it, or frostroot edit to change it\n", recipePath)
		return exitUserError
	}
	indexes, ok := a.packageIndexes(indexOptions)
	if !ok {
		return exitUserError
	}
	return a.runRecipeForm("init", form.Defaults(a.host()), recipePath, *overwrite, *plain, nil, nil, indexes)
}

// renderRecipe returns imageRecipe as the file init and edit write, byte
// for byte: the form shows this before asking whether to write it.
func renderRecipe(imageRecipe recipe.Recipe) (string, error) {
	var rendered strings.Builder
	if err := recipeTemplate.Execute(&rendered, imageRecipe); err != nil {
		return "", fmt.Errorf("rendering recipe: %w", err)
	}
	return rendered.String(), nil
}

// errRecipeExists is what writeRecipe returns for a recipe it may not
// replace: init and capture refuse an existing one before their form opens,
// and this is the same refusal for one that appeared while it was open.
var errRecipeExists = errors.New("a recipe appeared while the form was open; nothing written, use --force to overwrite it")

// writeRecipe renders imageRecipe to a temporary file next to recipePath,
// proves that it parses back to the same recipe, and renames it into place.
// Unless replace is set, a file already at recipePath is left alone and
// errRecipeExists returned: the check init and capture make before the form
// is a look at the directory that is minutes old by the time the form is
// answered, and a recipe that appeared meanwhile, from a checkout, an
// editor or a second frostroot, must not be replaced on its strength.
func writeRecipe(recipePath string, imageRecipe recipe.Recipe, replace bool) (err error) {
	renderedText, err := renderRecipe(imageRecipe)
	if err != nil {
		return err
	}
	temporary, err := export.CreateTemp(filepath.Dir(recipePath), ".frostroot.toml.*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		if err != nil {
			// Best effort: writing has already failed, and that error is the
			// one returned.
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
		}
	}()
	if _, err = temporary.WriteString(renderedText); err != nil {
		return err
	}
	if err = temporary.Close(); err != nil {
		return err
	}
	reloaded, err := recipe.Load(temporaryPath)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(reloaded, imageRecipe) {
		return fmt.Errorf("internal error: the rendered recipe does not parse back to the same recipe:\n%s", renderedText)
	}
	if replace {
		return os.Rename(temporaryPath, recipePath)
	}
	// Claiming the name with O_EXCL checks and creates in one step, so that
	// nothing slips in between the two; the rename then fills the claim.
	var claim *os.File
	claim, err = os.OpenFile(recipePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if errors.Is(err, os.ErrExist) {
		return fmt.Errorf("%s: %w", recipePath, errRecipeExists)
	}
	if err != nil {
		return err
	}
	if err = claim.Close(); err == nil {
		err = os.Rename(temporaryPath, recipePath)
	}
	if err != nil {
		// The claim is an empty file of this run's, not a recipe.
		_ = os.Remove(recipePath)
	}
	return err
}

// tomlQuote renders value as a TOML basic string.
func tomlQuote(value string) string {
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, character := range value {
		switch {
		case character == '"' || character == '\\':
			quoted.WriteByte('\\')
			quoted.WriteRune(character)
		case character < 0x20 || character == 0x7f:
			fmt.Fprintf(&quoted, `\u%04X`, character)
		default:
			quoted.WriteRune(character)
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}

// tomlQuoteList renders values as a single-line TOML array of strings.
func tomlQuoteList(values []string) string {
	quotedValues := make([]string, len(values))
	for index, value := range values {
		quotedValues[index] = tomlQuote(value)
	}
	return "[" + strings.Join(quotedValues, ", ") + "]"
}
