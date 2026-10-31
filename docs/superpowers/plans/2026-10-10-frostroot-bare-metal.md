# frostroot v0.16 Implementation Plan: bootable disk images

A recipe's image as a disk that boots a PC or a VM under Secure Boot, for
Ubuntu and Fedora. The spec, with the desk research, the owner's decisions
and the spike of the Ubuntu half this plan rests on, is
[`2026-10-09-frostroot-bare-metal.md`](../specs/2026-10-09-frostroot-bare-metal.md).
The work goes in five pull requests, one after the other, each opened once
the one before has merged. Every task ends with `scripts/check.sh` clean
and its own commit as `alone141` following `CONTRIBUTING.md`; a fix inside
a task comes with a test that fails without it, proven with
`scripts/mutate.sh`. The owner asked for CI to be switched off for now;
while it is off, each pull request lists what ran by hand, and says once
that CI could not run.

**Goal:** `[image] target = "disk"` builds a raw GPT image, and a qcow2 when
the recipe asks, that boots with Secure Boot on and Microsoft's keys, asks
for the user's password on the first boot, grows to fill its disk, and
rebuilds offline byte for byte; and no WSL recipe, lock or tarball changes
by a byte.

## The decisions this records

- **The six the owner answered** on 2026-10-10, in the spec: the first-boot
  prompt on the screen; the vhdx in a later version; the root grown at the
  first boot; the 2011-signed shim, recorded in the lock; Fedora's own
  `blscfg` boot entries if kernel-install runs in the build, a static
  `grub.cfg` otherwise; the spike before this plan.
- **And the four before them**: a raw disk with qcow2 (and vhdx, later);
  Ubuntu and Fedora in one spec; UEFI with Secure Boot through each
  distribution's signed shim and GRUB; the password set at first boot.
- **A recipe without `target` is a WSL recipe**, so every recipe and lock
  written so far stays valid and means what it meant.
- **One disk path for both families**: the family's build makes the image
  tarball as today, and tools from a disk tools tree make the disk. The
  rule in AGENTS.md becomes "only mmdebstrap, mkosi or the disk tools write
  the image".
- **The disk tools tree is part of what the lock pins**, as Fedora's tools
  tree is: its packages go into the lock, `vendor` keeps them, and an
  offline build makes the tree from `vendor/`.
- **The plain interface asks nothing new.** `init --target disk` answers
  the target, as `init --distro` answers the family.

## What is not done

- The vhdx (decision 2), an ISO, legacy BIOS, arm64, `capture` of a
  bare-metal machine, encrypted or LVM disks, cloud-init.
- Updating shim or GRUB on a machine already deployed from an image.
- A real PC with Secure Boot on is the owner's check, before the release,
  as `wsl-boot` is on Windows.

## Pull request 1: the target, with nothing built yet

No byte of any WSL recipe, lock or tarball changes; a disk recipe validates
and round-trips through the form, and `build` refuses it with an error that
says the disk target comes in a later version (exit 1).

