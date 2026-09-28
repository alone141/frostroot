# frostroot v0.15: Fedora

Date: 2026-09-28
Status: **approved by the project owner in conversation on 2026-09-28.**
The owner asked for Fedora ("do fedora") after the pros and cons of #8
were laid out; the spike below ran the same day without changing
frostroot's code; and the owner then answered the decisions at the end:
lab images for WSL, mkosi with a Fedora tools tree, byte-identical offline
rebuilds from the first version, Fedora in the form from the first
version, and the seam first, as a pull request of its own; after the seam
merged, the offline path was measured and the owner chose a lean WSL base
over `@core`. The tasks are in
[`2026-09-28-frostroot-fedora.md`](../plans/2026-09-28-frostroot-fedora.md).
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md)
(the extension point "Fedora / other families"),
[`2026-09-17-frostroot-vendor.md`](2026-09-17-frostroot-vendor.md) (the lock
and the pool) and
[`2026-09-17-frostroot-reproducible.md`](2026-09-17-frostroot-reproducible.md)
(the frozen instant)
Implements: [issue #8](https://github.com/alone141/frostroot/issues/8)

## Why

frostroot freezes Ubuntu. A course that teaches on the Red Hat family,
Fedora now and Alma or Rocky later, cannot have what an Ubuntu course has:
a recipe, a lock that says what the image holds, and a tarball that
rebuilds byte for byte offline. Fedora has shipped its own WSL image since
Fedora 42, so WSL is not the obstacle; the frozen, reproducible image is
what frostroot would add.

Issue #8 says a second family should mean "a new file rather than edits
across `recipe`, `cli` and `export`". That is not true yet. The `Distro`
interface the design promised was never drawn: `internal/distro` is a table
of Ubuntu releases (`Release{Suite, Components, …}`), `[image]` has no
`distro` field and documents `release` as "Ubuntu version", the builder
writes `distro = "ubuntu"` into every lock, and `BootstrapSpec` is apt from
end to end: a suite, deb lines, a keyring, apt hooks. So does the lock
(`suite`, deb lines, apt's auto marks, `.deb` file names), `vendor` and
the offline build (a flat apt repository), the index and the picker (apt
`Packages`), `capture` (dpkg's status, `sources.list`), the sources catalog
(PPAs) and the certificates step (`update-ca-certificates`). Fedora starts
by drawing that line.

## Goal

- `[image] distro = "fedora"` with `release = "44"` builds a WSL tarball
  from the recipe tables that mean the same thing on Fedora: packages, the
  user and passwordless sudo, locale, timezone and `wsl.conf`.
- The lock records every installed package with its checksum and where it
  came from; `vendor` fetches them; `build --offline` rebuilds the image
  byte for byte, as on Ubuntu.
- A build runs as an unprivileged user in a user namespace, or as root, as
  on Ubuntu.
- Every Ubuntu recipe, lock and image stays exactly as it is: a recipe
  without `distro` is Ubuntu, and every offline rebuild of an existing lock
  produces the same bytes as before.
- `init` and `edit` offer Fedora: the form chooses the distribution before
  the release, suggests Fedora packages from a catalog of its own, and
  searches Fedora's package index as it searches Ubuntu's.

## Non-goals, for the first Fedora version

- Third-party repositories (COPR, RPM Fusion), `[python]`,
  `[certificates]` and `capture`. Each is a follow-up once the base holds;
  the form leaves out their pages for a Fedora recipe.
- Other rpm distributions: Alma, Rocky, CentOS Stream, RHEL.
- A Fedora build host. The host stays Ubuntu, usually in WSL.
- Architectures other than amd64 (Fedora's `x86_64`).

## What Fedora changes

| | Ubuntu 24.04 | Fedora 44 |
|---|---|---|
| Packages | `.deb`, apt 2.8, dpkg | `.rpm`, dnf5 5.4.6, **rpm 6.0.2** |
| Bootstrapper | mmdebstrap, Debian's own | none that is Fedora's; mkosi is the candidate (below) |
| Signatures | the `InRelease` file is signed, and hashes chain from it to every package | each package is signed; `repomd.xml` is not (`repo_gpgcheck=0`), and its checksum comes from the metalink over HTTPS |
| Signing key | `ubuntu-keyring` on the host | Fedora 44 primary, rsa4096, `36F6 12DC F27F 7D1A 48A8 35E4 DBFC F71C 6D9F 90A6`, created 2025-01-14, the fingerprint fedoraproject.org/security publishes; `fedora.gpg` holds the keys of 43 to 46 |
| Mirror | `http://archive.ubuntu.com` | `dl.fedoraproject.org` redirects HTTP to HTTPS; `mirrors.fedoraproject.org` serves the metalink over HTTPS |
| Metadata per build | about 26 MB of indexes | 94 MB (`fedora`) and 46 MB (`updates`) as dnf5 loads them |
| Package database | `/var/lib/dpkg/status`, text | `/usr/lib/sysimage/rpm/rpmdb.sqlite`, and dnf5's state in `/usr/lib/sysimage/libdnf5` |
| "Installed as a dependency" | apt's `extended_states` | dnf5's `packages.toml`, `reason = "Dependency"` or `"User"` |
| Support | five years, and more with Pro | about 13 months per release |
| sudo group | `sudo` | `wheel` |
| Locale | `locale-gen` | `glibc-langpack-<lang>` and `/etc/locale.conf` |

## Spike results (2026-09-28, build host Ubuntu 24.04.4 in a container, as root and as uid 1001)

**The host's tools.** Ubuntu 24.04 packages rpm 4.18.2 (sqlite backend,
the same as Fedora's), dnf 4.14.0 (not dnf5), createrepo_c 0.17.3, mkosi
20.2 and bubblewrap 0.9.0. Ubuntu's rpm keeps its database at
`%_dbpath = /root/.rpmdb`; Fedora's is `/usr/lib/sysimage/rpm`. (This
container's `/usr/bin/python3` is a replaced 3.11, so the spike ran
Ubuntu's dnf with `python3.12`; a stock host needs nothing of the kind.)

**Approach A: the host's dnf 4 into an install root. Not viable.** dnf 4
installed Fedora 44 `@core` and `glibc-langpack-en` (366 packages, 166 MB
downloaded) in 93 s and exited 0, and the image was wrong three ways:

- Its rpm database went to `/root/.rpmdb`, and `/usr/lib/sysimage/rpm` was
  empty, so the image's own rpm and dnf5 would see nothing installed.
- rpm 4.18 created the system users after the files they own: "user avahi
  does not exist - using root", and the same for `polkitd` and `sssd`.
  `/etc/polkit-1/rules.d`, `/var/lib/polkit-1` and `/etc/sssd` came out
  `root:root` instead of `root:polkitd` and `root:sssd`, directories of mode
  0750 that the services then cannot read.
- Nothing was mounted in the install root, so systemd's catalog update
  failed, which is issue #102's symptom.

**Approach B: Fedora's own dnf5 from a tools root. Viable.** The host's
dnf 4 installed a small Fedora root holding dnf5 5.4.6 and rpm 6.0.2 (104
packages, 13 s from a warm cache); a chroot into it then ran dnf5 with
`--installroot` into the image, with `/proc`, `/sys` and `/dev` mounted in
both. 368 packages in 58 s, exit 0, and none of approach A's faults: the
database in `/usr/lib/sysimage/rpm`; `polkit-1` owned by gid 114 and
`sssd` by gid 994, the image's own `polkitd` and `sssd`; `/var/log/journal`
0:190 (`systemd-journal`) with setgid; systemd-tmpfiles' directories
present (`/etc/credstore` 0700, `/root/.ssh`, `/var/lib/private`); file
capabilities set (`newuidmap cap_setuid=ep`, `newgidmap`, `arping`,
`clockdiff`, sssd's helpers); 3,529 hardlinked files; and the image's own
dnf5 reported 366 packages installed, 40 of them by the user.

**mkosi with that tools root. Viable, and the candidate.** mkosi 20.2 from
Ubuntu's archive, given the Fedora root as its tools tree
(`--tools-tree`), ran Fedora's dnf5 in a bubblewrap sandbox, created the
system users, generated the volatile files, presets and hardware database,
and wrote the tarball: `@core` and `glibc-langpack-en`, 262 packages (mkosi
leaves weak dependencies out unless `--with-recommends`), 104 s, 162 MB
compressed with zstd, 490 MiB uncompressed. The tools tree needs bubblewrap,
tar, zstd or gzip and systemd besides dnf5 and rpm: without bubblewrap or
the compressor mkosi stops, and without systemd it says it skips the
volatile files, presets and hardware database and goes on. The tools tree
itself was made by the host's dnf 4, faults and all; they stay in the tools
tree, since the image is written by its dnf5. The tarball held 24,229
entries: 2,308 hardlinks, file capabilities as pax extended attributes,
numeric owners as
approach B had them, the rpm database and dnf5's state where Fedora keeps
them, and `/etc/machine-id` reading `uninitialized`.

**Unprivileged.** The same mkosi build as uid 1001, in a user namespace
through `newuidmap`, exited 0 in 80 s with a tarball of the same size whose
owners were right (`/etc/sssd` 0/994, `/var/log/journal` 0/190 setgid). It
differed from the root build in twelve of dnf5's state files, whose modes
followed the invoking user's umask (0002 against root's 0022), and in the
five files below.

**Reproducibility.** Two root builds with `--source-date-epoch=1790600000`,
one taking 81 s and the other 101 s, gave tarballs of 24,229 entries that
differed in five files and nothing else:

- dnf5's `transaction_history.sqlite` and its `-shm` and `-wal`: each
  transaction's wall-clock start and end, and its command line, which
  names the build's workspace (`--installroot=/var/tmp/mkosi-workspace…`).
  That is also a leak of the kind `no-build-leaks` refuses.
- `rpmdb.sqlite-shm`, SQLite's transient index. `rpmdb.sqlite` itself was
  identical: rpm 6 dates each package's install at `SOURCE_DATE_EPOCH` plus
  its place in the transaction (271 entries from 1790600000 to
  1790600259), not at the clock.
- `/var/cache/ldconfig/aux-cache`, which mmdebstrap also deletes for a
  reproducible build.

All five can go after the install. The build-only state has two more
things to decide: `/var/lib/libdnf5/system-repo.lock`, a lock file left
behind, and `/etc/resolv.conf`, a link into systemd-resolved's stub where
WSL writes its own.

**Offline, from a vendored set (measured after the seam merged, the same
day).** The spike's tools tree, with `createrepo_c` 1.2.1 added, and
mkosi 20.2, as root:

- An online build of `systemd`, `sudo`, `passwd`, `glibc-langpack-en`,
  `git` and `dnf5`, weak dependencies on, documentation kept, frozen at
  1790600000, took 31 s from a warm cache and left 253 entries in rpm's
  database: 252 packages and `gpg-pubkey-36f612dc…`, the key rpm imported,
  dated at the frozen instant and no package file. dnf5 recorded each
  package's reason in `packages.toml`, three of them, not two: 7 `User`,
  212 `Dependency` and 33 `Weak Dependency`; and each package's
  repository in `nevras.toml` (`fedora` or `updates`).
- The 252 `.rpm` files, copied from mkosi's package cache into a
  directory, got their repository metadata from the tools tree's
  `createrepo_c` under bubblewrap, without network, in 0.3 s. Two builds
  from it under `unshare --net`, each with a fresh cache, every package
  asked for by name and the Fedora key still checking every package, took
  26 s each and wrote the same tar: 22,061 entries, one SHA-256. Their
  `.tar.gz` files differed only in gzip's header, which records the time
  when mkosi compresses a stream (`gzip --fast --stdout -`); the tools
  tree's `gzip -n` compressed that tar to one SHA-256 twice.
- Against the online image, such an offline tar differed in two files
  only, dnf5's `packages.toml` and `nevras.toml`: the packages asked for
  by name were `User`, 246 of them, all but the 6 that mkosi's first step
  (`filesystem` alone) had brought in, and all came from one repository.
  With the files
  vendored per repository, each given its own metadata and its online
  repository's name, and `packages.toml` put back as the online build
  wrote it, the offline tar was byte-identical to the online one:
  `ececaee9…4329` for both, the rpm database and its install times
  included.

Three things the spike did not expect. mkosi removes a package manager's
database from an image that lacks the package manager, so an image
without dnf5 has no rpm database at all; `CleanPackageMetadata=no` and
dnf5 in every image prevent it. mkosi fetched `fedora.gpg` from
fedoraproject.org during the build, its fallback when the key is in
neither the tools tree nor the package manager tree; frostroot supplies
the pinned key in its own package manager tree, so `build` still never
fetches a key. And reading rpm's database recreates the SQLite side files,
so a script reads it first and removes them last.

**Unprivileged, from the tools tree up.** As uid 1001, with its subuid
range and the host's Python 3.12 (this container's `python3` is another,
which the spike set aside in a private mount namespace), mkosi built the
tools tree itself, as a `directory` image made with the host's dnf 4 from
frostroot's own package manager tree (the two metalinks and the pinned
key): 125 packages, 177 MiB, 66 s from a cold cache. That tree then built
the lean base and `git` as a 419 MiB tar in 67 s, 264 packages, with
`sudo` setuid root, `/var/log/journal` 0/190 and setgid, `/etc/shadow` 0/0
and mode 000, file capabilities on `newuidmap`, `newgidmap`, `clockdiff`
and `arping`, and 2,162 hardlinks. Offline, under `unshare --net`: the
tools tree came back from its 125 vendored files, indexed by the host's
`createrepo_c`, in 13 s with the same package set; and two image builds
with it, from the vendored files per repository and dnf5's reasons put
back, took 26 s and 24 s. The online tar and both offline tars had one
SHA-256, `8d3c5ef5…2dd5`.

So a Fedora build host needs, from Ubuntu's archive, `mkosi` (20.2 on
24.04), `dnf` (to make the tools tree), `rpm` (mkosi 20.2 runs it for any
rpm-based image, and 24.04's `dnf` does not pull it in) and
`createrepo-c` (to index the vendored files before any tools tree
exists), besides the `uidmap` unprivileged Ubuntu builds already use. The
tools tree's rpm database is at its root, `/.rpmdb`, where the host's
Debian-patched rpm puts it.

**The base set.** `@core` with `glibc-langpack-en`, `dnf5` and `sudo` came
to 367 packages and 599 MiB uncompressed, among them NetworkManager,
firewalld, the SELinux policy, sssd, openssh-server, audit, dracut,
plymouth, polkit, avahi and zram-generator. A list of what a WSL lab uses
(systemd, sudo, dnf5, the core command-line tools, `curl`, `man-db`,
`tzdata`, `ca-certificates`) came to 188 packages and 347 MiB. The owner
chose the second (decision 6).

**Not measured yet.** A first boot under WSL (`wsl-boot`, on Windows),
the POSIX ACLs systemd-tmpfiles sets on `/var/log/journal` (this host's
tar could not apply them on extract) as WSL imports them, and Ubuntu
26.04's newer mkosi.

## Proposed design

### The seam first, with Ubuntu alone behind it

A change of its own, before any Fedora code, and it changes no byte of any
Ubuntu image: `offline-identical`, `no-build-leaks`, `certificates` and
`capture-roundtrip` pass unchanged. Behind the seam go what the two
families do differently:

- bootstrapping the image and writing the tarball (mmdebstrap, or mkosi);
- a package's identity in the lock and the index vendor reads it from
  (apt's `Packages` and `.deb` file names, or `repodata` and `.rpm`
  locations);
- the offline repository (the flat apt repository, or a `createrepo_c`
  directory);
- what "installed as a dependency" is read from and restored to (apt's
  `extended_states`, or dnf5's `packages.toml`);
- provisioning: the sudo group, how a locale is made, the trust store.

`internal/distro` grows from a table into the family and its releases:
`Lookup(family, release, arch)`.

### Fedora is built by mkosi, with a Fedora tools tree

mkosi does for Fedora what mmdebstrap does for Ubuntu here: it installs in
a user namespace with the distribution's own package manager, mounts what
the scripts need, and writes the tarball with numeric owners, extended
attributes and hardlinks. So the rule "only mmdebstrap writes the tarball"
becomes "only mmdebstrap or mkosi writes it", and Go still never tars,
walks or deletes a root filesystem.

The tools tree is a small Fedora root (bash, coreutils, util-linux, dnf5,
rpm, bubblewrap, tar, gzip, zstd, systemd, ca-certificates) that mkosi
makes for each build as a `directory` image, with the host's dnf 4 and
frostroot's package manager tree, in a user namespace like any
unprivileged mkosi build; its downloads stay in a package cache under the
work root, so only the first build of a release fetches them all. Its
versions change the bytes the way mmdebstrap's do, so the lock records
them and `vendor` keeps the tools tree's packages too, which makes an
offline rebuild independent of what the archive holds a year later. An
offline build makes the tools tree from those files, indexed by the
host's `createrepo_c`, which is why the tools tree itself needs none.
mkosi removes the tree when the build ends, however it ends (`mkosi -f
clean`): it belongs to the subordinate ids, and frostroot deletes no root
filesystem itself.

frostroot hands mkosi a package manager tree of its own: the
repositories, each with the pinned key file beside it, so that mkosi
never writes its own or fetches a key. mkosi writes an uncompressed tar,
and the host's `gzip -n` compresses it, because mkosi's own gzip stamps
the header with the time. Every image holds dnf5, and
`CleanPackageMetadata=no` keeps rpm's database and dnf5's state.

After the install, in mkosi's finalize script: read what rpm installed and
what dnf5 recorded, then remove dnf5's transaction history, rpm's and the
history's SQLite side files, ldconfig's aux-cache, dnf5's
`system-repo.lock`, the build's resolver file, and `/var/log/dnf5.log`,
which the script's own dnf5 queries write into the image whatever dnf5 is
told (mkosi removes it after its own runs, and the queries come later). The build runs with
umask 022 whatever the caller's. Weak dependencies are installed, as
Fedora's dnf does and as frostroot's Ubuntu builds install recommends,
and documentation is kept, as on Ubuntu.

### The recipe

`[image] distro = "fedora"`, `release = "44"`, `arch = "amd64"` (written
`x86_64` wherever rpm reads it). A recipe without `distro` is Ubuntu. A
Fedora image starts from frostroot's lean WSL base (decision 6), and the
recipe's `[packages]` add to it. The
rest of the recipe keeps its meaning; on Fedora `sudo = true` puts the user
in `wheel` with passwordless sudo, and `[locale] lang` installs
`glibc-langpack-<language>`. A new recipe field needs a form field, a
mapping in `FromRecipe` and `ToRecipe`, and a line in the template;
AGENTS.md's rule holds.

### The form

The owner chose to offer Fedora in `init` and `edit` from the first
version, so a Fedora image never needs its recipe written by hand.

- **The distribution comes first**, then its releases: Ubuntu's four, and
  Fedora's that the table knows (44 at first). A new recipe still starts on
  Ubuntu's newest release.
- **A Fedora package catalog**, as the Ubuntu one: the names a lab asks
  for, as Fedora spells them (`gcc` and `make` rather than
  `build-essential`, `python3` without `python3-venv`), each checked to
  exist in every Fedora release the table knows, as
  `TestIntegrationCatalogExistsInEveryRelease` does for Ubuntu.
- **The picker searches Fedora's index.** Fedora 44 publishes its package
  list as `repodata/…-primary.xml.zst`, 15.7 MB compressed and 185 MB
  open, and `repomd.xml` declares both sizes, so the read is bounded before
  and after decompression as the apt index is. It is reduced to names and
  summaries and cached, per release and repository. `internal/index` then
  imports zstd, which AGENTS.md's package boundaries have to say.
  `repomd.xml` is not signed; the index only suggests names, as it does
  today, and `internal/builder` still never imports it.
- **The pages a Fedora recipe cannot use are left out**: third-party
  sources, Python and certificates, until each has a Fedora version.
- **The plain interface keeps its questions in their order**, because
  people pipe answers into it. A new first question would shift every
  answer after it, so `init --plain` takes the distribution as a flag,
  `--distro fedora`, and asks nothing new; the full-screen form has the
  field.

### The lock

`version = 1` stays, and every new field is optional and `omitempty`. A
Fedora lock says `distro = "fedora"`, the release, the repositories
actually used (URL and key checksum, as `LockRepository` does), and for
every package its name, epoch-version-release, rpm architecture, SHA-256,
size, the repository it came from, its path below that repository
(`Packages/<letter>/<file>`), the source rpm it was built from (which
names it in Koji, where `vendor` falls back to) and the reason dnf5
recorded for it: `User`, `Dependency` or `Weak Dependency`. `[[tools]]`
lists the tools tree's packages the same way, without a reason. The key rpm imports (`gpg-pubkey`) is not a package. A lock
is still read as hostile input, and one that mixes families is refused.

### Trust

- Fedora's primary key for each supported release is pinned in frostroot
  by fingerprint, as the sources catalog pins third-party keys, and every
  primary key in the key file must be pinned. `build` never fetches keys.
- The lock's SHA-256 decides what an offline build trusts, as on Ubuntu.
  Online, the packages' own signatures and the metalink's checksum of
  `repomd.xml` over HTTPS do; `--ca-bundle` and `--insecure` reach the
  tools tree's dnf5 the way they reach apt, and never the image.

### vendor and build --offline

`vendor` downloads each locked `.rpm` from its repository, or from Koji
once the updates repository has replaced it, and checks its SHA-256 before
renaming it into `vendor/rpms/`, one directory for the image's packages
and the tools tree's alike (a file name is unique across a release's
repositories, and the two share many files). `build --offline` stages the
files, hard-linked where it can, as local repositories: for each of
mkosi's two builds, one per repository of the release under its online
name, holding only that build's packages. The host's `createrepo_c`
indexes them, mkosi binds the directory above them into its sandbox
(`--local-mirror`), and both builds ask for every locked package as
`name-version.arch`, with the network repositories off, the key still
checking every package, the lock's instant as `SOURCE_DATE_EPOCH`, a
package cache of the build's own (removed with the tools tree, `mkosi
-ff clean`), and dnf5's reasons put back from the lock. The image and the
tools tree are then compared with the lock, each package's repository
included. Two offline builds of one lock must be byte-identical, as
on Ubuntu; the offline measurement above found them so, and found them
byte-identical with the online build too, which frostroot measures but
does not promise.

### What the implementation found

- **dnf5 logs into the image.** The finalize script's dnf5 queries wrote
  `/var/log/dnf5.log`, dated now and naming the workspace, so two offline
  rebuilds differed by it, 786 bytes against 791; online it also held the
  mirror list. The script removes it.
- **mkosi reads two variables of the host's.** `MKOSI_DNF` names the
  package manager and `MKOSI_INTERPRETER` the Python of mkosi's helpers;
  the wrapper that sets mkosi's umask unsets both. mkosi hands everything
  it runs an environment of its own besides.
- **Ubuntu's kernels keep mkosi out of its user namespace.** Since 23.10
  they let a program use the user namespace it makes only when an
  AppArmor profile of its own allows it; Ubuntu 24.04 has one for
  mmdebstrap and none for mkosi. CI's `ubuntu-24.04` runner found it, in
  a traceback; WSL's kernel, Microsoft's, has no such switch, and neither
  did the container every earlier run was made in. `Preflight` refuses
  such a host unprivileged, naming root and `sysctl` as the ways out, and
  CI lifts the restriction to be what WSL is.
- **The package cache belongs to the subordinate ids** when mkosi runs
  unprivileged, so the user cannot `rm -rf` it; `unshare --map-auto
  --map-root-user rm -rf` can, and the README says so.
- **Measured on one Fedora 44 lock** of git in `tr_TR.UTF-8`, 268
  packages and a 122-package tools tree: two offline rebuilds as root under
  `unshare --net` and one as uid 1001 wrote one SHA-256 in 54 to 57 s, and
  an online build at the lock's instant wrote the same lock and the same
  tarball.

## Risks

- **Size.** Drawing the seam touches the builder, the recipe, the lock,
  the pool and the command line, which is where regressions of the Ubuntu
  byte-identity would come from. The seam is its own pull request, and the
  scenarios above gate it.
- **mkosi's versions.** Ubuntu 24.04 has 20.2; newer releases have a mkosi
  whose options have moved. frostroot would check the version it supports
  in `Preflight`, or run the mkosi packaged in the Fedora tools tree, which
  the lock then pins.
- **Fedora's short life.** A release gets about 13 months, then its
  packages move to `archives.fedoraproject.org`. Offline rebuilds don't
  care; online builds of an old recipe need the archive's URL, as 20.04
  needed its pockets checked.
- **WSL's first boot of a Fedora image** is not measured; `wsl-boot` has
  learned Fedora (`E2E_DISTRO=fedora`), and runs only on Windows.
- **A native Ubuntu host** builds Fedora as root, or with its AppArmor
  restriction lifted (see "What the implementation found").
- **Two families double the verification** every release: each scenario
  runs for both.

## Decisions (answered by the owner on 2026-09-28)

1. **What the Fedora images are for: lab images for WSL**, the use
   frostroot has for Ubuntu. Fedora 44 first.
2. **mkosi with a Fedora tools tree builds them and writes the tarball**,
   and the rule becomes "only mmdebstrap or mkosi writes the tarball".
   Approach B by hand would have meant frostroot owning the mounts, the
   users and the tar that mkosi already gets right.
3. **Offline rebuilds are byte-identical from the first Fedora version.**
   The spike found five removable files between two builds; the offline
   path is the first thing the implementation measures.
4. **The form offers Fedora from the first version** (see "The form"),
   beside packages, the user, locale, timezone, `wsl.conf`, the lock,
   `vendor` and `build --offline`. It is the larger of the two choices
   offered: a Fedora catalog to keep, a second index format with zstd in
   `internal/index`, and more frames to record and read.
5. **The seam first**, as a pull request of its own that changes no byte
   of any Ubuntu image, then Fedora.
6. **A Fedora image starts from a lean WSL base** (answered after the
   seam merged): systemd, sudo, dnf5 and the core command-line tools, 188
   packages and 347 MiB, rather than `@core`'s 367 and 599 MiB, whose
   NetworkManager, firewalld, zram swap and sshd a WSL distribution does
   not use and partly fights. The cost is a list frostroot keeps.
