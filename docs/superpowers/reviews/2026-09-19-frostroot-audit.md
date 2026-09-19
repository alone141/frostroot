# frostroot code audit, 2026-09-19

Date: 2026-09-19
Scope: the whole tree at `dd3bf53` + `ecb0609` (v0.12, ~34,000 non-test lines of Go)
Method: eight parallel read-only reviewers, one per subsystem, plus a
cross-cutting sweep; every claim re-checked against the source before it was
written down here
Status: findings only. No code was changed and no test was weakened. Every
finding below is filed as an issue: #50 through #84, indexed by #85.

## Verdict

**Thirty-five findings, none of them a red test.** The suite passes, `go vet`
passes under both build tags, `golangci-lint` is clean, the race detector
finds nothing, and `go test -shuffle=on` is stable. Everything here is a
defect the tests do not reach.

Three of them are in the trust chain the project's claims rest on, and one of
those defeats fingerprint pinning outright. Five more can hang the program,
destroy a file a person put there, or exhaust memory from the network. Six
are incompleteness in the v0.12 index work of the last two days — including
two cases where the fix was applied to one half of a pair and a test was
written that does not exercise the loop it names.

What was **not** found is worth as much: the `.deb` and wheel download path
survived fuzzing, `pool`'s verify-then-rename and `--prune` containment are
sound, `shellQuote` holds across all sixty-four rendered script variants,
`internal/deb`'s parsers are bounded, and no `InsecureSkipVerify` exists
anywhere in the module.

## Tier 1 — trust

### T1. The pinned fingerprint is defeated by appending a key

`internal/sources/fetch.go:101`, with `internal/pgp/pgp.go:50`.

`FetchKey` compares the pin against `pgp.ParsePublicKey`, whose own doc
comment says "the fingerprint is the first one's", and then writes
`pgp.Armor(key.Binary)` — the whole fetched blob.

Whoever answers the key URL serves `<real key> ‖ <their key>`. The pin
passes; both keys are written to `keys/<name>.asc`; `build` uploads the file
to `/etc/apt/keyrings/frostroot-<name>.gpg`, which is that source's
`signed-by` keyring; apt accepts a `Release` signed by **any** key in a
`signed-by` keyring. Pinning exists to survive a compromised key host, and
this is exactly that case.

Evidence, with the repository's own fixtures:

```
pinned key fingerprint : 9DC858229FC7DD38854AE2D88D81803C0EBFCD88 (2760 bytes)
appended key           : 2C6106201985B60E6C7AC87323F3D4EA75716059 (4528 bytes)
the pair parses as     : 9DC858229FC7DD38854AE2D88D81803C0EBFCD88 (7288 bytes)
pin still matches      : true
```

Why the tests miss it: `sources_test.go:166` substitutes one *wrong* key for
another; no test presents the pinned key *plus* something else.
`pgp_test.go:49` pins down that a two-key file returns the first key's
fingerprint, and says nothing about where the second key came from.

**A fix cannot simply refuse multi-key files.** The v0.5 spec accepts them
deliberately: GitHub's real keyring holds two primary keys and the catalog
pins one of them. The fix is to pin the *set* — enumerate every primary key
in the file and require each to be named — which means the catalog entry for
`github-cli` gains its second fingerprint.

### T2. A key file is checked once, at first fetch, and never again

`internal/builder/lockcheck.go:196`, with `internal/builder/sources.go:80`
and `internal/recipe/lock.go:93`.

The lock records `key_sha256` for every source. `grep -rn KeySHA256` returns
three hits: the field, the write, and a test asserting it is 64 characters
long. **Nothing ever reads it back.** `repositoryDifferences` compares URL,
suite and components, and its comment says the key "is only trust" and is not
compared — but the key is a file that goes *into the image*, so it is part of
the tarball's bytes.

Replace `keys/docker.asc` with another valid key (a pull request touching one
file, a rotation, any local write) and run `build --offline`: no mismatch is
reported, the build succeeds, and the image ships the new key as that
source's `signed-by` keyring. `recipeform.go:202` also skips the fetch — and
so the pin check — whenever the key file already exists, and
`readSourceKeys` only asks whether the bytes are parseable OpenPGP.

