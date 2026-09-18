# frostroot

**Freeze an Ubuntu root filesystem into a recipe, a lockfile, and a golden image you can hand to anyone.**

> **Status: v0.8.0.** `init`, `edit`, `capture`, `validate`, `build`,
> `vendor` and `build --offline` work, a recipe can add third-party apt
> sources (PPAs, Docker, Node.js, VS Code...), Python packages from PyPI and
> certificate authorities for a network that inspects TLS, and two offline
> rebuilds of one lock produce the same bytes. In a terminal,
> `init`, `edit` and `capture` are a full-screen form driven with the arrow
> keys; `build` and `vendor` are a progress screen with bars. Every path in
> this README was run for real: images for Ubuntu 20.04, 22.04 and 24.04 were
> built with `frostroot build`, imported with `wsl --import` on Windows 11,
> and logged into; a lock was vendored and rebuilt offline twice, to one
> `sha256sum`; and a 24.04 image with `requests` and `numpy` imported both
> from its own environment on the first login. See
> [Verification](#verification).

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
with exact versions, the checksum of every `.deb` file, and the instant the
image is frozen at. You asked for three packages; installing them pulled in
several hundred, and the lock records all of them, marking the ones apt
chose for you. Commit it to git and you can see exactly what changed between
builds.

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
frostroot_version = '0.6.0'
requested = [
  'git',
  'build-essential',
  'cmake'
]
source_date_epoch = 1789662022

[[packages]]
name = 'binutils'
version = '2.38-4ubuntu2.12'
arch = 'amd64'
auto = true
sha256 = '7da8d527d9a4ba9b6fea5fe6126a98f81f538db3a4aaa4ced32d2a26761a4707'
size = 3184
filename = 'pool/main/b/binutils/binutils_2.38-4ubuntu2.12_amd64.deb'

[[packages]]
name = 'git'
version = '1:2.34.1-1ubuntu1.17'
arch = 'amd64'
sha256 = '8794fcf2c4606c445df0db3dc963c8fb852772208bfb12727a12717c03767af7'
size = 3173622
filename = 'pool/main/g/git/git_2.34.1-1ubuntu1.17_amd64.deb'
```

That is an excerpt of a real lock: this recipe produced 341 `[[packages]]`
entries, 123 of them marked `auto` (pulled in by `build-essential`, as
`binutils` was), and a 222 MB tarball. `source_date_epoch` is the instant
the image is frozen at, 2026-09-17 16:20:22 UTC here.

The recipe is **intent** and you edit it. The lock is **fact** and the build
writes it. Versions never appear in the recipe.

The lock is also what makes a rebuild exact. `frostroot vendor` downloads
every file it names into `vendor/debs/`, checked against those checksums, and
`frostroot build --offline` rebuilds the image from that directory alone: no
archive, no network, and it fails rather than produce an image whose packages
differ from the lock by one version. Two such rebuilds, on any day, on any
machine with the same mmdebstrap, produce the same bytes. See
[Rebuilding offline](#rebuilding-offline).

## Install

frostroot is a **Linux** program. On Windows, run it inside WSL. The images it
builds are imported into WSL too, but they do not have to be built there.

From a [release](https://github.com/alone141/frostroot/releases): download
`frostroot-linux-amd64` and `SHA256SUMS`, then

```sh
sha256sum -c SHA256SUMS
install -m 0755 frostroot-linux-amd64 ~/.local/bin/frostroot
frostroot version
sudo apt install mmdebstrap        # also pulls uidmap
```

The binary is static and runs on any distribution, including Ubuntu 20.04.
Or from source, with Go 1.24 or newer:

```sh
go build -o frostroot ./cmd/frostroot
```

On a Debian host, also `sudo apt install ubuntu-keyring`.

Building needs either **user namespaces** (normal on current distributions,
and what you get when you run frostroot as yourself) or **root** (`sudo
frostroot build`). It needs network access; consuming the tarball does not.

## Quick start

```sh
mkdir cpp-lab && cd cpp-lab
frostroot init
```

`init` opens a form. Five pages, each a few questions: the image name and
release; the user name and whether it gets passwordless sudo; the timezone
(type `ist` to filter the list down to `Europe/Istanbul`), locale and whether
the image boots with systemd; the packages, picked with Space from a catalog
grouped by category (C/C++, Python, editors, tools...), plus a line for any
other apt package names; then third-party apt sources, picked from a catalog
(deadsnakes, git-core, Docker, NodeSource, GitHub CLI, Kitware, LLVM, VS
Code), plus a line for other PPAs as `owner/name`. Enter moves on, Shift-Tab
goes back, Ctrl-C leaves without writing. A summary page shows the recipe
before it is written. You type an image name, a user name and, if you want,
extra package names or PPAs; everything else is a choice. The signing keys
of the sources you picked are fetched, checked against pinned fingerprints
and saved under `keys/` when the recipe is written.

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

To be able to rebuild this exact image later, run `frostroot vendor` now,
while the archive still has every file the lock names; see
[Rebuilding offline](#rebuilding-offline).

Without a terminal (a pipe, CI, a redirected log) or with `--plain`, `init`
and `edit` ask the same questions one line at a time, and `build` and
`vendor` print one line per phase and one at every tenth of a measured phase.

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
| `frostroot init [--force] [--plain]` | Opens the form and writes a commented `frostroot.toml`, then fetches the signing keys of the sources you picked into `keys/`. Refuses to overwrite a recipe without `--force`. Writes nothing unless the answers validate and you confirm. |
| `frostroot edit [--plain]` | Opens the existing `frostroot.toml` in the same form, with its values preselected, and writes it back; fetches any missing source keys. The file is regenerated from the template, so your own comments in it do not survive. |
| `frostroot capture [--root DIR] [--force] [--plain]` | Describes an installed Ubuntu system (this one, or one mounted at `DIR`) as a recipe: opens the form with what apt, the source files and the configuration say, writes `frostroot.toml` and the signing keys of the third-party sources it could carry, and writes `frostroot-capture.md`, a report of everything a recipe cannot carry. Copies nothing but those public keys; needs no root. |
| `frostroot validate` | Checks `frostroot.toml`, including that every source's key file is there and is a key, and prints every problem. No network, no root. |
| `frostroot build [--mirror URL] [--ca-bundle FILE] [--keep-work] [--plain]` | Recipe to `frostroot.lock` plus `dist/<name>-ubuntu-<release>-amd64.tar.gz`. A recipe with `[python]` also gets a virtual environment at `/opt/frostroot/venv`. Never prompts. Overwrites the previous lock and tarball. |
| `frostroot vendor [--mirror URL] [--ca-bundle FILE] [--prune] [--plain]` | Downloads every package `frostroot.lock` names into `vendor/debs/`, and every wheel it names into `vendor/wheels/`, checked against the lock's checksums. Keeps what is already there and correct, so rerunning resumes. `--prune` removes files the lock does not name. |
| `frostroot build --offline [--keep-work] [--plain]` | Rebuilds the image from `frostroot.lock` and `vendor/debs/`, without the archive. Fails unless the result has exactly the lock's packages. The lock is read, not written. |
| `frostroot version` | Prints the version and the commit it was built from. |

All of them work on the recipe in the current directory. `--plain` asks for
the line interface even in a terminal. `--mirror` replaces
`http://archive.ubuntu.com/ubuntu` in all three pockets, for a local or faster
mirror; for `vendor` it replaces the mirror recorded in the lock.
`--keep-work` keeps the work directory after a successful build (it is always
kept after a failure). `--ca-bundle` names a PEM file of certificate
authorities to trust while fetching, for a network that inspects TLS; see
[Networks that inspect TLS](#networks-that-inspect-tls).

| Exit code | Meaning |
|---|---|
| 0 | success |
| 1 | something you can fix: invalid or missing recipe, not Linux, mmdebstrap or ubuntu-keyring missing, unusable work directory, a `dist/` you cannot write to; for `vendor` and `--offline`, no lock, a lock without checksums, a recipe that changed since the lock, an incomplete `vendor/debs/` |
| 2 | the build failed: mmdebstrap failed (unknown package, mirror unreachable), provisioning failed, the tarball could not be placed, an offline build's packages differ from the lock; for `vendor`, a download failed or a file did not match its checksum |
| 130 | interrupted with Ctrl-C; nothing is written and the work directory is kept; `vendor` keeps finished downloads |

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
| `python.include` | PyPI names only; see [Python packages](#python-packages) | none |
| `[[sources]]` | extra apt repositories; see [Third-party sources](#third-party-sources) | none |
| `certificates.include` | PEM files beside the recipe; see [Networks that inspect TLS](#networks-that-inspect-tls) | none |

Unknown fields are an error, so a `[package]` typo fails loudly instead of
building an image without your packages. `locale` and `timezone` are checked
strictly because they reach shell scripts. Whether the timezone and locale
actually exist can only be checked inside the image, so `build` fails if they
do not. Files saved by Windows editors are fine: CRLF line endings and a UTF-8
byte order mark are both accepted.

## Third-party sources

Most labs need something the Ubuntu archive does not have: Python 3.12 on
22.04, a current git, Docker, Node.js, VS Code. A recipe can name apt
repositories besides the archive:

```toml
[[sources]]
name = "docker"
url = "https://download.docker.com/linux/ubuntu"
components = ["stable"]      # default ["main"]
key = "keys/docker.asc"      # the repository's OpenPGP public key, next to the recipe

[[sources]]
name = "ppa-deadsnakes-ppa"
url = "https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu"
key = "keys/ppa-deadsnakes-ppa.asc"
```

`suite` defaults to the release's code name (`noble`), which is what PPAs
and most vendors use. Every source has a suite, components and a key: flat
repositories and unsigned sources are not supported, and there is no
`key_url` that `build` fetches blindly.

The form offers a catalog (deadsnakes, git-core, Docker, NodeSource, GitHub
CLI, Kitware, LLVM, VS Code) whose key fingerprints are pinned in frostroot,
and takes PPAs as `owner/name`, whose fingerprints come from Launchpad's
API. When the recipe is written the keys are fetched, checked against those
fingerprints, and saved as `keys/<name>.asc`; a key that does not match is
refused. For any other repository, save its public key yourself at the path
the recipe names, and commit the `keys/` directory with the recipe.

`build` installs from the archive and the sources together, each source
verified by its own key and nothing else. The lock records which source
every package came from and the checksum of every key file
(`[[repositories]]`), `vendor` fetches each package from its own source (with
Launchpad as the fallback for PPAs), and `build --offline` rebuilds from the
pool as before, refusing if the recipe's sources no longer match the lock's.
In the image, the sources are in `/etc/apt/sources.list` with their keys
under `/etc/apt/keyrings/`, so `apt update` there works with the same trust.

HTTPS sources are fetched by apt on the build host, so they need the host's
CA certificates; see the note on networks that inspect TLS.

The package catalog `init` offers is a convenience, not a recipe feature: the
recipe holds plain apt names, whether they came from the catalog or were
typed. The catalog lives in `internal/form/catalog.go`; adding a package is
adding a line, and an integration test checks that every entry exists in all
three releases.

## Python packages

apt has `python3-numpy`, but not the version a course pins, and nothing from
PyPI that Ubuntu does not package. A recipe can name PyPI packages beside its
apt ones:

```toml
[packages]
include = ["python3", "git"]

[python]
include = ["numpy", "pandas", "jupyterlab", "requests"]
```

`build` creates one virtual environment in the image, at
`/opt/frostroot/venv`, installs those packages into it, and puts it on `PATH`
for every login shell, so `python` in the image is the environment's python
and `import numpy` works without activating anything. Ubuntu 24.04 refuses
`pip install` outside a virtual environment (PEP 668); this is the
environment, made once, at build time.

The lock records every wheel that ended up inside, the way `[[packages]]`
records every `.deb`:

```toml
[python]
requested = ['numpy', 'pandas', 'jupyterlab', 'requests']
venv = '/opt/frostroot/venv'
interpreter = '3.12.3'
pip_version = '24.3.1'

[[pypi]]
name = 'numpy'
version = '2.5.3'
sha256 = 'b7e18c623bb5c95acb3b3328861272816ba199fb531921c5d6d0b675f1fde9e3'
filename = 'numpy-2.5.3-cp312-cp312-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl'
url = 'https://files.pythonhosted.org/packages/65/af/aa78d1a88805456e212b65461354cd943197fb9acecc4c90fd12295123a3/numpy-2.5.3-cp312-cp312-manylinux_2_27_x86_64.manylinux_2_28_x86_64.whl'
```

Those four names pulled in 93 entries on 24.04: the four themselves, the 88
packages they depend on, and the pip below. Everything the recipe did not
ask for carries `auto = true`, as dependencies do on the apt side. `vendor`
downloads every one of those wheels into `vendor/wheels/`, checked against
these checksums, and `build --offline` reinstalls exactly them.

Three rules, each for a reason:

- **Names in the recipe, versions in the lock.** `numpy==2.5.3` in a recipe
  is an error that says so. The recipe is what you want; the lock is what you
  got.
- **Wheels only.** A package with no wheel for the image's Python would be
  compiled during the build, which needs a compiler in the image and produces
  files no second build can reproduce. The build fails instead, naming the
  package.
- **One pinned pip does the work.** 22.04 ships pip 22.0.2 and 20.04 pip
  20.0.2, and neither can say what it installed, which is where the lock's
  checksums come from. So frostroot installs one pinned pip (24.3.1, by
  checksum) into the environment first, on every release. It is part of the
  environment, so the lock records it and `vendor` fetches it like any other
  wheel.

Which versions you get still depends on the release's Python: 24.04 resolves
`numpy` to 2.5.3 for Python 3.12, 22.04 to 2.2.6 for 3.10, and 20.04 to
1.24.4 for 3.8, since that is the last one with wheels for it. The lock says
which.

An environment of `numpy`, `pandas` and `jupyterlab` adds about 370 MB to the
image and about 75 MB to `vendor/`.

## Networks that inspect TLS

Many corporate networks terminate TLS at a proxy and re-sign every response
with their own certificate authority. Nothing that verifies against the
public roots alone can talk to anything, and that is two separate problems:
the build has to fetch, and the image has to work afterwards.

Name the authority in the recipe and both are solved:

```toml
[certificates]
include = ["certs/corp-root.pem"]
```

The file sits beside `frostroot.toml`, like a source's signing key, and holds
one or more PEM certificates. `build` installs each one into the image under
`/usr/local/share/ca-certificates/` and runs `update-ca-certificates`, so
`git clone https://…`, `curl` and `pip` work inside the imported
distribution. It also trusts them while building: `pip` is given `--cert`,
`apt` is given `Acquire::https::CaInfo` for a recipe's HTTPS sources, and
frostroot's own downloads verify against the host's roots **plus** yours, so
a network that inspects only some hosts still verifies the rest.

The lock records each file's SHA-256, and an offline rebuild refuses a
certificate that changed since the lock was written.

To trust an authority while building without shipping it in the image:

```bash
frostroot build --ca-bundle /etc/ssl/corp/proxy-root.pem
```

That file never reaches the image or the lock, so a build with it produces
the same bytes as a build without it. `vendor` takes the same flag.

**This costs no integrity.** Wheels install under `--require-hashes` against
the lock's checksums and `.deb` files are checked against the archive's
indexes, so a proxy that altered a byte fails the build. The certificate buys
transport, not trust in what arrives.

If you do not have the proxy's certificate as a file, your browser or your IT
department has it; `openssl s_client -showcerts -connect pypi.org:443
</dev/null` prints the chain the proxy presents, and the last certificate in
it is the root to save.

## Capturing a machine you already have

Most labs start from a machine that works, not from a blank recipe. Run
`frostroot capture` on it (inside the WSL distribution, or on the server)
and it reads what apt installed and how the machine is set up, opens the
form with those values, and writes the recipe.

It recovers what you asked for, not the dependency closure: apt remembers
which packages were installed automatically, and `capture` drops those, the
base system and frostroot's own essentials. The user comes from
`/etc/wsl.conf` or the first ordinary account, sudo from the `sudo` group,
timezone and locale from `/etc/timezone` and `/etc/default/locale`, the
image name from the hostname. Third-party apt sources that have a signing
key (`signed-by` in a `.list` file, `Signed-By` in a `.sources` file, as a
key file or inline) become `[[sources]]` entries, with their keys saved
under `keys/`; sources without one, and flat repositories, are reported.

Two rules, both deliberate:

- **It reports what it could not see.** apt is the only thing a recipe
  understands. Software from pip, npm, cargo, `curl | sh` or `make install`,
  anything in `/opt` or `/usr/local`, edits to `/etc`, hand-written systemd
  units, cron jobs, other users, dotfiles: none of that fits in a recipe, so
  `frostroot-capture.md` lists each area with what was found and what to do
  about it. A capture that stayed quiet about these would leave you believing
  the machine was captured when it was not.
- **It copies nothing but apt signing keys, which are public.** A live
  machine holds SSH keys, tokens, `.env` files and shell history; an image
  built from a tarball of it would hand all of that to every student.
  `capture` reads package metadata and a few configuration files, lists the
  names of the entries in your home directory and nothing more, and needs no
  root. The report marks `.ssh`, `.gnupg`, `.aws`, `.kube` and `.docker` as
  secrets that must never be copied.

`--root DIR` captures another root filesystem, such as a mounted disk.

## Rebuilding offline

The Ubuntu archive moves. The `-updates` and `-security` pockets change
weekly, and superseded packages leave the pool soon after, so a lock older
than a few weeks names packages the archive no longer serves. If the frozen
image is the tarball, the frozen *recipe for the tarball* is the lock plus
the packages it names, and that is what `vendor` keeps:

```console
$ frostroot vendor
Vendored 341 packages (173 MB) into vendor/debs: 341 downloaded.

$ frostroot build --offline
Wrote dist/cpp-lab-ubuntu-22.04-amd64.tar.gz (222 MB), rebuilt from frostroot.lock: 341 packages, every one as locked.
Frozen at 2026-09-17 16:20:22 UTC: every offline build of this lock produces this tarball, byte for byte.
```

`vendor` reads the lock, checks what `vendor/debs/` already holds, and
downloads the rest from the mirror the lock records, four files at a time,
each verified by size and SHA-256 before it gets its final name. A package
the archive has dropped is fetched from Launchpad's librarian, which keeps
every file ever published to Ubuntu; the checksum decides, not the source.
Rerunning after an interruption resumes; a corrupt file is replaced.

`build --offline` refuses to start unless the lock still describes the
recipe (same release, architecture and `include` list; the user, sudo,
locale, timezone and systemd may change freely) and `vendor/debs/` holds
every locked file intact. It then hands mmdebstrap a local, trusted
repository of exactly those files, installs every one of them, and compares
the result with the lock. A difference is a failed build (exit 2) with the
packages named; nothing is placed. No archive is contacted and no keyring is
needed: the checksum check against the lock is the trust.

**Byte for byte.** Every image is frozen at one instant: the second the
online build started, or `SOURCE_DATE_EPOCH` if that is set, as the
reproducible-builds convention has it. The lock records it as
`source_date_epoch`, and every offline build of that lock freezes at the
same instant. mmdebstrap dates no file later than it, sorts the tarball's
entries, writes a gzip header without a timestamp and drops the files that
would carry the build time (dpkg and apt logs, `machine-id`); the image's
`/etc/shadow` is dated by it too. So two offline builds of one lock, days or
years apart, produce one `sha256sum`, and you can check a tarball someone
hands you by rebuilding it. Offline, `SOURCE_DATE_EPOCH` in the environment
is ignored, with a note, because the lock is the input; a lock written by
frostroot 0.5 or earlier records no instant, and `build --offline` says so
and freezes at its own start until you build online once more.

The online build itself is not byte-identical with its offline rebuild:
mmdebstrap installs the essential packages first online and everything in
one pass offline, which leaves the paragraph order of apt's
`extended_states`, the line order of `/var/lib/dpkg/triggers/File` and, on
24.04, one directory timestamp different; the files, their contents and
their owners are otherwise the same. Nor does the promise cross mmdebstrap
or dpkg versions, or packages that generate random material when installed
(an SSH host key, say; do not ship one in a golden image anyway).

The online build also records which packages apt installed on its own as
`auto = true` in the lock, and the offline build restores those marks, so
`apt autoremove` and `frostroot capture` see the same image either way.

A recipe with `[python]` vendors its wheels the same way, into
`vendor/wheels/`, each checked against the lock and fetched from PyPI, which
never drops a file it has published. The offline build hands that directory
to pip with every wheel pinned to its checksum, and compares the environment
with the lock afterwards, as it does the packages.

`vendor/` is a few hundred megabytes to a gigabyte. Ship it beside the
tarball or in an archive; add it to `.gitignore` unless you use git LFS.

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
- With `[certificates]`: each authority under
  `/usr/local/share/ca-certificates/` and merged into
  `/etc/ssl/certs/ca-certificates.crt`. Nothing `--ca-bundle` named is in the
  image.
- With `[python]`: a virtual environment at `/opt/frostroot/venv`, root-owned
  and readable by everyone, with your packages and a pinned pip in it, and
  one line in `/etc/profile.d` that puts it on `PATH`.
- No `/etc/resolv.conf` or `/etc/hostname` from the build machine, an empty
  `/etc/machine-id` so every import gets its own, and no home directory but
  your user's.

## Pipeline

```
frostroot.toml ──▶ validate ──▶ mmdebstrap ──▶ image.tar.gz ──▶ dist/*.tar.gz
                                     │              │
                                customize      dpkg status ──▶ frostroot.lock
                                  hooks
```

`mmdebstrap` bootstraps the base system and writes the tarball itself, from
inside its user namespace, which is the only place ownership, symlinks,
hardlinks and file capabilities come out correct, with `SOURCE_DATE_EPOCH`
in its environment. Customize hooks upload the rendered `wsl.conf` and
sudoers drop-in and run one generated provisioning script (user, sudo,
locale, timezone, cleanup), copy out the apt indexes the chroot verified
(the lock's checksums come from them), download apt's `extended_states`
(the lock's `auto` marks come from it), then download the image's dpkg
status, which frostroot parses into the lock. A recipe with `[python]` gets
one more hook in between, which creates the environment and installs into
it, and whose report becomes the lock's `[[pypi]]` entries; the build host's
`/etc/resolv.conf` is removed after it, since that hook is the one that
needs to resolve a name. The tarball is moved into `dist/` first and the
lock renamed into place second, so a lock never describes an image that does
not exist. A failed build writes neither.

Offline, the three `deb http://…` lines become one
`deb [trusted=yes] copy://<work>/pool ./` pointing at a flat repository
frostroot writes in the work directory from the vendored files (their own
control files, plus a `Release` naming the suite, which mmdebstrap needs to
find the essential set), every locked package goes into `--include`, and
hooks restore the archive's lines in the image's `sources.list` and the
lock's `auto` marks in its `extended_states`. The Python step works the same
way: the wheels are copied in, pip installs from that directory with every
checksum pinned and no index to reach, and the environment is compared with
the lock before anything is placed.

Work happens in `$XDG_CACHE_HOME/frostroot` if that is set, otherwise in
`/var/tmp/frostroot-<uid>` (one per user, so a `sudo` build cannot leave a
root-owned directory in the way of the next normal one), never under `/mnt`
(on WSL that is a slow 9p mount of a Windows drive). The recipe directory
itself can be on a Windows drive. Before spending minutes on a bootstrap,
`build` checks that it will be able to write the lock and the tarball.

## Notes

**Two ways to freeze.** The tarball is the golden image: hand it out and
everyone gets the same machine. The lock plus `vendor/debs/` is the frozen
build: `build --offline` produces that machine again, with a different user
name or timezone if you like, in a year, without the archive. A plain
`build` next month fetches whatever the archive holds then, and the new lock
shows exactly what moved.

**Online builds need network; consuming the tarball and offline builds do
not.**

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
fail by design. Name the packages in the recipe's `[python]` table and the
image has one; for anything installed after import, `python3 -m venv .venv`.

**`systemctl is-system-running` says `degraded`, not `running`.** On 24.04 the
failed units are gettys (`getty@tty1`, sometimes `console-getty`), because WSL
has no console to attach them to. On 22.04 and 20.04 it is `ua-auto-attach`
(Ubuntu Pro auto-attach, for cloud instances), and on the very first start of
a 20.04 image `user@1000.service` can also fail once. None of them affects
login, sudo, networking or apt.

**Networks that inspect TLS.** An image trusts the public certificate
authorities from `ca-certificates` plus whatever `[certificates]` names; see
[Networks that inspect TLS](#networks-that-inspect-tls). The Ubuntu archive
is fetched over plain HTTP with signed metadata, so it is never affected.
`init` fetches signing keys with the host's own certificate store, so on such
a network it needs the authority installed on the build host.

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

**Since then:** the arrow-key form and progress screen (v0.2), `frostroot
capture` (v0.3), vendoring and offline rebuilds with a release process
(v0.4), third-party apt sources (v0.5), byte-identical offline rebuilds
(v0.6), Python packages from PyPI (v0.7).

**Deliberately not yet:** Fedora or any non-Ubuntu family · flat or unsigned
apt repositories · npm and cargo lockfiles · Python source distributions ·
bare-metal disk or ISO images · a native Windows binary · architectures other
than amd64.

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
real mmdebstrap run, the form's field table and its recipe round trip, the
two screens driven key by key, `.deb` reading for every compression Ubuntu
has used, the flat repository writer, the vendor pool against a local HTTP
server (fresh, resumed, corrupt, dropped from the mirror, mismatched,
interrupted), offline builds against the fake bootstrapper, including every
refusal and the final comparison with the lock, and the frozen instant: taken
from the environment or the clock online, from the lock offline, refused
when unusable, with apt's marks parsed and rendered in its own format. For
the Python step it covers pip's own installation report parsed into lock
entries, every refusal (a source distribution, a missing checksum, a report
version frostroot does not know, a requested package the report never
mentions), the two rendered scripts through a real shell with a hostile
package name, the hook order online and offline, and the comparison of an
image's environment with the lock.

```sh
go test -tags=integration -run TestIntegration -v -timeout 30m ./...
```

builds a real 24.04 image with mmdebstrap and inspects the tarball: thousands
of symlinks all with targets, hardlinks and file capabilities intact, no
subordinate-uid owners, the user, sudoers, `wsl.conf`, timezone and locale in
place, and no leaked host files; it also checks that the build reported every
phase in order with a real download total, and that every package in the
`init` catalog exists in all three releases. A second test builds an image
online, vendors it, rebuilds it offline twice and requires one SHA-256, no
entry dated after the lock's instant, and the online image's apt marks in
the offline one. A third does the same for a recipe with Python packages: it
checks the lock's `[python]` table and `[[pypi]]` entries, that the
environment and its `profile.d` line are in the tarball, that nothing the
Python step used was left in the image, and that two offline rebuilds have
one SHA-256. They need Linux, mmdebstrap, ubuntu-keyring, network, and user
namespaces or root, and take about twenty minutes.

**The WSL boot check is manual**, because no CI runner can run `wsl --import`.
For every release you ship, import the tarball and check: `whoami` is your
user, `sudo -n id` needs no password, `systemctl is-system-running` is
`running` or `degraded`, `getent hosts archive.ubuntu.com` resolves, `locale`
has no warnings, and `date` shows the recipe's timezone. For v0.1.0 this was
done on Windows 11 with WSL 2.6.3 for all three releases, along with the
failure paths; the results are recorded in the
[feasibility analysis](docs/superpowers/reviews/2026-09-15-frostroot-feasibility.md#release-verification-v010-2026-09-16).

For v0.7.0 a 24.04 image with `requests` and `numpy` was imported the same
way: every check above passed, `systemctl is-system-running` said `running`,
and in a login shell `python3` was the environment's
(`/opt/frostroot/venv/bin/python3`, 3.12.3), `import numpy, requests` gave
the locked 2.5.3 and 2.34.2, and `pip --version` was the pinned 24.3.1. The
[Python spec](docs/superpowers/specs/2026-09-17-frostroot-python.md#verification)
records it.

## Documentation

| Document | What it is |
|---|---|
| [Design spec](docs/superpowers/specs/2026-09-14-frostroot-design.md) | Source of truth for v1: recipe, lock, tarball, build pipeline. |
| [TUI spec](docs/superpowers/specs/2026-09-17-frostroot-tui.md) | v0.2: the form, the build screen, the progress parser, the plain fallback. |
| [Capture spec](docs/superpowers/specs/2026-09-17-frostroot-capture.md) | v0.3: reading an installed machine, the report of the gaps. |
| [Vendor spec](docs/superpowers/specs/2026-09-17-frostroot-vendor.md) | v0.4: checksums in the lock, `vendor`, `build --offline`, releases; with the spike results. |
| [Sources spec](docs/superpowers/specs/2026-09-17-frostroot-sources.md) | v0.5: `[[sources]]`, the catalog and PPAs, keys, how `build`, the lock, `vendor` and `capture` handle them. |
| [Reproducible spec](docs/superpowers/specs/2026-09-17-frostroot-reproducible.md) | v0.6: the frozen instant in the lock, apt's auto marks, what byte identity does and does not cover; with the spike and the verification. |
| [Python spec](docs/superpowers/specs/2026-09-17-frostroot-python.md) | v0.7: `[python]`, the image's virtual environment, the pinned pip, `[[pypi]]` in the lock, wheels in `vendor/`; with the spike and the verification. |
| [Python plan](docs/superpowers/plans/2026-09-17-frostroot-python.md) | The nine tasks v0.7 was built from. |
| [Certificates spec](docs/superpowers/specs/2026-09-18-frostroot-certificates.md) | v0.8: `[certificates]`, `--ca-bundle`, what a TLS inspection proxy breaks and where the trust is applied; with the spike and the verification. |
| [Certificates plan](docs/superpowers/plans/2026-09-18-frostroot-certificates.md) | The ten tasks v0.8 was built from. |
| [Implementation plan](docs/superpowers/plans/2026-09-15-frostroot-v1.md) | The 13 tasks v1 was built from, with the spike's amendments. |
| [TUI plan](docs/superpowers/plans/2026-09-17-frostroot-tui.md) | The seven tasks v0.2 was built from. |
| [Plan review](docs/superpowers/reviews/2026-09-14-frostroot-plan-review.md) | Found four defects that would have shipped a non-booting image |
| [Feasibility analysis](docs/superpowers/reviews/2026-09-15-frostroot-feasibility.md) | Independent assessment, plus the recorded spike and WSL boot results |
| [Original plan](docs/superpowers/plans/2026-09-14-frostroot.md) | **Superseded.** Kept for history. |

## Layout

```
cmd/frostroot/      main
internal/cli/       init, edit, capture, validate, build, vendor, version; flags, exit codes, the plain line interface
internal/capture/   reading an installed system: packages asked for, user, locale, and the report of the gaps
internal/form/      the questions as data: fields, package catalog, timezones, locales, recipe mapping
internal/sources/   the catalog of third-party repositories, PPAs, and fetching and checking their keys
internal/pgp/       OpenPGP public keys: armor, the primary key's fingerprint; nothing else
internal/tui/       the full-screen form and progress screen (the only package using the Charm libraries)
internal/recipe/    frostroot.toml and frostroot.lock: types, strict parsing, validation
internal/distro/    Ubuntu releases, archive URL, the three pocket lines
internal/builder/   orchestration, mmdebstrap runner and progress parser, provisioning, the Python step, dpkg status, lock checksums, offline builds
internal/pool/      the vendored pools: manifests from the lock, verify, fetch, prune, stage as a flat repository or a directory of wheels
internal/deb/       Debian formats: control stanzas, Packages indexes, .deb control files, flat repository index
internal/export/    tarball naming and atomic placement
testdata/           recipe fixtures
```

## License

[MIT](LICENSE). This covers frostroot itself; the packages it bootstraps into
an image carry their own licences from the Ubuntu archive.
