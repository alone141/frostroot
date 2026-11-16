# frostroot: building on Windows without WSL

Date: 2026-11-16
Status: **a draft for the owner. Nothing is decided, no code is written,
and the spike below has NOT been run.** The owner asked for a
`frostroot.exe` that works on a Windows machine with no WSL at all, by
bringing a small Linux of its own to build in. This document compares the
ways to do that, recommends one, and lists the experiments that must run
before the plan is trusted. Every statement about a tool says how it is
known: **verified** (run for this spec, on the Windows 10 Enterprise LTSC
2019 machine, build 17763, without admin rights, or recorded as run in
[`docs/VERIFICATION.md`](../../VERIFICATION.md) or an earlier spec's
spike), **read** (in the tool's documentation on 2026-11-16, or in this
repository's code), or **assumed** (neither, and a spike item says how to
find out). The version this ships in is the owner's to name. The tasks
are in
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
| Speed | a first Ubuntu build takes 10 to 20 minutes in the project's TCG test VM, 8 virtual CPUs and 6 GiB (**verified**, project record); boot to a login prompt 64 s for a frostroot 26.04 disk (**verified**, bare-metal spike, on a Linux host) | near native (**assumed**; cannot be measured here) | native: it is how frostroot is used today (**verified**) | native where it runs (**assumed**) |
| What is shipped | `frostroot.exe`, the Linux binary beside it, and a guest image fetched once | the same | `frostroot.exe` and the Linux binary; the distribution is the user's, or a frostroot-built tarball imported as a private one | an image on a registry, or nothing |
| The guest | a frostroot disk image, built from a recipe and a lock in this repository (below) | the same image | a frostroot WSL tarball of the same recipe, or whatever the user has | a Dockerfile, which pins nothing unless made to |
| Files across | over the guest's forwarded SSH port (**verified** as a way of working: the test VM is used so); QEMU's own folder sharing (9p) does not work with a Windows host (**assumed**, from memory; the first design does not use it) | the same | `/mnt/c`, but frostroot refuses to build under `/mnt`, so the recipe directory is copied in and the outputs out (**read**, AGENTS.md) | bind mounts through the runtime's VM |
| The corporate CA | `--ca-bundle`'s bytes go over the channel to the build's own `--ca-bundle`; apt, pip and dnf then fetch through QEMU's user-mode network (**verified** in the test VM on this network, `docs/VERIFICATION.md`) | the same | as inside WSL today | the same file, mounted |
| Ctrl-C and exit codes | frostroot.exe owns both ends: it sends SIGINT over the SSH session and returns the remote exit status (**read**: `Session.Signal`, `ExitError.ExitStatus` in `golang.org/x/crypto/ssh`) | the same | `wsl.exe` passes the exit code; an interrupt reaching the right process group is what `scripts/wsl.sh` exists to get right | the runtime's `stop` signals and exit codes |
| Licences of what is redistributed | QEMU is not redistributed (decision 2). The guest image is Ubuntu packages: see decision 3 | the same | none, or the same tarball question | the base image's |
| Trust rules | the guest image and the Linux binary are checksummed by the release binary; QEMU is the machine's own tool, version-checked, as mmdebstrap is on a Linux host | the same | the user's distribution is unpinned, as a Linux host is | a registry tag is a server deciding what is trusted, unless pinned by digest |
| Byte-identical offline rebuilds | expected, since the same binary reads the same lock and pool with pinned tools; **assumed** until spike 4 compares | **assumed** the same bytes as TCG; spike 8 compares | as today (**verified**) | as on a Linux host, if privileged |

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
  SMBIOS type 11 strings, which `disk-boot` already does for the
  password (**verified** there). frostroot.exe makes an ed25519 key pair
  for each build and passes the public half. Root's password stays
  locked; sshd lets a key in for a locked account when PAM is on
  (**read**, bare-metal spec). The user's password prompt of a disk image
  is answered the same way, with the credential the README documents and
  a hash of random bytes nobody keeps.
- **The Linux binary goes in over that session** and is checked there
  against the SHA-256 the release compiled into `frostroot.exe`. So the
  guest image changes only when its packages do, not with every frostroot
  release, and the two ends of the conversation are always one version.
- **The build runs as root.** The guest is a single-purpose VM, root needs
  no user namespace, and Ubuntu's AppArmor restriction on mkosi does not
  apply to root. Whether root and an unprivileged user write the same
  Ubuntu bytes is spike 4; the Fedora spike found they do once the umask
  is fixed, which it is.

