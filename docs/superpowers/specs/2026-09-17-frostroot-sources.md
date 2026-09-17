# frostroot v0.5: extra apt sources

Date: 2026-09-17
Status: approved by the project owner in conversation ("plan and work for 2", the extra-apt-sources item of the roadmap); implemented on branch `feature/sources`
Extends: [`2026-09-14-frostroot-design.md`](2026-09-14-frostroot-design.md) (the extension point "Extra apt sources"), [`2026-09-17-frostroot-capture.md`](2026-09-17-frostroot-capture.md) (the report's third-party areas) and [`2026-09-17-frostroot-vendor.md`](2026-09-17-frostroot-vendor.md) (the lock and the pool)

## Verification (2026-09-17, build host)

- `gofmt`, `go vet` with and without the `integration` tag, `go test -race
  ./...`, `GOOS=windows go build ./...` and `golangci-lint`: clean.
- **Real binary against a local signed repository** (the spike's, served on
  `127.0.0.1:8099`, one package, a throwaway key): a 24.04 recipe with
  `curl`, the repository's package and one `[[sources]]` entry.
  `validate` reports one extra source. `build` in 126 s; the lock holds
  `[[repositories]]` with the URL, suite `noble`, `main` and the key's
  SHA-256, and the repository's package carries `source = 'spike'` while
  the other 238 carry none. The image's `sources.list` holds the archive's
  three lines and `deb [signed-by=/etc/apt/keyrings/frostroot-spike.gpg]
  http://127.0.0.1:8099 noble main`, and that keyring (239 bytes, binary)
  is in the image. `vendor` fetched all 239 files (82.7 MB, 35 s), the
  repository's from `127.0.0.1:8099`. `build --offline` in 73 s: "239
  packages, every one as locked", the lock unchanged, the same
  `sources.list` and keyring in the offline image.
- **`capture` of a fixture root** with a one-line source naming a
  `signed-by` key file: the recipe gets the source (named `127-0-0-1`, as
  neither a catalog entry nor a PPA), the key is saved armored under
  `keys/`, and `validate` accepts the result.
- **The Sources page under a pty**: `init` and `edit` render the two new
  fields and write the recipe with Enter through them.
- Real HTTPS sources (PPAs, Docker) could not be exercised from this build
  host, whose network intercepts TLS; their catalog entries were checked
  from the Windows side: every `InRelease` exists for the releases each
  entry claims, and every key's fingerprint was computed from the live key
  with gpg before being pinned. The mechanism the real run exercised is
  the same for every source.

## Why

A programming lab rarely lives on the Ubuntu archive alone. Python 3.12 on
22.04 comes from the deadsnakes PPA, a current git from the git-core PPA,
Docker from Docker's own repository, Node.js from NodeSource, a current
CMake from Kitware, clang from apt.llvm.org, VS Code from Microsoft. Today
a recipe cannot say any of that, `build` cannot install it, and `capture`
can only report that the machine had it. This version gives the recipe a
place for those sources, teaches `build` to install from them with their
signing keys, keeps the lock and `vendor` exact across sources, and lets
`capture` carry a machine's sources into the recipe instead of only naming
them.

It stays apt, and it stays one locker: a package from a PPA is a `.deb`
with a checksum like any other, listed in the same `[[packages]]`, vendored
into the same pool, rebuilt by the same offline build.

## Goal

```toml
[[sources]]
name = "docker"
url = "https://download.docker.com/linux/ubuntu"
suite = "noble"             # optional; defaults to the release's suite
components = ["stable"]     # optional; defaults to ["main"]
key = "keys/docker.asc"     # the source's OpenPGP public key, relative to the recipe directory
```

- `init` and `edit` gain a **Sources** page: a catalog of well-known sources
  to pick with Space, and a line for PPAs as `owner/name`. The signing keys
  are fetched when the recipe is written, checked against a pinned
  fingerprint, and saved under `keys/`.
- `validate` checks every source, including that its key file exists and is
  an OpenPGP public key.
- `build` installs from the archive and the sources together, each source
  verified by its own key and nothing else, and records in the lock which
  source every package came from.
- `vendor` fetches each package from its own source; `build --offline`
  rebuilds from the pool as before.
- `capture` turns the machine's third-party sources that have a signing key
  into `[[sources]]` entries with their keys, and reports the ones it could
  not carry.

## Non-goals

- Flat repositories (`deb URL ./`) and sources without a signing key
  (`[trusted=yes]`, legacy `apt-key` keyrings). Every source has a suite,
  components and a key. Capture reports what does not fit.
- `deb-src` lines, apt pinning and preferences, per-source architectures.
- Verifying that a source publishes the chosen suite before the build:
  that is the archive's `apt-get update`, and its error names the URL.
- Snap, pip, npm and the rest. Still apt only.
- Trusting a key on someone's say-so. A catalog entry pins the key's
  fingerprint; a PPA's fingerprint comes from Launchpad's API over HTTPS;
  a custom source's key is a file the user placed and committed. There is
  no `key_url` in the recipe that build fetches blindly.

## The recipe

`[[sources]]` is a list of tables, each:

| Field | Rule | Default |
|---|---|---|
| `name` | `^[a-z0-9][a-z0-9-]*$`, at most 32 characters, unique; names the keyring in the image (`/etc/apt/keyrings/frostroot-<name>.gpg`) and the source in the lock | required |
| `url` | `http://` or `https://`, a host, no whitespace; a trailing slash is removed | required |
| `suite` | `^[A-Za-z0-9][A-Za-z0-9._-]*$` | the release's code name (`noble`) |
| `components` | each `^[a-z0-9][a-z0-9.+-]*$` | `["main"]` |
| `key` | a relative path inside the recipe directory, no `..`; the file is an OpenPGP public key, ASCII-armored or binary | required |

`validate` needs the recipe directory to check `key`, so
`recipe.Validate` gains a sibling, `recipe.CheckSourceKeys(recipeDir,
sources)`, that every command loading a recipe runs too. Everything else
about the recipe is unchanged; a recipe without `[[sources]]` is exactly
what it was.

## Keys

`internal/pgp` is a small OpenPGP reader with no policy: it accepts a key
file armored or binary, returns the binary form, and computes the primary
key's fingerprint (version 4 keys, SHA-1 over the key packet, which is what
every Ubuntu, Launchpad and vendor key in use is; other versions are an
error naming the version). It rejects anything whose first packet is not a
public key, which is what a saved HTML error page looks like. No OpenPGP
library: armor is base64 with a checksum line, and the fingerprint is one
hash over one packet.

