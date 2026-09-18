# frostroot v0.10 Spec: the package picker

How `init`, `edit` and `capture` let someone find any package in the Ubuntu
release they chose, without leaving the form and without the form learning
to do anything but write `frostroot.toml`. Written before the code, from the
plan (`plans/2026-09-18-frostroot-tui-2.md`) and the spike it asked for, and
updated as the code teaches. Issue #10.

## The problem

The Packages page offers a catalog of 32 names and a free-text field for the
rest. The free-text field checks that a name is *shaped* like a package name
and nothing more, so `ninja-buld` is accepted, written, and refused two
minutes into `build` by apt. And someone who does not know that the Ninja
package is called `ninja-build` has to leave the terminal to find out.

## What was decided with the user

1. **Fetch and cache.** The index is downloaded on first use and kept in the
   user's cache directory; nothing is bundled into the binary.
2. **Search, with a section filter.** One search box over names and short
   descriptions, narrowed by archive section (`devel`, `python`, `libs`, …)
   when wanted. No browsing tree.
3. **Warn with suggestions; never refuse.** A name the index does not have
   may come from a third-party source (`docker-ce`, `code`, `gh`), so the
   form says what it sees and the nearest names, and lets it through.
4. **All four components**, and — once told that a build enabled only
   `main` and `universe`, which the first question did not say — **in
   builds too**. See "Decision A".
5. **One field**: the picker replaces "Other packages" in the full-screen
   form rather than sitting beside it. See "Decision B".

## The spike (2026-09-18, the build host, noble, amd64)

| | |
|---|---|
| `Packages.xz`, release pocket | main 1.40 MB · universe 15.04 MB · restricted 0.09 MB · multiverse 0.27 MB |
| `Packages.xz`, `-updates` | main 1.28 MB · universe 1.69 MB · restricted 1.58 MB · multiverse 0.05 MB |
| Other releases, universe `.xz` | jammy 14.1 MB · focal 8.6 MB |
| Compressions the archive lists | none, `.gz`, `.xz` — all three, every component |
| Download, all twelve noble files | 24 MB, under 9 s from here |
| Decompress + parse, universe | `.xz` (ulikunitz, pure Go) 1.68 s · `.gz` 0.36 s (klauspost), 0.41 s (stdlib) |
| Decompress + parse, everything | 131.9 MB raw in 2.85 s |
| Names | release pocket 72,499 (main 6,099 · universe 64,754 · restricted 492 · multiverse 1,154); `-updates` adds 13,075; `-security` adds 281 more |
| What `-updates` adds | 10,087 of the 13,075 are section `kernel`, most of the rest are versioned `linux-*` headers and tools; the remainder is real: `dotnet-sdk-10.0`, `libllvm19`, `libllvm20` |
| Reduced table in memory | 85,855 entries (name, version, component, section, short description): 20.8 MB heap |
| The same as a cache file | gzip'd TSV, 1.8 MB; written in 84 ms, loaded in 35 ms |
| Substring search, all entries | 2–4 ms a query, name and description, no index structure |
| Sections | 58; the big ones: kernel 10,623 · devel 9,990 · libs 7,935 · libdevel 6,892 · python 4,850 · doc 4,710 · perl 4,307 |
| Short descriptions | every package has one; median 49 characters, p90 66, p99 88, longest 348 |
| Names | median 19 characters, p90 40, p99 54, longest 75 |
| **huh `MultiSelect`, filterable** | 1,000 options: 35 ms a key. 10,000: 0.37 s a key. 85,855: 7.7 s to build, **2.9 s a key**, 5 s to type `ninja` |

What the numbers decide:

- **huh's list cannot hold the archive.** It rebuilds every option on every
  key. The picker is its own component with its own search; at 3 ms a query
  it needs no debouncing and no search index.
- **`.xz`, with `.gz` as the fallback.** 4 MB less to download is worth more
  on a slow link than 1.3 s of decoding is on any machine. Both decoders are
  already linked.
- **The release pocket and `-updates`; not `-security`.** `-updates` is where
  a newer SDK appears after release. Everything in `-security` is copied to
  `-updates`; the 281 names it had first are a publication-timing artefact,
  and they are kernels.
- **The kernel noise stays in the table** and is kept out of the way by
  ranking and the section filter. `linux-tools-generic` is there too, and
  people do install it for `perf`.
- **A cache file, not a database.** 35 ms to load is below what anyone sees.

## The design

### The index — `internal/index`

