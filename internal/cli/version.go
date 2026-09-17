package cli

import (
	"runtime/debug"
	"strings"
	"time"

	"frostroot/internal/builder"
)

// shortCommitLength is how many characters of the commit hash version shows.
const shortCommitLength = 7

func (a *App) runVersion(args []string) int {
	flags := a.newFlagSet("version", "usage: frostroot version\n\nPrint the version and, when known, the commit it was built from.\n")
	if exitCode, stop := a.parseFlags(flags, args); stop {
		return exitCode
	}
	info, known := a.BuildInfo()
	a.stdoutf("%s\n", versionLine(info, known))
	return exitSuccess
}

// versionLine renders "frostroot 0.4.0", followed by the commit and its date
// when the binary was built from a git checkout with version control
// information (Go's default; -buildvcs=false omits it). A checkout with
// uncommitted changes is marked +dirty.
func versionLine(info *debug.BuildInfo, known bool) string {
	line := "frostroot " + builder.Version
	if !known || info == nil {
		return line
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	revision := settings["vcs.revision"]
	if revision == "" {
		return line
	}
	if len(revision) > shortCommitLength {
		revision = revision[:shortCommitLength]
	}
	if settings["vcs.modified"] == "true" {
		revision += "+dirty"
	}
	details := []string{"commit " + revision}
	if committed, err := time.Parse(time.RFC3339, settings["vcs.time"]); err == nil {
		details = append(details, committed.UTC().Format("2006-01-02"))
	}
	return line + " (" + strings.Join(details, ", ") + ")"
}
