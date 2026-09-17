# frostroot v0.7: Python packages from PyPI

Date: 2026-09-17
Status: approved by the project owner in conversation ("implement" on the plan); the release question was settled by their suggestion to let the environment carry its own pip, rather than shipping 24.04 alone. Implemented on branch `feature/python`
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md) (the extension point "Language lockfiles"), [`2026-09-17-frostroot-vendor.md`](2026-09-17-frostroot-vendor.md) (the lock and the pool) and [`2026-09-17-frostroot-reproducible.md`](2026-09-17-frostroot-reproducible.md) (the frozen instant)
Implements: [issue #7](https://github.com/alone141/frostroot/issues/7), its Python half

## Verification (2026-09-17, build host, mmdebstrap 1.4.3)

- `gofmt`, `go vet` with and without the `integration` tag, `go test -race
  ./...` and `GOOS=windows go build ./...`: clean. `golangci-lint` is not
  installed on this host; CI runs it.
- **The real binary**, on a 24.04 recipe with `git`, `requests` and `numpy`,
  timezone `Europe/Istanbul`. `validate`: "1 package requested, 2 from
  PyPI". `build`: 486 s, a 182 MB tarball, a lock of 262 packages and 7
  `[[pypi]]` entries frozen at 19:21:28 UTC, with `requests 2.34.2` and
  `numpy 2.5.3` asked for and the pinned `pip 24.3.1` marked `auto`. The
  summary named the environment. `vendor`: 262 `.deb` files (96 MB) and 7
  wheels (19 MB).
- **Three offline rebuilds**, two ordinary (409 s and 410 s) and one in a
  network namespace whose only interface was a down loopback: all three
  produced `b3db16294ca15898ab7fe66723577ca23872780578096cb10fb48a4364bfba1e`,
  and the lock was not rewritten. The offline image holds 24,318 entries,
  2,710 of them the environment, with the `profile.d` line and `requests` in
  it and nothing the step used left behind.
- **`wsl --import` on Windows 11** of the online image, the check the README
  calls manual: `whoami` was `student`, `sudo -n id` needed no password,
  `systemctl is-system-running` said `running`, `getent hosts
  archive.ubuntu.com` resolved, `locale` printed no warnings and `date`
  showed `+03`. In a login shell `python3` was
  `/opt/frostroot/venv/bin/python3` (3.12.3), `import numpy, requests` gave
  2.5.3 and 2.34.2, and `pip --version` was 24.3.1. A non-login shell gets
  `/usr/bin/python3`, as designed; `sudo -i` gets the environment's.
- **Two defects only a real build could find**, both fixed with the
  verification rerun:
  - The provision script deleted `/etc/resolv.conf`, the file mmdebstrap
    copies from the build host, before the Python step ran. pip then could
    not resolve `files.pythonhosted.org` and every image with `[python]`
    failed. The host's files are removed in a hook of their own now, after
    everything that needs to resolve a name.
  - A hook inherits the build user's environment, so pip wrote its cache to
    `$HOME/.cache` inside the chroot: the image carried 126 entries of a
    `/home/<builder>` directory, 19 MB, under a user the image does not
    have. The step sets `HOME` to root's and asks pip for no cache; the
    integration test now fails on any home directory that is not the
    recipe's user. The rebuilt image holds `home/` and `home/student` and
    nothing else, no cache anywhere, and is 163 MB rather than 182 MB.
- **What was run where.** The whole pipeline — recipe, build, lock, vendor,
  offline rebuild, import — was run on 24.04. On 22.04 and 20.04 the step's
  own rendered scripts were run in mmdebstrap chroots of those releases,
  which is what the releases differ in; the Go around them is the same code
  on all three.

## Why

A programming lab is the project's own example, and the Python half of it
does not fit in `[packages]`. apt has `python3-numpy`, but not the version a
course pins, and nothing from PyPI that Ubuntu does not package: no
`jupyterlab`, no `polars`, no `ruff`. So the recipe stops short of the
machine the students need, and the missing part is filled in by hand after
import, which is the drift frostroot exists to remove: every student gets
whatever PyPI served that afternoon, and only if the lab has internet.

Ubuntu 24.04 sharpens it. PEP 668 marks the system Python as externally
managed, so `pip install` outside a virtual environment fails by design. The
README's answer until now was a note telling people to make one themselves.

`capture` sees the same gap from the other side: it finds pip packages on a
machine (`pip (system): requests 2.32.3`) and can only report them, because a
recipe has nowhere to put them.

## Goal

- A recipe names PyPI packages in `[python]`, as it names apt packages in
  `[packages]`: names only, no versions.
- `build` puts them in one virtual environment in the image, on `PATH` for
  every login shell, and records in the lock every wheel that ended up
  there, with its version, checksum and URL.
- `vendor` fills `vendor/wheels/` from those entries; `build --offline`
  reinstalls exactly them, from that directory, with no network, and fails
  if the environment comes out different from the lock.
- Two offline rebuilds of one lock stay byte-identical, as v0.6 promises for
  the apt side.
- What a recipe resolves to depends on the recipe, PyPI and the release's
  Python, not on how old the release's pip is.

## Non-goals

- **Source distributions.** A package with no wheel for the image's Python
  would be compiled during the build: a compiler in the image, minutes of
  build time, and output no second build reproduces. `--only-binary=:all:`
  is passed, and a resolution that needs an sdist fails, naming the package.
- **npm and cargo.** The same shape will fit them (one recipe table, one
  lock array, one install step, one pool), and they are their own versions.
- **More than one environment**, an interpreter other than the release's
  `python3`, extras (`requests[socks]`), environment markers, or editable
  and VCS requirements. Each is a lock question of its own; none is needed
  for a lab.
- **Reproducibility across pip or Python versions.** The lock records which
  pip and which interpreter produced it, the way it records the release; a
  different one may resolve differently.

## The recipe

```toml
[python]
include = ["numpy", "pandas", "jupyterlab"]
```

`[python]` is optional, and a recipe without it behaves exactly as before:
no environment, no apt packages added, nothing in the lock. The table is a
pointer in `recipe.Recipe`, so an absent table stays absent when the recipe
is written back.

Validation is stricter than PyPI's own rules, because every name reaches a
requirements file and pip's command line inside the image: PEP 503 names
only (`^[A-Za-z0-9]([A-Za-z0-9._-]*[A-Za-z0-9])?$`, at most 100 characters).
A version specifier, an extra, a URL, a path or a space fails with a message
that says versions belong in the lock. Two names that PEP 503 considers one
package (`zope.interface` and `zope-interface`) are reported as asked for
twice.

`build` adds `python3` and `python3-venv` to what apt installs when the
recipe has Python packages, the way it adds the essential packages.
`python3-pip` is deliberately not added: with Recommends on it drags
`build-essential` and `python3-dev` into every image, and the environment's
pip comes from `python3-venv`'s `ensurepip` anyway.

## The environment

One environment per image, at `/opt/frostroot/venv`, created with `python3 -m
venv` and owned by root. A single fixed path is what lets the lock name it,
an offline rebuild recreate it, and two rebuilds produce the same compiled
caches, which embed the paths of their sources.

`/etc/profile.d/frostroot-python.sh` puts `/opt/frostroot/venv/bin` first on
`PATH`, so a login shell, `sudo -i` and WSL's own shell find it: `python` in
the image is the environment's python, and `import numpy` works without
activating anything. A non-login, non-interactive shell (`wsl -d lab --
python3`) does not read `profile.d` and gets `/usr/bin/python3`, which is
Ubuntu's own; that is the normal behavior of a `PATH` addition and is
documented rather than worked around.

## The pinned resolver

The lock's wheels, checksums and URLs come from pip's installation report
(`pip install --report`), which arrived in pip 22.2. Ubuntu ships pip 24.0 on
24.04, 22.0.2 on 22.04 and 20.0.2 on 20.04, so two of the three releases
cannot produce one.

frostroot therefore installs one pinned pip into the environment before
anything else, on every release: version 24.3.1, by its wheel's SHA-256,
as `builder.PinnedPip`. It is the same habit as the pinned fingerprints of
the source catalog: the file decides, not the server. One pin covers all
three releases, because pip 24.3.1 supports Python 3.8 and up.

Two details the spike settled, both load-bearing:

- `--upgrade` is required. Without it, 20.04's pip calls a direct-URL
  requirement satisfied by the pip already installed, downloads nothing,
  checks no hash, and goes on to resolve with itself.
- The pin must be installed as its own step **offline too**. A single
  requirements run installs the new pip as one package among many while the
  old pip does all the work, and the old pip compiles caches its own way,
  which is what made two offline rebuilds differ on 20.04.

After the pin the script checks `pip --version` and fails the build if it is
not the pinned one, rather than producing a lock that cannot be vendored.

## Building

The Python step is one `--customize-hook`, after the provisioning script, so
the user, the locale and the timezone exist first. `PhaseInstallPython` is
its phase; the progress display lists it only for a recipe that has Python
packages, and the parser recognizes it by the script's name in mmdebstrap's
announcement of the hook.

Online, the script creates the environment, installs the pin, then runs

```
pip install --only-binary=:all: --report /frostroot-pip-report.json <names>
```

and frostroot downloads the report out of the image. Offline, the hooks
upload a requirements file rendered from the lock and copy `vendor/wheels/`
in as `/frostroot-wheels`, the script installs the pin from that directory
and then

```
pip install --no-index --find-links /frostroot-wheels --require-hashes -r /frostroot-requirements.txt
```

so pip can neither reach the network nor accept a file whose checksum
differs. It writes `pip list --format=json`, which frostroot downloads and
compares with the lock.

Three things make the caches the same twice, and the spike needed all three.
`SOURCE_DATE_EPOCH` is exported for the whole step, venv creation included:
Python writes hash-based `.pyc` files when it is set, and without it
`ensurepip` stamps pip's own bytecode with the moment it compiled. The
environment is then recompiled with `compileall --invalidation-mode
checked-hash`, because pip compiles as it installs and each release's pip
does it its own way. And `PYTHONHASHSEED` is fixed, because compiling a
module that holds a set constant marshals it in the order that run's hash
seed produced, which 3.8 does not sort. A module that will not compile is
not an error: nothing imports it during the build.

`HOME` is set to root's own. A customize hook inherits the environment of
whoever started the build, and pip writes its cache under `$HOME`, so
without this the image grows a `/home/<builder>` directory that its own
`/etc/passwd` knows nothing about. pip is also told to keep no cache at all.

Nothing the step used stays in the image: the script deletes the wheels, the
requirements and the pin file, and a hook deletes the report and the package
list after frostroot has them.

## The lock

```toml
[python]
requested = ['numpy', 'pandas', 'jupyterlab']
venv = '/opt/frostroot/venv'
interpreter = '3.12.3'
pip_version = '24.3.1'

[[pypi]]
name = 'numpy'
version = '2.5.3'
sha256 = '...'
filename = 'numpy-2.5.3-cp312-cp312-manylinux_2_28_x86_64.whl'
url = 'https://files.pythonhosted.org/packages/...'
```

`[[pypi]]` is its own array: it never mixes with `[[packages]]`, so the apt
locker stays the apt locker, and a tool that reads one ignores the other.
`auto = true` marks a package the resolver pulled in, as it marks a package
apt pulled in. Entries are sorted by the name PEP 503 compares, so the
lock's order depends on the package set and nothing else.

`size` is optional here: pip's report gives no file size, and the checksum
is what decides. The pinned pip is recorded as an entry like any other, with
the size frostroot knows for it.

A lock written before 0.7 has no `[python]` and no `[[pypi]]`, and every
command treats it exactly as before.

## Vendor and offline

`vendor/wheels/` is the second pool. `pool.WheelManifest` derives it from
`[[pypi]]`, refusing a file name that is not a plain `.whl`, a duplicate
file name, and a URL that is not https. A wheel carries its whole URL,
because PyPI serves files from a content-addressed path rather than a base
URL with a pool layout, so `--mirror` does not touch it and there is no
Launchpad fallback: PyPI does not drop files it has published.

`vendor` fills both pools in one run, `--prune` cleans both, and the summary
names them separately. `build --offline` verifies both before it makes a
work directory, and refuses when the recipe's `[python]` list no longer
matches the lock's, as it already refuses a changed `include`.

## Errors

| Situation | Exit | Message |
|---|---|---|
| A version, extra or URL in `[python]` | 1 | invalid python package name ...; versions belong in the lock |
| One package asked for twice | 1 | python.include names ... twice |
| `vendor/wheels/` short or corrupt | 1 | vendor/debs is incomplete: ... in vendor/wheels; run frostroot vendor |
| `[python]` changed since the lock | 1 | frostroot.lock does not match frostroot.toml: python packages added ... |
| The resolution needs a source distribution | 2 | not a wheel: ... (only wheels can be locked; ask for the apt package instead) |
| The report has no checksum for a package | 2 | unusable pip report: ... has no sha256 |
| The pinned pip did not replace the release's | 2 | frostroot: pip 24.3.1 did not replace this release's pip |
| The environment differs from the lock | 2 | the rebuilt image differs from frostroot.lock: ... in the environment ... |

## Testing

Offline, without root, mmdebstrap or a terminal, as the rest of the suite:

- The recipe and lock round trips, the PEP 503 name rules and normalization,
  and a 0.6 lock that has no Python in it.
- `ParsePipReport` against a fixture cut from pip 24.0's real report, and
  every refusal: a bad version, an empty install list, a missing checksum, a
  source distribution, a URL that is not https, a missing requested package.
- The rendered scripts, online and offline, through `sh -n` and with a
  hostile package name, and the hook order for both.
- `ComparePythonWithLock` for a match, a version difference, a missing
  package, an extra one, a differently spelled name, and the packages
  `ensurepip` seeds, which are in no lock and are not a difference.
- The wheel pool against an `httptest` server, as the deb pool already is.
- The form's new field and the recipe round trip through it.

Behind the `integration` tag, `TestIntegrationPython` builds a real image
with Python packages, vendors it and rebuilds it offline twice.

## Spike results (2026-09-17, build host, mmdebstrap 1.4.3)

Five rounds in mmdebstrap chroots of the three releases, with the archive's
three pockets, installing `numpy pandas jupyterlab requests`.

**24.04 (Python 3.12.3, pip 24.0 from ensurepip).** `--report` present.
Wheels-only resolution: 92 packages, 4 of them asked for, no sdist, 97 s,
370 MB in the environment, 19,465 files. The report carries, per package,
`metadata.name`, `metadata.version`, `download_info.url` and
`archive_info.hashes.sha256` — but **no size**, which is why the lock's
`size` had to become optional. `pip download --require-hashes` fetched all
92 wheels (72 MB) in 20 s, and two offline installs from them took 29 s and
30 s.

**Byte identity.** The first round differed in 990 files, every one of them
under `site-packages/pip/`: `ensurepip` had compiled pip's bytecode before
`SOURCE_DATE_EPOCH` was exported. With the variable exported for the whole
step, two offline installs of 19,553 files were **identical**, and both
pip's and numpy's `.pyc` files were hash-based (flags 3). Running
frostroot's own rendered scripts, 24.04 stayed identical over 19,401 files.

**22.04 (Python 3.10.12, pip 22.0.2).** No `--report`. With the pinned pip
installed first, `--report` present, 96 packages resolved (numpy 2.2.6,
pandas 2.3.3, jupyterlab 4.6.3), 97 wheels vendored (76 MB), and two offline
rebuilds of 20,736 files identical. A deliberately wrong hash was refused.

**20.04 (Python 3.8.10, pip 20.0.2).** Without `--upgrade` the pin silently
did not install: pip reported "Requirement already satisfied ... (20.0.2)",
checked no hash, and `--report` was still missing. With `--upgrade` the pin
installed and 101 packages resolved (numpy 1.24.4, pandas 2.0.3, jupyterlab
4.3.8 — the last versions with wheels for 3.8). A first offline pass still
differed in 9,326 files, because the requirements run left the installing to
the old pip; installing the pin as its own step offline is what fixed it.

**What determinism on 20.04 took**, measured one change at a time over
16,624 files: 1,148 `.pyc` files still differed after the pin was installed
first, because pip 24.3.1 on 3.8 compiles as it installs and writes what it
writes; recompiling the environment with `--invalidation-mode checked-hash`
brought that to 184; fixing `PYTHONHASHSEED` brought it to **0**. The 184
were modules holding set constants, which 3.8 marshals in the order that
run's hash seed produced. 22.04 and 24.04 were identical before those two
changes and stayed identical after.

**PATH.** In the chroot, a login shell (`su - student`) and `sudo -i` both
found `/opt/frostroot/venv/bin/python3` and imported numpy; a non-login
shell got `/usr/bin/python3`, as expected.

**python3-pip is not worth adding.** With Recommends on it pulls
`build-essential` and `python3-dev`; the first spike chroot took 9 minutes
against 5 for the same one without it.

## Open questions

None. The five rounds settled the resolver, the flags, the pool and the
determinism; what remains is the verification on real images, which is Task
8 of the plan.
