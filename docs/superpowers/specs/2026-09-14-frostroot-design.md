# frostroot Design

Date: 2026-09-14
Status: approved for spec review
Repo: `frostroot/` (new project, Linux CLI)

This spec is the source of truth for v1. An implementation plan should be written from it; do not invent extra product scope.

## Goal

frostroot freezes an Ubuntu LTS root filesystem. The user describes a distro release, a sudo user, and apt packages in a recipe. frostroot produces:

1. A **lockfile** with every installed apt package version.
2. A **rootfs tarball** that is a golden image (`wsl --import` today; other exporters later).

The tarball is what you hand to a lab or an airgapped machine. The recipe and lock are what you git and review. Rebuild-from-lock (exact `pkg=version` / vendoring) is a follow-up, not v1.

Primary audiences for v1: **golden images** (classroom/lab) and **offline consumption** of those images. Same-machine restore and teammate sharing are supported by the same artifacts.

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
| Engine | mmdebstrap → chroot provision → tar | No Docker; works on any Linux; 20.04 via old-releases |
| Distros v1 | Ubuntu 20.04, 22.04, 24.04 amd64 | 20.04 is off standard support; that is the pinning story |
| Packages v1 | apt names only | One locker; PPAs and language locks come later |
| Source of truth | `frostroot.toml` | `init` writes it; `build` never prompts |
| Reproducibility | Practical | Tarball is the golden image. Rebuild from recipe is best-effort against current mirrors. Vendoring is next. |
| Image profile | Minimal WSL-ready | sudo user, `/etc/wsl.conf` systemd, locale/timezone, passwordless sudo |
| Where it runs | Linux CLI | Including WSL. Windows users run frostroot inside WSL. |

## Success criteria (v1 done)

On a Linux host with `mmdebstrap` installed (user namespaces or root):

1. `frostroot init` writes a valid `frostroot.toml`.
2. `frostroot build` for Ubuntu 24.04 with a few packages produces `frostroot.lock` and `dist/<name>-ubuntu-24.04-amd64.tar.gz`.
3. The lock lists **every** installed dpkg (not only the requested names) with exact versions.
4. The tarball contains `/etc/wsl.conf` with systemd on and the default user, plus that user's home and passwordless sudo.
5. `wsl --import` of that tarball on Windows boots and logs in as that user (manual check; not in default tests).
6. Ubuntu 20.04 builds against old-releases (or `--mirror`), not archive.ubuntu.com.
7. `go test ./...` passes offline, without root, without mmdebstrap.

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
- Does not write a lock or tarball.
- Does not need root.

### `validate`

- Parse and check `frostroot.toml`.
- Known LTS, `arch == amd64`.
- Image name: `^[a-zA-Z0-9][a-zA-Z0-9._-]*$` (safe tarball filename; non-empty).
- User name: `^[a-z_][a-z0-9_-]*$`, length 1–32, not `root`.
- `[wsl].default_user`, if set, must equal `[user].name`. If omitted, it is `[user].name`.
- Package tokens match `^[a-z0-9][a-z0-9+.-]+$`. Empty `include` is allowed (base + provision essentials only).
- Print every problem; exit 0 or 1.
- No network, no mmdebstrap.

### `build`

- Runs `validate` first.
- Flags:
  - `--mirror URL` — override the default archive for this release
  - `--keep-rootfs` — leave the work directory after success
- Overwrites existing lock and matching tarball without asking.
- Prints the WSL import example on success:

```
wsl --import <image.name> <install-dir> dist/<image.name>-ubuntu-<release>-amd64.tar.gz
```

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

v1 user model: **passwordless sudo** when `[user].sudo` is true; if false, the user exists with no sudo. No password field. WSL login uses `[wsl].default_user` (defaults to `[user].name`). This is a lab image, not a hardened server.

### `frostroot.lock` (fact — build written)

```toml
version = 1
distro = "ubuntu"
release = "22.04"
suite = "jammy"
arch = "amd64"
mirror = "http://archive.ubuntu.com/ubuntu"
frostroot_version = "0.1.0"

requested = ["git", "build-essential", "cmake"]

[[packages]]
name = "git"
version = "1:2.34.1-1ubuntu1.11"
```

