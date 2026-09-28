# frostroot v0.15 Implementation Plan: Fedora

Fedora 44 as a second family beside Ubuntu, for lab images under WSL. The
spec, with the spike this plan rests on and the owner's decisions, is
[`2026-09-28-frostroot-fedora.md`](../specs/2026-09-28-frostroot-fedora.md).
The work goes in three pull requests, one after the other from the
session's branch, each opened once the one before has merged. Every task
ends with `scripts/check.sh` clean and its own commit as `alone141`
following `CONTRIBUTING.md`; a fix inside a task comes with a test that
fails without it, proven with `scripts/mutate.sh`.

**Goal:** `distro = "fedora"`, `release = "44"` builds a WSL tarball
online, `vendor` fetches what its lock names, two offline rebuilds of the
lock are the same bytes, `init` and `edit` offer Fedora with a catalog and
package search of its own, and no Ubuntu recipe, lock or image changes by
a byte.

## The decisions this records

- **The five the owner answered** on 2026-09-28, in the spec: lab images
  for WSL; mkosi with a Fedora tools tree; byte-identical offline rebuilds
  from the first version; the form from the first version; the seam first.
  And a sixth, after the seam merged: a lean WSL base, not `@core`.
- **A recipe without `distro` is Ubuntu**, so every recipe and lock
  written so far stays valid and means what it meant.
- **The tools tree is part of what the lock pins.** Its dnf5, rpm and
  mkosi change the bytes the way mmdebstrap does for Ubuntu, so the lock
  records its packages and `vendor` keeps them; an offline build installs
  the tools tree from `vendor/` too.
- **Weak dependencies are installed**, as Fedora's dnf does and as
  frostroot's Ubuntu builds install recommends. mkosi's default is off.
- **The plain interface asks nothing new.** `init --plain --distro fedora`
  chooses the family; piped answers keep working.

## What is not done

- Third-party repositories, `[python]`, `[certificates]` and `capture` for
  Fedora; the form leaves out their pages for a Fedora recipe.
- A Fedora build host. The host is Ubuntu, usually in WSL.
- The first boot under WSL needs `wsl.exe`: `wsl-boot` learns Fedora in
  pull request 2, and the owner runs it on Windows before the release.

## Pull request 1: the seam, with Ubuntu alone behind it

No byte of any Ubuntu image changes, and the proof is a rebuild: a lock
made by `master`'s binary rebuilt offline by the branch's must give the
same SHA-256.

1. **Spec and plan.** The spec with the spike's results and the owner's
   decisions, and this file. `docs: the Fedora spike, and a spec for the
   owner's approval`, then `docs: the owner's decisions and the Fedora
   plan`.
2. **Families in `internal/distro`.** `Lookup(family, release, arch)`,
   `SupportedVersions(family)` and the other readers take the family;
   Ubuntu's table is unchanged; an unknown family is an error that names
   the ones there are. Tests: every existing case with `"ubuntu"`, and the
   unknown family.
3. **`[image] distro` in the recipe.** Optional, `"ubuntu"` when absent;
   validated against `distro`'s families; the lock's `distro` comes from
   the recipe instead of the constant; a lock whose family is not the
   recipe's is refused with `ErrLockMismatch`. The form carries the field
   so that `edit` cannot drop it, with Ubuntu its only choice until pull
   request 3; `FromRecipe`, `ToRecipe` and the template in
   `internal/cli/init.go` say it. `TestRecipeRoundTrip` and
   `TestFormBindingCoversEveryField` cover it.
4. **The builder asks the family.** What the Ubuntu path does today, the
   source lines, the apt hooks, dpkg's status, apt's marks, the flat
   repository and the provisioning script, moves behind an `ubuntu` family
   implementation, and `Build` and `vendor` ask the recipe's family for
   each. Unit tests unchanged, or changed only in how they name the
   family.
5. **Proof, and the record.** `scripts/check.sh`; `scripts/integration.sh`;
   `offline-identical`, `no-build-leaks`, `certificates` and
   `capture-roundtrip`; and the rebuild across binaries above. The README's
   Verification paragraph says what ran.

## Pull request 2: building Fedora

6. **Fedora in the table.** Release 44: the `fedora` and `updates`
   repositories (metalink and base URL), and the primary keys of 43 to 46
   pinned by fingerprint, every primary key in the file checked as the
   sources catalog does.
7. **Preflight for Fedora.** mkosi in a version frostroot knows (20.2),
   the host's `dnf` and `rpm`, `createrepo_c`, bubblewrap, and `newuidmap`
   and `newgidmap` in unshare mode, each missing one a sentinel error with
   an install hint that `reportBuildFailure` maps to exit 1.
