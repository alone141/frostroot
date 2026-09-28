# frostroot v0.14 Implementation Plan: Ubuntu 26.04 LTS

Ubuntu 26.04 (resolute) as a fourth supported release, beside 20.04, 22.04
and 24.04. Branch `claude/next-steps-64pvh0`, from `master` at 7ce0b16.
Every task ends with `scripts/check.sh` clean and its own commit as
`alone141` following `CONTRIBUTING.md`. The spec, with the spike this plan
rests on, is
[`2026-09-28-frostroot-resolute.md`](../specs/2026-09-28-frostroot-resolute.md).

**Goal:** `release = "26.04"` builds online and offline with everything a
24.04 recipe can hold, two offline rebuilds of its lock are the same bytes,
`init` and `edit` offer it and start on it, and nothing changes for the
three releases already supported.

## The decisions this records

- **20.04 stays.** The owner decided on 2026-09-28 to keep it. It keeps its
  end-of-life warning.
- **A new recipe starts on 26.04.** `form.Defaults` says it starts "a lab
  image on the newest release"; it said "24.04" because that was the
  newest. The default becomes the newest release the table knows, computed,
  so that the next release does not need this change again.
- **The image is what the release ships.** sudo-rs as `sudo`, the Rust
  coreutils, chrony: the spike found frostroot's hooks and the user's sudo
  working with each, so none of them is replaced or pinned away.
- **The one-line `sources.list` stays.** apt 3.2 reads it without a notice,
  and an offline build writes the lock's deb lines in that format for every
  release.
- **What differs between releases is a column of the table, not a string
  comparison.** The PEP 668 note after `init` compared the release with
  "24.04"; 26.04 enforces PEP 668 too, and the next release will. The table
  says which releases do.

## What is not done

- A 26.04 build host is measured in the spec, not supported by any change
  here unless the spike found one necessary.
- The first boot under WSL cannot run in the spike's container: `wsl-boot`
  needs `wsl.exe`. It is the owner's check on Windows before the release,
  as the README asks of every release.

## Tasks

1. **Spec and plan.** The spec with the spike's results, and this file.
   `docs: spec and plan Ubuntu 26.04 support`.
2. **The table.** `internal/distro`: the 26.04 row (`resolute`, the
   archive, all four components), `SupportedVersions` in order, and a
   `ExternallyManagedPython` column, true for 24.04 and 26.04. Tests:
   `Lookup("26.04")`, its three deb lines, the order of
   `SupportedVersions`, and which releases manage their Python.
   `feat(distro): Ubuntu 26.04 LTS`.
3. **The form and the command line.** `Defaults` starts on the newest
   supported release; the PEP 668 note after `init` and `edit` reads the
   new column and names the release; the 20.04 warning's advice names the
   releases still in standard support, from the table; the recipe
   template's comment lists 26.04; the catalog's `python3-pip` line says "a
   venv on 24.04 and later". The TUI's golden frames re-recorded and read.
   Tests: the default is the last supported version; the note for 24.04
   and 26.04 and not for 22.04; the warning's advice. `feat(form): offer
   Ubuntu 26.04 and start new recipes on it`.
4. **A real 26.04 build in CI.** `TestIntegrationResoluteTiny`, the tiny
   image's test run on 26.04, sharing the 24.04 test's body, and its name
   added to the list `scripts/integration.sh` requires. The scenarios that
   write a recipe take `E2E_RELEASE`, as `offline-identical` already does,
   so that any of them can be pointed at 26.04. `test: build a real 26.04
   image in CI`.
5. **Comments that count releases.** The pinned pip's "covers all three
   releases", the sources catalog's check date and the GitHub CLI key
   comment (the repository signs with the 2026 key now, and the 2022 key
   expired on 2026-09-05). `docs: comments that count the releases`.
6. **The README.** The status paragraph, the recipe table, the notes (PEP
   668 on 24.04 and 26.04; what a 26.04 image runs: sudo-rs, the Rust
   coreutils, chrony), the scope line (v0.14), the Documentation table (this
   spec and plan, and the v0.12 source-search spec and v0.13 `--insecure`
   plan it lacked), the Go version under Install (go.mod asks for 1.27.1,
   the README said 1.24), and version 0.14.0. The Verification paragraph is
   written after the checks below have run, not before. `docs: Ubuntu
   26.04, version 0.14.0`.
7. **Real checks.** With the branch's binary: `offline-identical`,
   `certificates`, `no-build-leaks`, `insecure` and `capture-roundtrip`
   with `E2E_RELEASE=26.04`; `tui`; `scripts/integration.sh`. Then
   `wsl-boot` on Windows with `E2E_RELEASE=26.04`, by the owner.
