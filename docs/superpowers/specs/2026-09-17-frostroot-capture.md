# frostroot v0.3: `frostroot capture`

Date: 2026-09-17
Status: approved by the project owner in conversation ("first let's do capture, then vendoring"); implementation on branch `feature/capture`
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md) (the extension point "Capturing an existing machine") and [`2026-09-17-frostroot-tui.md`](2026-09-17-frostroot-tui.md) (the form it reuses)

## Verification (2026-09-17, build host)

- `gofmt`, `go vet` with and without the `integration` tag, `go test -race
  ./...`, `GOOS=windows go build ./...` and `golangci-lint`: clean. The
  fixture tests cover a WSL-like machine (PPA package, pip and npm installs,
  an edited conffile, a hand-written unit, a cron job, a second user, a
  docker group, dotfiles with `.ssh`), a bare server without `wsl.conf` or
  `extended_states`, and the four refusals; one fixture makes the secrets
  unreadable to prove they are never opened.
- `frostroot capture --plain` on the build host itself, accepting every
  default: 582 packages installed, 5 asked for (`curl git golang-1.24
  libattr1 mmdebstrap`), which is `apt-mark showmanual` minus the base,
  `ca-certificates` (an essential) and the two metapackages. User `builder`
  with no sudo, `C.UTF-8`, `Europe/Istanbul`, systemd on. The report lists
  the one hand-added file in `/etc` (`/etc/profile.d/go.sh`), the home's
  eleven entries, and "nothing found" for the other eight areas.
- `frostroot build` from that recipe, unchanged: a 308 MB image with 351
  packages and `requested` equal to the five names, in 236 seconds.

## Why

Most people do not start from a blank recipe. They start from a machine that
already works: a WSL distribution grown over a semester, with the compiler,
the tools and the habits of a course in it. Today the only way to turn that
into a frostroot image is to remember what was installed and retype it.
`frostroot capture` reads the machine and writes the recipe, and, just as
importantly, writes down what it could not read.

The v1 spec fixed two rules for this command, and they still hold:

1. **It must report what it could not see.** apt is the only thing capture
   understands. Software installed with pip, npm, cargo, `curl | sh`,
   `make install`, unpacked into `/opt`, or added by hand to `/etc` is
   invisible to the recipe. A capture that quietly writes an incomplete
   recipe is worse than none, because the user believes the machine is
   captured. The deliverable is the recipe **and** a report of the gaps.
2. **It must not tar the live root filesystem.** A live machine holds SSH
   keys, cloud credentials, `.env` files, shell history and password hashes;
   tarring it and handing the result to a class would distribute all of
   them. Capture reads package metadata and a few configuration files, and
   copies nothing else. It never reads the content of a home directory
   beyond listing names.

## Goal

```
frostroot capture [--root DIR] [--force] [--plain]
```

reads an installed Ubuntu system (the one frostroot runs on, or another root
filesystem mounted at `DIR`), opens the recipe form with every field set
from what it found, and writes `frostroot.toml` plus `frostroot-capture.md`,
the report. The recipe then flows through `validate` and `build` unchanged.
No root required: everything capture reads is world-readable on Ubuntu.

## Non-goals

- Capturing files, home directories, or anything that is not an apt
  package. Reported, never copied.
- Reproducing third-party apt sources. A package that came from a PPA is
  reported as such; the recipe has nowhere to put the PPA yet (roadmap item:
  extra apt sources).
- Capturing a non-Ubuntu system, or an Ubuntu release frostroot cannot
  build. Both are errors, stated plainly.