`CheckCertificatesAgainstLock` (`certificates.go:126`) is the same comparison
for `[certificates]`, written for the same reason. Keys have no counterpart.

Why the tests miss it: `TestBuildOfflineComparesSources` varies components
and adds and removes sources, but never rewrites a key file.

### T3. The build's CA bundle path is written permanently into the image

`internal/builder/certificates.go:108` → `internal/builder/bootstrap.go:163`.

`writeAptCaInfo` returns a path under the work directory, and frostroot
passes it as `--aptopt=Acquire::https::CaInfo "<that path>"`. mmdebstrap does
not treat `--aptopt` as a build-time option. Verified against mmdebstrap
itself, not its documentation alone:

- `/usr/bin/mmdebstrap:2096` writes every `--aptopt` into
  `$root/etc/apt/apt.conf.d/99mmdebstrap` — inside the chroot;
- its own POD at `:6530` says "permanently added … inside the chroot. Use
  hooks for temporary";
- cleanup at `:3123` unlinks `00mmdebstrap` only, so the file ships.

So any recipe with `[certificates]`, and any build with `--ca-bundle`, ships
an image containing

```
Acquire::https::CaInfo "/var/tmp/frostroot-1000/build-3f9a2b/apt-ca-bundle.pem";
```

Three consequences, each against something the README states:

1. `apt update` inside the imported image fails for every https source,
   because that CA file is not there — README:492 promises it works.
2. The build host's work path and uid are baked into the image.
3. `os.MkdirTemp(workRoot, "build-*")` randomises the path, so **two online
   builds of one recipe with certificates differ in bytes**, against
   README:637's "a build with it produces the same bytes as a build without
   it".

Offline rebuilds are unaffected: `writeAptCaInfo` runs only when online.

Why the tests miss it: `grep -rn CaInfoPath` matches `builder.go` and
`bootstrap.go` and no test at all; the fake bootstrapper does not model
`99mmdebstrap`, and no integration test passes certificates.

### T4. A source URL may carry credentials, and they reach the image

`internal/recipe/validate.go:177`, against `:202`.

`CheckPythonIndexURL` rejects `parsed.User != nil` "because a recipe is a
file people commit and review". `CheckSourceURL` has no such rule, so
`https://buildbot:s3cret@apt.corp.example/ubuntu` validates and the password
is written into `frostroot.toml`, into the lock twice (`sources` and
`[[repositories]].url`), and into `/etc/apt/sources.list` **inside the
distributed tarball**.

A second effect: `AptListPrefix` renders that URL as
`buildbot:s3cret%40apt.corp.example_ubuntu`, while apt clears the userinfo
before quoting, so `originOf` matches nothing and the build fails after
mmdebstrap has run.

## Tier 2 — hangs and data loss

### A1. A failed progress screen hangs the program

`internal/cli/build.go:189`, identically `internal/cli/vendor.go:152`. Found
independently by two reviewers.

When `tui.RunProgress` returns an error the fallback does `outcome =
tui.Outcome{Err: <-done}` — and stops draining `events`. The worker fills the
256-event buffer, blocks in `events <- event`, never reaches `done`, and
never observes the cancelled context. frostroot hangs; SIGINT and SIGTERM
cannot free it, because the builder is parked inside the progress callback.
Only SIGKILL ends it, with mmdebstrap still running.

Why the tests miss it: the one full-screen test has the screen succeed and
the fake bootstrapper emit three events. `runVendorFullScreen` has no test.

### A2. `capture` overwrites a certificate file that was already there

`internal/cli/capture.go:143`.

`writeCapturedCertificates` stats the target only to decide bookkeeping, then
writes unconditionally. Its doc comment says "A file that was already there
is left alone", and `capture.go:55` promises "A form the person abandons
leaves nothing behind that was not already there".

