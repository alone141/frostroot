# frostroot v0.2: terminal user interface

Date: 2026-09-17
Status: approved by the project owner in conversation; implementation on branch `feature/tui`
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md), whose product model (recipe, lock, tarball) is unchanged

## Why

v0.1.0 talks to the user like a 1990s installer: six questions, one line each,
answers typed by hand. The owner's verdict: "I don't like the console
interface." The requests behind this spec, in their words:

1. A "scalable modern TUI that's easy to choose different versions": options
   are picked with the arrow keys and Enter, and the user "should almost never
   type anything except username etc."
2. "It should be easy to add future options."
3. "Any downloading or loading phase should be indicated with a bar or loading
   animation."

Decisions taken with the owner on 2026-09-16:

| Question | Decision |
|---|---|
| Build screen | A checklist of phases with spinners, percentage bars where the work is measurable, the last lines of raw mmdebstrap output in a pane, and the full log saved to the work directory |
| Choosing packages | Categorized multi-select from a curated catalog, plus one optional free-text field for anything not listed |
| Scope | `init` form, `build` progress screen, and a new `frostroot edit` that opens an existing recipe in the same form with its values preselected |
| Without a terminal | Both commands keep the plain line output of v0.1.0, so pipes, CI and the test suite keep working |

## Goal

Every interactive moment of frostroot is a full-screen terminal interface:
`init` and `edit` are one form, `build` is one progress screen. Adding a
recipe option later means adding one row to a table, not writing widget code.
Nothing about recipes, locks, tarballs, validation or the build pipeline
changes.

## Non-goals

- Mouse support, themes chosen by the user, or a settings file for the UI.
- Searching the whole Ubuntu archive for packages. The catalog is curated and
  short; anything else goes in the free-text field, checked by `validate` as
  today.
- Editing a recipe while keeping the user's own comments. `edit` regenerates
  the file from the template; the template's comments come back, the user's
  do not. It says so before writing.
- Percentages for work that gives no measurable signal (extraction, tarball
  creation). Those phases get a spinner and an elapsed time, not a made-up
  number.
- A native Windows binary. Still Linux, still WSL for Windows users.

## Dependencies

