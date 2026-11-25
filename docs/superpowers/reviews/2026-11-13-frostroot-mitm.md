# frostroot man-in-the-middle analysis, 2026-11-13

Date: 2026-11-13
Scope: every network read `master` makes at `41c8cdd`, by frostroot itself
and by the tools it drives (apt under mmdebstrap, pip, dnf5 under mkosi)
Method: one reader, the code only. Nothing was built and nothing was
intercepted, so no finding here was demonstrated; each names the lines it
was read from. One fact was fetched: the headers of Ubuntu's
`noble-security` `InRelease`, over http and https.
Status: findings only. No code was changed. The five findings are filed as
#162 through #166.

## The three attackers

- **On the path.** Reads and alters traffic between the build host and a
  server, and holds no certificate the host trusts.
- **A hostile or compromised mirror.** Serves whatever it likes at the URL
  the build was pointed to, with a valid certificate for that name, and
  holds no signing key.
- **A TLS-inspecting proxy.** Its authority is trusted on purpose, through
  `--ca-bundle` or the recipe's `[certificates]`, so it can answer as any
  https host. `--insecure` gives the first attacker the same power.

## Verdict

**Nobody in these three positions can put a package of their own into an
image through apt, dnf or `vendor`.** What they can do is narrower: choose
*which genuine packages* a build freezes, and, where TLS is the only check,
choose a PPA's key or a Python wheel. Five findings, one of them medium.

