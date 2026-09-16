# frostroot v0.3 Implementation Plan: `frostroot capture`

Implements [`specs/2026-09-17-frostroot-capture.md`](../specs/2026-09-17-frostroot-capture.md).
Branch `feature/capture`, from `master` at 1dfbc3d. Every task ends with
`gofmt`, `go vet`, `go test -race ./...` and `golangci-lint run` clean, and
its own commit as `alone141` following `CONTRIBUTING.md`.

**Goal:** read an installed Ubuntu system, open the recipe form with what it
found, write `frostroot.toml` and `frostroot-capture.md`.

## Task 1: `internal/capture` readers

- [ ] `stanza.go`: `readStanzas(io.Reader) []stanza` for Debian control
      files; a `stanza` is field name to value with continuation lines kept.
- [ ] `system.go`: os-release (`ID`, `VERSION_ID`), hostname to image name,
      `wsl.conf` (`[user] default`, `[boot] systemd`), `default/locale`
      `LANG`, timezone from `/etc/timezone` or the `localtime` link, passwd
      and group parsing, the chosen user, sudo detection.
- [ ] `packages.go`: installed stanzas with priority and conffiles from the
      dpkg status; automatic set from `extended_states`; the asked-for set
      minus base, essentials and metapackages; apt index files by origin
      host; which indexes list a package.
- [ ] Tests with fixture roots built from `map[string]string`.

## Task 2: findings and the report

- [ ] `findings.go`: `Finding{Area, Count, Examples, Advice, Unavailable}`
      and one collector per area of the spec's table; `generatedEtcPatterns`
      in `etc.go`.
- [ ] `report.go`: `Snapshot.Report() string` (Markdown) and
      `Snapshot.Summary() []string`.
- [ ] `capture.go`: `Read(root string) (Snapshot, error)`; the three fatal
      conditions; `Snapshot.Recipe()`.
- [ ] Tests: the WSL-like fixture yields every expected finding; the bare
      server falls back as specified; the report has every area heading.

## Task 3: `frostroot capture` in the CLI

- [ ] `cli/capture.go`: flags `--root`, `--force`, `--plain`; `Read`; the
      shared recipe form starting from `form.FromRecipe(snapshot.Recipe())`;
      `writeRecipe`; the report file; the summary on stdout.
- [ ] Usage text and README: commands table, a "Capturing a machine"
      section that states the two rules.
- [ ] Tests: plain capture of a fixture root with scripted answers writes
      both files; unsupported release exits 1 and writes nothing; `--force`.

## Task 4: verification

- [ ] `capture --root / --plain` on the build host: recipe validates,
      `include` equals `apt-mark showmanual` minus the base, the report
      names `/etc/profile.d/go.sh` and the builder's dotfiles.
- [ ] `build` from the captured recipe (real mmdebstrap).
- [ ] Record results in the spec; pull request; ask before merging.
