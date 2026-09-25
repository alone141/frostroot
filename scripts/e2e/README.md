# End-to-end scenarios

Some of frostroot's promises only a real build can check. Examples: two
offline rebuilds are the same bytes, nothing the build trusted for itself
ships in the image, and the full-screen form draws in a real terminal. Unit
tests can't reach these, and the integration tests
(`scripts/integration.sh`) check only a few. The scenarios here check the
rest. Each one runs the real binary, mmdebstrap and network, checks its own
results, and ends in a verdict.

```sh
scripts/e2e/run.sh                      # list them, with what each proves and how long it takes
scripts/e2e/run.sh pip-trust tui        # run some
scripts/e2e/run.sh all                  # run every one: about two hours
scripts/e2e/clean.sh                    # remove everything they made
```

On a Windows checkout, run these through `scripts/wsl.sh` or
`scripts/wsl.ps1`, like every other command.

## The scenarios

| Scenario | Proves | Takes |
|---|---|---|
| `offline-identical` | Two offline rebuilds of one lock are byte-identical, the Python environment included, and leave the lock as it was. | ~30 min |
| `certificates` | `[certificates]` reaches the image and the lock. `--ca-bundle` reaches neither. Offline rebuilds are the same bytes with and without it. An offline build refuses a certificate changed under the lock. | ~30 min |
| `no-build-leaks` | Two online builds frozen at one instant over HTTPS, one plain and one with `--ca-bundle` and `--insecure`, come out the same bytes. No file in the image names the build's work directory. | ~15 min |
| `insecure` | `build --insecure` warns and marks the Python resolve unverified in the lock. `vendor` and `build --offline` repeat the warning, and a verified `vendor` reports what it checked. | ~25 min |
| `apt-trust` | The apt that mmdebstrap runs obeys the settings frostroot's setup hook writes, and the cleanup hook removes them. Shown by running mmdebstrap directly with a CA bundle that cannot verify the mirror. | ~10 min |
| `pip-trust` | The pinned pip refuses a private authority, and accepts it with `--cert` or `--trusted-host`, against a local HTTPS server. | ~1 min |
| `failed-build` | A misspelled package fails the build with exit 2. apt's own explanation is printed first, and neither a lock nor a tarball is written. | ~2 min |
| `capture-roundtrip` | `capture` of an image frostroot built writes a recipe that validates and asks for the same packages. | ~10 min |
| `tui` | The full-screen form draws in a pseudo-terminal, searches the real archive and PyPI, and writes what was picked. | ~3 min |
| `wsl-boot` | An image imports into WSL and boots: the user, sudo, systemd, DNS, locale, timezone, the Python environment, the recipe's certificate authority, and apt still verifying TLS. Needs `wsl.exe`, so it skips anywhere but WSL. `E2E_FROSTROOT=/path/to/binary` puts a release's binary through it. | ~15 min |
| `harness` | The harness reports a failure when it should, and never removes anything outside its root. | seconds |

AGENTS.md has a list of which change calls for which scenario.

## Verdicts

A scenario prints `ok` or `FAIL` for each check, then one of:

| Verdict | Exit | Meaning |
|---|---|---|
| `PASS` | 0 | Every check held. |
| `FAIL` | 1 | At least one check failed; the lab is kept to look at. |
| `INCONCLUSIVE` | 3 | The world moved under it, such as the archive publishing between two builds. Run it again. |
| `SKIP` | 4 | A tool it needs is missing, such as mmdebstrap or openssl. |

`run.sh` exits 0 only when every scenario it ran passed.

## Where things go

Everything is under one root, `$FROSTROOT_E2E_ROOT`, which defaults to
`/var/tmp/frostroot-e2e-<uid>`:

```
/var/tmp/frostroot-e2e-1000/
  cache/                  XDG_CACHE_HOME for every run: frostroot's work
                          directories and its package-index cache
  offline-identical/      one scenario's lab: recipes, locks, dist/, the
                          stdout and stderr of every command, unpacked images
  offline-identical.log   what run.sh printed while it ran
```

Each scenario empties its own lab when it starts. `clean.sh` removes the
whole root, and nothing outside it.

The root is under `/var/tmp`, not your home directory. mmdebstrap runs in a
user namespace that cannot enter a 0750 home, and some scenarios hand it
their files. It isn't under `/tmp` either, because WSL empties `/tmp`.

## Writing a scenario

A scenario is `scripts/e2e/NAME.sh`, and `run.sh` finds it by that name.
Start from this:

```bash
#!/usr/bin/env bash
# proves: one sentence; run.sh lists it
# needs: the network, mmdebstrap, ubuntu-keyring, user namespaces
# takes: about N minutes: what takes the time
#
# Why this needs a real build, and what the checks rest on.
source "$(dirname "$0")/lib.sh"

e2e_begin NAME
e2e_require_bootstrap                  # SKIP unless a real build can run here
e2e_build_frostroot                    # this checkout's binary, as $FROSTROOT
e2e_recipe "$LAB/lab" e2e-name 24.04 "git" "requests"

e2e_run build "$LAB/lab" "$FROSTROOT" build --plain
e2e_expect_status 0 build              # ends the scenario if it is not 0
e2e_check "the lock records the wheels" e2e_contains "$LAB/lab/frostroot.lock" "[[pypi]]"
e2e_end
```

`lib.sh` provides these:

- `e2e_build_frostroot` builds the checkout's binary into the lab as
  `$FROSTROOT`, or copies in the one `E2E_FROSTROOT` names.
- `e2e_run LABEL DIR COMMAND...` runs a command, with its output in
  `$LAB/LABEL.out` and `$LAB/LABEL.err`, and its exit status in
  `$e2eStatus`. Standard input is `/dev/null` unless `E2E_STDIN` names a
  file.
- `e2e_check DESCRIPTION COMMAND...` counts a check that passes when the
  command succeeds. The predicates are `e2e_contains`, `e2e_lacks`,
  `e2e_has_line`, `e2e_matches`, `e2e_lacks_match`, `e2e_tree_lacks`,
  `e2e_same_file` and `e2e_not`.
- `e2e_entries TARBALL` and `e2e_unpack TARBALL DIR` read an image.
- `e2e_check_no_build_trust TARBALL` checks that no build-only trust
  setting is left in the image's `/etc/apt`, and no staged file at its
  root.
- `e2e_certificate` makes a throwaway certificate authority.
- `e2e_inconclusive` ends a scenario that can't decide either way.

Rules the harness can't enforce:

- **Every check must be able to fail.** A "lacks" predicate insists that
  its file exists, because a file that was never written lacks everything.
  Before `openssl verify` expects a refusal, check that the store it reads
  exists. When a scenario passes on its first run, look at its logs and
  confirm it saw what it claims.
- **Prove the negative once.** A check that something is refused needs a
  run where it is refused. `apt-trust`'s `refuse` arm and `pip-trust`'s
  `refuse` download are the model.
- **Nothing outside the lab.** No reading `~/frostroot-*` left over from
  earlier sessions, and no writing into the repository. A scenario builds
  what it needs, certificates and servers included.
- **Under `set -o pipefail`, never put `grep -q` at the end of a pipe.**
  grep exits at its first match, and the SIGPIPE that the command before it
  gets turns a match into a failure. Write to a file first, or use a
  here-string.
- Run `harness` after changing `lib.sh`.
