# frostroot v0.4: vendoring, offline rebuilds and releases

Date: 2026-09-17
Status: approved by the project owner in conversation ("first capture, then vendoring/offline builds, with releases folded in"); implemented on branch `feature/vendor`
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md) (the extension point "Vendoring `.deb`s") and [`2026-09-17-frostroot-tui.md`](2026-09-17-frostroot-tui.md) (the progress screen it reuses)

## Verification (2026-09-17, build host)

- `gofmt`, `go vet` with and without the `integration` tag, `go test -race
  ./...`, `GOOS=windows go build ./...` and `golangci-lint`: clean. Every
  `go.sum` line, the two new modules included, matches `sum.golang.org`.
- **Real binary, plain interface.** A 24.04 recipe with `curl` and `git`:
  `build` in 169 s wrote a 137 MB tarball and a lock of 260 packages, every
  one with `sha256`, `size` and `filename`. `vendor` fetched all 260
  (98.2 MB) in 54 s; run again it downloaded nothing ("260 already there").
  With one file overwritten with garbage and a stale `.deb` added,
  `vendor --prune` replaced the one and removed the other. The lock was
  byte-identical before and after.
- **Offline, for real.** As root, `ip netns add`, then `build --offline`
  as `builder` inside that namespace, where `curl http://archive.ubuntu.com`
  fails to resolve: 87 s, "260 packages, every one as locked"; the
  image's dpkg status has exactly the lock's 260 (name, version, arch);
  its `/etc/apt/sources.list` holds the three archive lines; the lock was
  not rewritten.
- `TestIntegrationOfflineRebuild`: an online build of a minimal image,
  `pool.Fetch` from the archive, an offline rebuild with a changed user
  name, then checks on the phases reported (the eleven offline phases in
  order, a real copy total), the `sources.list`, the new user in `passwd`,
  and the absence of any `copy://` line. Passes in 306 s beside the
  existing `TestIntegrationNobleTiny`.
- **The vendor screen under a pty** (`script`, 100x40): the three phases
  drawn, percentages from 0% to 100%, one log line per file with its size
  and source URL, and the summary printed after the screen closed.
- **Release build:** `CGO_ENABLED=0 go build -trimpath -ldflags="-s -w"`
  yields a 10.7 MB statically linked, stripped ELF that prints
  `frostroot 0.4.0`. The workflow itself runs on the first pushed tag,
  which is the owner's call.

## Why

frostroot's promise is "freeze". Today it keeps half of it: the tarball is
frozen, but the act of building is not. `frostroot build` next month fetches
whatever the Ubuntu archive holds then, and the lock only tells you what
moved. The `-updates` and `-security` pockets move weekly, and the archive
removes superseded packages from its pool soon after, so a lock older than a
few weeks describes packages that can no longer be downloaded from the
archive at all.

This version completes the promise. `frostroot vendor` downloads every
package the lock names, checks each against the checksum the build recorded,
and keeps them in `vendor/debs/`. `frostroot build --offline` rebuilds the
image from that directory and nothing else: no archive, no network, no
keyring, and it refuses to produce an image whose package set differs from
the lock by even one version. A recipe directory with its lock and its
`vendor/debs/` is a lab that can be rebuilt in a year, on an air-gapped
machine, with a different user name or timezone, and come out with the same
packages.

A release process comes with it, because a tool meant to rebuild things in a
year must itself be obtainable in a year: tagged GitHub Releases with a
static binary, and `frostroot version`.

## Goal

```
frostroot vendor [--mirror URL] [--prune] [--plain]
frostroot build --offline [--keep-work] [--plain]
frostroot version
```

- `build` (online, unchanged for the user) now records `sha256`, `size` and
  `filename` for every package in the lock.
- `vendor` fills `vendor/debs/` from the lock. It is idempotent and
  resumable: files already there and correct are kept, files missing or
  corrupt are fetched again.
- `build --offline` installs from `vendor/debs/` only, and fails if the
  result differs from the lock.
- `version` prints the version and, when known, the commit it was built from.
- Tagging `vX.Y.Z` on GitHub publishes a release with a static
  `frostroot-linux-amd64` and its checksum.

