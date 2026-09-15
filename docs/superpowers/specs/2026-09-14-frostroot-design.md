# frostroot Design

Date: 2026-09-14, revised 2026-09-15
Status: approved; revised to match the corrected implementation plan
Repo: `frostroot/` (new project, Linux CLI)

This spec is the source of truth for v1. Implement it via
[`plans/2026-09-15-frostroot-v1.md`](../plans/2026-09-15-frostroot-v1.md); do not
invent extra product scope.

## Revision history

**2026-09-15.** Revised after
[`reviews/2026-09-14-frostroot-plan-review.md`](../reviews/2026-09-14-frostroot-plan-review.md)
and
[`reviews/2026-09-15-frostroot-feasibility.md`](../reviews/2026-09-15-frostroot-feasibility.md).
The product is unchanged — same commands, same artifacts, same audiences. What
changed is implementation detail this spec had got wrong, and which would have
shipped a tarball that does not boot:

| Was | Now | Why |
|---|---|---|
| `internal/export` turns a rootfs directory into `.tar.gz` | mmdebstrap writes the tarball; `export` names it and moves it | Go's `archive/tar` cannot safely tar a rootfs from outside the user namespace: symlink targets, hardlinks, file capabilities and ownership are all lost or wrong |
| Provision essentials `sudo`, `locales`, `tzdata`, `passwd` | adds `systemd`, `systemd-sysv`, `dbus`, `ca-certificates` | `--variant=important` has no systemd, so `[boot] systemd=true` was inert and criteria 4 and 5 were unreachable. Without `ca-certificates`, `git clone https://…` fails in the image |
| One mirror per release | three pocket lines: `<suite>`, `<suite>-updates`, `<suite>-security` | A single suite yields release-day packages and years of unpatched CVEs. The lock example below is an updates-pocket version that the old pipeline could not have produced |
| Recommends off (mmdebstrap default) | Recommends on | `include` should behave like `apt install` on stock Ubuntu |
| Work dir `os.MkdirTemp("", …)` | `$XDG_CACHE_HOME/frostroot` or `/var/tmp/frostroot`, never under `/mnt` | On WSL, `/mnt/<drive>` is 9p: slow, and unreliable for chown and device nodes |
| `--keep-rootfs` | `--keep-work` | There is no rootfs directory to keep any more; the work directory is what is useful |
| Package list via `dpkg-query --root` | parse `/var/lib/dpkg/status` in Go | `--root` needs host dpkg ≥ 1.21, excluding Ubuntu 20.04 and Debian 11 build hosts |
| Lock has no `sources`; packages have no `arch` | both recorded | Vendoring needs them; adding them later is a lock-format migration |

## Goal

frostroot freezes an Ubuntu LTS root filesystem. The user describes a distro
release, a sudo user, and apt packages in a recipe. frostroot produces:

1. A **lockfile** with every installed apt package version.
2. A **rootfs tarball** that is a golden image (`wsl --import` today; other exporters later).

The tarball is what you hand to a lab or an airgapped machine. The recipe and
lock are what you git and review. Rebuild-from-lock (exact `pkg=version` /
vendoring) is a follow-up, not v1.

Primary audiences for v1: **golden images** (classroom/lab) and **offline
consumption** of those images. Same-machine restore and teammate sharing are
supported by the same artifacts.

## Non-goals (v1)

Do not implement these in v1. The architecture must not block them.

- Fedora or any non-Ubuntu family
- Extra apt sources (PPAs, deadsnakes, nodesource)
- pip / npm / cargo (or other language) lockfiles
- Vendoring `.deb` files / `build --offline`
- Bit-identical tarball rebuilds
- Bare-metal disk or ISO export
- A full package-picker TUI
- A Windows-native `.exe` engine (the CLI is Linux; WSL is an export target)
- Architectures other than `amd64`

## Key decisions

| Decision | Choice | Why |
|---|---|---|
| Name | `frostroot` | Freeze a root filesystem; not WSL-specific (bare metal later) |
| Language | Go | Single Linux binary; fits a CLI that orchestrates apt/tar |
| Engine | mmdebstrap → customize hooks → mmdebstrap writes the tarball | No Docker; works on any Linux; 20.04 via old-releases |
| Who tars | **mmdebstrap, inside the user namespace** | Ownership, symlinks, hardlinks and xattrs are only correct from inside; Go never walks or deletes a rootfs |
| Distros v1 | Ubuntu 20.04, 22.04, 24.04 amd64 | 20.04 is off standard support; that is the pinning story |
| Pockets | release, `-updates`, `-security` | A golden image must not ship release-day CVEs |
| Recommends | on | `include` behaves like `apt install` on stock Ubuntu |
| Packages v1 | apt names only | One locker; PPAs and language locks come later |
| Source of truth | `frostroot.toml` | `init` writes it; `build` never prompts |
| Reproducibility | Practical | Tarball is the golden image. Rebuild from recipe is best-effort against current mirrors. Vendoring is next. |
| Image profile | Minimal WSL-ready | systemd, sudo user, `/etc/wsl.conf`, locale/timezone, passwordless sudo |
| Where it runs | Linux CLI | Including WSL. Windows users run frostroot inside WSL. Windows is not a build or test target. |

