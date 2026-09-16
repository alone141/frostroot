# frostroot

**Freeze an Ubuntu root filesystem into a recipe, a lockfile, and a golden image you can hand to anyone.**

> **Status: v0.2.0.** `init`, `edit`, `validate` and `build` work. In a
> terminal, `init` and `edit` are a full-screen form driven with the arrow
> keys, and `build` is a progress screen with bars. Every path in this README
> was run for real: images for Ubuntu 20.04, 22.04 and 24.04 were built with
> `frostroot build`, imported with `wsl --import` on Windows 11, and logged
> into. See [Verification](#verification).

---

## The problem

You are setting up a programming lab for thirty students. Everyone needs the
same compiler, the same tools, the same versions. "Run these apt commands"
does not work: a student installing today gets different package versions than
one who installed last month, and some lab machines have no internet at all.

frostroot builds the environment **once**, freezes it, and gives you a file.
Everyone imports that same file and gets an identical machine.

## How it works

You write about fifteen lines of TOML, or let `frostroot init` write them:

```toml
[image]
name = "cpp-lab"
release = "22.04"     # 20.04 | 22.04 | 24.04
arch = "amd64"

[user]
name = "student"
sudo = true

[wsl]
systemd = true
default_user = "student"

[locale]
lang = "en_US.UTF-8"
timezone = "Europe/Istanbul"

[packages]
include = ["git", "build-essential", "cmake"]
```

Run `frostroot build`, and you get two things:

**`dist/cpp-lab-ubuntu-22.04-amd64.tar.gz`**: the frozen machine, a few
hundred megabytes. This is what you hand out. On Windows:

```powershell
wsl --import cpp-lab C:\wsl\cpp-lab dist\cpp-lab-ubuntu-22.04-amd64.tar.gz
wsl -d cpp-lab
```

No internet needed on the receiving end.

**`frostroot.lock`**: a receipt listing every package that ended up inside,
with exact versions. You asked for three packages; installing them pulled in
several hundred, and the lock records all of them. Commit it to git and you
can see exactly what changed between builds.

```toml
version = 1
distro = 'ubuntu'
release = '22.04'
suite = 'jammy'
arch = 'amd64'
mirror = 'http://archive.ubuntu.com/ubuntu'
sources = [
  'deb http://archive.ubuntu.com/ubuntu jammy main universe',
  'deb http://archive.ubuntu.com/ubuntu jammy-updates main universe',
  'deb http://archive.ubuntu.com/ubuntu jammy-security main universe'
]
frostroot_version = '0.1.0'
requested = [
  'git',
  'build-essential',
  'cmake'
]

[[packages]]
name = 'git'
version = '1:2.34.1-1ubuntu1.17'
arch = 'amd64'
```

That is an excerpt of a real lock: this recipe produced 341 `[[packages]]`
entries and a 222 MB tarball.

The recipe is **intent** and you edit it. The lock is **fact** and the build
writes it. Versions never appear in the recipe.

## Install

frostroot is a **Linux** program. On Windows, run it inside WSL. The images it
builds are imported into WSL too, but they do not have to be built there.

```sh
sudo apt install mmdebstrap        # also pulls uidmap
go build -o frostroot ./cmd/frostroot
```

Go 1.24 or newer. On a Debian host, also `sudo apt install ubuntu-keyring`.

Building needs either **user namespaces** (normal on current distributions,
and what you get when you run frostroot as yourself) or **root** (`sudo
frostroot build`). It needs network access; consuming the tarball does not.

## Quick start

```sh
mkdir cpp-lab && cd cpp-lab
frostroot init
```

`init` opens a form. Four pages, each a few questions: the image name and
release; the user name and whether it gets passwordless sudo; the timezone
(type `ist` to filter the list down to `Europe/Istanbul`), locale and whether
the image boots with systemd; then the packages, picked with Space from a
catalog grouped by category (C/C++, Python, editors, tools...), plus a line
for any other apt package names. Enter moves on, Shift-Tab goes back, Ctrl-C
leaves without writing. A summary page shows the recipe before it is
written. You type an image name, a user name and, if you want, extra
package names; everything else is a choice.

```console
$ frostroot validate
frostroot.toml: ok (cpp-lab, Ubuntu 22.04 amd64, 3 packages requested)

$ frostroot build
```

`build` shows its phases as a checklist: a spinner and elapsed time while a
phase runs, a percentage bar where the work can be measured (the downloads,
in bytes; the installs, in dpkg steps), a check when it is done. The last
lines of mmdebstrap's own output scroll in a pane below (`l` grows it), and
the complete output is written to `mmdebstrap.log` in the work directory.
When the build finishes the screen closes and the summary stays in the
terminal:

```console
Wrote dist/cpp-lab-ubuntu-22.04-amd64.tar.gz (222 MB)
Wrote frostroot.lock (341 packages)

Import it on Windows:
  wsl --import cpp-lab <install-dir> dist/cpp-lab-ubuntu-22.04-amd64.tar.gz
or from any directory in PowerShell:
  wsl --import cpp-lab <install-dir> '\\wsl.localhost\Ubuntu\home\you\cpp-lab\dist\cpp-lab-ubuntu-22.04-amd64.tar.gz'
```

A build takes a few minutes and downloads a few hundred megabytes: about two
minutes for a minimal 24.04 image, and four and a half for this one on a
2 MB/s connection. On WSL, `build` also prints the tarball's Windows path, so
the import line can be pasted into PowerShell from any directory.

Without a terminal (a pipe, CI, a redirected log) or with `--plain`, `init`
and `edit` ask the same questions one line at a time, and `build` prints one
line per phase and one at every tenth of a measured phase.

Then, in PowerShell:

```powershell
wsl --import cpp-lab C:\wsl\cpp-lab '\\wsl.localhost\Ubuntu\home\you\cpp-lab\dist\cpp-lab-ubuntu-22.04-amd64.tar.gz'
wsl -d cpp-lab
```

You are logged in as `student`, with passwordless `sudo`, systemd running, and
`apt install` working against the same three pockets the image was built from.

## Commands

| Command | What it does |
|---|---|
| `frostroot init [--force] [--plain]` | Opens the form and writes a commented `frostroot.toml`. Refuses to overwrite one without `--force`. Writes nothing unless the answers validate and you confirm. |
| `frostroot edit [--plain]` | Opens the existing `frostroot.toml` in the same form, with its values preselected, and writes it back. The file is regenerated from the template, so your own comments in it do not survive. |
| `frostroot validate` | Checks `frostroot.toml` and prints every problem. No network, no root. |
| `frostroot build [--mirror URL] [--keep-work] [--plain]` | Recipe to `frostroot.lock` plus `dist/<name>-ubuntu-<release>-amd64.tar.gz`. Never prompts. Overwrites the previous lock and tarball. |

All four work on the recipe in the current directory. `--plain` asks for the
line interface even in a terminal. `--mirror` replaces
`http://archive.ubuntu.com/ubuntu` in all three pockets, for a local or faster
mirror. `--keep-work` keeps the work directory after a successful build (it is
always kept after a failure).

| Exit code | Meaning |
|---|---|
| 0 | success |
| 1 | something you can fix: invalid or missing recipe, not Linux, mmdebstrap or ubuntu-keyring missing, unusable work directory, a `dist/` you cannot write to |
| 2 | the build failed: mmdebstrap failed (unknown package, mirror unreachable), provisioning failed, the tarball could not be placed |
| 130 | interrupted with Ctrl-C; nothing is written and the work directory is kept |

## The recipe

| Field | Rules | `init` default |
|---|---|---|
| `image.name` | letters, digits, `.` `_` `-`; names the tarball and the WSL distro | `lab` |
| `image.release` | `20.04`, `22.04` or `24.04` | `24.04` |
| `image.arch` | `amd64` | `amd64` |
| `user.name` | lowercase, digits, `_` `-`, 1 to 32 characters, not `root` | `student` |
| `user.sudo` | `true` gives passwordless sudo; `false` gives none | `true` |
| `wsl.systemd` | boot with systemd | `true` |
| `wsl.default_user` | must equal `user.name`; may be omitted | the user name |
| `locale.lang` | e.g. `en_US.UTF-8`, `tr_TR.UTF-8`, `C.UTF-8` | `en_US.UTF-8` |
| `locale.timezone` | e.g. `UTC`, `Europe/Istanbul`, `America/Argentina/Buenos_Aires` | `UTC` |
| `packages.include` | apt package names only; no versions, no suites | preset plus extras |

Unknown fields are an error, so a `[package]` typo fails loudly instead of
building an image without your packages. `locale` and `timezone` are checked
strictly because they reach shell scripts. Whether the timezone and locale
actually exist can only be checked inside the image, so `build` fails if they
do not. Files saved by Windows editors are fine: CRLF line endings and a UTF-8
byte order mark are both accepted.

The package catalog `init` offers is a convenience, not a recipe feature: the
recipe holds plain apt names, whether they came from the catalog or were
typed. The catalog lives in `internal/form/catalog.go`; adding a package is
adding a line, and an integration test checks that every entry exists in all
three releases.

## What is in the image

- Ubuntu `--variant=important` plus your packages, with **Recommends on**, so
  `include` behaves like `apt install` on stock Ubuntu.
- Always: `systemd`, `systemd-sysv`, `dbus`, `sudo`, `locales`, `tzdata`,
  `passwd`, `ca-certificates`.
- Packages from the release, `-updates` and `-security` pockets: patched
  versions, not release-day ones. The same three lines are in
  `/etc/apt/sources.list`.
- Your user with a home directory and bash; passwordless sudo through
  `/etc/sudoers.d/90-frostroot`.
- `/etc/wsl.conf` with systemd on, your user as the default, and
  `useWindowsTimezone=false` so the recipe's timezone sticks (WSL otherwise
  resets it to the Windows zone at every start).
