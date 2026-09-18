# frostroot v0.8 Implementation Plan: certificate authorities

Makes frostroot work on a corporate network that intercepts HTTPS. Branch
`feature/certificates`, from `master` at 5132b88. Every task ends with
`gofmt`, `go vet` (both tags), `go test -race ./...`, `GOOS=windows go build
./...` and `golangci-lint run` clean, and its own commit as `alone141`
following `CONTRIBUTING.md`.

**Goal:** a build succeeds behind a TLS inspection proxy, and the image it
produces trusts the organisation's certificate authority, without either one
changing the bytes of a rebuild done anywhere else.

## What is broken today

Verified by reading the network paths, not assumed:

1. **The archive is fine.** `.deb`s come over plain HTTP from
   `archive.ubuntu.com` with signed metadata, and both Go clients
   (`internal/pool/fetch.go:167`, `internal/sources/fetch.go:48`) inherit
   `http.DefaultTransport`'s `ProxyFromEnvironment`. A proxy that is merely in
   the path costs nothing.
2. **frostroot's own HTTPS fetches** — PPA and vendor signing keys, PPA `.deb`s
   from Launchpad, wheels during `vendor` — verify against the host's root
   store. They work if the organisation's CA is installed on the build host,
   and there is no way to point frostroot at a bundle instead.
3. **The Python step fails.** pip runs inside the chroot, which has only
   Ubuntu's `ca-certificates`, and pip does not use the system store at all:
   it verifies against its vendored certifi unless told otherwise with
   `--cert`. `internal/builder/python.go:110` then deliberately unsets every
   `PIP_*` variable, so `PIP_CERT` is stripped by design. `https_proxy`
   survives the sweep, so the proxy is reachable; only the trust anchor is
   missing. A first online build with `[python]` cannot succeed.
4. **The image trusts nobody but the public CAs.** `git clone https://…`,
   `curl` and `pip` inside the imported distribution fail until someone runs
   `update-ca-certificates` by hand, which `README.md:607` documents as a
   limitation rather than solving.

## The design

**Intent in the recipe, fact in the lock**, as everywhere else. Two separate
inputs, because they answer two different questions.

```toml
[certificates]
include = ["certs/corp-root.pem"]
```

Paths are relative to the recipe directory and obey the rule `Source.Key`
already obeys. Each file is read, parsed as PEM, and every CERTIFICATE block
in it becomes one file in `/usr/local/share/ca-certificates/`, because
Debian's `update-ca-certificates` hashes the first certificate of a file and
would silently ignore the rest of a bundle. The lock records each file's name,
path and SHA-256, so an offline rebuild refuses a certificate that changed
under it. These certificates are baked into the image **and** trusted during
the build.

`build --ca-bundle PATH` is the second input: trust this for this build only.
It never reaches the image, the lock, or the tarball — a rebuild with it and a
rebuild without it must produce the same bytes. It exists because trusting a
proxy in order to fetch is not the same decision as shipping its CA to
everyone who imports the image.

**Trusting the proxy does not weaken anything.** Wheels install under
`--require-hashes` against the lock's SHA-256 and `.deb`s are checksummed, so
an intercepting proxy that altered a byte fails the build. The CA buys
transport, not trust in the artifact.

**Where the trust is applied:**

| Path | Mechanism |
|------|-----------|
| Go HTTPS fetches (keys, Launchpad, wheels) | `x509.CertPool`: system roots plus the bundle, on the transport |
| pip inside the chroot | a bundle uploaded to a temp path, `pip --cert`, deleted by the step |
| the image | `update-ca-certificates` in the provision script |

The chroot bundle is the image's `/etc/ssl/certs/ca-certificates.crt`
concatenated with the extra certificates, so a partially intercepted network
(some hosts bypassed) still verifies.

## Task 0: spike on the build host (blocker)

Nothing is coded until these four answer. Tasks 2-4 are the only ones that
could start early, and they are cheap to redo.

- **Is `update-ca-certificates` deterministic?** Build one image with a
  certificate twice and compare the tarballs. It writes hash-named symlinks
  into `/etc/ssl/certs` and regenerates `ca-certificates.crt`; if the
  concatenation order or the symlink set moves between runs, the install has
  to be done by hand instead (write the file, make the symlink, append to the
  bundle) and that changes Task 5.
- **Does `pip --cert` actually work against an intercepted index?** Serve a
  wheel index over HTTPS with a private CA, install from it with `--cert`
  naming that CA, and confirm it fails without. This is the mechanism the
  whole feature rests on.