## What "reproducible" means here

**Guaranteed:** an offline build installs exactly the packages the lock
lists, at exactly those versions, from `.deb` files whose SHA-256 matches
what the original build downloaded through apt's signed indexes. The build
checks this at the end and fails otherwise. This is what a lab needs: the
same compiler, the same libraries, the same behavior.

**Not guaranteed:** a byte-identical tarball. File timestamps are set when
dpkg unpacks, `locale-gen` and `useradd` run at build time, and gzip records
the time. Two offline builds of one lock have the same files with the same
contents and modes; `sha256sum` of the two tarballs differs. mmdebstrap can
clamp timestamps with `SOURCE_DATE_EPOCH`; making the tarball itself
bit-identical is a possible follow-up and is out of scope here.

## Non-goals

- Vendoring anything but the apt packages of the lock: no apt index files, no
  language packages, no source packages.
- Rebuilding a *different* recipe offline. If `frostroot.toml` changed its
  release, architecture or `include` list since the lock was written,
  `build --offline` refuses: the lock no longer describes that recipe. User
  name, sudo, locale, timezone and systemd may change freely; they are
  provisioned at build time and need no packages.
- A local mirror server, or serving `vendor/debs/` to other machines. The
  directory is copied around like the tarball is.
- Vendoring on Windows. `vendor` needs no Linux facility and does run
  anywhere Go runs, but frostroot stays a Linux program; there is nothing to
  do with the result elsewhere.
- Binaries for hosts other than linux/amd64. frostroot compiles anywhere, but
  mmdebstrap building an amd64 image on another architecture needs qemu, and
  that is untested.

## The lock

`[[packages]]` entries gain three fields. The lock format version stays 1:
the change is additive, as the v1 design reserved.

```toml
[[packages]]
name = 'curl'
version = '8.5.0-2ubuntu10.13'
arch = 'amd64'
sha256 = '7d0b2c1a…'
size = 227180
filename = 'pool/main/c/curl/curl_8.5.0-2ubuntu10.13_amd64.deb'
```

- `sha256`, `size`: of the `.deb` file, as apt's Packages index states them
  and as `vendor` verifies them.
- `filename`: the path relative to the mirror's base URL, as the index states
  it. `vendor` fetches `<mirror>/<filename>`. The local copy is the file's
  base name: `vendor/debs/curl_8.5.0-2ubuntu10.13_amd64.deb`. Base names are
  unique per (name, version, arch), epochs included (`git_1%3a2.43…`).

A lock written by frostroot 0.3 or earlier has none of these; `vendor` and
`build --offline` say so and ask for one online `build` first. `LoadLock`
accepts both, so `validate` and tests keep working on old locks.

### Where the checksums come from

apt verified the archive's signed `InRelease` files and stored the package
indexes it trusted in the chroot's `/var/lib/apt/lists/`. mmdebstrap deletes
them in its cleanup stage, *after* the customize hooks. So the build adds one
hook, before the dpkg status download:

```
copy-out /var/lib/apt/lists <work>/stage/apt-lists
```