- Your locale and timezone.
- No `/etc/resolv.conf` or `/etc/hostname` from the build machine, and an empty
  `/etc/machine-id`, so every import gets its own.

## Pipeline

```
frostroot.toml ──▶ validate ──▶ mmdebstrap ──▶ image.tar.gz ──▶ dist/*.tar.gz
                                     │              │
                                customize      dpkg status ──▶ frostroot.lock
                                  hooks
```

`mmdebstrap` bootstraps the base system and writes the tarball itself, from
inside its user namespace, which is the only place ownership, symlinks,
hardlinks and file capabilities come out correct. Customize hooks upload the
rendered `wsl.conf` and sudoers drop-in and run one generated provisioning
script (user, sudo, locale, timezone, cleanup), then download the image's dpkg
status, which frostroot parses into the lock. The tarball is moved into
`dist/` first and the lock renamed into place second, so a lock never describes
an image that does not exist. A failed build writes neither.

Work happens in `$XDG_CACHE_HOME/frostroot` if that is set, otherwise in
`/var/tmp/frostroot-<uid>` (one per user, so a `sudo` build cannot leave a
root-owned directory in the way of the next normal one), never under `/mnt`
(on WSL that is a slow 9p mount of a Windows drive). The recipe directory
itself can be on a Windows drive. Before spending minutes on a bootstrap,
`build` checks that it will be able to write the lock and the tarball.