Keys are stored in the recipe directory under `keys/`, in the format they
arrived in: `keys/<name>.asc` for an armored key (a text file people can
diff), `keys/<name>.gpg` for a binary one. A key file may hold several
keys, as GitHub's does; the fingerprint pinned and checked is the first
primary key's. During the build and inside the image the key is always
binary: apt on focal cannot read an armored `signed-by` file, and binary
works everywhere.

## The Sources page

The catalog, in `internal/sources`, lists sources with their URL, suite
(with `{suite}` standing for the release's code name), components, key URL
and the key's fingerprint, pinned. Entries are chosen for a programming lab
and checked, before this version ships, to publish an `InRelease` for
20.04, 22.04 and 24.04 where the entry claims to:

| Name | What | URL |
|---|---|---|
| `deadsnakes` | newer and older Python versions (PPA) | `https://ppa.launchpadcontent.net/deadsnakes/ppa/ubuntu` |
| `git-core` | the current git (PPA) | `https://ppa.launchpadcontent.net/git-core/ppa/ubuntu` |
| `docker` | Docker Engine and Compose | `https://download.docker.com/linux/ubuntu`, component `stable` |
| `nodesource` | Node.js 22 | `https://deb.nodesource.com/node_22.x`, suite `nodistro` |
| `github-cli` | the `gh` command | `https://cli.github.com/packages`, suite `stable` |
| `kitware` | the current CMake | `https://apt.kitware.com/ubuntu` |
| `llvm` | the current clang and lld | `https://apt.llvm.org/{suite}`, suite `llvm-toolchain-{suite}` |
| `vscode` | Visual Studio Code | `https://packages.microsoft.com/repos/code`, suite `stable` |

The page has two fields, like the Packages page: a multi-select over the
catalog (`sources`) and an input for other PPAs as `owner/name`, separated
by spaces (`ppas`). A PPA resolves to `name = "ppa-<owner>-<name>"`,
`url = https://ppa.launchpadcontent.net/<owner>/<name>/ubuntu`, the
release's suite and `main`. Sources in a recipe that are neither catalog
entries nor PPAs (hand-written ones) are kept as they are, in order, the
way `edit` keeps packages it does not know.

When the recipe is written, every source whose key file does not exist yet
gets one: the catalog's key URL, or for a PPA Launchpad's API for the
fingerprint (`https://api.launchpad.net/1.0/~<owner>/+archive/ubuntu/<name>`,
field `signing_key_fingerprint`) and the Ubuntu keyserver for the key. The
fetched key's fingerprint must equal the pinned or published one, or the
key is not written and the command fails. The recipe itself is written
first, so a network failure leaves a recipe that `validate` will refuse
until the keys are there, and `edit` tries again; the message says both.
Keys that already exist are never refetched or overwritten.

Without a terminal the page is asked line by line like the others.

## Building with sources

`distro.Release.SourceLines` stays the only place the archive's three lines
are built. `builder.SourceLines(release, mirror, sources, keyringDir)`
appends one line per source, with the key's path in `keyringDir`:

```
deb [signed-by=<keyringDir>/frostroot-docker.gpg] https://download.docker.com/linux/ubuntu noble stable
```

The spike (below) showed that apt, run by mmdebstrap from the host during
the bootstrap, resolves `signed-by` on the **host**, not under the chroot:
a key uploaded into the chroot is not found, a host path is. So the line
mmdebstrap gets names the binary key in the stage directory,
`<work>/stage/keys/docker.gpg`. That path would then be what mmdebstrap
writes into the image's `sources.list`, useless there, so two customize
hooks fix the image up, both mechanisms from v0.4: `upload` of each key to
`/etc/apt/keyrings/frostroot-<name>.gpg`, and `upload` of a rendered
`/etc/apt/sources.list` whose lines name those paths. Every build renders
that file now, not only the offline one, and the lock's `sources` records
the image's lines. `apt update` in the image then works with the same
trust the build had. `--mirror` replaces the archive URL only.

HTTPS sources are fetched by apt on the build host, so they need the host's
CA certificates, exactly as `curl` would; on a network with a TLS
inspection proxy the host has to trust that proxy. The README's existing
note on such networks gets this sentence.

### The lock

```toml
[[repositories]]
name = 'docker'
url = 'https://download.docker.com/linux/ubuntu'
suite = 'noble'
components = ['stable']
key_sha256 = '…'

[[packages]]
name = 'docker-ce'
…
source = 'docker'
```

`sources` keeps every `deb` line actually used, extras included.
`[[repositories]]` records each extra source and the SHA-256 of the key
file used, so a changed key shows in the diff. `source` on a package names
the repository the package's file is relative to; it is absent for the
archive. The format stays version 1.

The checksums are recorded as in v0.4, from the indexes in the chroot's
`/var/lib/apt/lists`. Each index is attributed to a source by apt's own
naming: the URL without its scheme, slashes turned into underscores,
followed by `_dists_<suite>_`: `download.docker.com_linux_ubuntu_dists_noble_stable_binary-amd64_Packages`
is Docker's. The archive's indexes match its mirror and the three pockets.
An index that matches nothing fails the build: a package would otherwise
get a checksum with no known origin. A package listed by more than one
source with the same checksum keeps the archive's, or the first source's,
file name; different checksums under one name and version are an error, as
before.

### Vendor and offline

`pool.Manifest` gives every entry a base URL: the lock's mirror (or
`--mirror`) for archive packages, the repository's URL for the others. The
Launchpad fallback applies to the archive and to PPAs
(`https://launchpad.net/~<owner>/+archive/ubuntu/<name>/+files/<basename>`);
other sources have none.

