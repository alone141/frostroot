# frostroot v0.4 Implementation Plan: vendoring, offline rebuilds, releases

Implements [`specs/2026-09-17-frostroot-vendor.md`](../specs/2026-09-17-frostroot-vendor.md).
Branch `feature/vendor`, from `master` at 5325294. Every task ends with
`gofmt`, `go vet` (both tags), `go test -race ./...`, `GOOS=windows go build
./...` and `golangci-lint run` clean, and its own commit as `alone141`
following `CONTRIBUTING.md`.

**Goal:** `build` records checksums; `vendor` fills `vendor/debs/`;
`build --offline` rebuilds the lock's exact package set from it; `version`
and a tag-driven release workflow.

**Order of work.** Formats first (`deb`), then the pool, then the builder
changes behind the existing fake bootstrapper, then the CLI and the screen,
then the real runs on the build host.

## Task 1: dependencies and the lock

- [ ] Mirror `github.com/klauspost/compress v1.20.0` and
      `github.com/ulikunitz/xz v0.5.16` into the offline proxy; add them to
      `go.mod`; verify every `go.sum` line against `sum.golang.org`.
- [ ] `recipe.LockPackage` gains `SHA256`, `Size`, `Filename`
      (`sha256`, `size`, `filename`); `Lockfile.HasChecksums()`. Round-trip
      and old-lock tests.
- [ ] `CONTRIBUTING.md`: the compression libraries join the Charm exception.

## Task 2: `internal/deb`

- [ ] `stanza.go`: `Stanza`, `ReadStanzas(reader, visit)` streaming; tests
      for continuation lines, a final paragraph without a blank line, long
      lines.
- [ ] `index.go`: `IndexEntry`, `ReadIndexEntries(reader, visit)`; fixture
      `testdata/Packages-excerpt` cut from the spike's real index (`curl`,
      `libc6`, `tzdata`).
- [ ] `package.go`: `ReadControl(path) (Control, error)` over ar +
      `control.tar.{gz,xz,zst}`; test helper that builds `.deb` files with
      each compressor; error cases (magic, missing member, truncated).
- [ ] `repository.go`: `WriteFlatRepository(dir, suite, debFileNames)`
      writing `Packages` (control paragraph + `Filename`, `Size`, `MD5sum`,
      `SHA256`) and `Release`; determinism and hash tests.

## Task 3: `internal/pool`

- [ ] `manifest.go`: `Entry`, `Manifest(lock)`; `ErrNoChecksums`; rejects
      unsafe `filename` values.
- [ ] `verify.go`: `Verify(dir, entries) (Status, error)` with present,
      missing, corrupt and extra; `Prune`.
- [ ] `fetch.go`: `Fetch(ctx, FetchOptions)` with four workers, temporary
      files, size and hash checks, the Launchpad fallback on 404, one retry
      otherwise, progress callbacks; `httptest` tests for every case in the
      spec.
- [ ] `stage.go`: `Stage(dir, entries, workDir, onProgress)` linking or
      copying, then `deb.WriteFlatRepository`.

## Task 4: `internal/builder`

- [ ] Phases: `PhaseVerifyVendored`, `PhasePrepareRepository`,
      `PhaseCheckLock`, `PhaseVendorRead`, `PhaseVendorCheck`,
      `PhaseVendorDownload`, `PhaseVendorPrune`; `Phases()`,
      `OfflinePhases()`, `VendorPhases()`; the parser's phase table
      unchanged.
- [ ] Online build: `copy-out /var/lib/apt/lists` hook into the stage;
      `checksumsFromAptLists(listsDir, installed)` with the pocket
      preference and the two failure rules; `Stage.AptListsDir`.
- [ ] `BootstrapSpec.Trusted`; `commandLine` omits `--keyring` when set;
      `Preflight` and `Run` skip the keyring check when set.
- [ ] `Options.Offline`: load and compare the lock (`ErrLockMismatch`,
      `ErrPoolIncomplete`), verify the pool before any work directory,
      stage the repository, every lock package in `Include`, the
      `sources.list` hook, compare the status with the lock afterwards, leave
      the lock untouched, `Result.Offline`.
- [ ] `Version = "0.4.0"`.
- [ ] Tests with the fake bootstrapper for every bullet above.

## Task 5: `internal/tui` and `internal/cli`

- [ ] `tui.Screen{Title, Subtitle, Phases, LogTitle}`, `RunProgress` with
      `done <-chan error`; `build.go` and its tests adapted; a vendor
      phase-list test.
- [ ] `cli/vendor.go`: flags, lock loading, the pool fetch behind the screen
      or plain lines, summary, exit codes; tests with an `httptest` mirror.
- [ ] `cli/build.go`: `--offline` (rejects `--mirror`), offline summary,
      the new refusals mapped to exit 1.
- [ ] `cli/version.go`: `version`, `--version`, `-v`; build-info test.
- [ ] Usage text, README (status, install from releases, commands, lock
      example, a section on offline rebuilds, layout, verification).

## Task 6: releases

- [ ] `.github/workflows/release.yml` as specified; the tag-equals-version
      check; static build; `SHA256SUMS`; `gh release create`.
- [ ] Build the release command by hand on the build host and check the
      binary is static and prints its version.

## Task 7: verification on the build host

- [ ] Online `build` of the capture recipe: every lock entry has a checksum.
- [ ] `vendor` for real; rerun to see it skip everything; corrupt one file
      and rerun.
- [ ] `build --offline` in a network namespace with no interfaces; compare
      the package set with the lock; import into WSL and log in.
- [ ] The pty smoke test extended to `vendor`.
- [ ] Record results in the spec; pull request; ask before merging.
