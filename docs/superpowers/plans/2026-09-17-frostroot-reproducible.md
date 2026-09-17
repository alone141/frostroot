# frostroot v0.6 Implementation Plan: byte-identical offline rebuilds

Implements [`specs/2026-09-17-frostroot-reproducible.md`](../specs/2026-09-17-frostroot-reproducible.md).
Branch `feature/reproducible`, from `master` at 404ef18. Every task ends with
`gofmt`, `go vet` (both tags), `go test -race ./...`, `GOOS=windows go build
./...` and `golangci-lint run` clean, and its own commit as `alone141`
following `CONTRIBUTING.md`.

**Goal:** the lock records the epoch and apt's auto marks; the offline
build uses both; two offline builds have one SHA-256.

## Task 1: lock and bootstrap

- [x] `recipe.Lockfile.SourceDateEpoch`, `recipe.LockPackage.Auto`;
      round-trip tests.
- [x] `BootstrapSpec.SourceDateEpoch`; `commandLine` adds
      `SOURCE_DATE_EPOCH=<n>` to the environment; test.

## Task 2: builder

- [ ] `Options.SourceDateEpoch`; `chooseEpoch(options, getenv, now,
      offline lock)`; `Result.SourceDateEpoch`, `Result.Reproducible`.
- [ ] `extendedstates.go`: `ParseExtendedStates`, `RenderExtendedStates`;
      the stage's `ExtendedStatesPath`; online hooks (ensure, download) and
      the `auto` marks in the lock; offline hook (upload the rendered file).
- [ ] Tests with the fake bootstrapper for every bullet; the fake records
      its environment and writes an `extended_states`.
- [ ] `Version = "0.6.0"`.

## Task 3: CLI and docs

- [ ] `build` summary lines: frozen-at online, byte-identical or not
      offline; the environment note offline; tests.
- [ ] README: the guarantee, the lock example, notes.

## Task 4: verification

- [ ] `TestIntegrationOfflineRebuild`: two offline builds, equal sums, no
      entry after the epoch, auto marks in the image.
- [ ] Build host: real binary, online build, vendor, two offline builds
      apart in time, `sha256sum`, and the online-vs-offline diff. Record in
      the spec.
- [ ] Pull request; ask before merging.
