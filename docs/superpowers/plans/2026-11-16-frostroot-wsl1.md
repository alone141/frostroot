# frostroot Implementation Plan: importing on a Windows without `wsl --import`

Registering a frostroot tarball as a distribution on a Windows whose
`wsl.exe` has no `--import`, which is every Windows 10 before version
1903 and every machine that has only WSL 1. The spec, with the probes and
the spike this plan rests on, is
[`2026-11-16-frostroot-wsl1.md`](../specs/2026-11-16-frostroot-wsl1.md).

**The owner decided on 2026-11-16: the import half only.** This plan
first held a build half as well, building inside WSL 1 as root. The spike
found that Ubuntu 24.04's systemd does not configure on the WSL 1 kernel,
which left 20.04 and perhaps 22.04 to build there, and the owner dropped
it. The image is built where frostroot builds today, on Linux or WSL 2,
and carried to the old machine.

The work goes in pull requests into `master`, one after the other. Every
task ends with `scripts/check.sh` clean and its own commit as `alone141`
following `CONTRIBUTING.md`; a fix inside a task comes with a test that
fails without it, proven with `scripts/mutate.sh`. CI is off at the
owner's choice, so each pull request lists what ran by hand and says once
that CI could not run. The version is the owner's to number.

**Goal:** `frostroot import NAME INSTALL-DIR TARBALL`, from a Windows
binary published with each release, registers a tarball on Windows 10
from version 1709 on, without an administrator, with the recipe's user as
the default; a failed import leaves nothing registered; `build` on WSL 1
keeps refusing and now says where to build instead; and no build changes
by a byte.

## The decisions this records

- **No build on WSL 1** (the owner, 2026-11-16). `build` there is
  refused as today, for every family and target, as root too.
- **One binary for Windows, with `import`**, built from the same module
  and published, summed and attested beside the Linux one. Proposed in
  the spec; the owner's to confirm.
- **Windows unpacks the tarball.** `import` hands it a copy made entry
  by entry without the pax records named
  `SCHILY.xattr.system.posix_acl_*`, which WSL 1 refuses (the owner,
  2026-11-17: "Yes, copy and strip"). Nothing is unpacked by frostroot,
  and every other header and record passes through byte for byte.
- **No recipe field and no lock field.** The form, the plain interface
  and every lock are untouched.
- **`wsl --import` stays what the README teaches** for a Windows that
  has it.

## What is not done

- Building inside WSL 1, in any mode, for any release.
- systemd in a distribution imported into WSL 1.
- Signing the Windows binary.
- A Windows builder that needs no WSL: pull request #173's. Pull
  requests 3 and 4 here are the part the two share, and whichever plan
  lands first carries them.

## What the experiments found (2026-11-16)

The spec's experiments A to E have run; its "Import experiments:
results" has them. For this plan:

- The API works for an ordinary user from an unsigned program, with no
  COM set-up. Task 6 loses that part.
- The file system lands beside the calling executable. Task 7's
  "place the executable" is: create the install directory, copy the
  binary into it, run the copy, remove it afterwards.
- A tarball as frostroot builds it is refused, for two POSIX ACL
  attributes on `/var/log/journal`; without them 24.04 and 26.04 both
  register in about 15 s and log in. Task 7 gains a step, the copy
  without them, which the owner chose on 2026-11-17; task 10 rewords
  the rule in AGENTS.md that it meets.
- Reinstalling systemd inside such a distribution fails and leaves apt
  exiting with an error from then on; the umask is 0000. Both go into
  the README in pull request 6.

## Pull request 1: the spec and this plan

1. **Spec and plan**, with the spike's results and the owner's decision.
   Pull request #172.

## Pull request 2: say the right thing on WSL 1

2. **The host kind.** One function in `internal/builder`, beside
   `checkUserNamespaces`, that reads `/proc/sys/kernel/osrelease` through
   the `ReadFile` the bootstrappers already take and answers whether the
   host is WSL 1. Tests: a table of release strings, and an unreadable
   file, which is not WSL 1.
