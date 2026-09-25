# AGENTS.md

For AI models and anyone else working in this repository.
[CONTRIBUTING.md](CONTRIBUTING.md) is the standard for code, tests and
commits; read it first. This file covers what CONTRIBUTING.md doesn't: how to
run things, the rules that are easy to break without noticing, and where to
find the project's state. Claude Code reads this file through
[CLAUDE.md](CLAUDE.md).

## What frostroot is

A Go command line that freezes an Ubuntu root filesystem for WSL. A recipe
(`frostroot.toml`) becomes a lock (`frostroot.lock`) and a tarball. The
tarball is built by driving [mmdebstrap], and `wsl --import` takes it as it
is. `vendor` downloads everything the lock names. `build --offline` rebuilds
from those files byte for byte. Builds need Linux. The module must still
compile for Windows.

The README's "Layout" section maps the packages.

[mmdebstrap]: https://gitlab.mister-muffin.de/josch/mmdebstrap

## Commands

| To | Run | Notes |
|---|---|---|
| Run every check before a push | `scripts/check.sh` | gofmt, vet with and without the integration tag, `go test -race`, the Windows build, golangci-lint. About 2 minutes. CI runs the same script. |
| Test one package, or one test | `scripts/check.sh --only test -- -run TestName ./internal/cli/` | Arguments after `--` go to `go test`. |
| Check for known vulnerabilities | `scripts/check.sh --only vuln` | Runs govulncheck against the toolchain go.mod pins. Needs the network. |
| Run the integration tests as CI does | `scripts/integration.sh` | Real mmdebstrap. About 6 minutes in CI and much longer on a slow link; `FROSTROOT_INTEGRATION_TIMEOUT=90m` raises the 40m limit. Fails if a required test skipped or went missing. |
| Prove a test pins a fix | `scripts/mutate.sh --revert COMMIT` | Reverts the commit's non-test code, keeps its tests, and runs them. Exit 0 means the tests caught it. |
| Break one line and run the tests | `scripts/mutate.sh --sed FILE 'SED'` | Restores the file however the run ends. |
| Run real-build scenarios | `scripts/e2e/run.sh`, then `scripts/e2e/run.sh NAME` | No arguments lists them. Each scenario checks its own results. See [scripts/e2e/README.md](scripts/e2e/README.md). |
| Remove what the scenarios made | `scripts/e2e/clean.sh` | Deletes everything under `/var/tmp/frostroot-e2e-<uid>`. |
| Re-record the TUI's golden frames | `FROSTROOT_UPDATE_FRAMES=1 go test ./internal/tui/` | Then read the diff; each frame is what a person sees. |
| Build the binary | `go build ./cmd/frostroot` | `frostroot version` shows the commit, with `+dirty` if there are uncommitted changes. |

### On a Windows checkout

The toolchain lives in WSL. Run every command above through the wrapper for
your shell:

- Git Bash: `scripts/wsl.sh scripts/check.sh`
- PowerShell: `.\scripts\wsl.ps1 scripts/check.sh`

Hand-typed alternatives break in ways that are easy to miss:

- `wsl -- bash -lc '...'` goes through WSL's default shell, which expands
  every `$VAR` before your command sees it. The wrappers use `wsl -e`, which
  starts no shell.
- Git Bash turns `/var/tmp/x` into `C:/Program Files/Git/var/tmp/x` when it
  calls a Windows program. `scripts/wsl.sh` switches that off.
- golangci-lint is installed in `$(go env GOPATH)/bin`, which no login
  profile puts on PATH. `scripts/wsl-exec.sh` adds it.
- A variable set on the Windows side reaches WSL only if `WSLENV` names
  it. The wrappers forward every `FROSTROOT_*` and `E2E_*` variable and
  `SOURCE_DATE_EPOCH`, so `FROSTROOT_INTEGRATION_TIMEOUT=90m scripts/wsl.sh
  scripts/integration.sh` works. Any other variable has to be set inside
  WSL.