8. **The tools tree.** Made by mkosi for each build as a `directory`
   image with the host's dnf 4 and frostroot's package manager tree, its
   downloads kept in a package cache under the work root: bash,
   coreutils, util-linux, dnf5, rpm, mkosi's helpers (bubblewrap, tar,
   gzip, zstd, systemd) and ca-certificates. Its packages go into the
   lock, optional and `omitempty`; offline it is made from `vendor/`,
   indexed by the host's `createrepo_c`. mkosi removes it when the build
   ends (`mkosi -f clean`), because it belongs to the subordinate ids.
9. **The mkosi bootstrapper.** frostroot writes mkosi's configuration into
   the work directory (distribution, release, an uncompressed tar, weak
   dependencies and documentation on, `CleanPackageMetadata=no`,
   `SOURCE_DATE_EPOCH`, the lean base and the recipe's packages) and a
   package manager tree of its own (the repositories, the pinned key
   beside them), runs mkosi with the tools tree and umask 022, provisions
   in a post-installation script (the user in `wheel` with the sudoers
   drop-in, `glibc-langpack-<language>` and `/etc/locale.conf`,
   `/etc/localtime`, `wsl.conf`), reads rpm's database and dnf5's state and
   then removes, in a finalize script, dnf5's transaction history, the
   SQLite side files, ldconfig's aux-cache, dnf5's `system-repo.lock` and
   the build's resolver file, and compresses the tar with the tools tree's
   `gzip -n`. Every value that reaches a script is validated and quoted,
   and no script swallows a failure, a pipe included. Progress reads
   mkosi's steps and dnf5's `[n/N] Installing` lines into the existing
   phases, and the #102 watch has its mkosi counterpart if mkosi can
   install unmounted.
10. **The Fedora lock.** Every installed package's name, epoch, version,
    release and rpm architecture from the image's rpm database, the key
    rpm imported left out; its SHA-256, size and location from the
    repository's `primary` metadata for that exact package; its
    repository, from `nevras.toml`, and dnf5's reason for it (`User`,
    `Dependency` or `Weak Dependency`), from `packages.toml`; the
    repositories and key checksums used. New fields optional; a lock is
    hostile input.
11. **`vendor` for Fedora.** One `vendor/rpms/`, the tools tree's
    packages included, each file once; bounded reads with timeouts; the
    SHA-256 checked before the rename into place; Koji, by the source rpm
    the lock records, for an update the repository has replaced;
    `--prune` as on Ubuntu. (Built as a flat directory: a file name is
    unique across a release's repositories, and the lock says which each
    came from.)
12. **`build --offline` for Fedora.** The vendored files staged as local
    repositories, one per repository of the release under its online
    name for each of mkosi's two builds, indexed by the host's
    createrepo_c and bound into mkosi's sandbox; mkosi with the network
    repositories off, every locked package as `name-version.arch`, a
    package cache of the build's own, the lock's instant and dnf5's
    reasons put back; the image and the tools tree compared with the lock
    afterwards, each package's repository included.
13. **Tests.** Unit: the configuration and scripts rendered for hostile
    values and run through a real `sh`; the lock's new fields. Integration:
    `TestIntegrationFedoraTiny`, the structure checks of `testTinyImage`
    (owners, capabilities, hardlinks, the user, sudo, no build leaks), and
    `TestIntegrationFedoraOfflineRebuild`, two offline rebuilds and one
    SHA-256; `scripts/integration.sh` requires both, and CI installs
    `mkosi`, `dnf`, `rpm`, `createrepo-c`, `bubblewrap` and `uidmap` on
    `ubuntu-24.04` by name, with the AppArmor restriction on user
    namespaces lifted, as WSL has none.
14. **Scenarios.** `E2E_DISTRO=fedora` for `offline-identical`,
    `no-build-leaks` and `failed-build`; `wsl-boot` learns a Fedora image's
    first login, for the owner to run.

## Pull request 3: the form

15. **Distribution before release.** The form's first page chooses the
    family, then its releases; a Fedora recipe gets no Sources, Python or
    Certificates page; `init --plain --distro fedora`; the template; the
    golden frames re-recorded and read.
16. **The Fedora catalog.** The packages a lab asks for, as Fedora names
    them, and an integration test that each exists in every Fedora release
    the table knows.
17. **Searching Fedora's index.** `internal/index` reads `repomd.xml` and
    `primary.xml.zst`, bounded by the sizes `repomd.xml` declares before
    and after decompression, reduces them to names and summaries, and
    caches them per release and repository; the picker uses it for a
    Fedora recipe. AGENTS.md's package boundaries say `index` imports
    zstd.
18. **Docs and the release.** The README (Fedora in Quick start, Scope and
    the recipe; the Verification paragraph), `build`'s and `init`'s usage
    text, AGENTS.md's rules (who writes the tarball; the boundaries), and
    `builder.Version` 0.15.0.