and, when writing the lock, reads every `*_Packages` file in it (about 100 MB
for noble's six indexes; a second of parsing) and takes `Filename`, `SHA256`
and `Size` from the stanza whose `Package`, `Version` and `Architecture`
match each installed package. Rules, from the spike:

- The same version can be listed in several indexes: `-updates` and
  `-security` often carry one version, and a package moved between
  components after release (`ocl-icd-*` moved from universe to main) is
  listed at two pool paths. The checksums must agree; the `filename` is
  taken from the most recent pocket (`-security`, then `-updates`, then the
  release pocket), which is where the file currently lives.
- Checksums that disagree, or an installed package that no index lists, fail
  the build with the package named. Neither happens in a frostroot build,
  where everything comes through apt; if it ever does, a lock without a
  checksum is not something `vendor` can act on, and silence would hide a
  broken invariant.

The alternative, fetching the indexes again at `vendor` time, was rejected:
it would take the checksums from an unauthenticated download instead of from
apt's verified copy, and it would fail exactly when vendoring matters most,
once the version has left the index.

## `frostroot vendor`

```
frostroot vendor [--mirror URL] [--prune] [--plain]
```

1. Load `frostroot.lock`; refuse a lock without checksums or with a format
   version other than 1. `--mirror` replaces the lock's mirror as the
   download base; the pool layout is the same on every Ubuntu mirror.
2. Check `vendor/debs/`. Every lock entry whose file exists with the right
   size and SHA-256 is kept; a wrong size or hash means the file is replaced.
   This check hashes what is there (a few hundred MB, a second or two) and
   is shown as a phase.
3. Download what is missing, four files at a time, each to a temporary name
   in `vendor/debs/` and renamed into place only after its size and SHA-256
   matched. A mismatch removes the temporary file and fails the run: the
   mirror served something other than what the build installed.
4. Sources, tried in order for each file:
   - `<mirror>/<filename>`.
   - On 404 only: Launchpad's librarian,
     `https://launchpad.net/ubuntu/+archive/primary/+files/<basename>`,
     which keeps every package Ubuntu ever published. The hash check makes
     the source irrelevant to trust. This is what makes an old lock
     vendorable at all.
   - Any other failure retries once, then fails the run with the URL and the
     reason. Rerunning `vendor` resumes.
5. `--prune` removes `.deb` files in `vendor/debs/` that the lock does not
   list, and says which. Without it they are counted and mentioned, never
   touched.
6. Summary: how many packages, how many bytes, how many were already there,
   where they are, and the next step (`frostroot build --offline`).

Interface: the progress screen, with phases "Read frostroot.lock", "Check
vendor/debs", "Download packages" (bytes bar with a real total) and, with
`--prune`, "Remove packages not in the lock"; each finished download is a line
in the log pane. `--plain` or no terminal prints one line per phase and per
tenth, as `build` does. Ctrl-C stops the downloads; finished files stay,
partial ones are removed.

HTTP: Go's default transport (so `http_proxy` and `https_proxy` are honored),
no overall timeout, a connection timeout per request, and Ctrl-C through the
context.

The directory is not for git. A `vendor/debs/` for a real lab is 300 MB to
a gigabyte; the README says to ship it beside the tarball or in an archive,
and to add `vendor/` to `.gitignore` unless git LFS is in use.

## `frostroot build --offline`

Before any work directory exists:

1. Load the lock; refuse one without checksums (as `vendor` does).
2. Compare it with the recipe: `release`, `arch` and the set of `requested`
   packages must match `image.release`, `image.arch` and `packages.include`
   (order does not matter). Otherwise: "frostroot.toml has changed since
   frostroot.lock was written (…): run frostroot build online, then
   frostroot vendor", exit 1, with the differences listed.
3. Preflight: mmdebstrap on PATH, work root usable and reachable. **No
   keyring check**: nothing is verified through apt's signatures, the pool
   is verified by frostroot against the lock.
4. Check `vendor/debs/` exactly as `vendor` step 2 does. Anything missing or
   corrupt: exit 1 naming up to ten files and "run frostroot vendor".
   `--mirror` and `--offline` together are a usage error.

Then, in the work directory, a **flat apt repository** at `<work>/pool/`:

- Every locked `.deb`, hard-linked from `vendor/debs/` when both are on one
  filesystem, copied otherwise (the usual case under WSL, where the recipe
  lives on a Windows drive), mode 0644 so that apt's unprivileged fetcher
  can read it. Copying a few hundred MB is a phase with a bytes bar.
- `Packages`: for each file, the control paragraph read from the `.deb`
  itself (its `control.tar` member, gzip, xz or zstd), followed by
  `Filename: ./<basename>`, `Size`, `MD5sum` and `SHA256`. Only the locked
  files go in, so apt cannot see anything else.
- `Release`: `Origin`/`Label` frostroot, `Suite` and `Codename` from the
  lock, `Architectures`, `Date`, and the `MD5Sum`/`SHA256` of `Packages`.
  Not decoration: mmdebstrap selects the essential set with an apt pattern
  narrowed to the suite's archive or codename, and without this file it
  finds no essential packages and the build fails (spike result below).

mmdebstrap then runs as for an online build, except:

| Online | Offline |
|---|---|
| three `deb http://…` source lines | one: `deb [trusted=yes] copy://<work>/pool ./` |
| `--keyring=…` | no keyring |
| `--include` = recipe packages + essentials | `--include` = **every** package in the lock |
| — | one more hook uploads `/etc/apt/sources.list` rendered from the lock's `sources`, so the image points at the archive again |
| "Write frostroot.lock" phase | "Check against frostroot.lock" phase |

`copy://` rather than `file://`: mmdebstrap's manual notes that a `file://`
path must also be reachable from inside the chroot, which a host path is not;
`copy://` copies each package in. `[trusted=yes]` because the repository is
unsigned; its integrity was established by the hash check above, which is
stronger than a signature on an index frostroot itself wrote.

**Why include every locked package.** The Packages index frostroot writes
takes `Priority` from each package's control file, but the archive's own
index applies ftp-master overrides, and the two disagree for most packages
(172 of 238 in the spike). mmdebstrap's `--variant=important`
chooses its first, essential set from `Priority`, so with control-file
priorities that set could be smaller than the online one. Listing every lock
package in `--include` makes the outcome independent of priorities: apt has
nothing else to choose, and whatever the first stage leaves out the second
installs. The essential packages of the recipe are in the lock like
everything else.

After the bootstrap, the dpkg status is parsed as usual and compared with the
lock: the sets of (name, version, arch) must be equal. A difference fails the
build (exit 2, work directory kept, tarball not placed) with every package
that is only in the lock or only in the image. The lock is **not rewritten**:
it is the input of an offline build, not its output. The tarball is placed
as usual and the summary says it was rebuilt from the lock.

The image's `apt` works normally afterwards against the archive, because the
sources hook restored the three pocket lines and mmdebstrap's cleanup removes
the `copy://` index it fetched.

## The progress screen, generalized

`internal/tui` keeps one screen for long-running commands. `RunBuild`
becomes `RunProgress(screen, events, done, cancel, in, out)` where `screen`
carries a title ("frostroot build", "frostroot vendor"), a subtitle, the
ordered list of phases to show and the log pane's title ("mmdebstrap",
"downloads"). The build screen is that with the build's phases. Phases are
`builder.Phase` values; the enum grows the vendor and offline phases, and
`builder.Phases()` (online), `builder.OfflinePhases()` and
`builder.VendorPhases()` list them. `done` delivers an `error`; the caller
keeps its own result. Outcome handling (Ctrl-C once cancels, twice abandons)
is unchanged.