## Success criteria (v1 done)

On a Linux host with `mmdebstrap` installed (user namespaces or root):

1. `frostroot init` writes a valid `frostroot.toml`.
2. `frostroot build` for Ubuntu 24.04 with a few packages produces `frostroot.lock` and `dist/<name>-ubuntu-24.04-amd64.tar.gz`.
3. The lock lists **every** installed package (not only the requested names) with exact versions and architectures, plus the three source lines used.
4. The tarball contains `/etc/wsl.conf` with systemd on and the default user, plus that user's home and passwordless sudo — and systemd is actually installed.
5. **The tarball is structurally sound**: every symlink carries a non-empty target, ownership is real (no subuid-range uids), and the host's `/etc/resolv.conf` and `/etc/hostname` are absent.
6. `wsl --import` of that tarball on Windows boots and logs in as that user (manual check; not in default tests).
7. Ubuntu 20.04 builds against old-releases (or `--mirror`), not archive.ubuntu.com, and `build` warns that its packages carry known unfixed CVEs.
8. Package versions come from the `-updates`/`-security` pockets, not release day.
9. `go test ./...` passes offline, without root, without mmdebstrap.

Criterion 6 is the only one that tests the product promise end to end, and it
cannot be automated. Criterion 5 is the automatable proxy for it; it exists
because an earlier plan passed every unit test while producing an unbootable
image.

## CLI

Binary: `frostroot`

```
frostroot init       # prompts → frostroot.toml
frostroot validate   # check recipe, no network
frostroot build      # recipe → lock + tarball
```

No other subcommands in v1.

### `init`

- Working directory = recipe directory.
- Abort if `frostroot.toml` exists unless `--force`.
- Prompts: image name, release (`20.04` / `22.04` / `24.04`), username (default `student`), timezone (default `UTC`), packages (comma-separated) **or** a preset that expands to package names.
- Presets exist only in `init` (not as a recipe feature). v1 presets:
  - `none` — empty include list
  - `build-essential` — `build-essential`, `git`, `cmake`, `pkg-config`
  - `python-lab` — `python3`, `python3-pip`, `python3-venv`, `git`
- Written from a text template so the file carries explanatory comments; the result must pass `validate`.
- Does not write a lock or tarball.
- Does not need root.

### `validate`

- Parse and check `frostroot.toml`. Unknown fields are an error, so a `[package]` typo fails loudly instead of silently yielding an empty include list.
- Known LTS, `arch == amd64`.
- Image name: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (safe tarball filename; non-empty).
- User name: `^[a-z_][a-z0-9_-]*$`, length 1–32, not `root`.
- `[wsl].default_user`, if set, must equal `[user].name`. If omitted, it is `[user].name`.
- `[locale].lang` and `[locale].timezone` are validated: they are interpolated into shell hooks, so this is an injection boundary, not a cosmetic check. Existence of the zoneinfo file is checked inside the chroot at build time, which `validate` cannot do offline.
- Package tokens match `^[a-z0-9][a-z0-9+.-]+$`. Empty `include` is allowed (base + provision essentials only).
- Print every problem; exit 0 or 1.
- No network, no mmdebstrap.

### `build`

- Runs `validate` first.
- Flags:
  - `--mirror URL` — override the base archive URL for this release; replaces it in **all three** pocket lines
  - `--keep-work` — leave the work directory after success
- Warns on stderr when the release is end-of-life (20.04): its packages carry known CVEs that cannot be fixed without Ubuntu Pro.
- Overwrites existing lock and matching tarball without asking.
- Prints the WSL import example on success:

```
wsl --import <image.name> <install-dir> dist/<image.name>-ubuntu-<release>-amd64.tar.gz
```

When `wslpath` is available, also prints the Windows form of the path so it can
be pasted into PowerShell.

## Files

### `frostroot.toml` (intent — user edited)

Versions do not belong here. Package entries are names only.