1. **Spec and plan.** The spec with the spike's results and the owner's
   decisions, and this file. (These two ride pull request #108, at the
   owner's choice, and are not repeated here.)
2. **`[image] target` and `[disk]` in the recipe.** `target` optional,
   `"wsl"` when absent, `"disk"` the other value; `[disk]` a pointer table
   with `size` (optional, a size with a unit, at least the content's) and
   `formats` (a subset of `["qcow2"]`; `"vhdx"` refused with a message
   naming the later version). `validate` refuses `[disk]` in a WSL recipe,
   and a `[wsl]` that says anything in a disk recipe. (As built: `[wsl]`
   stays a plain table rather than becoming a pointer, which ten readers
   would have had to learn; an absent table and one whose settings are all
   off are the same to a WSL recipe today, so a disk recipe is refused only
   one that says something.) Tests: every value, both refusals, every
   existing recipe fixture unchanged by a load and a write.
3. **The lock's new fields.** `target`, a `[disk]` table (`seed`, the
   disk's and each partition's size in bytes, `shim`) and `disk_tools`, all
   optional and `omitempty`; `version = 1` stays; an unknown field is still
   an error. A lock whose target is not the recipe's is refused with
   `ErrLockMismatch`, before anything else in it is read. (As built: a lock
   is hostile input, so its `[disk]` is checked where it is first read,
   in pull request 3: the seed a UUID, the sizes bounded, `shim` a name
   frostroot knows. Until then nothing reads it.) Tests: every older lock
   fixture loads and writes back byte for byte.
4. **The target in the form.** `KeyTarget` on the Image page after the
   release; a Disk page (`size`, `formats`) shown only for the disk target
   and the WSL page only for the WSL target, with `Field.Targets` beside
   `Field.Families`; `ToRecipe` leaves the other target's answers out, and
   an `edit` that switches the target carries every answer across.
   `FromRecipe`, `ToRecipe` and the template in `internal/cli/init.go` say
   it. `TestRecipeRoundTrip` and `TestFormBindingCoversEveryField` cover it;
   the golden frames are re-recorded with `FROSTROOT_UPDATE_FRAMES=1` and
   read.
5. **`init --target`, and the plain interface unchanged.** `init --target
   disk` answers the target for both interfaces; the plain interface's
   questions and their order stay; its test pipes the same answers as
   before. Usage text and README say it.
6. **Proof.** `scripts/check.sh`; `tui` and `tui` with
   `E2E_DISTRO=fedora`; a lock made by `master`'s binary rebuilt offline by
   the branch's gives the same SHA-256.

## Pull request 2: the disk tools tree

7. **The tree.** mmdebstrap makes a 26.04 directory tree,
   `--variant=essential`, with `e2fsprogs`, `libarchive13t64`,
   `dosfstools`, `mtools`, `fdisk`, `qemu-utils`, `tar` and `coreutils`, in
   the build's work directory, removed when the build ends whatever
   happens. Online it reads the archive frostroot already uses for 26.04;
   its packages go into the lock's `disk_tools`. (As built: the tree is
   made before the image, since it takes a minute where the image takes
   several, and a host that cannot make it fails before the long step; it
   has a work directory of its own under the build's, `disk-tools/`, with
   mmdebstrap's log, TMPDIR and the stage the lock is written from; its apt
   trusts what the image's apt trusts, `--ca-bundle` and `[certificates]`,
   which the family build says in `buildTrustPEM`; and the lock names
   `target = "disk"` from this pull request on, which pull request 1's
   offline check already reads. A failed removal fails the build before
   anything lands in `dist/`, as mkosi's does.)
8. **Running a tool in it.** One function runs a tool with bubblewrap, the
   tree read-only as `/`, the work directory bound read-write at `/work`,
   and an environment of its own: `PATH`, `LC_ALL=C.UTF-8`, `TZ=UTC`,
   `SOURCE_DATE_EPOCH`, `MTOOLS_SKIP_CHECK=1`, nothing from the host's.
   Preflight checks for `bwrap` (a sentinel error with an install hint,
   exit 1 through `reportBuildFailure`). Tests: the argument list for
   hostile work paths; a fake runner in the builder's tests. (As built:
   bwrap binds the tree's `/usr` and `/etc` read-only onto its own root,
   with the merged-`/usr` links beside them, rather than the tree as `/`,
   because a mount point for `/work` cannot be made under a read-only root.
   The runner also removes the tree, with `rm` in a user namespace that
   maps the subordinate ids again, `unshare --map-root-user --map-auto`, as
   mmdebstrap's manual says to; its preflight asks for `unshare` and
   uidmap's helpers too, unprivileged.)
9. **`vendor` and the offline tree.** `vendor` fetches `disk_tools` into
   `vendor/debs/` beside the image's packages, checked by SHA-256 before
   the rename; `build --offline` makes the tree from the pool and compares
   it with the lock afterwards. (As built: the manifest lists the image's
   files and then the tree's, once each, a file both hold with one checksum
   being one file; the tree's local repository holds the tree's files and
   the image's the image's, so that a 24.04 image's apt never sees the
   tree's versions; and the tree is compared with the lock as soon as it is
   made, before the image is built with it.)
10. **Integration.** `TestIntegrationDiskToolsTree`: the tree is made,
    each tool answers `--version` through the runner, and an offline tree
    from a vendored lock holds the same packages. `scripts/integration.sh`
    requires it. (As built: the test also runs `mkfs.fat` on a file under
    `/work` and `env`, which prove the work directory is bound read-write
    and the environment is frostroot's five variables alone; the script
    fails on a skip for want of bubblewrap too.)

## Pull request 3: the Ubuntu disk

11. **The image's additions.** For a disk recipe, the packages of the
    spec's "What the image gains" (on 26.04 `systemd-repart` too), and the
    last customize hook: `fstab` with `x-systemd.growfs`, `repart.d`'s
    `50-root.conf`, `/boot/grub/grub.cfg` from a template with
    `console=ttyS0 console=tty0`, the user's `!unprovisioned` field and
    root locked, the set-password script and unit (silencing PID 1 while
    it asks) linked under `/usr/lib/systemd/system/multi-user.target.wants`,
    netplan's DHCP file (mode 0600), the resolver link, `locale.conf` and
    `vconsole.conf`, the per-machine deletions, `machine-id` as
    `uninitialized`, and on 24.04 an `ssh-keygen -A` unit when
    openssh-server is in the image. mmdebstrap's `cleanup/reproducible` is
    skipped and its seven deletions done in the hook; no `wsl.conf`. Every
    value is validated and quoted, and the hook is run through a real `sh`
    with hostile paths. `TestHooksAndScriptNeverSwallowFailures` covers it.
12. **The identifiers.** From the lock's seed by HMAC-SHA256, as
    systemd-repart derives them: the disk GUID, each partition's UUID, the
    root filesystem's UUID and hash seed, the ESP's serial. An online build
    draws the seed and the lock freezes it; an offline build checks the
    lock's `[disk]` as hostile input first (the seed a UUID, the sizes
    bounded, `shim` a name frostroot knows). Tests: known vectors; every
    value a valid UUID; offline takes the lock's; each bad `[disk]` refused.
