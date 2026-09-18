# frostroot v0.8 Spec: certificate authorities

What a build and an image need in order to work on a network that intercepts
HTTPS. Written before the code, from the plan
(`plans/2026-09-18-frostroot-certificates.md`), and updated as the code
teaches.

## The problem

A corporate network terminates TLS at a proxy and re-signs every response
with its own certificate authority. Nothing that verifies against the public
roots alone can talk to anything. For frostroot that is four separate
questions, and only two of them are broken.

**Not broken.** Ubuntu `.deb`s come over plain HTTP from `archive.ubuntu.com`
with signed metadata, so interception cannot touch them, and both Go clients
(`internal/pool/fetch.go`, `internal/sources/fetch.go`) already inherit
`http.DefaultTransport`'s `ProxyFromEnvironment`.

**Broken, on the host.** frostroot's own HTTPS fetches — signing keys, PPA
`.deb`s from Launchpad, wheels during `vendor` — verify against the host's
root store, and there is no way to point frostroot at a bundle instead. A
host with the organisation's CA installed works by accident; nothing says so.

**Broken, in the chroot.** The Python step runs pip inside the image being
built, which has only Ubuntu's `ca-certificates` — and pip does not use the
system store at all. It verifies against its vendored certifi unless given
`--cert`. `internal/builder/python.go` then unsets every `PIP_*` variable so
that the build host's index cannot steer what a recipe resolves to, which
also removes `PIP_CERT`. `https_proxy` survives the sweep, so the proxy is
reachable; only the trust anchor is missing. A first online build of a recipe
with `[python]` cannot succeed behind a proxy.

**Broken, in the image.** The imported distribution trusts the public roots
only, so `git clone https://…`, `curl` and `pip` fail inside it.

## The design

Two inputs, because they answer two different questions.

### `[certificates]`: trust this, and ship it

```toml
[certificates]
include = ["certs/corp-root.pem"]
```

Paths are relative to the recipe directory, under the rule `Source.Key`
already obeys: relative, inside the directory, no backslash or NUL. Each file
is read and split into one file per CERTIFICATE block, because Debian's
`update-ca-certificates` hashes the first certificate of a file and would
silently ignore the rest of a bundle. They are installed into
`/usr/local/share/ca-certificates/` and `update-ca-certificates` runs in the
provision script. The lock records each file's name, path and SHA-256, so an
offline rebuild refuses a certificate that changed underneath it.

These are trusted **during the build too**: a recipe that ships a CA is a
recipe built in the environment that CA belongs to.

### `--ca-bundle PATH`: trust this, for this build

Never reaches the image, the lock or the tarball. A build with it and a build
without it produce the same bytes. It exists because trusting a proxy in
order to fetch is not the same decision as shipping its CA to everyone who
imports the image.

### Why this costs no integrity

Wheels install under `--require-hashes` against the lock's SHA-256 and
`.deb`s are checksummed against the archive's indexes. An intercepting proxy
that altered a byte fails the build. The CA buys transport, not trust in the
artifact.

### Where the trust is applied

| Path | Mechanism |
|------|-----------|
| Go HTTPS fetches | `x509.SystemCertPool()` plus the bundle, on the transport |
| pip in the chroot | image bundle + extra certificates in a temp file, `pip --cert`, deleted by the step |
| the image | `update-ca-certificates` in the provision script |

## What the spike found

All four questions ran on the build host (Ubuntu 24.04, WSL) against a
private CA generated for the test, and a wheel index served over HTTPS with a
certificate signed by it.

**`update-ca-certificates` is deterministic.** Three rounds, each a fresh
extraction of the same image tarball with the same certificate added, in a
user namespace: `/etc/ssl/certs/ca-certificates.crt` came out byte-identical
all three times (`36f65f96…`), and so did the whole directory — 246 entries,
same names, same symlink targets. So the install is `update-ca-certificates`,
not a hand-rolled write-the-file-and-make-the-link, which was the fallback
the plan reserved.

Two details worth keeping: the certificate lands as
`/etc/ssl/certs/<name>.pem` with a subject-hash symlink beside it and the
bundle goes from 121 certificates to 122, and `/etc/ca-certificates.conf` is
**not** touched, because it lists only `/usr/share/ca-certificates`. That
matters for `capture`, whose ignore list already covers `/etc/ssl/certs/*`
and `/etc/ca-certificates.conf`.

**pip needs `--cert`, and `--cert` is enough.** Without it, pip refused the
private index with an `SSLError`. With `--cert ca.pem` it downloaded the
wheel. This is the mechanism the whole feature rests on and it behaves.

**The concatenated bundle covers both worlds.** `/etc/ssl/certs/ca-certificates.crt`
followed by the extra certificate verified the private index *and*
`https://pypi.org`, so a partially intercepted network — some hosts bypassed
— still works. 122 certificates in the file.

**A Go pool keeps its system roots.** `x509.SystemCertPool()` then
`AppendCertsFromPEM` reached both the private host and `pypi.org`, each
`200 OK`.

**An aside that shaped the test, not the design.** urllib3 leaves
`server_hostname` empty for a bare IP literal, and python's `ssl` then
refuses before it ever looks at a certificate. The first run of the spike
"failed" for that reason and said nothing about trust. The test certificate
now names `localhost`. Worth remembering when writing the integration test.

## Verification

This branch is not done until, on the build host:

1. A recipe with a certificate builds, the image's
   `/etc/ssl/certs/ca-certificates.crt` contains it, and `openssl verify`
   against it succeeds inside the imported distribution.
2. Two builds of that recipe are byte-identical.
3. A build with `--ca-bundle` and a build without it produce the identical
   tarball, and the lock is unchanged by the flag.
4. The Python path works behind a private CA: an HTTPS wheel index the public
   roots do not know, `--ca-bundle`, and an offline rebuild that matches.

## Out of scope, and why

- **An internal package index.** The `PIP_*` sweep is what keeps a lock
  dependent on the recipe rather than on whoever's shell ran the build. A
  network that blocks `pypi.org` outright needs `[python] index_url` in the
  recipe, recorded in the lock: a real feature, a separate one.
- **`capture` reporting certificates.** `/usr/local/share/ca-certificates` is
  not under `/etc`, so a captured image will not round-trip its CA into the
  recipe. Small follow-up.
- Client certificates for mutual TLS, and per-source trust.
