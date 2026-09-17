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

- [x] `internal/pgp`: `ParsePublicKey(data) (Key, error)` with `Binary`,
      `Fingerprint` and `Armored`; armor decoding with the CRC-24 check;
      version 4 and 6 fingerprints; `Armor` for storage; tests with
      Docker's armored key and GitHub's binary two-key file as fixtures,
      plus an HTML page, an empty file, a bad armor checksum and a flipped
      byte.
- [x] `recipe.Source`, `Recipe.Sources`; `Validate` checks name, URL,
      suite, components, key path and uniqueness (`CheckSource`,
      `CheckSourceURL`, `CheckKeyPath`); `CheckSourceKeys(dir, sources)`;
      `KeyPath`; `Source.SuiteFor`, `Source.ComponentsOrDefault`. Round-trip
      tests and `testdata/sources.toml`.

## Task 2: catalog, PPAs and key fetching

- [x] `internal/sources`: the eight-entry catalog with pinned
      fingerprints; `Entry.Source(releaseSuite)`; `ParsePPA`, `PPA`,
      `PPAOf`, `PPAFilesURL`, `KeyPathFor`, `Describe`.
- [x] `FetchKey(ctx, client, source)` over a `Client` interface: catalog
      pin or Launchpad API, then the key, parsed and compared; `HTTPClient`
      for production; tests with a fake client for every case.

## Task 3: the form

- [x] Fields `sources` (multi-select over the catalog) and `ppas` (input)
      on page `Sources`; `Defaults`, `FromRecipe`, `ToRecipe`
      (`SplitSources`, `MergeSources`: custom sources kept in order),
      `Summary` with a Sources line.
- [x] The plain interface needed nothing new; the pty smoke test covers the
      page in the full-screen form.

## Task 4: builder and pool

- [x] `builder.SourceLines(release, mirror, sources, keyringDir)`;
      `StageOptions.Keys` writes binary keys to `<stage>/keys/`;
      `CustomizeHooks` creates `/etc/apt/keyrings`, uploads the keys and
      always uploads `sources.list`; mmdebstrap gets lines naming the
      staged keys, the image lines naming its own.
- [x] Lock: `LockRepository`, `Lockfile.Repositories`, `Repository()`,
      `LockPackage.Source`; `recordChecksums` attributes indexes through
      `indexOrigins`/`originOf` by apt's file naming; an unattributed index
      fails the build; `readSourceKeys` fails a build without its keys
      (`ErrSourceKey`).
- [x] Offline: `repositoryDifferences` compares recipe sources with lock
      repositories; keys uploaded from the recipe.
- [x] `pool.Entry.BaseURL`/`Source`; `Manifest` resolves each package's
      base URL; `--mirror` overrides the archive's only;
      `pool.FallbackURL(lock)` for the archive and PPAs.

## Task 5: CLI

- [x] The recipe template renders `[[sources]]` with a comment;
      `loadRecipe` runs `CheckSourceKeys` with a hint; `runRecipeForm`
      writes the recipe, then `fetchMissingKeys` (fetched, or provided by
      capture); `App.KeyClient`; `validate` counts the extra sources.
- [x] `build` refuses a recipe whose keys are missing or not keys before
      any work (exit 1).
- [x] Tests: plain init with a catalog source and a PPA against a fake
      client; an existing key left alone; a fingerprint mismatch; validate
      without and with keys.

## Task 6: capture

- [x] `capture/sources.go`: one-line and deb822 parsing (options,
      `Enabled`, inline keys); classification into carried and left with
      reasons; names from the catalog, PPAs or the host; the third-party
      packages finding ignores carried hosts.
- [x] `cli/capture.go` passes the keys to the form runner, which writes
      them beside the recipe.
- [x] Fixtures: deb822 Docker with a key file, an inline-key source with
      two suites, a PPA without `signed-by`, a flat repository, a `deb-src`
      line, unreadable and non-key files.

## Task 7: documentation and verification

- [x] README: status, the form's fifth page, the recipe table, a
      "Third-party sources" section, capture's rule, the TLS note, layout,
      documentation table.
- [x] Build host: real `build` with a local signed repository as a
      source, lock fields, `vendor` from two origins, `build --offline`,
      `capture` of a fixture root, the pty smoke test. Results in the spec.
- [ ] Pull request; ask before merging.