## Notes

**The tarball is the golden image.** v1 does not rebuild from the lock's
versions. Running `build` again next month fetches whatever the Ubuntu archive
holds then, and the new lock shows exactly what moved. The file you hand out is
reproducible; the act of building is not, yet. Vendoring `.deb` files is the
planned next step.

**Builds need network; consuming the tarball does not.**

**Building as root.** `sudo frostroot build` works, but everything it writes
into the recipe directory (`dist/`, `frostroot.lock`) belongs to root
afterwards. A later build as yourself in that directory stops before
bootstrapping and says so; `sudo chown -R $USER dist frostroot.lock` or
removing them fixes it. Its work directory, `/var/tmp/frostroot-0`, stays out
of the way of unprivileged builds.

**One build per recipe directory at a time.** Two builds running in the same
directory do not disturb each other's temporary files, but whichever finishes
last wins, and the lock and tarball may then come from different builds.

**20.04 images ship known, unfixed CVEs.** Focal's standard support ended in
May 2025. Its packages are still on the archive, but security fixes since then
go to Ubuntu Pro, not to `focal-security`, and `build` warns about it. Freezing
an old release is a legitimate use of this tool; prefer 22.04 or 24.04 unless
you specifically need focal.

**Python on 24.04.** PEP 668 makes `pip install` outside a virtual environment
fail by design. Use `python3 -m venv .venv`.

**`systemctl is-system-running` says `degraded`, not `running`.** On 24.04 the
failed units are gettys (`getty@tty1`, sometimes `console-getty`), because WSL
has no console to attach them to. On 22.04 and 20.04 it is `ua-auto-attach`
(Ubuntu Pro auto-attach, for cloud instances), and on the very first start of
a 20.04 image `user@1000.service` can also fail once. None of them affects
login, sudo, networking or apt.

**Networks that inspect TLS.** An image trusts the public certificate
authorities from `ca-certificates`, nothing else. On a network with a TLS
inspection proxy, HTTPS from inside the image (`git clone https://…`) fails
until the organisation's CA certificate is added in the image with
`update-ca-certificates`. `apt` itself uses plain HTTP and signed metadata, so
building is not affected.

