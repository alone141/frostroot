# frostroot on WSL 1

Date: 2026-11-16
Status: **the owner decided on 2026-11-16: the import half only.** The
spike, approved the same day ("Yes, run it"), found that Ubuntu 24.04's
systemd does not configure on the WSL 1 kernel, so building inside WSL 1
could offer 20.04 and perhaps 22.04 only, and the owner dropped it.
Nothing is implemented. This document is desk research and read-only
probes of one such machine, the design as first proposed, the spike and
its results, and the decision. Everything it says about building on
WSL 1 (in "Goal", "What is promised", "The build mode" and the rules) is
kept as the record of what was considered and is **not** what will be
built; "The import path" is. The version this ships in is the owner's to
number.
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md).
A sibling document, planned separately, covers a Windows builder that
needs no WSL at all; "What the two could share" below is the seam.

## Why

frostroot builds only where the kernel makes user namespaces, or as root,
and its README sends every WSL user to WSL 2. WSL 2 needs Windows 10
version 1903 or newer and a hypervisor. A lab PC on Windows 10 Enterprise
LTSC 2019 (version 1809, build 17763) has neither and never will: LTSC
2019 is supported until January 2029, it has no Store, and machines like
it are exactly where a frozen, offline-rebuildable image is wanted. Such a
machine fails twice today:

- `frostroot build` refuses it: "this is WSL 1, which has none; convert
  the distribution to WSL 2".
- The image it would have built cannot be imported: the `wsl.exe` of 1809
  has `-d`, `-e` and `-u` and nothing else. `wsl --import` arrived in
  build 18305 (version 1903).

So there are two halves, and each is useful without the other: building
inside WSL 1, and turning a tarball into a distribution on a Windows whose
`wsl.exe` cannot.

## Goal

- On WSL 1, `sudo frostroot build` builds an Ubuntu WSL tarball, online
  and offline, with everything such a recipe can hold: packages,
  third-party sources, `[python]`, `[certificates]`, `--ca-bundle` and
  `--insecure`.
- `init`, `edit`, `validate`, `capture` and `vendor` work on WSL 1 as
  yourself, as they do anywhere.
- A Windows program shipped with each release registers a frostroot
  tarball as a distribution on Windows 10 from version 1709 on, without
  administrator rights, and leaves the user of the recipe as its default
  user.
- Where frostroot does not work on WSL 1 it says so before anything
  starts, with exit 1 and the way out.
- Nothing changes for a build on Linux or WSL 2: the same command lines,
  the same bytes, every lock still loading and rebuilding.

## Non-goals, for the first WSL 1 version

- **Fedora on WSL 1, and disk images on WSL 1.** Both are refused, as root
  too; "What is promised" says why.
- **Building without root.** WSL 1 has no user namespaces, and the modes
  that do without them make a worse image ("The build mode").
- **systemd in the imported distribution.** WSL 1 starts its own init and
  never systemd, whatever `/etc/wsl.conf` says.
- **A build host older than Ubuntu 24.04.** The one distribution that
  starts on the machine at hand is 20.04, and it cannot be the build
  host ("The build host").
- **A promise that an image built on WSL 1 is the bytes a Linux host
  builds from the same lock.** The spike measures it; the promise stays
  what it is, two offline builds of one lock on one host.
- **A Windows builder that needs no WSL.** The sibling document's.

## What was looked at, and how far each statement goes

Every statement in this document is one of three kinds, and says which:
**verified** on the machine at hand on 2026-11-16 with a probe that
created, mounted and unpacked nothing; **read** in documentation or in a
tool's own source; or **assumed**, which the spike must turn into one of
the other two.