3. **The refusal, for everyone.** Today root on WSL 1 passes every
   Preflight and fails later, in systemd's maintainer script after
   minutes for Ubuntu, in bubblewrap for Fedora and disks. A new
   sentinel, `ErrWSL1`, from all three Preflights before anything else:
   WSL 1 cannot build an image, build on Linux or WSL 2 and bring the
   tarball here. It joins the `errors.Is` list in `reportBuildFailure`,
   exit 1. The message for an ordinary user says the same and no longer
   suggests only converting to WSL 2, which these machines cannot do.
   Tests: the "WSL 1 as root" case of `TestMmdebstrapPreflight` and
   `TestMkosiPreflightRefusesAKernelWithoutUserNamespaces` want nil
   today and want the new error; the disk tools' Preflight the same; a
   `cli` test for the exit code and the first line on stderr.
   `scripts/mutate.sh --revert`, the result in the commit body.
4. **The words.** `build`'s usage text and the README's Install
   paragraph, together.
5. **Proof.** `scripts/check.sh` in the test VM, golangci-lint and
   shellcheck there too. No scenario: nothing an image holds changes.

## Pull request 3: `frostroot import`

6. **`internal/wslapi`.** `wslapi_windows.go`: the DLL loaded by its full
   path under the system directory with the standard library's
   `syscall`; no COM set-up, which the experiments found unnecessary; `Register`,
   `Unregister`, `IsRegistered`, `Configure` and `Launch` (a command,
   its stdout through a pipe, a timeout from the context).
   `wslapi_other.go`: each returns `ErrNotWindows`. It imports no
   frostroot package and decides nothing. Tests that run everywhere: the
   HRESULT-to-error mapping as a table. Tests behind a `windows`
   constraint call only `IsRegistered` on a name that cannot exist, so
   they register nothing.
7. **The command.** `import NAME INSTALL-DIR TARBALL` in `internal/cli`,
   against an interface `cli` declares and a fake in its test file. In
   order: validate the name (the recipe's image-name pattern, exported
   from `internal/recipe` as a function, not copied), the tarball
   (exists, regular, `.tar.gz`), the install directory (absent or
   empty), the name not registered; copy the binary into the install
   directory and continue from the copy, since Windows puts the file
   system beside the calling executable; write a copy of the tarball
   into a temporary `.tar.gz` beside the install directory without the
   pax records named `SCHILY.xattr.system.posix_acl_*` (the owner,
   2026-11-17). The copy goes block by block, not through a tar writer
   that would encode headers its own way: every 512-byte block passes
   through as read, except a pax extended header that holds such a
   record, which is written again with the same records in the same
   order less those, its size and checksum recomputed, and left out
   whole if nothing remains in it. Nothing is unpacked, and a tarball
   with no such record comes out equal to the original once
   decompressed. Register that copy and remove it, however the call
   ends; read the default user from the distribution's
   `/etc/wsl.conf`, hold it to the recipe's user-name pattern, ask for
   its uid, configure; on any failure after registering, unregister.
   Never prompts; exit 0, 1 or 2; on anything but Windows, exit 1 naming
   the Windows binary. Tests: every refusal; the order of calls;
   unregistering after a failed configure; the copy removed after a
   failed register; for the copy, a fixture tarball with a device
   entry, a hardlink, a long name, `security.capability` and both ACL
   records: decompressed, the copy equals the original byte for byte
   in every header block, every other pax record and every content
   block, the only difference being the two records gone and the size
   and checksum of the header that held them, with a pax header that
   held nothing else gone whole; a fixture with no ACL record comes out
   identical; a truncated or malformed tarball is refused, exit 1, with
   no copy left; and a fuzz target for the copy, which reads a file a
   user hands over. `scripts/mutate.sh --sed` on the record's name
   pattern, the result in the commit body. A hostile `wsl.conf` (a user
   name with a quote or a newline, a section that is not `[user]`, no
   file, which leaves root the default and says so); the usage text. A
   fuzz target for the `wsl.conf` reader, since it parses what an image
   hands over.
8. **`GOOS=windows` in the checks.** `scripts/check.sh` gains
   `GOOS=windows go vet ./...` beside the Windows build. The Windows
   machine has no Go and gets none: the tests of `internal/wslapi` and
   of `import` are cross-compiled in the VM (`GOOS=windows go test -c`),
   carried over and run by hand, and the pull request records their
   output. Only those packages, since some forty tests elsewhere fail
   on Windows at `master` for reasons of the platform. Pull request
   #173 tests its Windows code the same way; the commands go into
   AGENTS.md once, in pull request 6.
