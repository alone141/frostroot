# Verification

What was run for real, one paragraph per version, oldest first: the
checks no CI runner can make, the end-to-end scenarios, and what CI said.
The README's [Verification](../README.md#verification) section describes
the tests and the WSL boot check these paragraphs refer to. A version's
paragraph is added after it was run, not before.

For v0.1.0 the README's WSL boot check was done on Windows 11 with WSL
2.6.3 for all three releases, along with the failure paths; the results
are recorded in the
[feasibility analysis](superpowers/reviews/2026-09-15-frostroot-feasibility.md#release-verification-v010-2026-09-16).

For v0.7.0 a 24.04 image with `requests` and `numpy` was imported the same
way: every boot check passed, `systemctl is-system-running` said `running`,
and in a login shell `python3` was the environment's
(`/opt/frostroot/venv/bin/python3`, 3.12.3), `import numpy, requests` gave
the locked 2.5.3 and 2.34.2, and `pip --version` was the pinned 24.3.1. The
[Python spec](superpowers/specs/2026-09-17-frostroot-python.md#verification-2026-09-17-build-host-mmdebstrap-143)
records it.

For v0.8.0 a 24.04 image with `[certificates]` and `requests`, rebuilt
offline, was imported the same way: every boot check passed,
`systemctl is-system-running` said `running`, the recipe's authority was at
`/etc/ssl/certs/corp-root.pem` and `openssl verify` accepted it against the
image's bundle, `urllib` (which reads the system store) fetched
`https://pypi.org` through that regenerated bundle, `python3` was the
environment's with `requests` 2.34.2 and pip 24.3.1, and nothing the build
had merely been allowed to trust was left in the image. The
[Certificates spec](superpowers/specs/2026-09-18-frostroot-certificates.md#verification)
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
[TUI plan](superpowers/plans/2026-09-18-frostroot-tui-2.md#v09-verification);
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
under [Rebuilding offline](../README.md#rebuilding-offline)), with `/usr/bin/unrar`
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

For v0.13.0, `--insecure` was verified in three pieces on the build host,
none of them a byte-identity run, which this flag is not held to. First the
apt mechanism, with mmdebstrap driven directly over
`https://archive.ubuntu.com/ubuntu` and a `CaInfo` bundle holding one bogus
authority and nothing else, so that verification could not succeed: with
that alone apt refused the mirror (`The certificate is NOT trusted`, exit 25
after 14 s, an empty tarball); with the two `Verify` settings the setup hook
now writes beside it, the same bundle bootstrapped noble in 222 s, and the
tarball held neither `99frostroot-build-ca` nor any mention of `Verify-Peer`
or `CaInfo` under `etc/apt`. Then pip 24.3.1, the pinned resolver, run from
its own wheel against the v0.8 spike's server, whose certificate a private
authority signed: it refused with `CERTIFICATE_VERIFY_FAILED`, and with
`--trusted-host localhost:8443` it downloaded `requests`. Then the whole
thing: a 24.04 recipe asking for `git` and `requests` built with `build
--insecure` in 673 s, printing the three warning lines first, and its lock's
`[python]` table ended in `transport = 'unverified'`, with 262 packages and 6
wheels locked. `vendor` without the flag repeated the lock's warning,
downloaded all 268 files, and reported that every wheel had matched the lock
over a verified connection. `build --offline` repeated the warning and
rebuilt the image in 447 s, every package as locked; `build --offline
--insecure` noted that nothing is fetched and did the same. The image's
`etc/apt/apt.conf.d` held no trust setting of any kind.

For v0.13.1 the first login, which the README asks to be checked for
every release, was checked by the new `wsl-boot` scenario with the
**v0.13.0 release binary**, the one people download: a 24.04 image with
`git`, `requests` and a `[certificates]` authority built in 467 s, imported
as a throwaway distribution in 9 s through `wsl.exe` from inside WSL, and at
first login: the recipe's user, passwordless sudo,
`systemctl is-system-running` = `running`, DNS, a locale without warnings,
UTC, `python3` at `/opt/frostroot/venv/bin/python3` with `requests` 2.34.2
and pip 24.3.1, the authority trusted by the image's store, and apt holding
no setting of the build's; then the distribution was removed. The four
fixes of v0.13.1 are each pinned by a test that fails with the fix
reverted, run with `scripts/mutate.sh` and recorded in the commit; the
index bound was also measured, a body that would expand to 80 MB being
refused with under 32 MB allocated.

For v0.13.2 the six fixes are each pinned by a test that fails with the fix
reverted, run with `scripts/mutate.sh` and recorded in the commit, and
`scripts/check.sh` ran every step. With the branch's binary, the
`offline-identical` scenario built a 24.04 image with `git` and `requests`
in 477 s, vendored it in 26 s and rebuilt it offline twice, in 501 s and
441 s, the second time with `PYTHONPYCACHEPREFIX` set on the build host:
both rebuilds came out as one SHA-256, and the image holds no cache under
the host's prefix. The same scenario had failed with only the Python hook
sweeping that variable, the image then holding 930 caches under
`<prefix>/usr/lib/python3/` written by dpkg's `py3compile`, which is what
showed that mmdebstrap needs the host's `PYTHON` and `PIP` variables dropped
before it starts. `tui` passed its 4 checks in 14 s, and `capture-roundtrip`
its 10, with a real build placed through the new staging order and captured
back into a recipe that validates. The five capture fixes that followed
(certificates below the trust directory and behind symlinks, conffile paths
kept inside `--root`, apt's reading of `Enabled`, one repository listed with
and without a trailing slash, several keyrings in one `Signed-By`) are each
pinned the same way, and `capture-roundtrip` passed its 10 checks again
with that binary, the 24.04 image built in 650 s and captured back into a
recipe that validates. So are the six fixes to the package index (a field
folded over two lines in the cache, a source deadline served from the cache
rather than reported, sections counted for section-less packages, names a
private PyPI index serves that the cache could not give back, and the two
memos that kept a stale answer), and `tui` passed its 4 checks again with
that binary, searching the real archive and PyPI. So are the six fixes to
the form and the picker (the Python field's own name rule, one row per
project whatever its spelling, one summary lookup per rest of the cursor,
the summary page cut to the terminal's width, a byte-order mark that is not
a change, and two spellings of one project refused before the
confirmation); the golden frames now refuse a line wider than the terminal
they were recorded at, and `tui` passed its 4 checks once more with that
binary. The first login was checked by `wsl-boot` with the **v0.13.2 release
binary**, the one people download: a 24.04 image with `git`, `requests` and a
`[certificates]` authority built in 487 s, imported as a throwaway
distribution in 12 s, and at first login the recipe's user, passwordless
sudo, `systemctl is-system-running` = `running`, DNS, a locale without
warnings, UTC, `python3` at `/opt/frostroot/venv/bin/python3` with `requests`
2.34.2 and pip 24.3.1, the authority trusted by the image's store, and apt
holding no setting of the build's; then the distribution was removed.

For v0.14.0, Ubuntu 26.04 went through a spike before any code changed:
`master` at 7ce0b16 with the release's row as its only change, on a 24.04
build host and on a 26.04 one. A minimal image built in 90 s, one with
`requests` and `numpy` on Python 3.14.4 and the pinned pip in 111 s, one
with all 32 packages of the form's catalog (1,135 installed) in 540 s, and
one with all eight catalog sources, each package from its own repository,
in 326 s; the 26.04 spec records what each showed. Then, with the branch's
binary (de48515), `scripts/check.sh` ran every step,
`TestIntegrationNobleTiny` and the new `TestIntegrationResoluteTiny` passed
in 105 s and 104 s, and every scenario that runs without Windows passed
with `E2E_RELEASE=26.04`: `offline-identical` its 12 checks, two offline
rebuilds with a Python environment coming out as one SHA-256;
`certificates` its 21, the rebuilds with and without `--ca-bundle` one
SHA-256 too; `no-build-leaks` its 15, `insecure` its 17,
`capture-roundtrip` its 10 and `failed-build` its 7; and `tui` its 4 on the
form's new default, writing `release = "26.04"`. The container they ran in
re-signs every TLS connection it makes, PyPI's included, so the online
builds of `offline-identical` and `certificates` were given its authority
with `--ca-bundle`, as such a network needs; the other scenarios ran as
committed. Issue #102, which the spike found, went into the same version.
On the 24.04 build host (mmdebstrap 1.4.3), a root build of a 24.04 recipe
with `CAP_SYS_ADMIN` dropped by `setpriv`, as in a container that was not
given it, exited 0 after 97 s from `master` at 147d846; against the same
recipe built with the capability, its image lacked the eleven entries
systemd-tmpfiles makes, and held `/etc/credstore` and
`/etc/credstore.encrypted` at 0755 and `/var/log/journal` owned by root.
The branch's binary (998219d) stopped that build after 0.27 s with exit 1,
quoting mmdebstrap's warning, kept the work directory and wrote neither a
lock nor a tarball; with `mount` off PATH it refused with exit 1 before
any work directory existed. Each fix is pinned by a test that fails with
it broken, run with `scripts/mutate.sh` and recorded in the commits, and
the new `TestIntegrationHostThatCannotMountFailsFast` stopped a real
mmdebstrap in 0.3 s as root and as an unprivileged user, and failed after
93 s with the watch broken. `scripts/check.sh` ran every step,
`failed-build` passed its 7 checks, and `scripts/integration.sh` as root
passed every required test but `TestIntegrationPython`, whose pip refused
PyPI's certificate because the container re-signs it; a `[python]` build
given the container's authority with `--ca-bundle` succeeded in 114 s, and
CI, where nothing re-signs, ran all seven required tests to a pass as an
unprivileged user on 3d9eeee. The first login of a 26.04 image,
`wsl-boot` with `E2E_RELEASE=26.04`, needs Windows and is still to run.

For v0.15.0, the seam Fedora fits behind went in first, as a pull request
of its own, held to changing no byte of an Ubuntu image. `master`'s binary
(9e2b43f) built a 24.04 recipe with `git`, `curl`, `build-essential`, the
`tr_TR.UTF-8` locale and `six` from PyPI (354 packages, and 2 in the
virtual environment), vendored its lock and rebuilt it offline; the
branch's binary (47fa517) rebuilt the same lock offline to the same
SHA-256, and neither rebuild touched the lock. Built online at the instant
that lock records, the branch's binary wrote the same lock as `master`'s,
and a tarball of the same SHA-256 as `master`'s own second build at that
instant. With the branch's binary, `offline-identical` passed its 12
checks, `certificates` its 21, the rebuilds with and without `--ca-bundle`
one SHA-256, `no-build-leaks` its 15 and `capture-roundtrip` its 10. The
online builds with Python packages, in those scenarios and above, were
given the container's authority with `--ca-bundle`, as such a network
needs. Each new refusal (a family frostroot does not build, a lock of
another family offline and in `vendor`) is pinned by a test that fails
with it broken, run with `scripts/mutate.sh` and recorded in the commits.
`scripts/check.sh` ran every step, and `scripts/integration.sh` as root
passed every required test but `TestIntegrationPython`, whose pip refused
PyPI's re-signed certificate as before; CI ran all seven to a pass on
16b2307.

Then Fedora 44 itself, in the second pull request, from a recipe of `git`
in `tr_TR.UTF-8` written by hand. Built online, it took 69 s as root and
76 s as uid 1001 from a warm cache, and wrote a lock of 268 packages (28
`User`, 209 `Dependency` and 31 `Weak Dependency`) and a tools tree of 122.
The image had the user in `wheel`, the sudoers drop-in 0440, `wsl.conf`,
`LANG=tr_TR.UTF-8`, the timezone, `sudo` setuid and the journal setgid,
and a gzip header without a time. `vendor` fetched its 271 files, 120 MB,
in 8 s. Two offline rebuilds as root under `unshare --net` and one as uid
1001, each about a minute, wrote one SHA-256, `5f62096b…45b9`, and an
online build at the lock's instant wrote the same lock, byte for byte, and
the same tarball; the uid 1001 build left nothing in its work root. The
first two rebuilds had differed, in a `/var/log/dnf5.log` the finalize
script's own dnf5 queries left in the image, and the pull request removes
it. With `E2E_DISTRO=fedora`, `failed-build` passed its 7 checks, dnf5's
own explanation printed first; `offline-identical` its 10 as root and as
uid 1001, the second rebuild with `MKOSI_DNF=dnf` set; and
`no-build-leaks` its 15, a plain build and one with `--ca-bundle` and
`--insecure` writing one lock and one image. With Ubuntu, `failed-build`,
`no-build-leaks` and `harness` passed again after the scenarios' library
changed. `TestIntegrationFedoraTiny` and
`TestIntegrationFedoraOfflineRebuild` passed here as root and as uid 1001.
Their first run in CI failed in mkosi's user namespace: Ubuntu's kernels
let a program use the one it makes only when an AppArmor profile of its
own allows it, and mkosi, unlike mmdebstrap, has none. frostroot now
refuses such a host with the two ways out, and CI lifts the restriction to
be what WSL is; CI then ran all nine required tests to a pass as an
unprivileged user on f883de0, the two Fedora ones in 104 s and 172 s.
Each change is pinned by a test that fails with it broken, run with
`scripts/mutate.sh` and recorded in the commits, and `scripts/check.sh`
ran every step. A Fedora image's first login, `wsl-boot` with
`E2E_DISTRO=fedora`, needs Windows and is still to run.

The third pull request put Fedora in the form. Fedora 44's index, read from
Fedora's own server here, was 22 MB of primary metadata over its two
repositories, 274 MB of XML, reduced to 69,653 packages in 9 s and reopened
from its cache in 0.2 s; a search took 6 ms, the nearest names to a typo 2.
Each of the Fedora catalog's 33 names is in it, and a Fedora 44 recipe
asking for all 33 at once built online to 720 packages and a 1119 MB
tarball, with `node`, `npm`, `npx`, `gcc`, `g++`, `make`, `java`, `javac`,
`go`, `rustc`, `cargo`, `wget`, `emacs`, `vim`, `clang-format`, `ipython3`
and the rest on the path. The plan had named `java-latest-openjdk-devel`,
which on Fedora 44 is OpenJDK 27's early-access build, and `nodejs24` alone
installs only `node-24`, so the catalog names Java 25 and the `-bin`
packages. `tui` drove `init` in a pseudo-terminal for each family: for
Ubuntu it passed its 5 checks, and for Fedora, where the driver moves the
distribution with the arrow key and picks from Fedora's real index, its 6,
after its first run found the last page held up while it read PyPI's cache
for a recipe with no Python packages, long enough for the next Enter to
write the recipe unseen, which the pull request fixes. Every Ubuntu fixture,
and the recipe `init` writes by default, renders to the same bytes as
before. Each change is pinned by a test that fails with it broken, run with
`scripts/mutate.sh` and recorded in the commits; `scripts/check.sh` ran
every step, and CI ran all ten required tests,
`TestIntegrationFedoraCatalogExistsInEveryRelease` among them, to a pass on
d7ed8e5.

v0.15.1 is v0.15.0 built with go1.27.2. The v0.15.0 tag's release run
stopped at govulncheck and published nothing: go1.27.1 had nine
vulnerabilities in `net/http`, `net/textproto` and `crypto/tls`, disclosed
after master last passed CI, that frostroot's fetches reach. Run here,
`scripts/check.sh --only vuln` reported the same nine on go1.27.1 and none
on go1.27.2. golangci-lint v2.13.2 could not type-check a go1.27.2 build,
whose export data its `golang.org/x/tools` cannot read, and v2.14.0, which
CI now pins, found 0 issues on either toolchain; `scripts/check.sh` ran
every other step clean on go1.27.2. The first logins of a 26.04 and of a
Fedora 44 image are still to check.

Toward v0.16, the first pull request of disk images settled the recipe, the
lock and the form without building a disk: a disk recipe validates, and
`build` refuses it. Run here: `scripts/check.sh` clean on each commit; every
WSL fixture and `init`'s defaults for both families rendered by the old and
the new recipe template to the same bytes, 22 recipes; the golden frames
changed only on the Image page, by the target question; the `tui` scenario
passed its 6 checks for Ubuntu and its 7 with `E2E_DISTRO=fedora`, among
them that the recipe the form writes names no target. And a lock of
`testdata/valid.toml` (Ubuntu 22.04), made online and vendored by the
binary this pull request starts from, rebuilt offline by that binary and by
this pull request's to the same SHA-256, `79fb9edb`, with the lock untouched
by both.

The second pull request made the disk tools tree, with nothing assembled
from it yet. Run here: `scripts/check.sh` clean on each commit, with
golangci-lint v2.14.0 built with go1.27.2; and `TestIntegrationDiskToolsTree`
as root, in a container on Ubuntu 24.04 with mmdebstrap 1.4.3 and bubblewrap
0.9.0, in 51 s: mmdebstrap made the 26.04 tree, 104 packages with e2fsprogs
1.47.2, in 34 s; mke2fs, mcopy, sfdisk, qemu-img, tar and dd answered
through bubblewrap, mkfs.fat wrote a FAT onto a file under `/work`, and
`env` in the sandbox showed frostroot's five variables, the `PWD` bwrap
sets, and nothing of the host's; the 104 packages were vendored from the
archive, a tree from the pool alone held the same 104, as the lock records
them, in 12 s, and both trees were removed. The test's first run found a
defect: a tool's output collected in one buffer lost all that mke2fs wrote
to stderr, which is fixed and pinned (its revert cannot compile, so the
comparison was broken instead, and `TestBubblewrapRunKeepsOneBufferOneWriter`
failed). The unprivileged path, where mmdebstrap's tree belongs to the
subordinate ids and `unshare --map-auto` removes it, could not run here,
where only root is available. CI ran on the pull request after all. Its
govulncheck found GO-2026-6809, compromised intermediate certificates
distrusted in `crypto/x509`, in go1.27.2, which go.mod pinned, disclosed
since master last passed and reachable from the index's fetches; go1.27.3
fixes it and go.mod now pins that, `scripts/check.sh --only vuln` reported
the one on go1.27.2 and none on go1.27.3, and `go mod tidy -diff` reported
no change. And its integration job ran the unprivileged path as the
runner's own user, on go1.27.3: `scripts/integration.sh` saw all eleven
required tests pass, `TestIntegrationDiskToolsTree` among them, in 13
minutes.

The third pull request made the Ubuntu disk. Run here, as root in a
container on Ubuntu 24.04 with mmdebstrap 1.4.3, bubblewrap 0.9.0, QEMU
8.2.2 and OVMF, without KVM: `scripts/check.sh` clean on each commit, with
golangci-lint v2.14.0 built with go1.27.3. The two disk integration tests
with `FROSTROOT_INTEGRATION_DISK=1`, which CI never runs, at the owner's
choice: `TestIntegrationResoluteDisk` in 224 s, a 26.04 disk whose GPT,
root and EFI system partition, read back with the host's sfdisk,
e2fsprogs, dosfstools and mtools, carry the identifiers the lock's seed
gives, the shim, GRUB and MokManager where the firmware looks, and nothing
per machine; and `TestIntegrationDiskOfflineRebuild` in 590 s, two
rebuilds from the pool alone one SHA-256 and the lock untouched. Its first
run found an expectation of the test's own, since mdir prints names that
fit 8.3 as two columns. The four scenarios with `E2E_TARGET=disk`, each
a 24.04 disk: `failed-build` passed its 8 checks in 137 s, with neither a
lock nor an image nor `dist/` written; `no-build-leaks` its 15 in 400 s,
two online disks at one instant, one plain and one with `--ca-bundle` and
`--insecure`, whose roots differ only in fstab and grub.cfg, which carry
the seed each build drew, and whose locks differ only in the seed line;
`disk-boot` its 18 in 400 s: the disk built in 270 s and booted under TCG
in 111 s by OVMF with Microsoft's keys, mokutil reporting Secure Boot
enabled under kernel 6.8.0-147, systemd running with no unit failed, the
root grown to fill the 8G disk, an address by DHCP and archive.ubuntu.com
resolved, the host name `e2e-disk`, a machine-id of its own, the password
set from the SMBIOS credential by `frostroot-set-password.service` with
`Result=success`, the ed25519 host key made at the first boot, and the
firmware with OVMF's snakeoil variables refusing the disk in 3 s; and
`offline-identical` its 14 in 613 s, the online build in 274 s, vendor in
15 s, and two offline rebuilds, 146 s and 145 s, one SHA-256, `2ab8f48c`,
with the lock as it was. The scenarios' first runs found three things of
the scenarios' own and nothing of the build's: the boot driver's pattern
cut the echoed command rather than the answer out of the serial log, 4
checks; pip in `offline-identical`'s online build could not verify
files.pythonhosted.org through this container's inspecting proxy, which
`E2E_CA_BUNDLE` now answers with `--ca-bundle`; and its check of the lock's
seed looked for double quotes where the lock writes single ones. CI ran on
the pull request: all four jobs green on the heads before the scenario's
one-line fix, and the three fast jobs green on it while its integration
job was still running as this was written. A real PC with Secure Boot on,
and the first login of a 26.04 disk, are still to check.
