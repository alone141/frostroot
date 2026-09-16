# frostroot feasibility analysis

Date: 2026-09-15
Scope: is v1 as specified buildable, shippable, and worth building?
Inputs: `specs/2026-09-14-frostroot-design.md`, `plans/2026-09-14-frostroot.md`, `reviews/2026-09-14-frostroot-plan-review.md`
Status: analysis only. No code written, no spike run.

## Verdict

**Feasible.** Small, well-scoped, and the architecture is right. Nothing in the
design needs to change.

Two qualifications:

1. The plan as written ships a **broken artifact**. Not "rough edges" — the
   tarball would not boot. Four defects, all in the Go↔mmdebstrap seam, all
   with known fixes. The prior review found them; this analysis confirms three
   of the four by reading the plan's own code, and adds five findings it missed.
2. The riskiest assumption in the project — that an mmdebstrap rootfs with
   systemd boots correctly under `wsl --import` — is **still unverified**, and
   cannot be verified by anything in the automated test plan. It needs a human
   at a Windows machine.

Feasibility is therefore not in doubt. **Schedule confidence is**, and it stays
low until the spike runs.

## Method, and what this analysis does not cover

Read-only. I read the spec, the plan (2501 lines), and the prior review, and
probed a Linux host for toolchain facts.

I did **not** run the bootstrap spike — this was a planning session. So every
claim below about mmdebstrap's *runtime* behaviour rests on the prior review's
man-page reading, not on observation. Claims about the plan's *Go code* rest on
reading the plan's own listings and are independent.

The one thing no amount of reading settles: whether the resulting image boots
under WSL. See "The verification gap".

### Host facts (measured)

Measured in the cloud container this analysis ran in, **not on your WSL host**.
Useful as a second data point on the build environment, nothing more.

| Item | Value |
|---|---|
| OS | Ubuntu 24.04.4, kernel 6.18 |
| Go | 1.24.7 (plan targets 1.22) |
| mmdebstrap | not preinstalled; apt candidate 1.4.3-6, installs clean |
| mmdebstrap deps pulled | `uidmap`, `fakeroot`, `fakechroot`, `arch-test`, `libsubid4` |
| User namespaces | `unshare -Ur` works; subuid range present |
| dpkg | 1.22.6 (so `dpkg-query --root` available *here*) |
| archive.ubuntu.com | reachable, ~6 MB/s |
| Disk | 31 GB free |

Read: **the toolchain side of this project is a non-issue.** One `apt install`
and a Go toolchain. The prior review's host survey reached the same conclusion
about your WSL box.

## Feasibility by dimension

| Dimension | Verdict | Note |
|---|---|---|
| Technical — architecture | **Strong** | Package seams are right; the `Bootstrapper` interface is the correct cut |
| Technical — mmdebstrap seam | **Weak, fixable** | Where all four blockers live |
| Effort | **Small** | 1.5k–2.5k LOC; days, not weeks |
| Testability | **Split** | Unit layer excellent; the acceptance criterion is manual-only |
| Operational (20.04 story) | **Concerning** | See N4 — the headline use case is the weakest one |
| Legal / redistribution | **Fine, unexamined** | See N5 |

## Confirmed blockers

The prior review found four. My independent status on each:

| ID | Claim | My status |
|---|---|---|
| B1 | Pure-Go tar writer breaks every symlink | **Confirmed by reading.** Plan Task 4 calls `tar.FileInfoHeader(info, "")`. Go's documented contract: the second argument *is* the symlink target. Every symlink in the rootfs — merged-`/usr`, every library soname, all of `/etc/alternatives` — ships with an empty target. The image cannot start. The plan's own test only asserts proc/sys/dev skipping, so TDD goes green on a broken product. This is the single most dangerous item in the plan: it fails silently and passes its tests. |
| B2 | Host-side tar and cleanup are wrong in unshare mode | **Consistent with the code; runtime behaviour unverified.** The Go side is as described — `WriteTarball` walks from outside the namespace. Whether the uid remapping bites exactly as the review says is a man-page claim I did not test. |
| B3 | systemd is not in minbase | **Confirmed by reading.** Plan Task 10 passes `--variant=minbase`; `Essentials` in Task 5 is `{sudo, locales, tzdata, passwd}`. No systemd anywhere. Spec success criteria 4 and 5 are unreachable as planned. |
| B4 | No updates or security pockets | **Confirmed by reading.** `Bootstrapper.Run` takes a single `mirror string` and Task 10 passes one mirror argument. Release-day versions only. Note the spec's own lock example (`git 1:2.34.1-1ubuntu1.11`) is an updates-pocket version — **the spec's illustration is unattainable by the spec's own pipeline.** Good sign the two documents were never reconciled. |

