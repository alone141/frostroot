# frostroot on a Windows without `wsl --import`

Date: 2026-11-16
Status: **the owner decided on 2026-11-16: import only** ("Import half
only"; and, asked again, "Yes, import only"). This began as "frostroot on
WSL 1", with two halves: building inside WSL 1, and importing on a
Windows whose `wsl.exe` has no `--import`. The spike, approved and partly
run the same day, found that Ubuntu 24.04's systemd does not configure on
the WSL 1 kernel, and the owner dropped the build half. Nothing is
implemented. The experiments that decide the shape of `import` ran the
same day, with the owner's go-ahead: it works on build 17763 for an
ordinary user, and both a 24.04 and a 26.04 image start there, once two
ACL attributes are taken off one directory. See "Import experiments:
results", which also says which of the assumptions below turned out
wrong.
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md).
Beside it: the Windows builder that needs no WSL, pull request #173;
"What `import` shares with the Windows builder" is the seam.

## Why

A lab PC on Windows 10 Enterprise LTSC 2019 (version 1809, build 17763)
has WSL 1 and nothing newer: WSL 2 needs version 1903 and a hypervisor,
LTSC 2019 has no Store, and it is supported until January 2029. Machines
like it are where a frozen image is wanted, and a frostroot image cannot
get onto one. The `wsl.exe` of 1809 has `-d`, `-e` and `-u` and nothing
else; `wsl --import`, which is the only way in the README teaches,
arrived in build 18305 (version 1903).

So the tarball exists, built on a Linux machine, in a VM or, later, by
the Windows builder, and the last step is missing. Windows has had the
call that does it since version 1709, `WslRegisterDistribution`, which
is what the Store's launchers use. frostroot ships no caller.

## Goal

- An image built elsewhere, on a Linux host or VM, or by the Windows
  builder of pull request #173, is registered and started on a Windows
  whose `wsl.exe` has no `--import`: `frostroot import NAME INSTALL-DIR
  TARBALL`, a command of a Windows `frostroot.exe` published with each
  release.
- It needs no administrator, leaves the recipe's user as the default
  user, and a failed import leaves nothing registered.
- `build` inside WSL 1 keeps being refused, before anything starts and
  as root too, and says where to build instead.
- Nothing changes for a build on Linux or WSL 2: the same command lines,
  the same bytes, every lock still loading and rebuilding.

## Non-goals

