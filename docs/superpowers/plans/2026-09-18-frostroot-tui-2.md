# frostroot TUI Improvement Plan: v0.10 and v0.11

What the full-screen interface should do next, in two releases, from a
walk-through of what it does today. Branch `feature/tui-2`, from `master`
at a278abf. Every task ends with `gofmt`, `go vet` (both tags), `go test
-race ./...`, `GOOS=windows go build ./...` and `golangci-lint run` clean,
and its own commit as `alone141` following `CONTRIBUTING.md`.

**The constraint that does not move**, from issue #10 and the v0.2 spec: the
form writes `frostroot.toml` and nothing else; `build` never prompts; the
recipe stays the only source of truth, so anything the form can produce is
also hand-writable and reviewable in git. Every task below respects it.

**Two releases, because they are two kinds of work.** v0.10 is a set of
small, independent improvements to what already exists, each an afternoon,
none needing a spike. v0.11 is one feature — the package picker issue #10
asked for — and it needs a spike, a cache and a new widget.

## What the interface does today, and where it falls short

Established by reading `internal/tui` and `internal/form`, the v0.2 spec,
and walking `frostroot init` on the build host with scripted answers.

**The form.** Five pages — Image, User, System, Packages, Sources — twelve
fields, then a summary page that asks whether to write. Selects and
multiselects are arrow-key lists; the timezone list filters as you type;
every input validates on the spot with `recipe`'s own rules. This all works
and stays. What does not:

1. **The summary is a précis, not the recipe.** The v0.2 spec says the last
   page "shows the rendered recipe first". It shows five lines (`Image lab,
   Ubuntu 24.04 amd64` …). For `init` that is a summary of what you typed
   ten seconds ago; for `edit` it does not say what will *change*, and it
   does not mention that the file's own comments are about to be replaced
   by the template's, which the README warns about and the spec says the
   form does.
2. **A package typo costs five minutes.** "Other packages" is checked
   against the package-name *pattern* only, so `ninja-buld` is accepted and
   the build fails inside mmdebstrap with "unable to locate package",
   minutes in. The Python field is the same. frostroot parses Ubuntu's
   `Packages` indexes already (`internal/deb.ReadIndexEntries`, used for
   the lock); nothing asks them at form time.
3. **The catalog is 32 packages in six categories.** Good defaults, and the
   only browsable thing; everything else is typed blind. This is issue #10.
4. **The form cannot say everything a recipe can.** `[certificates]` has no
   field — v0.8 carries it through `edit` invisibly, so a user cannot *add*
   one in the form. A hand-written `[[sources]]` entry is likewise carried
   but not editable. `index_url` will join this list when v0.9 lands.
5. **`capture` decides before it tells you.** The findings — what capture
   saw and what a recipe cannot carry — go to `frostroot-capture.md` and to
   a list printed *after* the form closes. The user answers the packages
   page without knowing what was not captured.

**The progress screen.** A fixed phase list with spinners, byte bars for the
downloads, package counts for the installs, a scrollable log pane (`l`
grows it), elapsed time, and the promise that Ctrl-C waits for mmdebstrap.
Solid; `vendor` reuses it. What does not work as well:

6. **A failure prints the tail, which may not hold the reason.** After the
   screen tears down, a failed build prints the error with the last 4 KiB
   of mmdebstrap's output — the bootstrapper keeps that tail and puts it in
   its error — and `work directory kept at … (mmdebstrap output in
   mmdebstrap.log)`. Forty lines is enough when apt's `E:` is at the end.
   It is not when the explanation is followed by more than that: pip's
   traceback after its `ERROR:`, dpkg's cleanup after the failing package,
   the chroot teardown after the provision script's `frostroot:` message.
   Then the reason scrolled out of the tail too, and the user opens a
   2000-line log to find it. (An earlier draft of this item said no tail
   was printed at all; the code corrected it.)
7. **Layout assumes eighty columns and UTF-8.** The phase title column is a
   fixed 40 cells; below about 70 columns rows wrap and the pane border
   breaks. The glyphs `✓ ✗ · ╭─` and the bar's block characters are
   mojibake on a `LANG=C` terminal, which WSL sessions started from some
   Windows tools have.
8. **The views are tested by substring.** `progress_test.go` asserts that
   `View()` contains `"failed"` and so on. Nothing pins the layout, so 7
   cannot be fixed with confidence, and a Charm upgrade can move things
   without a test noticing.

## v0.10: see what you are about to do, and why it failed

Six tasks. Task 0 is the enabler for every other; after it they are
independent and can land in any order.

### Task 0: golden frames