```toml
[image]
name = "cpp-lab"
release = "22.04"    # 20.04 | 22.04 | 24.04
arch = "amd64"

[user]
name = "student"
sudo = true

[wsl]
systemd = true
default_user = "student"

[locale]
lang = "en_US.UTF-8"
timezone = "UTC"

[packages]
include = ["git", "build-essential", "cmake"]
```

v1 user model: **passwordless sudo** when `[user].sudo` is true; if false, the
user exists with no sudo. No password field. WSL login uses
`[wsl].default_user` (defaults to `[user].name`). This is a lab image, not a
hardened server.

### `frostroot.lock` (fact — build written)

```toml
version = 1
distro = "ubuntu"
release = "22.04"
suite = "jammy"
arch = "amd64"
mirror = "http://archive.ubuntu.com/ubuntu"
sources = [
  "deb http://archive.ubuntu.com/ubuntu jammy main universe",
  "deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
  "deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
]
frostroot_version = "0.1.0"

requested = ["git", "build-essential", "cmake"]

[[packages]]
name = "git"
version = "1:2.34.1-1ubuntu1.11"
arch = "amd64"
```

- `requested` is the recipe `packages.include` list as written (not provision essentials).
- `[[packages]]` is **every** installed package (dependencies and essentials included), sorted by name so a no-op rebuild produces an empty diff.
- `sources` records the three lines actually used, including any `--mirror` override.
- That `git` version is an updates-pocket version. It is reachable precisely because `sources` carries `-updates`; with a single suite line it would not be.
- No `sha256` / `filename` in v1. Keep this list-of-tables shape so vendoring can add those fields later.
- If `include` changed since the last lock, `build` overwrites the lock.

### Tarball

Path: `dist/<image.name>-ubuntu-<release>-<arch>.tar.gz`

Written by mmdebstrap inside the user namespace, then moved into `dist/`
(rename where possible, copy+fsync+rename across filesystems — the work
directory and `dist/` are routinely on different devices under WSL).

This is the golden image. Airgap **run** = import this file. Airgap **build** is
not a v1 promise.

## Architecture

One Go module, one binary. Five internal packages plus `cmd`.

```
frostroot.toml --> recipe --> builder --> mmdebstrap --> image.tar.gz --> export --> dist/*.tar.gz
                                   |                          |
                                   |                          +--> dpkg status --> frostroot.lock
                                   +--> customize hooks
```

### `cmd/frostroot`

`main` only. Wires CLI.

### `internal/cli`

`init` / `validate` / `build`. `init` is the only command that prompts. `build`
never prompts.

Prompting goes through a `Prompt` interface so tests inject scripted answers (no
real TTY). `build` installs a `signal.NotifyContext` handler and maps
`context.Canceled` to exit 130.

### `internal/recipe`

- Parse/validate toml and lock. Unknown fields rejected.
- Types: `Recipe`, `Lockfile`, `LockPackage`.
- Nothing else reads the raw files.

### `internal/distro`

Ubuntu LTS table. Same package should later grow a `Distro` interface; v1 has
one implementation.

| release | suite  | base URL | EOL |
|---------|--------|----------|-----|
| 20.04   | focal  | `http://old-releases.ubuntu.com/ubuntu` | yes |
| 22.04   | jammy  | `http://archive.ubuntu.com/ubuntu` | no |
| 24.04   | noble  | `http://archive.ubuntu.com/ubuntu` | no |

Components: `main universe`.

`Sources(baseOverride)` is the **only** place `deb` lines are constructed. It
returns three per release: `<suite>`, `<suite>-updates`, `<suite>-security`.
old-releases carries focal's pockets frozen at end of standard support, so the
shape holds there too — it simply cannot receive anything new, which is what the
`EOL` flag warns about.

Unknown release → validation error.

Interface to aim at (even if only Ubuntu exists):

```text
BootstrapInfo(release, arch) → { suite, base, components, eol }
Sources(baseOverride)        → [3]string
```

### `internal/builder`

Orchestrates one build. Host requirements: Linux, `mmdebstrap` on `PATH`, and either:

- user namespaces (`mmdebstrap --mode=unshare`), or
- uid 0 (`--mode=root`)

If `mmdebstrap` is missing, fail with the host install hint
(`sudo apt install mmdebstrap`), not a stack trace. If user namespaces are
unavailable, mmdebstrap's own stderr is the explanation — it is streamed to the
terminal and its tail is attached to the error.

`Bootstrapper` interface (real impl shells out to mmdebstrap; tests fake it):

```text
Run(ctx context.Context, spec BootstrapSpec) error

BootstrapSpec { Suite, Sources, Include, Hooks, TarPath, WorkDir, Arch, Recommends, Keyring }
```

