# frostroot v0.14: Ubuntu 26.04 LTS

Date: 2026-09-28
Status: approved by the project owner in conversation ("26.04, run the spike here, keep 20.04"); spike run on 2026-09-28, implementation on branch `claude/next-steps-64pvh0`
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md), whose distro table this adds a row to

## Why

Ubuntu 26.04 LTS (resolute) was released on 2026-04-23. frostroot builds
20.04, 22.04 and 24.04, so a
lab that wants the current LTS for the new academic year cannot have it.
Nothing in the recipe, the lock or the pipeline is specific to one release:
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
  command, the Rust coreutils for most of coreutils, chrony for time and
  one-line and deb822 sources side by side. An image is what a stock
  installation of the release would be, with the recipe's packages; it does
  not second-guess the release.
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
| python3 | 3.12 | 3.14.3 |
| systemd | 255 | 259.5 |
| coreutils | GNU | `coreutils-from-uutils`: rust-coreutils 0.10, with gnu-coreutils 9.7 for the tools uutils does not yet cover (`cp` is `gnucp`) |
| sudo | sudo | sudo 1.9.17 and sudo-rs 0.2.13, with sudo-rs the `sudo` and `visudo` alternatives |
| time daemon | systemd-timesyncd, which systemd recommends | chrony, which satisfies the same recommendation; systemd-timesyncd is not installed |

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

**What starts at boot.** Among the units the image enables, the ones a
first boot under WSL has to show harmless are `chrony.service`,
`netplan-configure.service`, and systemd-resolved with its
`systemd-resolved-monitor.socket` and `systemd-resolved-varlink.socket`.
That is the `wsl-boot` scenario's question; it needs `wsl.exe`, so it runs
on the owner's Windows machine, not in the spike.

**Python.** Before any build, pip 24.3.1, the pinned resolver, was run from
its own wheel (checksum verified) in a Python 3.14 environment: it resolved
and installed `requests` 2.34.2 and `numpy` 2.5.3 as cp314 wheels, and
wrote an installation report of version 1, the format frostroot parses.
That was a standalone 3.14.0rc2, not Ubuntu's 3.14.3; the image build is
below.

Pending: the Python step in a 26.04 image, the whole catalog and every
catalog source installed on 26.04, the offline byte identity and trust
scenarios, and a 26.04 build host.