**Building from a Windows checkout.** If `go build` inside WSL reports `error
obtaining VCS status`, git is refusing a repository owned by Windows; add
`-buildvcs=false`.

**Redistribution.** A golden image contains Ubuntu binaries. frostroot's own
MIT licence covers frostroot, not the packages it bundles into an image.
Redistributing unmodified archive packages is fine; Canonical's trademark
policy constrains calling a modified image "Ubuntu". Worth a look before
publishing images publicly.

## Scope

**In v1:** Ubuntu 20.04, 22.04 and 24.04, amd64, apt packages by name, one sudo
user, WSL-ready images.

**Deliberately not in v1:** Fedora or any non-Ubuntu family · PPAs and extra
apt sources · pip / npm / cargo lockfiles · vendoring `.deb` files and offline
builds · bit-identical rebuilds · bare-metal disk or ISO images · a package
picker TUI · a native Windows binary · architectures other than amd64 ·
capturing an existing machine (`frostroot capture`, planned for after v1).

Each exclusion has a door left open in the design. Adding Fedora means a new
`internal/distro` implementation, not a rewrite.

## Verification

```sh
go test ./...
```

runs offline, without root, without mmdebstrap and without a terminal. It
covers recipe and lock parsing, the distro table, dpkg status parsing, the
rendered files and hooks (including running the hook text through a real
shell with hostile paths), the build orchestration against a fake
bootstrapper, every exit code, the progress parser against a recording of a
real mmdebstrap run, the form's field table and its recipe round trip, and
the two screens, driven key by key.

```sh
go test -tags=integration -run TestIntegration -v -timeout 30m ./...
```

builds a real 24.04 image with mmdebstrap and inspects the tarball: thousands
of symlinks all with targets, hardlinks and file capabilities intact, no
subordinate-uid owners, the user, sudoers, `wsl.conf`, timezone and locale in
place, and no leaked host files; it also checks that the build reported every
phase in order with a real download total, and that every package in the
`init` catalog exists in all three releases. It needs Linux, mmdebstrap,
ubuntu-keyring, network, and user namespaces or root, and takes about two
minutes.

**The WSL boot check is manual**, because no CI runner can run `wsl --import`.
For every release you ship, import the tarball and check: `whoami` is your
user, `sudo -n id` needs no password, `systemctl is-system-running` is
`running` or `degraded`, `getent hosts archive.ubuntu.com` resolves, `locale`
has no warnings, and `date` shows the recipe's timezone. For v0.1.0 this was
done on Windows 11 with WSL 2.6.3 for all three releases, along with the
failure paths; the results are recorded in the
[feasibility analysis](docs/superpowers/reviews/2026-09-15-frostroot-feasibility.md#release-verification-v010-2026-09-16).

## Documentation

| Document | What it is |
|---|---|
| [Design spec](docs/superpowers/specs/2026-09-14-frostroot-design.md) | Source of truth for v1: recipe, lock, tarball, build pipeline. |
| [TUI spec](docs/superpowers/specs/2026-09-17-frostroot-tui.md) | v0.2: the form, the build screen, the progress parser, the plain fallback. |
| [Implementation plan](docs/superpowers/plans/2026-09-15-frostroot-v1.md) | The 13 tasks v1 was built from, with the spike's amendments. |
| [TUI plan](docs/superpowers/plans/2026-09-17-frostroot-tui.md) | The seven tasks v0.2 was built from. |
| [Plan review](docs/superpowers/reviews/2026-09-14-frostroot-plan-review.md) | Found four defects that would have shipped a non-booting image |
| [Feasibility analysis](docs/superpowers/reviews/2026-09-15-frostroot-feasibility.md) | Independent assessment, plus the recorded spike and WSL boot results |
| [Original plan](docs/superpowers/plans/2026-09-14-frostroot.md) | **Superseded.** Kept for history. |

## Layout

```
cmd/frostroot/      main
internal/cli/       init, edit, validate, build; flags, exit codes, the plain line interface
internal/form/      the questions as data: fields, package catalog, timezones, locales, recipe mapping
internal/tui/       the full-screen form and build screen (the only package using the Charm libraries)
internal/recipe/    frostroot.toml and frostroot.lock: types, strict parsing, validation
internal/distro/    Ubuntu releases, archive URL, the three pocket lines
internal/builder/   orchestration, mmdebstrap runner and progress parser, provisioning, dpkg status, work directory
internal/export/    tarball naming and atomic placement
testdata/           recipe fixtures
```

## License

[MIT](LICENSE). This covers frostroot itself; the packages it bootstraps into
an image carry their own licences from the Ubuntu archive.