A recipe directory holding your own `certs/corp-root.pem`; `frostroot capture
--force`; answer **no** at "Write frostroot.toml?". Exit 1, nothing else
written — and your file now holds the machine's bytes. It is not in
`written`, so it is never restored. A write that precedes and survives a
refused confirmation.

### A3. A failure after writing deletes the certificates the recipe names

`internal/cli/capture.go:69`.

`removeCapturedCertificates` is keyed on the exit code, not on whether the
recipe was written. `runRecipeForm` writes the recipe at `recipeform.go:111`
and can still fail afterwards, fetching keys. When it does, capture deletes
the certificate files the recipe on disk now names, and the advice printed
("`frostroot edit` fetches them again") cannot restore a file read off the
machine. The error path at `capture.go:56` also discards the `written` list,
leaving earlier certificates behind when a later one fails.

### A4. An index download is unbounded until after it is parsed

`internal/index/fetch.go:231`, and the same shape in
`internal/index/pypi.go:218`.

The body is read to EOF, every stanza accumulated, the remainder drained with
an unbounded `io.Copy`, and only then is `counted.count` compared with the
size `Release` declared. The declared size therefore bounds nothing, and
`packageindex.go:251` states outright that the archive path has no deadline.

Measured by the reviewer: a 1.7 MB gzip expanding to 144 MB of stanzas drove
`OpenSource` to 278 MB of heap and ~2.1 GB of allocation before the digest
was looked at — about 170× the bytes on the wire, with no ceiling. Scaled up,
the interactive form is OOM-killed. That defeats the spec's own rule that a
source which cannot be read must not break the picker: it ends the process,
not the source.

Every other network read in the project is capped — `pool/fetch.go:299`,
`summaries.go:125`, `sources/fetch.go:70`, `deb/package.go`. This is the one
that is not.

### A5. An offline build with an empty `sources` list ships the work path

`internal/builder/builder.go:283`.

`stageOptions.SourceLines = offline.lock.Sources` is unvalidated: `LoadLock`
validates nothing and `planOffline` never inspects `lock.Sources`. Empty, no
`sources.list` is staged and no upload hook is emitted, while `builder.go:327`
has already handed mmdebstrap `deb [trusted=yes] copy://<workdir>/pool ./`,
which mmdebstrap writes into the image and never removes.

A hand-edited or truncated lock therefore produces a tarball whose
`/etc/apt/sources.list` names the build host's scratch directory with
`[trusted=yes]`, and — because the directory is randomised per run — two
offline rebuilds of that lock differ in bytes.

## Tier 3 — reproducibility

### R1. `PYTHONPYCACHEPREFIX` escapes the environment sweep

`internal/builder/python.go:145`.

The hook inherits the build user's environment; mmdebstrap unsets only
`TMPDIR` and `APT_CONFIG`. The script now neutralises `PIP_*`, `HOME`,
`PYTHONHASHSEED` and the four CA variables — but nothing else named
`PYTHON*`. With `PYTHONPYCACHEPREFIX` set, `compileall` writes no
`__pycache__` into the venv at all and instead creates
`<prefix>/<absolute path>/…` — inside the image, carrying the builder's home
path, and never removed. Two hosts then produce different tarballs from one
recipe. Same class as the leak `HOME=/root` was added to fix.

### R2. A failed lock rename can orphan the previous lock

`internal/builder/builder.go:272`.

`export.Place` has already replaced `dist/<name>…tar.gz` by the time the lock
rename is attempted, so removing the new tarball on failure leaves the old
lock describing an image that no longer exists — the mirror of the invariant
that removal was added to protect — and destroys the only copy of the image
just built. Partly deliberate; the case where `dist/` was not empty is
unhandled in both directions.

## Tier 4 — the v0.12 index work

Six defects in code written in the last two days. Two are fixes applied to
one half of a pair; one is a test that does not exercise what it names.

### V1. `oneLine` does not strip a newline

`internal/index/fetch.go:312`.

