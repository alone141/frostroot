# frostroot Implementation Plan: building on Windows without WSL

`frostroot.exe build` on a Windows machine with no WSL: it starts a
pinned Linux guest under QEMU, runs the existing Linux build in it, and
brings the lock and the image back. The spec, with the comparison of the
ways, the rules it touches and the decisions it asks of the owner, is
[`2026-11-16-frostroot-windows-builder.md`](../specs/2026-11-16-frostroot-windows-builder.md).

**This plan is a draft. The spec's spike has NOT been run and the owner
has answered none of its decisions.** Pull request 0 is the spike; what it
finds may reorder or remove what follows, and the plan is rewritten then,
with "(As built: …)" notes later, as the earlier plans have them.

The work goes in eight pull requests, one after the other, each opened
once the one before has merged. Every task ends with `scripts/check.sh`
clean and its own commit as `alone141` following `CONTRIBUTING.md`; a fix
inside a task comes with a test that fails without it, proven with
`scripts/mutate.sh`. CI is off at the owner's choice; while it is, each
pull request lists what ran by hand, on Linux and on Windows, and says
once that CI could not run.

**Goal:** an Ubuntu WSL recipe builds online and offline with
`frostroot.exe` on Windows 10 1809 without admin rights; two offline
builds of one lock are the same bytes; and no Linux recipe, lock or image
changes by a byte.

## What this assumes the owner will answer

