// Package cli implements the frostroot command line: init, validate and build.
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

// recipeFileName is the recipe every command works on, in App.RecipeDir.
const recipeFileName = "frostroot.toml"

// Exit codes returned by App.Run.
const (
	exitSuccess     = 0
	exitUserError   = 1   // something the user can fix: a recipe, a flag, the host
	exitBuildFailed = 2   // the build itself failed
	exitInterrupted = 130 // Ctrl-C, by shell convention 128 + SIGINT
)

// Prompt asks one question and returns the answer, or defaultAnswer when the
// answer is empty. init is the only command that prompts, and tests script it.
type Prompt interface {
	Ask(question, defaultAnswer string) (string, error)
}

// App is one invocation of frostroot. Fields left at their zero value fall
// back to the real process environment.
type App struct {
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
	RecipeDir string // directory holding frostroot.toml; defaults to the working directory
	GOOS      string // operating system; defaults to runtime.GOOS
	Prompt    Prompt
	Builder   *builder.Builder
	Getenv    func(string) string
	// WSLPath converts a Linux path to its Windows form for the import hint.
	// An error only means that the hint is not printed.
	WSLPath func(linuxPath string) (string, error)
	// IsTerminal reports whether Stdin or Stdout is a terminal, which decides
	// between the full-screen and the plain interface. Tests set it.
	IsTerminal func(stream any) bool
	// ReadFile reads host files the form consults, such as the timezone list.
	ReadFile func(name string) ([]byte, error)
}

// New returns an App wired to the real process.
func New() *App {
	return (&App{}).withDefaults()
}

// withDefaults fills every unset field from the real process and returns a.
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
	if a.RecipeDir == "" {
		if workingDir, err := os.Getwd(); err == nil {
			a.RecipeDir = workingDir
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
		a.WSLPath = windowsPathOf
	}
	if a.IsTerminal == nil {
		a.IsTerminal = isCharacterDevice
	}
	if a.ReadFile == nil {
		a.ReadFile = os.ReadFile
	}
	if a.Builder == nil {
		a.Builder = &builder.Builder{Bootstrapper: &builder.Mmdebstrap{}}
	}
	return a
}

const usageText = `usage: frostroot <command> [flags]

Commands:
  init       answer a few questions and write frostroot.toml
  edit       change an existing frostroot.toml with the same questions
  capture    describe this installed Ubuntu system as a recipe, with a report of the gaps
  validate   check frostroot.toml (no network, no root)
  build      build frostroot.lock and dist/<name>-ubuntu-<release>-amd64.tar.gz

init, edit, capture and build show a full-screen interface in a terminal and plain
lines otherwise; --plain asks for the lines. Run "frostroot <command> -h" for
a command's flags.
`

// Run executes the command named by args[0] and returns the process exit
// code: 0 success, 1 user error, 2 build error, 130 interrupted.
func (a *App) Run(args []string) int {
	a.withDefaults()
	if len(args) == 0 {
		a.stderrf("%s", usageText)
		return exitUserError
	}
	command, commandArgs := args[0], args[1:]
	switch command {
	case "init":
		return a.runInit(commandArgs)
	case "edit":
		return a.runEdit(commandArgs)
	case "capture":
		return a.runCapture(commandArgs)
	case "validate":
		return a.runValidate(commandArgs)
	case "build":
		return a.runBuild(commandArgs)
	case "help", "-h", "--help":
		a.stdoutf("%s", usageText)
		return exitSuccess
	default:
		a.stderrf("frostroot: unknown command %q\n\n%s", command, usageText)
		return exitUserError
	}
}

// stdoutf writes formatted text to standard output. A failed write to the
// terminal has nowhere better to be reported, so its error is deliberately
// discarded here, in one place, rather than at every call site.
func (a *App) stdoutf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stdout, format, args...)
}

// stderrf writes formatted text to standard error; see stdoutf about errors.
func (a *App) stderrf(format string, args ...any) {
	_, _ = fmt.Fprintf(a.Stderr, format, args...)
}

// newFlagSet returns a flag set for a subcommand that prints usageText,
// followed by the flag defaults, to standard error.
func (a *App) newFlagSet(command, usageText string) *flag.FlagSet {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(a.Stderr)
	flags.Usage = func() {
		a.stderrf("%s", usageText)
		flags.PrintDefaults()
	}
	return flags
}

// parseFlags parses a subcommand's flags. When the command should stop, for
// help or for bad flags, it returns stop == true and the exit code.
func (a *App) parseFlags(flags *flag.FlagSet, args []string) (exitCode int, stop bool) {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitSuccess, true
		}
		return exitUserError, true
	}
	if flags.NArg() > 0 {
		a.stderrf("frostroot %s: unexpected argument %q\n", flags.Name(), flags.Arg(0))
		flags.Usage()
		return exitUserError, true
	}
	return exitSuccess, false
}

// loadRecipe loads and validates the recipe, printing every problem. ok is
// false when the command should stop with exitUserError.
func (a *App) loadRecipe() (imageRecipe recipe.Recipe, ok bool) {
	recipePath := filepath.Join(a.RecipeDir, recipeFileName)
	imageRecipe, err := recipe.Load(recipePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.stderrf("frostroot: no %s in %s; create one with: frostroot init\n", recipeFileName, a.RecipeDir)
		} else {
			a.stderrf("frostroot: %v\n", err)
		}
		return recipe.Recipe{}, false
	}
	if problems := recipe.Validate(imageRecipe); len(problems) > 0 {
		for _, problem := range problems {
			a.stderrf("%s: %s\n", recipeFileName, problem)
		}
		return recipe.Recipe{}, false
	}
	return imageRecipe, true
}