The machine: Windows 10 Enterprise LTSC 2019, build 17763, WSL 1 only.
`wslconfig /l` lists two distributions. The default, an Ubuntu 24.04
registered from an unpacked launcher package, does not start ("the system
cannot find the path specified"): its registration names a base path
that no longer exists. The other, Ubuntu 20.04.6, starts, and every Linux
probe ran there: kernel release `4.4.0-17763-Microsoft`, glibc 2.31,
util-linux 2.34, apt 2.0.11, root file system `lxfs`.

### The kernel WSL 1 emulates

| | As yourself | As root | |
|---|---|---|---|
| `unshare --user true` | fails, EINVAL | fails, EINVAL | verified |
| `unshare --mount --propagation unchanged -- true` | fails, EPERM | **exit 0** | verified |
| `unshare --pid --fork`, `--uts`, `--ipc` | fail, EPERM | exit 0 | verified |
| `unshare --net true` | fails, EACCES | **fails, EACCES** | verified |
| effective capabilities | none | all 37, `CAP_SYS_ADMIN` among them | verified |
| `/proc/sys/user/max_user_namespaces` | absent | absent | verified |

Also verified: `/proc`, `/sys`, a tmpfs `/dev` and `devpts` are mounted;
`/proc/filesystems` lists proc, sysfs, tmpfs and devpts; `getcap` reads
`cap_net_raw+ep` off `/usr/bin/ping`, so `lxfs` stores the
`security.capability` attribute; `sleep 0.2` works; `wsl -u root` gives
a root shell without a password, where `sudo -n` asks for one.

Read, in Microsoft's WSL release notes: `chroot` since build 14936,
`pivot_root` since 14942, mount and unmount of tmpfs, procfs and sysfs
since 14901, `unshare` of the mount namespace since 14915, the xattr
system calls and `security.capability` since 16226.

Assumed, and the spike's to find out: that `mknod` of a character device
works on `lxfs` and the node behaves; that a bind mount of a file onto a
file works; that proc and sysfs mount a second time inside a chroot; that
hardlinks made by dpkg survive; and that the userland of Ubuntu 22.04,
24.04 and 26.04 runs its maintainer scripts on this kernel. glibc 2.31's
`sleep` once failed on WSL 1 kernels (`cannot read realtime clock`); it
works here on 20.04, whose glibc Canonical patched, and that says
nothing of 24.04's.

### mmdebstrap

Read in the source of mmdebstrap 1.4.3, the version Ubuntu 24.04 ships
and frostroot is verified against:

- `--mode=root` runs `chroot` directly and unshares nothing. Before it
  starts it decides whether it can mount: `CAP_SYS_ADMIN` in the
  effective and the bounding set, and `unshare --mount --propagation
  unchanged -- true` exiting 0. **Both hold for root on this machine**
  (verified, the table above), so mmdebstrap would not print the
  "cannot mount because" warnings that frostroot's mount watch stops a
  build on.
- It then tries `mknod test-dev-null c 1 3` in the new root and writes
  to the node. If that works it makes the device nodes itself; if not,
  and it can mount, it bind-mounts the host's nodes over empty files.
  Either way the `/dev` entries of the tarball are tar headers it writes
  by hand, so the image's `/dev` does not depend on the answer.
- It mounts sysfs and proc in the chroot with `mount(8)` after the
  essential packages are unpacked, and unmounts them at the end, in the
  host's own mount namespace. That is why frostroot never sends it
  SIGKILL.
- It packs with `tar --numeric-owner --xattrs --format=pax`, as real
  root, so owners are the real numbers and capabilities travel as pax
  records.
- `--mode=unshare` needs `CLONE_NEWUSER` unless it is root, and as root
  still unshares the mount, pid, uts and ipc namespaces, which root on
  WSL 1 can make. It is a second way to build as root, and the fallback
  if root mode trips over the shared mount namespace.
- `--mode=fakechroot` cannot mount anything and (read in the manual of
  1.5.7) fails when the chroot's libc is not the host's.
- There is no proot mode; the word does not occur in the source.
- The package depends on apt 2.3.14 or newer (read from `dpkg -s` in the
  project's 24.04 test VM).

### Registering a distribution on Windows 10 1809

- `C:\Windows\System32\wslapi.dll` on this machine exports
  `WslRegisterDistribution`, `WslUnregisterDistribution`,
  `WslConfigureDistribution`, `WslGetDistributionConfiguration`,
  `WslIsDistributionRegistered`, `WslLaunch` and `WslLaunchInteractive`
  (verified, read from the file's export names).
- `WslRegisterDistribution(distributionName, tarGzFilename)` takes a
  name and the full path of a `.tar.gz`, and returns
  `ERROR_ALREADY_EXISTS` for a name in use (read, Microsoft's reference
  page). The page says nothing of where the file system goes.
- Registrations are per user, under
  `HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss\{GUID}`, with
  `DistributionName`, `BasePath`, `Version`, and, once configured,
  `DefaultUid` and `Flags` (verified: the Store Ubuntu has uid 1000 and
  flags 7; the broken 24.04 has neither). So no administrator is needed
  to register one (assumed from that; the key is the user's own).
- The base path of the Store distribution is its package's `LocalState`;
  the base path of the one registered from an unpacked launcher is the
  directory that launcher's executable was unpacked into (verified in
  the registry). wsldl, a third-party launcher that calls the same
  function, tells its users to put the executable in "the installation
  directory" (read). So **the file system lands beside the calling
  executable** (assumed from those two; the spike's first experiment).
- Calling `wslapi.dll` from PowerShell through `Add-Type` fails: every
  function returned `0x80070005`, access denied, for every name, the
  registered ones included (verified). The cause is assumed, not known:
  either the calling process must set up COM security before the DLL
  talks to the LxssManager service, which PowerShell has already done
  its own way, or the endpoint software refuses it. Either way a script
  is not the launcher.
- `wslconfig.exe` on this machine has `/l`, `/s`, `/t`, `/u` (unregister)
  and `/upgrade` (verified).
- `/etc/wsl.conf` on build 17763 is read for `[automount]`, `[network]`
  and `[interop]`. `[user] default=` arrived in build 18980, and
  `[boot] systemd=true` belongs to WSL 2 (read, the release notes). So
  on this Windows the default user frostroot writes into `wsl.conf` does
  nothing, and whoever registers the distribution has to set it.
- From inside WSL 1, `wslpath -w` of a path on `lxfs` fails with
  "Invalid argument", and of a path under `/mnt/c` answers `C:\...`
  (verified). There is no `\\wsl$` before version 1903, and
  `WSL_DISTRO_NAME` is not set. A tarball left on `lxfs` cannot be named
  to a Windows program at all.

### The security software

On 2026-11-10, unpacking a Go toolchain tarball inside the 20.04
distribution got `tar` killed, and `/bin/tar` itself was gone afterwards
(recorded by an earlier session). **It is still gone** (verified today:
`tar: not found`). Every file a WSL 1 distribution holds is an NTFS file
that software on the Windows side can read, quarantine or delete, and
every Linux process is a Windows process it can kill. A build is the
same thing ten thousand times over: dpkg unpacks every package's
executables into the work directory, and tar packs them.

## What is promised on WSL 1, and what is not

| | On WSL 1 | Why |
|---|---|---|
| `init`, `edit`, `validate`, `capture`, `vendor` | work, as yourself | None of them needs a namespace or root. The `tui` scenario already passes on this machine (recorded 2026-11-11); the rest is assumed. |
| `build` of an Ubuntu WSL tarball | works **as root**, if the spike's experiments 3 to 6 pass | mmdebstrap's root mode needs chroot, mount and the mount namespace test, which root has here. |
| The Ubuntu releases | each one the spike builds and boots, and no other | Whether a release's maintainer scripts run on this kernel is measured per release, not argued. |
| `build` without root | refused, exit 1, naming `sudo frostroot build` | No user namespace. |
| `build` of a Fedora image | refused, exit 1, as root too | mkosi 20.2 runs every step in bubblewrap, and frostroot gives it a tools tree made the same way. Not probed beyond that; the cost of finding out is a Fedora tools tree unpacked under the security software, for a family the owner did not ask for here. |
| `build` of a disk image | refused, exit 1, as root too | Every disk tool runs under `bwrap --unshare-all`, which unshares the network namespace, and root cannot make one here (verified). |
| Two offline builds of one lock, on one WSL 1 host, are the same bytes | promised only if experiment 8 shows it | It is what `build --offline` means. If it fails, WSL 1 support does not ship with offline builds claimed. |
| The same bytes as a Linux host builds | not promised; measured in experiment 9 | The README already says the promise does not cross mmdebstrap versions. |
| systemd, and services that need it, after import | no | WSL 1. |
| The image imported on a newer Windows with `wsl --import` | as for any image | It is the same tarball. |

## The build mode: root, and why not the others

**Root mode under `sudo`.** It is the mode frostroot already drives for
uid 0, tested on every release, and it is the only one in which the
image's ownership, hardlinks, capabilities and device entries come from
the same code path as on Linux: real root owns real files, and
mmdebstrap's tar reads them. Root on WSL 1 is not a Windows
administrator; it is the Windows user with another number, so `sudo`
here crosses no boundary that matters on Windows.

`Mmdebstrap.Preflight` already passes for root on WSL 1; the test case
"WSL 1 as root" says so. So **the released v0.15.1 may already build on
WSL 1 under `sudo`**, and nobody has tried (assumed; experiment 6). What
the code must gain is then small: the refusal for an unprivileged user
names `sudo frostroot build` beside WSL 2, Fedora and disk recipes are
refused on WSL 1 before they start, and whatever the spike finds.

**Unshare mode as root** is the fallback. mmdebstrap then works in mount
and pid namespaces of its own, which root on WSL 1 can make, so an
interrupted build cannot leave proc mounted in the host's namespace. It
is not the first choice because frostroot has never driven it as root
and because build 14915's notes record a wrong working directory after
`unshare(CLONE_NEWNS)`. Experiment 5 runs both.

**fakechroot is rejected.** It mounts nothing, so systemd's maintainer
scripts come out as they did in the builds the mount watch was written
to stop: no tmpfiles entries, a stub `hwdb.bin`. It needs the build
host's libc to be the image's, so one host builds one release. And
ownership is a database fakeroot keeps, not the file system's.

**proot is rejected.** mmdebstrap has no such mode, and proot rests on
ptrace, which WSL 1 emulates in part.

**Unpacking with Go, or bootstrapping by hand, is rejected** by the rule
that only mmdebstrap writes the tarball.

### The build host

mmdebstrap 1.4.3 needs apt 2.3.14. Ubuntu 20.04 has apt 2.0.11 and
offers mmdebstrap 0.4.1 (verified with `apt-cache policy`), which
frostroot was never run against. So the build host is **an Ubuntu 24.04
distribution on WSL 1**, and this machine has none that starts. Making
one is the import problem again: Ubuntu publishes its WSL root file
system as a `.tar.gz`, and the launcher below registers it. The spike
therefore starts with the import half, and its first imported
distribution is its build host.

### What the build does differently on WSL 1

Nothing in the image. On the host:

- **The work directory stays on `lxfs`**, under `/var/tmp/frostroot-0`.
  frostroot already refuses `/mnt`.
- **The recipe directory goes on a Windows drive**, `/mnt/c/...`, so
  that `dist/` is somewhere Windows can name. frostroot already supports
  that: outputs are renamed into place and never chmodded. On `lxfs` the
  tarball would be unreachable from Windows. `build` says so on WSL 1
  when the recipe directory is not under `/mnt`: a warning on stderr,
  not a refusal, since the tarball can still be copied.
- **The import hint names the launcher**, not `wsl --import`, when the
  host is WSL 1.
- **A tripwire, if experiment 10 shows it clean on a normal build**: the
  last customize hook runs `dpkg --verify` in the image and fails the
  build on a missing file or a changed checksum outside the
  configuration files. It only reads. It is there because a file the
  security software removed from the work directory would otherwise
  leave an image that looks whole. Whether it runs on every host or on
  WSL 1 alone is decided by what it costs and finds on Linux.

## The import path

A tarball becomes a distribution through `WslRegisterDistribution`, the
call the Store launchers make. frostroot ships the caller.

**One binary, `frostroot-windows-amd64.exe`, with one new command:**

```
frostroot import NAME INSTALL-DIR TARBALL
```

mirroring `wsl --import NAME INSTALL-DIR TARBALL`, so that the README
teaches one shape. It:

1. Validates before touching anything: the name against the pattern the
   recipe already holds image names to, the tarball a readable `.tar.gz`,
   the install directory absent or empty, the name not registered
   (`WslIsDistributionRegistered`). Exit 1 for each, saying which.
2. If the file system lands beside the calling executable (experiment
   1): creates the install directory, copies itself into it, and runs
   the copy, which does the rest. The parent removes the copy
   afterwards. If the base path follows something else, this step is
   whatever experiment 1 says.
3. Calls `WslRegisterDistribution(NAME, TARBALL)`. Windows unpacks the
   tarball; frostroot's code never opens it. That keeps the rule that Go
   never tars or walks a root file system.
4. Reads `[user] default` out of the new distribution's own
   `/etc/wsl.conf`, the file a newer WSL would read, by running `cat`
   there through `WslLaunch`; asks the distribution for that user's uid
   with `id -u`; and calls `WslConfigureDistribution` with it and the
   three default flags (interop, Windows PATH, drive mounting). The
   image stays the bytes it is, and behaves on 1809 as it does on
   Windows 11.
5. On any failure after step 3, unregisters what it registered
   (`WslUnregisterDistribution`), so a failed import leaves nothing, as a
   failed build does.

It never prompts, prints results on stdout and progress on stderr, and
exits 0, 1 or 2 as `build` does. On Linux, `frostroot import` exits 1 and
says to run the Windows binary.

**Why the same binary and not a second program.** The module already has
to compile for Windows, and `scripts/check.sh` proves it on every push;
the Windows binary is that build, published. One binary means one
version, one `SHA256SUMS`, one attestation, and `frostroot version` on
both sides of an import. Its `build` on Windows keeps saying that builds
need Linux until the sibling's work changes that. The cost is size: the
Charm libraries ride along for a command that draws nothing.

**The DLL** is loaded by its full path under the system directory, never
by bare name, so that a `wslapi.dll` beside a downloaded executable is
not what runs. The standard library's `syscall` does this; no new
dependency. The calls live in a new package, `internal/wslapi`, which
imports no other frostroot package and holds no policy: `_windows.go`
with the calls, `_other.go` returning "not Windows". `internal/cli`
talks to it through a small interface it declares, and its tests use a
fake.

**COM security** is assumed to be the launcher's to set up before the
first call (impersonation, as the PowerShell failure suggests).
Experiment 1 settles it with the real thing.

**Shipping and attesting.** `release.yml` gains a second build step,
`GOOS=windows GOARCH=amd64`, with the same flags. `SHA256SUMS` lists both
files, the attestation's `subject-path` names both, and `gh release
create` uploads both. The Linux runner cannot run the `.exe`, so "Check
the binary" stays the Linux file's; the Windows binary's proof is
`scripts/check.sh`'s Windows build, a `GOOS=windows go vet`, and the
`wsl-boot` scenario on a WSL 1 host before the tag, by the owner. The
binary is unsigned. Whether a locked-down Windows runs an unsigned
executable from a download is not frostroot's to decide; experiment 13
finds out for this machine.

**On a Windows that has `wsl --import`**, the README keeps teaching
`wsl --import`. `frostroot import` works there too (assumed; the function
is still in `wslapi.dll`), and registers with whatever version is that
machine's default.

## The rules in AGENTS.md this touches

**What reaches the image**

- *Build-only apt settings go in through a setup hook.* Unchanged; the
  hooks are the same text. `apt-trust` runs on WSL 1 in the plan.
- *Only mmdebstrap or mkosi writes the tarball; Go never tars, walks or
  deletes a root filesystem.* Kept on both halves: mmdebstrap packs as
  root, and Windows unpacks. The rule gains a sentence: `import` hands
  the tarball to Windows and never reads inside it.
- *Two offline builds of one lock are byte-identical.* Kept only if
  measured (experiment 8). What could break it is what root mode reads
  from the host that unshare mode does not, and a file system that
  stores Linux metadata in NTFS attributes.
- *Nothing the build only used stays in the image.* The risk particular
  to root mode is mmdebstrap's own scaffolding, the device nodes or
  bind mounts and `test-dev-null`. `no-build-leaks` runs on WSL 1 in
  the plan.
- *The host's environment must not change the result.* `sudo` resets the
  environment; `wsl -u root` does not, and WSL appends the Windows PATH
  to every process's. That is so on WSL 2 as well and has not reached an
  image there. Experiment 9 compares a WSL 1 build with a Linux one and
  would show it.
- *Every value that reaches a shell is validated and quoted.* The
  tripwire hook, if it ships, is new hook text: run through a real `sh`
  with hostile paths, and covered by
  `TestHooksAndScriptNeverSwallowFailures`. `import` passes the name,
  the paths and the user name to Windows calls as arguments, never
  through a command line it assembles, and the user name read back out
  of `wsl.conf` is hostile input: it is held to the recipe's user-name
  pattern before it goes anywhere.

**The lock**

- *A failed or interrupted build writes nothing and leaves the previous
  image.* Unchanged in code, and newly at risk in one way: root mode
  mounts in the host's namespace, so a build the security software
  kills leaves proc and sysfs mounted in the kept work directory.
  Experiment 7 looks at what is left after an interrupt and after a
  kill. If mounts are left, the next build must refuse a work root with
  mounts under it, and say how to clear them, rather than meet them.
- *The lock stays `version = 1`.* No new field. Where an image was
  built is not a fact about the image.

**Trust**

- Nothing changes in what is fetched or verified. `import` verifies
  nothing about the tarball, as `wsl --import` does not; the release's
  checksum and attestation cover the Windows binary as they cover the
  Linux one.

**Files and the network**

- *Outputs are renamed into place and never chmodded.* This is what lets
  `dist/` sit on `/mnt/c`. As root on a Windows drive, files come out
  owned by the drive's mount uid, not root, so the README's "Building as
  root" note reads differently there (assumed; experiment 6).
- *`capture` only reads, and needs no root.* Unchanged; capturing a
  WSL 1 distribution should just work, and is in the plan's checks.

**The command line**

- *A new error the user can fix goes into the `errors.Is` list in
  `reportBuildFailure`.* Three do: WSL 1 without root, a Fedora recipe on
  WSL 1, a disk recipe on WSL 1. Exit 1 each.
- *`build` never prompts*, and neither does `import`.
- *mmdebstrap's process group gets SIGINT, never SIGKILL.* More
  important here, since root mode must unmount.
- *The plain interface asks the same questions in the same order.* No
  recipe field is added, so no question is.

**Package boundaries**

- `internal/wslapi` joins the packages that import no other frostroot
  package. `builder` learns whether the host is WSL 1 from the file it
  already reads, `/proc/sys/kernel/osrelease`, through the `ReadFile` it
  already takes.

**Tests**

- Unit tests stay without root and without mmdebstrap: the host kind is
  a table of kernel release strings, and `import` runs against a fake.
- No integration test can cover WSL 1: CI has no such runner, and CI is
  off. The proof is the scenarios, run on a WSL 1 host by hand before a
  release, as `wsl-boot` is run on Windows today. `wsl-boot` learns
  WSL 1: it imports with the Windows binary, skips the systemd check,
  and unregisters with `wslconfig.exe /u`.

## What the two could share

With the Windows builder the sibling document plans:

- **The published Windows binary** and everything in `release.yml` that
  builds, sums and attests it.
- **`frostroot import`** and `internal/wslapi`: a builder on Windows
  ends at the same tarball and needs the same registration on a Windows
  without `wsl --import`.
- **The host-kind check**: one place that says Linux, WSL 2, WSL 1 or
  Windows, read by Preflight and by the import hint.
- **The README's "Deliberately not yet" line.** "A native Windows
  binary" stops being true the day the Windows binary ships with
  `import` alone; the line should then say what is still missing, a
  build without WSL, until the sibling's work lands.

Neither design depends on the other. If the Windows builder ships first,
this document's import half is already there and only the build half
remains.

## Risks

- **The security software.** It has already removed a system binary from
  a distribution on this machine. A build may be killed midway, may lose
  files without failing, or may get the machine reported. The tripwire
  and the byte-identity check catch a damaged image; nothing in frostroot
  can make the build welcome. The owner may need an exclusion from
  whoever runs the software, for the distribution's directory under
  `%LOCALAPPDATA%` and for the install directories. Without one, WSL 1
  support may be something frostroot has and this machine cannot use.
- **The kernel is an emulation frozen in 2018**, with fixes until 2021
  by its build date. A maintainer script in a release newer than it may
  make a system call it does not have. That is per release and per
  package, and a recipe's own packages can hit it where the base image
  did not. The failed build then explains itself as any failed
  maintainer script does.
- **`lxfs` is slow**, and a build is mostly small files. Minutes on
  Linux may be an hour here; experiment 6 times it.
- **Root-mode leftovers** in the host's mount namespace after a killed
  build, above.
- **An unsigned executable** on a managed Windows.
- **Nobody else has this machine.** CI is off and could not run it
  anyway, so WSL 1 stays verified by hand, on one build of Windows, by
  one person, per release.

## The spike: the experiments

This is the list as written before any ran; "Spike results" below says
which ran and what they found. Each unpacks, installs or registers
something on the machine at hand and needs the owner's go-ahead; the
first two can be given separately from the rest, since they touch
Windows and not the existing distribution. Run in order: each decides
whether the next is worth running. Record after each whether `/bin/tar`
and the files just written are still there, and anything the security
software says.

Throughout: `SPIKE` is a new distribution name such as
`frostroot-spike`; commands marked `PS>` run in PowerShell, `$` inside
the spike distribution as root (`wsl -d SPIKE -u root`), `vm$` in the
project's Linux test VM.

**0. Say so first.** Tell whoever runs the endpoint software what is
about to happen, or decide not to. *Decides:* whether the rest runs on
this machine at all, and whether an exclusion is on the table.

**1. Register a root file system with a throwaway launcher.** With a Go
toolchain for Windows in a scratch directory (no install), build a
program of some sixty lines that sets up COM security and calls
`WslRegisterDistribution`, and run it from an empty directory `D`,
started from another directory, on Ubuntu's published 24.04 WSL root
file system, checked against Ubuntu's `SHA256SUMS`:

```
PS> cd $env:TEMP; & D\spike-import.exe SPIKE C:\path\ubuntu-noble-wsl-amd64.rootfs.tar.gz
PS> wslconfig /l
PS> reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss /s
PS> wsl -d SPIKE -e sh -c "id; cat /etc/os-release; sleep 1; echo ok"
```

*Decides:* whether the call works without an administrator and from an
unsigned program; whether COM security must be set up and how; whether
`BasePath` is the executable's directory, the current directory or
something else, which is step 2 of `import`; whether an Ubuntu 24.04
userland starts on this kernel at all. If it does not start, repeat with
22.04, and the build host question reopens: 22.04 ships an older
mmdebstrap than the 1.4 and 1.5 frostroot has been run against.

**2. Configure it.** With the same program: `WslLaunch` running
`cat /etc/wsl.conf` and `id -u root` with a pipe for stdout, then
`WslConfigureDistribution(SPIKE, 0, 7)`, then
`WslUnregisterDistribution` on a second throwaway name. *Decides:*
steps 4 and 5 of `import`.

**3. Read-only look at the new host.**

```
$ uname -r; id; grep Cap /proc/self/status
$ unshare --mount --propagation unchanged -- true; echo $?
$ apt-get --version; apt-cache policy mmdebstrap
```

*Decides:* that the table under "The kernel WSL 1 emulates" holds in a
24.04 userland as it did in 20.04.

**4. Install the build tools.**

```
$ apt-get update && apt-get install -y mmdebstrap ubuntu-keyring
$ command -v tar mmdebstrap; dpkg --verify tar mmdebstrap
```

*Decides:* whether apt and dpkg of 24.04 run here, and whether the
security software lets a few hundred packaged files land. If `tar`
disappears again, stop: nothing below can work without an exclusion.

**5. mmdebstrap by hand, smallest first.**

```
$ mkdir -p /var/tmp/spike && cd /var/tmp/spike
$ mmdebstrap --verbose --mode=root --variant=essential \
    --customize-hook='cat "$1/proc/mounts" > mounts.txt' \
    --customize-hook='ls -l "$1/dev" > dev.txt' \
    noble essential-root.tar.gz 2>&1 | tee essential-root.log
$ grep -n '^W:\|^E:\|mknod\|skipping' essential-root.log
$ grep -c spike /proc/mounts
```

and the same with `--mode=unshare`. *Decides:* root mode or unshare
mode; whether `mknod` works or mmdebstrap falls back to bind mounts;
whether proc and sysfs were really mounted in the chroot (`mounts.txt`);
whether anything stays mounted afterwards (the last line must print 0).
Any `W: cannot mount` or `W: skipping mount` line means frostroot's
mount watch would stop the build, and WSL 1 support stops here.

**6. The released frostroot, unchanged.** The v0.15.1 Linux binary,
checked against its `SHA256SUMS`, with a recipe for 24.04 asking for
`git` and `jq`, in a recipe directory under `/mnt/c`:

```
$ cd /mnt/c/path/spike-recipe && time frostroot build --plain
$ ls -ln dist frostroot.lock
```

*Decides:* whether the build half needs any code beyond messages; how
long a build takes on `lxfs`; who owns outputs written as root to a
Windows drive. A failure names the step the plan has to fix.

**7. Interrupt and kill.** Start the same build, press Ctrl-C during the
installs; start it again and, from a second shell, `kill -9` mmdebstrap.
After each:

```
$ grep frostroot /proc/mounts; ls dist; ls /var/tmp/frostroot-0
```

*Decides:* whether an interrupted build unmounts and leaves no lock and
no tarball (it must); what a killed one leaves mounted, and so whether
Preflight has to look for mounts under the work root.

**8. Offline, twice.**

```
$ frostroot vendor --plain
$ frostroot build --offline --plain && sha256sum dist/*.tar.gz
$ frostroot build --offline --plain && sha256sum dist/*.tar.gz
```

*Decides:* whether the byte-identity promise can be made on WSL 1. Two
different sums: list the differing entries (`tar --xattrs -tvf` of each,
compared), and the promise is not made until they are explained.

**9. Against Linux.** Copy the recipe, the lock and `vendor/` to the
test VM and rebuild offline there with the same binary:

```
vm$ frostroot build --offline --plain && sha256sum dist/*.tar.gz
vm$ tar --xattrs --xattrs-include='*' --numeric-owner -tvf dist/*.tar.gz > linux.txt
$   tar --xattrs --xattrs-include='*' --numeric-owner -tvf dist/*.tar.gz > wsl1.txt
```

*Decides:* what an image built on WSL 1 lacks or gains: owners,
hardlinks, capabilities, the eleven tmpfiles entries, `hwdb.bin`, the
systemd catalog. An identical sum is the best case and would let the
README say so. A difference in content, as against debconf's backups,
means the image is incomplete and WSL 1 builds do not ship until it is
gone.

**10. The tripwire's baseline.** In the VM and on WSL 1, the build of
experiment 5 again with one more hook:

```
--customize-hook='chroot "$1" dpkg --verify > verify.txt; echo $? >> verify.txt'
```

*Decides:* whether `dpkg --verify` is silent on a healthy image, or
what it always reports, and so whether it can fail a build on a missing
file without failing every build.

**11. The other releases, and Python.** Experiment 6 for 22.04 and
26.04, and for 20.04 if the owner wants it; then a 24.04 recipe with
`[python]` naming `requests`. *Decides:* the list of releases promised
on WSL 1, and whether the Python step, which runs the image's own
interpreter and pip in the chroot over the network, works there.

**12. Import what was built, and log in.** With the launcher of
experiment 1, register the tarball of experiment 6 as a second name and
set its default user as experiment 2 did; then the README's first-login
checks without the systemd one:

```
PS> wsl -d SPIKE2 -e sh -c "whoami; sudo -n id -u; getent hosts archive.ubuntu.com; locale; date; getcap -r /usr 2>/dev/null"
PS> wslconfig /u SPIKE2
```

*Decides:* that a frostroot image is usable on WSL 1; whether
capabilities and owners survive Windows' unpacking; whether the recipe's
timezone holds on a WSL that never heard of `useWindowsTimezone`; what
`sudo` says with no `/etc/hostname`.

**13. The refusals, cheaply.** `apt-get install -y bubblewrap`, then
`bwrap --unshare-all --ro-bind / / true` and `bwrap --ro-bind / / true`
as root. *Decides:* that the disk refusal is right, and whether mkosi's
sandbox, which unshares less, has any chance; if the second succeeds,
Fedora on WSL 1 becomes a question for a later version rather than a
closed one.

**14. Clean up.** `wslconfig /u` every spike distribution, remove the
install directories and the scratch toolchain, and write what happened
into this document as "Spike results", with the versions and the date,
before the plan is revised.

## Spike results (2026-11-16, Windows 10 build 17763, WSL 1, the Ubuntu 20.04.6 distribution, as root)

The owner approved the spike with these limits: use the `Ubuntu`
distribution that starts, unregister and change nothing, install nothing
on the Windows side, run no Go inside WSL, and keep the work under the
WSL file system. Everything below ran as root through `wsl -d Ubuntu -u
root`, with umask 022 set by hand, in `/var/tmp/frostroot-spike`, which
is left in place: logs, status files and the tarballs, 158 MB.

That is not the host the design asks for. The build host here is 20.04
with **mmdebstrap 0.4.1**, the only one its archive offers, where
frostroot is verified against 1.4.3 and 1.5.7. So each result says
whether it is the kernel's or could be the old mmdebstrap's.

**Experiment 0, say so first.** Not done as written: the owner chose to
run without telling whoever runs the endpoint software.

**The security software did nothing that was seen.** Over about
twenty-five minutes apt downloaded and dpkg unpacked several hundred
packages across four chroots, and tar packed three images. `/bin/tar` was checked
after every step and is still there, no process was killed, and no
notice appeared. That is one afternoon, not a clearance.

**Before experiment 4: `tar` restored.** dpkg unpacks with `tar`, so
with `/bin/tar` gone apt could not reinstall it. `apt-get download tar`,
then Python's `tarfile` reading `dpkg-deb --fsys-tarfile` wrote
`/bin/tar` back, and `apt-get install --reinstall tar` after it put the
package right; `dpkg --verify tar` is clean.

**Experiment 4, the build tools: passed.** `apt-get install -y
mmdebstrap ubuntu-keyring` installed mmdebstrap 0.4.1-6 with uidmap,
fakechroot and arch-test. The keyring holds the 2018 archive key,
`F6EC B376 2474 EDA9 D21B 7022 8719 20D1 991B C93C`, which signs every
release frostroot builds.

**Experiment 5, mmdebstrap by hand in root mode, the essential set of
24.04: passed, in 70 s.**

```
mmdebstrap --verbose --mode=root --variant=essential \
  --customize-hook='cat "$1/proc/mounts" > noble-mounts.txt' \
  --customize-hook='ls -l "$1/dev" > noble-dev.txt' \
  --customize-hook='chroot "$1" sh -c "uname -r; sleep 1; echo chroot-sleep-ok"' \
  noble noble-root.tar.gz http://archive.ubuntu.com/ubuntu
```

- No `W:` and no `E:` line. Exit 0, a 25 MB tarball.
- **`mknod` works on `lxfs`**: `/dev` in the chroot held real character
  devices (`null` 1,3, `zero` 1,5, `full`, `random`, `urandom`, `tty`,
  `console`, `ptmx`), made by mmdebstrap, not bind mounts.
- **proc and sysfs were mounted inside the chroot**, with devpts and a
  tmpfs on `/dev/shm`, by `/proc/mounts` read from inside it.
- **Nothing stayed mounted**: `grep -c spike /proc/mounts` printed 0
  after this run and after every later one, the failed ones included.
- **Ubuntu 24.04's userland runs on this kernel**: its dpkg 1.22.6
  configured the essential set, and in the chroot `uname -r` printed
  `4.4.0-17763-Microsoft` and glibc 2.39's `sleep 1` returned.
- The tarball, listed and not unpacked: 3,645 entries, owners real
  numbers (0/0, 0/42 for shadow's setgid helpers), `su` and `newgrp`
  setuid, both hardlinks (`perl5.38.2`, `uncompress`) as hardlinks, the
  eight device entries.

All of that is the kernel's and would hold under mmdebstrap 1.4.3,
whose root mode asks the same things of it.

**Experiment 5, unshare mode as root: not answered.** mmdebstrap 0.4.1
refuses it outright ("cannot use unshare mode when executing as root",
then it looks for `/proc/sys/kernel/unprivileged_userns_clone`). 1.4.3
allows it; that needs a newer build host.

**Experiment 5 with what frostroot installs, 24.04: FAILED, and this is
the finding that matters.** The same command with `--variant=important`,
the three pockets, and
`--include=systemd,systemd-sysv,dbus,sudo,locales,tzdata,passwd,ca-certificates,git,jq`
exited 1 after 155 s:

```
/usr/lib/tmpfiles.d/systemd-network.conf:10: Failed to resolve user 'systemd-network': No such process
/usr/lib/tmpfiles.d/systemd.conf:22: Failed to resolve group 'systemd-journal': No such process
Failed to take /etc/passwd lock: Invalid argument
dpkg: error processing package systemd (--configure):
 installed systemd package post-installation script subprocess returned error exit status 1
```

systemd 255's post-installation script creates its users with
`systemd-sysusers`, which locks `/etc/passwd` in a way this kernel
answers with EINVAL. Which call that is was not traced. It runs inside
the chroot, as the image's own binary on the host's kernel, so no
mmdebstrap version and no build mode changes it: fakechroot and unshare
mode would run the same binary on the same kernel. **A 24.04 image with
systemd, which every frostroot image has, cannot be built on this
WSL 1.** 26.04 ships systemd 259 and was not tried; nothing suggests it
would differ.

**The same for 22.04: failed, for a reason that may not be the
kernel's.** Exit 1 after 289 s:

```
FATAL ERROR:
Can't rename /lib: Permission denied at /usr/lib/usrmerge/convert-usrmerge line 255.
dpkg: error processing package usrmerge (--configure):
```

mmdebstrap 0.4.1 does not lay out a merged `/usr` before unpacking, so
jammy's `usrmerge` package tried to convert a live root, and `lxfs`
refused to rename a directory in use. mmdebstrap 1.4.3 makes the
symlinks first, and `usrmerge` then has nothing to convert. So 22.04 is
**undecided**: systemd 249 was configured without complaint before the
failure, which is the hopeful half.

**The same for 20.04: passed, in 301 s.** Exit 0, 243 packages
configured, a 126 MB tarball, no `W:`, no `E:`, no "Failed". In the
chroot: `/var/lib/private` at 0700, `/var/log/journal` setgid and owned
by gid 102, and systemd's catalog database written (140,108 bytes), so
the maintainer scripts that need `/proc` and `/sys` ran. Not compared
with a Linux build of the same thing, so "complete" is not claimed.
`getcap -r /usr` in the chroot printed nothing and `ping` in the
tarball is 0755 with neither a capability nor a setuid bit; whether
that is 20.04's own choice in a chroot or a capability lost on `lxfs`
was not found out.

**Experiment 10, the tripwire's baseline: not silent.** `dpkg --verify`
in the 20.04 chroot, from a customize hook, printed one line and exited
0:

```
??5??????   /sbin/start-stop-daemon
```

mmdebstrap puts its own `start-stop-daemon` in place while it installs
and restores the real one afterwards, so a hook sees the stand-in. A
tripwire would have to know that line, or run on the finished tarball.

**Experiment 6, the released frostroot under root: NOT run.** The
v0.15.1 binary was downloaded and matches its `SHA256SUMS`
(`6ff2622b…dd76d`), and is in the work directory. The session's
permission system refused to run it as root, and that was not worked
around. It would not have settled much: 0.4.1 predates the hooks
frostroot's command line relies on, and the 24.04 result above is
independent of frostroot.

**Experiments 1, 2 and 12, the import half: NOT run.** They need a
launcher program, and there is no compiler the owner approved on the
Windows side.

**Experiments 3, 7, 8, 9, 11 and 13: NOT run.** 3, 8, 9 and 11 need
frostroot running on a 24.04 host. 7 and 13 were not reached.

**Found along the way**

- **umask 000.** A process started by `wsl -e` on this build has umask
  000 (the default became 022 in build 18945), and frostroot never sets
  one: it is written to survive a strict umask, not an absent one.
  Whatever a maintainer script creates without naming a mode would be
  world-writable in the image. A build on WSL 1 must set 022 itself, or
  refuse to start without it.
- **The default distribution does not start**, so `scripts/wsl.sh`
  needs `FROSTROOT_WSL_DISTRO=Ubuntu` on this machine, as AGENTS.md
  says such a machine would.

### What the spike changes

- **The build half as proposed does not stand.** "An Ubuntu 24.04 build
  host building 24.04 images as root" fails at systemd, on the kernel.
  What can be said for a WSL 1 build today: the mechanism works (root
  mode, chroot, `mknod`, mounts, clean unmount, real ownership), 20.04
  builds by hand, 22.04 is undecided, 24.04 does not build and 26.04
  almost certainly does not.
- **20.04 and perhaps 22.04 are what is left to offer**, and 20.04 is
  the release whose README note says "prefer 22.04, 24.04 or 26.04".
  Whether a WSL 1 build that makes only the oldest images is worth its
  code and its hand-run verification is the owner's question, and this
  document's recommendation is that it is not: the sibling's Windows
  builder, or a build on any Linux machine or VM with the result
  carried over, serves these machines better.
- **The import half is untouched by any of this and is now the larger
  part of the value**: an image built elsewhere still cannot be
  registered on build 17763 without it. Whether a 24.04 image *runs*
  there after import is still open, and experiment 5 is encouraging:
  its userland ran in a chroot on the same kernel. What will not work
  inside such a distribution is whatever upgrades systemd: `apt
  upgrade` of the systemd package would hit the same failure.
- **Before the 22.04 question can be closed** it needs mmdebstrap 1.4.3
  on WSL 1, which needs a 22.04 or 24.04 distribution registered, which
  needs the import half. The order of the plan follows from that.

## The decision (the owner, 2026-11-16)

Asked whether to drop the WSL 1 build half and keep only the import
half, the owner answered: "Import half only".

- frostroot does not build inside WSL 1. `build` there is refused before
  it starts, for every family and target and as root too, and says to
  build on Linux or WSL 2 and bring the tarball over. Today root on
  WSL 1 passes Preflight and fails minutes later in systemd's script.
- `frostroot import`, in a published Windows binary, is what these
  machines get. It is also what the Windows builder needs.
- Of the questions below, 2, 4, 5 and 6 were about the build half and
  fall away. 3 (one binary or a separate launcher), 7 (the distribution
  that does not start) and 8 (the version, and which of the two plans
  goes first) stay open.
- Experiments 1, 2 and 12 are still to run, and decide the shape of
  `import`; the plan puts them first.

## Open questions for the owner

1. *Answered 2026-11-16: yes, run it.* Still open: **is the build half
   wanted at all** now that it can offer 20.04 and perhaps 22.04 only,
   or does WSL 1 support become the import half alone?
2. **Is `sudo frostroot build` acceptable** as the only way to build on
   WSL 1?
3. **One Windows binary with `import`, or a separate small launcher?**
   This document proposes the first.
4. **Which Ubuntu releases must work** on WSL 1: all four, or the ones
   the spike finds working?
5. **Fedora and disks refused on WSL 1**: agreed, or is either wanted
   enough to spike?
6. **If two offline builds on WSL 1 are not the same bytes**, does WSL 1
   support ship without that promise, or not at all?
7. **Is the Ubuntu 24.04 that does not start on this machine** safe to
   unregister, or is it someone's?
8. **The version number**, and whether this or the Windows builder goes
   first.

## What follows approval

The spike, experiment by experiment, with its results written into this
document; then the plan in
[`../plans/2026-11-16-frostroot-wsl1.md`](../plans/2026-11-16-frostroot-wsl1.md),
revised by what the spike found.