- `requested` is the recipe `packages.include` list as written (not provision essentials).
- `[[packages]]` is **every** installed package (dependencies and essentials included).
- No `sha256` / `filename` in v1. Keep this list-of-tables shape so vendoring can add those fields later.
- If `include` changed since the last lock, `build` overwrites the lock.

### Tarball

Path: `dist/<image.name>-ubuntu-<release>-<arch>.tar.gz`

This is the golden image. Airgap **run** = import this file. Airgap **build** is not a v1 promise.

## Architecture

One Go module, one binary. Five internal packages plus `cmd`.

```
frostroot.toml --> recipe --> builder --> workdir rootfs --> frostroot.lock
                                   |
                                   +--> export --> dist/*.tar.gz
```

### `cmd/frostroot`

`main` only. Wires CLI.

### `internal/cli`

`init` / `validate` / `build`. `init` is the only command that prompts. `build` never prompts.

Prompting goes through a `Prompt` interface so tests inject scripted answers (no real TTY).

### `internal/recipe`

- Parse/validate toml and lock.
- Types: `Recipe`, `Lockfile`, `LockPackage`.
- Nothing else reads the raw files.

### `internal/distro`

Ubuntu LTS table. Same package should later grow a `Distro` interface; v1 has one implementation.

| release | suite  | default mirror |
|---------|--------|----------------|
| 20.04   | focal  | `http://old-releases.ubuntu.com/ubuntu` |
| 22.04   | jammy  | `http://archive.ubuntu.com/ubuntu` |
| 24.04   | noble  | `http://archive.ubuntu.com/ubuntu` |

Components: `main universe`.

Unknown release → validation error.

Interface to aim at (even if only Ubuntu exists):

```text
BootstrapInfo(release, arch) → { suite, mirror, components }
```

### `internal/builder`

Orchestrates one build. Host requirements: Linux, `mmdebstrap` on `PATH`, and either:

- user namespaces (`mmdebstrap --mode=unshare`), or
- uid 0 (`--mode=root`)

If `mmdebstrap` is missing, fail with the host install hint (`sudo apt install mmdebstrap`), not a stack trace.

`Bootstrapper` interface (real impl shells out to mmdebstrap with `--customize-hook`; tests fake it):

```text
Run(suite, mirror, include []string, hooks []string, destDir string) error
```

`hooks` are shell snippets generated by the builder (locale, useradd, sudoers, wsl.conf, cleanup). The fake bootstrapper does not run them; tests that need files on disk call a `WriteProvisionFiles(rootfs, recipe)` helper (wsl.conf, sudoers.d, home) without executing useradd.

### `internal/export`

Rootfs directory → `.tar.gz`. `--numeric-owner`. Do not pack `/proc`, `/sys`, `/dev` junk.

Later disk/ISO exporters implement the same “directory in → artifact out” idea. v1 has only tar.

## Build pipeline

1. Validate the recipe (fail closed).
2. Resolve release → suite + mirror (`--mirror` overrides).
3. Check Linux + `mmdebstrap`.
4. Create a work directory with `os.MkdirTemp("", "frostroot-*")`. Delete on success unless `--keep-rootfs`. Keep on failure and print the path.
5. mmdebstrap into `workdir/rootfs`:
   - suite, mirror, components `main universe`
   - `--include` = recipe `packages.include` **plus** provision essentials: `sudo`, `locales`, `tzdata`, `passwd`
   - mode `unshare` if userns works, else `root` if uid 0, else error explaining both options
6. Provision via mmdebstrap `--customize-hook` (not a separate chroot tool):
   - locale from `[locale].lang`, timezone from `[locale].timezone`
   - user `[user].name`, home, passwordless sudo if `[user].sudo`
   - `/etc/wsl.conf`: `[boot] systemd=true` when `[wsl].systemd`, `[user] default=<default_user>`
   - drop obvious dirt: machine-id content, apt list caches (not for bit-reproducibility; just a clean lab image)
7. Query packages with `dpkg-query --root <rootfs> -W -f '${Package}\t${Version}\n'` → write `frostroot.lock` (`requested` = recipe include; `[[packages]]` = every dpkg).
8. Tar rootfs to `dist/<name>-ubuntu-<release>-amd64.tar.gz` via a `.tmp` file, then rename.
9. Print the `wsl --import` line.

