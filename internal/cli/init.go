package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"
	"unicode"

	"frostroot/internal/distro"
	"frostroot/internal/export"
	"frostroot/internal/recipe"
)

// presetPackages maps each init preset to the packages it expands to. Presets
// exist only in init; they are not a recipe feature.
var presetPackages = map[string][]string{
	"none":            {},
	"build-essential": {"build-essential", "git", "cmake", "pkg-config"},
	"python-lab":      {"python3", "python3-pip", "python3-venv", "git"},
}

// presetNames lists the presets in the order init offers them.
var presetNames = []string{"none", "build-essential", "python-lab"}

// recipeTemplate renders the recipe init writes. The recipe is read and edited
// by people, so it is written from a commented template rather than with
// toml.Marshal, which cannot emit comments.
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
`))

// initAnswers are the user's answers to init's questions.
type initAnswers struct {
	imageName     string
	release       string
	userName      string
	timezone      string
	presetName    string
	extraPackages string // separated by commas or whitespace
}

func (a *App) runInit(args []string) int {
	flags := a.newFlagSet("init", "usage: frostroot init [--force]\n\nAsk a few questions and write frostroot.toml in the current directory.\n\n")
	overwrite := flags.Bool("force", false, "overwrite an existing frostroot.toml")
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}

	recipePath := filepath.Join(a.RecipeDir, recipeFileName)
	if _, err := os.Stat(recipePath); err == nil && !*overwrite {
		a.stderrf("frostroot: %s already exists; use --force to overwrite it\n", recipePath)
		return exitUserError
	}

	a.stdoutf("Answer a few questions to create frostroot.toml. Press Enter to accept the default in [brackets].\n")
	answers, err := a.askInitQuestions()
	if err != nil {
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}

	var problems []string
	chosenPresetPackages, presetExists := presetPackages[answers.presetName]
	if !presetExists {
		problems = append(problems, fmt.Sprintf("unknown preset %q (choose one of: %s)", answers.presetName, strings.Join(presetNames, ", ")))
	}
	imageRecipe := recipe.Recipe{
		Image:    recipe.Image{Name: answers.imageName, Release: answers.release, Arch: distro.SupportedArch},
		User:     recipe.User{Name: answers.userName, Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: answers.userName},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: answers.timezone},
		Packages: recipe.Packages{Include: combinePackages(chosenPresetPackages, answers.extraPackages)},
	}
	problems = append(problems, recipe.Validate(imageRecipe)...)
	if len(problems) > 0 {
		for _, problem := range problems {
			a.stderrf("frostroot init: %s\n", problem)
		}
		a.stderrf("frostroot init: nothing written\n")
		return exitUserError
	}

	if err := writeRecipe(recipePath, imageRecipe); err != nil {
		a.stderrf("frostroot: %v\n", err)
		return exitUserError
	}
	a.stdoutf("\nWrote %s. Next: frostroot validate, then frostroot build.\n", recipeFileName)
	if answers.presetName == "python-lab" && answers.release == "24.04" {
		a.stdoutf("Note: Ubuntu 24.04 enforces PEP 668, so pip install outside a virtual environment fails by design. Use: python3 -m venv .venv\n")
	}
	return exitSuccess
}

// askInitQuestions asks init's questions in order, trimming each answer.
func (a *App) askInitQuestions() (initAnswers, error) {
	var answers initAnswers
	questions := []struct {
		text          string
		defaultAnswer string
		answer        *string
	}{
		{"Image name", "lab", &answers.imageName},
		{"Ubuntu release (" + strings.Join(distro.SupportedVersions(), ", ") + ")", "24.04", &answers.release},
		{"User name", "student", &answers.userName},
		{"Timezone, e.g. UTC or Europe/Istanbul", "UTC", &answers.timezone},
		{"Package preset (" + strings.Join(presetNames, ", ") + ")", "none", &answers.presetName},
		{"Extra packages, separated by spaces or commas", "", &answers.extraPackages},
	}
	for _, question := range questions {
		answer, err := a.Prompt.Ask(question.text, question.defaultAnswer)
		if err != nil {
			return initAnswers{}, err
		}
		*question.answer = strings.TrimSpace(answer)
	}
	return answers, nil
}

// combinePackages returns the preset's packages followed by the extras,
// keeping the first occurrence of each name. Extras may be separated by
// commas or whitespace: package names contain neither, and people type them
// as they would for apt install.
func combinePackages(presetPackageNames []string, extraPackages string) []string {
	combined := []string{}
	alreadyListed := map[string]bool{}
	addOnce := func(packageName string) {
		if packageName != "" && !alreadyListed[packageName] {
			alreadyListed[packageName] = true
			combined = append(combined, packageName)
		}
	}
	for _, packageName := range presetPackageNames {
		addOnce(packageName)
	}
	isSeparator := func(character rune) bool { return character == ',' || unicode.IsSpace(character) }
	for _, packageName := range strings.FieldsFunc(extraPackages, isSeparator) {
		addOnce(packageName)
	}
	return combined
}

// writeRecipe renders imageRecipe to a temporary file next to recipePath,
// proves that it parses back to the same recipe, and renames it into place.
func writeRecipe(recipePath string, imageRecipe recipe.Recipe) (err error) {
	var rendered strings.Builder
	if err := recipeTemplate.Execute(&rendered, imageRecipe); err != nil {
		return fmt.Errorf("rendering recipe: %w", err)
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
	if _, err = temporary.WriteString(rendered.String()); err != nil {
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
		return fmt.Errorf("internal error: the rendered recipe does not parse back to the same recipe:\n%s", rendered.String())
	}
	return os.Rename(temporaryPath, recipePath)
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
