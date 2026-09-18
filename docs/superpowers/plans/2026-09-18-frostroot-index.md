# frostroot v0.9 Implementation Plan: an internal package index

Lets a recipe resolve its Python packages from an internal mirror instead of
PyPI. From [issue #23](https://github.com/alone141/frostroot/issues/23), the
follow-up the v0.8 spec left. Branch `feature/index`, from `master` at
a278abf. Every task ends with `gofmt`, `go vet` (both tags), `go test -race
./...`, `GOOS=windows go build ./...` and `golangci-lint run` clean, and its
own commit as `alone141` following `CONTRIBUTING.md`.

**Goal:** `[python] index_url` in the recipe, the index recorded in the lock,
every wheel URL the lock records being where it actually came from, and the
whole corporate path — an HTTPS index signed by a private authority — proved
end to end in one real build, which v0.8 could only prove in halves.

## What is broken today

A network that inspects TLS is very often one that blocks `pypi.org` outright
and mandates Artifactory, Nexus or devpi. v0.8 made such a network's
certificate trusted; it did nothing about the index, and the Python step's
`PIP_*` sweep — the thing that keeps a lock dependent on the recipe rather
than on whoever's shell ran the build — also removes the only way pip could
have been pointed elsewhere. On that network a recipe with `[python]` still
cannot build.

There is a second, quieter problem in the same place. The pinned pip is
installed from a direct `files.pythonhosted.org` URL (`PinnedPip.URL` in
`internal/builder/python.go`), and because the pin step runs without
`--report`, the lock's `[[pypi]]` entry for pip carries that constant URL
rather than anything observed. On a network that blocks PyPI the pin fails
before the index is even consulted, and even if it did not, `vendor` would
later go to `files.pythonhosted.org` for it.

## The design

**Intent in the recipe, fact in the lock**, as everywhere.

```toml
[python]
include = ["requests"]
index_url = "https://nexus.example.com/repository/pypi/simple"
```

- Absent means PyPI, and nothing about v0.7 or v0.8 changes.
- It is a *replacement* index, passed as `--index-url`. `--extra-index-url`
  is deliberately not offered: two indexes for one name is the
  dependency-confusion attack, and a mirror that proxies PyPI needs no
  second one.
- The pinned pip goes through the same index, as `pip==24.3.1
  --hash=sha256:…` under `--require-hashes --only-binary=:all:`, which is
  the form the offline rebuild already uses. The hash is what makes it the
  same pip whichever host served it.
- **The pin step reports, always.** Online, `--report` is added to the pin
  install as well as to the main one, and the lock's pip entry comes from
  that report — the URL it was actually downloaded from — with the constant
  kept only as the fallback for a report that names no pip. This is a
  change to the PyPI path too, but not to what it records: a direct-URL
  requirement reports the URL it was given. The lock then says where every
  wheel came from, pip included, and `vendor` believes it.
- The lock records `index_url` under `[python]`, trailing slash stripped
  like a source's URL. An offline rebuild whose recipe names a different
  index than the lock is a lock mismatch, reported the way a changed
  `[[sources]]` entry is; the wheels the lock names are still what installs,
  so there is no ambiguity about what the image holds, only about whether
  the recipe still describes it.
- Validation: `http` or `https` with a host, no whitespace, brackets or
  quotes, as `CheckSourceURL` requires — and **no userinfo**. A URL with a
  password in it would land in a committed recipe and a committed lock;
  authenticated mirrors are out of scope and say so.
- `index_url` without `include` is a validation problem, not a silent
  no-op.

## Task 0: spike on the build host (blocker)

The design assumes four things about pip that have to be seen, not read.
Nothing is coded until they are, and Task 2 is the only one cheap enough to
start early.

Stand up a real PEP 503 index over HTTPS with the v0.8 spike's private
authority: a `simple/` tree generated from the wheels already vendored in
`~/frostroot-e2e/python-lab/vendor/wheels` (an `index.html` per project with
`#sha256=` fragments), served by the v0.8 `serve.py` on `localhost`. Then,
with the pinned pip run from its own wheel:

1. **A static index resolves.** `pip download --index-url
   https://localhost:8443/simple/ --cert ca.pem --only-binary=:all:
   --report r.json requests` succeeds, and the report's `download_info.url`
   for every wheel is the mirror's URL with the right `archive_info.hash`.
2. **The pin goes through the index.** `pip==24.3.1 --hash=sha256:…` under
   `--require-hashes --only-binary=:all: --index-url …` installs, and its
   report names the mirror URL for pip.
3. **A direct-URL pin reports its URL.** The current form, `pip @
   https://files.pythonhosted.org/…`, run with `--report`, records that URL
   as `download_info.url`. This is what lets the pin step report on the
   PyPI path without changing what the lock says there.
4. **Nothing else is contacted.** The server log shows only `/simple/…` and
   `/packages/…`; `--disable-pip-version-check` is already there, and this
   confirms it is enough.

Record what happened in the spec. A spike that disagrees with the design
changes the design.

## Task 1: the spec

`docs/superpowers/specs/2026-09-18-frostroot-index.md`: the two breakages,
the design, the spike results, the verification this branch has to pass.

## Task 2: the recipe

`Python.IndexURL string \`toml:"index_url,omitempty"\`` and
`Recipe.PythonIndexURL()` as the single reader, returning "" for PyPI with
the trailing slash stripped. `CheckIndexURL` alongside `CheckSourceURL`,
with the userinfo refusal. `Validate` reports an index with no packages.
Table-driven tests, including `https://user:secret@host/simple`.

## Task 3: the lock

`LockPython.IndexURL` (`omitempty`, absent for PyPI). `planOffline`
compares it with the recipe's, in the same list as `repositoryDifferences`.
A test for a lock from v0.7 or v0.8, which has no field, against a recipe
that still names none: no difference.

## Task 4: the Python step

The template gets `--index-url {{shellQuote .IndexURL}}` on both online
installs when set. The pin step chooses its requirement form by whether an
index is set, gains `--report {{shellQuote .PinReportPath}}` online in both
forms, and the hooks download that report beside the main one and delete
it. `ParsePipReport` takes both reports; `withPinnedPip` prefers the pip the
pin report names and checks its digest is `PinnedPip.SHA256` — pip already
refused anything else, but the lock is what people read. Tests assert the
flag present with an index and absent without, the two pin forms, the extra
report hook, the fallback to the constant, and shell validity for a hostile
URL that validation would refuse.

## Task 5: the form and the template

This is where v0.8 found its silent-drop bug, so it is its own task. A
`KeyPythonIndexURL` input on the packages page beside the Python packages
field ("empty means PyPI"), `FromRecipe`/`ToRecipe`/`Summary` carrying it,
and the `init` template writing `index_url` inside its `[python]` block. The
test is the round trip through `edit` that v0.8 added for certificates.

## Task 6: docs

README: the recipe table row, a paragraph in "Python packages", a sentence
in "Networks that inspect TLS" that now points here, and `extra-index-url`
and authenticated mirrors under "Deliberately not yet".

## Task 7: real images

On the build host:

1. **The PyPI path is unchanged.** The v0.7 recipe builds, `vendor`s, and
   rebuilds offline twice to one SHA-256; the lock's pip entry still names
   `files.pythonhosted.org`.
2. **The corporate path, composed at last.** The Task 0 index behind its
   private authority, named in `index_url`, the authority in
   `[certificates]`: an online build resolves through it with no access to
   PyPI at all (the server log is the proof), every `[[pypi]]` URL in the
   lock is the mirror's, `vendor --ca-bundle` fetches from the mirror, and
   two offline rebuilds match byte for byte.
3. **A changed index is refused offline**, naming both URLs.
4. **The WSL import**, as for v0.7 and v0.8.

## Deliberately out of scope

- `extra_index_url`, and `find-links` directories.
- **Authenticated mirrors.** A password belongs in neither the recipe nor
  the lock, and the step sets `HOME=/root` inside the image, so `.netrc` and
  keyrings on the build host are not consulted either. A later version can
  take a credential from the environment the way `--ca-bundle` takes a
  file; it would need the same care about never reaching the image.
- Rewriting the lock's wheel URLs at `vendor` time for a second mirror. The
  lock says where the wheels came from; `--mirror` is an apt concept.
- `capture` reporting an index; it reports no Python at all.