The image is published as a release asset, gzip-compressed (`gzip -n`, so
the file is reproducible too; `compress/gzip` reads it on Windows without
a new dependency). A 26.04 disk with only the base was 1.4 GB on disk in
the bare-metal spike (**verified**); the guest adds mkosi, dnf and their
Python, and its size is **assumed** to stay under GitHub's 2 GiB per
asset (**read**) once compressed. Spike 9 measures it.

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

`-accel whpx -accel tcg`, `-cpu max`. Memory and processors default to 4
GiB and to the host's processors up to 8; `FROSTROOT_VM_MEMORY` and
`FROSTROOT_VM_CPUS` change them, and must never change the image
(spike 8).

QEMU runs in a Windows job object that ends it when frostroot.exe ends,
however that happens, and in a process group of its own, so that the
console's Ctrl-C reaches frostroot.exe and not QEMU (**assumed**; spike
7).

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
  a line that writes `/etc/ssh/ssh_host_ed25519_key`. The client then
  accepts that key and no other. Whether tmpfiles writes it before sshd
  starts on this image is spike 2; if it does not, the client accepts the
  first key the forwarded port shows, which is weaker only against
  another program on the same machine that takes the port first.
- **Credentials stay off the command line.** They go in a file in the
  work directory that `-smbios type=11,path=` names (**assumed**: that
  this QEMU has the option and that systemd reads it so is spike 1; the
  fallback is `value=` on the command line, as `disk-boot` passes it).
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
  the host's loopback is reachable from the guest as `10.0.2.2`
  (**assumed**, QEMU's user-mode network default); frostroot.exe rewrites
  that one case and says so.
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

- An Ubuntu recipe for WSL builds online and offline on Windows 10 1809
  or newer, amd64, with a QEMU in the measured range, 4 GiB of memory to
  give the guest and 20 GiB of free disk (both **assumed** until spikes 4
  and 5 measure them).
- Two offline builds of one lock on Windows are byte-identical with each
  other. That they are the bytes a Linux host writes is measured (spike
  4) and written down, and promised only if the owner chooses to
  (decision 5).
- Without acceleration, expect a first Ubuntu build to take 15 to 25
  minutes: a boot of one to two minutes, the 10 to 20 minutes the test VM
  takes, and the copy. This is an estimate from the project's own VM,
  not a measurement of this design.

Not promised until measured: Fedora recipes and disk recipes on Windows.
Nothing in the design excludes them, since the guest is a whole Linux and
the build is root's, but a 24.04 disk took 28 minutes to build in the
TCG test VM (**verified**, `docs/VERIFICATION.md`) and nobody has run
mkosi under this guest. Spike 5 decides which
of them the first version names as working, and `build` refuses the rest
on Windows with exit 1.

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

## Spike: NOT yet run

Each experiment, and what its result decides. They need the Windows
machine, its existing QEMU 11.1.0, and a guest image built in the test VM
from a draft of `guest/frostroot.toml`; no frostroot code changes. Canonical's
24.04 cloud image, which the test VM already boots here, stands in where a
step fails for a reason that is the image's.

1. **The stock disk image takes a key.** Boot the draft guest with QEMU
   for Windows, its bundled edk2 and TCG, passing `ssh.authorized_keys.root`
   and the password credential in a file through `-smbios
   type=11,path=`; log in as root with the key. *Decides:* whether the
   guest can be a plain recipe. If the key does not arrive, the choice is
   between a recipe field that adds a file (a feature of its own) and
   Canonical's cloud image with cloud-init (decision 3's other answer).
2. **The host key from a credential.** Pass `tmpfiles.extra` writing the
   host key; check sshd presents it. *Decides:* pinned host key, or
   first-key-wins on loopback.
3. **Boot time from a fresh overlay**, five times: QEMU start to an SSH
   answer, with the root grown into 40 GiB. *Decides:* if the median is
   over three minutes, the plan adds a direct kernel boot (`-kernel`,
   `-initrd`, two more release assets) before anything else.
4. **Bytes.** One Ubuntu WSL lock with a pool: two offline builds as root
   in the guest, and one unprivileged on a Linux host with the same
   mmdebstrap. *Decides:* the promise. All three equal: Windows and Linux
   are promised the same bytes (decision 5). The two guest builds equal
   and the Linux one not: promised within Windows only, and the difference
   is found before any code. The two guest builds unequal: the design
   stops here.
