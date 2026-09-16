# frostroot Design

Date: 2026-09-14, revised 2026-09-15 and 2026-09-16
Status: approved; implemented as v0.1.0 (2026-09-16); revised to match the corrected plan, the Task 0 spike, and what real builds taught the implementation
Repo: `frostroot/` (new project, Linux CLI)

This spec is the source of truth for v1. Implement it via
[`plans/2026-09-15-frostroot-v1.md`](../plans/2026-09-15-frostroot-v1.md); do not
invent extra product scope.

## Revision history

**2026-09-16, review.** A code review of the finished branch and a further
bug hunt found four more defects, each fixed with a test that fails without
the fix:

| Was | Now | Why |
|---|---|---|
| Default work root `/var/tmp/frostroot` | `/var/tmp/frostroot-<uid>`; the root must be a directory owned by the current user | `/var/tmp` is shared: one `sudo frostroot build` left a root-owned directory that made every later unprivileged build fail with exit 2 |
| Nothing checked about the output location until the tarball was placed | `build` verifies up front that the recipe directory (and `dist/`, if present) is writable | The same `sudo` history leaves a root-owned `dist/`; the failure came after minutes of bootstrapping, as a build error |
| Temporary lock `frostroot.lock.tmp` | `.frostroot.lock.<random>.tmp`, created O_EXCL | Two builds in one directory shared the name; a failing one deleted the other's, which then failed after its tarball had landed |
| Release and arch validated through one call that returned the first error | both reported | `validate` promised every problem and hid the release problem behind the arch problem |
| BOM in the recipe was a parse error naming U+00EF | a leading UTF-8 byte order mark is ignored | Notepad's "UTF-8 with BOM" is a real way for a recipe to be saved |
| Ctrl-C and SIGTERM interrupt a build | SIGHUP too | mmdebstrap now runs in its own process group, so a closed terminal no longer reaches it directly |

**2026-09-16, implementation.** v0.1.0 was implemented from the plan and built,
imported and logged into for all three releases. Real builds exposed four
defects that every unit test had passed; each is fixed with a test that fails
without the fix. The product is unchanged.

| Was | Now | Why |
|---|---|---|
| mmdebstrap `TMPDIR` is the per-build directory, created by `os.MkdirTemp` | `TMPDIR` is `<work>/tmp`, sticky and world-writable; every directory frostroot creates gets an explicit mode regardless of umask; in unshare mode a preflight checks the work root is reachable | In unshare mode mmdebstrap's root is a subordinate uid, "other" to the user's files, and `MkdirTemp` makes 0700 directories: every non-root build failed. mmdebstrap(1) requires a world-writable `TMPDIR` with world-executable ancestors. Home directories are 0750 on 24.04, so a cache under `$HOME` is refused up front |
| Provision hooks: one shell snippet per step, values quoted inside `sh -c '…'` | Go renders one provision script; the hook runs it in the chroot. Values are assigned once, single-quoted, and only expanded in double quotes. The script checks results, not exit codes | Nested quoting was only safe because validation happens to exclude quotes. `locale-gen` exits 0 without generating anything for an unknown locale, and does nothing at all if `/etc/locale.gen` is empty |
| Cross-device `Place` copies to a temp file and chmods it 0644 | the temp file is created with mode 0644 directly; no chmod | chmod returns EPERM on drvfs, the mount WSL uses for Windows drives, which is where `dist/` usually is under WSL |
| On Ctrl-C, SIGINT mmdebstrap | run mmdebstrap in its own process group and SIGINT the group | mmdebstrap's main process answers SIGINT by waiting for its workers; only a group signal, as a terminal sends, stops them. Signalling the main process alone let `kill`/`timeout` stops run the whole bootstrap |
| Lock counts `Status: install ok installed` stanzas | counts stanzas whose status word is `installed` | A held package (`hold ok installed`) is in the image and belongs in a lock of every installed package |
| mmdebstrap/keyring checked by the builder | a bootstrapper preflight runs before any work directory exists; both, and an unusable work root, are exit 1 | Matches this spec's error table; a missing keyring would otherwise have been a build error (2) with an empty work directory left behind |

