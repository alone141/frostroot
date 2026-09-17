# frostroot v0.5 Implementation Plan: extra apt sources

Implements [`specs/2026-09-17-frostroot-sources.md`](../specs/2026-09-17-frostroot-sources.md).
Branch `feature/sources`, from `master` at 851faaa. Every task ends with
`gofmt`, `go vet` (both tags), `go test -race ./...`, `GOOS=windows go build
./...` and `golangci-lint run` clean, and its own commit as `alone141`
following `CONTRIBUTING.md`.

**Goal:** `[[sources]]` in the recipe, picked from a catalog or as PPAs;
`build` installs from them with their keys and locks their packages;
`vendor` and `--offline` follow; `capture` carries a machine's sources.

**Order of work.** Leaves first (keys, recipe), then the catalog and the
form, then the builder and the pool, then the CLI and capture, then the
real run on the build host against a local signed repository.

## Task 1: keys and the recipe

- [ ] `internal/pgp`: `ParsePublicKey(data) (Key, error)` with `Binary`
      and `Fingerprint`; armor decoding; version 4 fingerprint; tests with
      Docker's armored key and GitHub's binary two-key file as fixtures,
      plus an HTML page, an empty file and a bad armor checksum.
- [ ] `recipe.Source`, `Recipe.Sources`; `Validate` checks name, URL,
      suite, components, key path and uniqueness; `CheckSourceKeys(dir,
      sources)`; `Source.SuiteFor(release)`, `Source.ComponentsOrDefault()`.
      Round-trip tests and a fixture recipe with sources.

## Task 2: catalog, PPAs and key fetching

- [ ] `internal/sources`: `Entry` catalog with the eight sources and their
      fingerprints; `Resolve(entry, release) recipe.Source`; `PPA(owner,
      name)`; `IsPPA(url)`, `PPAFilesURL`; `CheckPPAName`.
- [ ] `sources.Fetcher` with `Client` interface: `FetchKey(ctx, source)`
      that downloads, parses, compares fingerprints (catalog pin or
      Launchpad API) and returns the key bytes; tests with `httptest`.

## Task 3: the form

- [ ] Fields `sources` (multi-select over the catalog) and `ppas` (input)
      on page `Sources`; `Defaults`, `FromRecipe`, `ToRecipe` (custom
      sources kept in order), `Summary`.
- [ ] Plain interface needs nothing new; a full-screen test drives the new
      page.

## Task 4: builder and pool

- [ ] `builder.SourceLines` with `signed-by` paths; `StageOptions.Sources`
      writes binary keys to `<stage>/keys/`; `CustomizeHooks` uploads the
      keys and always uploads `sources.list`; `RenderSourcesList`.
- [ ] Lock: `LockRepository`, `Lockfile.Repositories`,
      `LockPackage.Source`; `recordChecksums` attributes indexes by apt's
      file naming; unattributed index is an error.
- [ ] Offline: compare recipe sources with lock repositories; keys
      uploaded from the recipe.
- [ ] `pool.Manifest` base URL per repository; `pool.FallbackURL(lock,
      entry)` for the archive and PPAs.

## Task 5: CLI

- [ ] Recipe template renders `[[sources]]` with comments; `loadRecipe`
      runs `CheckSourceKeys`; `runRecipeForm` fetches missing keys after
      writing the recipe; `App.KeyClient` for tests.
- [ ] `build` refuses a recipe whose keys are missing before any work.
- [ ] Tests: plain init with a catalog source and a PPA against a fake
      client; mismatch; validate on a missing key.

## Task 6: capture

- [ ] Parse one-line and deb822 source files; classify; produce
      `recipe.Source` entries and key contents; report what was not
      carried; adjust the third-party packages finding.
- [ ] `cli/capture.go` writes the keys beside the recipe.
- [ ] Fixtures: a PPA with `signed-by`, deb822 Docker, inline key,
      `trusted.gpg.d`, flat repository.

## Task 7: documentation and verification

- [ ] README: recipe table, the Sources page, a "Third-party sources"
      section, capture's rule, the TLS note, layout.
- [ ] Build host: real `build` with a local signed repository as a
      source, lock fields, `vendor` from two origins, `build --offline`,
      `capture` of a fixture root. Record results in the spec.
- [ ] Pull request; ask before merging.