`internal/pool`, which downloads and verifies, must not import `builder`
(`builder` imports it for the offline check), so it reports through a small
callback interface of its own; the CLI turns those into progress events.

## `frostroot version` and releases

`frostroot version` prints `frostroot 0.4.0`, followed by `(commit abc1234,
2026-09-17)` when the binary was built from a git checkout (Go's build info
carries `vcs.revision` and `vcs.time`; `-buildvcs=false` builds print the
version alone; a dirty checkout gets `+dirty`). `frostroot --version` and
`-v` do the same. `builder.Version` becomes `0.4.0` and stays the single
source of the version string.

`.github/workflows/release.yml` runs on a pushed tag matching `v*`:

1. Check the tag equals `v` + `builder.Version`; fail otherwise, so a
   binary never claims a version other than its tag.
2. `go test ./...` (the default, offline suite).
3. `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath
   -ldflags="-s -w" -o frostroot-linux-amd64 ./cmd/frostroot`: static, so
   it runs on any glibc or musl distribution, including 20.04.
4. `sha256sum frostroot-linux-amd64 > SHA256SUMS`.
5. `gh release create <tag> --title "frostroot <version>" --generate-notes`
   with both files. Permissions: `contents: write`. Actions pinned to the
   versions CI already uses.

Cutting a release is a manual act (pushing a tag) and is not part of this
change; the workflow is exercised by the first tag the owner pushes. The
README's install section gains the download, `chmod +x`, checksum and
`frostroot version` steps, and keeps `go build` as the source route.