A test helper that renders a model's `View()` at a fixed size with a fixed
clock and spinner frame, and compares it with a file under
`internal/tui/testdata/frames/<name>-<cols>x<rows>.txt`. Regenerating is
`FROSTROOT_UPDATE_FRAMES=1 go test ./internal/tui`, which the test says in
its failure message. Frames for the form's first page and summary, and for
the progress screen pending, running with a bar, done, failed and
interrupting, at 80×24 and 120×40. The existing substring tests stay; the
frames are what lets Tasks 4 and 5 change layout safely.

### Task 1: a failure shows the line that explains it, first

After a failed `build`, in both interfaces: the error, then the first line
of `mmdebstrap.log` that carries an error prefix — apt's and mmdebstrap's
`E:`, dpkg's `dpkg: error`, pip's `ERROR:`, the provision and Python
scripts' `frostroot:` — with its line number, then the 4 KiB tail the
bootstrapper already keeps, then the kept-directory line. The whole log is
searched, not the tail, because the tail is exactly what the explanation
has scrolled out of when this matters. The bootstrapper's error becomes a
type carrying the tail, so the command line can put the cause above it;
`Error()` renders both unchanged for everyone else. `--plain` and the
screen share the code, since the screen tears down before the summary.
`vendor` keeps no log file and its error already names the file and the
reason, so it is unchanged; an interrupted build is unchanged too. Tests:
the `E:` 300 lines above the tail, a log with no error prefix, a missing
log, and the first of several.

### Task 2: the last page is the recipe, or the diff

`init` and `capture`: the summary page shows the rendered `frostroot.toml`
— the same template `writeRecipe` uses, so what is shown is byte for byte
what is written — in a scrollable pane, then the write question. `edit`:
a unified diff of the current file against what will be written, in the
same pane, with a one-line count above it ("3 lines change; the file's own
comments are replaced by the template's" when the current file has any line
that is a comment the template does not produce). No difference at all
says so, and the question becomes "Nothing changes. Write anyway?" with
the default No — and declining then is not an error, since nothing was
asked for that did not happen. The five-line précis stays as the pane's
title block. The plain interface prints the same text before its `y/n`.
The template stays in `internal/cli`: the command line hands the form a
function that renders the preview from the answers, so the form never
imports the CLI and the CLI never imports a widget, and the line diff is
its own small package, `internal/textdiff`. Long lines in the pane wrap
into fragments below about 70 columns, as the log pane's do; Task 5 fixes
both.

### Task 3: the form can say everything the recipe can

A **Trust** page after Sources with one field, `Certificate files`: free
text, paths beside the recipe separated by spaces or commas, each checked
on the spot with `recipe.CheckCertificatePath`, and — because the form runs
in the recipe directory — with `recipe.CheckCertificateFiles`, so "no
certificate in this file" and "this is a private key" are said before the
summary, not by `build`. The certificate carry-through v0.8 added becomes
this field's initial value. Adding a row to `form.Fields()` is the whole
change, as the v0.2 spec promised; the test is the `edit` round trip. When
v0.9's `index_url` lands it gets a field on the Packages page the same
way, and the spec of v0.9 already says so.

Hand-written `[[sources]]` entries stay carried through, not editable: a
repeating sub-form is not a row in a table, and it is a v0.12 question.

### Task 4: `capture` tells you first

A `KindNote` field kind: a titled block of text with no answer, rendered as
a huh note in the form and as indented lines in the plain interface. The
capture form opens with one on a **Captured** page: the machine read, the
release, the counts, and then `Snapshot.Summary()` — every area a recipe
cannot carry, with its count — and the sentence that the details are in
`frostroot-capture.md`. The user then reaches the packages page knowing
what is missing. The report file and the printed list after the form stay
as they are.

### Task 5: narrow and plain terminals

- The phase title column is `min(40, width/2)` cells, the subtitle and the
  footer truncate with an ellipsis, and the log pane keeps its border down
  to 60 columns. Below 60 the screen still draws without wrapping; nothing
  is promised about legibility there. Golden frames at 60×20 join Task 0's
  set.
- On a terminal whose locale is not UTF-8 (`LC_ALL`/`LC_CTYPE`/`LANG`
  without `UTF-8`/`utf8`), the glyphs become `[ok]`, `[!!]`, `-`, the bar
  uses `#` and `.`, and the pane border is ASCII. One function decides, used
  by both screens; a frame at 80×24 pins the ASCII rendering.
- The byte phases show a rate and a remaining estimate (`28.1 MB · 3.2 MB/s
  · 0:12 left`), from a ten-second window of the progress events, hidden
  until the window is full. Counts and spinners are unchanged.

### v0.10 verification