`ctx` is on the interface from the start so Ctrl-C works without a later
signature change across every fake.

`Hooks` are shell snippets generated by the builder (upload wsl.conf and
sudoers, useradd, locale, timezone, host-artifact cleanup, dpkg status
download). They fail closed: no `|| true`. Every interpolated recipe value is
shell-quoted even though `validate` already checked it.

The package list comes from parsing the downloaded `/var/lib/dpkg/status`, not
from a host `dpkg-query`. Only `Status: install ok installed` stanzas count.

There is no production `WriteProvisionFiles` helper. Tests that need files on
disk write them themselves.

### `internal/export`

Names the artifact and moves it into place. **It does not tar anything.**

```text
TarballRelPath(imageName, release, arch) → dist/<name>-ubuntu-<release>-<arch>.tar.gz
Place(src, dest)                         → atomic where possible, copy+fsync+rename across devices
```

Later disk/ISO exporters implement the same “artifact in → artifact out” idea.

## Build pipeline

1. Validate the recipe (fail closed).
2. Resolve release → suite + base URL + three pocket lines (`--mirror` overrides the base URL in all three). Warn if the release is EOL.
3. Check Linux + `mmdebstrap` + the Ubuntu archive keyring.
4. Choose a work root: `$XDG_CACHE_HOME/frostroot`, else `/var/tmp/frostroot`; never under `/mnt`. Create a per-build directory inside it. Delete on success unless `--keep-work`. Keep on failure and print the path.
5. Render `/etc/wsl.conf` and the sudoers drop-in into `<work>/stage/` and generate the hook list.
6. Run mmdebstrap with `TMPDIR=<work>`:
   - suite, three `deb` lines, components `main universe`, `--architectures=amd64`
   - `--variant=important`, `--aptopt='Apt::Install-Recommends "true"'`
   - explicit `--keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg`
   - `--include` = recipe `packages.include` **plus** provision essentials: `systemd`, `systemd-sysv`, `dbus`, `sudo`, `locales`, `tzdata`, `passwd`, `ca-certificates`
   - target is `<work>/image.tar.gz`, so mmdebstrap tars from inside the namespace
   - mode `unshare` unless uid 0, then `root`
   - stderr streamed to the terminal; last 4 KiB kept for the error message
7. Provision via `--customize-hook` (not a separate chroot tool):
   - `upload` the rendered wsl.conf and sudoers drop-in; `chmod 0440` the sudoers file
   - verify `/usr/share/zoneinfo/<tz>` exists, then set localtime and `/etc/timezone`
   - `useradd --create-home --shell /bin/bash --user-group <name>`
   - locale from `[locale].lang`
   - remove the host `/etc/resolv.conf` and `/etc/hostname` that mmdebstrap copies in
   - `download /var/lib/dpkg/status` to the work directory, last
   - mmdebstrap's default cleanup already empties machine-id and removes apt lists and cache; do not duplicate it
8. Parse the downloaded dpkg status → write `frostroot.lock.tmp` (`requested` = recipe include; `[[packages]]` = every installed package, sorted).
9. Move `<work>/image.tar.gz` to `dist/<name>-ubuntu-<release>-amd64.tar.gz`, then rename the lock into place — so a failed move never leaves a lock describing an image that does not exist.
10. Print the `wsl --import` line.

v1 does **not** re-install from lock versions (`pkg=version`). A second `build`
hits current mirrors and may drift. The distributed golden image is the tarball.
Feeding lock versions back into apt is part of the vendoring/rebuild-strict
follow-up.

## Error handling

| Class | Exit | Examples |
|-------|------|----------|
| User error | 1 | not Linux; missing/invalid toml; `init` without `--force` when file exists; mmdebstrap not on PATH; keyring missing |
| Build error | 2 | no userns and not root; mmdebstrap failed (unknown package, mirror down); provision hook failed; disk full; tarball missing |
| Interrupted | 130 | Ctrl-C |

Rules:

- Do not write lock or tarball unless the whole build succeeded.
- Write the lock to `*.tmp` and rename only after the tarball lands.
- On failure: delete tmp artifacts; keep the work directory; print its path; reprint the tail of mmdebstrap stderr when mmdebstrap failed.
- On Ctrl-C: treat as failure (keep work directory, remove tmp artifacts). Signal mmdebstrap with SIGINT and a generous wait, never SIGKILL: in root mode it has proc, sys and dev mounted inside the chroot.
- frostroot never deletes a chroot directory. Under this design it never creates one it would have to.
- Mirror failure: name the URL and suggest `--mirror`.
- v1 **build** requires network. v1 **consume** (the tarball) does not.

## Testing

