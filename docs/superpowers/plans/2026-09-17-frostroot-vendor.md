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

- [x] Mirror `github.com/klauspost/compress` and `github.com/ulikunitz/xz`
      into the offline proxy; add them to `go.mod`; verify every `go.sum`
      line against `sum.golang.org`. (compress v1.20.0 needs Go 1.25, so
      v1.19.2, the newest for Go 1.24; xz v0.5.16.)
- [x] `recipe.LockPackage` gains `SHA256`, `Size`, `Filename`
      (`sha256`, `size`, `filename`); `Lockfile.HasChecksums()`. Round-trip
      and old-lock tests.
- [x] `CONTRIBUTING.md`: the compression libraries join the Charm exception.

## Task 2: `internal/deb`

- [x] `stanza.go`: `Stanza`, `ReadStanzas(reader, visit)` streaming,
      `ParseStanza`; tests for continuation lines, a final paragraph without
      a blank line, CRLF, skipped lines, a visitor error.
- [x] `index.go`: `IndexEntry`, `ReadIndexEntries(reader, visit)`; fixture
      `testdata/Packages-excerpt` cut from the spike's real index (`curl`,
      `libc6`, `tzdata`).
- [x] `package.go`: `ReadControl(path) (Control, error)` over ar +
      `control.tar{,.gz,.xz,.zst}`; `debtest` builds `.deb` files with each
      compressor; error cases (magic, missing member, truncated,
      unsupported compression, control without Version, no control file).
- [x] `repository.go`: `WriteFlatRepository(dir, release, debFileNames)`
      writing `Packages` (control paragraph + `Filename`, `Size`, `MD5sum`,
      `SHA256`) and `Release`; determinism and hash tests; `SHA256File`.

## Task 3: `internal/pool`

- [x] `manifest.go`: `Entry`, `Manifest(lock)`, `TotalSize`;
      `ErrNoChecksums`, `ErrBadLock`; rejects unsafe `filename` values and
      duplicate file names.
- [x] `verify.go`: `Verify(dir, entries, onChecked) (Status, error)` with
      present, missing, corrupt and extra; `Status.Describe`; `Prune`.
- [x] `fetch.go`: `Fetch(ctx, FetchOptions)` with four workers, temporary
      files, size and hash checks, the Launchpad fallback on 404, one retry
      otherwise, progress callbacks; `httptest` tests for every case in the
      spec, including cancel and 25 files over 4 workers.
- [x] `stage.go`: `Stage(poolDir, entries, repositoryDir, release,
      onProgress)` linking (only a 0644 source) or copying, then
      `deb.WriteFlatRepository`.

## Task 4: `internal/builder`

- [x] Phases: `PhaseVerifyVendored`, `PhasePrepareRepository`,
      `PhaseCheckLock`, `PhaseVendorRead`, `PhaseVendorCheck`,
      `PhaseVendorDownload`, `PhaseVendorPrune`; `Phases()`,
      `OfflinePhases()`, `VendorPhases(prune)`; `UnitFiles` summaries with a
      total; the parser's phase table unchanged.
- [x] Online build: `copy-out /var/lib/apt/lists` hook into the stage
      (`StageOptions.CopyAptLists`, `Stage.AptListsDir`);
      `recordChecksums(listsDir, installed)` in `lockcheck.go` with the
      pocket preference and the two failure rules.
- [x] `BootstrapSpec.Trusted`; `commandLine` omits `--keyring` when set;
      `Preflight` and `Run` skip the keyring check when set.
- [x] `Options.Offline`: `planOffline` (`ErrNoLock`, `ErrLockMismatch`,
      `pool.ErrNoChecksums`), `verifyPool` before any work directory
      (`ErrPoolIncomplete`), `stageRepository`, every lock package in
      `Include`, the `sources.list` hook (`StageOptions.SourceLines`),
      `compareWithLock` afterwards (`ErrImageDiffersFromLock`), the lock
      untouched, `Result.Offline`.
- [x] `Version = "0.4.0"`.
- [x] Tests with the fake bootstrapper for every bullet above, including
      the file name from the newest pocket.

## Task 5: `internal/tui` and `internal/cli`

- [x] `tui.Screen{Title, Subtitle, Phases, LogTitle}`, `tui.BuildScreen(...)`,
      `RunProgress` with `done <-chan error` and `Outcome`; `build.go`
      renamed `progress.go`; a vendor phase-list test.
- [x] `cli/vendor.go`: flags, lock loading, `vendorRun` behind the screen or
      plain lines, summary, exit codes, the Launchpad fallback for Ubuntu
      locks (`App.VendorFallback` for tests); tests with an `httptest`
      mirror.
- [x] `cli/build.go`: `--offline` (rejects `--mirror`), offline summary,
      the new refusals mapped to exit 1, a vendoring hint after an online
      build.
- [x] `cli/version.go`: `version`, `--version`, `-v`; `App.BuildInfo`;
      build-info tests.
- [x] Usage text, README (status, install from releases, commands, lock
      example, "Rebuilding offline", pipeline, notes, scope, layout,
      verification, documentation table).

## Task 6: releases

- [x] `.github/workflows/release.yml` as specified; the tag-equals-version
      check; static build; `SHA256SUMS`; `gh release create`.
- [x] Build the release command by hand on the build host and check the
      binary is static and prints its version.

## Task 7: verification on the build host

- [x] Online `build` of a small recipe: every lock entry has a checksum.
- [x] `vendor` for real; rerun to see it skip everything; corrupt one file
      and add a stale one, rerun with `--prune`.
- [x] `build --offline` in a network namespace with no interfaces; compare
      the package set with the lock.
- [x] `TestIntegrationOfflineRebuild` (online build, real vendor, offline
      rebuild, image checks) beside the existing integration test.
- [x] The vendor screen under a pty.
- [x] Record results in the spec.
- [ ] Pull request; ask before merging.