- **Does the concatenated bundle still verify the public web?** Same run,
  fetch a real `https://` URL with the concatenated file.
- **Does an extra root in a Go `CertPool` leave the system roots working?**
  `x509.SystemCertPool()` then `AppendCertsFromPEM`, against a real host and
  the private one.

Record what happened in the spec. A spike that disagrees with the design above
changes the design, not the record.

## Task 1: the spec

`docs/superpowers/specs/2026-09-18-frostroot-certificates.md`: the four
breakages, the design, the spike results, and the verification this branch
will have to pass. Written before the code, updated as the code teaches.

## Task 2: the recipe table

`internal/recipe`: `Certificates *Certificates` with `Include []string`, a
pointer so a recipe without one stays as written. `CertificatePaths()` as the
single reader, like `PythonPackages()`. Validation reuses the key-path rule
(relative, inside the recipe directory, no backslashes or NUL); duplicate
paths, and paths whose base names would collide in
`/usr/local/share/ca-certificates`, are errors. `CheckCertificateFiles`
alongside `CheckSourceKeys`: exists, parses as PEM, holds at least one
CERTIFICATE block, holds no private key.

Table-driven tests, including a `.pem` that is really a key and a bundle of
three certificates.

## Task 3: the lock

`LockCertificate{Name, Path, SHA256}` and `Lockfile.Certificates`, written by
an online build, checked by an offline one: a file whose digest no longer
matches the lock fails the build naming the file. Same shape as
`LockRepository.KeySHA256`.

## Task 4: reading and splitting

`internal/builder/certificates.go`: read each recipe path, split it into one
`*x509.Certificate` per block, render each back to PEM deterministically, and
name them `<file base name>.crt` or `<file base name>-<n>.crt` for a bundle.
Assemble the build-time bundle (recipe certificates plus `--ca-bundle`) once,
and expose the `*x509.CertPool` the Go clients use. Pure Go, fully unit
tested, no mmdebstrap.

## Task 5: into the image

`Stage.CertificateDir`/`CertNames` and the hooks that upload them, mirroring
`KeyringDir`/`KeyNames`. The provision script runs `update-ca-certificates`
after the certificates are in place and before anything else needs TLS, and
fails the build if the certificate it just installed is not in
`/etc/ssl/certs/ca-certificates.crt`. Hook-order test, as for the Python step.

## Task 6: pip

`PythonOptions.CACertPath`; the step concatenates the image's bundle with the
uploaded certificates into a temp file, passes `--cert` to every online pip
invocation (the pinned pip and the install), and deletes the file. Offline
installs touch no network and take no `--cert`. Tests assert the flag is
present online, absent offline, and shell-quoted.

## Task 7: the Go clients and the flag

`--ca-bundle PATH` on `build` and `vendor`, plumbed into `pool.Fetcher` and
`sources.HTTPClient` as a transport with the extended pool. A `httptest` TLS
server with its own CA proves the fetch fails without the bundle and succeeds
with it.

## Task 8: docs

`README.md`: the recipe table, the commands table, "What is in the image", and
a rewritten "Networks that inspect TLS" that says what to do instead of what
does not work. The `--ca-bundle` flag in the usage text.

## Task 9: real images

On the build host, with the release matrix the Python work used:

1. A recipe with a certificate builds; the image's
   `/etc/ssl/certs/ca-certificates.crt` contains it and `openssl verify`
   against it succeeds inside the imported distribution.
2. Two builds of that recipe are byte-identical.
3. A build with `--ca-bundle` and a build without produce the identical
   tarball, and the lock is unchanged by the flag.
4. The full Python path behind a private CA: an HTTPS wheel index with a
   certificate the public roots do not know, `--ca-bundle`, a lock whose URLs
   are PyPI's, and an offline rebuild that matches.

## Deliberately out of scope

- **An internal package index** (`PIP_INDEX_URL` pointing at Artifactory or
  Nexus). The sweep in `python.go` that ignores it is what keeps a lock
  dependent on the recipe rather than on whoever's shell ran the build. A
  corporate network that blocks `pypi.org` outright needs `[python] index_url`
  in the recipe, recorded in the lock — a real feature, a separate one, and
  worth doing next if this one lands.
- **`capture` reporting certificates.** An image captured with a corporate CA
  will not round-trip it into the recipe: `/etc/ssl/certs/*` is on capture's
  ignore list and `/usr/local/share/ca-certificates` is not under `/etc`.
  Small follow-up, not this branch.
- Client certificates for mutual TLS, and per-source trust.