9. **The hint after a build.** The lines after "Import it on Windows"
   name both commands: `wsl --import`, and `frostroot import` for a
   Windows without it. Tests: the hint tests in `internal/cli` gain the
   line; nothing else in them moves.
10. **The README and the usage text, together; and the rule in
    AGENTS.md.** Commands table, Quick start's import lines, Install.
    The tarball rule under "What reaches the image" is reworded in this
    pull request, with the code that meets it and not before: Go never
    makes an archive from a tree or a tree from an archive; `import`
    copies a tarball block by block and drops only the pax records
    named `SCHILY.xattr.system.posix_acl_*`, and the test of task 7
    is named there as what holds it to that.
11. **Proof.** `scripts/check.sh` in the VM. On the Windows host: the
    binary imports a tarball built in the VM, `wsl -d NAME` logs in as
    the recipe's user, `wslconfig /u NAME` removes it; an import of a
    name already taken, and of a file that is not a tarball, each exit 1
    and leave nothing registered and no install directory.

## Pull request 4: publish the Windows binary

12. **`release.yml`.** A second build step with the Linux one's flags
    and `GOOS=windows GOARCH=amd64`, output
    `frostroot-windows-amd64.exe`; `SHA256SUMS` over both files; both in
    the attestation's `subject-path` and in `gh release create`. Nothing
    can run the workflow before a tag, and Actions is off: the pull
    request says what was checked instead, the two build commands run by
    hand in the VM and `sha256sum -c` on their output.
13. **Install, in the README.** The Windows binary, how to check it
    (`Get-FileHash` against `SHA256SUMS`, `gh attestation verify`), and
    that it is unsigned.

## Pull request 5: the first-login check on such a Windows

14. **A scenario for it.** `wsl-boot` builds and imports on one machine,
    which a WSL 1 machine cannot do. A second entry point takes a
    tarball built elsewhere, `E2E_TARBALL`, and the Windows binary,
    `E2E_FROSTROOT_WINDOWS`; imports with `frostroot import`; runs
    `wsl-boot`'s checks except systemd, and says in its verdict that it
    skipped it; unregisters with `wslconfig.exe /u`. It sets `umask 022`
    first: `wsl -e` on build 17763 starts processes with 000. shellcheck
    clean; `scripts/e2e/README.md` says so.
15. **The wrappers' umask.** `scripts/wsl-exec.sh` sets `umask 022`
    when it finds 000, so that nothing run through `scripts/wsl.sh` on
    such a Windows writes world-writable files into a checkout or a
    work directory. A commit of its own; shellcheck clean.
16. **Proof.** The scenario on the Windows 10 1809 host, for a 24.04 and
    a 26.04 tarball built in the VM.

## Pull request 6: the documentation, after the runs

17. **README.** Install and Quick start as above; Notes, a paragraph
    "On a Windows without `wsl --import`" (build elsewhere, import with
    the Windows binary, no systemd under WSL 1, what experiments C and D
    found: the umask a login gets, and upgrading systemd); "Deliberately not yet", where "a native Windows binary"
    becomes what is still missing, agreed with the sibling plan; the
    Documentation table.
18. **AGENTS.md.** The module's Windows binary and what it does (the
    tarball rule was reworded in task 10); the package boundary of
    `internal/wslapi`; the scenario list.
19. **`docs/VERIFICATION.md`.** One paragraph, written after the runs.
20. **The version**, if the owner decides this is a release.

## What each pull request needs run, in one place

| Pull request | `scripts/check.sh` (VM) | On Windows 10 1809 |
|---|---|---|
| 2 refusal | yes | nothing |
| 3 `import` | yes | the `wslapi` and `import` tests; an import, a login, an unregister; two refusals |
| 4 release | yes | `Get-FileHash` of a hand-built binary against its sums |
| 5 scenario | yes | the scenario, for 24.04 and 26.04 |
| 6 docs | yes | nothing |