**2026-09-16.** Revised after the Task 0 spike (results appended to
[`reviews/2026-09-15-frostroot-feasibility.md`](../reviews/2026-09-15-frostroot-feasibility.md#spike-results-task-0-2026-09-16)).
A real build was imported into WSL and logged into. The design held; two facts
did not:

| Was | Now | Why |
|---|---|---|
| 20.04 base URL `http://old-releases.ubuntu.com/ubuntu` | `http://archive.ubuntu.com/ubuntu` | Every focal pocket returns 404 on old-releases. LTS releases under ESM stay on the archive. The EOL warning stays: post-May-2025 security fixes go to Ubuntu Pro, not `focal-security` |
| `wsl.conf` has `[boot]` and `[user]` | adds `[time] useWindowsTimezone=false` | WSL rewrites `/etc/localtime` to the Windows zone at every start unless told not to, so `[locale].timezone` was silently ignored |
| Open question: does `systemd-resolved` fight WSL? | No masking hook | WSL generated `resolv.conf` and DNS worked with `systemd-resolved` active |

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
- Capturing an existing machine (`frostroot capture`) — planned, see Extension points

## Key decisions

| Decision | Choice | Why |
|---|---|---|
| Name | `frostroot` | Freeze a root filesystem; not WSL-specific (bare metal later) |
| Language | Go | Single Linux binary; fits a CLI that orchestrates apt/tar |
| Engine | mmdebstrap → customize hooks → mmdebstrap writes the tarball | No Docker; works on any Linux |
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
7. Ubuntu 20.04 builds (against archive.ubuntu.com, or `--mirror`), and `build` warns that its packages carry known unfixed CVEs.
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

The rendered `/etc/wsl.conf` also carries `[time] useWindowsTimezone=false`.
WSL's default is to rewrite `/etc/localtime` to the Windows zone every time the
distro starts, which would silently override `[locale].timezone`. The recipe
is intent, so the recipe wins.

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

- Parse/validate toml and lock. Unknown fields rejected. A leading UTF-8 byte order mark is ignored; CRLF is TOML.
- Types: `Recipe`, `Lockfile`, `LockPackage`.
- Nothing else reads the raw files.

### `internal/distro`

Ubuntu LTS table. Same package should later grow a `Distro` interface; v1 has
one implementation.

| release | suite  | base URL | EOL |
|---------|--------|----------|-----|
| 20.04   | focal  | `http://archive.ubuntu.com/ubuntu` | yes |
| 22.04   | jammy  | `http://archive.ubuntu.com/ubuntu` | no |
| 24.04   | noble  | `http://archive.ubuntu.com/ubuntu` | no |

Components: `main universe`.

`SourceLines(mirrorURL)` is the **only** place `deb` lines are constructed. It
returns three per release: `<suite>`, `<suite>-updates`, `<suite>-security`.
The shape holds for focal too. Its pockets are still on the archive, because an
LTS release under ESM is not moved to old-releases. Since standard support ended
in May 2025, though, security fixes for it go to Ubuntu Pro rather than
`focal-security`, which is what the `EndOfLife` flag warns about. When a release does
move to old-releases, this table changes; until then `--mirror` covers it.

Unknown release → validation error.

Interface to aim at (even if only Ubuntu exists):

```text
Lookup(version, arch)        → Release { Suite, ArchiveURL, Components, EndOfLife }
(Release) SourceLines(mirrorURL) → [3]string
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

BootstrapSpec { Suite, SourceLines, Include, CustomizeHooks, TarballPath, WorkDir, Arch, InstallRecommends, KeyringPath }
```

`ctx` is on the interface from the start so Ctrl-C works without a later
signature change across every fake.

An optional `Preflighter` (`Preflight(spec) error`) runs before any work
directory exists. The mmdebstrap implementation checks `mmdebstrap` on `PATH`,
the keyring, and in unshare mode that the work root is reachable from the user
namespace (every existing ancestor world-executable).

`CustomizeHooks` are generated by the builder, in order: `upload` the rendered wsl.conf,
`upload` the sudoers drop-in, run the rendered provision script inside the
chroot, `download` the dpkg status. The provision script sets the timezone,
creates the user, enables sudo, generates the locale and removes host
artifacts. Everything fails closed: no `|| true`, and where a tool reports
success without doing the work (`locale-gen`) the script checks the result.
Every recipe value is assigned once, single-quoted, and only ever expanded in
double quotes, even though `validate` already checked it. Host paths in hooks
are single-quoted too; mmdebstrap splits special hooks with `shellwords`.

The package list comes from parsing the downloaded `/var/lib/dpkg/status`, not
from a host `dpkg-query`. A stanza counts when its status word is `installed`
(`install ok installed`, `hold ok installed`); removed packages that left
config files behind do not.

There is no production `WriteProvisionFiles` helper. Tests that need files on
disk write them themselves.

### `internal/export`

Names the artifact and moves it into place. **It does not tar anything.**

```text
TarballRelPath(imageName, release, arch) → dist/<name>-ubuntu-<release>-<arch>.tar.gz
Place(sourcePath, destinationPath)       → atomic where possible, copy+fsync+rename across devices
```

`Place` never chmods: the cross-device temporary file is created with its final
mode, because chmod fails on drvfs.

Later disk/ISO exporters implement the same “artifact in → artifact out” idea.

## Build pipeline

1. Validate the recipe (fail closed).
2. Resolve release → suite + base URL + three pocket lines (`--mirror` overrides the base URL in all three). Warn if the release is EOL.
3. Check Linux + `mmdebstrap` + the Ubuntu archive keyring, and (unshare mode) that the work root is reachable from mmdebstrap's user namespace. Check that the recipe directory, and `dist/` if it exists, can be written: a `sudo` build in the past leaves them root-owned, and that should fail now, not after the bootstrap.
4. Choose a work root: `$XDG_CACHE_HOME/frostroot`, else `/var/tmp/frostroot-<uid>` (per user, because `/var/tmp` is shared); never under `/mnt`. It must be a directory owned by the current user. Create a per-build directory inside it, mode 0755 whatever the umask. Delete on success unless `--keep-work`. Keep on failure and print the path.
5. Render `/etc/wsl.conf`, the sudoers drop-in and the provision script into `<work>/stage/` (0755, files 0644) and generate the hook list.
6. Run mmdebstrap with `TMPDIR=<work>/tmp` (sticky, world-writable, as mmdebstrap(1) requires in unshare mode), in its own process group:
   - suite, three `deb` lines, components `main universe`, `--architectures=amd64`
   - `--variant=important`, `--aptopt='Apt::Install-Recommends "true"'`
   - explicit `--keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg`
   - `--include` = recipe `packages.include` **plus** provision essentials: `systemd`, `systemd-sysv`, `dbus`, `sudo`, `locales`, `tzdata`, `passwd`, `ca-certificates`
   - target is `<work>/image.tar.gz`, so mmdebstrap tars from inside the namespace
   - mode `unshare` unless uid 0, then `root`
   - stderr streamed to the terminal; last 4 KiB kept for the error message
7. Provision via `--customize-hook` (not a separate chroot tool):
   - `upload` the rendered wsl.conf and sudoers drop-in
   - run the provision script in the chroot, which:
     - verifies `/usr/share/zoneinfo/<tz>` exists, then sets localtime and `/etc/timezone`
     - `useradd --create-home --shell /bin/bash --user-group <name>`
     - `chmod 0440` the sudoers drop-in and checks it with `visudo -c`
     - generates the locale unless present, fails if it still is not, then `update-locale`
     - removes the host `/etc/resolv.conf` and `/etc/hostname` that mmdebstrap copies in
   - `download /var/lib/dpkg/status` to the work directory, last
   - mmdebstrap's default cleanup already empties machine-id and removes apt lists and cache; do not duplicate it
8. Parse the downloaded dpkg status → write a uniquely named `.frostroot.lock.*.tmp` (`requested` = recipe include; `[[packages]]` = every installed package, sorted).
9. Move `<work>/image.tar.gz` to `dist/<name>-ubuntu-<release>-amd64.tar.gz`, then rename the lock into place — so a failed move never leaves a lock describing an image that does not exist.
10. Print the `wsl --import` line.

v1 does **not** re-install from lock versions (`pkg=version`). A second `build`
hits current mirrors and may drift. The distributed golden image is the tarball.
Feeding lock versions back into apt is part of the vendoring/rebuild-strict
follow-up.

## Error handling

| Class | Exit | Examples |
|-------|------|----------|
| User error | 1 | not Linux; missing/invalid toml; invalid `--mirror` (not http or https); `init` without `--force` when file exists; mmdebstrap not on PATH; keyring missing; work root under `/mnt`, unreachable from the user namespace, or owned by someone else; recipe directory or `dist/` not writable |
| Build error | 2 | no userns and not root; mmdebstrap failed (unknown package, mirror down); provision hook failed (timezone or locale missing from the image); disk full; tarball missing |
| Interrupted | 130 | Ctrl-C, SIGTERM, SIGHUP |

Rules:

- Do not write lock or tarball unless the whole build succeeded.
- Write the lock to a uniquely named `*.tmp` and rename only after the tarball lands. Builds never touch another build's temporary files; concurrent builds in one directory are otherwise last-writer-wins and not supported.
- On failure: delete tmp artifacts; keep the work directory; print its path; reprint the tail of mmdebstrap stderr when mmdebstrap failed.
- On Ctrl-C: treat as failure (keep work directory, remove tmp artifacts). Signal mmdebstrap's process group with SIGINT, as a terminal would, and wait however long it takes, never SIGKILL: in root mode it has proc, sys and dev mounted inside the chroot. A second Ctrl-C stops frostroot waiting; mmdebstrap's cleanup carries on.
- frostroot never deletes a chroot directory. Under this design it never creates one it would have to.
- Mirror failure: name the URL and suggest `--mirror`.
- v1 **build** requires network. v1 **consume** (the tarball) does not.

## Testing

Default `go test ./...`: offline, no root, no mmdebstrap, on Linux. Windows is
not a test target — the CLI is Linux and Windows users run it inside WSL.

**Always-on unit tests**

- Recipe parse/validate tables: good file, unknown release, bad user, bad package token, bad locale, bad timezone, unknown field, missing sections, empty name.
- Distro table: 20.04 → focal + archive + EOL; 22.04/24.04 → archive, not EOL; unknown → error; `SourceLines` yields three pockets and honours `--mirror`.
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

**Capturing an existing machine (`frostroot capture`)**
Point frostroot at a running Ubuntu system and have it write a
`frostroot.toml` describing what is installed, so an environment built by hand
over a semester can be adopted without retyping it. This is the main adoption
path: most users do not start from a blank recipe, they start from a machine
that already works.

It is a *recipe writer*, exactly like `init` — same shape as the TUI above. It
emits a recipe (and optionally a lock, read straight from the local
`/var/lib/dpkg/status` with the parser `build` already uses); output then flows
through the existing `validate` and `build`. No new artifact type, no new trust
boundary, no root required.

Two constraints on the design, both non-negotiable:

1. **It must report what it could not see.** `capture` reads apt and nothing
   else. Packages installed via pip, npm, cargo, `curl | sh`, `make install`
   or unpacked into `/opt` are invisible, as is all configuration — dotfiles,
   `/etc` edits, enabled services, cron. PPAs are visible in
   `sources.list.d` but have nowhere to go in a v1 recipe. A `capture` that
   silently emits an incomplete recipe is worse than no `capture` at all,
   because the user believes their machine is captured. The command's
   deliverable is the recipe **and** a report of the gaps.
2. **It must not tar the live root filesystem.** That is a different product
   and it is rejected, not deferred. It abandons the recipe-and-lock model for
   an opaque blob; tarring a live root is unsound (inconsistent snapshot,
   pseudo-filesystem and bind-mount exclusions, machine-id and SSH host keys
   duplicated on every import); and above all it exfiltrates secrets. A live
   machine holds SSH private keys, cloud credentials, `.env` files, shell
   history, kubeconfig and real password hashes. A teacher capturing their
   laptop and handing the result to a class would distribute all of it in one
   command, and no exclusion list is ever reliably complete. Capturing a
   package list cannot do this; capturing a filesystem always can.

Naming: `capture`, not `freeze`. Freezing is what `build` already does — the
project's whole metaphor — and reusing the word for introspection muddles the
vocabulary.

Pairs well with vendoring: capture a machine's packages, vendor the `.deb`s,
then rebuild the same environment offline.

## Repo layout

New git repository at `frostroot/` (sibling of other projects, not nested in them).

```
frostroot/
  cmd/frostroot/          # main
  internal/cli/           # app.go, init.go, validate.go, build.go, prompt.go
  internal/recipe/        # recipe.go, validate.go, lock.go
  internal/distro/        # ubuntu.go
  internal/builder/       # builder.go, bootstrap.go, provision.go, status.go, workdir.go
  internal/export/        # export.go
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

None. The one open question was **how a mmdebstrap-built rootfs with systemd
behaves on first boot under WSL**, and the Task 0 spike settled it on
2026-09-16:

- `systemd-resolved` does not conflict with WSL's generated
  `/etc/resolv.conf`. DNS works, so no masking hook is added.
- `systemctl is-system-running` reports `degraded`, never `offline`. The only
  failed unit on 24.04 is `getty@tty1` (WSL has no tty1); on 20.04 it is
  `ua-auto-attach` (Ubuntu Pro auto-attach). Neither affects login, sudo, DNS or
  apt, so neither is masked.
- WSL overrides the timezone unless `wsl.conf` says otherwise; see `Files`.

Anything else not in this spec is out of scope until a new spec says otherwise.
