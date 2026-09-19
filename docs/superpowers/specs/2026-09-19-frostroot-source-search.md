# frostroot v0.12 Spec: searching the sources a recipe adds

Since v0.10 the Packages page searches Ubuntu's whole archive, and since
v0.11 PyPI. It does not search the repositories the recipe itself adds, so
the three names that send people to a third-party source in the first place
— `docker-ce`, `code`, `gh` — are exactly the names the picker cannot find.
The README said so outright: "which the index knows nothing about."

Written alongside the code, from the measurements below.

## The problem

Two things were in the way, and only one of them was code.

**The order of the questions.** Sources were asked on the page *after* the
packages. An index opened when the picker is reached can only cover
repositories the answers already name, so with that order there was nothing
to search even if the machinery existed.

**The index knew one repository.** `index.Options` carried a
`distro.Release`, and everything below it — the pockets to read, the cache
file's name, what `Describe` says — assumed the release's own archive.

## What was decided

- **The Sources page moves before the Packages page.** The alternative, a
  picker that offers to add the repository a chosen name comes from, covers
  the catalog but not an arbitrary PPA, and is a bigger change; it stays
  open as a follow-up.
- **A source's index is advisory, exactly as the archive's is.** Files are
  checked against the sizes and digests in the repository's own `Release`,
  which is read unverified. `build` still verifies every package against the
  key the recipe names. The picker cannot make a build install anything.
- **A source that cannot be read must not break the picker.** It is dropped,
  named in the line above the results, and the rest is searched.

## The design

`index.Options` keeps the transport and cache knobs; a new unexported
`target` says *what* to index — label, origin, cache name, base URL, suite,
components and the pockets to read. `Open` builds the archive's target as
before; `OpenSource` builds one for a `Source{Name, URL, Suite, Components}`.

| | archive | source |
|---|---|---|
| pockets | `<suite>`, `<suite>-updates` | `<suite>` alone |
| cache file | `<suite>-amd64.tsv.gz`, unchanged | `source-<name>-<8 hex>-amd64.tsv.gz` |
| `--mirror` | replaces the archive URL | never applied |
| `Describe` | `noble · …` | `docker · …` |

The cache name carries a digest of URL, suite and components because a PPA
publishes under the release's own code name: keyed by suite alone, the
deadsnakes index and `noble` itself would overwrite each other, and two
recipes that both call a repository "docker" would collide.

`Union([]*Index, unreachable []string)` merges the parts into the one list
the picker searches, so ranking, sections and "did you mean" stay whole
rather than being interleaved per repository. Later parts win a shared name,
which puts Kitware's `cmake` in front of `noble`'s — apt resolves such a name
by version, so the row is what the repository offers, not a promise of what
will be installed. The union's age is its oldest part's.

`Entry.Origin` (and `form.Match.Origin`) carries the source's name. It is not
cached: which repository served a file is a property of the index that was
opened, not of the file, and stamping it on open keeps the cache format at
five fields.

The form's `IndexOpener` now takes an `IndexRequest{Release, Sources}` rather
than a release string, and `IndexRequest.Key()` decides when the picker
re-opens: the field already re-opened when the release changed, and adding a
source is the same kind of change. `internal/cli` caches the archive per
release and each source per repository, so adding a source does not fetch
the 21 MB archive again.

## Verification (2026-09-19)

Against the real repositories, not test servers:

| | packages | time |
|---|---|---|
| deadsnakes PPA | 164, `python3.13 3.13.15-1+noble1` | 0.8 s |
| Docker | 11, `docker-ce 5:29.8.1-1~ubuntu.24.04~noble` | 0.5 s |
| Kitware | 9, `cmake 3.29.6-0kitware1ubuntu24.04.1` | 0.5 s |
| `noble` | 85,574 | 5.5 s |

Merged: `noble + kitware, docker (gone not reachable) · 85,589 packages`.
`cmake` resolves to Kitware's 3.29.6 over noble's 3.28.3, `git` stays the
archive's with no origin, and a source pointed at a host that does not exist
is named in the line rather than failing the open. Two requests per source
and no more — the `-updates` pocket is Ubuntu's scheme, and asking a vendor
for one costs every open a 404.

## Not in v0.12

- **Offering the repository a name comes from.** Typing `docker-ce` with no
  source chosen still finds nothing; the catalog's repositories are not
  indexed until the recipe names them.
- **A flat or unsigned repository.** Out of scope for `build`, and so for
  the index: such a source contributes nothing and is reported unreachable.
- **`capture` filling `[python]`**, which remains the last hole in capture.
