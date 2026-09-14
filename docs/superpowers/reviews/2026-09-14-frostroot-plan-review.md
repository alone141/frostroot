# frostroot v1 plan review

Date: 2026-09-14
Reviewed: `docs/superpowers/specs/2026-09-14-frostroot-design.md` and `docs/superpowers/plans/2026-09-14-frostroot.md`
Status: review only. Next step is a revised concrete plan.

Host facts gathered for this review (read-only, nothing installed):

| Item | Value |
|---|---|
| WSL | 2.7.12, kernel 6.18 |
| Distro in WSL | Ubuntu 24.04.4, systemd on, uid 1000, subuid/subgid `100000:65536` |
| User namespaces | `unshare -Ur true` works; AppArmor inactive in WSL; an `/etc/apparmor.d/mmdebstrap` profile exists for non-WSL 24.04 hosts |
| mmdebstrap | not installed; apt candidate 1.4.3-6; `uidmap` is a Recommends, so `apt install mmdebstrap` pulls it |
| Go | not installed on Windows or in WSL; apt candidate 1.22 |
| `/tmp`, `/var/tmp` | on the ext4 root disk, about 928 GB free (not tmpfs) |
| `/mnt/d` (this repo) | 9p mount of `D:` |
| dpkg on host | 1.22.6 |
| Keyring | `/usr/share/keyrings/ubuntu-archive-keyring.gpg` present |

mmdebstrap behaviour below was checked against the noble (1.4.3) and bookworm man pages.

## Verdict

The project is feasible and small. The spec is tight, the Go layout is sound, and the fake-bootstrapper test strategy is the right way to keep `go test ./...` offline and Windows-friendly. Expect roughly 1.5k to 2.5k lines of Go including tests.

The plan as written, however, would produce a tarball that does not boot, and the image would not have systemd. There are four blocking problems and they all sit in the seam between Go and mmdebstrap, not in the architecture. Fix the plan before executing it; the CLI, recipe, distro and lock tasks survive almost unchanged.

## Blocking issues

### B1. The pure-Go tar writer breaks every symlink

Task 4 calls `tar.FileInfoHeader(info, "")`. The second argument is the symlink target, so every symlink in the rootfs is written with an empty target. A rootfs has thousands of symlinks: shared-library sonames, `/etc/alternatives`, and the merged-/usr `/bin`, `/lib`, `/sbin` links. The imported distro would not start. The unit test only checks proc/sys/dev skipping, so the TDD loop would pass while the product is broken.

### B2. Host-side tar and cleanup are wrong in unshare mode

The man page says a directory created in unshare mode "will end up with wrong ownership information (seen from outside the unshared user namespace)". Consequences for the plan:

- `export.WriteTarball` walks the rootfs from outside the namespace and records the remapped uids. `wsl --import` then yields a system with wrong ownership everywhere.
- Files owned by non-root users inside the chroot (`_apt`, the lab user's home) sit in the subuid range. The host user cannot read some of them and cannot delete their directories. `os.RemoveAll(work)` in Task 6 fails silently and leaves gigabytes behind after every build.
- Even in root mode, `tar.FileInfoHeader` fills `Uname` and `Gname` from the host passwd, the opposite of the `--numeric-owner` the spec asks for. Extended attributes such as file capabilities are dropped as well.

Recommended fix (design A): let mmdebstrap write the tarball. Target `<workdir>/image.tar.gz` (format and compression are inferred from the extension) with `TMPDIR` pointed at the work directory. mmdebstrap tars from inside the namespace as pax with extended attributes and correct ownership. Obtain the package list with the special hook `download /var/lib/dpkg/status <workdir>/dpkg-status` and parse it in Go (keep `Status: install ok installed`, sort by name), or with a `chroot "$1" dpkg-query ...` hook redirected to a host file. `internal/export` shrinks to naming plus an atomic move into `dist/`. `WriteProvisionFiles` becomes the test-only helper the spec intended.

Alternative (design B) if inspecting a kept rootfs matters: keep the directory target and run tar and rm inside the namespace via `mmdebstrap --unshare-helper` (documented in 1.3.x and later) or `unshare --map-root-user --map-auto` (util-linux 2.38 and later). More shell-outs and version checks; still never use `archive/tar` on a rootfs from outside the namespace.

`--keep-rootfs` needs a decision under design A: drop it for v1, or redefine it as "keep the work directory" (tarball, dpkg status, mmdebstrap log).

### B3. systemd is not in minbase

The man page defines minbase as the essential set plus Priority:required plus apt. `[boot] systemd=true` does nothing without systemd installed, so success criteria 4 and 5 cannot be met. Add `systemd` and `systemd-sysv` to the provision essentials, at least when `[wsl].systemd` is true.

Related: mmdebstrap does not install Recommends by default. So `git` does not pull `ca-certificates`, and `git clone https://...` fails in the lab image. Add `ca-certificates` to essentials, consider `dbus` (systemctl and friends), and decide whether to pass `--aptopt='Apt::Install-Recommends "true"'` so `include` behaves like `apt install` on stock Ubuntu. For a classroom golden image, Recommends on is the better default.

### B4. No updates or security pockets

A single suite argument yields a sources.list with only `jammy main universe`. The image gets release-day package versions with no `-updates` or `-security` pocket, so the golden image ships years of unpatched CVEs. The spec's own lock example, `git 1:2.34.1-1ubuntu1.11`, is an updates-pocket version and is unattainable with the planned invocation.

mmdebstrap accepts several MIRROR arguments as full `deb ...` lines. The distro table should produce three lines per release: `<suite>`, `<suite>-updates`, `<suite>-security`, all against one base URL (old-releases carries every pocket for focal; archive.ubuntu.com carries security). `--mirror` replaces the base URL for all three. Record the lines in the lock (for example `sources = [...]`) so vendoring can reuse them later.

## Important issues

### I1. Work directory location

The spec says `os.MkdirTemp("", ...)`; the `.gitignore` already expects `.frostroot-work/` under the recipe directory. Neither fits this setup. The repo lives on `D:`, which WSL mounts as 9p at `/mnt/d`. A bootstrap there is slow, and the chown and device-node operations it needs are unreliable on that filesystem. Recommend:

- Default work dir under `$XDG_CACHE_HOME/frostroot/` or `/var/tmp`, never under `/mnt/`; warn or refuse otherwise.
- Set `TMPDIR` for the mmdebstrap child to the work dir (it stages the tarball there).
- Final move into `dist/`: rename when on the same device, otherwise copy, fsync, rename. `dist/` on `D:` is exactly where `wsl --import` wants the file.
- When running inside WSL, print the `wsl --import` line with the Windows path from `wslpath -w` so it can be pasted into PowerShell.

### I2. Hooks swallow failures and interpolate unvalidated strings

- `useradd ... || true` and `locale-gen || true` contradict fail-closed. A failed useradd means WSL logs in as root while the build reports success.
- `[locale].lang` and `[locale].timezone` are not validated and are spliced into shell. Add regexes in `Validate`, check that `/usr/share/zoneinfo/<tz>` exists inside the chroot, and shell-quote every interpolated value.
- Also write `/etc/timezone`, and remove the host-copied `/etc/resolv.conf` and `/etc/hostname` in a hook (the man page says mmdebstrap copies them from the host and leaves them in place).
- The machine-id and apt-lists hooks are redundant: default cleanup already empties machine-id and removes lists and cache.

### I3. Two sources of truth for provisioning

Task 6 calls `WriteProvisionFiles` in the real build after mmdebstrap, duplicating what the hooks already wrote. Have Go render `wsl.conf` and the sudoers line once, unit-test the rendered strings, place them with the `upload` special hook, and follow with a `chmod 0440` hook for sudoers. Drop the post-bootstrap host write.

### I4. Ctrl-C, progress and the stderr tail are not in the plan

The spec requires exit 130 with tmp cleanup and a kept work dir; the plan has no task for it. Add `context.Context` to `Bootstrapper.Run` now (changing the interface later is churn), use `signal.NotifyContext` and `exec.CommandContext`, and set `cmd.Cancel` to send SIGINT with a generous `WaitDelay`. Never SIGKILL mmdebstrap in root mode: it has proc, sys and dev mounted inside the chroot, and a recursive delete of a work directory with live mounts reaches into the host. Under design A frostroot never deletes a chroot directory, which removes that hazard. Stream mmdebstrap stderr to the terminal through a `MultiWriter` while keeping the last few KiB for the error message; a five-minute silent build looks hung.

### I5. dpkg-query --root and host-tool assumptions

`dpkg-query --root` is a dpkg 1.21 addition, which Ubuntu 20.04 and Debian 11 predate. Parsing the status file removes the host dependency entirely; otherwise use `--admindir`. Also pass `--keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg` explicitly and fail with an install hint if it is missing (Debian hosts need `ubuntu-keyring`). Pass `--architectures=amd64` and refuse non-amd64 hosts in v1 rather than depending on binfmt.

### I6. Dead error values

`ErrNoPrivilege` and `ErrNoMmdebstrap` are declared in the builder but never returned. Either implement a cheap probe (`newuidmap` on PATH, subuid entry present, `unshare -Ur true`) that returns `ErrNoPrivilege` with both remedies, or delete them and let mmdebstrap's stderr explain.

## Minor

- `recipe.Load` should use `DisallowUnknownFields` so a `[package]` typo fails `validate` instead of silently producing an empty include list.
- `init` writes via `toml.Marshal`, which cannot emit the comments shown in the spec example; use a text template.
- Tests: assert on `boot.hooks`; add a symlink round-trip test for whatever tars the rootfs (skip on Windows); a 0440 file in `t.TempDir` may need a chmod in cleanup on Windows.
- Go 1.22 is old for 2026; use the current stable. A module path without a dot cannot be `go install`ed; fine for v1.
- Tasks 9 and 10 need stub types to keep `main` compiling; merge them or put 10 first.
- `python-lab` preset: pip on 24.04 refuses system installs (PEP 668); a README note about venv avoids the first support question.
- `Validate` should give a clear message when `arch` is empty.
- Consider recording per-package `arch` in the lock now; vendoring will need it.

## Suggested plan changes

1. Add Task 0, a manual spike in WSL, before any Go: install mmdebstrap, run one noble build with the real hooks and three `deb` lines to a `.tar.gz`, `wsl --import` it, confirm login as the user, passwordless sudo, `systemctl status`, DNS, and an https `git clone`. About one hour, and it validates every risky assumption above.
2. Rewrite Task 4 (export becomes name plus move), Task 5 (rendered files plus upload hooks, no `|| true`, validated inputs), Task 6 (no post-bootstrap host write, context, tmp handling), Task 10 (tarball target, `TMPDIR`, keyring, architectures, `deb` lines, stderr streaming).
3. Add a small task for Ctrl-C and exit 130.
4. Update the spec: essentials list, pockets, work dir location, `--keep-rootfs` semantics, lock `sources`.
5. Optional: GitHub Actions running `go test ./...` on ubuntu and windows, with a manual or nightly integration job.

## Feasibility summary

Nothing here changes the shape of the product. The risk is concentrated in the mmdebstrap seam, and every item has a concrete fix. The host is ready apart from installing Go and mmdebstrap. The remaining unknown is WSL-specific first-boot behaviour with systemd (DNS, failing units), which the spike covers.