The v1 spec kept to the standard library. A terminal interface is the one
place where that costs more than it buys: raw terminal handling, cell-width
arithmetic, resize events and key parsing are all solved problems. The
[Charm](https://charm.sh) libraries are the idiomatic Go answer and are used
as a set:

| Module | Role |
|---|---|
| `github.com/charmbracelet/bubbletea` | The Elm-style event loop: a model, `Update(msg)`, `View()` |
| `github.com/charmbracelet/huh` | Forms: `Select`, `MultiSelect`, `Input`, `Confirm`, grouped into pages, with per-field validation |
| `github.com/charmbracelet/bubbles` | Spinner, progress bar and viewport components for the build screen |
| `github.com/charmbracelet/lipgloss` | Layout and styling |

No other new dependency. `CONTRIBUTING.md` keeps "standard library first"; this
is the reasoned exception it asks for.

## Commands

```
frostroot init [--force] [--plain]      # form → frostroot.toml
frostroot edit [--plain]                # form preloaded from frostroot.toml → frostroot.toml
frostroot validate                      # unchanged
frostroot build [--mirror URL] [--keep-work] [--plain]
```

**Interface selection.** A command uses the full-screen interface when both
standard input and standard output are terminals and `--plain` is not given.
Otherwise it uses the plain interface: `init` and `edit` ask line by line as
v0.1.0 did, `build` streams progress as lines. `TERM=dumb` also selects plain.
The choice is made once, in `cli`, and passed down; no other package looks at
the terminal.

`--plain` exists so the line interface can be reached deliberately (screen
readers, a terminal the full-screen view misbehaves in, a log that should
show what was answered).

Exit codes are unchanged: 0, 1 (user error), 2 (build failed), 130
(interrupted). Leaving the form with Ctrl-C, or answering no on its summary
page, is a user error (1) and writes nothing.

## The form (`init` and `edit`)

One form, several pages. Enter accepts a field and moves on; Shift-Tab goes
back; Ctrl-C cancels. Esc is not a cancel key: in a filtering list it clears
the filter, and a stray Esc must never throw away five pages of answers.
Selects are chosen with the arrow keys; long selects filter as the user types
(the timezone list), so choosing `Europe/Istanbul` is typing `ist` and
pressing Enter. Every field validates on the spot with the same
rules as `recipe.Validate`, so the form cannot produce a recipe that
`validate` rejects.

### Fields

The form is generated from this table. Each row is a `Field` value in
`internal/form`; the type says which widget renders it, the recipe accessor
says which value it reads and writes. Adding an option is adding a row.

| Page | Field | Widget | Options / rule | Default (`init`) |
|---|---|---|---|---|
| Image | Name | Input | `recipe` image-name rule | `lab` |
| Image | Ubuntu release | Select | from `distro.SupportedVersions()`, each with code name and support status; 20.04 is marked "end of standard support" | `24.04` |
| User | User name | Input | `recipe` user-name rule | `student` |
| User | Passwordless sudo | Confirm | yes / no | yes |
| System | Timezone | Select, filterable | IANA zones from `/usr/share/zoneinfo/zone1970.tab` on the host, falling back to an embedded list of the same zones when the file is missing; `UTC` first | the host's `/etc/timezone` if it is in the list, else `UTC` |
| System | Locale | Select | curated list of UTF-8 locales (`C.UTF-8`, `en_US`, `en_GB`, `tr_TR`, `de_DE`, `fr_FR`, `es_ES`, `it_IT`, `pt_BR`, `ru_RU`, `ja_JP`, `zh_CN`, ...) | `en_US.UTF-8` |
| System | Boot with systemd | Confirm | yes / no | yes |
| Packages | Packages | MultiSelect, filterable | the catalog below, Space toggles, grouped by category | none |
| Packages | Other packages | Input | apt names separated by spaces or commas; each checked with the `recipe` package rule | empty |
| Summary | Write `frostroot.toml`? | Confirm | shows the rendered recipe first | yes |

`arch` is not asked: there is one. `wsl.default_user` is not asked: it is the
user name. Both are written.

### Package catalog

Presets are replaced by a catalog: a package is an entry with a category, an
apt name, and a one-line description shown next to it. The v0.1 presets map
onto it (`build-essential` preset = the four "C/C++" entries; `python-lab` =
the "Python" entries plus `git`).

| Category | Entries |
|---|---|
| C/C++ | `build-essential`, `cmake`, `gdb`, `pkg-config`, `clang`, `clang-format`, `valgrind`, `ninja-build` |
| Python | `python3`, `python3-pip`, `python3-venv`, `ipython3` |
| Version control | `git`, `git-lfs` |
| Editors | `vim`, `nano`, `emacs-nox` |
| Tools | `curl`, `wget`, `htop`, `tmux`, `tree`, `unzip`, `jq`, `rsync`, `openssh-client` |
| Languages | `default-jdk`, `nodejs`, `npm`, `golang-go`, `rustc`, `cargo` |

The catalog lives in one Go table (`internal/form/catalog.go`). Names must be
real Ubuntu package names present in all three supported releases; the
integration test installs the whole catalog once to prove it.

### `edit`

`edit` loads `frostroot.toml`, validates it, and opens the same form with
every field set from the recipe: the release select lands on the recipe's
release, the catalog entries the recipe includes are pre-checked, and packages
the recipe has that are not in the catalog appear in the "Other packages"
field. Saving rewrites the file through the template, atomically, exactly as
`init` does. If the file does not exist, `edit` says to run `init` and exits 1.
It never touches the lock or `dist/`.

### Plain interface

The plain interface asks the same fields in the same order, one line each,
through the existing `Prompt` interface. Selects list their options with a
number, and accept the number or the value. Confirms accept `y`/`n`.
MultiSelects accept numbers or names separated by spaces or commas. Defaults
show in brackets and Enter accepts them. This is what the tests script, and
what runs when there is no terminal.

## The build screen

```
frostroot build   cpp-lab · Ubuntu 22.04 (jammy, amd64) · http://archive.ubuntu.com/ubuntu

  ✓  Update package index                                      4s
  ✓  Download packages           ████████████████  28.1 MB   16s
  ⠸  Install essential packages  ██████████░░░░░░  62 / 95   41s
     Install requested packages
     Provision user, locale and timezone
     Create tarball
     Write frostroot.lock
     Place dist/cpp-lab-ubuntu-22.04-amd64.tar.gz

  ╭─ mmdebstrap ─────────────────────────────────────────────────────╮
  │ Setting up libpam-modules:amd64 (1.5.3-5ubuntu5.1) ...           │
  │ Setting up libpam-runtime (1.5.3-5ubuntu5.1) ...                 │
  │ Setting up passwd (1:4.13+dfsg1-4ubuntu3.2) ...                  │
  ╰──────────────────────────────────────────────────────────────────╯
  1:02 elapsed · log: /var/tmp/frostroot-1000/build-3821950674/mmdebstrap.log · Ctrl-C interrupts
```

Phases are a fixed list, known before the build starts, so the user sees how
much is left. A phase is pending, running (spinner, elapsed time, bar when
measurable), done (check, duration) or failed (cross). The pane shows the last
lines of mmdebstrap's output as they arrive and can be scrolled with the arrow
keys while the build runs; `l` toggles it between eight lines and the whole
free height. On Ctrl-C the screen stays up and shows "interrupting; waiting for
mmdebstrap to clean up" until mmdebstrap exits, as the process-group handling
from v0.1 requires.

When the program exits, whether success, failure or interrupt, the
full-screen view is torn down and the same summary v0.1.0 prints is printed in
plain text: the files written, the `wsl --import` lines, or the error and the
kept work directory. That text stays in the scrollback; the screen does not.

### Where the numbers come from

mmdebstrap draws its own progress bars only on a terminal, from apt's
`APT::Status-Fd` and dpkg's `--status-fd`; through a pipe it prints nothing
but its `I:` phase lines. frostroot therefore runs it with `--verbose`, which
replaces the bars with apt's and dpkg's ordinary output, and parses that. The
v0.1 verification build of a tiny 24.04 image was rerun this way to record
what the stream looks like (2096 lines, 133 s); the parser's tests use excerpts
of that recording.

| Phase | Starts at | Progress from | Ends at |
|---|---|---|---|
| Update package index | `I: running apt-get update...` | count of `Get:N` index lines, no total: spinner and "N files" | `I: downloading packages with apt...` |
| Download packages | `I: downloading packages with apt...` | `Need to get 28.1 MB of archives.` sets the total; each `Get:N ... [3264 kB]` adds its size. Bytes, so the bar moves at download speed | `I: extracting archives...` |
| Extract archives | `I: extracting archives...` | none: spinner | `I: installing essential packages...` |
| Install essential packages | `I: installing essential packages...` | `Unpacking X` and `Setting up X` lines; the total is twice the package count of the download phase | `I: installing remaining packages inside the chroot...` |
| Install requested packages | `I: installing remaining packages inside the chroot...` | a second apt round: `0 upgraded, 163 newly installed` sets the count, its `Need to get` and `Get:` lines drive a download sub-bar, then `Unpacking`/`Setting up` count against twice the package count | first `I: running special hook` |
| Provision | `I: running special hook: upload ...` | none: spinner; the provision script's output goes to the pane | `I: cleaning package lists and apt cache...` |
| Create tarball | `I: creating tarball...` | none: spinner | `I: done` |
| Write frostroot.lock | frostroot, after mmdebstrap exits | instant | |
| Place tarball | frostroot | bytes copied when the move is a copy across filesystems (`export.Place` reports them); instant for a rename | |

Everything else in the stream (dpkg's `Selecting`, `Preparing to unpack`,
`Processing triggers`, `W:` warnings, the provision script's `Generating
locales`) is a log line for the pane and the file; nothing is dropped.

The recognizers are string prefixes and one regular expression for the
`Get:` size. An mmdebstrap release that changes a message degrades gracefully:
a phase that is never seen stays pending until the next one starts, then is
marked done at that moment; a total that never arrives leaves the bar
indeterminate. The parser never fails a build.

### The log file

`build` always writes mmdebstrap's complete output to
`<work-dir>/mmdebstrap.log`, in the full-screen and plain interfaces alike.
On failure the work directory is kept, as before, and the summary names the
log. `--keep-work` keeps it after success.

## Architecture

```
internal/cli        chooses the interface, parses flags, prints summaries, exit codes
internal/form       the field table, the package catalog, recipe <-> field values,
                    the timezone and locale lists; no terminal code
internal/tui        Bubble Tea programs: the form (huh) and the build screen;
                    the only package that imports the Charm libraries
internal/builder    unchanged pipeline; gains a Progress interface and the
                    parser of mmdebstrap's verbose output
```

### `internal/form`

`Field` describes one question: key, page, title, description, widget kind,
options (static or provided by a function), default, validator, and two
functions that read the value from a `recipe.Recipe` and write it back. `Fields()`
returns the table. `FromRecipe(recipe) Values` and `(Values).ToRecipe()
recipe.Recipe` convert both ways; `edit` is `FromRecipe` then the form then
`ToRecipe`. `Catalog` is the package table with `Categories()` and
`Lookup(name)`. `Timezones(readFile)` and `Locales()` provide the long lists.
All of it is testable without a terminal, and the round trip recipe → values →
recipe is a test.

### `internal/tui`

`RunForm(ctx, fields, values, io) (Values, error)` builds a `huh.Form` from
the table, one `huh.Group` per page, and runs it. `RunBuild(ctx, plan, events,
io) error` runs the build screen: `plan` is the ordered phase list, `events` is
the channel of `builder.ProgressEvent`. Both take the reader and writer to use
so tests can drive them with a fake terminal; the models' `Update` functions
are tested with synthetic messages, and `View` output is checked for the
strings that matter, not compared pixel by pixel.

### `internal/builder`

`Mmdebstrap` gets `--verbose` unconditionally. `Options` gains
`Progress Progress`, an interface with one method, `Report(ProgressEvent)`. A
nil `Progress` discards events. `ProgressEvent` is a small struct: phase,
kind (started, progress, log line, finished, failed), `Done` and `Total`
(bytes or steps, with a unit), and the line text. `parseProgress(io.Reader,
Progress)` turns the verbose stream into events by the table above and is
what `Mmdebstrap.Run` pipes the command's output through, alongside the log
file and the error tail. The builder reports its own phases (lock, place) the
same way. `export.Place` gets an optional byte-count callback for the copy
case.

The plain interface's `Progress` prints one line per phase change and a line
every 10% of a measured phase; the full-screen interface's `Progress` sends
the event to the Bubble Tea program.

### `internal/cli`

`App` gains `Interface` (`auto`, `tui`, `plain`), set from the flag and the
terminal check. `runInit` and `runEdit` share `runForm(initial Values)`.
`runBuild` starts the build in a goroutine and, in the full-screen case, runs
the screen in the foreground; the signal handling from v0.1 is unchanged, and
the screen learns about the interrupt through the context.

## Compatibility

- Recipes, locks and tarballs are byte-for-byte what v0.1.0 produced for the
  same answers. The template is unchanged.
- `frostroot init --force` and `build --mirror`/`--keep-work` keep their
  meaning. Presets disappear from the interface; a script that piped answers
  into `init` must give the plain interface's new answers instead. v0.1.0
  never promised that input format.
- `builder.Version` becomes `0.2.0`; the lock records it.

## Testing

- `internal/form`: table tests for every field's validator; recipe → values →
  recipe round trip on the fixtures; catalog entries match the package-name
  rule and have no duplicates; timezone list parsing on a `zone1970.tab`
  excerpt and the embedded fallback.
- `internal/builder`: the progress parser against excerpts of the recorded
  verbose log: phase transitions, byte totals, package counts, a stream with
  no totals, a stream that ends mid-phase, an unknown phase message.
  `Mmdebstrap.Run` writes the log file and passes `--verbose`.
- `internal/tui`: the form builds one widget per field with the right kind,
  options and default; the build model marks phases from events, computes
  percentages, and its view contains each phase title, the percentage of a
  measured phase and the last log line.
- `internal/cli`: interface selection (terminal, `--plain`, `TERM=dumb`);
  `edit` preloads and rewrites; plain `init`/`edit`/`build` as today, scripted
  through `Prompt`; the full-screen path is exercised once per command with a
  fake terminal and scripted key presses.
- Integration (tag `integration`): a real build reports every phase in order
  and a download total greater than zero; the whole catalog installs on 24.04.
- Manual: `init`, `edit` and `build` in Windows Terminal at 80×24 and at a
  narrow width; resize during a build; Ctrl-C during a download.

## Success criteria

1. `frostroot init` in a terminal asks nothing by typing except image name,
   user name and the optional other-packages field; every other choice is
   made with arrow keys, Space and Enter.
2. Adding a new recipe option is one row in `form.Fields()` (plus the recipe
   field it maps to); no change in `tui`.
3. `frostroot edit` reopens a recipe with its values preselected and writes
   back an equivalent recipe when nothing is changed.
4. During `build`, the download phases show a percentage that moves with the
   bytes fetched, the install phases show a package count, every other phase
   shows a spinner, and no phase is ever silent for more than the spinner's
   frame time.
5. `mmdebstrap.log` exists in the work directory after every build, complete.
6. With standard output redirected, `init`, `edit` and `build` behave as
   v0.1.0 did, and `go test ./...` passes offline without a terminal.
7. Ctrl-C during a build shows the interruption on screen, waits for
   mmdebstrap, exits 130, and leaves the plain summary in the scrollback.