5. **Fedora and a disk in the guest.** One Fedora WSL build and one 24.04
   disk build, online then offline twice, with time and peak memory.
   *Decides:* which targets the first version names, and the default
   memory.
6. **The copy.** Send 300 MB and 3 GB through an SSH session under TCG,
   both directions, with `cat` standing in for `frostroot guest`.
   *Decides:* below 5 MB/s, big files move through a second virtio disk
   instead, which is a larger plan.
7. **Ctrl-C and the job object.** A small Go program on Windows starts
   QEMU in a job object and a process group of its own; press Ctrl-C;
   kill the program from Task Manager. *Decides:* how the launcher keeps
   QEMU from outliving it and from dying first.
8. **The host must not show.** Spike 4's offline build again with 2
   processors and 3 GiB, and on a machine with the Hypervisor Platform
   enabled, with WHPX. *Decides:* whether the VM's shape is free or
   fixed, and what WHPX saves. Needs a second machine, which is the
   owner's to find.
9. **The guest's size.** Raw, on disk, and after `gzip -n -9`. *Decides:*
   one asset or a split; and whether a smaller kernel flavour for a disk
   recipe is worth a feature.
10. **A locked-down machine.** Does an unsigned `frostroot.exe`, built by
    cross-compiling and copied here, run from the user's profile, and
    does endpoint security leave the overlay and the SSH port alone?
    *Decides:* whether decision 6 can wait.
11. **The network.** An online Ubuntu build in the guest through this
    network with `--ca-bundle`, with and without a proxy variable.
    *Decides:* the proxy rewriting rule. The test VM has shown the CA
    half already.
12. **QEMU versions.** Spikes 1 and 3 again with the oldest QEMU for
    Windows the owner wants supported. *Decides:* the range.

## Risks

- **The whole design rests on spike 1 and spike 4.** Both are cheap, and
  both come before the first pull request.
- **Time.** Twenty minutes for a build is what the project's own VM does,
  and a student will find it long. WHPX is the answer where an
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

## Decisions for the owner

1. **The way.** *Recommended:* QEMU, WHPX first and TCG behind it, behind
   an interface a WSL 2 backend could implement later. The alternative is
   WSL 2 first, which serves the machines that are served already.
2. **Where QEMU comes from.** *Recommended:* the first version finds a
   QEMU the user unpacked, and says how. It redistributes nothing and
   every other step can be built and measured against it. The alternative
   is a QEMU the project republishes, pinned by checksum, which is
   friendlier and makes the project a distributor of GPL binaries built by
   a third party, with the source to offer beside them. It can follow
   without changing the design.
3. **The guest: frostroot's own disk image, or Canonical's cloud image.**
   *Recommended:* frostroot's own, for the lock. But Canonical's
   intellectual property policy allows redistributing Ubuntu "only where
   there has been no modification to it", and asks for approval or
   rebranding otherwise (**read**). Whether an image assembled from
   unmodified archive packages is a modification is not this document's
   to say; the project hands such images to students today and has not
   published one. The alternative fetches Canonical's own cloud image
   from Canonical, pinned by SHA-256, and installs the build tools in it
   with apt at first use: nothing is redistributed, the tools are as
   unpinned as on any Linux host, and the guest keeps state between
   builds.
4. **`build` never downloads the guest.** *Recommended:* an explicit
   `guest fetch`, as `sudo apt install mmdebstrap` is explicit on Linux.
   The alternative is a first `build` that fetches several hundred
   megabytes by itself.
5. **What is promised about bytes across Windows and Linux**, once
   spike 4 has said what is true.
6. **Signing `frostroot.exe`.** Unsigned, Windows warns about it and some
   machines refuse it. A certificate costs money and a name. *Recommended:*
   unsigned for the first version, unless spike 10 says otherwise.
7. **How Windows tests run.** There is no Go on the Windows machine by
   the owner's choice, and CI is off. *Recommended:* keep the launcher's
   logic free of Windows so that `scripts/check.sh` tests it on Linux,
   keep the Windows-only files to a few dozen lines each, and say in each
   pull request what ran on Windows by hand.
8. **The version** this ships as, and whether Fedora and disk recipes
   must work on Windows before it does.