13. **The assembly.** Through the tools runner: the three ESP binaries out
    of the tarball by their member names (the shim's from the lock),
    `mke2fs -d` with `-U`, `-E nodiscard,lazy_itable_init=1,hash_seed=` and
    a `MKE2FS_CONFIG` frostroot writes, `mkfs.fat -F 32 --invariant -i`,
    `mmd` and one `mcopy` per file in sorted order, the stub `grub.cfg` in
    `EFI/BOOT/` and `EFI/ubuntu/`, the `sfdisk` script with `label-id:` and
    `uuid=`, `dd conv=notrunc,sparse` at each offset; the root sized as the
    tarball plus a quarter plus 256 MiB, or `[disk] size`. The tarball is a
    work file, never an output. Tests: the rendered `sfdisk` script,
    `grub.cfg`s and `MKE2FS_CONFIG`; the order of tool calls against a
    fake.
14. **The outputs.** `dist/<name>-ubuntu-<release>-amd64.img` staged with
    `export.Stage`, the lock renamed, then the image; a failure between
    puts both back. `export`'s cross-device copy keeps holes
    (`SEEK_DATA`/`SEEK_HOLE` on Linux; the Windows build still compiles).
    Tests: a sparse file copied across a fake device boundary stays
    sparse; an interrupted placement leaves the previous image and lock.
15. **Integration.** `TestIntegrationResoluteDisk`: a disk is built, its
    GPT read back with `sfdisk --dump`, both filesystems checked read-only
    with `fsck`, the ESP listed, and the root's owners, capabilities and
    hardlinks checked as `testTinyImage` does; `TestIntegrationDiskOfflineRebuild`:
    two offline rebuilds, one SHA-256. `scripts/integration.sh` requires
    both.
16. **Scenarios.** A new `disk-boot`: QEMU and OVMF with Microsoft's keys,
    the password passed as a hashed credential, a login on the serial line,
    and `mokutil --sb-state`, `systemctl is-system-running`, the grown root,
    DHCP and no host keys checked; and with the snakeoil variables, the
    firmware refusing the disk. `offline-identical`, `no-build-leaks` and
    `failed-build` learn `E2E_TARGET=disk`. `scripts/e2e/README.md` and
    AGENTS.md's list say which scenarios a disk change runs.

## Pull request 4: the Fedora disk

17. **A spike of the Fedora half first**, recorded in the spec: whether
    kernel-install can run in mkosi 20.2's finalize step with
    `/etc/kernel/entry-token` and `/etc/kernel/cmdline` (decision 5), the
    exact paths `shim-x64` and `grub2-efi-x64` install, the prefix Fedora's
    signed GRUB reads, `dracut --no-hostonly --reproducible` with one zstd
    thread, and two builds compared.
18. **The image's additions for Fedora.** The packages, `fstab`,
    `repart.d`, the boot entries (BLS or the static fallback), the
    initramfs, the user and the set-password unit, NetworkManager's DHCP,
    the per-machine deletions; every unit frostroot enables linked
    statically, out of reach of Fedora's first-boot `disable *` preset.
19. **The Fedora ESP.** The binaries from the tarball's
    `/boot/efi/EFI/fedora/`, the stub in `EFI/BOOT/` and `EFI/fedora/`, the
    rest of the assembly shared with Ubuntu.
20. **Integration and scenarios.** `TestIntegrationFedoraDisk` and its
    offline rebuild; `disk-boot`, `offline-identical`, `no-build-leaks` and
    `failed-build` with `E2E_DISTRO=fedora E2E_TARGET=disk`.

## Pull request 5: the qcow2, and the release

21. **The qcow2.** `qemu-img convert -O qcow2` through the tools runner
    when `formats` names it, staged and placed with the image; its SHA-256
    compared in `offline-identical`.
22. **Docs.** The README: a section "Disk images" (the target, the first
    boot, the credential for a headless VM, Secure Boot and the shim, what
    a deployed machine does not update), the recipe's fields, the host
    packages (`bubblewrap`), the scope, the Layout, the Documentation
    table's rows for the spec and this plan; AGENTS.md's rules ("only
    mmdebstrap, mkosi or the disk tools write the image") and scenarios;
    usage text in `internal/cli`.
23. **The record and the version.** The README's Verification section gets
    what ran; `builder.Version` is 0.16.0; pushing the tag stays the
    owner's, after their check on a real PC.