- Running on the machine as root, or reading files that need root (the
  shadow file, another user's home). Nothing capture needs is behind root.

## What goes into the recipe

Everything is read from files under the root, never by running commands:
the target may be a mounted disk, and file parsing is what the test suite
can fixture.

| Recipe field | Source | Rule |
|---|---|---|
| `image.name` | the hostname (`/etc/hostname`), else `captured` | made valid: lowercased, characters outside the image-name rule replaced by `-` |
| `image.release` | `/etc/os-release` `VERSION_ID`, with `ID=ubuntu` | must be a supported release, else the capture fails |
| `image.arch` | `/var/lib/dpkg/status` `Architecture` of `dpkg` | must be `amd64`, else the capture fails |
| `user.name` | `/etc/wsl.conf` `[user] default`, else the lowest uid ≥ 1000 in `/etc/passwd` that has a home under `/home`, else `student` | reported when guessed |
| `user.sudo` | the user is in the `sudo` or `admin` group (`/etc/group`), or named in a readable `/etc/sudoers.d/*` file | `NOPASSWD` is not checked; the recipe only knows passwordless sudo or none, and the report says which was seen |
| `wsl.systemd` | `/etc/wsl.conf` `[boot] systemd`; `true` when the file or key is missing | |
| `locale.lang` | `/etc/default/locale` `LANG`, else `C.UTF-8` | must pass the locale rule, else `C.UTF-8` and a report line |
| `locale.timezone` | `/etc/timezone`, else the target of the `/etc/localtime` symlink under `zoneinfo/`, else `UTC` | must pass the timezone rule |
| `packages.include` | see below | |

### Which packages

The recipe lists what the person asked for, not the closure. Capture
recovers that from apt's own bookkeeping:

1. Installed packages: stanzas in `/var/lib/dpkg/status` whose status word
   is `installed` (the parser `build` already has).
2. Automatically installed: `/var/lib/apt/extended_states` stanzas with
   `Auto-Installed: 1`. Everything installed and not marked automatic was
   asked for by someone (`apt-mark showmanual`).
3. From the manual set, drop what `build` provides anyway: packages of
   priority `required` or `important` (the `--variant=important` base),
   frostroot's provisioning essentials (`builder.EssentialPackages`), and
   the metapackages `ubuntu-minimal`, `ubuntu-standard`, `ubuntu-wsl`,
   `ubuntu-server`, `ubuntu-desktop*` (their content is base or reported
   separately).
4. What remains is `packages.include`, sorted. Names that fail the package
   rule (there should be none; dpkg enforces the same grammar) are dropped
   and reported.

If `extended_states` is missing or empty, apt has no record of what was
automatic, and every installed package of priority `optional`/`extra`/
`standard` would be listed. Capture does that and says so in the report:
the recipe is correct (it rebuilds the same set) but verbose.

## The report

`frostroot-capture.md`, next to the recipe, Markdown, written every time
capture writes a recipe. It has two parts.

**Captured** restates what went into the recipe and from where, in one line
each, so a reader can check the guesses: "user `melik` from `/etc/wsl.conf`",
"timezone `Europe/Istanbul` from `/etc/timezone`", "58 packages asked for,
of 1 203 installed".

**Not captured** lists what the recipe cannot carry, one section per area,
each with a count, up to twenty examples, and one sentence on what to do
about it. Areas, in order:

| Area | How it is found | Note in the report |
|---|---|---|
| Third-party apt sources | source files (`/etc/apt/sources.list`, `sources.list.d/*`) whose URIs are not on an Ubuntu archive host (`archive.ubuntu.com`, `security.ubuntu.com`, `*.archive.ubuntu.com`, `ports.ubuntu.com`, `esm.ubuntu.com`) | "the recipe cannot add apt sources yet; extra sources are on the roadmap" |
| Packages from third-party sources | apt index files in `/var/lib/apt/lists/*_Packages` whose name does not start with an Ubuntu archive host; an included package that appears only in such indexes came from there | "the recipe lists them, but build installs from the Ubuntu archive; if they are not there the build fails" |
| Packages from no source | included packages that appear in no index at all | "installed from a downloaded `.deb`; build will not find them" |
| Modified configuration | `Conffiles` entries in the dpkg status whose file's MD5 differs from the recorded one | "edits to `/etc` are not part of a recipe" |
| Files added to `/etc` | regular files under `/etc` that no package's `.list` owns, excluding what the system generates (alternatives, `*.wants`, account databases, `ld.so.cache`, certificates, locale and timezone files, machine identity, `wsl.conf`, sudoers, apt sources, and the other well-known generated paths listed in `capture/etc.go`) | same |
| Software outside apt | regular files under `/usr/local/bin`, `/usr/local/sbin`, `/usr/local/lib`; entries of `/opt`; `*.dist-info` directories under `/usr/local/lib/python3*/dist-packages` and `~/.local/lib/python3*/site-packages` (pip); `/usr/local/lib/node_modules/*` (npm); `~/.cargo/bin/*`, `~/go/bin/*`; `~/.nvm`, `~/.rustup`, `~/.pyenv`, `~/miniconda3`, `~/anaconda3`, `~/.local/pipx` (toolchain managers, by presence); `/snap/*` or `/var/lib/snapd/snaps/*.snap` (snaps); `/var/lib/flatpak` (flatpak) | "not from apt; reinstall after import, or wait for post-install hooks (roadmap)" |
| Services and scheduled jobs | `/etc/systemd/system/*.service` and `*.timer` not owned by a package; `/var/spool/cron/crontabs/*` (names only; the files are not readable without root, which is fine), `/etc/cron.d/*` not owned | |
| Other people | uid ≥ 1000 users in `/etc/passwd` other than the chosen one | "a recipe has one user" |
| The user's home | the names of dotfiles and dot-directories at the top of the home directory, and a count of everything else; `.ssh`, `.gnupg`, `.aws`, `.kube`, `.docker` are called out as secrets that must never be copied | "dotfiles are not part of a recipe; keep them in a repository the students clone" |
| Groups | the user's supplementary groups beyond `sudo`, `adm`, `users`, `plugdev`, `dialout`, `cdrom`, `floppy`, `audio`, `video`, `dip`, `netdev`, `lxd` (the defaults `adduser` gives), for example `docker` | |

An area with nothing to report is listed with "nothing found", so the
reader knows it was looked at rather than skipped.

The report is also summarized on standard output after the recipe is
written: one line per area with its count, and the path of the file.

## The flow

1. **Read.** `capture.Read(root, home)` walks the files above and returns a
   `Snapshot`: the recipe values, the evidence for each (which file), and
   the findings. It never fails on a missing or unreadable file: each area
   records that it could not be read and moves on. It fails only when the
   system is not Ubuntu, the release is unsupported, or the architecture is
   not amd64, because then no recipe can be written.
2. **Adjust.** The recipe values become form values (`form.FromRecipe`), and
   the same form as `init` opens with them: the image name defaults to the
   hostname and is worth changing, the packages are pre-checked in the
   catalog or listed in the free-text field. With `--plain` or no terminal,
   the same questions come as lines with the captured values as defaults.
3. **Write.** `frostroot.toml` through the template, atomically, as `init`
   does, then `frostroot-capture.md`. `--force` is needed to overwrite an
   existing recipe; the report is always overwritten. The summary of the
   report is printed last, so it is what the user sees.

Nothing outside the current directory is written. Nothing is written when
the form is canceled or the recipe does not validate.

### `--root DIR`

Every path capture reads is joined to `DIR`, default `/`. This is how the
tests work (fixture trees under `testdata/capture/`), and it lets a mounted
disk or an extracted rootfs be captured. With `--root`, the home directory
is the user's home from `DIR/etc/passwd`, under `DIR`; without it, the same
under `/`.

## Architecture

```
internal/capture    Read(root, options) (Snapshot, error); the parsers for
                    os-release, passwd, group, wsl.conf, default/locale,
                    extended_states, dpkg info lists, apt index names, the
                    generated-path list; Snapshot.Recipe(), Snapshot.Report()
internal/cli        runCapture: flags, Read, the form (shared with init/edit),
                    writeRecipe, the report file, the summary
```

`capture` has its own small reader for Debian control stanzas (the dpkg
status, `extended_states` and apt's `Packages` indexes share the format),
because it needs fields the lock never records: `Priority`, `Conffiles`.
`builder.ParseDpkgStatus` stays as it is. `capture` uses `recipe`'s check
functions and `builder.EssentialPackages`; it imports nothing from `tui`,
and `tui` nothing from it.

Every finding is a `Finding{Area, Count, Examples []string, Advice string}`;
the report renderer and the summary printer are the only code that knows
Markdown or line formats. Areas are a table; adding one is adding a
collector function and a row.

## Testing

- `internal/capture`: fixture roots built by the tests in a temporary
  directory from a map of path to content (a few dozen small files each):
  a WSL-like machine (wsl.conf user, sudo group, PPA index, pip packages,
  modified conffile, unowned unit, dotfiles), a bare server (no wsl.conf, no
  extended_states, two users), a non-Ubuntu root, an unsupported release.
  Table tests per parser; `Read` on each fixture compared with the expected
  recipe and findings; the report rendered and checked for each area's
  heading and count.
- `internal/cli`: `capture --plain` on a fixture with scripted answers writes
  both files; `--force`; unsupported release exits 1 with the reason and
  writes nothing; the report summary on stdout.
- Integration: `capture --root /` on the build host writes a recipe that
  validates, lists the packages installed there by hand, and reports the
  host's known gaps.

## Success criteria

1. On the build host, `frostroot capture --plain` accepting every default
   writes a recipe that `validate` accepts and whose `include` is the set
   `apt-mark showmanual` gives minus the base, and a report naming every
   area.
2. A machine with a PPA package, a pip package and an edited `/etc` file
   gets all three in its report, each under the right heading.
3. A non-Ubuntu root, or Ubuntu 23.10, is refused with one clear sentence
   and no file written.
4. Capture reads nothing under the home directory except entry names, and
   nothing from `/etc/shadow`, `/etc/gshadow` or `/etc/sudoers`; a test with
   a fixture that makes those unreadable still passes.
5. `edit` on a captured recipe and `build` from it need no special cases.
