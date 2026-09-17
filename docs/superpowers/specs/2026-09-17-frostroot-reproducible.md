# frostroot v0.6: byte-identical offline rebuilds

Date: 2026-09-17
Status: approved by the project owner in conversation ("plan and work for 2", reproducible tarballs); implementation on branch `feature/reproducible`
Extends: [`2026-09-17-frostroot-vendor.md`](2026-09-17-frostroot-vendor.md), whose "What reproducible means here" left the tarball's bytes out of the promise

## Why

v0.4 made an offline rebuild install exactly the lock's packages, and said
plainly that the tarball's bytes were not part of the promise: timestamps
and the gzip header differ between builds. That is the last gap in
"freeze". A lab that can produce the same `sha256sum` a year later has a
proof it can hand around; a lab whose tarball merely "has the same files"
has an argument. mmdebstrap was designed for this, given one input,
`SOURCE_DATE_EPOCH`, and the spike showed that with it set, two offline
builds of one lock are already byte-identical, provisioning included. What
is missing is for frostroot to choose the epoch, record it in the lock and
use it again, so that nobody has to know the variable exists.

## Goal

- `build` records `source_date_epoch` in the lock: the instant the image
  is frozen at. Every file in the tarball is dated no later than it.
- `build --offline` uses the lock's epoch. Two offline builds of one lock,
  on any machines with the same mmdebstrap and dpkg behavior, produce the
  same bytes, and `sha256sum` says so.
- The offline image gets the same apt auto-installed marks as the online
  one, so `apt autoremove` and `frostroot capture` behave the same in
  either.

## Non-goals

- Byte identity between the **online** build and the offline rebuild. The
  spike measured the distance after the auto-mark fix: one directory's
  timestamp (`lib.usr-is-merged/`) and the line order of
  `/var/lib/dpkg/triggers/File`, both consequences of a different
  installation order. Making the online build install in the offline
  build's order would mean giving up mmdebstrap's essential stage for it.
  Not worth it: the online build is the one that runs once; the offline
  build is the one that must be repeatable.
- Reproducibility across mmdebstrap or dpkg versions. The lock records
  what was installed, not the tools that installed it; a different
  mmdebstrap may lay files out differently.
- Packages that generate random material at install time (an
  `openssh-server` host key, for one). They are not reproducible by
  nature, and shipping their output in a golden image is a separate
  problem the README already warns about in spirit.

## The epoch

`SOURCE_DATE_EPOCH` is the reproducible-builds convention: a Unix time in
seconds that every tool clamps timestamps to. mmdebstrap honors it for the
tarball's mtimes, sorts entries, drops atime and ctime from PAX headers,
writes gzip with no name or timestamp, and removes the files that would
otherwise carry the build time (dpkg and apt logs, `ldconfig`'s aux cache,
`machine-id`). shadow-utils honors it for `/etc/shadow`'s last-change
day, which the spike confirmed: the field read 20348, the epoch's day, not
the build day's.

**Online build:** the epoch is `SOURCE_DATE_EPOCH` from the environment
when it is set and is a whole number of seconds, otherwise the second the
build started. It is passed to mmdebstrap in its environment and recorded
in the lock:

```toml
source_date_epoch = 1789647780
```

**Offline build:** the epoch is the lock's. The environment variable is
ignored offline, with a note when it is set, because the lock is the
input. A lock without the field (frostroot 0.4 or 0.5) still builds, with
the build's own time and a message that its output is not byte-identical
to anything until the recipe is built online once more.

## Auto-installed marks

apt keeps `/var/lib/apt/extended_states`, which packages it installed on
its own to satisfy dependencies. An offline build puts every locked
package in `--include` (the v0.4 spec says why), so apt marks none of them
automatic and the file comes out empty. That changes what `apt autoremove`
would remove and what `frostroot capture` would list as asked for.

The online build downloads that file after the install (a hook makes sure
it exists first, since apt writes it only once it has something to say)
and the lock gains `auto = true` on every package apt marked:

```toml
[[packages]]
name = 'libcurl4t64'
version = '8.5.0-2ubuntu10.6'
arch = 'amd64'
auto = true
…
```

The offline build renders the file from the lock in apt's own format, one
paragraph per marked package in lock order, and uploads it as the last
hook before the dpkg status download. Two offline builds render it
identically; the online file's paragraph order is apt's, so online and
offline still differ in that file's bytes, not in its meaning.

## What changes where

- `recipe.Lockfile.SourceDateEpoch int64` (`source_date_epoch`, omitted
  when zero); `recipe.LockPackage.Auto bool` (`auto`, omitted when false).
- `builder.BootstrapSpec.SourceDateEpoch`; `Mmdebstrap.commandLine` adds
  `SOURCE_DATE_EPOCH=<n>` to the environment beside `TMPDIR`.
- The builder decides the epoch itself from the environment, the clock or
  the lock (no option: the environment variable is the convention, and
  nothing else would set one); `Result.SourceDateEpoch` and
  `Result.Reproducible` for the summary. The stage gains
  `ExtendedStatesPath`, where the online image's file is downloaded to, and
  `AutoMarksPath`, the file rendered from the lock that the offline build
  uploads. `RenderExtendedStates(packages, nativeArch)`,
  `ParseExtendedStates(reader)`. apt records a package of architecture
  `all` under the image's native architecture, and the renderer does the
  same.
- `cli build` prints the instant the image is frozen at, in UTC, and
  offline says that the tarball is byte-identical with any other offline
  build of this lock, or, for an old lock, that it is not.
- `builder.Version` becomes `0.6.0`; the lock format stays 1.

## Errors

| Situation | Exit | Message says |
|---|---|---|
| `SOURCE_DATE_EPOCH` in the environment is not a whole number of seconds | 1 | the value, and that it is ignored offline anyway |
| the online image has no `extended_states` even after the hook | 2 | the hook's failure, as any hook failure |

## Testing

- `recipe`: the two fields round-trip and old locks still load.
- `builder`: the fake bootstrapper records its environment; an online
  build passes an epoch and records it, taking the environment's when
  valid; an offline build passes the lock's, ignores the environment, and
  warns without one; `ParseExtendedStates` and `RenderExtendedStates`
  round-trip against a real excerpt; the online build records `auto`
  marks from the fake's file; the offline build stages the rendered file
  and its upload hook.
- `cli`: the frozen-at line online, the byte-identical line offline, the
  old-lock note.
- Integration: `TestIntegrationOfflineRebuild` builds offline twice and
  requires equal SHA-256 sums, checks that no tarball entry is dated after
  the epoch, and that the offline image's `extended_states` lists the
  packages the lock marks.
- Build host: the real binary, online build, `vendor`, two offline builds
  a few minutes apart with `sha256sum` agreeing, and the diff against the
  online image reduced to the two cosmetic differences.

## Spike results (2026-09-17, build host, mmdebstrap 1.4.3, gzip 1.12)

- Two `build --offline` runs of the v0.4 lock (260 packages) with
  `SOURCE_DATE_EPOCH=1758067200` in the environment, nothing else changed:
  identical SHA-256. The gzip header is `1f8b 0800 0000 0000 0003`: no
  timestamp, no name.
- `/etc/shadow` in both: `student:!:20348:0:99999:7:::`, the epoch's day.
- An online build with the same epoch against the offline one: same 21594
  entries, listing differs in one directory's mtime (`lib.usr-is-merged/`
  keeps a 2024 date offline, gets the epoch online), contents differ in
  two files: `/var/lib/apt/extended_states` (empty offline, 2199 bytes
  online) and `/var/lib/dpkg/triggers/File` (two lines in another order).