## Architecture

Two new packages; the import graph stays a tree:

```
cli ──▶ builder ──▶ pool ──▶ deb ──▶ recipe
 │        │          │
 │        └──▶ recipe┘
 └──▶ pool, tui, …
```

**`internal/deb`**: Debian package formats, no frostroot policy.
- `ReadStanzas(reader, visit func(Stanza) error) error`: control-file
  paragraphs, streamed, for indexes of tens of megabytes.
- `ReadControl(debPath) (Control, error)`: the `control` file of a `.deb`
  (ar archive, `control.tar.gz`/`.xz`/`.zst`), returned as the raw paragraph
  plus parsed fields.
- `WriteFlatRepository(dir, suite, debFileNames) error`: `Packages` and
  `Release` next to the files.
- `IndexEntries(reader)`: `Package`, `Version`, `Architecture`, `Filename`,
  `Size`, `SHA256` from a Packages file.

**`internal/pool`**: the vendored pool.
- `Manifest(lock) ([]Entry, error)`: base name, URL path, size, hash per
  package; rejects locks without checksums and unsafe file names.
- `Verify(dir, entries) Status`: present, missing, corrupt, extra.
- `Fetch(ctx, FetchOptions) (Summary, error)`: the download described above.
- `Prune(dir, entries) ([]string, error)`.
- `Stage(dir, entries, workDir, onProgress) error`: link or copy into the
  work directory, world-readable, then `deb.WriteFlatRepository`.

`builder.Options` gains `Offline bool`; `Build` branches after loading the
recipe and returns the same `Result` (with `Result.Offline` set and
`LockPath` naming the lock it was checked against). `BootstrapSpec` gains
`Trusted bool` (no keyring, source lines as given) and the extra hook comes
from `CustomizeHooks` as today.

### Dependencies

Two new modules, both pure Go, both mirrored into the offline proxy and
verified against `sum.golang.org` like the others:

- `github.com/klauspost/compress` (its `zstd` package): every `.deb` from
  jammy on has `control.tar.zst`; the standard library has no zstd.
- `github.com/ulikunitz/xz`: focal's packages have `control.tar.xz`; the
  standard library has no xz.

`compress/gzip` covers older packages. The alternative was shelling out to
`dpkg-deb -f`, which a focal host's dpkg cannot do for zstd packages, and
which makes the unit tests depend on a host tool. `CONTRIBUTING.md` records
the exception.

## Errors

| Situation | Exit | Message says |
|---|---|---|
| `vendor` or `build --offline` without a lock | 1 | run `frostroot build` first |
| lock without checksums (frostroot ≤ 0.3) | 1 | rebuild once with this version |
| recipe changed since the lock (`--offline`) | 1 | what differs; build online, then vendor |
| `vendor/debs` incomplete or corrupt (`--offline`) | 1 | up to ten names; run `frostroot vendor` |
| `--offline` with `--mirror` | 1 | usage |
| `vendor`: a file 404s on the mirror and on Launchpad | 2 | the package, both URLs |
| `vendor`: network failure after a retry | 2 | the URL and the error |
| `vendor`: checksum mismatch | 2 | the package and both hashes; nothing kept |
| `--offline`: image differs from the lock | 2 | the packages only in one of them; work directory kept |
| interrupted | 130 | finished downloads kept, partial ones removed |

Everything in the table before the bootstrap starts is exit 1 and happens
before any work directory exists, as the v1 error table requires.

## Testing

Default suite, offline, without root or mmdebstrap:

- `deb`: stanza streaming (continuation lines, last paragraph without a blank
  line, oversize lines); `.deb` reading for gzip, xz and zstd control
  members built in the test with the same libraries, plus a bad magic, a
  missing member and a truncated archive; `Packages`/`Release` output checked
  field by field and for determinism; index entries from a fixture cut from
  the spike's real lists.
