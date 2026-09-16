// Package cli implements the frostroot command line: init, validate, build.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"frostroot/internal/builder"
	"frostroot/internal/recipe"
)

const recipeFile = "frostroot.toml"

// Prompt asks one question and returns the answer, or defaultValue when the
// answer is empty. init is the only command that prompts; tests script it.
type Prompt interface {
	Ask(question, defaultValue string) (string, error)
}

// App is one invocation of frostroot. Zero-valued fields fall back to the
// real process environment.
type App struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Dir     string // recipe directory, default the working directory
	GOOS    string
	Prompt  Prompt
	Builder *builder.Builder
	Getenv  func(string) string
	// WSLPath converts a Linux path to its Windows form for the import hint.
	// An error just means the hint is not printed.
	WSLPath func(linuxPath string) (string, error)
}

// New returns an App wired to the real process.
func New() *App {
	return (&App{}).withDefaults()
}

func (a *App) withDefaults() *App {
	if a.Stdin == nil {
		a.Stdin = os.Stdin
	}
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	if a.Dir == "" {
		if wd, err := os.Getwd(); err == nil {
			a.Dir = wd
		}
	}
	if a.GOOS == "" {
		a.GOOS = runtime.GOOS
	}
	if a.Getenv == nil {
		a.Getenv = os.Getenv
	}
	if a.Prompt == nil {
		a.Prompt = newLinePrompt(a.Stdin, a.Stdout)
	}
	if a.WSLPath == nil {
		a.WSLPath = wslpath
	}
	return a
}

const usage = `usage: frostroot <command> [flags]

Commands:
  init       ask a few questions and write frostroot.toml
  validate   check frostroot.toml (no network, no root)
  build      build frostroot.lock and dist/<name>-ubuntu-<release>-amd64.tar.gz

Run "frostroot <command> -h" for a command's flags.
`

// Run executes one command and returns the process exit code: 0 success,
// 1 user error, 2 build error, 130 interrupted.
func (a *App) Run(args []string) int {
	a.withDefaults()
	if len(args) == 0 {
		fmt.Fprint(a.Stderr, usage)
		return 1
	}
	switch args[0] {
	case "init":
		return a.cmdInit(args[1:])
	case "validate":
		return a.cmdValidate(args[1:])
	case "build":
		return a.cmdBuild(args[1:])
	case "help", "-h", "--help":
		fmt.Fprint(a.Stdout, usage)
		return 0
	default:
		fmt.Fprintf(a.Stderr, "frostroot: unknown command %q\n\n%s", args[0], usage)
		return 1
	}
}

// parseFlags parses a subcommand's flags. It returns done=true with the exit
// code when the command should stop (help requested, or bad flags).
func (a *App) parseFlags(fs *flag.FlagSet, args []string) (code int, done bool) {
	fs.SetOutput(a.Stderr)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0, true
		}
		return 1, true
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(a.Stderr, "frostroot %s: unexpected argument %q\n", fs.Name(), fs.Arg(0))
		fs.Usage()
		return 1, true
	}
	return 0, false
}

// loadRecipe loads and validates frostroot.toml, printing every problem.
func (a *App) loadRecipe() (recipe.Recipe, bool) {
	path := filepath.Join(a.Dir, recipeFile)
	r, err := recipe.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(a.Stderr, "frostroot: no %s in %s; create one with: frostroot init\n", recipeFile, a.Dir)
		} else {
			fmt.Fprintf(a.Stderr, "frostroot: %v\n", err)
		}
		return recipe.Recipe{}, false
	}
	if probs := recipe.Validate(r); len(probs) > 0 {
		for _, p := range probs {
			fmt.Fprintf(a.Stderr, "%s: %s\n", recipeFile, p)
		}
		return recipe.Recipe{}, false
	}
	return r, true
}

func (a *App) cmdValidate(args []string) int {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprint(fs.Output(), "usage: frostroot validate\n\nCheck frostroot.toml in the current directory. No network, no root.\n")
	}
	if code, done := a.parseFlags(fs, args); done {
		return code
	}
	r, ok := a.loadRecipe()
	if !ok {
		return 1
	}
	fmt.Fprintf(a.Stdout, "%s: ok (%s, Ubuntu %s %s, %d packages requested)\n",
		recipeFile, r.Image.Name, r.Image.Release, r.Image.Arch, len(r.Packages.Include))
	return 0
}
