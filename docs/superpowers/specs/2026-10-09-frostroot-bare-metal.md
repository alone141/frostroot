# frostroot v0.16: bootable disk images

Date: 2026-10-09
Status: **decisions answered by the owner on 2026-10-10, and the spike of
the Ubuntu half run the same day** (see "Spike results"); the plan is
next. The owner asked for bare-metal export ("work on
bare metal issue") after the pros and cons of #9 were laid out, and
answered four scoping questions: a raw disk image plus qcow2 and vhdx;
Ubuntu and Fedora in one spec; UEFI with Secure Boot through each
distribution's signed shim and GRUB; and the user's password set at first
boot. The owner then chose desk research for the first draft, so the
research below was read, not run: every statement about mkosi,
systemd-repart, e2fsprogs, dosfstools, mtools, GRUB, shim, QEMU, PAM or
systemd comes from those projects' documentation, source or package lists,
read on 2026-10-09, and the ones the documents themselves left open are
marked *unconfirmed*. On 2026-10-10 the owner took the recommended answer
to each of the six decisions at the end, which puts the vhdx in a later
version and a spike of the Ubuntu half before the plan.
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md)
(the extension point "Bare metal"),
[`2026-09-28-frostroot-fedora.md`](2026-09-28-frostroot-fedora.md) (the
seam, mkosi and its tools tree) and
[`2026-09-17-frostroot-reproducible.md`](2026-09-17-frostroot-reproducible.md)
(the frozen instant)
Implements: [issue #9](https://github.com/alone141/frostroot/issues/9)

## Why

frostroot is called that, and not something with WSL in it, because a
frozen root filesystem was always meant to reach more than WSL. A lab that
runs on real PCs or on VMs wants what a WSL lab has: a recipe, a lock that
says what the machine holds, and an image that rebuilds byte for byte
offline. Today it gets none of that from frostroot.

Issue #9 says "the builder is unchanged; only the export step gains an
implementation". That is not true. Every image frostroot builds is shaped
for WSL, which brings its own kernel and has no disks:

- No kernel, no initramfs, no bootloader, no `fstab`. `capture` strips
  `linux-image-*` and `grub-*` from what it finds
  (`internal/capture/packages.go`), and the Fedora build says `Bootable=no`
  (`internal/builder/fedoraconfig.go`).
- `/etc/wsl.conf` is uploaded into every image.
- The user has no password and logs in as WSL's default user.
- `/etc/machine-id` is empty in an Ubuntu image (mmdebstrap's reproducible
  cleanup), and an empty machine-id is not a first boot to systemd, so no
  first-boot unit would ever run on it.

A bootable image changes what goes into the image as well as what comes out
of the build.

## Goal

- `[image] target = "disk"` builds, from one recipe, a raw GPT disk image
  that boots on a typical UEFI PC **with Secure Boot on and Microsoft's keys
  enrolled**, and in a VM with the same firmware, for Ubuntu 26.04 and 24.04
  and for Fedora 44.
- The same build writes a qcow2 of that disk when the recipe asks. A vhdx
  follows in a later version (decision 2).
- The image holds no secret and no per-machine state. The user sets their
  password on the first boot.
- The lock records everything the disk was made from; `vendor` fetches it;
  `build --offline` rebuilds every output byte for byte.
- A build runs as an unprivileged user, or as root, as it does today.
- Every WSL recipe, lock and tarball stays exactly as it is: a recipe
  without `target` is a WSL recipe, and every offline rebuild of an existing
  lock produces the same bytes as before.

## Non-goals, for the first disk version

- An ISO, live or installer. The disk image is written to a USB stick or an
  SSD with `dd`, or attached to a VM.
- Legacy BIOS boot.
- Secure Boot with keys of our own: frostroot signs nothing and holds no
  key. The binaries that boot are the distribution's, signed by the
  distribution and by Microsoft.
- Updating the bootloader on a machine already deployed from an image (see
  Risks). The image is frozen; a new image is rebuilt from the recipe.
- `capture` of a bare-metal machine into a disk recipe.
- Encrypted disks, LVM, RAID, btrfs subvolumes, A/B partitions.
- cloud-init, and any network configuration beyond DHCP on wired
  interfaces.
- Architectures other than amd64.

## What a bootable image changes

| | WSL tarball (today) | Disk image |
|---|---|---|
| Kernel and initramfs | WSL's own | the distribution's, in the image |
| Boot | `wsl --import` | UEFI firmware → shim → GRUB → kernel |
| Partitions | none | GPT: an EFI system partition (vfat) and a root partition (ext4) |
| Login | WSL's default user, no password | a password the user sets at first boot |
| `wsl.conf` | uploaded | not written |
| machine-id | empty (Ubuntu) or `uninitialized` (Fedora) | `uninitialized`, so the first boot is one |
| SSH host keys, random seed | irrelevant | must not be in the image |
| Network | WSL's | DHCP on wired interfaces |
| Written by | mmdebstrap or mkosi | the tarball as today, then disk tools from a tools tree the lock pins |
| Size | a compressed tarball | a sparse raw file, plus an optional qcow2 |

## Desk research (2026-10-09)

### mkosi 20.2 cannot make the Secure Boot disk

frostroot pins mkosi 20.2, the version Ubuntu 24.04 ships, and refuses
another. Its `Format=disk` makes a GPT image with systemd-repart, and
`ShimBootloader=signed` puts the distribution's signed shim at
`EFI/BOOT/BOOTX64.EFI`. But in 20.2 every bootable path ends in a unified
kernel image that mkosi builds itself, unsigned or signed with a key of our
own; `Bootloader=grub` does not install GRUB, and only writes a `grub.cfg`
that chainloads those UKIs. Shim checks what GRUB hands it, so on firmware
that trusts only Microsoft the UKI is refused. Picking up the
distribution's already-signed GRUB and kernel arrived in mkosi v22
(`ShimBootloader=signed`, `UnifiedKernelImages=`), and `Bootloader=grub-signed`
in v25.
Sources: [mkosi.md at v20.2](https://raw.githubusercontent.com/systemd/mkosi/v20.2/mkosi/resources/mkosi.md),
`mkosi/__init__.py` at v20.2,
[mkosi.news.7](https://raw.githubusercontent.com/systemd/mkosi/main/mkosi/resources/man/mkosi.news.7.md).

Two more things rule out `Format=disk` for frostroot's Fedora:

- With the Fedora 44 tools tree, the disk would not be byte-identical.
  Fedora 44 has systemd 259; repart passes no ext4 `hash_seed` before 260
  and honours `SOURCE_DATE_EPOCH` only from 262, and mkfs.fat in dosfstools
  4.2 stamps the label with the wall clock.
- mkosi builds its default initrd as a second build from the repositories,
  which an offline build would have to vendor too, and it exports
  `KERNEL_INSTALL_BYPASS=1`, so the distribution's own initramfs is never
  made.

So mkosi keeps doing what it does today for Fedora, `Format=tar`, and the
disk is assembled after it, the same way for both families.

### Ubuntu: the signed shim and GRUB, and their maintainer scripts

- Packages: `linux-image-generic` (it brings `linux-modules-extra`,
  `linux-firmware` and microcode; `-virtual` lacks drivers for real PCs),
  `initramfs-tools` named explicitly (Ubuntu 26.04 also ships dracut, and
  which one a minimal install pulls in is *unconfirmed*), `grub-efi-amd64`,
  `grub-efi-amd64-signed`, `shim-signed`. Not `grub-pc`, which conflicts.
- Signed binaries, on 24.04 and 26.04:
  `/usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed` and
  `/usr/lib/shim/shimx64.efi.signed.latest`, with `mmx64.efi` beside it.
  Not the `shimx64.efi.signed` symlink: its postinst picks the target with
  update-alternatives from the **build host's** EFI variables and
  `uname -r`.
- shim-signed's master branch (no release yet) replaces `.latest` and
  `.previous` with `.2023` (shim 16.1, signed by Microsoft's 2023 UEFI CA)
  and `.2011` (shim 15.8). A 2023-signed shim does not boot firmware whose
  db has only the 2011 CA. frostroot has to name the file it copies and
  record it.
- The postinsts are safe in a fresh build: `grub-multi-install` runs only if
  `/boot/grub/x86_64-efi/core.efi` exists, and `update-grub` (also from
  `zz-update-grub` on a kernel install) only if `/boot/grub/grub.cfg` exists
  and the build is not in a container. **So `/boot/grub/grub.cfg` must not
  exist while apt runs**; the last customize hook writes it.
- `grub-install`, `grub-probe` and `update-grub` must not run in the build:
  they probe the device under `/boot/grub`, which in a chroot is the build
  host's. Canonical's own cloud images run them as root on loop devices and
  then rewrite `root=` with sed.
- A fresh disk boots from the removable path. On the ESP:
  `EFI/BOOT/BOOTX64.EFI` (shim), `EFI/BOOT/grubx64.efi` (the signed GRUB),
  `EFI/BOOT/mmx64.efi`, and the same three-line `grub.cfg` in `EFI/BOOT/`
  and `EFI/ubuntu/`, because the signed GRUB is built with
  `-p /EFI/ubuntu` and Ubuntu's `fw_path` patch also tries the directory it
  was loaded from (whether `fw_path` is set when shim loads GRUB is
  *unconfirmed*):

  ```
  search.fs_uuid <root filesystem UUID> root
  set prefix=($root)'/boot/grub'
  configfile $prefix/grub.cfg
  ```

  `fbx64.efi` stays off the ESP: shim on the removable path runs it when it
  is there.

Sources: packages.ubuntu.com file lists for noble and resolute;
[grub2's debian/ on Launchpad](https://git.launchpad.net/ubuntu/+source/grub2/tree/debian?h=ubuntu/resolute);
[shim-signed](https://git.launchpad.net/~ubuntu-core-dev/shim/+git/shim-signed);
[shim 15.8 `shim.c`](https://github.com/rhboot/shim/blob/15.8/shim.c);
[livecd-rootfs's `disk-image-uefi.binary`](https://git.launchpad.net/livecd-rootfs/tree/live-build/ubuntu-cpc/hooks.d/base/disk-image-uefi.binary).

### Assembling a disk without root

Two candidates were read:

- **systemd-repart** (255 on Ubuntu 24.04) with `--offline=yes`,
  `--empty=create`, `CopyFiles=` and `--seed=`. It keeps ownership only when
  it runs inside a user namespace, copies the whole tree to `/var/tmp`
  first, and on 255 passes mkfs.ext4 no `hash_seed` and runs `mcopy -s` in
  the host's readdir order.
- **The tools one at a time, as debvm's `debefivm-create` does**: the
  mmdebstrap tarball piped into `mke2fs -d -`, `mkfs.fat` and `mcopy` for
  the ESP, `sfdisk` writing the GPT onto a regular file, and
  `dd conv=notrunc` placing each partition. Ownership, hardlinks and xattrs
  come from the tar headers, so nothing needs a namespace or root, and every
  byte is written by a tool, not by Go.

The second is the candidate. It needs **e2fsprogs 1.47.1 or newer**, which
added a tar file as `-d`'s source and `SOURCE_DATE_EPOCH`; Ubuntu 24.04 has
1.47.0, which copies each file's host ctime and atime into the filesystem,
so no two builds on that host could match. Hence the tools tree below.
Sources: [debvm](https://salsa.debian.org/helmutg/debvm/-/blob/main/bin/debefivm-create),
[e2fsprogs release notes](https://e2fsprogs.sourceforge.net/e2fsprogs-release.html),
[systemd-repart(8) for noble](https://manpages.ubuntu.com/manpages/noble/man8/systemd-repart.8.html),
systemd v255 `repart.c` and `mkfs-util.c`.

### Byte identity, layer by layer

| Layer | Identical? | How | What still varies |
|---|---|---|---|
| GPT | yes | `sfdisk` with `label-id:` and each partition's `uuid=`, from a seed; a fresh sparse file of a fixed size | without them libfdisk picks random GUIDs |
| ext4 | yes, with e2fsprogs ≥ 1.47.1 | `-U <uuid> -E nodiscard,lazy_itable_init=1,hash_seed=<uuid>`, `SOURCE_DATE_EPOCH`, a pinned `MKE2FS_CONFIG`, a fresh file | on 1.47.0 the host's ctime and atime; any read-write mount |
| vfat ESP | yes, with care | `mkfs.fat --invariant` (or Debian's 4.2-1.2, patched for `SOURCE_DATE_EPOCH`), then `mmd` and `mcopy` one sorted file at a time, `TZ=UTC` | the label's timestamp; `mcopy -s` in readdir order |
| initramfs | yes | initramfs-tools ≥ 0.131 with `SOURCE_DATE_EPOCH`; dracut with `SOURCE_DATE_EPOCH`, `--no-hostonly`, and `compress="zstd -q -15 -T1"` | zstd with `-T0` (*unconfirmed*); Fedora's `hostonly="yes"` |
| GRUB | yes, if `grub-mkconfig` never runs in the build | copy the signed binaries; write `grub.cfg` from a template | `grub-mkconfig` probes the build host |
| qcow2 | yes in practice | `qemu-img convert -O qcow2`, without `-W` | another QEMU or zlib; no upstream promise, so `cmp` checks it |
| vhdx | **no**, not from `qemu-img` alone | rewrite the header fields after `qemu-img`, or write the vhdx in Go | QEMU fills FileWriteGuid, DataWriteGuid, the disk's identifier and the sequence number at random, and writes its own version as creator |

Prior art: `mmdebstrap-autopkgtest-build-qemu` 1.5.2 claims bit-for-bit
reproducible EFI raw images with the same tools, though its `sfdisk` script
sets no GUIDs (*unconfirmed*); NixOS's `make-disk-image` makes the root
deterministic but not the boot partition.
Sources: e2fsprogs `misc/create_inode.c` at v1.47.0 and v1.47.1;
[dosfstools 4.2-1.2's patches](https://sources.debian.org/src/dosfstools/4.2-1.2/debian/patches/);
mtools 4.0.43; [initramfs-tools 0.151 `mkinitramfs`](https://sources.debian.org/src/initramfs-tools/0.151/mkinitramfs);
[dracut-ng](https://github.com/dracut-ng/dracut-ng);
[QEMU v8.2.2 `block/vhdx.c`](https://gitlab.com/qemu-project/qemu/-/blob/v8.2.2/block/vhdx.c);
MS-VHDX v20210625.

### The password at first boot

- **An empty password with a forced change** (`passwd -d`, `chage -d 0`)
  works on both families: `nullok` is in Ubuntu's `common-auth` on 24.04
  and 26.04 and in Fedora 44's default authselect profile, and SSH refuses
  an empty password. It was not chosen: until somebody logs in, which on a
  headless VM may be never, any local process can `su` to the user without a
  password, and with passwordless sudo that is root.
- **systemd-firstboot** sets only root's password, and skips it when
  `/etc/shadow` exists unless root's field is exactly `!unprovisioned`.
- **systemd-sysusers credentials** apply only when sysusers creates the
  account, and frostroot's user is made by `useradd`.

The candidate: the user's shadow field is `!unprovisioned`, root is locked,
and a unit of frostroot's own, `frostroot-set-password.service`, asks for
the password on the console:

- It runs while the field still reads `!unprovisioned` (an `ExecCondition`,
  not `ConditionFirstBoot`, which would make every boot of an unanswered VM a
  first boot), after `systemd-vconsole-setup.service` and
  `plymouth-quit-wait.service`, before `getty-pre.target` and
  `display-manager.service`, on `/dev/console` with `TTYReset=yes`, running
  `passwd <user>` until it succeeds.
- If the hypervisor passes a hashed-password credential
  (`ImportCredential=`, SMBIOS type 11 or fw_cfg), it applies that instead,
  so a headless VM can be provisioned.
- It is enabled by a static `.wants/` link under `/usr/lib`, because on a
  first boot Fedora re-applies its presets, whose `99-default-disable.preset`
  says `disable *`.

Until the password is set every login fails, so there is no window with an
empty password. A public SSH key still logs in, as sshd checks for a locked
account only without PAM.

`/dev/console` is the **last** `console=` on the kernel command line, so the
prompt appears on one console only; decision 1 puts it on the screen.
Sources: Ubuntu's `pam-configs/unix`; [authselect 1.7.1's `local`
profile](https://github.com/authselect/authselect/blob/1.7.1/profiles/local/system-auth);
linux-pam v1.7.0 `pam_unix`; OpenSSH `servconf.c` and `auth-pam.c`;
[systemd-firstboot v259](https://github.com/systemd/systemd/blob/v259/src/firstboot/firstboot.c);
[userconf-pi](https://github.com/RPi-Distro/userconf-pi) and
[initial-setup](https://github.com/rhinstaller/initial-setup) for how
others do it; [systemd CREDENTIALS](https://systemd.io/CREDENTIALS/).

### What a shared image must not carry

- `/etc/machine-id` reads `uninitialized`, not empty. On Ubuntu that also
  starts systemd-firstboot, which may prompt for locale, timezone or keymap
  unless `/etc/locale.conf`, `/etc/localtime` and `/etc/vconsole.conf` are
  there (whether Ubuntu's are is *unconfirmed*).
- No `/var/lib/dbus/machine-id`, `/var/lib/systemd/random-seed`,
  `/var/lib/systemd/credential.secret` or `/etc/machine-info`.
- No SSH host keys. openssh-server's postinst makes them on Ubuntu, so the
  build deletes them. 26.04 regenerates them with `sshd-keygen.service`, but
  only on a first boot; 24.04 has no such unit, and the image ships one.
  Fedora's `sshd-keygen@.service` makes any key that is missing.
- No `/etc/hostname`. systemd 258 and newer (26.04, Fedora 44) derive one
  from the machine-id; 24.04 falls back to `localhost`.
- Every copy of an image has the same partition and filesystem UUIDs, as
  every copy of a cloud image does. Two disks from one image in one machine
  would confuse a mount by UUID.

Sources: [systemd's BUILDING_IMAGES](https://systemd.io/BUILDING_IMAGES/),
machine-id(5), openssh's `debian/systemd` on Launchpad, Fedora's
`sshd-keygen@.service`.

### Booting it in QEMU

Ubuntu 24.04's `ovmf` has `OVMF_CODE_4M.secboot.fd` and
`OVMF_VARS_4M.ms.fd`, whose variables enrol Microsoft's KEK and db with
Secure Boot on; `OVMF_VARS_4M.snakeoil.fd` must refuse the image, which is
the negative test. Since 2024.02-2ubuntu0.9 the `ms` variables include
Microsoft's 2023 keys, so they cannot catch a 2023-signed shim failing on a
PC that has only the 2011 CA; making a 2011-only variable store is
*unconfirmed*. QEMU emulates SMM without KVM, so a boot works in a container
without `/dev/kvm`; the spike reached a login prompt in 64 s. Ubuntu 26.04's
`ovmf` package holds only documentation, so the files have moved there.
Sources: packages.ubuntu.com, `ovmf.README.Debian`,
[Debian's SecureBoot/VirtualMachine](https://wiki.debian.org/SecureBoot/VirtualMachine).

## Spike results (2026-10-10, build host Ubuntu 24.04.5 in a container, as root, no KVM)

The Ubuntu half, built by hand the way the design below proposes, without
frostroot's code: mmdebstrap 1.4.3 made a 26.04 image tarball from
`snapshot.ubuntu.com` at 2026-10-09T00:00:00Z, with that instant as
`SOURCE_DATE_EPOCH`, `--variant=important`, the packages under "What the
image gains", and one customize hook writing `fstab`, `grub.cfg`, the
`repart.d` file, the user, the set-password unit and script, netplan, and
the per-machine deletions. A 26.04 tools tree (mmdebstrap,
`--variant=essential`, run with `chroot`) then made the disk: `mke2fs -d`
from the tarball, `mkfs.fat --invariant` and `mcopy` for the ESP, `sfdisk`
with fixed GUIDs, `dd conv=notrunc,sparse`, `qemu-img convert -O qcow2`. All
identifiers came from one seed. The disk was booted with QEMU 8.2.2 and
Ubuntu 24.04's OVMF (2024.02), Secure Boot on, under TCG (`-cpu max -smp 4
-m 4G`), from an 8 GiB overlay.

| Measured | Result |
|---|---|
| Image tarball | 1.37 GB uncompressed, 183–247 s from the snapshot; kernel 7.0.0-38-generic, initrd 77 MB |
| Disk assembly | 33–39 s; root partition 1,892 MiB (the tarball plus a quarter, plus 256 MiB), ESP 512 MiB |
| Outputs | raw 2.4 GB apparent, 1.4 GB on disk (sparse); qcow2 1.4 GB uncompressed |
| Two builds, one after the other | **byte-identical**: `image.tar`, `disk.img` and `disk.qcow2` all had the same SHA-256 |
| Secure Boot, Microsoft's keys (`OVMF_VARS_4M.ms.fd`) | booted: shim 15.8 (`shimx64.efi.signed.latest`), the signed GRUB from the ESP stub, the signed kernel; `mokutil --sb-state` said `SecureBoot enabled` |
| Secure Boot, other keys (`OVMF_VARS_4M.snakeoil.fd`) | refused: `BdsDxe: failed to load Boot0001 … Access Denied` |
| Time to a login prompt without KVM | 64 s; the kernel at 13 s |
| First boot | a machine-id was generated; `systemctl is-system-running` said `running`, nothing failed; no locale, keymap or timezone prompt |
| Password from a credential (SMBIOS type 11) | applied by the unit; the user logged in on the serial line with it; `sudo` worked |
| Password on the screen, no credential | the prompt appeared on the VGA console; the serial line stayed silent, as `serial-getty` waits for it too |
| Growth at first boot | systemd-repart grew the root partition from 1.9 GB to 7.5 GB of the 8 GiB disk, moving the backup GPT, and `systemd-growfs-root` grew ext4 online |
| File capabilities through tar → `mke2fs -d` | kept: `/usr/bin/ping cap_net_raw=ep` |
| Network | netplan with networkd: DHCP on `enp0s2`, `10.0.2.15/24` |
| Per-machine state in the image | `machine-id` read `uninitialized`; no SSH host keys; no random seed |
| ESP timestamps | every file and directory dated 2026-10-09 00:00, the instant: mtools 4.0.49 honours `SOURCE_DATE_EPOCH` without `-m` |

What it found that the documents had not said:

1. **mmdebstrap empties machine-id after the last hook.** Its
   `cleanup/reproducible` runs after the customize hooks and writes an
   empty `/etc/machine-id`; 1.4.3 can skip only the whole cleanup. A disk
   build skips it and deletes its seven files itself.
2. **`libarchive13t64` has to be named.** mke2fs loads it at run time to
   read a tar; the tools tree did not have it until it was asked for.
3. **mke2fs needs a UTF-8 locale to read the tarball.** Under `env -i` it
   stopped at `Pathname can't be converted from UTF-8 to current locale`;
   `LC_ALL=C.UTF-8` fixed it.
4. **`systemd-repart` is a package of universe on 26.04.** frostroot
   enables every component, so a recipe gets it, but it must be in the
   disk package set.
5. **The boot messages print into the password prompt.** The unit must
   silence PID 1's status output while it asks.
6. **Without `/etc/hostname` the machine is `localhost`.** systemd 258 and
   newer can derive a name from the machine-id only from a pattern written
   in `/etc/hostname` (such as `lab-????`); whether the image should ship
   one is for the plan.

Not measured: Ubuntu 24.04; a build as an unprivileged user with bubblewrap;
an offline build from a pool (both builds read the snapshot); typing the
password at the screen's prompt (only its appearance was captured); a
kernel update on a deployed machine; firmware that trusts only the 2011 CA
(Ubuntu's `ms` variables enrol both); real hardware; and all of Fedora.

## Proposed design

### One disk path for both families

The family's build makes the tarball as it does today, with a disk-target
package set and disk-target hooks. A new step then assembles the disk from
that tarball with tools from a **disk tools tree**, the same for Ubuntu and
Fedora:

```text
recipe ─▶ mmdebstrap or mkosi ─▶ image.tar ─┐
                                            ├─▶ mke2fs -d        root.ext4
            disk tools tree (locked) ───────┤   mkfs.fat, mcopy  esp.vfat
                                            ├─▶ sfdisk, dd       <name>.img
                                            └─▶ qemu-img         <name>.qcow2
```

The rule in AGENTS.md becomes "only mmdebstrap, mkosi or the disk tools
write the image": Go still never tars, walks or deletes a root filesystem,
and never writes a filesystem or partition table. It renders text (the GPT
script, `fstab`, `grub.cfg`, the unit) and runs tools.

### The recipe

```toml
[image]
name = "lab"
release = "26.04"
target = "disk"            # "wsl", the default, or "disk"

[disk]
size = "16G"               # optional; by default the content plus headroom,
                           # and the root grows to fill the disk at first boot
formats = ["qcow2"]        # the raw .img is always written; "vhdx" later
```

`[wsl]` belongs to the WSL target and `[disk]` to the disk target.
`validate` refuses `[disk]` in a WSL recipe, as it refuses `[python]` in a
Fedora one, and `[wsl]` in a disk recipe; the second needs `[wsl]` to tell
absent from present, which as a plain struct in `recipe.Recipe` it cannot
today. A recipe without `target` is a WSL recipe and stays the bytes it is.

### The form and the plain interface

- The full-screen form asks for the target on the Image page, after the
  release. The WSL page is shown only for the WSL target and a Disk page
  only for the disk target, with a `Field.Targets` beside `Field.Families`.
  `ToRecipe` leaves the other target's answers out, and an `edit` that
  switches the target carries every answer across, as switching the family
  does.
- The plain interface keeps its questions and their order, because people
  pipe answers into it. It never asks for the target: `init --target disk`
  answers it, as `init --distro` answers the family.
- `FromRecipe`, `ToRecipe`, the recipe template in `internal/cli/init.go`,
  `TestRecipeRoundTrip` and `TestFormBindingCoversEveryField` follow.

### What the image gains

For both families, when the target is `disk`:

- The packages: Ubuntu adds `linux-image-generic`, `initramfs-tools`,
  `grub-efi-amd64`, `grub-efi-amd64-signed`, `shim-signed`, `mokutil`,
  `dosfstools`, `netplan.io`, `systemd-resolved` and, on 26.04,
  `systemd-repart`, which is in universe there (on 24.04 it is part of
  `systemd`); Fedora adds `kernel`, `dracut`, `dracut-config-generic`,
  `shim-x64`, `grub2-efi-x64`, `mokutil` and `dosfstools`. Their versions
  land in `[[packages]]` like any other.
- `/etc/fstab` mounting the root by filesystem UUID with
  `x-systemd.growfs`, and the ESP at `/boot/efi` with `nofail`, and
  `/usr/lib/repart.d/50-root.conf` (`Type=root`), so that systemd-repart
  grows the root partition to the end of the disk on the first boot.
- `/boot/grub/grub.cfg` (Fedora: `/boot/grub2/grub.cfg`) from a template:
  one entry booting `/boot/vmlinuz` and `/boot/initrd.img` with
  `root=UUID=…`. It is written by the last customize hook, after every
  postinst, so `update-grub` never runs in the build.
- The initramfs is made inside the build with `SOURCE_DATE_EPOCH` set: by
  initramfs-tools' own postinst on Ubuntu; on Fedora, where mkosi bypasses
  kernel-install, by a finalize step running
  `dracut --no-hostonly --reproducible` with single-threaded zstd. How
  mkosi 20.2's finalize scripts reach the image's dracut is *unconfirmed*.
- `/etc/machine-id` reads `uninitialized`; the SSH host keys, random seeds,
  `credential.secret`, `machine-info` and `/etc/hostname` are deleted.
  mmdebstrap's own `cleanup/reproducible` runs after the last customize
  hook and empties machine-id again, so a disk build skips it
  (`--skip=cleanup/reproducible`) and its last hook deletes the same seven
  files itself. The WSL build keeps the cleanup as it is.
- The user's shadow field is `!unprovisioned`, root is locked, and
  `frostroot-set-password.service` is installed and enabled statically. It
  silences PID 1's status messages while it asks (`SIGRTMIN+21` to PID 1
  before, `SIGRTMIN+20` after, as Fedora's initial-setup does): without
  that, the spike's prompt came out as `New password: [  OK  ] Started
  systemd-logind.service`.
- Ubuntu 24.04 gets a first-boot unit that runs `ssh-keygen -A` when
  openssh-server is in the image.
- DHCP on wired interfaces, the family's own way: netplan with
  systemd-networkd on Ubuntu, NetworkManager on Fedora. `/etc/resolv.conf`
  points at the resolver the family uses, instead of being deleted as the
  WSL build deletes it.
- No `/etc/wsl.conf`.

### The disk tools tree

A small Ubuntu 26.04 tree, made by mmdebstrap on the build host, holding
e2fsprogs (1.47.2), `libarchive13t64` (which mke2fs loads at run time to
read a tar, and which nothing pulls in), dosfstools (4.2-1.2, with the
`SOURCE_DATE_EPOCH` patch), mtools, fdisk and qemu-utils (10.2.1): 104
packages and 164 MB. The tools run inside it with bubblewrap, in an
environment of their own (`PATH`, `LC_ALL=C.UTF-8`, `TZ=UTC`,
`SOURCE_DATE_EPOCH`); mke2fs refuses a tar whose names are UTF-8 under any
other locale. The host's versions never reach the image, as the Fedora
tools tree keeps the host's dnf out. Its packages are recorded in the lock
as `disk_tools`, fetched by `vendor` and rebuilt from the pool by
`build --offline`. A Fedora disk uses the same Ubuntu tree: the host is
Ubuntu, and one set of tools means one set of byte-identity rules.

### Assembling the disk

From the tarball and the lock's seed, in a work directory on one
filesystem:

1. The root partition: `mke2fs -t ext4 -d image.tar -U <uuid>
   -E nodiscard,lazy_itable_init=1,hash_seed=<uuid>` with
   `SOURCE_DATE_EPOCH` and frostroot's own `MKE2FS_CONFIG`, into a fresh
   file of the partition's size. The partition type is the discoverable
   root type for x86-64, so systemd finds it without `fstab` too.
2. The ESP: a fresh 512 MiB file, `mkfs.fat -F 32 --invariant -i <id>`
   (`-i` after `--invariant` wins), then `mmd` and `mcopy` of the shim,
   GRUB, MokManager and the small `grub.cfg`, one file at a time in sorted
   order, with `TZ=UTC`. The binaries are taken out of the tarball by
   `tar` in the tools tree, by their exact member names, and the shim's name
   comes from the lock. Ubuntu's stub `grub.cfg` goes to `EFI/BOOT/` and
   `EFI/ubuntu/`; Fedora's to `EFI/BOOT/` and `EFI/fedora/`, the prefix
   Fedora's signed GRUB is assumed to be built with (*unconfirmed*), with
   the binaries from `shim-x64` and `grub2-efi-x64` under
   `/boot/efi/EFI/fedora/` in the tarball.
3. The disk: a fresh sparse file, `sfdisk` with `label-id:` and each
   partition's `uuid=`, then `dd conv=notrunc,sparse` of each partition at
   its offset.
4. `qemu-img convert -O qcow2` when the recipe asks for it.

All the identifiers come from one seed by HMAC-SHA256, as systemd-repart
derives them: the disk GUID, each partition's UUID, the root filesystem's
UUID and hash seed, and the ESP's serial.

### The lock

`version = 1` stays. New fields, all optional and `omitempty`, so every
older lock loads and rebuilds unchanged:

- `target = "disk"`, absent for WSL.
- A `[disk]` table: the seed, the size in bytes of the disk and of each
  partition, and the shim file the build copied
  (`shimx64.efi.signed.latest` today).
- `disk_tools`: every package of the disk tools tree, like Fedora's
  `tools`.

The seed is chosen at random by an online build and frozen in the lock, as
the instant is; an offline build takes it from the lock and never writes it.

### vendor and build --offline

`vendor` fetches the `disk_tools` packages with the rest, into the same
pool. `build --offline` makes the disk tools tree from the pool, the image
from the pool as today, and the disk from both, and two offline builds of
one lock produce the same `.img` and `.qcow2`.

### Trust

- The kernel, shim, GRUB and the disk tools are packages of the
  distribution, checked by apt's or dnf's signatures as every other package
  is.
- Secure Boot is the distribution's and Microsoft's: frostroot signs
  nothing, holds no key, and a disk that boots under Secure Boot does so
  because the firmware trusts what the distribution shipped.
- The password never passes through frostroot: not in the recipe, the lock,
  the image or a log. A hashed-password credential is the hypervisor's to
  pass at boot.

### Outputs and the failed build

`dist/<name>-<family>-<release>-amd64.img`, and a `.qcow2` beside it when
the recipe asks. The tarball is a work file of a disk build, not an
output. The rule for a failed or interrupted build stays: no lock, no
output and no temporary file is left, and a previous image and its lock
stay as they were. With up to three outputs, `export.Stage` and
`export.Unstage` stage each of them, the lock is renamed into place, then
each output, and a failure between puts every one back. Copying a sparse
raw image across filesystems must keep it sparse, which `export`'s copy
does not do today.

### Verification

- Unit tests for every renderer (the `sfdisk` script, `fstab`, both
  `grub.cfg`s, the units, the `MKE2FS_CONFIG`), with hostile names, run
  through a real `sh` where they are shell. `TestHooksAndScriptNeverSwallowFailures`
  covers the new hooks.
- Integration tests, `TestIntegrationDisk*` for both families: build a
  disk, check its GPT with `sfdisk --dump`, `fsck` both filesystems
  read-only, list the ESP, rebuild offline and compare. `scripts/integration.sh`
  requires them by name.
- Scenarios: a new `disk-boot`, which boots the image in QEMU with OVMF and
  Microsoft's keys, answers the password prompt on the serial console, logs
  in, and checks `mokutil --sb-state`, `systemctl is-system-running` and
  that the network came up; and with the snakeoil variables, that the
  firmware refuses it. `offline-identical` and `no-build-leaks` learn the
  disk target, and `failed-build` learns the multi-output rollback. All of
  them for both families.
- A real PC with Secure Boot on is the owner's check, as `wsl-boot` is on
  Windows, recorded in the README's Verification section once run.
- CI is switched off at the moment. Until it is back on, every check above
  runs by hand, and each pull request lists what ran.

## Risks

- **Only the Ubuntu 26.04 half has run, and only as root.** The spike
  measured it (see "Spike results"); 24.04, Fedora, an unprivileged build
  with bubblewrap, an offline build from a pool and real hardware are still
  read, not measured. The Fedora spec's spike found five removable
  differences between two builds that no document predicted; this spike
  found none on Ubuntu, but the Fedora half has its own dracut, dnf5 and
  mkosi to get through.
- **Shim and the Microsoft CAs are moving.** The 2011 UEFI CA expires in
  2026 and shim-signed is moving to 2023-signed binaries. An image made with
  one will not boot on firmware that trusts only the other. The lock
  records which file was used; decision 4 takes the 2011-signed one while
  the archive has it.
- **A deployed machine never updates its ESP.** Nothing mounts the ESP
  while apt runs in the build, and `grub-multi-install` runs only once
  `core.efi` exists, so a later `apt upgrade` on the machine updates the
  kernel and `/boot/grub/grub.cfg` (*unconfirmed* for Fedora's BLS
  entries) but not shim or GRUB on the ESP. A machine that needs a newer
  shim gets a rebuilt image.
- **Fedora's boot entries.** Fedora's GRUB reads Boot Loader Specification
  entries, which kernel-install names by machine-id and fills with the
  **build host's** `/proc/cmdline` when `/etc/kernel/cmdline` is missing. A
  static `grub.cfg` avoids both in the image, but a kernel update on a
  deployed machine writes BLS entries the static file does not read
  (*unconfirmed*).
- **Size and time.** A disk build writes gigabytes and holds the tarball
  and every partition at once in the work directory, about three times the
  image: 1.4 GB of tarball became 2.4 GB of raw image and 1.4 GB of qcow2
  in the spike. A boot test without KVM took about a minute there, which
  makes `disk-boot` affordable.
- **Two families and two formats multiply the checks**, and with CI off
  they all run by hand.
- **vhdx, when it comes.** Hyper-V accepting fixed header GUIDs, and two
  disks with one identifier on one host, are *unconfirmed*; the vhdx needs
  a run on Windows.

## Decisions (answered by the owner on 2026-10-10)

The owner took the recommended option of each.

1. **The first-boot prompt appears on the screen**: the kernel command line
   is `console=ttyS0 console=tty0`, so a PC or a VM's window gets the
   prompt. A serial-only VM sees nothing until a hashed-password credential
   is passed. Rejected: the serial line, which leaves a PC's screen blank;
   every console at once, as Fedora's initial-setup does, which is more
   code.
2. **The vhdx waits for a later version.** The first version writes the raw
   image and the qcow2. The owner asked for vhdx in the scoping questions,
   and it comes back when it can be checked on Hyper-V, with frostroot
   rewriting the header fields after `qemu-img` (GUIDs from the seed, a
   fixed sequence number, the log zeroed, CRC-32C recomputed). Rejected for
   now: writing a vhdx not promised byte-identical, which breaks the rule
   for one output.
3. **The disk is the content plus headroom, and grows on the first boot**:
   systemd-repart grows the root partition to fill the disk, and
   `x-systemd.growfs` grows the filesystem. A fixed 16 GiB root would
   surprise anyone writing the image to a 1 TB SSD. It is one more
   boot-time path to test.
4. **The shim is the 2011-signed `shimx64.efi.signed.latest`** (15.8),
   which boots on older firmware, while the archive has it. The lock
   records the file, and a release note says so when the archive moves to
   the 2023-signed one.
5. **Fedora boots through its own `grub.cfg` with `blscfg`**, with
   `/etc/kernel/entry-token` and `/etc/kernel/cmdline` written, so later
   kernel updates keep booting, if kernel-install can be made to run in the
   build. That is measured when the Fedora half is built; a static
   `grub.cfg`, as for Ubuntu, is the fallback.
6. **A spike of the Ubuntu half comes before the plan**: one disk, one boot
   in QEMU with Secure Boot, two builds compared. It ran on 2026-10-10;
   its results are under "Spike results".

## What follows approval

A plan in `docs/superpowers/plans/` with the tasks, in pull requests of
their own:

1. The recipe, the form and the lock: `target`, `[disk]`, `init --target`,
   the new lock fields, nothing built yet. Every WSL recipe and lock stays
   the same bytes.
2. The disk tools tree, with its lock entry and `vendor`.
3. The Ubuntu disk: the image's additions, the assembly, the raw `.img`, the
   scenarios.
4. The Fedora disk.
5. The qcow2, the README, AGENTS.md's rules and scenarios, and the
   version.

The vhdx is a later version's.