- Windows PowerShell 5.1 drops the double quotes inside an argument it
  passes to a native program. Put anything that needs them in a script file.
- frostroot refuses to build under `/mnt`, a Windows drive.
- WSL empties `/tmp`. Keep anything you need later under `/var/tmp` or `~`.

## Before you call a change done

- `scripts/check.sh` passes.
- A behavior change comes with a test that fails without it. For a fix,
  commit it, then run `scripts/mutate.sh --revert HEAD`. Write the result in
  the commit body, as "with X reverted, TestY fails", and only after
  running it. If the fix added a function its own tests call, the revert
  can't compile and proves nothing, and the script says so. Then break the
  fix with `--sed` instead: keep the function, empty what it does.
- If the change touches what the image holds, the lock, trust, or
  mmdebstrap's flags, run the matching scenario:
  - offline builds or reproducibility: `offline-identical`
  - certificates: `certificates`
  - anything the build uses but must not ship: `no-build-leaks`
  - hook text: `apt-trust`
  - pip's trust flags: `pip-trust`
  - the form: `tui`
  - capture: `capture-roundtrip`
- A new recipe field needs a form field, a mapping in `FromRecipe` and
  `ToRecipe`, and a line in the recipe template in `internal/cli/init.go`.
  Otherwise `edit` silently drops it. `TestRecipeRoundTrip` and
  `TestFormBindingCoversEveryField` catch most of this.
- A user-visible change updates the README and the command's usage text in
  `internal/cli` together.

## Rules that are easy to break

**What reaches the image**

- Build-only apt settings go in through a `--setup-hook` and come out in
  the last `--customize-hook`. Never use `--aptopt` for them: mmdebstrap
  ships every `--aptopt` in the image. See `aptTrustSetupHook` in
  `internal/builder/bootstrap.go`.
- Only mmdebstrap writes the tarball. Go code never tars, walks or deletes
  a root filesystem, because ownership, hardlinks and capabilities would
  come out wrong. `internal/export` only names and places the tarball.
- Two offline builds of one lock are byte-identical. Online builds and
  offline builds are not promised to match. An offline build takes
  everything from the lock and never writes it: the frozen instant, the deb
  lines, apt's auto marks and the pip version.
- Nothing the build only used stays in the image, whether pip's reports,
  the wheels, `--ca-bundle` copies or the host's `resolv.conf`. If you stage
  a new file, a hook must delete it, and `no-build-leaks` must still pass.
- The host's environment must not change the result. The Python step
  unsets every `PIP_*` variable and the certificate path variables. Don't
  pass anything else through.
- Every value that reaches a shell is validated with a strict pattern and
  quoted with `shellQuote`. A hook never swallows a failure: no `|| true`.
  `TestHooksAndScriptNeverSwallowFailures` checks this.

**The lock**

- A failed or interrupted build writes no lock, no tarball and no temporary
  file, and keeps its work directory. The tarball goes into place first,
  then the lock is renamed into place.
- The lock format stays `version = 1`. A new field is optional and
  `omitempty`. An unknown field is an error, and every older lock must still
  load and rebuild.
- Package names belong in the recipe, versions in the lock. The recipe is
  intent and the lock is fact.

**Trust**

- Every primary key in a fetched key file must be pinned, not only the
  first, because apt trusts any key in a signed-by keyring.
  `TestFetchKeyRefusesAKeyAppendedToThePinnedOne` checks this.
- `build` never fetches keys. An offline build refuses a key or
  certificate file that changed since the lock was written.
- No credentials in a source URL or `index_url`, and no error message
  quotes them.
- Extra certificate authorities are added to the host's roots, never used
  in place of them. `--ca-bundle` reaches neither the image nor the lock.
- Every HTTP client comes from `pki.Transport`, which keeps the proxy
  environment and requires TLS 1.2 or newer. It is the only place
  `InsecureSkipVerify` is set.
