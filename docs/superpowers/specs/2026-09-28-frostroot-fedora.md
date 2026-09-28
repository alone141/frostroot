# frostroot v0.15: Fedora

Date: 2026-09-28
Status: **draft for the project owner's approval.** The owner asked for
Fedora ("do fedora") after the pros and cons of #8 were laid out, and the
spike below ran on 2026-09-28 without changing frostroot's code. Nothing is
built until the owner approves the design and answers the decisions at the
end.
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

## Non-goals, for the first Fedora version

- Third-party repositories (COPR, RPM Fusion), `[python]`,
  `[certificates]`, `capture`, the package picker and the form's Fedora
  catalogs. Each is a follow-up once the base holds.
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

**Not measured yet.** An offline build from a local package set (mkosi's
`--package-directory` with the network repositories off), a first boot
under WSL (`wsl-boot`, on Windows), gzip output for `wsl --import`, the
POSIX ACLs systemd-tmpfiles sets on `/var/log/journal` (this host's tar
could not apply them on extract) as WSL imports them, a cold tools tree,
and Ubuntu 26.04's newer mkosi.

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

The tools tree is a small Fedora root (dnf5, rpm, bubblewrap, tar, gzip,
zstd, systemd, createrepo_c) that frostroot installs with the host's dnf 4
the first time a release is built, and keeps in its cache. Its versions
change the bytes the way mmdebstrap's do, so the lock records them and
`vendor` keeps the tools tree's packages too, which makes an offline
rebuild independent of what the archive holds a year later.

After the install, in mkosi's post-installation hook: remove dnf5's
transaction history, `rpmdb.sqlite-shm`, ldconfig's aux-cache,
`/var/lib/libdnf5/system-repo.lock` and the build's resolver file. The
build runs with umask 022 whatever the caller's. Weak dependencies are
installed, as Fedora's dnf does and as frostroot's Ubuntu builds install
recommends.

### The recipe

`[image] distro = "fedora"`, `release = "44"`, `arch = "amd64"` (written
`x86_64` wherever rpm reads it). A recipe without `distro` is Ubuntu. The
rest of the recipe keeps its meaning; on Fedora `sudo = true` puts the user
in `wheel` with passwordless sudo, and `[locale] lang` installs
`glibc-langpack-<language>`. A new recipe field needs a form field, a
mapping in `FromRecipe` and `ToRecipe`, and a line in the template;
AGENTS.md's rule holds.

### The lock

`version = 1` stays, and every new field is optional and `omitempty`. A
Fedora lock says `distro = "fedora"`, the release, the repositories
actually used (URL and key checksum, as `LockRepository` does), and for
every package its name, epoch-version-release, rpm architecture, SHA-256,
size, location below its repository and whether dnf5 installed it as a
dependency. A lock is still read as hostile input, and one that mixes
families is refused.

### Trust

- Fedora's primary key for each supported release is pinned in frostroot
  by fingerprint, as the sources catalog pins third-party keys, and every
  primary key in the key file must be pinned. `build` never fetches keys.
- The lock's SHA-256 decides what an offline build trusts, as on Ubuntu.
  Online, the packages' own signatures and the metalink's checksum of
  `repomd.xml` over HTTPS do; `--ca-bundle` and `--insecure` reach the
  tools tree's dnf5 the way they reach apt, and never the image.

### vendor and build --offline

`vendor` downloads each locked `.rpm` from its repository and checks its
SHA-256 before renaming it into `vendor/rpms/`. `build --offline` hands
mkosi that directory with the network repositories off, the lock's
instant as `SOURCE_DATE_EPOCH`, and the lock's dependency marks to
restore. Two offline builds of one lock must be byte-identical, and the
first task of the implementation measures it.

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
  to learn Fedora before the first release that offers it.
- **Two families double the verification** every release: each scenario
  runs for both.

## Decisions for the owner

1. **What the Fedora images are for.** The spike assumed Fedora lab images
   on WSL; groundwork for Alma or Rocky would change which releases come
   first.
2. **mkosi as Fedora's bootstrapper and tarball writer**, with the rule
   "only mmdebstrap or mkosi writes the tarball". Recommended; approach B
   by hand would mean frostroot owning the mounts, the users and the tar
   that mkosi already gets right.
3. **Byte-identical offline rebuilds from the first Fedora version.**
   Assumed yes: the spike found five removable files between two builds.
4. **The first version's scope**: packages, user, locale, timezone,
   `wsl.conf`, the lock, `vendor` and `build --offline`, and whether `init`
   and `edit` offer Fedora in it or Fedora recipes are written by hand at
   first.
5. **The order**: the seam as its own pull request, with no change to any
   Ubuntu output, then Fedora.

With these answered, the task list goes into
`docs/superpowers/plans/2026-09-28-frostroot-fedora.md` before any code.