```go
// Open returns the index for a release: from the cache when it is fresh,
// from the archive otherwise, and from a stale cache when the archive cannot
// be reached. Progress reports the download.
func Open(ctx context.Context, options Options) (*Index, error)

type Options struct {
    Release   distro.Release      // suite, archive URL, components
    Mirror    string              // replaces Release.ArchiveURL when set
    CacheDir  string              // $XDG_CACHE_HOME/frostroot/index, or ~/.cache/…
    MaxAge    time.Duration       // 7 days; 0 with --refresh-index
    Offline   bool                // the plain interface: the cache or nothing
    Client    *http.Client        // carries --ca-bundle's pool for an https mirror
    Progress  func(doneBytes, totalBytes int64)
    Now       func() time.Time
}

func (*Index) Search(query, section string, limit int) (matches []Entry, total int)
func (*Index) Has(name string) bool
func (*Index) Nearest(name string, limit int) []string
func (*Index) Sections() []SectionCount   // for the current release, largest first
func (*Index) Describe() string           // "noble · 85,855 packages · fetched 2 days ago"
```

**Fetch.** `dists/<pocket>/Release` first, for each of `<suite>` and
`<suite>-updates`: it lists every index with its size and SHA-256, which
gives the progress bar its total before the first byte and lets a truncated
or half-published file be recognised. Then
`dists/<pocket>/<component>/binary-amd64/Packages.xz` for each component,
checked against that SHA-256; on a mismatch the pocket is fetched once more
(a mirror mid-publication), then given up on. `.gz` when `Release` lists no
`.xz`. A missing `-updates` (a frozen mirror) is not an error.

The hash check is for **integrity, not authenticity**: `Release` is read
unsigned, and frostroot's `pgp` package verifies nothing, by design. The
index is advisory — it can only suggest names, and `build` verifies every
package it installs against the signed archive exactly as today. A hostile
mirror can make the picker lie about what exists; it cannot make a build
install anything Ubuntu did not sign. The README says this.

**Reduce.** Streamed through `deb.ReadStanzas`, keeping name, version,
component, section (the `universe/` prefix cut) and the first line of
`Description`. The later pocket's version replaces the earlier one's.
Sorted by name.

**Cache.** One file a release, `<suite>-amd64.tsv.gz`, whose first line is a
header: format version, archive URL, components, fetch time. Written to a
temporary file and renamed. A header that does not match what was asked for
(another mirror, other components, another format) means fetch again. A file
that does not parse is deleted and fetched again, never trusted. The recipe
directory is never written to.

**Search.** The query is lowercased and matched as a substring. Rank: the
exact name; names that start with it; names that contain it; descriptions
that contain it. Inside a rank, shorter names first, then alphabetical — so
`cmake` comes before `cmake-data` comes before `extra-cmake-modules`. The
section filter applies before ranking. `limit` bounds what is returned (the
widget asks for 200); `total` is what matched, so the list can say "and
4,263 more — keep typing".

**Nearest.** Edit distance with a bound of 2 (3 for names over twelve
characters), skipping names whose length differs by more than the bound,
the closest three. Not measured by the spike; the length skip leaves a few
thousand short comparisons, and it runs only when a name is not found. Task
2 records the real figure.

### The field — `form.KindSearch`

"Other packages" **becomes** the picker. One field, one Values key
(`other_packages`, the space-separated string it has always been), two
renderings:

- **Plain** (`--plain`, a pipe, `TERM=dumb`): the free-text question it is
  today. It never fetches. With a cache present, names the index lacks get a
  note after the answer; without one, nothing is said.
- **Full screen**: the component below. See "Decision B" for why this
  replaces the free-text field rather than sitting beside it.

`form` gains no network code and `tui` does not import `index`: the field
carries an interface, and `cli` plugs `internal/index` into it.

```go
// PackageIndex is what a Search field looks names up in.
type PackageIndex interface {
    Search(query, section string, limit int) ([]Match, int)
    Has(name string) bool
    Nearest(name string, limit int) []string
    Sections() []SectionCount
    Describe() string
}

// Host gains:
//   OpenIndex func(ctx, releaseVersion string, progress func(done, total int64)) (PackageIndex, error)
// nil means no index at all: tests, and a build of frostroot without one.
```

The release is an answer on the first page, so the index is opened when the
field is first focused, for the release chosen by then, and again if the
user goes back and changes it.

### The component — `internal/tui/picker.go`

A `huh.Field`, so it lives in the Packages group between the catalog and
"Python packages", moves on with the form's own Tab and Enter, takes the
form's theme and glyph set, and zooms to the form's height while focused.
A sketch, not a frame; the rows are illustrative:

```
 Other packages
 > ninja▏                                   all sections · noble · 85,855 packages
 [x] ninja-build      1.11.1-2          small build system closest in spirit to Make
 [ ] ninja-build-doc  1.11.1-2          documentation for ninja-build
 [ ] python3-ninja    1.11.1.1-1        Python bindings for ninja
 [ ] gn               0.0~git2023…      meta-build system that generates build files for Ninja
   + add "ninja" as typed (not in noble's archive)
 chosen: ninja-build valgrind-dbg
 type to search · ↑/↓ move · space add/remove · / section · enter continue
```

- **Typing searches.** Every printable character but Space and `/` goes to
  the query. Package names hold neither.
- **Space adds or removes the highlighted row**, as it does in the catalog
  above it. Chosen names that are catalog entries are shown as chosen and
  are toggled in the catalog's answer, not duplicated.