`build --offline` compares the recipe's sources (name, URL, suite,
components) with the lock's repositories as it compares the packages, and
refuses on a difference. It installs from the pool as before and uploads
the recipe's keys into the image, so `apt update` there works; the lock's
source lines already carry the `signed-by` paths.

## Capture

Capture reads `/etc/apt/sources.list` and `sources.list.d/*.list` (one-line
format, with `[options]`) and `sources.list.d/*.sources` (deb822: `Types`,
`URIs`, `Suites`, `Components`, `Signed-By`, `Enabled`). `deb-src` entries
install nothing and are ignored. For every `deb` entry that is not the
Ubuntu archive, has a suite that is not `./`, and names a `Signed-By` that
is a readable key file or an inline armored key, it produces one
`[[sources]]` entry per URL and suite: the catalog's name when the URL and
suite match an entry, `ppa-<owner>-<name>` for a PPA, otherwise the host
with dots turned into dashes (`download-docker-com`), made unique. The key
is read and written beside the recipe as `keys/<name>.asc`.

This is the one file capture copies, and it is a public key. The README's
rule becomes "it copies nothing but apt signing keys, which are public".
Everything else holds: no home directory content, no `/etc` files, no
secrets.

The carried sources appear in the report's captured section, each with
the file and the key it came from. The "Third-party apt sources" area
lists what was not carried and why (no `signed-by` key, a flat
repository, an unreadable key). "Packages from third-party sources" keeps
only packages whose source was not carried: the others are now ordinary
requested packages that `build` will find.