- **Building inside WSL 1**, in any mode, for any family, release or
  target. It was the other half of this document and the spike is the
  reason it is gone: with the packages every frostroot image holds, a
  24.04 build fails in systemd 255's post-installation script (`Failed
  to take /etc/passwd lock: Invalid argument`), which is the image's own
  binary on the WSL 1 kernel and so beyond any build mode; 22.04 failed
  too, perhaps for the old mmdebstrap's sake; only 20.04 built. A build
  that makes the oldest images, verified by hand on one machine every
  release, is not worth its code. "Spike results" has the record, and
  "Building on WSL 1: considered and dropped" what was proposed.
- **systemd in the imported distribution.** WSL 1 starts its own init
  and never systemd, whatever `/etc/wsl.conf` says.
- **A promise about what runs inside a new release on an old kernel.**
  Whether a 24.04 or 26.04 image starts on WSL 1, and what breaks in it,
  is measured in the experiments that remain and written into the
  README; it is not something frostroot can fix.
- **Signing the Windows binary.**
- **Replacing `wsl --import`** where there is one.

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

## What is promised, and what is not

| | | Why |
|---|---|---|
| `frostroot import` on Windows 10 from version 1709 on | registers the tarball, sets the default user, needs no administrator | `WslRegisterDistribution` and `WslConfigureDistribution` are in `wslapi.dll` on build 17763 (verified). That the call works for an ordinary user from an unsigned program is assumed until experiment A. |
| A failed import | leaves nothing registered and no install directory | `import` unregisters what it registered. |
| `frostroot import` on a Windows that has `wsl --import` | works too, not the documented way | Assumed; the function is still exported there. |
| The imported image is the bytes of the tarball | yes, but for two ACL attributes on `/var/log/journal`, and as far as Windows keeps them | `import` registers a copy without the pax records `SCHILY.xattr.system.posix_acl_*`, which WSL 1 refuses (the owner, 2026-11-17); Windows unpacks it. Owners, modes, hardlinks and the file capability survived in experiment C. |
| A 24.04 or 26.04 image starts on WSL 1 | not promised until experiment C | The spike saw 24.04's dpkg, bash and `sleep` run in a chroot on this kernel, which is encouraging and is not a login. |
| systemd, and services that need it, on WSL 1 | no | WSL 1. |
| Upgrading packages inside the distribution on WSL 1 | not promised; `apt upgrade` of systemd is expected to fail | It runs the same post-installation script that failed in the spike. Experiment D. |
| `init`, `edit`, `validate`, `capture`, `vendor` inside WSL 1 | work, as yourself | None needs a namespace or root. The `tui` scenario passes there (recorded 2026-11-11); the rest is assumed. |
| `build` inside WSL 1 | refused, exit 1, for everyone | The decision. |

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
   A): creates the install directory, copies itself into it, and runs
   the copy, which does the rest. The parent removes the copy
   afterwards. If the base path follows something else, this step is
   whatever experiment A says.
3. Writes a copy of the tarball into a temporary `.tar.gz` beside the
   install directory, entry by entry, without the pax records named
   `SCHILY.xattr.system.posix_acl_*`, which WSL 1's unpacking refuses
   (experiment A; the owner's decision of 2026-11-17, below). Every
   other header, every other pax record and every byte of content
   passes through as mmdebstrap or mkosi wrote it; nothing is unpacked.
   Then calls `WslRegisterDistribution(NAME, COPY)` and removes the
   copy, however the call ends. Windows unpacks; frostroot's code makes
   no tree from an archive and no archive from a tree, which is what
   the rule that Go never tars or walks a root file system protects.
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
need Linux until pull request #173 changes that. The cost is size: the
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
Experiment A settles it with the real thing.

**Shipping and attesting.** `release.yml` gains a second build step,
`GOOS=windows GOARCH=amd64`, with the same flags. `SHA256SUMS` lists both
files, the attestation's `subject-path` names both, and `gh release
create` uploads both. The Linux runner cannot run the `.exe`, so "Check
the binary" stays the Linux file's; the Windows binary's proof is
`scripts/check.sh`'s Windows build, a `GOOS=windows go vet`, the test
binaries cross-compiled and run on Windows, and the import scenario on
such a Windows before the tag, by the owner. The
binary is unsigned. Whether a locked-down Windows runs an unsigned
executable from a download is not frostroot's to decide; experiment A
finds out for this machine.

**On a Windows that has `wsl --import`**, the README keeps teaching
`wsl --import`. `frostroot import` works there too (assumed; the function
is still in `wslapi.dll`), and registers with whatever version is that
machine's default.

## Building on WSL 1: considered and dropped

Kept short, as the record of what the spike tested.

The proposal was mmdebstrap's root mode under `sudo`, for Ubuntu WSL
tarballs only, on an Ubuntu 24.04 build host. Root on WSL 1 has every
capability and can make a mount namespace, which is all root mode asks
before it mounts, and it is the one mode in which ownership, hardlinks
and device entries come from the same code path as on Linux. fakechroot
was rejected because it mounts nothing and needs the host's libc to be
the image's; proot because mmdebstrap has no such mode; Fedora and disks
because bubblewrap needs namespaces root cannot make there.

The spike bore the mechanism out (`mknod`, proc and sysfs in the chroot,
a clean unmount, real owners) and then failed where no mechanism helps,
in a maintainer script of the image. See "Spike results".

### What `build` does on WSL 1 now

It refuses, and the refusal changes in two ways.

- **It refuses root too.** Today `Mmdebstrap.Preflight` passes for uid 0
  on WSL 1 (the test case "WSL 1 as root" says so), and so do mkosi's
  and the disk tools'. The build then runs for minutes and fails in
  systemd's script, or in bubblewrap. A new sentinel error, checked
  before anything else in all three Preflights, stops it at once with
  exit 1.
- **It says what works.** The message for a kernel without user
  namespaces stays for every other such host. On WSL 1 it stops
  advising only `wsl --set-version DISTRIBUTION 2`, which these machines
  cannot do, and says: build on Linux or in WSL 2, or with the Windows
  builder once there is one, and bring the tarball here with `frostroot
  import`. It names `import` only from the release that has it.

### The umask on this Windows

A process started by `wsl -e` on build 17763 has umask 000; the default
became 022 in build 18945. It was found because a build would have
inherited it. With no build on WSL 1 it matters in three smaller places:

- **`import` does not depend on it.** What it runs inside the new
  distribution, through `WslLaunch`, only reads: `cat /etc/wsl.conf` and
  `id -u`. It creates no file there.
- **The people who use the imported distribution do.** A login shell on
  such a Windows starts with umask 000 unless the image sets one, and
  files a student creates are then world-writable. Ubuntu's images set
  none in `/etc/profile`; they leave it to PAM, which WSL does not run.
  Experiment C records what a login shell in a frostroot image reports.
  If it is 000 the README says so. Making the image set it is a change
  to what every image holds, on every Windows, and is not proposed here.
- **`scripts/wsl.sh` runs everything through `wsl -e`**, so a scenario
  or a `vendor` run on such a machine writes its files with whatever
  modes it asked for and no mask. The scenario this plan adds sets
  `umask 022` first. `scripts/wsl-exec.sh` doing so for every command is
  a small change of its own, noted in the plan.

## The rules in AGENTS.md this touches

**What reaches the image**

- *Only mmdebstrap or mkosi writes the tarball; Go never tars, walks or
  deletes a root filesystem.* Kept in what it protects, and the reason
  Windows does the unpacking. Its wording changes with the
  implementation, not before (the owner, 2026-11-17): Go never makes an
  archive from a tree or a tree from an archive. `import` copies the
  tarball entry by entry and drops only the pax records named
  `SCHILY.xattr.system.posix_acl_*`; it decides no owner, mode, link or
  capability, and a test holds every other header and record to the
  bytes it had. Writing the registry keys and unpacking by hand was
  rejected for this.
- *Every value that reaches a shell is validated and quoted.* `import`
  passes the name and the paths to Windows calls as arguments, never
  through a command line it assembles. The two commands it runs inside
  the distribution are fixed text plus one value, the user name read
  out of the image's `wsl.conf`; that name is hostile input, and is
  held to the recipe's user-name pattern before it goes anywhere.
- Nothing else under this heading is touched: no build changes.

**The lock**

- Untouched. No field is added, and `import` reads no lock.

**Trust**

- `import` verifies nothing about the tarball, as `wsl --import` does
  not. The release's checksum and attestation cover the Windows binary
  as they cover the Linux one.
- *Everything read out of a lock is hostile input* has a cousin here:
  everything read out of a distribution is. `import` reads two things
  back, a `wsl.conf` and a uid, with a size bound and a timeout on each.

**Files and the network**

- `import` makes no network request.
- It writes one thing itself, the install directory, and, if experiment
  A says the file system lands beside the calling executable, a copy of
  itself there, removed afterwards. It refuses an install directory
  that exists and is not empty, so it can remove what it made and
  nothing else.

**The command line**

- *Only `internal/cli` prints or exits.* `import` is a command there.
  Exit 0, 1 for what the user can fix (a name taken, a file that is not
  a tarball, a directory in use, not Windows) and 2 for a registration
  Windows refused.
- *A new error the user can fix goes into the `errors.Is` list in
  `reportBuildFailure`.* The WSL 1 refusal does.
- *`build` never prompts*, and neither does `import`. Results on stdout,
  progress on stderr.
- *The plain interface asks the same questions in the same order.* No
  recipe field is added, so no question is.

**Package boundaries**

- A new package, `internal/wslapi`, joins those that import no other
  frostroot package and hold no policy. It is the only place
  `wslapi.dll` is named.
- `builder` learns whether the host is WSL 1 from the file it already
  reads, `/proc/sys/kernel/osrelease`, through the `ReadFile` it already
  takes.

**Tests**

- Unit tests stay without root, without mmdebstrap and without Windows:
  the host kind is a table of kernel release strings, and `import` runs
  against a fake of the interface `cli` declares.
- What only Windows can show, the real calls, is run by hand on a
  Windows host and recorded, as `wsl-boot` is today. "What `import`
  shares with the Windows builder" says how, with no Go on that machine.

## What `import` shares with the Windows builder

With pull request #173, the builder that needs no WSL:

- **One Windows binary.** The module already has to compile for
  Windows, and `scripts/check.sh` proves it on every push;
  `frostroot-windows-amd64.exe` is that build, published. `import` is a
  command of it, and so is whatever the builder adds. One version, one
  `frostroot version` on both sides of an import. Whichever plan lands
  first publishes the binary; the other adds to it.
- **The release changes.** One more build step in `release.yml`
  (`GOOS=windows GOARCH=amd64`, the Linux step's flags), both files in
  `SHA256SUMS`, both in the attestation's `subject-path`, both uploaded.
  Written once, in whichever pull request comes first.
- **Signing.** Neither plan signs. An unsigned executable from a
  download meets SmartScreen on a home machine and possibly a refusal on
  a managed one, and the answer is the same for both: say so in the
  README, give `Get-FileHash` and `gh attestation verify`, and leave a
  certificate as the owner's later decision. It should be one decision,
  not two.
- **Testing Windows code with no Go on the Windows machine.** Both
  plans have code only Windows can run, and the one Windows machine at
  hand has no toolchain and is not to get one. Go cross-compiles test
  binaries: in the Linux VM, `GOOS=windows go test -c -o wslapi.test.exe
  ./internal/wslapi/` makes an executable that runs the package's tests
  on Windows with nothing installed. `scripts/check.sh` vets and builds
  for Windows; the test binaries are carried over and run by hand, and
  the pull request records the output. Some forty tests elsewhere fail
  on Windows at `master` for reasons of the platform, so each plan
  names the packages whose tests it runs there.
- **The end of the builder's road.** A tarball built on Windows by #173
  on such a machine still has to be registered, and `import` is how.
  The builder's success message and `build`'s hint should name the same
  command in the same words.
- **The host-kind check**, and the README's "Deliberately not yet" line,
  where "a native Windows binary" stops being true the day either plan
  ships and should say what is still missing.

Neither design depends on the other.

## Risks

- **The call may not be ours to make.** The same functions called from
  PowerShell returned access denied for every name. If that is COM
  set-up, a program of our own fixes it; if it is the endpoint software
  or a policy, nothing does, and experiment A is where that shows.
- **Where the file system lands** is inferred from two registry entries
  and a third-party README. If it is not beside the executable, step 2
  of `import` is different.
- **The image may not start, or may start and misbehave.** A 2018
  emulation of Linux 4.4 is running a 2024 or 2026 userland. The spike
  found one program that fails outright there (`systemd-sysusers`); a
  login does not run it, an upgrade does. What else fails is not known
  and cannot all be found: experiment D looks at the obvious.
- **Windows' unpacking may lose what mmdebstrap kept**: file
  capabilities, hardlinks, owners. Experiment C compares.
- **The security software.** It removed `/bin/tar` from a distribution
  on this machine once. During the spike it did nothing that was seen.
  An import has Windows itself writing some tens of thousands of Linux
  executables into the install directory, which is the same thing from
  its side.
- **An unsigned executable** on a managed Windows.
- **Nobody else has this machine.** CI is off and could not run it
  anyway; this stays verified by hand, on one build of Windows, per
  release.

## The spike as first listed (the record)

This is the list as written before any experiment ran and before the
decision, kept because "Spike results" refers to it by number. Most of
it tested the build half and will not run now; what remains is in "The
experiments that remain", lettered.

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

## The experiments that remained (run; results below)

Lettered, so as not to be confused with the numbered list above. This
is the list as written before they ran; A to E ran on 2026-11-16, and
F, the clean-up, on 2026-11-17. Each needed the owner's go-ahead, and A needs two things not yet
approved: a Go toolchain unpacked in a scratch directory on the Windows
side, or a throwaway program cross-compiled in the Linux VM and carried
over, which installs nothing; and one new distribution, named
`frostroot-spike`, created by the experiment and removed only when the
owner says. Nothing existing is unregistered or changed.

`PS>` runs in PowerShell; `vm$` in the project's Linux test VM.

**A. Register a tarball.** In the VM, cross-compile a program of some
sixty lines that sets up COM security and calls
`WslRegisterDistribution`, and build a 24.04 image with the released
frostroot from a recipe asking for `git` and `jq`:

```
vm$ GOOS=windows go build -o spike-import.exe ./spike-import
vm$ frostroot build --plain
```

Carry both to Windows. Put the program in an empty directory `D`, start
it from another directory:

```
PS> cd $env:TEMP; & D\spike-import.exe frostroot-spike C:\path\spike-ubuntu-24.04-amd64.tar.gz
PS> wslconfig /l
PS> reg query HKCU\Software\Microsoft\Windows\CurrentVersion\Lxss /s
```

*Decides:* whether an ordinary user's unsigned program may register a
distribution; what COM set-up the call needs; whether `BasePath` is the
executable's directory, the current one or something else; how long
Windows takes to unpack an image. A refusal that no set-up cures ends
the plan: these machines then get a paragraph in the README.

**B. Configure, and undo.** With the same program: `WslLaunch` running
`cat /etc/wsl.conf` and `id -u student` with a pipe for stdout;
`WslConfigureDistribution(frostroot-spike, 1000, 7)`; and, on a second
throwaway name registered from the same tarball,
`WslUnregisterDistribution`, then a look at what is left of its
directory and its registry key. *Decides:* steps 4 and 5 of `import`,
and whether a failed import can be undone completely.

**C. Log in, and compare with the tarball.**

```
PS> wsl -d frostroot-spike -e sh -c "whoami; umask; sudo -n id -u; getent hosts archive.ubuntu.com; locale; date; cat /etc/os-release"
PS> wsl -d frostroot-spike -u root -e sh -c "getcap -r /usr 2>/dev/null; find / -xdev -links +1 -type f | wc -l; find / -xdev ! -user root | wc -l"
vm$ tar --xattrs --xattrs-include='*' --numeric-owner -tvf spike-ubuntu-24.04-amd64.tar.gz > tarball.txt
```

*Decides:* that a frostroot image is usable on WSL 1: the default user,
passwordless sudo and what it says with no `/etc/hostname`, DNS, the
locale, whether the recipe's timezone holds on a WSL that never heard of
`useWindowsTimezone`; the umask a login gets; and whether capabilities,
hardlinks and owners are what `tarball.txt` says they should be. An
image that does not start for 24.04 is tried for 22.04 before the plan
stops.

**D. What breaks inside.** In the imported distribution:

```
PS> wsl -d frostroot-spike -u root -e sh -c "apt-get update && apt-get install -y hello && hello"
PS> wsl -d frostroot-spike -u root -e sh -c "apt-get install -y --reinstall systemd; echo exit $?"
PS> wsl -d frostroot-spike -e sh -c "git --version; jq --version; python3 -c 'import ssl, sqlite3; print(1)'; systemctl is-system-running"
```

*Decides:* what the README has to warn of. Installing an ordinary
package is expected to work; reinstalling systemd is expected to fail
as it did in the spike, and if it does, what state apt is left in and
how to hold the package (`apt-mark hold systemd`) is part of the
finding.

**E. The same for 26.04**, A to D with a 26.04 tarball, as a second
name, if 24.04 passed. *Decides:* which releases the README names as
importable on WSL 1.

**F. Clean up, when the owner says.** `wslconfig /u` the spike's own
distributions, remove their directories, and write the results into
"Spike results", dated, before the plan is revised.

## Import experiments: results (2026-11-16, Windows 10 build 17763, WSL 1, an ordinary user)

The owner approved experiments A to E ("Yes, run them"): the program
cross-compiled in the test VM, nothing installed on Windows, new
distributions named `frostroot-spike` only. Everything is under
`%USERPROFILE%\frostroot-spike`: `program\` (the test program),
`images\` (the tarballs), and `install\`, `install-2\`, `install-3\`
(one per distribution). **Three distributions are left registered, all
made by these experiments: `frostroot-spike`, `frostroot-spike-2` and
`frostroot-spike-3`**, 2.8 GB together. None is the default; `Ubuntu`
and `Ubuntu-24.04` were not touched. Removing them waited for the
owner's word.

**Removed on 2026-11-17, on the owner's word** (experiment F):
`wslconfig.exe /u` returned 0 for each of the three, the registry lists
only `Ubuntu-24.04` and `Ubuntu` again with the default unchanged, and
each install directory was left holding its empty `temp\` and the copied
program, as the API's unregister had left it. The whole
`%USERPROFILE%\frostroot-spike` was then deleted. What ran is in this
section and in the program's source beside this file; the images can be
built again in the test VM.

**The program** is
[`2026-11-16-frostroot-wsl1-spike-import.go.txt`](2026-11-16-frostroot-wsl1-spike-import.go.txt),
some 250 lines of Go with the standard library's `syscall`, built in the
VM with Go 1.27.3 (`GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build
-trimpath`), SHA-256 `64039129…436f58`, unsigned. **The images** were
built in the VM by the released v0.15.1 binary from a recipe asking for
`git` and `jq`, user `student`, timezone `Europe/Istanbul`: 24.04 in
716 s (143 MB, `0bdb7b65…934345f`) and 26.04 in 732 s (150 MB,
`9ac9d6f1…caafa7cf`).

**The security software did nothing that was seen**: the unsigned
program ran from a user directory every time, nothing vanished, and no
notice appeared.

### A. Registering

- **An ordinary user's unsigned program may call the API.** No
  administrator, no prompt.
- **No COM set-up is needed.** `WslIsDistributionRegistered` and
  `WslGetDistributionConfiguration` answered correctly for `Ubuntu`
  (registered, version 1, uid 1000, flags 7) with nothing initialized,
  with `CoInitializeEx` alone, and with `CoInitializeSecurity` for
  impersonation. Registering behaved the same with none and with the
  last. So the access-denied answers from PowerShell were PowerShell's.
- **The file system lands beside the calling executable**, whatever the
  working directory: started from `%TEMP%`, the program in `install\`
  got `BasePath = …\frostroot-spike\install`, with `rootfs\` and an
  empty `temp\` there.
- **A frostroot tarball as built is refused.**
  `WslRegisterDistribution` returned `0x80004005` (E_FAIL) after 19 to
  24 s for the 24.04 image, twice, and for the 26.04 image. Each
  time nothing was registered and the directory held only the empty
  `temp\`: Windows cleans up after itself.
- **The cause is two extended attributes on one directory.** The image
  carries three pax `SCHILY.xattr` records: `security.capability` on
  `/usr/bin/ping`, and `system.posix_acl_access` and
  `system.posix_acl_default` on `/var/log/journal`, which
  systemd-tmpfiles sets. Copies of the 24.04 tarball rewritten entry by
  entry in the VM (Python's `tarfile`, nothing unpacked):

  | Copy | Result |
  |---|---|
  | without the eight device entries | `0x80004005` |
  | without device entries and without all three attributes | registered, 12.7 s |
  | with device entries, without all three attributes | registered, 13.1 s |
  | with device entries and the capability, without the two ACL attributes | registered, 12.9 s |

  So device entries are fine, the capability is fine, and the ACLs are
  what WSL 1's unpacking cannot take. The 26.04 image without them
  registered in 15.0 s.
- Registering takes 13 to 15 s for a 140 MB tarball.

### B. Configuring, and undoing

- `WslLaunch` with an inheritable pipe returns a command's output:
  `cat /etc/wsl.conf` printed the file frostroot wrote, and `id -u
  student` printed `1000`. About 0.3 s each.
- **Before configuring, the default user is root**: `[user] default` in
  `wsl.conf` does nothing on this Windows, as read. After
  `WslConfigureDistribution(name, 1000, 7)`,
  `WslGetDistributionConfiguration` reports uid 1000 and `wsl -d
  frostroot-spike` is `student`.
- `WslUnregisterDistribution` on `frostroot-spike-2` returned 0 after
  7 s, the name was no longer registered, and `rootfs\` was gone. It
  leaves the directory, the empty `temp\` and whatever else was there.
  (The name was then used again for the last row of the table above,
  which is why it is registered now.)
- Registering never changed `DefaultDistribution`.

### C. Logging in, and what Windows kept

In the 24.04 image, as the default user: `whoami` is `student`;
`sudo -n id -u` prints 0, with no complaint about the host name, which
WSL writes into `/etc/hostname` itself; `getent hosts
archive.ubuntu.com` resolves, through a `resolv.conf` WSL generates;
`locale` is `en_US.UTF-8` with no warning; `git`, `jq` and Python's
`ssl` and `sqlite3` run; `sleep 1` returns; `systemctl
is-system-running` says `offline`, and PID 1 is WSL's `init`.

- **The umask is 0000**, in `wsl -e` and in a login shell alike.
- **Hardlinks survive**: six files with more than one link, the three
  pairs the tarball has; `perl` and `perl5.38.2` share an inode.
- **Owners and modes survive**: `/etc/shadow` 0640 root:shadow, the
  sudoers drop-in 0440, `/home/student` 0750 student:student,
  `/var/log/journal` setgid root:systemd-journal, `/etc/credstore` and
  `/root/.ssh` 0700, `sudo` setuid.
- **The capability survives**: `getcap /usr/bin/ping` prints
  `cap_net_raw=ep` in the copy that kept it.
- **The device entries are not used**: `/dev` is WSL's own tmpfs.
- **The timezone was not put to the test**: the recipe's zone and the
  Windows zone are both +03 here. `/etc/localtime` is still the
  recipe's symlink.

### D. What breaks inside

- `apt-get update` and `apt-get install -y hello` work, and `hello`
  runs.
- **`apt-get install --reinstall systemd` fails as the spike's build
  did**: `Failed to take /etc/passwd lock: Invalid argument`, and dpkg
  leaves systemd half configured (`iF`). From then on every apt run
  ends with "Errors were encountered while processing: systemd" and
  dpkg's error, though it still installs what was asked: `sl` installed
  after it. So the first security update of systemd breaks apt's exit
  status in such a distribution for good. `frostroot-spike` and
  `frostroot-spike-3` are in that state now.
- The reinstall also wrote a machine id ("Initializing machine ID from
  random generator").

### E. 26.04

The same throughout: refused as built, registered without the ACLs in
15 s, `student` with `sudo-rs` 0.2.13 answering `sudo -n`, the Rust
coreutils, git 2.53, Python with OpenSSL 3.5.5, the capability on
`ping`, umask 0000, `hello` installed, and systemd 259.9 failing to
reconfigure the same way.

### What these results change

- **`import` works, and is simpler than proposed**: no COM set-up, and
  the copy-itself step is confirmed as needed, since the install
  directory is wherever the executable is.
- **`import` cannot hand over the tarball untouched.** Something has to
  take the two ACL attributes off `/var/log/journal` first, and that
  meets the rule that Go never tars a root filesystem. Three ways:
  1. `import` copies the tarball entry by entry into a temporary
     `.tar.gz` beside the install directory, dropping only pax records
     named `SCHILY.xattr.system.posix_acl_*`, and registers that. No
     file is unpacked and no owner, mode, link or capability is
     decided by Go: every header passes through as mmdebstrap wrote
     it. **This is what the owner chose on 2026-11-17** ("Yes, copy
     and strip"), with the rule reworded, when `import` is written, to
     say what it protects: Go never *makes* an archive from a tree or
     a tree from an archive.
  2. `build` gains a way to leave the ACLs out. It changes the bytes
     of images, or adds a recipe field, for the sake of one old
     Windows.
  3. The README tells people to filter the tarball themselves on the
     build host. Nobody will.
- **What is lost with the ACLs** is read access to the journal for the
  `adm` group and the like, in a distribution that runs no journal.
- **The README has three things to say** about such a Windows: no
  systemd; hold the systemd package (`apt-mark hold systemd`) before
  the first upgrade, or apt's exit status is broken from then on; and
  the umask is 0000 unless the user sets one. Whether `import` should
  do the hold itself is the owner's question.
- **Both current releases are importable.** Nothing here was tried
  with 22.04 or 20.04, or with Fedora.

## The decision (the owner, 2026-11-16)

Asked whether to drop the WSL 1 build half and keep only the import
half, the owner answered "Import half only"; asked again by the session
that coordinates this work, "Drop building on WSL 1 and keep only the
import command in that plan?", "Yes, import only".

- frostroot does not build inside WSL 1.
- `frostroot import`, in a published Windows binary, is what these
  machines get.
- This document was revised to that on the same day. "Spike results" is
  the record and was left as written.

## Open questions for the owner

1. ~~How `import` gets rid of the two ACL attributes that WSL 1
   refuses.~~ Answered on 2026-11-17: asked "Should import strip the
   two ACL attributes by copying the tarball entry by entry?", the
   owner answered "Yes, copy and strip". So it is the first way under
   "What these results change": a copy written entry by entry into a
   temporary `.tar.gz` beside the install directory, without the pax
   records named `SCHILY.xattr.system.posix_acl_*`, and that copy
   registered. The rule in AGENTS.md is reworded when `import` is
   written, not before.
2. ~~Should `import` hold the systemd package, or is a README warning
   enough?~~ Answered on 2026-11-17 by the owner, for this and the
   questions below that this document had a proposal for: "go with the
   recommended ones for #172, and do not ask me until they are done".
   So the README warns, and `import` holds nothing.
3. ~~May the three spike distributions be removed?~~ Answered on
   2026-11-17: the owner said to remove them if nothing needed them,
   and they are gone, with `%USERPROFILE%\frostroot-spike`.
4. ~~One Windows binary with `import`, or a separate small launcher?~~
   Answered on 2026-11-17 by the owner, with the same words: one
   binary, as this document proposed, shared with pull request #173.
5. ~~Which goes first, this or the Windows builder?~~ This document
   had no proposal, and the owner did not choose. **The session that
   coordinates this work chose a default on 2026-11-17, not the
   owner:** this goes first and carries the release changes. The owner
   may still turn it round.
6. ~~Signing: unsigned for now, for both plans?~~ Answered on
   2026-11-17 by the owner, with the same words: unsigned, for both.
7. ~~Is the Ubuntu 24.04 registration that does not start safe to
   unregister?~~ No proposal here and no answer from the owner. **The
   coordinating session's default, not the owner's:** it stays
   registered; nothing needs it gone.
8. ~~The version number.~~ No proposal here and no answer from the
   owner. **The coordinating session's default, not the owner's:** the
   version this would be released as is 0.16.0, and none of the plan's
   pull requests changes `builder.Version` or pushes a tag. Releasing
   stays the owner's decision.

Nothing is open. Three of the eight were settled by a default the owner
has not confirmed: 5, 7 and 8.

## What follows

The plan in
[`../plans/2026-11-16-frostroot-wsl1.md`](../plans/2026-11-16-frostroot-wsl1.md),
which the experiments' results and these answers have revised. The
owner asked for it to be carried through.

## What the implementation found (2026-11-17)

The plan's pull requests 2 to 6 were written and run the same day:
#174 (the refusal), #175 (`import`), #176 (the release workflow), and the
scenario and documentation after them. On Windows 10 1809 the real
binary agreed with the experiments, and added three things this
document did not have:

- **Windows 10 1809 starts no distribution from inside another.** From
  a shell in one distribution, `wsl.exe -d OTHER` answers "Operation not
  supported", and a command `WslLaunch` starts in a distribution
  registered from there exits 4294967295. So `import` started from a
  WSL shell registered the image and could not read its `wsl.conf`; the
  first version took that for a missing file and left root the default.
  It now fails and unregisters, and says to start it from PowerShell or
  cmd. Verified. The same holds for the scenario: `import-boot` runs in
  Git Bash, not in WSL, which the plan's task 14 did not foresee.
- **The timezone holds.** Experiment C could not tell, the recipe's
  zone and the Windows zone being the same. An image built for UTC shows
  UTC on this Windows at +03. Verified, for 24.04 and 26.04.
- **`SHA256SUMS` with two files breaks `sha256sum -c`** for someone who
  downloaded one of them, so the README's check now passes
  `--ignore-missing`. Verified in the test VM.

The copy `import` registers was compared with both real images, block
by block: it differs by the one pax header that held the two ACL
records, 1024 bytes, and by nothing else. A failed import was seen to
unregister and remove its directory. What was not run is in
`docs/VERIFICATION.md`, in the pull request that brings the
documentation.
