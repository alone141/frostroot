# frostroot v0.13 Implementation Plan: --insecure

A flag that skips TLS certificate verification on every fetch, for a network
whose certificate authority the person building does not have. Branch
`feature/insecure`, from `master` at 8768524. Every task ends with `gofmt`,
`go vet` (both tags), `go test -race ./...`, `GOOS=windows go build ./...` and
`golangci-lint run` clean, and its own commit as `alone141` following
`CONTRIBUTING.md`.

**Goal:** `frostroot build --insecure` works behind a proxy that re-signs TLS
without anyone obtaining its root certificate first, and so do `vendor`,
`init`, `edit` and `capture`. The person is told, in the places where it
matters, what the flag gives up, and the lock says when its Python packages
were resolved that way.

## The decision this records

Issue #20 deferred this flag and proposed a scoped one: allowed where a
signature or a checksum authenticates what arrives, refused for the first
`[python]` resolve, which nothing but TLS protects. The owner decided on
2026-09-24 to skip verification everywhere, python resolve and PPA key
discovery included, and to warn instead of refuse: a build made this way is
not held to the byte-identity standard, and the risks are stated rather than
prevented. The scoped design's one non-negotiable is kept, because it costs
nothing: no setting of the flag reaches the image. apt inside the imported
distribution verifies TLS exactly as before.

## What the flag touches

| Path | Mechanism | What still protects it |
|---|---|---|
| frostroot's Go clients: signing keys, Launchpad, vendoring, the package picker, PyPI search | `InsecureSkipVerify` on `pki.Transport` | vendor checks every file against the lock's SHA-256; catalog keys are pinned by fingerprint in code; picker and search results are suggestions that `build` checks |
| apt run by mmdebstrap on the build host | `Acquire::https::Verify-Peer "false"` and `Verify-Host "false"`, written by the setup hook that already carries `CaInfo` and removed by its cleanup hook | the archive's and each source's signatures |
| pip inside the chroot, online | `--trusted-host` for the index host and `files.pythonhosted.org`, on the pinned pip's install and on the resolve | **nothing**: the resolve trusts whatever the network answers, and the lock then pins it by hash |
| PPA key discovery in `init`, `edit` and `capture` | the Go client above | **nothing**: the fingerprint is Launchpad's answer over the same connection, and the key file is trusted by every later build |
| `build --offline` | fetches nothing | unaffected; the flag is noted as doing nothing |

The pinned pip itself is still checked against the SHA-256 compiled into
frostroot, so the resolver cannot be substituted; only what it resolves can.

## Where the person is told

1. Every command run with the flag prints one warning first: certificates
   are not verified, so anyone on the network path can impersonate the
   servers this run talks to. `build` adds that the `.deb`s are still checked
   against their signatures and, when the recipe has `[python]`, that the
   Python packages are not and the lock will say so. `vendor` adds that every
   file is still checked against the lock. `init`, `edit` and `capture` add
   that a PPA's key is fetched from wherever the network says.
2. The lock records the unverified resolve: `transport = "unverified"` in its
   `[python]` table, and nowhere else, since nothing else that reached the
   lock was unverified. `build --offline` and `vendor` print a warning
   whenever they read such a lock, so the risk follows the lock and not the
   person who passed the flag.
3. `vendor` without the flag on such a lock reports, on success, that the
   wheels it downloaded matched the lock over a verified connection, and how
   many it did not download because they were already there. Deleting
   `vendor/wheels` and running `vendor` again on a trusted network is the
   check; a mismatch means the resolve was tampered with. A downgrade to a
   real older release passes it, and the README says so.
4. A PPA key saved under the flag comes with the Launchpad page to compare
   its fingerprint against.
5. The README gets a section under "Networks that inspect TLS" with this
   table, and the usage texts say what the flag does.

## What is not done

- No real byte-identity run. The apt setting goes in and out through the
  hooks `--ca-bundle` proved, and the unit tests run those hooks in a real
  shell; that is the evidence.
- No second flag for the python resolve: the owner asked for one flag that
  skips everything.
- `--trusted-host` is per host. A custom `index_url` whose files come from a
  third host still fails on that host; PyPI and the usual Nexus and
  Artifactory layouts serve files from the index's own host.

## Tasks

1. **Plan.** This file. `docs: plan --insecure, a flag that skips TLS
   verification`.
2. **The Go clients.** `pki.Transport(pool, insecure)`; `Insecure` beside
   `RootCAs` on `sources.HTTPClient`, `pool.FetchOptions` and `index.Options`;
   `index.NewSummaries` takes an `Options`. `sources.FetchedKey.Discovered`
   says when the fingerprint was Launchpad's answer. Tests against
   `httptest.NewTLSServer`, whose certificate no root signed: refused without
   the flag, accepted with it, and a wheel that does not match the lock still
   refused with it.
3. **apt and pip.** `BootstrapSpec.Insecure` writes the two `Verify` settings
   into the same file as `CaInfo`; `PythonOptions.Insecure` adds
   `--trusted-host` to the online pip commands and nothing to the offline
   ones; `builder.Options.Insecure` threads both and writes
   `LockPython.Transport`. Tests run the hooks in a shell, render the script
   with and without an `index_url`, and build a python recipe against the
   fake bootstrapper to read the mark back from the lock.
4. **The commands.** `--insecure` on all five, the warnings above, the
   lock-carried warning in `build --offline` and `vendor`, vendor's
   confirmation line, the PPA key's Launchpad line. Tests for each message
   and for the flag reaching each client.
5. **Docs.** README: the commands table, the flags paragraph, the new
   section, the scope line; version 0.13.0. Close #20 with the pull request.
6. **A real check on the build host,** time-boxed: mmdebstrap over an HTTPS
   mirror with a `CaInfo` bundle that cannot verify it, once with the two
   `Verify` settings (must fetch) and once without (must refuse); pip 24.3.1
   against the v0.8 spike's private-CA index with `--trusted-host` (must
   install) and without (must refuse); one `frostroot build --insecure` of a
   24.04 recipe with `[python]`, then `vendor` and `build --offline`, to read
   the lock's mark and the three messages for real.