| | On the path | Hostile mirror | Inspecting proxy / `--insecure` |
|---|---|---|---|
| Alter a `.deb` or `.rpm` | no: signed | no: signed | no: signed |
| Serve stale signed packages | **yes, by default** (#162) | **yes** (#162) | **yes** (#162) |
| Alter a vendored file | no: lock's SHA-256 | no | no |
| Substitute a catalog source's key | no: pinned | no | no: pinned |
| Substitute a PPA's key | no | n/a | **yes**, warned only under `--insecure` (#163) |
| Choose what pip resolves | no | the index always does | **yes**, by design; recorded only under `--insecure` (#165) |
| Touch an offline build | no network | no network | no network |

## Findings

### M1. A build can be frozen at stale Ubuntu packages (#162, medium)

The default archive is `http://archive.ubuntu.com/ubuntu`
(`internal/distro/ubuntu.go:28`). apt verifies `InRelease` against the
host's `ubuntu-archive-keyring.gpg`, which mmdebstrap is given with
`--keyring` (`internal/builder/bootstrap.go:197`), so content cannot be
changed. Ubuntu's `InRelease` has no `Valid-Until`, though, and no code in
the module checks a `Date`, so an old, correctly signed `InRelease` for
`-updates` and `-security` is accepted. The build then installs genuine
packages with known holes.

This matters more here than for an ordinary apt user. The stale versions go
into `frostroot.lock`, `vendor` fetches exactly them, every offline rebuild
reproduces them, and the image goes to a whole lab. The product's promise
is that the frozen thing is the thing you meant.

A stale or hostile `--mirror` does the same over any scheme. So does any
https source under `--insecure`, and so does Fedora there: `repomd.xml` is
unsigned and pinned only by the metalink, which `sslverify=0`
(`internal/builder/fedoraconfig.go:100`) leaves unverified. Without
`--insecure`, Fedora's metalink over https bounds this to the few recent
`repomd.xml` checksums it lists.

The README's "fetched over plain HTTP with signed metadata, so it is never
affected" is true of tampering and silent about replay.

Fix: fetch the default archive over https while building, and check each
pocket's `InRelease` date against the clock. The second catches a stale
mirror, which the first cannot.

### L1. A PPA's key is trusted on first fetch (#163, low)

`locateKey` (`internal/sources/fetch.go:148`) asks Launchpad for a PPA's
fingerprint and the keyserver for the key, over one client. A catalog entry
is checked against compiled-in fingerprints; a PPA outside it has only the
connection. An inspecting proxy, or anyone under `--insecure`, picks the
key, which is saved, trusted by every later build and installed as the
source's keyring. The compare-with-Launchpad warning is printed only for
`--insecure` (`internal/cli/recipeform.go:339`).

Fix: warn whenever the fingerprint was discovered over a connection that
trusted more than the host's own roots.

### L2. Redirects from https to http are followed (#164, low)

No client sets `CheckRedirect` (`sources/fetch.go:58`, `pool/fetch.go:201`,
`index/fetch.go:125`, `index/pypi.go:234`, `index/summaries.go:54`).
vendor's SHA-256 check, the catalog's pins and "the index only suggests"
bound the result everywhere but PPA key discovery, and Launchpad and the
keyserver do not redirect to http today. A hardening.

Fix: one redirect policy beside `pki.Transport` that refuses the downgrade.

### L3. A changed checksum for the same file replaces the locked one silently (#165, low)

An online resolve trusts TLS alone, since PyPI signs nothing. A second
online build reads the previous lock only to restore it on failure
(`internal/builder/builder.go:423`). A wheel file name is immutable on
PyPI, so the same name with another SHA-256 than last time is a sign of
tampering by the index, a proxy or the path, and it passes unremarked.

Fix: compare the new lock with the old before placing it, and refuse a
same-name, different-hash file.

### L4. The TLS 1.2 floor is Go's on the default path (#166, low)

`pki.Transport` sets `MinVersion` only `if pool != nil || insecure`
(`internal/pki/pki.go:142`), and an ordinary run passes a nil pool. Go's
own client floor is 1.2, so nothing is weaker; the rule AGENTS.md states is
simply not what the function does.

Fix: always set the configuration.

## Not filed

- **The recipe's `[certificates]` are trusted for every host.** They join
  the pool for frostroot's own fetches (`trustPool`,
  `internal/cli/app.go:45`) and reach apt and pip. A recipe taken from
  someone else carries their authority into key discovery and the Python
  resolve. This is what the feature is for; a reviewer of a recipe should
  read its certificate files as they would its sources. Worth a sentence in
  the README when #163 is done.
- **The package indexes are unsigned.** Already stated in the README and
  left out of #147 on purpose. A forged index costs a wrong suggestion.
- **`--insecure` on pip.** `--trusted-host` covers the index's host and
  `files.pythonhosted.org`; the lock records `transport = unverified` and
  `vendor` warns. Sound as designed.

## Checked and found sound

Worth recording so it is not read again:

- Every HTTP client is built from `pki.Transport`; `InsecureSkipVerify`
  appears there and nowhere else; extra authorities are appended to the
  system pool, never substituted; the proxy environment is kept.
- `pool.download` checks size and SHA-256 before the rename, from the
  mirror and from each fallback (Launchpad, PPA files, Koji), so the
  fallback's host is trusted for nothing.
- A lock's wheel URL must be https and without credentials
  (`WheelManifest`); a recipe's `index_url` must be https.
- `FetchKey` checks every primary key in a fetched file against the pins.
- apt's build-only trust settings, `CaInfo` and `Verify-Peer`, go in by a
  setup hook and out by the last customize hook, so an image never ships
  with verification off. `[trusted=yes]` is written only for the `copy://`
  repository of an offline build.
- Fedora packages are checked with `gpgcheck=1` against a key embedded in
  the binary, and `--mirror` is refused for Fedora, so no flag replaces the
  metalink.
- The pinned pip is installed by URL with `--require-hashes`, and every
  `PIP_*` variable and certificate path variable is dropped first.
- An offline build opens no connection.
- CI's actions are pinned by commit, and a release is attested.

## Suggested order

1. **#162**: the only finding that changes what an image holds on a
   default run. It needs one decision: whether the image's own source lines
   become https with the build's.
2. **#165**: small, and the only check PyPI packages would get beyond TLS.
3. **#163, #164, #166**: each a few lines and one test.