## Errors

| Situation | Exit | Message says |
|---|---|---|
| invalid source field (`validate`, and every command loading the recipe) | 1 | the field and the rule |
| key file missing or not an OpenPGP public key | 1 | the path, and for a catalog source or PPA that `frostroot edit` fetches it |
| key fetch fails or the fingerprint differs (`init`, `edit`, `capture`) | 1 | the source, the URL, both fingerprints when they differ; the recipe was written; `edit` retries |
| a source's `InRelease` cannot be fetched or verified (`build`) | 2 | apt's own message, which names the URL, in the mmdebstrap tail as today |
| an index matches no source (`build`) | 2 | the index file name |
| recipe sources differ from the lock's repositories (`--offline`) | 1 | what differs; build online, then vendor |

## Testing

Default suite, offline:

- `pgp`: armored and binary keys (a real Launchpad key and a real vendor
  key as fixtures, both public), fingerprints against the published
  values, a wrong checksum line, an HTML page, an empty file, a v3 key
  header.
- `recipe`: source validation table; `CheckSourceKeys` with a missing
  file, a non-key file, a path with `..`, a good key; a recipe with
  sources round-trips through `Save`/`Load`.
- `sources`: catalog entries resolve for each release; PPA resolution;
  key fetching against `httptest` with a matching and a mismatching
  fingerprint, a Launchpad API answer, and a failing server.
- `form`: the Sources page fields; `FromRecipe`/`ToRecipe` keep custom
  sources and map catalog and PPA entries both ways; the summary names the
  sources.
- `builder`: source lines and setup hooks; the stage holds binary keys;
  checksums attributed by index name for the archive and two sources; an
  unattributed index fails; the lock's repositories and `source` fields;
  offline refusal on a changed source.
- `pool`: manifest base URLs per source; the PPA fallback URL.
- `capture`: fixtures with a one-line PPA source with `signed-by`, a
  deb822 Docker source, a deb822 source with an inline key, a `trusted.gpg.d`
  source and a flat repository; the recipe gets the first three, the
  report explains the last two; packages from a carried source leave the
  third-party finding.
- `cli`: `init --plain` selecting a catalog source against a fake key
  client writes the recipe and the key; a fingerprint mismatch writes the
  recipe, not the key, and exits 1; `validate` on a recipe with a missing
  key; the recipe template renders sources with comments.

On the build host: the spike below, then a real `build` of a recipe with a
local signed repository as an extra source (this network intercepts TLS,
so real HTTPS sources cannot be fetched from the build host; the mechanism
is the same), the lock's `[[repositories]]` and `source` fields, `vendor`
from two origins, `build --offline`, and `capture` of a fixture root with
sources. The catalog's URLs, suites and fingerprints are checked from the
Windows side, which trusts this network's proxy, against the live
repositories and Launchpad.

## Spike results (2026-09-17, build host, mmdebstrap 1.4.3)

A throwaway ed25519 key, one tiny package, a repository built with
`apt-ftparchive` and `gpg --clearsign`, served on `127.0.0.1:8099`, added
to a noble bootstrap as a second source with `--include` of its package.

- **Key uploaded into the chroot by setup hooks, `signed-by` naming the
  chroot path:** fails in nine seconds at `apt-get update` with
  `NO_PUBKEY`: apt looked for `/etc/apt/keyrings/frostroot-spike.gpg` on
  the host. The setup hooks themselves ran (the directory was created).
- **`signed-by` naming the key's host path, nothing uploaded:** succeeds in
  121 seconds; the package is installed; the image's `sources.list` holds
  the host path verbatim, and `/etc/apt/keyrings/` in the image is empty.
  Hence the fix-up hooks above.
- Ubuntu's `apt` (2.8 on the host) read the binary key; the image side
  always gets binary, since focal's apt cannot read armored `signed-by`
  files.
- Catalog keys, fetched from the Windows side and fingerprinted with gpg:
  all OpenPGP version 4; six armored, GitHub's binary and holding two
  primary keys (the first one's fingerprint is pinned; apt accepts either).
  Launchpad's API fingerprints for the two PPAs equal the keys the keyserver
  returned. Every catalog URL published an `InRelease` for each release it
  claims.

## Open questions

None. The spike settled the mechanics, and the catalog's fingerprints were
verified against the live keys before being written down.