`oneLine` replaces `\t` and `\r`; `deb.ReadStanzas` joins continuation lines
with `\n` (`internal/deb/stanza.go:51`). A `Package:`, `Version:` or
`Section:` folded over a continuation line therefore writes a broken line
into the tab-separated cache, the next read returns "unreadable entry",
deletes the file, and refetches — for ever, 21 MB a run for the archive. The
comment above `reduce` claims precisely the guarantee that is missing.

### V2. An answer missing a repository is remembered as complete

`internal/cli/packageindex.go:177`.

`get` decides completeness from its local `unreachable` slice, but
`index.Open` returns **no error** when a repository is unreachable and a
cache exists: it returns the stale cache with `missing` set. So `unreachable`
is empty, the incomplete answer is memoised, and the retry added in v0.12
never fires. The guard covers the error path; the stale-cache path is the
common one.

### V3. PyPI still shares one memo between offline and fetched answers

`internal/cli/packageindex.go:365`.

The apt path keeps the two apart; `python` does not. Anything that runs
`KnownPython` before the Python field's own load lands stores the stale cache
in `p.pypi`, and every later `OpenPython` returns it: `--refresh-index` is
silently ignored for PyPI for the rest of the run.

### V4. A deadline discards a usable cache

`internal/index/index.go:155`.

`openTarget` tests `ctx.Err() != nil` before it considers the cache, so the
20-second per-source deadline added in v0.12 turns a slow vendor into a
thrown-away cache: the picker says "not reachable" while a readable index
sits in the cache directory. Cancellation should still win; a deadline should
fall through to the cache.

### V5. `SectionsMatching` still counts sections that do not exist

`internal/index/search.go:88`, and the test at `source_test.go:196`.

`newIndex` skips an empty `Section`; the query path does not, so a vendor's
section-less packages produce a second "all sections" row that filters by
nothing. The test written for this calls `SectionsMatching("")`, which
short-circuits to the already-filtered list and never reaches the loop it
names.

### V6. A newline in a project name breaks the PyPI cache

`internal/index/pypi.go:428`.

Names are written one per line with no escaping and read back requiring
strictly increasing normalised order, so a private index (`--python-index`)
serving a name with a newline — or two names that normalise alike, which
pypi.org forbids and a private index does not — makes the 9.7 MB index
refetch on every run.

## Tier 5 — the form and the picker

### F1. The Python picker applies the apt name rule

`internal/tui/picker.go:570`, and `:529` for a pasted list.