- **An empty query lists what is chosen**, so picks can be reviewed and
  dropped; with none, a line says what to do. Nothing in the archive is
  highlighted until something is typed, so a stray Space adds nothing.
- **A name the index lacks can still be added**: the last row offers the
  query as typed, when it is a valid package name. Chosen names the index
  does not have are marked `?` in the chosen line.
- **`/` opens the section list** in place of the results: "all sections"
  first, then the sections that have matches for the query, with counts.
  Enter picks, Esc leaves it as it was.
- **A paste of several names** (spaces, commas, newlines) adds each, the way
  the free-text field took them.
- **Loading.** On first focus: "fetching noble's package index · 12.3 /
  19.4 MB" with the build screen's bar. Tab and Enter work throughout; the
  fetch is cancelled when the form ends.
- **No index** (no cache and no network, or `OpenIndex` nil): the field says
  "archive index not available; names are added as typed and checked by
  build", and is a list editor: type a name, Space adds it. Offline is not
  an error.
- ASCII glyphs and the 60-column layout follow v0.9's rules: the version
  column goes first, then the description is cut.

### The warning

Names that are neither in the index nor in the catalog are listed on the
summary page, above the preview, in both interfaces:

```
Not in Ubuntu's noble archive:
  ninja-buld   nearest: ninja-build
  docker-ce
A third-party source on the Sources page may provide them; otherwise build
will stop at "Unable to locate package".
```

With no sources and no PPAs chosen the second sentence is the first. The
write question is unchanged and still defaults to Write: a warning, never a
refusal. No index, no warning. "Python packages" are PyPI names, which no
apt index knows; they stay pattern-checked and the summary says nothing
about them.

### The commands

`init`, `edit` and `capture` gain:

- `--refresh-index` — fetch even when the cache is fresh.
- `--mirror URL` — where to fetch the index from, as `build` has it.
- `--ca-bundle FILE` — trust for an HTTPS mirror behind an inspecting
  proxy, as `build` and `vendor` have it. The default archive is plain HTTP
  and needs none; `http_proxy` is honoured as everywhere else.

## Decision A: all four components, in builds too

The user chose all four, and then — told that `distro` enables `main` and
`universe` only, so that a picker offering `nvidia-cuda-toolkit`
(multiverse) would offer a build failure — chose to make builds enable all
four in this release, as a stock Ubuntu install and the official WSL image
do.

An earlier draft of this section said that needed a lock schema addition to
keep old locks rebuilding byte-identically. The code corrected it: the lock
already records the image's `deb` lines (`Lockfile.Sources`), an offline
rebuild writes `/etc/apt/sources.list` from them and installs from the
vendored pool, and nothing in `planOffline` recomputes the archive lines. So:

- `distro` lists `main restricted universe multiverse` for every release.
  That one table feeds the build's apt sources, the image's
  `sources.list`, and the picker's index, so the three cannot drift.
- An existing lock rebuilds offline to the same bytes, with the two
  components it was made with. An online build re-resolves, as it always
  has, and its new lock carries the four.
- No recipe knob. Nobody has asked for fewer components, and a knob is a
  field `edit` has to carry, the way `[certificates]` taught.
- What could change for an existing recipe built online: a `Recommends`
  that only `multiverse` or `restricted` can satisfy now gets installed.
  The verification looks for it in the catalog's closure.

## Decision B: one field

The plan had "Find a package" beside "Other packages". This spec merges
them, and the user agreed. With two fields the page holds two views of one
list that must be kept in step, the warning logic exists twice, and the page
is 12 + 12 + 3 + 3 rows on a 24-row terminal. With one, the picker is the
list editor and the index is optional help. The cost: in the full-screen
form the bespoke component is the only way to type a package name, so it
has to be right — which is what the golden frames and the driver tests are
for. Plain keeps the free-text field either way.

## Not in v0.10

- Searching third-party sources' indexes. They are HTTPS, keyed, and chosen
  on a later page; the warning's wording covers them.
- Long descriptions, dependencies, sizes. `apt show` is one command away in
  the built image; the picker answers "what is it called".
- Signature-checking the index. See "Fetch".
- arm64. `distro.SupportedArch` is amd64; the cache file name carries the
  architecture so that the day it is not costs nothing.

## Verification

Unit: the index against a small real excerpt of noble's `Packages` (exact,
prefix, substring, description, section, `Nearest`), a stale cache, a
corrupt cache, a header for another mirror, an unreachable archive with and
without a cache, a hash mismatch that clears on the second fetch. The
component with the driver: search, add, remove, the as-typed row, the
section list, paste, loading, no index, Tab away mid-fetch. Golden frames at
80×24, 120×40, 60×20 and ASCII.

On the build host: a first `init` that fetches with the progress line on
screen; a second that starts from the cache with no network traffic; one
with the network cut and no cache; one with `ninja-buld` typed, seeing the
warning and the recipe still written; `--plain` with and without a cache;
`--mirror` pointed at a local copy of the archive.