All four are contained. None touches `recipe`, `distro`, `cli`, or the lock
format. That is why the verdict is still "feasible".

## Findings not in the prior review

### N1. The plan invented a Windows constraint the spec never asked for

Spec, Testing: "offline, no root, no mmdebstrap." Plan, Global Constraints:
"offline, without root, without mmdebstrap, **on Windows**."

The spec is emphatic that this is a Linux CLI and that Windows users run it
inside WSL. Nothing needs to compile or test on Windows. That self-inflicted
constraint buys nothing and costs real effort: `0440` file modes in `t.TempDir`
need Windows cleanup workarounds, symlink round-trip tests need skips, and
path handling gets fussier.

**Drop it.** Test on Linux. It is a Linux CLI.

### N2. B1 is worse than symlinks

Same root cause, three more losses the review did not enumerate:

- **Hardlinks.** `tar.FileInfoHeader` never emits `TypeLink`. Every hardlinked
  file is written as a full duplicate copy. Silent tarball bloat.
- **File capabilities.** `security.capability` xattrs are dropped, so `ping`
  and friends lose `cap_net_raw` and break in the image.
- **`Uname`/`Gname` from the host passwd.** The header gets *this machine's*
  name for uid 1000 — the exact opposite of the `--numeric-owner` the spec asks
  for.

Reinforces the recommendation: **never hand-roll rootfs tar.** Let mmdebstrap
write it (design A).

### N3. `WriteProvisionFiles` is dead weight with a test that lies

Task 5 ships `WriteProvisionFiles`, Task 6 calls it from the real build *after*
mmdebstrap has already run the equivalent hooks. So provisioning has two
implementations that can drift.

Worse: its test passes on an empty `t.TempDir`. It asserts that `/home/student`
exists as a directory — created by `os.MkdirAll` at uid 0, with no user, no
group, no ownership. In the real image `useradd -m` does this correctly. **The
test proves nothing about the shipped image and would stay green if the hook
path broke entirely.**

The spec intended this as a *test-only* helper. The plan promoted it to
production. Demote it.

### N4. The 20.04 story is the headline feature and the weakest one

The spec's stated justification: "20.04 is off standard support; that is the
pinning story." It is also, by 2026, the release where **you cannot get security
fixes at all** without Ubuntu Pro/ESM credentials. old-releases carries focal's
pockets frozen at EOL. So the flagship use case produces a golden image with a
permanent, unpatchable CVE backlog, handed to a classroom.

This is not a blocker — freezing an old distro is the *point*, and for an
airgapped teaching lab it may be entirely acceptable. But it should be a
**deliberate, documented decision**, not a surprise. Concretely: have `build`
print a warning for 20.04, and say so in the README.

Secondary: old-releases.ubuntu.com is a single slow, rate-limited host. Builds
against it will be markedly slower and flakier than 22.04/24.04, and that will
read to users as "frostroot is flaky."

### N5. Redistribution is unexamined

The deliverable is a tarball of Ubuntu binaries handed to third parties. For
unmodified archive packages this is fine — that is what the archive is for —
but Canonical's trademark policy constrains calling a *modified* image "Ubuntu",
and the artifact name is literally `<name>-ubuntu-<release>-amd64.tar.gz`.

Low risk for a classroom. Worth one paragraph in the README before anyone
publishes images publicly. Flagging because the existing review covers zero
non-technical ground.

## The verification gap

This is the part I would worry about more than any individual blocker.

Spec success criterion 5 — `wsl --import` boots and logs in as the user — is
the **only** criterion that tests the actual product promise. Everything else
tests that files have the right names and contents.

