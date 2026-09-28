# frostroot v0.14: Ubuntu 26.04 LTS

Date: 2026-09-28
Status: approved by the project owner in conversation ("26.04, run the spike here, keep 20.04"); spike run on 2026-09-28, implementation on branch `claude/next-steps-64pvh0`
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md), whose distro table this adds a row to

## Why

Ubuntu 26.04 LTS (resolute) was released on 2026-04-23. frostroot builds
20.04, 22.04 and 24.04, so a lab that wants the current LTS for the new
academic year cannot have it. Nothing in the recipe, the lock or the
pipeline is specific to one release:
the release is a row in `internal/distro`, and the design put it there so
that a new one would be a row and not a rewrite. What a new release can
break is everything the row does not say: the tools inside the image that
frostroot's hooks call, the Python the image's environment is built on, the
third-party repositories the catalog offers, and the first boot under WSL.
The spike below measured each of them before any code changed.

## Goal

- `release = "26.04"` builds, online and offline, with everything a 24.04
  recipe can hold: packages, third-party sources, `[python]`,
  `[certificates]`, `--ca-bundle` and `--insecure`.
- Two offline rebuilds of a 26.04 lock are byte-identical, as for every
  other release.
- `init` and `edit` offer 26.04, and start a new recipe on it, since the
  form's defaults are "a lab image on the newest release".
- `capture` reads a 26.04 machine.
- 20.04, 22.04 and 24.04 stay exactly as they are: every existing lock
  still loads and rebuilds to the same bytes.

## Non-goals

- A 26.04 build host is not required for any of this, and is measured
  below rather than promised: the build host's apt drives mmdebstrap, and
  frostroot's progress parser was written against apt 2.x's output.
- Replacing what 26.04 itself chose. It ships sudo-rs as the `sudo`
  command, the Rust coreutils for most of coreutils, chrony for time, and
  an apt that reads one-line and deb822 sources alike. An image is what a
  stock installation of the release would be, with the recipe's packages;
  it does not second-guess the release.
- Architectures other than amd64. 26.04's Release file also lists
  `amd64v3`, a variant for newer processors; frostroot builds plain amd64,
  which runs everywhere amd64v3 does.

## What 26.04 changes

Read from the archive on 2026-09-28:

| | 24.04 (noble) | 26.04 (resolute) |
|---|---|---|
| Release file signed by | archive key 2018, `F6EC B376 2474 EDA9 D21B 7022 8719 20D1 991B C93C` | the same key |
| apt | 2.8 | 3.2.0 |
| dpkg | 1.22 | 1.23.7 |
| python3 | 3.12 | 3.14 (3.14.3 in the release pocket, 3.14.4 in `-updates`) |
| systemd | 255 | 259.5 |
| coreutils | GNU | `coreutils-from-uutils`: rust-coreutils 0.10, with gnu-coreutils 9.7 for the tools uutils does not yet cover (`cp` is `gnucp`) |
| sudo | sudo | sudo 1.9.17 and sudo-rs 0.2.13, with sudo-rs the `sudo` and `visudo` alternatives |
| time daemon | systemd-timesyncd | chrony; systemd-timesyncd is not installed |

The signing key matters most: a 24.04 build host's `ubuntu-keyring`
(2023.11.28.1) verifies resolute without an update, and so does every
host frostroot already supports.

The three pockets, `resolute`, `resolute-updates` and `resolute-security`,
are on `archive.ubuntu.com`. Every repository in the sources catalog
publishes an InRelease for resolute, or for the single suite it always
uses: deadsnakes, git-core, LLVM (`llvm-toolchain-resolute`), Kitware and
Docker for resolute, NodeSource for `nodistro`, GitHub CLI and VS Code for
`stable`. All 32 packages in the form's catalog exist in resolute's
`main` or `universe`.

## Spike results (2026-09-28, build host Ubuntu 24.04.4, mmdebstrap 1.4.3-6, apt 2.8.3)

The spike binary was `master` at 7ce0b16 with one change: the 26.04 row in
the distro table and in `SupportedVersions`. Everything else ran as
released.

**A minimal image.** A recipe asking for `git` and `jq` built in 90 s:
265 packages, a 143 MB tarball, and a lock recording `release = '26.04'`,
`suite = 'resolute'`, the three deb lines and every package's checksum.
Every phase was announced and measured as on 24.04: the download total
(30.6 MB, then 70.4 MB for the requested packages) and every dpkg step. In
the image: the user with a locked password (`!` in `/etc/shadow`, no
hash), the sudoers drop-in at 0440, `wsl.conf`, `en_US.UTF-8` and UTC; no
`resolv.conf` or `hostname`; no entry at the root named `frostroot-*`; and
nothing under `/etc` or `/var/lib` naming the work directory.

**sudo.** frostroot installs `sudo` by name, and with recommends on (the
default) 26.04 brings sudo-rs, which the alternatives make `sudo` and
`visudo`. The provision script's `visudo -cqf` check ran under sudo-rs's
`visudo` and passed, and `sudo -n id -u` as the user printed 0. The C sudo
is still installed, as `sudo.ws`.

**coreutils.** The provision script, which uses `ln`, `printf`, `tr`,
`test` and `chmod` inside the image, ran unchanged under the Rust
coreutils.

**apt in the image.** `apt update` inside the image, with the one-line
`/etc/apt/sources.list` frostroot writes, printed no warning or notice and
reported every package up to date. apt 3.2 still reads that format
silently, so the image keeps it.

**What starts at boot.** The same recipe was built for 24.04 to compare
(261 packages against 265). Enabled only in the 26.04 image:
`chrony.service`, `netplan-configure.service`, and systemd-resolved's
`systemd-resolved-monitor.socket` and `systemd-resolved-varlink.socket`.
Enabled only in the 24.04 image: `systemd-timesyncd.service` and
`uuidd.socket`. Whether the four new units come up quietly under WSL is
the `wsl-boot` scenario's question; it needs `wsl.exe`, so it runs on the
owner's Windows machine, not in the spike.