v1 does **not** re-install from lock versions (`pkg=version`). A second `build` hits current mirrors and may drift. The distributed golden image is the tarball. Feeding lock versions back into apt is part of the vendoring/rebuild-strict follow-up.

## Error handling

| Class | Exit | Examples |
|-------|------|----------|
| User error | 1 | not Linux; missing/invalid toml; `init` without `--force` when file exists; mmdebstrap not on PATH |
| Build error | 2 | no userns and not root; mmdebstrap failed (unknown package, mirror down); provision failed; disk full; tar failed |
| Interrupted | 130 | Ctrl-C |

Rules:

- Do not write lock or tarball unless the whole build succeeded.
- Write lock and tarball to `*.tmp`, then rename.
- On failure: delete tmp artifacts; keep workdir; print its path; reprint the tail of mmdebstrap stderr when mmdebstrap failed.
- On Ctrl-C: treat as failure (keep workdir, remove tmp artifacts).
- Mirror failure: name the URL and suggest `--mirror`.
- v1 **build** requires network. v1 **consume** (the tarball) does not.

## Testing

Default `go test ./...`: offline, no root, no mmdebstrap.

**Always-on unit tests**

- Recipe parse/validate tables: good file, unknown release, bad user, bad package token, missing sections, empty name.
- Distro table: 20.04 → focal + old-releases; 22.04/24.04 → archive; unknown → error.
- Lock round-trip encode/decode; `requested` vs full `[[packages]]`.
- Tarball file name for a sample image.
- `validate` CLI against `testdata/` fixtures (exit 0 vs 1).
- `init` with a fake `Prompt` writes the expected toml.

**Builder tests with a fake `Bootstrapper`**

- Fake records args (including hooks) and plants stub dpkg data the locker reads via `dpkg-query --root` **or** a test double of the query function.
- Assert include list (user packages + essentials) is passed through.
- Assert `/etc/wsl.conf` and sudoers exist after provision.
- Assert lock is produced from stub data.
- Assert tarball path is created.
- On fake failure: no lock, no tarball, workdir kept.

**Integration (`-tags=integration`)**

- Skip if not Linux or mmdebstrap missing.
- Build a tiny 24.04 image (`include = ["bash"]` plus essentials).
- Assert tarball exists, lock contains `bash` with a version, tar contains `/etc/wsl.conf` and the user home.
- Needs network + userns or root. Not part of the default test loop.

Do not automate `wsl --import` in v1 tests. Document it as a manual check.

## Extension points (later, required)

These must remain possible without rewriting `recipe` / `cli` / `export`.

**Vendoring `.deb`s (first follow-up after v1)**  
Add `sha256` and `filename` on `[[packages]]`. New command `frostroot vendor` fills `vendor/debs/`. `build --offline` uses that pool. Lock shape is already a list of tables for this.

**Extra apt sources**  
Recipe `[sources]` (PPA, deadsnakes, nodesource). Builder registers them before install. Lock records url, suite, signed-by. Still apt, still one locker.

**Language lockfiles**  
Optional `[python]`, `[node]`, `[rust]` in the recipe; separate lock arrays (`[[pypi]]`, …). Run after apt provision. Never mix with `[[packages]]`.

**Fedora / other families**  
New `internal/distro` implementation: `dnf --installroot` + `rpm -qa`. Recipe gains `distro = "fedora"`. CLI verbs stay the same.

**Bare metal**  
New exporter: rootfs directory → disk/ISO. Builder unchanged.

**TUI**  
Writes `frostroot.toml` only. `build` stays non-interactive.

## Repo layout

New git repository at `frostroot/` (sibling of other projects, not nested in them).

```
frostroot/
  cmd/frostroot/          # main
  internal/cli/
  internal/recipe/
  internal/distro/
  internal/builder/
  internal/export/
  testdata/               # recipe fixtures
  docs/superpowers/specs/ # this document
  docs/superpowers/plans/ # implementation plan (next)
  README.md
  go.mod
```

Module path: `frostroot` for v1 unless/until published (`github.com/<user>/frostroot`).

## Open questions

None remaining for v1. Anything not in this spec is out of scope until a new spec says otherwise.
