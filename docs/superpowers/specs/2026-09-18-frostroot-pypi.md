# frostroot v0.11 Spec: finding Python packages

The Packages page can search Ubuntu's whole archive since v0.10. "Python
packages", the field under it, is still checked against a pattern only, so
`reqeusts` is accepted, written, and refused minutes into `build` by pip —
the five-minute hole v0.10 closed for apt, still open for PyPI. Written
before the code, from the spike below and the decisions taken with the
owner.

## The problem, and why it is not the same problem

The apt picker shows `ninja-build  1.11.1-2  small build system closest in
spirit to Make` because Ubuntu publishes every package's version, section
and one-line description in one file. PyPI publishes **names and nothing
else**.

## The spike (2026-09-18, the build host)

| | Ubuntu (noble) | PyPI |
|---|---|---|
| The complete index | `Packages.xz`, 21 MB, four files | `pypi.org/simple/` (PEP 691 JSON), **9.7 MB gzipped on the wire, 43.9 MB decompressed, 1.2 s** |
| Entries | 85,574 | **894,088** |
| Fields per entry | name, version, section, description | **`name` and `_last-serial`, nothing else** |
| Reduced and cached | 1.7 MB gzip TSV | **names alone 12.8 MB, 4.2 MB gzipped** |
| Parse | 2.85 s (all four, xz) | **1.26 s** |
| Name length | median 19, p99 54 | **median 11, p99 41, max 188** |
| Substring scan (Python, for scale) | — | **~17 ms** for `requests`, `http`, `num` |

**Descriptions in bulk are impossible.** `pypi.org/pypi/<name>/json` is one
request per project: 894,088 of them is about **75 hours**.

**One description on demand is cheap.** Measured against the two endpoints:

| | `/pypi/<name>/json` | `/pypi/<name>/<version>/json` |
|---|---|---|
| requests | 43 kB, 158 ms | **4 kB**, 180 ms |
| numpy | 646 kB, 162 ms | **26 kB**, 183 ms |
| django | 138 kB, 107 ms | **7 kB**, 198 ms |

The per-release endpoint carries the same `info.summary` for a fraction of
the bytes, because it lists one release's files instead of every release's.
Both need the project's current version, which the simple index does not
carry — so the full endpoint is what a first lookup must use, and its cost
is the 43–646 kB above. That is fine for the row someone has stopped on,
and only for that row.

## What was decided with the owner

**Picker, with the summary fetched on demand.** Names come from the cached
index, instantly and offline; the highlighted row's one-line summary is
fetched when the cursor rests on it and shown under the list. The
alternatives were a picker with bare names (cheap, but it would look like
the apt picker and tell you much less) and no picker at all, just the
warning (smallest, but not the search that was asked for).

**v0.11, on its own branch, after v0.10 was merged.**

## The design

Everything that can be reused, is. `KindSearch`, `form.PackageIndex`, the
picker component, `UnknownPackages` and the summary-page warning were all
built for v0.10 and are not specific to apt. What is new is a second index
and one new behaviour in the widget.

### The index — `internal/index`

```go
// OpenPyPI returns the names PyPI publishes, from the cache when it is
// fresh and from the archive otherwise, exactly as Open does for apt.
func OpenPyPI(ctx context.Context, options Options) (*PyPIIndex, error)
```

`Options` is shared with the apt index; `Release` is ignored and `Mirror`
names an alternative simple index. The draft had `--mirror` filling it, which
the code corrected: `--mirror` is an apt mirror and says nothing about PyPI,
so the flag is `--python-index` and `Mirror` is cleared before the PyPI
options are built. A devpi or Artifactory mirror is what `index_url`, issue
#23, will want too.

- **Fetch.** `https://pypi.org/simple/` with
  `Accept: application/vnd.pypi.simple.v1+json`, which is PEP 691. There is
  no `Release` file and no digest to check against: PyPI is HTTPS, and the
  transport is the integrity. This is the **first index frostroot fetches
  over HTTPS**, so `--ca-bundle` stops being decoration on `init` — a
  network that inspects TLS breaks this fetch where it could not break the
  plain-HTTP Ubuntu one.
- **Reduce.** The display name as PyPI spells it, and
  `recipe.NormalizePythonName` of it for matching, because PEP 503 makes
  `Flask_SQLAlchemy`, `flask-sqlalchemy` and `Flask.SQLAlchemy` one project.
  Sorted by the normalized name.
- **Cache.** `pypi.tsv.gz` beside the apt caches, same header discipline:
  format version, index URL, fetch time; a header for another index means
  fetch again, a file that does not parse is deleted rather than trusted.
  4.2 MB, a week old at most.
- **Search.** Normalized substring. Rank: exact, then prefix, then contains,
  shorter names first inside a rank. There is no description to rank by and
  no section to filter on.
- **`Nearest`** as for apt, over normalized names.

### The summary — `internal/index`