`go test ./...` offline. Then, for real, in Windows Terminal running the
WSL distribution and in VS Code's integrated terminal, light and dark: the
form at 80 and 120 columns and at 60; `edit` on a recipe with hand-written
comments, checking the diff pane says they go; a build made to fail by a
misspelled package, checking the `E:` line is on screen after the teardown
without opening the log; a build under `LANG=C`; and Ctrl-C mid-build,
which must still wait. The README's "Verification" section records it.

## v0.11: the package picker (issue #10)

One feature: on the Packages page, a **Find a package** field that searches
the release's whole archive by name and description as you type, adds with
Space, and knows whether a typed name exists.

### The design

- The index is the archive's own `Packages` files for `main` and
  `universe`, `binary-amd64`, from the release pocket and `-updates`, the
  same files the build later installs from. Fetched on first use over plain
  HTTP from the archive (or `--mirror`), decompressed with what
  `internal/deb` already links, reduced to name, version, section and the
  short description, and cached under
  `$XDG_CACHE_HOME/frostroot/index/<suite>/` with the fetch time. Refreshed
  when older than seven days or with `init --refresh-index`. The recipe
  directory is never written to; the constraint holds.
- The index is advisory. It is not signature-checked (frostroot's `pgp`
  package reads keys and verifies nothing, by design); it can only suggest
  names, and `build` verifies every package it installs against the signed
  archive exactly as today. This is said in the README.
- **Offline is not an error.** No cache and no network: the field says
  "archive index not available; the catalog and the free-text field still
  work" and the form goes on. `--plain` never fetches; it validates typed
  names against the cache when there is one, and says nothing when there
  is not.
- "Other packages" and the picker share the index: a typed name that is
  not in it is refused on the spot with the nearest names ("no `ninja-buld`
  in noble; did you mean `ninja-build`?"), which is issue 2 above solved.
- The catalog stays as the curated shelf at the top of the page; the picker
  is the shelf behind it.

### Task 0: spike on the build host (blocker)

- How big and how fast: fetch noble's four index files, record compressed
  and uncompressed sizes, `ReadIndexEntries` time, and the memory of the
  reduced table. The guess is ~70,000 entries, under 15 MB compressed, a
  second or two to parse, tens of megabytes resident; the spike replaces
  the guess.
- Which compressions the archive offers (`.gz` and `.xz` both, on Ubuntu,
  is the belief) and whether `-updates` is worth the second fetch.
- Whether a huh `MultiSelect` with `Filterable(true)` stays responsive with
  70,000 options, or the picker has to be its own Bubble Tea component with
  its own filtering. This decides the size of Task 3.
- What the short description looks like across `main` and `universe`
  (length, the odd package with none), so the list renders evenly.

### Task 1: the spec

`docs/superpowers/specs/2026-09-18-frostroot-picker.md`, with the spike's
numbers.

### Task 2: the index

`internal/index`: fetch, decompress, reduce, cache, load, search. Search is
substring over name first and description second, name matches ranked
above, exact name at the top. `Nearest(name)` for the typo message. Tests
with a small real excerpt, an unreachable archive, a stale cache, and a
corrupt cache file that is deleted and refetched rather than trusted.

### Task 3: the widget

`KindSearch` in `form`, rendered by whichever the spike chose: a filtered
huh multiselect fed from the index, or a bespoke component with an input,
a result list, Space to add, and the chosen names shown above the list.
The plain interface renders the field as the existing free-text one, index
validation included.

### Task 4: the validation

"Other packages" and "Python packages" gain index-aware validation when a
cache is present; the message names the release and the nearest names.
Python names are not in an apt index and stay pattern-only; the message says
"checked at build time".

### Task 5: docs and verification

README: the Packages page description, the cache location, the refresh
flag, the advisory note, and the offline behaviour. On the build host: a
first `init` fetching the index with a visible progress line, a second one
using the cache, one with the network cut, and one with a misspelled
package refused before the summary. Frames for the picker at 80×24 join the
golden set.

## Deliberately not in either

- **Comment-preserving `edit`.** `go-toml/v2` does not keep comments, and
  the design writes from a template on purpose so that every recipe reads
  the same. Task 2 makes the loss visible instead of silent; that is the
  honest fix.
- **A sub-form for hand-written `[[sources]]`.** A repeating group is a new
  kind of page, not a row in the field table. It is the natural v0.12 if
  people ask for it.
- Mouse support, user-chosen themes, a settings file, a native Windows
  binary: the v0.2 non-goals, still.
- Searching PyPI from the form: it needs the network at form time for
  something `build` resolves properly anyway, and the lock is where the
  answer belongs.