- A checksum decides what is trusted, not a server: the pinned pip's
  SHA-256, `--require-hashes`, and vendor's check before it renames a file
  into place.

**Files and the network**

- Outputs are written to a temporary file in the same directory, then
  renamed. Never `chmod` them: it fails with EPERM on a Windows drive
  mounted in WSL. `internal/export` has the helper.
- Paths a recipe names are relative and stay inside the recipe directory.
  Everything read out of a lock is hostile input.
- `capture` only reads. It copies public signing keys and CA certificates,
  nothing else, and needs no root.
- A network read needs a size bound and a timeout, and honors the
  context. The index download is the one still missing its bound: issue
  #57.
- The package index only suggests names. `internal/builder` never imports
  `internal/index`, and `--plain` never fetches.

**The command line**

- Only `internal/cli` prints or exits. Exit codes are 0, 1 (the user can fix
  it), 2 (the build failed) and 130 (interrupted). A new error the user can
  fix must be added to the `errors.Is` list in `reportBuildFailure`,
  otherwise it exits 2.
- `build` never prompts. Results go to stdout; warnings and progress go to
  stderr.
- mmdebstrap's process group gets SIGINT, never SIGKILL.
- The plain interface asks the same questions in the same order, because
  people pipe answers into it.

**Package boundaries**

- The Charm libraries are imported only in `tui`.
- zstd and xz are imported in `deb`; xz is also imported in `index` for
  `Packages.xz`.
- go-toml is imported only in `recipe`.
- `builder` doesn't import `index`, and `pool` doesn't import `builder`.
- `deb`, `pgp`, `pki`, `distro`, `export` and `textdiff` import no other
  frostroot package. `deb`, `pgp` and `pki` know nothing of recipes and
  hold no policy.

**Tests**

- Unit tests use no internet (`httptest` is fine), no root and no
  mmdebstrap. Use `t.TempDir()`, and inject the environment through
  `Getenv`.
- Integration tests carry the `integration` build tag, are named
  `TestIntegration*`, and call `skipUnlessMmdebstrapAvailable`.
  `scripts/integration.sh` requires five of them by name, so renaming one
  means editing that list.
- A fake implements the real interface and lives in the test file that uses
  it. `builder` and `cli` each have their own `fakeBootstrapper`.
- New hook text is run through a real `sh`, with hostile paths.

## Where the state is

- **What is open:** GitHub issues. #85 indexes the 2026-09-19 audit, and
  each issue says whether it is fixed. Don't infer state from branch
  names; old branches linger.
- **Design:** `docs/superpowers/specs/` has one spec per version. Each is
  written before the code, then updated as the code teaches.
  `docs/superpowers/plans/` holds the task lists features are built from.
  `docs/superpowers/reviews/` holds dated findings. The audit there is a
  point-in-time record and is never edited after the fact; its issues
  track what changed.
- **Verification:** the README's "Verification" section records what was
  run for real, one paragraph per version. Add yours after you have run it,
  not before.
- **Releases:** `builder.Version` must equal the tag, or `release.yml`
  refuses to publish. Keep the line as `const Version = "X.Y.Z"`, because
  the workflow reads it with sed. Pushing the tag is the owner's decision.

## Working agreements

- A branch per change, and a pull request into `master`. Never commit to
  `master` directly.
- CI runs only on pull requests and on pushes to `master`, so open the PR
  to get checks. Merge when CI is green.
- Commits follow CONTRIBUTING.md and are authored as
  `alone141 <68744286+alone141@users.noreply.github.com>`. Before the first
  commit, check that `git config user.name` and `git log -1 --format='%an'`
  agree: the local config has drifted once.
- For a feature, write its plan in
  `docs/superpowers/plans/<date>-frostroot-<topic>.md` before the code.
- Don't commit `vendor/`, `dist/` or a lock produced by a scenario.