```go
// Summaries fetches one-line summaries, one project at a time, and
// remembers them. Nothing is fetched until something asks.
type Summaries struct{ ... }

func (s *Summaries) Cached(name string) (string, bool) // never blocks
func (s *Summaries) Fetch(ctx context.Context, name string)
```

`Cached` is what the widget draws from, so `View` never waits on the
network. `Fetch` is what a Bubble Tea command runs, and it returns nothing:
everything it learns goes into the cache the widget reads, so there is one
place a summary lives. A name that has no summary, or whose fetch failed, is
remembered as empty so it is not asked for twice; failures are never an
error the user sees, because a missing summary is a missing nicety. A
*canceled* lookup is left unknown, because that is not an answer.

Summaries come from the host the index belongs to, never from pypi.org when
the index is somewhere else: pointing frostroot at a private index must not
mean telling PyPI which names were looked up. An index with no JSON API
answers nothing, which is already the degradation for a failed lookup.

### The widget — `internal/tui/picker.go`

The component from v0.10, with two differences, both driven by what the
index can answer rather than by which field it is:

- **No version or section column** when the index has none, `/` says there
  are no sections, the help line stops offering it, and the status line
  stops saying "all sections". The row is the name alone. The last two were
  found against the real PyPI rather than by a test: the draft only thought
  of the column and the `/` key.
- **A summary line under the list.** When the cursor rests on a row for
  250 ms, the summary is fetched; while it is in flight the line reads
  `looking up requests…`, and then it is the summary. Moving on cancels
  nothing — the request is cheap and its answer is cached for the next
  time the cursor passes.

The debounce matters: holding Down through twenty rows must not be twenty
requests. The tick machinery is the one v0.10 built for the index load,
including the counter that exists because huh hands its focused field every
message twice; a rest that has been superseded, or whose row the cursor has
since left, is dropped rather than fetched.

A field **declares** that its index has summaries rather than the widget
discovering it when the index loads, because the line they go on has to be
there from the first frame: finding out later would resize the list under
someone mid-search, and v0.10 promised the field does not jump.

Offline, or with the fetch failing, the line is simply absent and the picker
is the bare-name list the cheaper option would have been. That is the
degradation this design is chosen for.

### The warning

`form.UnknownPackages` takes the "Other packages" answer today. It gains the
Python answer, against the PyPI index, and the summary page gains a second
block when there is anything to say:

```
Not on PyPI:
  reqeusts  nearest: requests
```

Names are compared normalized, so `Flask_SQLAlchemy` is not reported
missing. No index, no warning. The write question still defaults to Write:
a private index, a package published between the cache and now, or a name
that `index_url` will resolve are all reasons a name may be right and
unknown here.

## Not in v0.11

- **Versions in the picker.** The simple index does not carry them, and the
  recipe does not hold them: versions belong in the lock, which is the
  design's oldest rule.
- **Searching summaries.** They arrive one at a time; there is nothing to
  search.
- **`index_url`** (#23) beyond `--python-index` pointing the search
  elsewhere. That flag says where the *form* looks; the recipe still says
  nothing, and `build` still resolves against PyPI. The parked plan owns
  the recipe field and authenticated mirrors.

## Verification

Unit: the index against a recorded excerpt of the real simple index
(exact, prefix, contains, normalization, `Nearest`, a stale cache, a corrupt
cache, a header for another index, an unreachable index with and without a
cache); `Summaries` for a hit, a 404, a malformed body and the
never-ask-twice rule. The widget with the driver: a name-only index draws no
version column, the summary appears after the debounce and not before,
holding Down makes one request and not twenty, and a failed lookup leaves
the line empty. Golden frames at 80×24, 120×40, 60×20 and ASCII.

On the build host, against the real PyPI, done (2026-09-18): the index
arrived as 894,105 projects, seventeen more than the spike counted an hour
before. `requests` found 713 and showed two hundred, saying so; resting on
the first row put `Python HTTP for Humans.` under the list and moving down
replaced it. `reqeusts` was added as typed, marked `? not in the index`,
warned about as `nearest: requests, reqwests` — both real projects — and
written anyway; `requestsH` and `Flask_SQLAlchemy` were not warned about,
the second because PEP 503 makes it `Flask-SQLAlchemy`. `/` answered "this
index has no sections" and the help line offered no `/`.

Two faults that only a live index shows were found there rather than by the
tests: the status line claimed "all sections" and the help line offered
`/ section` for an index with none, and the summary lookup went to pypi.org
whatever `--python-index` said. Both are fixed, and the second is now a
test.

A real `build` of a recipe naming `requests` and `Flask_SQLAlchemy` — the
second spelled the way PEP 503 allows rather than the way PyPI spells it —
finished in 425 s and locked 17 `[[pypi]]` entries, among them
`Flask-SQLAlchemy 3.1.1` and `requests`: the normalization the picker and the
warning rely on is the same one pip applies, all the way to the lock.
