# frostroot v0.7 Implementation Plan: Python packages in the recipe

Implements the spec Task 1 writes (`specs/2026-09-17-frostroot-python.md`),
from [issue #7](https://github.com/alone141/frostroot/issues/7) and the design
spec's [extension points](../specs/2026-09-14-frostroot-design.md#extension-points-later-required).
Branch `feature/python`, from `master` at 79d427b. Every task ends with
`gofmt`, `go vet` (both tags), `go test -race ./...`, `GOOS=windows go build
./...` and `golangci-lint run` clean, and its own commit as `alone141`
following `CONTRIBUTING.md`.

**Goal:** a recipe can name Python packages; `build` installs them into one
virtual environment inside the image and records every wheel in the lock with
its version, checksum and URL; `vendor` fetches those wheels; `build
--offline` reinstalls exactly them.

**Scope: Python only.** `[node]` and `[rust]` wait for v0.8 and later. What
this task builds — one recipe table, one lock array, one install hook, one
vendor pool, one comparison with the lock — is the skeleton they reuse, and
building it once for three ecosystems at the same time is how this feature
stalls. Also out of scope: source distributions, per-user environments,
extras and environment markers beyond what the resolver reports, and any
change to the apt path.

**Order of work.** Settle the unknowns on a real machine (Task 0), write them
down (Task 1), then the pure Go that needs no mmdebstrap (Tasks 2–3), then the
build behind the fake bootstrapper (Tasks 4–6), then the interface and the
docs (Task 7), then real images (Task 8).

**What the spike changed.** Two things, both in the risk table below. The
release's pip cannot report on 22.04 or 20.04, and the fallback taken was the
first one listed: frostroot installs one pinned pip, by checksum, into the
environment before anything else, so `[python]` works on all three releases
rather than on 24.04 alone. And `.pyc` caches differed between two rebuilds on
20.04 whatever the environment variable said, so the step recompiles the
environment with hash-based invalidation instead of trusting each release's
pip to do it the same way twice. Nothing else moved.

## Task 0: spike on the build host (blocker)

As for v1, nothing is coded until this passes; Tasks 2 and 3 are the only ones
that may start early, because they touch no mmdebstrap. Work by hand in a
24.04 and a 22.04 chroot with a realistic lab list (`numpy`, `pandas`,
`jupyterlab`, `requests`). Every question gets a written answer.

- [x] **Resolution.** Does the release's own pip produce an installation
      report with `pip install --report` (focal ships roughly pip 20.0.2,
      jammy 22.0.2, noble 24.0; `--report` arrived in pip 22.2)? Check what
      `python3 -m venv` puts in a fresh environment on each release, which is
      what actually matters.
- [x] **Hashes and URLs.** Confirm the report names, for every resolved
      package, the wheel's URL, its `sha256` and its size — the same three
      things `[[packages]]` records for a `.deb`. If it does not, vendoring
      cannot work from it and the route changes.
- [x] **Wheels only.** Does `--only-binary=:all:` resolve the test list
      without a compiler on each release? Record every package that fails.
- [x] **Where the environment lives.** Create `/opt/frostroot/venv` with
      `python3 -m venv`: confirm `python3-venv` is needed, that one
      `/etc/profile.d` line puts it on PATH for a login shell, a non-login
      shell and `sudo -i`, and that the user imports a package without
      activating anything.
- [x] **Inside or outside.** Run pip inside the chroot from a
      `--customize-hook` and confirm it reaches PyPI over HTTPS with the
      image's certificates; compare against resolving on the host with
      `--platform` / `--python-version` / `--only-binary=:all:`. Record which
      one the spec picks and why.
- [x] **Offline install.** `pip install --no-index --find-links <dir>
      --require-hashes -r requirements.txt` from the downloaded wheels, in a
      chroot with no network. Confirm a wrong hash and a missing wheel both
      fail loudly.
- [x] **Byte identity.** Install the same wheel set twice with
      `SOURCE_DATE_EPOCH` set and compare file by file: `.pyc` caches,
      `RECORD`, `INSTALLER`, `direct_url.json`. This decides whether v0.6's
      promise extends to Python or gets scoped in the README.
- [x] **Size.** Image growth and wheel pool size for the test list, for the
      README's numbers.
- [x] Record all of it; it becomes the spec's "Spike results" section.

**Stop rules.** If wheels-only resolution fails for an ordinary list, the
answer is a clearer error, not a source build. If byte identity fails and no
environment variable fixes it, scope the promise in the spec and move on —
same versions and checksums is still worth shipping. If the release's pip
cannot report, take a fallback from the risk table below rather than widening
the task.

## Task 1: the spec

- [x] `docs/superpowers/specs/2026-09-17-frostroot-python.md`, in the shape of
      the sources and reproducible specs: Why, Goal, Non-goals, the recipe
      table, the lock array, where the environment lives, what `build`,
      `vendor`, `build --offline` and `capture` each do, errors, testing, and
      the spike results.
- [x] Decisions the spec states outright: names in the recipe and versions
      only in the lock; one environment per image at a fixed path; wheels
      only; `[[pypi]]` never mixes with `[[packages]]`; the Python step runs
      after apt provisioning; what a recipe with `[python]` but no `python3`
      does.
- [x] The design spec's extension points and revision history: mark the
      Python half of "Language lockfiles" done in v0.7, as the TUI entry does.

## Task 2: recipe and lock (pure Go, no mmdebstrap)

- [x] `recipe.Python` with `Include []string`, as `[python]` on `Recipe`. An
      absent table and an empty list mean the same thing: no Python step.
- [x] `Validate`: distribution names only (PEP 503 shape), and a clear
      refusal for a version specifier, a URL, an extra or a duplicate, each
      saying that versions live in the lock. Unknown fields already fail
      through `decodeStrict`.
- [x] `recipe.LockPyPI` (`name`, `version`, `sha256`, `size`, `url`, `auto`)
      as `Lockfile.PyPI`, plus the recipe's Python list recorded the way
      `Requested` records the apt one. `auto` marks what the resolver pulled
      in, exactly as it does for apt.
- [x] `Lockfile.HasWheelChecksums()` beside `HasChecksums()`.
- [x] Round-trip tests, including a v0.6 lock with no Python in it and a lock
      with Python but no apt changes.

## Task 3: the Python step, rendered and parsed (pure Go)

New file `internal/builder/python.go`, the only place that produces this text,
as `provision.go` is for the rest.

- [x] `RenderPythonScript(imageRecipe)`: create the environment, write the
      `profile.d` line, set ownership, run pip, write the report. Tested
      through a real `/bin/sh` with hostile paths, like the provision script.
- [x] `ParsePipReport(io.Reader) ([]recipe.LockPyPI, error)` against
      `testdata/pip-report.json` captured in Task 0: name, version, URL,
      hash, size, and which entries the recipe asked for.
- [x] `RenderRequirements(lock) string`: sorted `name==version
      --hash=sha256:…` lines for the offline install.
- [x] Refusals with their own errors: a report without hashes, a source
      distribution in the report, a requested package missing from it.

## Task 4: builder wiring (behind the fake bootstrapper)

- [x] `PhaseInstallPython` in `Phases()` and `OfflinePhases()` after
      `PhaseProvision`, with a title; the mmdebstrap progress parser is left
      alone, since pip's output is not apt's.
- [x] `Stage` and `StageOptions`: the script path, the report path to
      download, and offline the requirements file and the wheel directory.
- [x] `CustomizeHooks`: the Python script after the provision script, then
      `download` the report. Offline: bring the wheels in (`copy-in`; confirm
      the command in mmdebstrap(1)), install from them, and remove the
      directory before the tarball is made.
- [x] `PackagesToInstall` adds `python3` and `python3-venv` when the recipe
      has Python packages, the way `EssentialPackages` works today.
- [x] The lock gains `[[pypi]]` from the parsed report;
      `Result.PythonPackageCount` for the summary line.
- [x] Offline: requirements rendered from the lock, `--require-hashes`, and
      the installed set compared with the lock afterwards, as
      `compareWithLock` does for apt, with its own error.
- [x] Fake-bootstrapper tests for every bullet: hook order and content, the
      lock written, each refusal.

## Task 5: the wheel pool

- [x] `pool.Entry` gains an absolute `URL` (empty keeps today's base plus
      file name), so `Fetch`, `Verify` and `Prune` serve both pools without a
      second downloader; `WheelsDirName = "vendor/wheels"`; `WheelManifest`
      beside `Manifest`, with the same refusals for unsafe or duplicate file
      names and a lock without checksums.
- [x] `httptest` tests mirroring the existing pool tests: fresh, resumed,
      corrupt, mismatched, canceled.
- [x] No Launchpad-style fallback. Files on PyPI are immutable, so a missing
      one is an error naming the package, not a second source to try.

## Task 6: vendor and offline

- [x] `vendor` fills both pools in one run: the phases and the summary say
      how many `.deb`s and how many wheels; `--prune` covers both; a lock
      with no Python behaves exactly as it does today.
- [x] `build --offline` verifies both pools before it makes a work directory,
      and `ErrPoolIncomplete` says which one is short.
- [x] It refuses when the recipe's Python list no longer matches the lock's,
      the way it already refuses a changed `include`.
- [x] Exit codes unchanged: 1 for what the user can fix, 2 for a failed
      build.

## Task 7: interface and docs

- [x] `internal/form`: a "Python packages" line on the Packages page, through
      `Fields`, `Values`, `FromRecipe`, `ToRecipe` and `Summary`; the 24.04
      PEP 668 note becomes a statement that frostroot installs these into an
      environment for you.
- [x] `cli/validate` and `cli/build` summaries; usage text.
- [x] README: status, the recipe table, a "Python packages" section, the lock
      excerpt, the commands table, scope (pip leaves "deliberately not yet"),
      layout, verification.
- [x] `capture`: the pip findings stay as they are, and their advice points at
      `[python]`. Writing them into the recipe is a later change, not this one.

## Task 8: verification on the build host

- [ ] A real 24.04 image with `numpy` and `jupyterlab`: import it in WSL,
      check the environment is on PATH for the user, that `python -c "import
      numpy"` works, and that nothing was compiled during the build.
- [ ] `vendor` for real, then `build --offline` with no network: the same
      versions, every hash satisfied.
- [ ] Two offline rebuilds: one `sha256sum`, or the narrower statement Task 0
      forced.
- [ ] The same on 22.04, and on 20.04 if Task 0 kept it.
- [ ] `TestIntegrationPython` beside the existing integration tests.
- [ ] Record the results in the spec's Verification section and update the
      README's status paragraph.

## Task 9: finish

- [ ] `Version = "0.7.0"`.
- [ ] Pull request; ask before merging.

## Why this is attackable

- The unknowns are all in Task 0, and they are answered on a machine, not in
  a design argument. Nothing downstream depends on a guess.
- Tasks 2 and 3 are ordinary Go with fixtures: no mmdebstrap, no network, no
  root. They can be written while the spike is still running.
- Every build change is testable against the fake bootstrapper that already
  exists, so the slow path runs only in Task 8.
- The pool is extended by one field, not rewritten, and the apt path is never
  touched. A recipe without `[python]` produces the same bytes as today, and
  that is a test.
- Each task is one commit that keeps the tree green, so the branch can be
  abandoned after any of them without leaving the project worse.

## Risks and fallbacks

| Risk | Where it shows | Fallback |
|---|---|---|
| The release's pip cannot report hashes | Task 0 | Pin a `pip` wheel in the lock and upgrade the environment's pip from it; or ship `[python]` for 24.04 only in v0.7; or a pinned, checksummed resolver binary, as source keys are pinned today |
| A common package has no wheel | Task 0 | Wheels-only stays; the error names the package and suggests its apt equivalent |
| Rebuilds differ in `.pyc` or metadata | Task 0 | Scope byte identity to apt-only images and say so in the README; keep same versions and checksums as the promise |
| pip cannot reach PyPI from the chroot | Task 0 | Resolve on the host with `--platform` and `--python-version`, then install offline inside the image |
| Wheels bloat the image | Task 0 size numbers | Delete the wheel directory in the hook before the tarball is made; document the pool's size next to `vendor/debs` |
| The feature grows toward Node and Rust | Any task | The spec's non-goals; they are v0.8 and later, on this skeleton |
