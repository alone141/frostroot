# frostroot Implementation Plan: WSL 1

Building an Ubuntu WSL tarball inside WSL 1, as root, and registering a
tarball as a distribution on a Windows whose `wsl.exe` has no `--import`.
The spec, with the probes this plan rests on and the spike it still
waits for, is
[`2026-11-16-frostroot-wsl1.md`](../specs/2026-11-16-frostroot-wsl1.md).

**This plan was written before the spike, and part of the spike has now
run** (the spec's "Spike results", 2026-11-16). It found that Ubuntu
24.04's systemd does not configure on the WSL 1 kernel, so the build
half below, pull requests 3 and 6 and the build parts of 7, has no
24.04 or 26.04 to offer and waits for the owner's decision on whether a
build of 20.04, and perhaps 22.04, is wanted at all. Pull request 2
still stands, with one change: its refusal must not promise that
`sudo frostroot build` works on WSL 1 until that decision. Pull
requests 4 and 5, the import half, are untouched by the finding and are
the part to do first: they are also what makes a newer build host on
WSL 1 possible, without which the 22.04 question cannot be closed. If
the build half goes ahead, it gains a task: set umask 022 for
mmdebstrap on WSL 1, where `wsl -e` starts processes with 000. The
tasks below are otherwise as first written.

The work goes in pull requests into `master`, one after the other. Every
task ends with `scripts/check.sh` clean and its own commit as `alone141`
following `CONTRIBUTING.md`; a fix inside a task comes with a test that
fails without it, proven with `scripts/mutate.sh`. CI is off at the
owner's choice, so each pull request lists what ran by hand and says once
that CI could not run. The version, and which release this rides, are the
owner's to decide.

**Goal:** on WSL 1, `sudo frostroot build` makes an Ubuntu WSL tarball,
online and offline; `frostroot import NAME INSTALL-DIR TARBALL`, from a
Windows binary published with each release, registers it on Windows 10
from version 1709 on with the recipe's user as the default; every other
case on WSL 1 is refused before it starts, with exit 1; and no build on
Linux or WSL 2 changes by a byte.

## The decisions this records

Proposed in the spec, and the owner's to confirm (its "Open questions"):

- **Root mode under `sudo`** is the way to build on WSL 1. fakechroot,
  proot and unpacking by hand are rejected, with reasons, in the spec.
- **Ubuntu WSL tarballs only.** A Fedora recipe and a disk recipe are
  refused on WSL 1, as root too.
- **The build host is an Ubuntu 24.04 distribution on WSL 1**, for
  mmdebstrap 1.4.3 and the apt it needs.
- **One binary for Windows, with `import`**, built from the same module
  and published, summed and attested beside the Linux one. Windows
  unpacks the tarball; frostroot's code never opens it.
- **No recipe field and no lock field.** So the form, the plain
  interface and every existing lock are untouched.
- **`wsl --import` stays what the README teaches** for a Windows that
  has it.

## What is not done

- Building without root on WSL 1, Fedora there, disks there, systemd in
  the imported distribution.
- A build host older than Ubuntu 24.04.
- A promise that a WSL 1 build is the bytes a Linux build is.
- Signing the Windows binary.
- The Windows builder that needs no WSL: the sibling plan's. This plan's
  pull requests 4 and 5 are the part the two share.

## Pull request 1: the spec and this plan

1. **Spec and plan.** `docs: spec and plan WSL 1 support`. Nothing else
   changes.

## The spike, between pull requests 1 and 3

2. **Run the spec's experiments 0 to 14**, each after the owner's
   go-ahead, on the machine at hand. Not a pull request of code: its
   product is a "Spike results" section in the spec, dated, with the
   versions, and this plan revised by it.
   `docs: record the WSL 1 spike`.

   What stops the plan: experiment 4 losing `tar` again with no
   exclusion to be had; experiment 5 printing a mount refusal in both
   modes; experiment 9 showing content missing from the image.

## Pull request 2: say the right thing on WSL 1

Needs no spike: these are refusals and messages, and each is true today.

3. **The host kind.** One function in `internal/builder`, beside
   `checkUserNamespaces`, that reads `/proc/sys/kernel/osrelease` through
   the `ReadFile` the bootstrappers already take and answers whether the
   host is WSL 1. `checkUserNamespaces` uses it rather than its own
   suffix test. Tests: a table of release strings, WSL 1's
   `4.4.0-17763-Microsoft`, WSL 2's, a stock kernel, an unreadable file
   (not WSL 1: a file that cannot be read proves nothing).
4. **Without root, the refusal names the way that works.** The WSL 1
   message of `ErrNoUserNamespaces` for an Ubuntu WSL recipe gains
   "build as root with sudo frostroot build" before the advice to
   convert to WSL 2. Tests: the "WSL 1" case of `TestMmdebstrapPreflight`
   wants both; `internal/cli`'s test of the exit code stays 1.
5. **Fedora and disks are refused on WSL 1, as root too.** A new
   sentinel, `ErrWSL1Unsupported`, from `Mkosi.Preflight` and the disk
   tools' `Preflight`, before the user-namespace check, saying what
   WSL 1 lacks for each and that an Ubuntu WSL recipe builds under
   `sudo`. It joins the `errors.Is` list in `reportBuildFailure`, so it
   exits 1. Tests: `TestMkosiPreflightRefusesAKernelWithoutUserNamespaces`
   today wants nil for root on WSL 1; it and the disk tools' Preflight
   test want the new error for root there, and nothing run. A `cli` test for the
   exit code and the first line on stderr. `scripts/mutate.sh --revert`
   on the commit, the result in its body.
6. **The words.** `build`'s usage text in `internal/cli/build.go` and the
   README's Install paragraph say the same thing together: under WSL 1,
   Ubuntu WSL images build as root; Fedora and disks need WSL 2 or
   Linux. Marked as unverified until pull request 7's checks have run,
   in the pull request's description and not in the README: the README
   says it only once it is true, so this task lands the usage text now
   and holds the README sentence for pull request 7 if the spike has
   not run by then.
7. **Proof.** `scripts/check.sh` in the test VM, golangci-lint and
   shellcheck there too. No scenario: nothing the image holds changes.

## Pull request 3: what root mode on WSL 1 needs

Shaped by experiments 5 to 11. If v0.15.1 already builds there, tasks 8
and 9 are empty and this pull request is task 10 alone, or nothing.

8. **Whatever mmdebstrap needs.** A flag, a skip, or `--mode=unshare`
   for root on WSL 1, if experiment 5 chose it: then `bootstrapMode`
   takes the host kind, and the table test of modes gains the WSL 1
   rows. Any new argument is tested in `commandLine`'s table, and no
   argument changes for another host: the existing rows stay as they
   are, which is the test that nothing moved for Linux.
9. **Mounts a killed build left**, if experiment 7 found any: Preflight
   in root mode reads `/proc/mounts` through `ReadFile` and refuses a
   work root with a mount under it, exit 1, naming the `umount` lines.
   Tests: a table of `/proc/mounts` contents with hostile paths
   (spaces, the `\040` escapes); the sentinel in `reportBuildFailure`.
10. **The tripwire**, if experiment 10 found `dpkg --verify` quiet on a
    healthy image: the last customize hook before the status download
    runs it in the image and fails on a missing file or a changed
    checksum that is not a configuration file's. Hook text through a
    real `sh` with hostile paths and through shellcheck;
    `TestHooksAndScriptNeverSwallowFailures` covers it; the hook order
    tests, online and offline, gain it. Whether every host gets it or
    WSL 1 alone is the spike's finding; if every host, `offline-identical`
    and `no-build-leaks` run in the VM to show no image changed.
11. **The recipe directory and the hint.** On WSL 1, `build` warns on
    stderr when the recipe directory is not under `/mnt`, since Windows
    cannot name a file on `lxfs`; and the lines after "Import it on
    Windows" name `frostroot import` with the tarball's Windows path
    from `wslpath -w`. Tests: `internal/cli`'s hint tests gain the WSL 1
    cases, with `WSLPath` and the host kind injected; the existing
    cases keep their text. (The hint names a command that pull request
    4 adds; this task merges after it, or moves into it.)
12. **Proof.** `scripts/check.sh` in the VM. On the WSL 1 host, as root,
    with the branch's binary: `offline-identical`, `no-build-leaks`,
    `failed-build`, `apt-trust`, `certificates` and `pip-trust`, each
    with `E2E_RELEASE` for every release the spike promised. What the
    harness needs to run as root there (it assumes user namespaces
    today) is part of this task. In the VM: `offline-identical` and
    `no-build-leaks`, to show Linux unchanged.

## Pull request 4: `frostroot import`

13. **`internal/wslapi`.** `wslapi_windows.go`: the DLL loaded by its
    full path under the system directory with the standard library's
    `syscall`; COM set up as experiment 1 found; `Register`,
    `Unregister`, `IsRegistered`, `Configure` and `Launch` (a command,
    its stdout captured through a pipe, a timeout from the context).
    `wslapi_other.go`: each returns `ErrNotWindows`. It imports no
    frostroot package and decides nothing. Tests that run everywhere:
    the HRESULT-to-error mapping (`ERROR_ALREADY_EXISTS`, access
    denied) as a table. Tests that need Windows are behind a
    `windows` build constraint and call only `IsRegistered` on a name
    that cannot exist, so that they register nothing.
14. **The command.** `import NAME INSTALL-DIR TARBALL` in
    `internal/cli`, against an interface `cli` declares and a fake in
    its test file. In order: validate the name (the recipe's image-name
    pattern, exported from `internal/recipe` as a function rather than
    copied), the tarball (exists, regular, `.tar.gz`), the install
    directory (absent or empty), the name not registered; place the
    executable as experiment 1 requires; register; read the default
    user from the distribution's `/etc/wsl.conf`, hold it to the
    recipe's user-name pattern, ask for its uid, configure; on any
    failure after registering, unregister. Never prompts; exit 0, 1 or
    2; on anything but Windows, exit 1 naming the Windows binary. Tests:
    every refusal; the order of calls; unregistering after a failed
    configure; a hostile `wsl.conf` (a user name with a quote, a
    newline, a section that is not `[user]`, no file at all, which
    leaves root the default and says so); the usage text. A fuzz target
    for the `wsl.conf` reader, since it parses what an image hands
    over, with its seeds run by the tests.
15. **`GOOS=windows` in the checks.** `scripts/check.sh` gains
    `GOOS=windows go vet ./...` beside the Windows build it already
    has. The Windows unit tests of `internal/wslapi` and of `import`
    run by hand on the Windows host with the scratch toolchain; the
    pull request says so and names the result. Against the known
    baseline: some forty tests elsewhere fail on Windows at `master`
    for reasons of the platform, so only these packages' are run.
16. **The README and the usage text, together.** Commands table,
    Quick start's import lines (both: `wsl --import` where there is
    one, `frostroot import` where there is not), Install.
