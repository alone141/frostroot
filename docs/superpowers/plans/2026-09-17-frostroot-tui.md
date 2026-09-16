# frostroot v0.2 Implementation Plan: terminal user interface

Implements [`specs/2026-09-17-frostroot-tui.md`](../specs/2026-09-17-frostroot-tui.md).
Branch `feature/tui`, from `master` at 7d041e4. Every task ends with `gofmt`,
`go vet`, `go test -race ./...` and `golangci-lint run` clean, and its own
commit as `alone141` following `CONTRIBUTING.md`.

**Goal:** `init` and the new `edit` are one arrow-key form; `build` is a
progress screen with bars; without a terminal everything behaves as v0.1.0.

**Order of work.** The pieces that need no terminal come first, so most of the
new code is tested the ordinary way before any Charm code exists. The
full-screen programs come last and are thin.

## Task 1: dependencies

- [x] Mirror the Charm module graph into the offline file proxy (metadata for
      145 module versions; zips for the 42 the build selects), verify every
      `go.sum` line against `sum.golang.org`.
- [ ] `go.mod`: `bubbletea v1.3.10`, `bubbles v1.0.0`, `huh v1.0.0`,
      `lipgloss v1.1.0`; `go 1.24.2` as the graph requires. No `toolchain`
      line.
- [ ] `CONTRIBUTING.md`: note the exception to "standard library first" and
      that `internal/tui` is the only package allowed to import Charm.

## Task 2: `internal/builder` progress events and the verbose parser

- [ ] `progress.go`: `Phase` (Update, Download, Extract, InstallEssential,
      InstallRequested, Provision, Tarball, WriteLock, PlaceTarball),
      `ProgressEvent{Phase, Kind, Done, Total, Unit, Line}`, `Progress`
      interface, `discardProgress`.
- [ ] `parseProgress(reader io.Reader, progress Progress)`: line scanner with
      the recognizer table from the spec. `Get:` sizes parsed from `[3264 kB]`
      / `[51.0 kB]` / `[1,234 B]` into bytes; `Need to get 28.1 MB` likewise;
      `N newly installed` into a count. Every line is also emitted as a log
      event. Never returns an error for content; only for the reader.
- [ ] `testdata/mmdebstrap-verbose.log`: an excerpt of the recorded stream
      (index round, one download round, a few dpkg lines per install phase,
      hooks, tarball), about 120 lines. Table tests for transitions, totals,
      counts, missing totals, truncated stream, unknown `I:` message.
- [ ] `Mmdebstrap.Run`: add `--verbose`; tee output to the parser, to
      `<work>/mmdebstrap.log` and to the error tail. `ProgressOutput` is
      replaced by `Progress` on `Options`/`BootstrapSpec` (the plain interface
      renders lines from events). Tests: the flag is present, the log file is
      complete.
- [ ] `Builder.Build` reports WriteLock and PlaceTarball; `export.Place` gets
      a `func(copiedBytes, totalBytes int64)` callback used on the copy path.
- [ ] `Version = "0.2.0"`.

## Task 3: `internal/form`

- [ ] `field.go`: `Kind` (Input, Select, MultiSelect, Confirm), `Option{Value,
      Label, Description}`, `Field{Key, Page, Title, Description, Kind,
      Options func() []Option, Default, Validate func(string) error, Get
      func(recipe.Recipe) Values-part, Set ...}`, `Values map[Key]any` with
      typed accessors, `Fields() []Field`, `Pages()`.
- [ ] `recipe.go`: `FromRecipe`, `(Values).ToRecipe`, `Defaults(host Host)`
      where `Host` supplies the timezone file readers. Round-trip tests on
      every fixture in `testdata/`.
- [ ] `catalog.go`: `Entry{Category, Name, Description}`, `Catalog()`,
      `Categories()`, `Split(include []string) (catalogNames, otherNames)`.
      Tests: unique names, all match the package rule, presets of v0.1 are
      subsets.
- [ ] `timezones.go`: `Timezones(readFile)` parses `zone1970.tab`, `UTC`
      first, sorted; embedded fallback list generated from the same file
      (`go:embed zone1970.tab`, public domain). `HostTimezone(readFile)`.
- [ ] `locales.go`: curated list with labels.
- [ ] Validators reuse `recipe`'s rules: export `recipe.CheckImageName`,
      `CheckUserName`, `CheckPackageName`, `CheckTimezone`, `CheckLocale`
      (today's regexes, one function each); `Validate` calls them.

## Task 4: plain interface on the new fields

- [ ] `cli`: `Interface` selection (`--plain`, terminals, `TERM=dumb`), a
      `terminalChecker` field for tests.
- [ ] `cli/plainform.go`: asks `form.Fields()` through `Prompt`: numbered
      options for selects, `y/n` for confirms, numbers-or-names for
      multi-selects. `init` and `edit` share `runForm`.
- [ ] `cli/edit.go`: load, validate, `FromRecipe`, form, `ToRecipe`,
      `writeRecipe`. Missing file → exit 1 with the `init` hint.
- [ ] `cli/plainprogress.go`: `Progress` that prints phase lines and every
      10% of a measured phase to stderr; replaces the raw stream.
- [ ] Rewrite `init_test.go` for the new questions; add `edit_test.go`;
      `build_test.go` checks the phase lines.
- [ ] README: commands table gains `edit` and `--plain`; the quick start shows
      the form (as a description, not a transcript).

## Task 5: `internal/tui` form

- [ ] `form.go`: `RunForm(ctx, fields, values, in io.Reader, out io.Writer)`:
      one `huh.Group` per page; `Field.Kind` → `huh.NewInput` / `NewSelect` /
      `NewMultiSelect` / `NewConfirm`; validators attached; filtering on for
      long selects; a summary group that shows the rendered recipe.
- [ ] `theme.go`: one lipgloss theme, colors that read on dark and light
      terminals.
- [ ] Tests: the built form has one field per table row with the right kind
      and default; a scripted key sequence through a fake terminal
      (`tea.WithInput`/`WithOutput`) completes `init` with the defaults.
- [ ] `cli` wires the full-screen path for `init` and `edit`.

## Task 6: `internal/tui` build screen

- [ ] `build.go`: `buildModel` with the phase list, spinner, per-phase
      `progress.Model`, a `viewport` for the log pane, elapsed timer, resize
      handling, `l` to grow the pane, Ctrl-C → cancels the context and shows
      the waiting notice. `RunBuild(ctx, plan, events <-chan ProgressEvent,
      in, out)`.
- [ ] `cli.runBuild`: full-screen path runs `Builder.Build` in a goroutine
      with a channel-backed `Progress`, the screen in the foreground; after it
      returns, prints the v0.1 summary in plain text.
- [ ] Tests: model transitions from events, percentage arithmetic, view
      contains titles, percentage and last log line; narrow width does not
      panic.

## Task 7: verification

- [ ] Integration test additions: phases arrive in order with a positive
      download total; the whole catalog installs on 24.04.
- [ ] Manual, in Windows Terminal: `init`, `edit`, `build` at 80×24 and
      narrow; resize mid-build; Ctrl-C mid-download; `--plain` for each;
      `frostroot build > log` uses plain automatically. Record results in the
      spec's revision history.
- [ ] `docs/superpowers/specs/2026-09-14-frostroot-design.md`: mark the TUI
      extension point as done by v0.2 and point here.
- [ ] Pull request; ask before merging.
