# frostroot: building on Windows without WSL

Date: 2026-11-16
Status: **decisions 1, 2, 3, 4, 6 and 7 answered by the owner on
2026-11-17, and the spike run the same day** (see "Spike: run on
2026-11-17"); decisions 5 and 8 are open, and no code is written. The
owner asked for a `frostroot.exe` that works on a Windows machine with no
WSL at all, by bringing a small Linux of its own to build in. This
document compares the ways to do that, recommends one, and records the
experiments that tested it. Every statement about a tool says how it is
known: **verified** (run for this spec, on the Windows 10 Enterprise LTSC
2019 machine, build 17763, without admin rights, or recorded as run in
[`docs/VERIFICATION.md`](../../VERIFICATION.md) or an earlier spec's
spike), **read** (in the tool's documentation on 2026-11-16, or in this
repository's code), or **assumed** (neither). The version this ships in
is the owner's to name. The tasks are in
[`2026-11-16-frostroot-windows-builder.md`](../plans/2026-11-16-frostroot-windows-builder.md).
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md),
[`2026-09-17-frostroot-vendor.md`](2026-09-17-frostroot-vendor.md) (the
pool an offline build reads),
[`2026-09-17-frostroot-reproducible.md`](2026-09-17-frostroot-reproducible.md)
(the frozen instant) and
[`2026-10-09-frostroot-bare-metal.md`](2026-10-09-frostroot-bare-metal.md)
(the disk image, which this design boots)
Removes from the README's "Deliberately not yet": a native Windows binary.

## Why

frostroot's images are for WSL, so its users sit at Windows machines, and
today the first instruction they read is "run it inside WSL". That needs
WSL 2: WSL 1 has no user namespaces and frostroot refuses it. WSL 2 needs
Windows 10 version 1903 or newer and a hypervisor an administrator
switched on. The machine this spec was written on has neither: LTSC 2019
is version 1809, its WSL is WSL 1, and its user cannot enable a Windows
feature. A teaching lab's machines are often like it. The people who most
need a frozen image are the ones who cannot build one.

The build itself cannot move to Windows. mmdebstrap, mkosi, dpkg's
maintainer scripts, the mounts, the owners, the capabilities and the
hardlinks are Linux's, and AGENTS.md's first rules exist because getting
them nearly right is wrong. So the Linux has to come along. What can
change is who starts it.

## Goal

- `frostroot.exe build`, in a recipe directory on an NTFS drive, on a
  Windows machine with no WSL, no admin rights and no hypervisor enabled,
  writes `frostroot.lock` and `dist\<name>-<distro>-<release>-amd64.tar.gz`
  as a Linux build does.
- `frostroot.exe build --offline` rebuilds from the lock and `vendor\`, and
  two such builds of one lock are byte-identical, as on Linux.
- The build is the existing Linux build, run by the existing Linux binary.
  Nothing in `internal/builder` learns about Windows, and no Go code on
  either side tars or walks a root filesystem.
- The Linux it runs in is pinned: the binary names its guest image by
  SHA-256, and anyone can rebuild that image from a lock in this
  repository and get the same bytes.
- `init`, `edit`, `validate`, `vendor` and `version` run on Windows
  natively, as they compile to today.
- A machine that does have a hypervisor enabled builds faster with no
  change to the command line.
- No Linux recipe, lock or image changes by a byte.

## Non-goals, for the first version

- `capture` on Windows. It reads an installed Linux; there is none.
- The full-screen build screen. On Windows `build` prints progress as
  lines, as `--plain` does. The form of `init` and `edit` is unaffected.
- Shipping or downloading QEMU. The first version runs a QEMU the machine
  already has (decision 2 asks whether that is enough).
- Driving WSL 2, Hyper-V's own VMs or a container runtime, where they
  exist. The seam leaves room for them; see "The ways compared".
- Importing the result. `wsl --import` is still the user's step, and a
  machine with no WSL builds images for machines that have it. The
  sibling WSL 1 work is about importing and is not designed here.
- Windows on arm64, and Windows versions older than 10 1809.
- macOS. The design does not exclude it, and nothing is promised.

## The ways compared

Each row says what the way needs from the machine. Sizes and times that
are not marked verified are estimates.

| | QEMU, software emulation (TCG) | QEMU with WHPX | WSL 2, driven by frostroot.exe | A container runtime |
|---|---|---|---|---|
| Admin rights | none: unpacked into the user's profile and run (**verified**: QEMU 11.1.0 here, unpacked with 7-Zip, runs the project's test VM daily) | an administrator must enable the "Windows Hypervisor Platform" feature once (**assumed**: enabling a Windows feature needs elevation; the API exists from Windows 10 1803, **read**) | an administrator must enable WSL 2's features; Windows 10 1903 or newer (**read**, README) | Docker Desktop and Podman run their Linux in WSL 2 or Hyper-V, so the same and more (**assumed**) |
| Works on the machine at hand | yes (**verified**) | no: `-accel whpx` fails to initialise here and QEMU says "falling back to tcg" (**verified**) | no: WSL 1 only (**verified**) | no (**verified**: none installed) |
| Speed | the guest answers SSH 43 s after QEMU starts (median of five); in it, an online Ubuntu build of a small recipe took 14 minutes, Fedora 21, an Ubuntu disk 29, and offline rebuilds 7 to 22 (**verified**, spikes 3 to 5, at 4 processors and 2 GiB) | near native (**assumed**; cannot be measured here) | native: it is how frostroot is used today (**verified**) | native where it runs (**assumed**) |
| What is shipped | `frostroot.exe`, the Linux binary beside it, and a guest image fetched once | the same | `frostroot.exe` and the Linux binary; the distribution is the user's, or a frostroot-built tarball imported as a private one | an image on a registry, or nothing |
| The guest | a frostroot disk image, built from a recipe and a lock in this repository (below) | the same image | a frostroot WSL tarball of the same recipe, or whatever the user has | a Dockerfile, which pins nothing unless made to |
| Files across | over the guest's forwarded SSH port, 13 to 24 MiB/s each way (**verified**, spike 6); QEMU's own folder sharing (9p) does not work with a Windows host (**assumed**, from memory; the design does not use it) | the same | `/mnt/c`, but frostroot refuses to build under `/mnt`, so the recipe directory is copied in and the outputs out (**read**, AGENTS.md) | bind mounts through the runtime's VM |
| The corporate CA | `--ca-bundle`'s bytes go over the channel to the build's own `--ca-bundle`; apt, pip and dnf then fetch through QEMU's user-mode network (**verified** in the test VM on this network, `docs/VERIFICATION.md`) | the same | as inside WSL today | the same file, mounted |
| Ctrl-C and exit codes | frostroot.exe owns both ends: it sends SIGINT over the SSH session and returns the remote exit status (**read**: `Session.Signal`, `ExitError.ExitStatus` in `golang.org/x/crypto/ssh`) | the same | `wsl.exe` passes the exit code; an interrupt reaching the right process group is what `scripts/wsl.sh` exists to get right | the runtime's `stop` signals and exit codes |
| Licences of what is redistributed | QEMU is not redistributed (decision 2). The guest image is Ubuntu packages: see decision 3 | the same | none, or the same tarball question | the base image's |
| Trust rules | the guest image and the Linux binary are checksummed by the release binary; QEMU is the machine's own tool, version-checked, as mmdebstrap is on a Linux host | the same | the user's distribution is unpinned, as a Linux host is | a registry tag is a server deciding what is trusted, unless pinned by digest |
| Byte-identical offline rebuilds | yes, and for Ubuntu the very bytes an unprivileged Linux build writes (**verified**, spikes 4 and 5) | **assumed** the same bytes as TCG; spike 8 compares | as today (**verified**) | as on a Linux host, if privileged |

**WSL 2 lost** as the first way because the owner asked for no WSL at
all, and because the machines that have WSL 2 are served already. It is
the cheapest second backend: the launcher, the copy in and out and the
placement are the same, and only "start a Linux and run a command in it"
differs.

**A container runtime lost** because on Windows it is WSL 2 or Hyper-V
with more in between, the build needs `--privileged` (README, "Install"),
and Docker Desktop's licence is not every lab's to accept. Someone who has
one can already run the Linux binary in it.

**Hyper-V's own VMs lost**: creating one needs an administrator or
membership of Hyper-V Administrators, and a vhdx frostroot does not yet
write.

**QEMU is the recommendation, with WHPX asked for first and TCG behind
it.** QEMU's documentation says of `-accel` given more than once that
"the next one is used if the previous one fails to initialize" (**read**),
and on this machine `-accel whpx -accel tcg` did exactly that
(**verified**). One command line serves the locked-down machine and the
fast one.

## Proposed design

### The shape

```
frostroot.exe build                         (Windows, internal/cli)
  ├─ reads and validates the recipe, as today
  ├─ finds QEMU, the guest image and the Linux binary; checks each
  ├─ starts QEMU: the guest image read-only under a fresh overlay
  ├─ over SSH on a forwarded loopback port:
  │    puts the Linux binary in, checks its SHA-256
  │    sends the recipe directory's files
  │    runs  frostroot build --plain …   as root, relaying its lines
  │    brings back the lock and the image, each checked by SHA-256
  ├─ places the image and the lock in the recipe directory, atomically
  └─ stops QEMU and deletes the overlay
```

The guest is thrown away after every build. Each build starts from the
pinned bytes, so nothing an earlier build left can change a later one.

### The guest is a frostroot disk image

frostroot already builds a disk that boots in QEMU with DHCP, a root that
grows to fill its disk and no per-machine state (v0.15.1; `disk-boot`).
The guest is one of those: `guest/frostroot.toml`, a disk recipe of Ubuntu
24.04 whose packages are what a build host installs (`mmdebstrap`,
`uidmap`, `ubuntu-keyring`, `mkosi`, `dnf`, `rpm`, `createrepo-c`,
`bubblewrap`) and `openssh-server`. It is 24.04 because frostroot drives
mkosi 20.2 and refuses another, and 24.04 is where the project's builds
have all been measured (mmdebstrap 1.4.3).

`guest/frostroot.lock` is committed beside it. That is a lock in the
repository on purpose, unlike a scenario's. With `frostroot vendor` and
`frostroot build --offline` on any Linux host, anyone gets the image the
release publishes, byte for byte: that is the promise frostroot already
makes for a disk image, used on itself. The guest's own tools are
therefore pinned by a lock, which no Linux build host's are today.

Nothing of frostroot is in the image. The recipe format has no way to add
a file or a unit, and the design does not add one:

- **The way in is a credential.** systemd writes the credential
  `ssh.authorized_keys.root` to `/root/.ssh/authorized_keys` (since
  systemd 252, **read**; 24.04 has 255), and QEMU passes credentials as
  SMBIOS type 11 strings. frostroot.exe makes an ed25519 key pair for
  each build and passes the public half. Root's password stays locked,
  and sshd lets the key in. The user's password prompt of a disk image is
  answered the same way, with the credential the README documents and a
  hash of random bytes nobody keeps. (**Verified**, spike 1.)
- **The Linux binary goes in over that session** and is checked there
  against the SHA-256 the release compiled into `frostroot.exe`. So the
  guest image changes only when its packages do, not with every frostroot
  release, and the two ends of the conversation are always one version.
- **The build runs as root.** The guest is a single-purpose VM, root needs
  no user namespace, and Ubuntu's AppArmor restriction on mkosi does not
  apply to root. Root in the guest and an unprivileged user on Linux
  wrote the same Ubuntu tarball, byte for byte (**verified**, spike 4).

The image is published as a release asset, gzip-compressed (`gzip -n`, so
the file is reproducible too; `compress/gzip` reads it on Windows without
a new dependency): 1.13 GiB, from 2,898 MiB raw (**verified**, spike 9),
under GitHub's 2 GiB per asset (**read**).

### QEMU

frostroot.exe looks for `qemu-system-x86_64.exe` and `qemu-img.exe` where
`FROSTROOT_QEMU` says, then on `PATH`, then in `%ProgramFiles%\qemu`. It
asks the version and refuses one outside the range it was measured with,
as it does for mkosi. A missing QEMU is an error the user can fix (exit
1), and the message says where QEMU for Windows comes from and that its
installer unpacks without admin rights.

The machine is `q35` with QEMU's own UEFI firmware, `edk2-x86_64-code.fd`,
which the Windows build ships (**verified**: present in 11.1.0). Secure
Boot is not enforced by it, and shim boots without. The guest image is
attached read-only as the backing file of a qcow2 overlay that `qemu-img
create` makes in the build's work directory, 40 GiB virtual and as large
on NTFS as the build writes; the disk image's own first boot grows the
root into it. Networking is user-mode (`-netdev user`), which needs no
driver and no rights, with one forward from a loopback port to the guest's
port 22. No display. The serial console goes to a log file, kept when a
build fails.

`-accel whpx -accel tcg`, `-cpu max`. Memory defaults to 2 GiB, which every build of the spike had with room (spike 5), and
processors to the host's, up to 4; `FROSTROOT_VM_MEMORY` and
`FROSTROOT_VM_CPUS` change them, and must never change the image (spike
4 found 2 GiB and 4 processors in the guest write what 6 GiB and 8 write
on Linux; spike 8, with WHPX, is open).

QEMU runs in a Windows job object that ends it when frostroot.exe ends,
however that happens, and in a process group of its own, so that the
console's Ctrl-C reaches frostroot.exe and not QEMU. Both are needed: in
frostroot's group QEMU died of the console's Ctrl-C at once, and without
the job object it outlived a killed frostroot (**verified**, spike 7).

### The conversation with the guest

`golang.org/x/crypto/ssh` (BSD-3-Clause) is the one new dependency.
CONTRIBUTING.md asks a new dependency for a reason that would survive the
question "why not the standard library": the reason is that the
alternative is a protocol and an agent of frostroot's own, started by a
unit the image would have to carry, to do what sshd does in every Ubuntu
image and what the project already trusts its test VM with. Windows'
`ssh.exe` is there on this machine and absent on the next.

- **Who the guest is.** frostroot.exe also makes the guest's host key and
  passes it as the credential `tmpfiles.extra` (systemd 252, **read**),
  two `f~` lines that write `/etc/ssh/ssh_host_ed25519_key` and its
  public half. tmpfiles writes them before sshd starts, and the client
  accepts that key and no other (**verified**, spike 2).
- **Credentials stay off the command line.** They go in files in the
  work directory that `-smbios type=11,path=` names, each holding
  `io.systemd.credential.binary:NAME=BASE64` (**verified**, spike 1).
- **Files go through frostroot's own framing, not a shell.** The Linux
  binary gains a hidden command, `frostroot guest`, that receives a set
  of files on standard input and sends a set on standard output: a name,
  a size, the bytes, a SHA-256. Names are checked against what each side
  expects, relative and without `..`, sizes are bounded, and nothing a
  recipe or a lock names ever becomes part of a command line. The same Go
  code is both ends, so a pipe tests it and a fuzz target feeds it.
- **What goes in:** `frostroot.toml`; `frostroot.lock` when there is one;
  the key and certificate files the recipe names, which validation
  already confines to the recipe directory; `--ca-bundle`'s file; and for
  `--offline` the pool under `vendor\`. Nothing else in the directory.
- **What the guest runs:** `frostroot build --plain` with the flags the
  user gave, each rebuilt from its parsed value and quoted. Its standard
  error and output are relayed line by line.
- **The environment** is a list: `SOURCE_DATE_EPOCH`, and the proxy
  variables, sent inside the conversation and never printed. A proxy on
  the host's loopback is reachable from the guest as `10.0.2.2` and not
  as `127.0.0.1`, and a whole build ran through one so (**verified**,
  spike 11); frostroot.exe rewrites that one case and says so.
- **Files** cross at 13 to 24 MiB/s under TCG, so a 3 GiB disk image
  comes back in about three and a half minutes (**verified**, spike 6).
- **What comes back:** on success the lock (online) and the image, each
  with the SHA-256 the guest computed, which frostroot.exe checks as it
  writes. The lock is parsed as hostile input before it is placed, as
  every lock is. On failure, the build's log files, into a directory
  frostroot.exe names in its message; the work directory itself is a root
  filesystem and stays in the overlay, which `--keep-work` keeps.

### Placing the outputs

The order is the builder's: the image is written under a temporary name
in `dist\`, the lock under a temporary name beside the old one, then the
lock is renamed, then the image, and a failure between puts things back.
`internal/export` already has the helper and compiles for Windows; the
part of the builder that orders the two renames moves where both callers
reach it. A recipe directory is held against a second build by a lock
file, which on Windows needs `LockFileEx` where Linux uses `flock`.

### Exit codes, Ctrl-C and what is printed

Only `internal/cli` prints or exits, on Windows too. The guest build's
exit code is frostroot.exe's: 0, 1, 2 or 130. New failures of the
launcher itself:

| Failure | Exit |
|---|---|
| QEMU not found, or a version outside the range | 1 |
| The guest image or the Linux binary missing, or not the pinned bytes | 1 |
| Too little free disk for the overlay | 1 |
| The guest did not answer within the boot timeout (`FROSTROOT_VM_BOOT_TIMEOUT`, 10 minutes) | 2 |
| The connection to the guest was lost | 2 |
| A file came back with another checksum than the guest named | 2 |

Each one the user can fix joins the `errors.Is` list in
`reportBuildFailure`.

The first Ctrl-C sends SIGINT to the guest's frostroot, which interrupts
mmdebstrap's process group as on Linux, and frostroot.exe waits for exit
130. A second Ctrl-C ends QEMU. The guest's cleanup matters less than on a
Linux host, since the overlay is deleted either way, but the first path
is the one that brings the logs back.

### The commands

- `build`, `build --offline`: through the guest.
- `init`, `edit`, `validate`, `vendor`, `version`: natively. They compile
  for Windows today and only `build` refuses (**read**, `internal/cli`);
  about 40 unit tests fail on Windows at master for platform reasons,
  symlinks and renames over open files (**verified**, 2026-11-10). A
  supported platform cannot have a failing baseline, so they are fixed
  first.
- `guest fetch` downloads the guest image for this binary into
  `%LOCALAPPDATA%\frostroot\guest\`, bounded by the pinned size, through
  `pki.Transport` with `--ca-bundle` and `--insecure` as `vendor` has
  them, checked against the pinned SHA-256 before the rename. `guest
  fetch --from FILE` takes a file carried in by hand instead, with the
  same check: a machine with no network needs it. `build` never
  downloads the guest: when it is absent, `build` says the one command
  that gets it (decision 4).
- `capture`: refused on Windows, with the reason.

### What is promised

- An Ubuntu or Fedora recipe for WSL, and an Ubuntu disk recipe, build
  online and offline on Windows 10 1809 or newer, amd64, with a QEMU in
  the measured range (11.1.0 so far), 2 GiB of memory to give the guest,
  and the free disk a build needs (task 29 of the plan measures it per
  target; the spike's one guest ran nine builds into one overlay, which
  reached 20 GB). All three were built so in the spike (**verified**,
  spikes 4, 5 and 11). Whether the first version names Fedora and disk
  recipes, or must have them working before it ships, is decision 8.
- Two offline builds of one lock on Windows are byte-identical with each
  other (**verified** for all three, spikes 4 and 5). That they are the
  bytes a Linux host writes was measured for Ubuntu (spike 4) and is
  promised only if the owner chooses to (decision 5).
- Without acceleration, on a machine like this one, with the guest at 4
  processors and 2 GiB: a boot of about 45 seconds to the guest's
  answer, then, measured in the guest, an online Ubuntu build of a
  small recipe in about 14 minutes, a Fedora one in 21 and an Ubuntu disk
  in 29; offline rebuilds in 7 to 9 minutes for either tarball and 19 to
  22 for the disk; and the outputs back at 13 to 15 MiB/s (**verified**,
  spikes 3, 4, 5, 6 and 11). WHPX would be faster; by how much is spike 8,
  not run.

## The rules this touches

From AGENTS.md's "Rules that are easy to break":

- **What reaches the image.** Nothing new can: the image is written by
  the same mmdebstrap or mkosi, driven by the same binary. frostroot.exe
  copies finished files and never reads inside them.
- **Only mmdebstrap, mkosi or the disk tools write.** Holds. Nothing on
  the Windows side tars, and `frostroot guest` moves named files, not a
  tree.
- **The host's environment must not change the result.** Windows'
  environment does not reach the guest: only the listed variables cross.
  The VM's memory, processor count and accelerator are host facts too,
  and spike 8 checks that they change no byte.
- **Every value that reaches a shell is validated and quoted.** The
  command the guest runs is built from parsed flags; file names and file
  contents travel as data.
- **A failed or interrupted build writes nothing.** frostroot.exe keeps
  the builder's order and its undo, and its tests interrupt between every
  two steps. The overlay and the keys are removed on every path.
- **The lock.** No new field, `version = 1`. The lock does not say where
  it was built, because where it was built must not matter.
- **Trust.** `build` still never fetches keys, and on Windows it fetches
  nothing at all itself. The guest image and the Linux binary are trusted
  by a checksum compiled into a binary the release attests, not because a
  server served them. `--ca-bundle` reaches the guest's build as it
  reaches a Linux one, and neither the image nor the lock. Proxy
  credentials are never printed. The image fetch uses `pki.Transport`.
- **Files and the network.** Outputs go to a temporary file in the same
  directory and are renamed, never chmod'ed. The fetch is bounded by the
  pinned size and a timeout and honours the context. What the guest
  sends is bounded by the sizes it declares, under a cap, and the lock it
  sends is hostile input.
- **The command line.** Exit codes stay 0, 1, 2 and 130. `build` never
  prompts and never downloads. QEMU is ended only after the guest's
  frostroot has exited, or on the second Ctrl-C; inside the guest,
  mmdebstrap's process group still gets SIGINT.
- **Package boundaries.** `golang.org/x/crypto/ssh` is imported in one
  new package only. `builder` imports neither it nor the launcher. The
  package that finds and runs QEMU imports no other frostroot package.
- **Tests.** Unit tests start no QEMU: a fake guest that implements the
  real interface lives in the test file, and the SSH client is tested
  against the library's own server on loopback. The real thing is a
  scenario, run by hand on Windows, as `wsl-boot` is.

## The release

- `frostroot.exe` is built beside the Linux binary: `CGO_ENABLED=0
  GOOS=windows GOARCH=amd64`, `-trimpath`, and the Linux binary's SHA-256
  set with `-ldflags -X`, so the workflow builds Linux first. The guest
  image's URL, size and SHA-256 are constants in the source, changed by a
  commit that changes `guest/frostroot.lock`.
- `frostroot-windows-amd64.zip` holds `frostroot.exe`,
  `frostroot-linux-amd64` and the licence. `SHA256SUMS` names every file,
  and the attestation covers the zip and the guest image too.
- The workflow rebuilds the guest image from the committed lock, with
  `vendor` and `build --offline`, and fails unless it gets the pinned
  SHA-256. A release cannot publish a guest other than the one the source
  names. A release that does not change the guest re-uses the earlier
  asset by its URL.
- `go test ./...` also runs for `GOOS=windows` on a Windows runner, when
  Actions is on. While it is off, each pull request says what ran by
  hand, as now.
- `frostroot.exe` is not signed (decision 6).
- The README's "Install" gets a Windows section, "Scope" moves the native
  Windows binary out of "Deliberately not yet", and `build`'s message on
  Windows stops saying "run frostroot inside WSL".

## What the WSL 1 work could share

A sibling design covers building inside WSL 1 and importing through
`WslRegisterDistribution`. It is not designed here. The two meet in:

- **`frostroot.exe` itself**: the release's Windows build, its zip, its
  tests on Windows and the fixed test baseline.
- **The contract "run the Linux frostroot somewhere, and bring the lock
  and the image back"**: copying a recipe directory in by an explicit
  list, relaying lines and the exit code, and placing outputs atomically
  on NTFS. A WSL backend would be a second implementation of the
  interface the QEMU one is the first of.
- **The recipe-directory hold on Windows.**
- **An import step after the build**, which this design leaves to the
  user and that one automates.

## Spike: run on 2026-11-17

The owner asked for it on 2026-11-17 ("go with the recommended ones and
run the spike"), and for spikes 8 and 12 to wait (below). It ran on the
Windows 10 Enterprise LTSC 2019 machine (build 17763, an Intel i7-10700
with 16 logical processors and 16 GB, no admin rights, no hypervisor
enabled, endpoint security running) with the QEMU 11.1.0 for Windows
already unpacked there, beside the project's test VM, which kept running
throughout (6 GiB, 8 processors, TCG). Nothing was installed on Windows
and no setting changed. Images and Windows programs were built in the
test VM; no frostroot code changed. The frostroot that ran, in the VM
and in every guest, is a 0.15.1 Linux binary the test VM held from
earlier work, which reports master at `41c8cdd` with uncommitted changes
(`+dirty`), SHA-256 `096ec72d…7446964a`; it is not the published
v0.15.1 (`6ff2622b…ef0dd76d`), which was built later from `1e1558b`. The
spike measured the design, not that build, but a run of the plan's
scenarios uses a binary built from a commit. The programs and scripts of the spike are
beside this file as `.txt` files (see "The record").

The guest was the draft recipe of "The guest is a frostroot disk image",
built by that binary in the test VM: 558 packages, mmdebstrap 1.4.3, mkosi
20.2, kernel 6.8.0-149, frozen at 2026-11-17 11:13:01 UTC, in 2,285 s.
Each boot used a fresh qcow2 overlay of 40 GiB over the raw image, a copy
of QEMU's own `edk2-i386-vars.fd`, `edk2-x86_64-code.fd`, `q35`, `-cpu
max`, user-mode networking with one forward from 127.0.0.1:2223, and
`-accel whpx -accel tcg`, which here always said "falling back to tcg".
The guests ran with 4 processors and 2 GiB: the host had 1.3 to 2.6 GB
free beside the test VM, and the brief said to stop the guest, not the
VM, if the two did not fit. They fitted.

| Spike | Result | Decides |
|---|---|---|
| 1. The key | **Passed.** All three credentials (`ssh.authorized_keys.root`, `tmpfiles.extra`, `passwd.hashed-password.builder`) arrived through `-smbios type=11,path=FILE`, each file holding `io.systemd.credential.binary:NAME=BASE64`; `systemd-creds` listed them; root logged in with the per-boot key while its password stayed locked (`passwd -S root`: `L`); the user's password was set from the credential (`P`), so the first-boot prompt did not wait. | The guest is a plain frostroot disk recipe; nothing of frostroot is in the image. Credentials stay off the command line. |
| 2. The host key | **Passed.** `tmpfiles.extra` with two `f~` lines (base64) wrote the ed25519 host key at 18.2 s of the boot; sshd started at 24.4 s; the client, with `StrictHostKeyChecking=yes` and only that key known, connected on its first try. No other host key existed, so the image's own first-boot `ssh-keygen` made none. | The client pins the host key; no first-key-wins. |
| 3. Boot time | See "Spike 3" below. | Whether a direct kernel boot is needed. |
| 4. Bytes | **All three equal.** One Ubuntu 24.04 lock (`git`, `curl`, 260 packages, a 95 MB pool), made online in the test VM: rebuilt offline there unprivileged in 691 s, and twice offline as root in the guest in 452 s and 544 s. All three tarballs: `8fc4a571…dd06acbb`, 137 MB; the lock `122b645c…24ef763e` unchanged. | Decision 5 can promise Windows the bytes Linux writes. It is still the owner's. |
| 5. Fedora and a disk | Fedora: **passed**. The 24.04 disk: see "Spike 5" below. | Which targets the first version names, and the guest's memory. |
| 6. The copy | **Passed**, well above the 5 MB/s line: host to guest 17.4 MiB/s for 300 MiB and 23.9 MiB/s for 3 GiB (to the guest's disk, 18.5 and 21.6); guest to host 14.8 and 14.6 MiB/s (to NTFS, 12.6 and 15.1). Incompressible bytes, `cat` at both ends of Git for Windows' OpenSSH; both round trips intact. Copying the 1,208 MB compressed guest out of the test VM the same way: 92 s. | Files go through the SSH session; no second disk. A 3 GiB disk image comes back in about 3.5 minutes. |
| 7. Ctrl-C and the job object | **Settled, and it needs both.** With QEMU in frostroot's process group, a console Ctrl-C ended QEMU 66 ms after the event, exit status 0, before frostroot could do anything. With `CREATE_NEW_PROCESS_GROUP`, QEMU ignored it and only frostroot received it. A kill-on-close job object ended QEMU whenever frostroot ended, by exiting or by `Stop-Process -Force`; without the job object, QEMU outlived a killed frostroot. | The launcher starts QEMU in a process group of its own and a kill-on-close job object, as designed. |
| 8. The host must not show | **Not run.** It needs a second machine with the Hypervisor Platform enabled, which is the owner's to find. Spike 4 does show that a guest of 2 GiB and 4 processors writes the bytes 6 GiB and 8 processors write on Linux. | Still open: what WHPX saves. |
| 9. The guest's size | 3,038,773,248 bytes raw (2,898 MiB), 1,890,627,584 on disk (sparse), **1,208,380,371 with `gzip -n -9`** (1.13 GiB), in 666 s; Git for Windows' `gzip -d` unpacked it in 27 s. | One release asset, under GitHub's 2 GiB. A smaller kernel is not needed for that. |
| 10. A locked-down machine | **Passed, as far as it was seen.** A `frostroot.exe` cross-compiled in the VM (unsigned, 19 MB) ran from the user's profile: `version`, `init --plain` and `validate` worked and `build` refused as today. During the whole spike the endpoint security did nothing that was seen: no program blocked or removed, no file quarantined, the overlay and the forwarded port left alone. | Decision 6 can wait, on this machine. |
| 11. The network | **Passed.** Fedora's online build fetched over HTTPS through the re-signing network with `--ca-bundle` (spike 5). A proxy bound to the host's 127.0.0.1:3128 answered the guest as `http://10.0.2.2:3128` and refused it as `127.0.0.1`; a whole online Ubuntu build with `http_proxy=http://10.0.2.2:3128` passed in 813 s, all 254 of its requests answered 200 by that proxy. | The launcher rewrites a loopback proxy to 10.0.2.2, as designed. |
| 12. QEMU versions | **Not run.** It needs the owner to name the oldest QEMU to support. | The range is open; 11.1.0 is the one measured. |

### Spike 3: boot time

Five boots, each from a fresh overlay with fresh keys, 4 processors and
2 GiB, from QEMU's start to the first SSH login with the pinned host key:
**42, 43, 43, 46 and 47 s, median 43 s.** Every one reached `running`
with nothing failed; `systemd-analyze` said 36.8 s for the first and
39.3 to 39.6 s for the other four (10.7 to 11.2 s in
the kernel, the rest in user space, systemd-repart's growth of the root
into the 40 GiB overlay among it). Far under three minutes, so no direct
kernel boot: the guest boots through its own firmware, shim and GRUB, as
any frostroot disk does.

### Spike 5: Fedora and a disk in the guest

**Fedora 44** (a WSL recipe of `git`, 268 packages): online with
`--ca-bundle` in 1,250 s, `vendor` in 76 s (116 MB), then two offline
rebuilds in 466 s and 506 s. All three tarballs were `35072c9a…07564754`,
157 MB: the offline rebuilds equal each other and the online build. The
lock was `b4610281…4469e877`.

**Ubuntu 24.04 as a disk** (a disk recipe of `git`, 331 packages): online
with `--ca-bundle` in 1,765 s, a raw image of 2,434 MB, sparse
(`d0bba2b3…d91089ff`); `vendor` in 129 s (950 MB, the disk tools tree's
packages among them); then two offline rebuilds in 1,320 s and 1,166 s,
both `43afccad…f7139368`, the lock `ce3eb7df…562399e2` unchanged. The
offline image is not the online one, which frostroot does not promise
for any target. The disk tools tree ran under bubblewrap as root in the
guest without anything changed.

Over Fedora and the disk, sampled every two seconds, the guest never had
more than **516 MiB** in use of its 2 GiB, and swapped nothing; no
process was killed for memory. So 2 GiB is the default, with room.

### Found on the way

- **A suspended host stops the test VM's clock.** The VM's system clock
  was 14 hours behind its own hardware clock after the host slept, and
  apt refused the archive's Release files as "not valid yet". The guest
  boots fresh for each build and reads the clock as it boots, so it
  cannot drift that way within a build; a guest kept running across a
  suspend would. The launcher keeps none.
- **The overlay does not shrink on NTFS.** The first guest's overlay
  reached 20 GB holding builds the guest had since deleted, while C: fell
  to 6.8 GB free. `fstrim` in the guest released 34.5 GiB, and the file
  then stopped growing while the guest wrote, since QEMU reuses the freed
  clusters, but it never became smaller. A throwaway overlay per build,
  as designed, is what gives the space back; the launcher checks free
  space before it starts (exit 1), and the README says how much a build
  takes.
- **The disk image's own first boot costs little.** systemd-repart grew
  the root to 38.8 GB of the 40 GiB overlay and growfs grew ext4 in the
  same boot; `systemd-analyze` said 9.7 s in the kernel and 27.2 s in
  user space, and nothing failed.

### The record

The programs and scripts that ran, kept as text so that nothing builds
or runs them:

- [`…-spike-ctrlc.go.txt`](2026-11-16-frostroot-windows-builder-spike-ctrlc.go.txt),
  spike 7's program, built in the VM with Go 1.27.3 (`GOOS=windows
  GOARCH=amd64 CGO_ENABLED=0 go build`), SHA-256 `7386f2df…e33860fc`,
  unsigned, and driven by a PowerShell script that started it hidden four
  ways and asked afterwards whether its QEMU was alive;
- [`…-spike-boot-guest.sh.txt`](2026-11-16-frostroot-windows-builder-spike-boot-guest.sh.txt),
  one boot from a fresh overlay with its credentials (spikes 1 to 3);
- [`…-spike-in-guest.sh.txt`](2026-11-16-frostroot-windows-builder-spike-in-guest.sh.txt),
  the build steps run as root in the guest (spikes 4, 5 and 11);
- [`…-spike-copy.sh.txt`](2026-11-16-frostroot-windows-builder-spike-copy.sh.txt),
  spike 6;
- [`…-spike-tinyproxy.py.txt`](2026-11-16-frostroot-windows-builder-spike-tinyproxy.py.txt),
  spike 11's proxy;
- [`…-spike-guest.toml.txt`](2026-11-16-frostroot-windows-builder-spike-guest.toml.txt),
  the draft guest recipe.

The `frostroot.exe` of spike 10 was cross-compiled with `-trimpath` from
the test VM's checkout at `f13f09a`, a master commit before the release
that reports 0.15.1, SHA-256 `39f1e6d3…0d65b687`.


## Risks

- **One machine measured it.** Every spike ran on one Windows 10 LTSC
  2019 machine with one QEMU, beside a test VM that took most of its
  memory. WHPX (spike 8) and older QEMU builds (spike 12) are not
  measured, and neither is a machine whose security software is stricter.
- **Time.** Fourteen to thirty minutes for a first build under TCG, as
  the spike measured, and a student will find it long. WHPX is the answer where an
  administrator will give it; nothing is the answer where they will not.
- **The guest is large.** Kernel, firmware and two package managers, to
  be downloaded once per guest version by every user.
- **QEMU is somebody else's program on somebody else's schedule.** The
  Windows builds come from one maintainer's site, built from the master
  branch and described there as experimental (**read**). A version range
  and a spike per new version is the same discipline mkosi needed.
- **Security software.** This machine's once killed `tar` inside WSL. It
  has let QEMU run for weeks, which says nothing about the next machine.
- **Two operating systems double the checks**, with CI off.
- **The first-boot path runs on every build**: repart, growfs, host keys.
  It is the best-tested boot path the project has and still the slowest
  one to choose.

## Decisions

The owner answered on 2026-11-17, "go with the recommended ones and run
the spike", which takes the recommended answer to 1, 2, 3, 4, 6 and 7.
Decisions 5 and 8 stay open.

1. **The way: QEMU, WHPX first and TCG behind it**, behind an interface
   a WSL 2 backend could implement later. Rejected for now: WSL 2 first,
   which serves the machines that are served already.
2. **The first version finds a QEMU the user unpacked**, and says how.
   Nothing is redistributed. A QEMU the project republishes, pinned by
   checksum, would make the project a distributor of GPL binaries built
   by a third party; it can follow without changing the design.
3. **The guest is frostroot's own disk image**, for the lock. The owner
   chose the design, not a legal reading: Canonical's intellectual
   property policy allows redistributing Ubuntu "only where there has
   been no modification to it", and asks for approval or rebranding
   otherwise (**read**), and whether an image assembled from unmodified
   archive packages is a modification **stays unresolved for publishing
   it**. It has to be answered before a release carries the guest. The
   other answer remains Canonical's own cloud image, fetched from
   Canonical and pinned by SHA-256, with the build tools installed by apt
   at first use.
4. **`build` never downloads the guest.** An explicit `guest fetch`, as
   `sudo apt install mmdebstrap` is explicit on Linux.
5. **Open: what is promised about bytes across Windows and Linux.**
   Spike 4 says what is true; the promise is the owner's.
6. **`frostroot.exe` is unsigned for the first version**, unless spike 10
   says otherwise.
7. **The launcher's logic is testable on Linux**, the Windows-only files
   are small, and each pull request says what ran on Windows by hand.
8. **Open: the version** this ships as, and whether Fedora and disk
   recipes must work on Windows before it does.