- `pool`: manifest from a lock (epoch base names, rejection of `..` and
  absolute file names, rejection of missing checksums); verify with present,
  missing, corrupt and extra files; fetch against `httptest` servers: all
  fresh, resume with a corrupt file, 404 on the mirror then success on the
  fallback, 404 on both, checksum mismatch leaves nothing behind, cancel
  mid-download removes the partial file, four workers do not corrupt
  progress totals; prune; stage by link and by copy.
- `builder`: an online build with a fake bootstrapper that writes a lists
  directory records the checksums; a package missing from the lists fails;
  two indexes disagreeing fail; an offline build with a fake bootstrapper
  gets the `copy://` line, no keyring, every lock package in `--include`,
  the sources hook, and does not touch the lock; the post-build comparison
  fails on a differing status; each pre-bootstrap refusal happens before the
  bootstrapper runs.
- `cli`: `vendor` exit codes and summary with an `httptest` mirror;
  `build --offline` refusals and the success summary; `version` output with
  and without build info; the phase lines of the plain interface for both.
- `tui`: the generalized screen with a vendor phase list.

By hand on the build host (recorded in this document when done): an online
build of the capture recipe with a lock full of checksums; `vendor` for real
(≈300 MB); `build --offline` producing the same package set, run inside a
network namespace with no interfaces to prove nothing is fetched; the pty
smoke test extended to `vendor`; the release build command producing a
static binary that runs on the host.

## Spike results (2026-09-17, build host, mmdebstrap 1.4.3)

- `.deb` control members: `control.tar.gz` for packages built before 2018,
  `control.tar.xz` through focal, `control.tar.zst` from jammy on (checked
  on `hello` across releases and `libc6` for focal, jammy and noble). All
  three decoders are needed.
- At customize-hook time the chroot's `/var/lib/apt/lists/` holds the six
  uncompressed `Packages` indexes (107 MB for noble, 73 MB of it universe)
  and the three `InRelease` files. `copy-out` of the directory works. Every
  one of the 238 packages of a minimal image was found in them, with
  consistent checksums; three (`ocl-icd-*`) were listed at two pool paths
  after a component move.
- `/var/cache/apt/archives/` at that point still held 143 of the 238
  `.deb` files: mmdebstrap deletes the essential packages' files after
  extracting them. Not a usable source; the lists are.
- Offline rebuild from a `[trusted=yes] copy://` flat repository of those
  238 files, with every one of them in `--include`:
  - **Without a `Release` file it fails in one second.** apt itself accepts
    the repository, but mmdebstrap chooses the essential set with an apt
    pattern narrowed to `?archive(^noble$)` or `?codename(^noble$)`, which
    matches nothing in a repository without `Suite` and `Codename`; it
    reports "no essential packages -- skipping" and the install into an
    empty chroot fails. The `Release` file is not optional.
  - **With `Release` (`Suite: noble`, `Codename: noble`) it succeeds in
    70 seconds** and the dpkg status lists exactly the 238 packages and
    versions of the online build. The image's `/etc/apt/sources.list` holds
    the `copy://` line, which is what the sources hook corrects.
  - `--verbose` output for `copy://` has the same shape the progress parser
    reads: `Need to get 28.1 MB of archives.` and
    `Get:1 copy:/…/pool ./ gcc-14-base 14.2.0-4ubuntu2~24.04.1 [51.0 kB]`.
    The index round shows `Get:2 copy:/… ./ Release [329 B]` and the
    `Packages` file.
  - Control-file `Priority` disagreed with the archive's index for 172 of
    the 238 packages (`libc6` is `optional` in its control file and
    `required` in the archive; `tzdata` the other way round). mmdebstrap's
    first stage therefore installed 95 packages instead of 143, and the
    second stage the rest. The final set was identical only because every
    package was in `--include`; the rule in "Why include every locked
    package" is load-bearing.
  - Five `W: Unable to read …/mmdebstrap.apt.conf.* - RealFileExists`
    warnings appear in the online log too: a mmdebstrap quirk, not an
    offline problem.

## Open questions

None. The spike answered the two that were open: the flat repository needs
a `Release` file with `Suite` and `Codename`, and apt reports `copy://`
fetches in the form the progress parser already understands.