**Python.** Before any build, pip 24.3.1, the pinned resolver, was run from
its own wheel (checksum verified) in a Python 3.14 environment: it resolved
and installed `requests` 2.34.2 and `numpy` 2.5.3 as cp314 wheels, and
wrote an installation report of version 1, the format frostroot parses.
Then the real step: a 26.04 recipe with `git`, `requests` and `numpy`
built in 111 s. The lock's `[python]` table records interpreter 3.14.4
and pip 24.3.1, and seven `[[pypi]]` entries, `numpy` and
`charset-normalizer` as cp314 wheels. In the image, a login shell as the
user runs `/opt/frostroot/venv/bin/python3` 3.14.4 and imports both
packages; nothing the step used is left at the root, and apt names no
trust setting. The spike's container re-signs every TLS connection it
makes, PyPI's included, with an authority of its own, so the online
resolve needed `--ca-bundle`, as any such network does; the authority
reached neither the image's certificate store nor the lock. Without the
flag pip refused the certificate, and the failed build's first line
quoted pip's `CERTIFICATE_VERIFY_FAILED`, as on 24.04.

**The catalogs.** The form's whole package catalog, all 32 names in one
recipe, built in 540 s: 1,135 packages, among them default-jdk, Go 1.26,
Rust 1.93, clang 21, nodejs 22 and npm 9. `init --plain` offered 26.04,
took every repository in the sources catalog, and fetched their eight
keys, each matching its pinned fingerprints (GitHub CLI's both). A recipe
with those eight sources and one package from each built in 326 s, 523
packages, each from its own repository: python3.13 from deadsnakes, git
2.55 from git-core, clang-24 from LLVM, cmake 4.4.3 from Kitware,
docker-ce 29.8, nodejs 22.23 from NodeSource, gh 2.101 and code 1.139.
apt 3.2, given that image's `sources.list` and keyrings, verified every
source with no warning about any key: not GitHub's keyring, which still
holds the key that expired on 2026-09-05 beside the one that signs now,
nor NodeSource's or Microsoft's 2048-bit RSA keys.

**The package index.** `TestIntegrationOpenEveryRelease`, which walks
`SupportedVersions`, opened resolute's index: 78,415 packages, fetched and
reduced in 6.8 s, reopened from the cache in 125 ms.
`TestIntegrationCatalogExistsInEveryRelease` found every catalog entry in
resolute.

**A 26.04 build host.** A 26.04 root with mmdebstrap 1.5.7, apt 3.2.0 and
dpkg 1.23.7 was made the build host, and the spike binary run inside it.
The 26.04 image built in 103 s with every phase, download total and dpkg
step measured, so the progress parser reads apt 3.2 and mmdebstrap 1.5 as
it reads their predecessors. `vendor` and `build --offline` took 65 s
together, apt 3.2 taking frostroot's flat repository as it is, and two
offline rebuilds there came out as one SHA-256. The same lock rebuilt on
the 24.04 host gives another: the two images differ in debconf's
`config.dat-old` and `templates.dat-old` and an empty
`var/cache/debconf/tmp.ci/`, and in nothing else among 26,351 entries,
which is the README's "the promise does not cross mmdebstrap versions"
measured.

**The scenarios, pointed at 26.04.** `offline-identical` passed its 12
checks: two offline rebuilds with a Python environment, one of them with
`PYTHONPYCACHEPREFIX` set on the host, came out as one SHA-256, and the
lock was left as it was. `capture-roundtrip` passed its 10: a 26.04 image
captured back into a recipe for 26.04 that validates and asks for the same
packages. `no-build-leaks` passed its 15: two online 26.04 builds frozen
at one instant over HTTPS, one plain and one with `--ca-bundle` and
`--insecure`, came out as the same bytes and the same lock, and no file in
the image names the build's work root. `failed-build` passed its 7: a
misspelled package fails with exit 2 and apt's own line first, and writes
neither a lock nor a tarball. `certificates` passed its 21: the recipe's
authority reached the image and the lock, `--ca-bundle`'s reached neither,
offline rebuilds with and without the flag came out as one SHA-256, and a
certificate changed under the lock was refused by name. `insecure` passed
its 17: the warnings, the lock's mark on an unverified Python resolve, and
`vendor` and `build --offline` repeating it. `tui` passed its 4 with the
form listing four releases, and `pip-trust` its 7 under Python 3.14.
`apt-trust` was not pointed at 26.04: it measures the build host's apt,
whatever the image's release. `wsl-boot` needs `wsl.exe`.

Nothing the spike found needs a change to the builder, the hooks, the
lock or the Python step. What 26.04 needs from the code is what the plan
lists: the row, the form's default, the PEP 668 note, a real 26.04 build
in CI, and the documentation.

## Found along the way

These are not 26.04's. The first is left for its own change.

- **A build host without `mount`.** The first 26.04 build host was a
  minimal root without it. mmdebstrap printed `cannot execute mount`,
  went on without `/proc`, `/sys` and `/dev` in the chroot, and the image
  came out without systemd's catalog, its tmpfiles directories and a real
  `hwdb.bin`, while frostroot reported success. A real installation ships
  `mount`; a stripped container used as a build host might not. A
  preflight check is issue #102, a change of its own.
- **The GitHub CLI key's comment.** `internal/sources/catalog.go` said
  GitHub signs with its 2022 key; the repository signs with the 2026 key,
  and the 2022 key expired on 2026-09-05. Both are pinned, so nothing
  broke; the comment is corrected with this change.