Both hardcode `recipe.CheckPackageName` — lowercase, no underscore — instead
of the field's own validator, which for the Python field is
`CheckPythonPackageName`. So on the Python field, typing `Flask`, `Django`,
`PyYAML` or `flask_sqlalchemy` offers no row, Space does nothing, and Enter
says `"Flask" is not added: Space adds it` — advice that cannot be followed.
The name is silently lost. Pasting a list keeps `requests` and drops the
rest. This contradicts both the stated invariant ("a name the index does not
have is a warning, never a refusal") and the picker spec's "Offline is not an
error".

### F2. A byte-order mark makes `edit` report two identical lines

`internal/textdiff/textdiff.go:124`, called from `recipeform.go:144`.

`recipe.Load` strips a UTF-8 BOM; `recipePreview` reads the file raw and
`splitLines` normalises CRLF and trailing newlines but not the BOM. A
`frostroot.toml` saved by Notepad as "UTF-8 with BOM" therefore parses to an
identical recipe and still shows "2 lines change", the pane showing two lines
a reader cannot tell apart, and the question defaults to Write instead of
Cancel.

### F3. The Python picker shows a project twice, and the twin undoes the first

`internal/tui/picker.go:569`.

`exact` compares the query byte-for-byte with the top row, while the PyPI
index matches by PEP 503 normalisation and returns the published spelling.
Type `flask`: the list is `Flask`, `Flask`, `Flask-SQLAlchemy`, with a status
line reading "2 found" above three rows. Space on the first adds it; Space on
its twin removes it again.

### F4. Every rest of the cursor fires two PyPI lookups

`internal/tui/picker.go:385`.

huh hands the focused field every non-key message twice. The tick path
defends against this by bumping its counter; the summary path does not, so
`fetchSummary` is returned twice and both goroutines miss the cache — two
HTTPS requests per row, against the pypi spec's "one request and not twenty".

### F5. The summary is not cut to the terminal width

`internal/tui/form.go:270`.

`resize` counts the summary and the warning as one row per `\n`, but `View`
writes them raw; only the pane is fitted. `capture` produces recipes with
hundreds of packages on one `Packages` line — 40 names is already 729 cells —
so at 60×20 the frame needs 32 terminal rows and the heading, the answers and
the "not in the archive" warning scroll off the one screen that asks whether
to write. The recorded golden frames already contain over-wide lines;
`assertFrame` compares text and never checks the size it recorded at.

### F6. A PEP 503 duplicate reaches validation after the form is over

`internal/tui/picker.go:529` with `internal/form/form.go:308`.

The picker dedupes by exact string and the field validator checks names in
isolation, but `recipe.Validate` compares normalised. `flask-sqlalchemy
flask.sqlalchemy` passes the form, shows no warning, defaults to Write, and
is then refused after the confirmation with "nothing written", discarding the
session.

## Tier 6 — capture

### C1. A symlinked `.crt` is read outside `--root`

`internal/capture/certificates.go:54`. `os.ReadFile(root.path(...))` with no
`pathInRoot`. `update-ca-certificates` accepts symlinks, so this is an
ordinary layout: capturing a mounted tree resolves the link on the *auditing*
machine and copies that host's bytes into the recipe.

### C2. A `..` in dpkg's `Conffiles` is read outside `--root`

`internal/capture/packages.go:202`. `root.path` is `filepath.Join`, which
folds `..` away rather than refusing it, and follows symlinks. A hostile
`status` file reads host files and names them in the report; a benign
symlinked conffile is compared against the wrong machine's copy.

### C3. Only `Enabled: no` counts as disabled

`internal/capture/sources.go:103`. Verified against apt 2.8.3: of
`yes/no/false/0/off/disable/enabled`, apt fetches only `yes` and `enabled`. A
`.sources` file saying `Enabled: false` is dead on the machine, and capture
carries it as live — re-enabling a repository its owner turned off. (Distinct
from the documented one-line `enabled=no` limitation.)

### C4. Certificates in a subdirectory are neither carried nor reported

`internal/capture/certificates.go:50` skips directories and never descends,
though `update-ca-certificates` trusts every `*.crt` below the directory. A
machine with `…/ca-certificates/corp/root.crt` yields `carried == 0 && left
== 0`: the authority is silently dropped, against "must be reported, not
silently dropped".

### C5. A trailing slash makes one repository into two

`internal/capture/sources.go:165`. The duplicate-copy identity is the raw
`uri + " " + suite`, while everything downstream trims the trailing slash. A
machine with the same repository in a `.list` (with slash) and a `.sources`
(without) yields `docker` and `docker-2`, two key files, and a recipe that
configures one repository twice. `recipe.Validate` only refuses duplicate
*names*, so it is written out.

### C6. A deb822 `Signed-By` naming two keyrings is dropped

`internal/capture/sources.go:119`. The one-line parser splits on a comma; the
deb822 parser passes the whole string to `pathInRoot`. apt accepts the
multi-path form, which is what a vendor's key-rotation instructions produce,
so a working signed repository is reported uncarriable with an error that
reads like a broken machine.

## Tier 7 — validation, vendor, and the release

### N1. `--mirror` does not reject `#`

`internal/cli/build.go:316` re-implements the source-URL rule with the
pre-`3c2db6a` character set, rejecting only whitespace.
`sourceURLMetacharacters` includes `[]#` precisely because apt comments to
the end of the line. `--mirror 'http://mirror.example/ubuntu#'` therefore
emits a deb line with no suite and no components.

### N2. `[python] index_url` with no packages can never be rebuilt offline

`internal/recipe/validate.go:108`. Validation checks the index URL in
isolation; `RenderPythonScript` returns "" for zero packages, so the online
build writes no `[python]` table, and `build --offline` then fails for ever
with advice ("run frostroot build online, then vendor") that cannot help,
because the online build writes the identical lock every time. Only a
hand-edited recipe reaches this shape — which is what validation is the gate
for.

### N3. Abandoning `vendor` leaves temporary files `--prune` will not remove

`internal/cli/vendor.go:118` with `internal/pool/fetch.go:277`. The second
Ctrl-C returns immediately and `main` calls `os.Exit`, so the `defer` that
removes a partial download never runs. `pool.Verify` counts only `.deb` and
`.whl` as extra, so the `.tmp` files accumulate across abandons — while the
message printed on that exact path says "the rest is removed".

### N4. The overwrite check is a `Stat` taken before the form

`internal/cli/init.go:92` against the unconditional rename at `:146` (same
shape at `capture.go:42`). A recipe that appears while the form is open — a
checkout, an editor, a second frostroot — is replaced silently, exit 0.

### N5. The released binary is built with a vulnerable toolchain

`go.mod:3` says `go 1.24.2`, and CI's `setup-go` with `go-version-file:
go.mod` installs exactly that (confirmed from a job log:
`/opt/hostedtoolcache/go/1.24.2/x64`). `govulncheck` against the *newer*
1.24.7 already reports 28 reachable standard-library vulnerabilities, in
`crypto/x509` (7), `crypto/tls` (6), `net/http` (4), `net/url` (3),
`archive/tar` (2), `encoding/asn1` (2) and `encoding/pem` (1) — precisely the
packages this project's security rests on. Twelve are fixed inside the 1.24
line (to 1.24.13); the rest need 1.25.x (to 1.25.13).

### N6. Two integration tests exist and CI has never run either

The job hard-codes `./internal/builder/`, but there are three
`//go:build integration` files:
`internal/form/catalog_integration_test.go` (every catalog package exists in
all three releases) and `internal/index/integration_test.go` (the real index
of all three releases) have never run in CI — **and the README claims both
do**, at lines 500 and 1056. Measured here: they pass, in 22 s and 5 s. This
is the same gap PR #48 closed for the builder, left half finished.

## Checked and found sound

Worth recording so it is not re-audited: `pool.download`'s status,
`Content-Length`, byte-count and digest checks before `os.Rename`, and its
removal of the temporary on every error path; the bound on decompressed bytes
there; no `Range` header anywhere, so a resume cannot concatenate; `Manifest`
name validation and `--prune` containment, both fuzzed; `pgp.ParsePublicKey`
fuzzed 45 s with no crash and no malformed key accepted; `pki.Transport`
never weakening `TLSClientConfig`; every template interpolation going through
`shellQuote`, across 64 rendered variants checked with `sh -n`; `set -eu`
holes looked for and found closed or deliberate; `SOURCE_DATE_EPOCH`, locale,
umask, hostname and `resolv.conf` handling; `prepareWorkRoot`'s ownership and
symlink checks; the progress parser against a real mmdebstrap `--verbose`
run; `internal/deb`'s stanza, index and ar bounds; and a 200×12-keystroke
fuzz of the picker and 2000-case fuzz of the summary across widths −5…200
that produced no panic.

## Suggested order

1. **T1, T2, T3** — the trust chain. T1 needs the catalog to pin GitHub's
   second key; T2 is the comparison `certificates.go:126` already models; T3
   needs the CA bundle to reach apt some way that is not `--aptopt`.
2. **A1, A2, A3** — a hang and two ways to lose a file someone put there.
3. **A4, A5** — bound the download; refuse a lock with no sources.
4. **V1–V6** — the v0.12 work, all small, all with a test that should have
   existed.
5. **F1** — the Python picker refusing valid names is the worst of the form
   defects; F2–F6 after it.
6. **C1–C6, N1–N4** — capture and validation.
7. **N5, N6** — the toolchain and the two tests CI never runs; both are
   changes to `go.mod` and one workflow line.