The recommended answer to each of the spec's decisions: QEMU with WHPX
first and TCG behind it; a QEMU the user already has; frostroot's own
disk image as the guest; an explicit `guest fetch`; an unsigned
`frostroot.exe`; Windows-only code kept thin and checked by hand. A
different answer to decision 3 (Canonical's cloud image) replaces pull
request 4 and leaves the rest; a different answer to decision 1 (WSL 2
first) replaces pull request 3.

## What is not done

- `capture` on Windows, the full-screen build screen on Windows, a QEMU
  that frostroot ships or fetches, a WSL 2 or container backend, the
  import into WSL, Windows on arm64.
- Fedora and disk recipes on Windows, unless spike 5 says they hold; then
  they join pull request 6's scenarios, and otherwise `build` refuses
  them on Windows with exit 1.

## Pull request 0: the spike, and the decisions

No code.

1. **Run the spec's twelve experiments**, in its order, on the Windows
   machine with its QEMU 11.1.0 and a guest built in the test VM from a
   draft recipe. Spikes 1 and 4 first: either can end the design.
2. **Write the results into the spec**, under "Spike results" with the
   date, the machine and what was not measured, as the Fedora and
   bare-metal specs do; turn each **assumed** it settles into
   **verified**, or strike what it disproved.
3. **The owner's answers** to the eight decisions, recorded in the spec.
4. **This plan, rewritten** to what the spike and the answers leave.

## Pull request 1: Windows, for the commands that need no Linux

`build` still refuses. No behavior changes on Linux.

5. **The failing baseline.** The unit tests that fail on Windows at
   master in `capture`, `cli`, `index` and `pool` (about 40 on
   2026-11-10): each is either a real fault on Windows, fixed with the
   test as its proof (a rename over an open file in `pool` or `index`
   is one a user would meet in `vendor`), or a test of something Windows
   lacks, skipped with the reason in the skip. `internal/builder` and
   `internal/export` tests that call `syscall.Umask` move behind a build
   tag so the packages' other tests compile.
6. **The recipe-directory hold on Windows.** `recipedir_windows.go` with
   `LockFileEx`, so that `recipeDirLocking` is true there; `vendor` holds
   the directory as on Linux. Test: a second hold is refused while the
   first is open, and taken after it closes.
7. **`capture` and `build` say what is true.** `capture` on Windows is
   refused with exit 1 and the reason. `build`'s message loses "run
   frostroot inside WSL" only in pull request 5; here it gains nothing.
8. **`scripts/check.sh` vets and builds for Windows** as it does today,
   and gains `--only windows-test`, which runs `go test ./...` where
   `go env GOOS` is windows and says so elsewhere. The README says how it
   was run.
9. **Proof.** `scripts/check.sh` on Linux; the Windows unit tests green
   on the Windows machine, with the count before and after in the pull
   request; `tui` and `tui` with `E2E_DISTRO=fedora`.

## Pull request 2: files across, and the hidden command

Linux only, and useful to nobody yet: the part both ends share.

10. **`internal/transfer`.** A set of files as a stream: for each, a
    name, a size, the bytes, a SHA-256; then an end mark. The receiver is
    given the names it will accept and a cap on each size and on the
    total, writes each file to a temporary file in its directory and
    renames it, and refuses an absolute name, `..`, a backslash, a name
    it was not told to expect, a size over its cap and a checksum that
    does not match. It imports no other frostroot package but `export`.
    Tests: a round trip through `io.Pipe`; each refusal; a cancelled
    context between two files leaves no temporary file. A `Fuzz` target
    for the receiver, since the guest's stream is hostile input;
    `scripts/check.sh --only fuzz` finds it by its name.
11. **`frostroot guest receive DIR` and `frostroot guest send`**, hidden
    from the usage text, Linux only. `receive` reads a set from standard
    input into DIR; `send` writes the lock and the output of a finished
    build in DIR, or after a failed one its log files. Exit codes 0, 1
    and 2 as everywhere. Tests in `internal/cli` with a temporary
    directory on both sides.
12. **The list of what a build reads.** One function from a recipe, its
    directory and the flags to the files that go in: the recipe, the
    lock if there is one, each key and certificate file the recipe names,
    and offline the pool's files the lock names. It lives beside the
    validation that already confines those paths. Tests: every fixture
    recipe; a file the recipe does not name is not listed; a pool file
    the lock does not name is not listed.
13. **Proof.** `scripts/check.sh`; `scripts/check.sh --only fuzz`.

## Pull request 3: QEMU, and a session in the guest

Nothing calls it yet.

14. **`internal/vm`: finding QEMU.** `FROSTROOT_QEMU`, then `PATH`, then
    `%ProgramFiles%\qemu`; the version parsed from `--version` and
    checked against the measured range; `qemu-img` and the firmware file
    beside it. Sentinel errors `ErrNoQEMU` and `ErrQEMUVersion`, which a
    user can fix. The environment and the file system are injected, so
    the tests run on Linux. The package imports no other frostroot
    package.
15. **The command line.** One function from a specification (image,
    overlay, firmware, credentials file, loopback port, serial log,
    memory, processors) to QEMU's arguments, with `-accel whpx -accel
    tcg`. Tests: the rendered arguments for paths with spaces, quotes and
    non-ASCII letters; nothing of a credential's value among them.
16. **The overlay and the credentials.** `qemu-img create` with the
    pinned image as backing file; an ed25519 key pair for the client and
    one for the host, made per build with `crypto/ed25519`; the SMBIOS
    file with `ssh.authorized_keys.root`, the host key through
    `tmpfiles.extra` and the password credential. All of it in one
    directory that is removed on every path. Tests: the file's content
    for a known key; removal after a failure at each step.
17. **Starting and ending QEMU.** A process group of its own on every
    system; on Windows a job object that ends it with frostroot.exe
    (`vm_windows.go`, as small as it can be); a forwarded port chosen
    free, and chosen again when QEMU says it could not bind. Ending is
    asked through the SSH session first and then by ending the process.
18. **`internal/remote`: the session.** `golang.org/x/crypto/ssh`,
    imported here and nowhere else: connect to the loopback port until
    the boot timeout, accept the pinned host key only, run one command
    with its output streamed and its exit status returned, send SIGINT.
    Tests against the library's own server on loopback: a wrong host key
    is refused; an exit status of 2 and of 130 come back as they are; a
    cancelled context sends the interrupt and then closes; every read
    has a deadline.
19. **go.mod, and the boundaries in words.** AGENTS.md's "Package
    boundaries" gains the SSH library's line and `vm` among the packages
    that import no other. CONTRIBUTING.md's note on dependencies gains
    the reason, as it has `internal/deb`'s.
20. **Proof.** `scripts/check.sh`; `scripts/check.sh --only vuln`, since
    a dependency was added; on Windows by hand, a ten-line program that
    boots the spike's guest through `vm` and `remote` and runs `uname
    -a`, with Ctrl-C and Task Manager tried on it.

## Pull request 4: the guest image

21. **`guest/frostroot.toml` and `guest/frostroot.lock`.** The disk
    recipe of Ubuntu 24.04 the spike settled, and its lock, committed.
    AGENTS.md's "Don't commit … a lock produced by a scenario" gains the
    exception by name. A test loads both and checks that the lock
    describes the recipe.
22. **The pin.** The asset's URL, the compressed size and SHA-256, and
    the raw image's size and SHA-256, as constants in one file of
    `internal/remote`, with the guest lock's own SHA-256 beside them so
    that a test fails when the lock changes and the pin does not.
23. **`scripts/guest-image.sh`.** On Linux: `vendor`, `build --offline`
    in `guest/`, `gzip -n -9`, and a comparison with the pin. It is what
    the release runs and what anyone runs to check the release. It builds
    in a copy under `/var/tmp`, so that `guest/` gains no `vendor/` or
    `dist/`.
24. **`frostroot guest fetch`**, with `--from FILE`, `--ca-bundle` and
    `--insecure`. The download is bounded by the pinned size, has a
    timeout, honours the context, goes through `pki.Transport`, is
    written to a temporary file in `%LOCALAPPDATA%\frostroot\guest\` (on
    Linux, for the tests, under `XDG_CACHE_HOME`), is decompressed with
    both checksums checked, and is renamed last. A file already there
    with the pinned checksum is left alone. Tests with `httptest`: a
    short body, a long body, a wrong checksum, an interrupted download
    and a server that stalls each leave nothing; `--from` with a good
    and a bad file.
25. **Usage text and README** for `guest fetch`, together.
26. **Proof.** `scripts/check.sh`; `scripts/guest-image.sh` twice in the
    test VM, one SHA-256, equal to the pin; `disk-boot` on the guest
    image where it can boot; `guest fetch --from` on Windows with the
    file the VM built.

## Pull request 5: `build` on Windows

27. **The builder's placement, where both can reach it.** The order
    "stage the image, rename the lock, rename the image, undo on
    failure" moves out of `Builder.Build` into a function `internal/export`
    or the builder exports, unchanged in behavior. Its existing tests
    move with it and pass unchanged, on Linux and on Windows.
28. **The launcher.** In `internal/remote`, behind an interface with one
    method per step the fake needs: check the host (QEMU, the guest
    image, the Linux binary and its checksum, free disk); start the
    guest; put the binary in and check it there; send the files of task
    12; run `frostroot build --plain` with the flags rebuilt from their
    parsed values and the listed environment; relay the lines; bring the
    outputs back and check them; parse the lock as hostile input; place
    them with task 27; on failure bring the logs back; end the guest and
    remove the overlay and the keys on every path.
29. **`internal/cli`.** On Windows `build` calls the launcher where it
    calls the builder on Linux, prints through the same plain progress,
    and returns the guest's exit code. The new sentinel errors join
    `reportBuildFailure`'s `errors.Is` list. `--keep-work` keeps the
    overlay and says where. A Fedora or disk recipe is refused with exit
    1 unless the spike cleared it. The WSL import hint prints the Windows
    path it already has.
30. **Tests, with a fake guest in the test file.** The exit code of each
    of 0, 1, 2 and 130 passed through; each launcher failure of the
    spec's table and its exit code; an interrupt before the guest
    answers, during the build and during the copy back, each leaving no
    lock, no image and no temporary file, and a previous image and lock
    as they were; a checksum that does not match what the guest named; a
    lock from the guest with an unknown field; a proxy variable with a
    password, which no output quotes; a loopback proxy rewritten and
    said so.
31. **Proof.** `scripts/check.sh`; on Windows by hand, an online build
    of the harness recipe with `--ca-bundle`, its lock and tarball
    compared with a Linux build of the same recipe at the same
    `SOURCE_DATE_EPOCH` as far as the spike said they compare; Ctrl-C
    once and twice.

## Pull request 6: offline, and the scenarios

32. **The pool goes in.** `build --offline` sends the files the lock
    names from `vendor\`, checked against the lock on the Windows side
    before anything starts, as the builder checks them again inside.
    Tests: a pool file changed on disk is refused before QEMU starts; a
    missing one names `frostroot vendor`.
33. **A scenario, `windows-build`**, in `scripts/e2e/`, run from Git Bash
    on Windows with `E2E_FROSTROOT` naming `frostroot.exe`: an online
    build; `vendor`; two offline builds, one SHA-256; a failed build
    (a package that does not exist) that leaves nothing and brings its
    log back; an interrupted build that leaves nothing. It checks its own
    results, as every scenario does, and `scripts/e2e/README.md` and
    AGENTS.md's list say when to run it: anything of the launcher, the
    guest image or `internal/transfer`.
34. **Windows against Linux.** `offline-identical` learns
    `E2E_COMPARE_SHA256`, a checksum another machine's offline build of
    the same lock and pool gave; `windows-build` prints the one to pass.
    Run both ways.
35. **Fedora and disks**, if the spike cleared them: `windows-build`
    with `E2E_DISTRO=fedora` and with `E2E_TARGET=disk`, and task 29's
    refusal lifted for what passed.
36. **Proof.** `windows-build` on the Windows machine; `offline-identical`,
    `no-build-leaks` and `failed-build` in the test VM, unchanged, since
    the builder was touched by task 27; `wsl-boot` on a machine that has
    WSL, with a tarball built on one that has not, which is the owner's
    check.

## Pull request 7: the release, and the words

37. **`release.yml`.** The Linux binary first; its SHA-256 into
    `frostroot.exe` with `-ldflags -X`; `frostroot.exe version` run under
    the runner's Wine or on a Windows job; the zip made with fixed
    timestamps; `scripts/guest-image.sh` reproducing the pinned guest
    when this release's pin is new; `SHA256SUMS` over every file; the
    attestation's subjects extended. The check that the tag matches
    `builder.Version` stays as it is, sed and all.
38. **`ci.yml`.** A Windows job running `go test ./...`, for when Actions
    is on. Repository settings are the owner's and are not touched.
39. **README.** "Install" gains Windows: the zip, `SHA256SUMS` checked
    with `certutil -hashfile`, where QEMU comes from and how to unpack it
    without admin rights, `guest fetch`, how long a build takes with and
    without the Hypervisor Platform. "Scope" moves the native Windows
    binary out of "Deliberately not yet" and says what Windows does not
    build. "Layout" gains `internal/transfer`, `internal/vm`,
    `internal/remote` and `guest/`. The Documentation table gains the
    spec and this plan. `build`'s usage text and its message on Windows
    change with it.
40. **AGENTS.md.** "What frostroot is" stops saying only that the module
    must compile for Windows. "On a Windows checkout" says which commands
    now run without the wrappers. The rules gain: the launcher never
    reads inside an image; only the listed variables cross to the guest;
    `build` never downloads the guest; QEMU is ended only after the
    guest's build has exited. The scenario list gains `windows-build`.
41. **The record and the version.** `docs/VERIFICATION.md` gets what ran,
    after it ran. `builder.Version` becomes what the owner named. Pushing
    the tag stays the owner's, after their own build on a Windows machine
    that has never had WSL.

## What a later version could add

Each is a door the design leaves open, and none is promised: a WSL 2
backend behind the launcher's interface; a QEMU the project publishes,
pinned by checksum; the full-screen build screen on Windows, drawn there
from progress events the guest sends; the Windows certificate store's
roots offered to the guest's build as `--ca-bundle` is; a smaller kernel
for a disk recipe meant for a VM, which would shrink the guest.