Default `go test ./...`: offline, no root, no mmdebstrap, on Linux. Windows is
not a test target — the CLI is Linux and Windows users run it inside WSL.

**Always-on unit tests**

- Recipe parse/validate tables: good file, unknown release, bad user, bad package token, bad locale, bad timezone, unknown field, missing sections, empty name.
- Distro table: 20.04 → focal + old-releases + EOL; 22.04/24.04 → archive; unknown → error; `Sources` yields three pockets and honours `--mirror`.
- Lock round-trip encode/decode; `requested` vs full `[[packages]]`; deterministic output.
- dpkg status parsing: installed-only, sorted, epoch versions, continuation lines, trailing stanza, empty input is an error.
- Tarball name and cross-device `Place`.
- Hook generation: no `|| true`, values shell-quoted, timezone existence check present, host-artifact cleanup present.
- `validate` CLI against `testdata/` fixtures (exit 0 vs 1).
- `init` with a fake `Prompt` writes the expected toml, and it validates.
- `build` exit codes 0/1/2/130 and the EOL warning.

**Builder tests with a fake `Bootstrapper`**

- The fake writes the same two artifacts the real one does: a tarball at `TarPath` and a dpkg status file in the work directory. It does not run hooks.
- Assert the include list (user packages + essentials, including systemd) is passed through.
- Assert three pocket lines reach the bootstrapper, and `--mirror` replaces the base in all three.
- Assert the lock is produced from the stub status and records `sources`.
- Assert the tarball lands in `dist/`.
- On fake failure: no lock, no tarball, no `.tmp` residue, work directory kept.

**Integration (`-tags=integration`)**

- Skip if not Linux or mmdebstrap missing.
- Build a tiny 24.04 image (`include = ["bash"]` plus essentials).
- Assert the lock contains `bash` and `systemd` with versions, and three `sources`.
- **Assert every symlink in the tarball has a non-empty target, and that there are thousands of them.** This is the regression test for the defect that motivated the 2026-09-15 revision.
- Assert no entry has a subuid-range owner.
- Assert `/etc/wsl.conf` content, the user's home, the sudoers drop-in, and the absence of `/etc/resolv.conf`.
- Needs network + userns or root. Not part of the default test loop.

Do not automate `wsl --import` in v1 tests. Document it as a manual check.

## Extension points (later, required)

These must remain possible without rewriting `recipe` / `cli` / `export`.

**Vendoring `.deb`s (first follow-up after v1)**
Add `sha256` and `filename` on `[[packages]]`; `arch` and `sources` are already
there. New command `frostroot vendor` fills `vendor/debs/`. `build --offline`
uses that pool. Lock shape is already a list of tables for this.

**Extra apt sources**
Recipe `[sources]` (PPA, deadsnakes, nodesource). Builder appends them to the
`deb` lines it already constructs. Lock records url, suite, signed-by. Still
apt, still one locker.

**Language lockfiles**
Optional `[python]`, `[node]`, `[rust]` in the recipe; separate lock arrays
(`[[pypi]]`, …). Run after apt provision. Never mix with `[[packages]]`.

**Fedora / other families**
New `internal/distro` implementation: `dnf --installroot` + `rpm -qa`. Recipe
gains `distro = "fedora"`. CLI verbs stay the same.

**Bare metal**
New exporter: image in → disk/ISO out. Builder unchanged.

**TUI**
Writes `frostroot.toml` only. `build` stays non-interactive.

## Repo layout

New git repository at `frostroot/` (sibling of other projects, not nested in them).

```
frostroot/
  cmd/frostroot/          # main
  internal/cli/           # app.go, initcmd.go
  internal/recipe/        # recipe.go, validate.go, lock.go
  internal/distro/        # ubuntu.go
  internal/builder/       # builder.go, bootstrap.go, provision.go, status.go, workdir.go
  internal/export/        # name.go
  testdata/               # recipe fixtures
  docs/superpowers/specs/ # this document
  docs/superpowers/plans/ # implementation plan
  docs/superpowers/reviews/
  README.md
  go.mod
```

Module path: `frostroot` for v1 unless/until published
(`github.com/<user>/frostroot`). A module path without a dot cannot be
`go install`ed; that is acceptable for v1.

## Open questions

One, and it is the reason the plan opens with a manual spike: **how a
mmdebstrap-built rootfs with systemd behaves on first boot under WSL.**
Specifically whether `systemd-resolved` conflicts with WSL's generated
`/etc/resolv.conf`, and which units fail. Task 0 of the plan settles it by
observation; if masking is needed, the hook is added then and not before.

Anything else not in this spec is out of scope until a new spec says otherwise.