And it is explicitly excluded from automation ("manual check; not in default
tests"), correctly: no CI runner can `wsl --import`.

So the test pyramid is inverted. The unit layer is genuinely excellent — the
fake-`Bootstrapper` design is the right call and keeps the loop fast and
offline. But **every test in it can pass while the product is broken**, which is
precisely what B1 demonstrates. The suite's green is not evidence about the
artifact.

Residual unknowns that only a real boot settles: systemd-under-WSL first-boot
behaviour, `systemd-resolved` fighting WSL's generated `/etc/resolv.conf`,
failing units, and whether passwordless sudo and the default user actually take.

**Implication for the plan:** the spike is not a nice-to-have first task. It is
the only thing that converts this from "plausible" to "known". Until it runs,
any completion estimate is a guess.

## Positives

- **Scope discipline is unusually good.** The non-goals list is explicit and the
  extension points are designed for, not hand-waved. "Vendoring adds `sha256`
  to the existing list-of-tables" is a real, checkable forward-compatibility
  claim.
- **The seam choice is correct.** Putting mmdebstrap behind an interface is what
  makes the project testable at all.
- **Recipe/lock split is right** and matches how people already think (intent in
  git, facts generated).
- **Small.** 1.5k–2.5k LOC. One developer, days.
- **No Docker dependency** — a genuine differentiator for lab and airgapped
  environments where Docker is unavailable or unwelcome.
- **The failure modes are all concentrated in one seam.** That is the good kind
  of risk: bounded and fixable, not diffuse.

## Negatives

| Severity | Issue |
|---|---|
| **High** | Plan produces a non-booting artifact (B1–B3), and its tests pass anyway |
| **High** | Acceptance depends on manual Windows verification; not reproducible in CI |
| **Medium** | Golden images ship unpatched CVEs by default (B4); 20.04 permanently so (N4) |
| **Medium** | Two provisioning implementations that will drift (N3/I3) |
| **Medium** | `\|\| true` in hooks contradicts fail-closed — a failed `useradd` reports success and yields a root-login image |
| **Medium** | Unvalidated `locale`/`timezone` spliced into shell hooks |
| **Low** | Self-inflicted Windows test constraint (N1) |
| **Low** | Work directory defaults collide with 9p `/mnt/d` (I1) |
| **Low** | `dpkg-query --root` needs dpkg ≥ 1.21; excludes 20.04/Debian 11 hosts (I5) |
| **Low** | Redistribution terms unexamined (N5) |
| **Low** | Go 1.22 target is two years stale |

## Recommendation

Proceed. Change the sequence:

1. **Spike first, on the real WSL host.** Install mmdebstrap, do one noble build
   with real hooks and three `deb` lines straight to `.tar.gz`, `wsl --import`
   it, and check: login as the user, passwordless sudo, `systemctl status`, DNS,
   and an https `git clone`. ~1 hour. It settles B1–B4 and the WSL unknown at
   once. **Write nothing in Go until this passes.**
2. **Then revise the plan** — Task 4 (export becomes name + atomic move), Task 5
   (rendered files via `upload` hooks, no `|| true`, validated and shell-quoted
   inputs, `WriteProvisionFiles` demoted to test-only), Task 6 (no post-bootstrap
   host write, `context.Context`), Task 10 (tarball target, `TMPDIR`, keyring,
   `--architectures`, three `deb` lines, stderr streaming).
3. **Add one test that would have caught B1** — a symlink and hardlink
   round-trip through whatever produces the tarball.
4. **Update the spec**, which is currently self-contradictory on pockets: fix
   the essentials list, the pocket lines, the work directory, `--keep-rootfs`
   semantics, and add lock `sources`.

## Open decisions

These need your call before the plan can be rewritten:

1. **Design A or B** for the tar problem (mmdebstrap writes the tarball, vs.
   keep the directory and tar inside the namespace). A is simpler and safer;
   B preserves rootfs inspection. This determines what `--keep-rootfs` means,
   or whether it survives v1.
2. **Recommends on or off.** On makes `include` behave like `apt install` on
   stock Ubuntu — better for a classroom, larger images.
3. **20.04 policy.** Ship it with a loud unpatched-CVE warning, or drop it from
   v1? It is the spec's flagship justification, so dropping it is a real product
   change, not a cleanup.
4. **Drop the Windows test constraint?** (Recommend: yes.)

## Spike results (Task 0, 2026-09-16)

Task 0 of `plans/2026-09-15-frostroot-v1.md` was run by hand, including the
Windows half: the build, the tarball checks, `wsl --import`, and logging in.
**The gate passes.** The design assumptions hold. Two facts in the spec were
wrong and are corrected below; neither changes the architecture.

### Setup

| Item | Value |
|---|---|
| Windows | 11 Pro 26200, WSL 2.6.3, kernel 6.6.87.2 |
| Build host | fresh Ubuntu 24.04 WSL distro, used only for building |
| mmdebstrap | 1.4.3-6 from noble; apt 2.8.3, dpkg 1.22.6 |
| Mode | `--mode=unshare` as a normal user (uid 1000, subuid `100000:65536`) |
| Network | institutional TLS inspection on HTTPS; plain-HTTP apt unaffected (GPG-verified) |

The machine's existing 22.04 distro could not act as the build host: `uidmap`
was not installed and `sudo` needs a password. A clean 24.04 distro was the
simplest host, and it is also what the spec targets.

### Build and tarball

Exactly the Step 1 command, plus an identical focal build (see finding 1).

| | noble (24.04) | focal (20.04) |
|---|---|---|
| Wall clock | 134 s | 133 s |
| Tarball | 137 MB | 131 MB |
| Installed packages | 258 | 265 |
| Tar entries | 21,574 | 20,940 |
| Symlinks / empty targets | 2,173 / **0** | 2,588 / **0** |
| Hardlinks | 3, preserved (`perl5.38.2 → perl`) | 5, preserved |
| Owners | uids 0–104 and 1000 only; **none in the subuid range** | same |
| `security.capability` xattr | kept on `/usr/bin/ping` | kept on `/bin/ping` |
| `/home/student` | `1000:1000`, 0750 | `1000:1000`, 0755 |
| `/etc/sudoers.d/90-frostroot` | `0:0`, 0440 | same |
| Host `resolv.conf`, `hostname` | absent | absent |
| `/etc/machine-id` | empty; regenerated at first boot, unique per import | same |
| `git` / `systemd` versions | `1:2.43.0-1ubuntu7.3` / `255.4-1ubuntu8.17`: updates and security pockets used | `1:2.25.1-1ubuntu3.14` / `245.4-4ubuntu3.24` |

Defects 1, 2 and 4 are absent when mmdebstrap writes the tarball from inside the
namespace. Defect 3 is fixed by the essentials list: systemd is installed and
runs as PID 1.

### Boot under WSL (Step 3)

`wsl --import` took 6.6 s. Both images:

| Check | Result |
|---|---|
| `whoami` | `student`, uid 1000, `/bin/bash` |
| `sudo id` | root, no password prompt |
| PID 1 | systemd |
| `systemctl is-system-running` | `degraded`, never `offline`. noble: only `getty@tty1` fails (WSL has no tty1). focal: only `ua-auto-attach` fails (Ubuntu Pro auto-attach, not a cloud instance) |
| `/etc/resolv.conf` | generated by WSL (`→ /mnt/wsl/resolv.conf`), **even with `systemd-resolved` installed and active** |
| DNS | works |
| TLS with the image's CA bundle | `git ls-remote https://…` completes the TLS handshake against hosts this network does not inspect. github.com fails here only because of the inspection proxy's own CA |
| `locale` | `LANG=en_US.UTF-8`, no warnings |
| `sudo apt-get update` | all three pockets fetch |
| `ping` | works (file capability survived) |

**`systemd-resolved` does not fight WSL.** The open question in the spec is
settled: no masking hook. It is pulled in through Recommends, sits beside WSL's
generated `resolv.conf`, and DNS resolves.

### Findings that contradict the plan

1. **Focal is not on old-releases.** `http://old-releases.ubuntu.com/ubuntu/dists/focal{,-updates,-security}/Release`
   all return 404. An LTS release under ESM stays on `archive.ubuntu.com`;
   bionic is still there too. The table's 20.04 row would have failed every
   build. focal, focal-updates and focal-security all build from
   `http://archive.ubuntu.com/ubuntu`. The EOL warning still stands, on
   different grounds: security fixes after May 2025 are published only to
   Ubuntu Pro (ESM), not to `focal-security`. The frozen state is visible in the
   image, which ships `ca-certificates 20240203` and `tzdata 2025b`, against
   noble's `20260601` and `2026c`.
   **Change:** the 20.04 base URL becomes `http://archive.ubuntu.com/ubuntu`,
   `EOL` stays true, and criterion 7 is reworded.
2. **WSL overrides the image's timezone at every start.** `[time]
   useWindowsTimezone` defaults to true, so the recipe's `UTC` became
   `Europe/Istanbul` on first boot and `[locale].timezone` had no effect.
   Controlled check on the same image: without a `[time]` section, a
   terminate/start cycle rewrote `/etc/localtime` to the Windows zone again;
   with `useWindowsTimezone=false`, `UTC` survived the restart.
   **Change:** `RenderWSLConf` always writes `[time] useWindowsTimezone=false`,
   so the recipe decides.

### Smaller observations

- mmdebstrap splits the arguments of `upload` and `download` with
  `Text::ParseWords::shellwords`, so staged paths can and should be quoted.
- mmdebstrap creates the output file at 0 bytes before it starts. A failed run
  leaves an empty `image.tar.gz` in the work directory, so only trust the file
  after a zero exit.
- Harmless dpkg warning in unshare mode: `failed to open configuration file
  '/home/<user>/.dpkg.cfg'` (namespace root cannot read the user's home).
- `git` on the host reads the working directory's `.git/config`, so run image
  checks from a neutral directory.

## Release verification (v0.1.0, 2026-09-16)

The spike checked hand-written commands. This section checks the product:
the `frostroot` binary, from `init` to `wsl --import` and logging in, using the
exact import line `build` printed. Same Windows host and build host as the
spike.

### Three releases, end to end

| | 24.04 `noble-lab` | 22.04 `cpp-lab` | 20.04 `focal-lab` |
|---|---|---|---|
| Recipe | `python-lab` preset plus `curl`; `Asia/Tokyo` | `git`, `build-essential`, `cmake`; `Europe/Istanbul` | `git`; user `teacher`; `UTC` |
| Recipe directory | `/mnt/c/…`: drvfs, so `dist/` needs the cross-device copy | Linux filesystem | Linux filesystem |
| `build` | exit 0, 246 s (in parallel with 22.04) | exit 0, 275 s (in parallel) | exit 0, 147 s, EOL warning printed |
| Tarball / lock entries | 255 MB / 368 | 222 MB / 341 | 130 MB / 265 |
| `wsl --import` via the printed path | `C:\…` form, 4.2 s | `\\wsl.localhost\…` form, 10.6 s | `\\wsl.localhost\…` form, 6.4 s |
| Login, sudo, PID 1 | `student`, no password, systemd | `student`, no password, systemd | `teacher`, no password, systemd |
| `systemctl is-system-running` | `degraded`: `getty@tty1`, `console-getty` | `degraded`: `ua-auto-attach` | `degraded`: `ua-auto-attach`, plus `user@1000` on the first start only (active on the next two) |
| Timezone after boot (Windows is Istanbul) | `Asia/Tokyo` | `Europe/Istanbul` | `UTC` |
| DNS, `sudo apt-get update`, `ping`, TLS with the image's CA bundle | all work | all work | all work |
| Workload | venv and pip work; pip outside a venv stops at PEP 668 | CMake and g++ build and run a C++ program | n/a |

### Test suites

- `go test ./...`: passes. The compiled test binaries also pass **inside the
  24.04 image**, as uid 1000, with no Go, no mmdebstrap and no network (a
  network namespace in which name resolution fails): criterion 9 checked
  literally, not by assumption.
- `go test -tags=integration`: passes in 120 s.

### Failure paths, real binary and real mmdebstrap

| Case | Result |
|---|---|
| `XDG_CACHE_HOME` under a 0750 directory | exit 1 before any download, naming the blocking directory |
| Unknown package | exit 2; the error ends with apt's `Unable to locate package`; no lock, no tarball |
| Timezone that validates but is not in tzdata (`Mars/Olympus_Mons`) | exit 2 with `timezone Mars/Olympus_Mons does not exist in this image` |
| SIGINT to the frostroot process (not a terminal) 25 s into the bootstrap | mmdebstrap's main process and its workers all logged the signal; exit 130; no lock; no mmdebstrap processes left |

Every failed build kept its work directory, and each held only frostroot's
stage files and mmdebstrap's truncated output: mmdebstrap removed its chroot
even when interrupted, and a normal user could delete what was left.

### Defects that only real builds found

The unit suite was green for every one of these. Each now has a test that fails
without its fix.

1. **Every non-root build failed.** `os.MkdirTemp` makes 0700 directories, and
   in unshare mode mmdebstrap's root is a subordinate uid that cannot enter
   them. Found by the first integration run. The spike had used a 0755 `mkdir`.
2. **Every build with `dist/` on a Windows drive failed at the last step.** The
   cross-device copy chmodded its temporary file, and chmod returns EPERM on
   drvfs. Found by the first end-to-end run with the recipe under `/mnt/c`.
3. **Stopping a build with `kill` or `timeout` did not stop it.** mmdebstrap's
   main process answers SIGINT by waiting for its workers, relying on a
   terminal to signal the whole process group. Found by reading mmdebstrap
   while writing the interrupt test; mmdebstrap now runs in its own process
   group and frostroot signals that group.
4. **Unknown locales were not a failure.** `locale-gen` reports the error and
   exits 0. Found by running the provision script against the spike's noble
   and focal userlands before wiring it in.

### Code review and second bug hunt

After the release verification, a review of the whole branch found three
defects, and a further hunt (reading every file again, probing the platform,
and an independent reviewer) found more. All were invisible to the green
suite; each now has a test that fails without its fix.

| Defect | How it showed |
|---|---|
| One `sudo frostroot build` left a root-owned `/var/tmp/frostroot`; every later unprivileged build then failed at `MkdirTemp` with exit 2 | review; fixed with a per-user work root that must be owned by the current user |
| The same `sudo` build leaves `dist/` and the lock root-owned; the next unprivileged build in that directory failed only after the whole bootstrap | follow-on from the above; `build` now probes the output directories first |
| Two builds in one directory shared `frostroot.lock.tmp`; a failing one deleted the other's, which then failed after its tarball was already placed | review; unique O_EXCL temporary names |
| `validate` hid an unknown release behind an unsupported arch | review; `distro.Lookup` reports both |
| A UTF-8 byte order mark, as Notepad's "UTF-8 with BOM" writes, made the recipe unparseable with `invalid character at start of key: U+00EF` | writing the CRLF/BOM test; CRLF was already fine |
| A closed terminal no longer interrupted mmdebstrap, because it now runs in its own process group | reasoning about the process-group change; SIGHUP is handled like SIGINT |

Checked and found sound: `rename(2)` replaces an existing file on drvfs, even
one that is open, so `Place` and `init --force` behave on Windows drives.

Verified with real builds, as root inside the build host:

| Step | Result |
|---|---|
| `frostroot build` as root (mode `root`), tiny 24.04 recipe in a directory owned by `builder` | exit 0 in 163 s; 111 MB, 225 packages; `dist/`, the lock and `/var/tmp/frostroot-0` owned by root |
| `builder` then builds in the same directory | exit 1 in 0 s, before any download: `cannot write the build output: … dist/… permission denied (was a previous build run with sudo? …)` |
| `builder` builds in a fresh directory, with `/var/tmp/frostroot-0` still there | exit 0 in 130 s, working in `/var/tmp/frostroot-1000`; before the fix this was the exit-2 failure |

### Success criteria

| # | Criterion | Status |
|---|---|---|
| 1 | `init` writes a valid `frostroot.toml` | met: unit tests, and three real recipes that `validate` accepted |
| 2 | `build` for 24.04 writes the lock and `dist/<name>-ubuntu-24.04-amd64.tar.gz` | met |
| 3 | lock lists every installed package with versions, architectures and the three sources | met: 368 and 341 and 265 entries, three `sources` each |
| 4 | tarball has `wsl.conf` with systemd and the user, home, passwordless sudo, and systemd installed | met: integration test and three boots |
| 5 | tarball structurally sound | met: integration test (symlinks, hardlinks, owners, capabilities, no host files) |
| 6 | `wsl --import` boots and logs in as the user | met, manually, for all three releases |
| 7 | 20.04 builds (from the archive, see finding 1 above) and warns | met |
| 8 | versions come from `-updates` and `-security` | met: e.g. `git 1:2.34.1-1ubuntu1.17` on 22.04 |
| 9 | `go test ./...` passes offline, without root, without mmdebstrap | met, including inside an image with none of the three |