17. **Proof.** `scripts/check.sh` in the VM. On the Windows host: the
    binary imports a tarball built in the VM, `wsl -d NAME` logs in as
    the recipe's user, `wslconfig /u NAME` removes it; an import of a
    name already taken, and of a file that is not a tarball, each exit
    1 and leave nothing registered and no install directory.

## Pull request 5: publish the Windows binary

18. **`release.yml`.** A second build step with the Linux one's flags
    and `GOOS=windows GOARCH=amd64`, output
    `frostroot-windows-amd64.exe`; `SHA256SUMS` over both files; both in
    the attestation's `subject-path` and in `gh release create`. The
    version check and the tests stay as they are. Nothing can run the
    workflow before a tag, and Actions is off: the pull request says
    what was checked instead, which is the two build commands run by
    hand in the VM and `sha256sum -c` on their output.
19. **Install, in the README.** The Windows binary, how to check it
    (`Get-FileHash` against `SHA256SUMS`, `gh attestation verify`), and
    that it is unsigned.

## Pull request 6: `wsl-boot` on WSL 1

Can start before the spike; cannot be proven before pull request 4.

20. **The scenario learns WSL 1.** `scripts/e2e/wsl-boot.sh` on a WSL 1
    host: builds as root; lists and unregisters with `wslconfig.exe`
    rather than `wsl.exe --list` and `--unregister`; imports with the
    Windows binary named by `E2E_FROSTROOT_WINDOWS` rather than
    `wsl.exe --import`; skips the systemd check, and says in its
    verdict that it did. Every other check stays: the user, sudo, DNS,
    locale, timezone, the Python environment, the certificate
    authority, apt still verifying TLS. On WSL 2 it runs as it does
    today. shellcheck clean. `scripts/e2e/README.md` says so.
