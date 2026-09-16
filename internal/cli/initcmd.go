package cli

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"text/template"

	"frostroot/internal/distro"
	"frostroot/internal/recipe"
)

// Presets exist only in init. They expand to package names in the recipe and
// are not a recipe feature.
var presets = map[string][]string{
	"none":            {},
	"build-essential": {"build-essential", "git", "cmake", "pkg-config"},
	"python-lab":      {"python3", "python3-pip", "python3-venv", "git"},
}

var presetNames = []string{"none", "build-essential", "python-lab"}

// The recipe is read and edited by people, so init writes it from a commented
// template rather than with toml.Marshal.
var recipeTmpl = template.Must(template.New("recipe").Funcs(template.FuncMap{
	"q":    tomlString,
	"list": tomlList,
}).Parse(`# frostroot recipe: the image you want. Edit it, then run: frostroot build
#
# Versions do not belong here. frostroot build writes the exact version of
# every installed package to frostroot.lock; commit both files.

[image]
name = {{q .Image.Name}}  # file name of the tarball and name of the WSL distro
# 20.04 | 22.04 | 24.04. 20.04 is past standard support: its packages carry
# known security vulnerabilities that only Ubuntu Pro fixes.
release = {{q .Image.Release}}
arch = {{q .Image.Arch}}  # the only architecture in v1

[user]
name = {{q .User.Name}}
sudo = {{.User.Sudo}}  # passwordless sudo: this is a lab image, not a hardened server

[wsl]
systemd = {{.WSL.Systemd}}
default_user = {{q .WSL.DefaultUser}}

[locale]
lang = {{q .Locale.Lang}}
timezone = {{q .Locale.Timezone}}  # kept under WSL instead of following Windows

[packages]
# apt package names, exactly as you would pass them to apt install.
# Dependencies and Recommends come along automatically.
include = {{list .Packages.Include}}
`))

func (a *App) cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	force := fs.Bool("force", false, "overwrite an existing frostroot.toml")
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: frostroot init [--force]\n\nAsk a few questions and write frostroot.toml in the current directory.\n\n")
		fs.PrintDefaults()
	}
	if code, done := a.parseFlags(fs, args); done {
		return code
	}

	path := filepath.Join(a.Dir, recipeFile)
	if _, err := os.Stat(path); err == nil && !*force {
		fmt.Fprintf(a.Stderr, "frostroot: %s already exists; use --force to overwrite it\n", path)
		return 1
	}

	fmt.Fprintln(a.Stdout, "Answer a few questions to create frostroot.toml. Press Enter to accept the default in [brackets].")
	var answers [6]string
	for i, q := range []struct{ question, def string }{
		{"Image name", "lab"},
		{"Ubuntu release (" + strings.Join(distro.KnownReleases(), ", ") + ")", "24.04"},
		{"User name", "student"},
		{"Timezone, e.g. UTC or Europe/Istanbul", "UTC"},
		{"Package preset (" + strings.Join(presetNames, ", ") + ")", "none"},
		{"Extra packages, comma-separated", ""},
	} {
		answer, err := a.Prompt.Ask(q.question, q.def)
		if err != nil {
			fmt.Fprintf(a.Stderr, "frostroot: %v\n", err)
			return 1
		}
		answers[i] = strings.TrimSpace(answer)
	}
	name, release, user, tz, preset, extra := answers[0], answers[1], answers[2], answers[3], answers[4], answers[5]

	var probs []string
	base, ok := presets[preset]
	if !ok {
		probs = append(probs, fmt.Sprintf("unknown preset %q (choose one of: %s)", preset, strings.Join(presetNames, ", ")))
	}
	r := recipe.Recipe{
		Image:    recipe.Image{Name: name, Release: release, Arch: "amd64"},
		User:     recipe.User{Name: user, Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: user},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: tz},
		Packages: recipe.Packages{Include: packageList(base, extra)},
	}
	probs = append(probs, recipe.Validate(r)...)
	if len(probs) > 0 {
		for _, p := range probs {
			fmt.Fprintf(a.Stderr, "frostroot init: %s\n", p)
		}
		fmt.Fprintf(a.Stderr, "frostroot init: nothing written\n")
		return 1
	}

	if err := writeRecipe(path, r); err != nil {
		fmt.Fprintf(a.Stderr, "frostroot: %v\n", err)
		return 1
	}
	fmt.Fprintf(a.Stdout, "\nWrote %s. Next: frostroot validate, then frostroot build.\n", recipeFile)
	if preset == "python-lab" && release == "24.04" {
		fmt.Fprintln(a.Stdout, "Note: Ubuntu 24.04 enforces PEP 668, so pip install outside a virtual environment fails by design. Use: python3 -m venv .venv")
	}
	return 0
}

// packageList expands a preset and appends comma-separated extras, keeping
// the first occurrence of each name.
func packageList(preset []string, extra string) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(p string) {
		if p = strings.TrimSpace(p); p != "" && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	for _, p := range preset {
		add(p)
	}
	for _, p := range strings.Split(extra, ",") {
		add(p)
	}
	return out
}

// writeRecipe renders the template to a temporary file, proves it parses back
// to the same recipe, and renames it into place.
func writeRecipe(path string, r recipe.Recipe) error {
	var b strings.Builder
	if err := recipeTmpl.Execute(&b, r); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o644); err != nil {
		return err
	}
	back, err := recipe.Load(tmp)
	if err == nil && !reflect.DeepEqual(back, r) {
		err = fmt.Errorf("internal error: rendered recipe does not round-trip:\n%s", b.String())
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}

// tomlString renders a TOML basic string.
func tomlString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch {
		case c == '"' || c == '\\':
			b.WriteByte('\\')
			b.WriteRune(c)
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, c)
		default:
			b.WriteRune(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func tomlList(xs []string) string {
	quoted := make([]string, len(xs))
	for i, x := range xs {
		quoted[i] = tomlString(x)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
