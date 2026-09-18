# frostroot

**Freeze an Ubuntu root filesystem into a recipe, a lockfile, and a golden image you can hand to anyone.**

> **Status: v0.11.0.** `init`, `edit`, `capture`, `validate`, `build`,
> `vendor` and `build --offline` work, a recipe can add third-party apt
> sources (PPAs, Docker, Node.js, VS Code...), Python packages from PyPI and
> certificate authorities for a network that inspects TLS, and two offline
> rebuilds of one lock produce the same bytes. In a terminal,
> `init`, `edit` and `capture` are a full-screen form driven with the arrow
> keys in which any package of the release, and any project on PyPI, is
> found by typing (see [Finding packages](#finding-packages)) and which
> shows the recipe, or the diff, before writing it; `build` and
> `vendor` are a progress screen with bars, and a failed build says which
> line of the log explains it. Every path in
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
the image is frozen at, 2026-09-17 16:20:22 UTC here. (It was written by
v0.6.0; since v0.10 the `sources` lines name all four components, `main
restricted universe multiverse`.)

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

`init` opens a form. Six pages, each a few questions: the image name and
release; the user name and whether it gets passwordless sudo; the timezone
(type `ist` to filter the list down to `Europe/Istanbul`), locale and whether
the image boots with systemd; the packages, picked with Space from a catalog
grouped by category (C/C++, Python, editors, tools...), and any other package
of the release, found by typing into a search over the whole archive, and
Python packages found the same way in PyPI (see
[Finding packages](#finding-packages)); then third-party apt sources, picked from a catalog
(deadsnakes, git-core, Docker, NodeSource, GitHub CLI, Kitware, LLVM, VS
Code), plus a line for other PPAs as `owner/name`; and last the certificate
files the image should trust, for a network that inspects TLS, each checked
to be there and to be a certificate as you type. Enter moves on, Shift-Tab
goes back, Ctrl-C leaves without writing. The last page shows the recipe
exactly as it will be written — or, for `edit`, a diff of the file against
it, with a count and a warning when comments of your own are about to be
replaced by the template's; when nothing would change, the question defaults
to not writing. `capture` opens with a page of what it found and what a
recipe cannot carry. You type an image name, a user name and, if you want,
extra package names or PPAs; everything else is a choice. The signing keys
of the sources you picked are fetched, checked against pinned fingerprints
and saved under `keys/` when the recipe is written.

During `build` and `vendor` the screen is a checklist of phases with a
spinner, a bar or a count for each, the last lines of mmdebstrap's output in
a pane (`l` grows it, the arrow keys scroll it), the download rate and the
time left once ten seconds of downloading have been seen, and a footer with
the elapsed time and where the log is. It draws down to 60 columns without
wrapping, and in ASCII when the locale is not UTF-8 (a `LANG=C` session).
When a build fails, the plain summary that follows the screen prints the
first line of `mmdebstrap.log` that explains it — apt's `E:`, pip's
`ERROR:`, the provision script's `frostroot:` — before the tail of the
output, so a pip traceback or an apt cleanup cannot hide the reason.

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
| `frostroot init [--force] [--plain] [--mirror URL] [--python-index URL] [--ca-bundle FILE] [--refresh-index]` | Opens the form and writes a commented `frostroot.toml`, then fetches the signing keys of the sources you picked into `keys/`. Refuses to overwrite a recipe without `--force`. Writes nothing unless the answers validate and you confirm. |
| `frostroot edit [--plain] [--mirror URL] [--python-index URL] [--ca-bundle FILE] [--refresh-index]` | Opens the existing `frostroot.toml` in the same form, with its values preselected, and writes it back; fetches any missing source keys. The file is regenerated from the template, so your own comments in it do not survive. |
| `frostroot capture [--root DIR] [--force] [--plain] [--mirror URL] [--python-index URL] [--ca-bundle FILE] [--refresh-index]` | Describes an installed Ubuntu system (this one, or one mounted at `DIR`) as a recipe: opens the form with what apt, the source files and the configuration say, starting with a page of what it found and what a recipe cannot carry, writes `frostroot.toml` and the signing keys of the third-party sources it could carry, and writes `frostroot-capture.md`, a report of everything a recipe cannot carry. Copies nothing but those public keys; needs no root. |
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
[Networks that inspect TLS](#networks-that-inspect-tls). For `init`, `edit` and
`capture`, `--mirror` and `--ca-bundle` say where the form's apt index comes
from and whom to trust for it, `--python-index` points the PyPI search at
another simple index, and `--refresh-index` fetches them again before their
week is up.

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

## Finding packages

The catalog on the Packages page is thirty-odd names. "Other packages", under
it, is the rest of the release: type, and the list shows the packages whose
name or one-line description holds what you typed — the exact name first,
then names that start with it, then the others, shorter names before longer.
Space adds the highlighted row or takes it out again, `/` narrows to an
archive section (`devel`, `python`, `libs`...), Esc clears the search, and an
empty search lists what you have chosen. A comma after a name, or a pasted
list, adds names the way the old free-text line took them.

```
┃ Other packages
┃ type to search the release's archive, or a name; Space adds the row
┃ > cmake
┃ 26 found · all sections · noble · 85,574 packages · fetched just now
┃ > [ ] cmake         3.28.3-1build7    cross-platform, open-source make system
┃   [ ] cmake-doc     3.28.3-1build7    extended documentation in various formats…
┃   [ ] cmake-data    3.28.3-1build7    CMake data files (modules, templates and…
┃   [ ] cmake-extras  1.7-2             Extra CMake utility modules
┃ chosen (1): ninja-build
```

**A name the archive does not have is a warning, never a refusal.** What you
typed is offered as the first row whenever it is not an exact match, so Space
after a whole name adds that name and never a neighbor; `docker-ce`, `code`
and `gh` come from third-party sources, which the index knows nothing about.
The last page lists such names with the nearest ones the archive does have
(`ninja-buld  nearest: ninja-build`) and says whether the recipe has a source
that could provide them. The question below it still defaults to Write.

**Where the index comes from.** The archive's own `Packages` files, for the
release pocket and `-updates`, in the four components a build enables: about
21 MB for 24.04, fetched the first time the field is reached, with a progress
line, in three to seven seconds here. It is reduced to names, versions,
sections and one-line descriptions and kept as one 1.7 MB file a release under
`$XDG_CACHE_HOME/frostroot/index/` (or `~/.cache/frostroot/index/`) for a
week; `--refresh-index` fetches it sooner. The recipe directory is never
written to. `--mirror` fetches from another archive, `--ca-bundle` trusts an
authority for an HTTPS one, and `http_proxy` is honored as everywhere else.

**Offline is not an error.** With no cached index and no archive in reach the
field says so and is a list editor: type a name, Space adds it, `build` checks
it. `--plain` never fetches; it asks for the names on one line as before, and
warns about the ones a cached index lacks when there is a cached index.

**The index only suggests.** Each file is checked against the size and
SHA-256 in the pocket's `InRelease`, which catches a cut or half-published
download, but `InRelease` is read without verifying its signature. A hostile
mirror could make the search lie about what exists. It could not make a build
install anything: `build` never reads this index, and apt verifies every
package against Ubuntu's signed archive exactly as before.

### Python packages, which are a different kind of index

"Python packages", under it, searches PyPI the same way, with one honest
difference: **PyPI publishes names and nothing else.** Its complete index is
894,000 project names — no version, no section, no description — so the rows
are bare names, `/` says the index has no sections, and what tells one
`requests-*` from the next is the summary of the row your cursor is resting
on, fetched when it rests there and remembered:

```
┃ Python packages
┃ type to search PyPI, or a name; installed into the image's virtual environment
┃ > requests
┃ 200 of 713 shown, keep typing
┃ > [ ] requests
┃   [ ] requestsH
┃   [ ] requestsaa
┃   [ ] requests-go
┃ Python HTTP for Humans.
┃ chosen: none
```

Getting every summary up front is not on offer: one request a project is
894,000 of them, about 75 hours. One on demand is 4–26 kB and under a fifth
of a second, and it is asked for only after the cursor has been still for a
quarter of a second, so running down the list is one lookup rather than
twenty. Offline, or against an index with no JSON API, the line is simply
absent and the list is names — which is all PyPI gave anyone anyway.

Names are compared the way PEP 503 compares them, so `Flask_SQLAlchemy`,
`flask-sqlalchemy` and `Flask.SQLAlchemy` are one project and none of them
is reported missing. The index is 9.7 MB gzipped, cached as 4.2 MB for a
week. `--python-index URL` searches another PEP 691 simple index instead,
and summaries then come from that host and not from pypi.org: an index of
your own must not mean telling PyPI which names you looked up.

The warning on the last page has a second block when a Python name is not on
PyPI, and it is a warning like the other — a private index, or a project
published this morning, is reason enough for a name to be right and unknown
here:

```
Not on PyPI:
  reqeusts  nearest: requests, reqwests
build will stop when pip cannot resolve them, unless they come from an
index of your own.
```


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

If the recipe directory is also a **Go module**, `vendor/` is the one name
`go build` reads as that module's vendored dependencies: it switches to
vendoring mode and stops with `inconsistent vendoring`, an error that names
`go.mod` rather than the recipe beside it. frostroot's own checkout is such
a directory. `vendor` and `build --offline` say so when they see a `go.mod`;
build that module with `go build -mod=mod`, or keep the recipe in a
directory of its own.

## What is in the image

- Ubuntu `--variant=important` plus your packages, with **Recommends on**, so
  `include` behaves like `apt install` on stock Ubuntu.
- Always: `systemd`, `systemd-sysv`, `dbus`, `sudo`, `locales`, `tzdata`,
  `passwd`, `ca-certificates`.
- Packages from the release, `-updates` and `-security` pockets: patched
  versions, not release-day ones. The same three lines are in
  `/etc/apt/sources.list`.
- All four components, `main restricted universe multiverse`, as a stock
  Ubuntu install and the official WSL image enable them, so that
  `nvidia-cuda-toolkit` or `unrar` is a line in the recipe like any other.
  Up to v0.9 it was `main universe`. A lock made then still rebuilds offline
  to the same bytes: the lock records the image's `deb` lines, and an offline
  build writes those.
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
(v0.6), Python packages from PyPI (v0.7), certificate authorities for
networks that inspect TLS (v0.8), a form that shows the recipe or the diff
before writing, capture's findings first, a Trust page, a failed build that
explains itself, and screens for narrow and non-UTF-8 terminals (v0.9).

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

For v0.8.0 a 24.04 image with `[certificates]` and `requests`, rebuilt
offline, was imported the same way: every check above passed,
`systemctl is-system-running` said `running`, the recipe's authority was at
`/etc/ssl/certs/corp-root.pem` and `openssl verify` accepted it against the
image's bundle, `urllib` (which reads the system store) fetched
`https://pypi.org` through that regenerated bundle, `python3` was the
environment's with `requests` 2.34.2 and pip 24.3.1, and nothing the build
had merely been allowed to trust was left in the image. The
[Certificates spec](docs/superpowers/specs/2026-09-18-frostroot-certificates.md#verification)
records the offline rebuilds that matched byte for byte with and without
`--ca-bundle`, and the one thing not yet proved end to end.

For v0.9.0 the interface was verified two ways. The screens are pinned by
golden frames — the rendered view at 80×24, 120×40 and 60×20, in UTF-8 and
in ASCII, compared by `go test` and recorded again with
`FROSTROOT_UPDATE_FRAMES=1` — and the plain interface, which shares every
line of logic but the drawing, was run for real on the build host: `init`
printed the recipe before asking; `edit` on a file with a hand-written
comment printed "3 lines change; your own comments in the file are replaced
by the template's" with the comment in the diff, and the file written had
lost it; an `edit` that changed nothing defaulted to not writing and touched
nothing; `capture --root /` opened with what it could not carry; and a
build of a recipe naming `ninja-buld` ended with `the first error in
mmdebstrap.log, line 1035: E: Unable to locate package ninja-buld` printed
first — the tail below it, eighty short dpkg lines, held the same line
seventy lines down, which is what the first line saves reading; the case
it exists for, an explanation followed by more output than the tail holds,
is the unit test with three hundred lines of cleanup. What only a person at
a terminal can check — the full-screen form at 60, 80 and 120 columns, the
diff pane, the summary after a failed build's teardown, a `LANG=C` session,
Ctrl-C mid-build — is the checklist in the
[TUI plan](docs/superpowers/plans/2026-09-18-frostroot-tui-2.md#v09-verification);
the frames stand in for it until it is run.

For v0.10.0 the full-screen form was run for real for the first time, by a
driver rather than a person: `frostroot init` on the build host inside a
pseudo-terminal that answers the two questions lipgloss and Bubble Tea ask a
terminal at start (the background color, the cursor position; `script`
answers neither, which is why an earlier attempt drew nothing), with keys
sent when the screen showed what each step waited for, at 100×32 and at
80×24. With an empty cache the picker said it was fetching, showed 22
readings such as `8.95 MB / 21.4 MB`, and had noble's 85,574 packages 3.6 s
after it was reached. `ninja-buld` was offered as typed, added, and marked
`? not in the archive`; `cmake` found 26 packages, thirteen in `devel`; `/`
listed them with counts, and Down and Space picked `cmake-doc`. The last page
said `ninja-buld  nearest: ninja-build` above the recipe, fitted 24 rows, and
wrote the recipe when told to: a warning, not a refusal. A second run found
the index there when the picker appeared. With `--mirror` at a port nothing
listens on and no cache, the field said the index was not available, took
`htop` as typed, and the recipe was written. With `--mirror` at a local copy
of the archive the form made exactly ten requests — two `InRelease`, eight
`Packages.xz` — and the cache file's header named that mirror. `init --plain`
made no request with or without a cache, said nothing without one, and with
one printed the same warning between the summary and the recipe, as did
`edit --plain`; the default archive, being another source, did not use the
mirror's cache. An integration test opens the real index of all three
releases: 3.4 to 6.7 s to fetch and reduce, 7 to 9 ms a search, under 2 ms
for the nearest names, under 90 ms to reopen from the cache.

The components were verified on images. The lock v0.8.0 wrote, which names
`main universe`, was rebuilt offline by this version from the pool vendored
then: `1ff441b7…`, the `sha256sum` v0.8.0 produced, with the two-component
`sources.list` inside. A new recipe asking for `git` and `unrar` built
online in 433 s: `unrar` locked as
`pool/multiverse/u/unrar-nonfree/unrar_7.0.7-1build1_amd64.deb`, the other
258 packages all from `main`, four components in the lock's `sources` and in
the image's `sources.list`. It was then vendored and rebuilt offline twice, to one `sha256sum`
(`2f888fc2…`; the online build differs, as it always has, for the reason
under [Rebuilding offline](#rebuilding-offline)), with `/usr/bin/unrar`
inside. Of everything in noble's `main`
and `universe`, 28 packages recommend something only `restricted` or
`multiverse` provides — blends and games, none in `main`, none in the
catalog, none in any lock built so far — so an image that asks for none of
them installs what it did before. What still only a person can check is how
the picker feels in Windows Terminal; the driver sees what is drawn, not
whether it is pleasant.

For v0.11.0 the Python field was driven against the real PyPI by the same
pseudo-terminal driver, at 100×32. The index arrived as **894,105 projects**
— seventeen more than the spike had counted an hour earlier, which is what a
live index looks like. Typing `requests` found 713 projects and showed the
first two hundred, saying so; resting on the first row put `Python HTTP for
Humans.` under the list, fetched from PyPI at that moment, and moving down
replaced it with the next project's. `/` answered `this index has no
sections`, and the help line offered no `/` at all, because PyPI publishes
none. A misspelled `reqeusts` was added as typed, marked `? not in the
index`, and warned about on the last page as `nearest: requests, reqwests`
— both of which are real projects — and the recipe was written all the
same. `requestsH`, which exists, was not warned about, and neither was
`Flask_SQLAlchemy`, which PEP 503 makes the same project as
`Flask-SQLAlchemy`.

Two things that run against a live index were found this way and not by the
tests. The status line read `all sections · PyPI · 894,105 projects` and the
help line offered `/ section`, for an index that has no sections; both are
now asked of the index rather than assumed. And the summary lookup went to
pypi.org whatever `--python-index` said, so an index of your own would still
have told PyPI which names you looked up; summaries now follow the index
they belong to. That one was caught by a test that resolved `numpy` against
the real PyPI while pointed at a fake one.

A real `build` of a recipe naming `requests` and `Flask_SQLAlchemy` — the
second spelled the way PEP 503 allows rather than the way PyPI spells it —
finished in 425 s and locked 17 `[[pypi]]` entries, among them
`Flask-SQLAlchemy 3.1.1` and `requests`: the normalization the picker and the
warning rely on is the same one pip applies, all the way to the lock.


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
| [Picker spec](docs/superpowers/specs/2026-09-18-frostroot-picker.md) | v0.10: the package picker and the index behind it, all four components in builds; with the spike, the decisions and the verification. |
| [PyPI spec](docs/superpowers/specs/2026-09-18-frostroot-pypi.md) | v0.11: searching PyPI, why its picker cannot look like the apt one, and the summary fetched on demand; with the spike and the verification. |
| [TUI plan, second round](docs/superpowers/plans/2026-09-18-frostroot-tui-2.md) | What a walk through the interface found, the six tasks v0.9 was built from, and the tasks of v0.10's package picker. |
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
internal/form/      the questions as data: fields, package catalog, timezones, locales, recipe mapping, what a package search needs of an index
internal/index/     what the form searches: the archive's Packages files and PyPI's simple index, fetched, checked, reduced, cached and searched, and PyPI summaries one at a time
internal/sources/   the catalog of third-party repositories, PPAs, and fetching and checking their keys
internal/pgp/       OpenPGP public keys: armor, the primary key's fingerprint; nothing else
internal/tui/       the full-screen form with its package picker, and the progress screen (the only package using the Charm libraries)
internal/recipe/    frostroot.toml and frostroot.lock: types, strict parsing, validation
internal/distro/    Ubuntu releases, archive URL, components, the three pocket lines
internal/builder/   orchestration, mmdebstrap runner and progress parser, provisioning, the Python step, dpkg status, lock checksums, offline builds
internal/pool/      the vendored pools: manifests from the lock, verify, fetch, prune, stage as a flat repository or a directory of wheels
internal/deb/       Debian formats: control stanzas, Packages indexes, .deb control files, flat repository index
internal/export/    tarball naming and atomic placement
testdata/           recipe fixtures
```

## License

[MIT](LICENSE). This covers frostroot itself; the packages it bootstraps into
an image carry their own licences from the Ubuntu archive.