21. **Proof.** The scenario on the WSL 1 host for each release the
    spike promised, and on a WSL 2 host unchanged if the owner has one;
    otherwise the pull request says WSL 2 was not re-run.

## Pull request 7: the documentation, after the runs

22. **README.** Install (WSL 1: what builds, as whom, on which build
    host; the recipe directory on a Windows drive); Notes, a paragraph
    "On WSL 1" (no systemd, slower, root, the security-software
    caution, what was measured against Linux); Scope's version line;
    "Deliberately not yet", where "a native Windows binary" becomes
    what is still missing, agreed with the sibling plan; Verification's
    paragraph on what cannot run in CI; the Documentation table.
23. **AGENTS.md.** "What frostroot is" (builds need Linux, or WSL 1 as
    root for Ubuntu WSL tarballs; the module compiles for Windows and
    its `import` works there); the tarball rule's new sentence; the
    package boundary of `internal/wslapi`; the scenario list ("anything
    of WSL 1 or `import`: `wsl-boot` on a WSL 1 host").
24. **`docs/VERIFICATION.md`.** One paragraph, written after the runs
    and not before: the Windows build, the kernel string, the build
    host, each scenario and its count of checks, the times, and what
    the security software did.
25. **The version.** `const Version` and the README's, if the owner
    decides this is a release.

## What each pull request needs run, in one place

| Pull request | `scripts/check.sh` (VM) | On the WSL 1 host | On Windows |
|---|---|---|---|
| 2 messages | yes | nothing | nothing |
| 3 root mode | yes, and `offline-identical`, `no-build-leaks` in the VM | `offline-identical`, `no-build-leaks`, `failed-build`, `apt-trust`, `certificates`, `pip-trust`, as root, per release | nothing |
| 4 `import` | yes | nothing | the `wslapi` and `import` tests; an import, a login, an unregister; two refusals |
| 5 release | yes | nothing | `Get-FileHash` of a hand-built binary against its sums |
| 6 `wsl-boot` | yes | `wsl-boot` per release | through the scenario |
| 7 docs | yes | `tui`, and `tui` with `E2E_DISTRO=fedora`, since both must still pass there | nothing |
