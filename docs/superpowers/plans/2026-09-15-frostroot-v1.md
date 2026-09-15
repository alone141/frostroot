# frostroot v1 Implementation Plan (revised)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

Supersedes `2026-09-14-frostroot.md`. Revised after
`reviews/2026-09-14-frostroot-plan-review.md` and
`reviews/2026-09-15-frostroot-feasibility.md`.

**Goal:** Ship a Linux CLI `frostroot` that reads `frostroot.toml`, bootstraps Ubuntu 20.04/22.04/24.04 with mmdebstrap, and writes `frostroot.lock` plus a WSL-importable rootfs tarball.

**Architecture:** One Go module, one binary. `internal/recipe` owns toml/lock, `internal/distro` owns Ubuntu suites and mirrors, `internal/builder` orchestrates mmdebstrap + provision + lock, `internal/export` names and places the artifact, `internal/cli` is `init`/`validate`/`build`. Real mmdebstrap sits behind a `Bootstrapper` interface so default tests run offline.

**Tech Stack:** Go (current stable, ≥1.23), stdlib `flag`/`os/exec`/`context`/`os/signal`, `github.com/pelletier/go-toml/v2`. Host tools (Linux build only): `mmdebstrap`. No `dpkg-query` dependency.

## What changed from the 2026-09-14 plan, and why

Read this before executing. Four defects in the previous plan would have shipped
a tarball that does not boot.

| # | Was | Now | Why |
|---|-----|-----|-----|
| 1 | `internal/export` walked the rootfs with `archive/tar` and `tar.FileInfoHeader(info, "")` | **mmdebstrap writes the tarball**; export only names it and moves it | The second argument to `FileInfoHeader` *is* the symlink target. Passing `""` gives every symlink an empty target — merged-`/usr`, every library soname, all of `/etc/alternatives`. The image cannot start. Same root cause also dropped hardlinks (duplicated as full copies), dropped file capabilities (`ping` breaks), and filled `Uname`/`Gname` from the host passwd, the opposite of the `--numeric-owner` the spec asks for. |
| 2 | Host-side tar and `os.RemoveAll` of a rootfs built in `--mode=unshare` | frostroot never reads or deletes a chroot directory | Files in unshare mode carry subuid-range ownership from outside the namespace: the tar records wrong uids and `RemoveAll` cannot delete them. Letting mmdebstrap tar from *inside* the namespace fixes ownership, xattrs and cleanup at once, and removes the recursive-delete-with-live-mounts hazard. |
| 3 | `--variant=minbase`, essentials `{sudo, locales, tzdata, passwd}` | `--variant=important` plus `systemd`, `systemd-sysv`, `dbus`, `ca-certificates`; Recommends on | minbase has no systemd, so `[boot] systemd=true` is inert and spec criteria 4 and 6 are unreachable. Recommends-off also meant `git` did not pull `ca-certificates`, so `git clone https://…` failed in the lab image. |
| 4 | One mirror argument → `sources.list` with only `<suite> main universe` | Three `deb` lines per build: `<suite>`, `<suite>-updates`, `<suite>-security` | Release-day versions only, so every golden image shipped years of unpatched CVEs. Note the old spec's own lock example (`git 1:2.34.1-1ubuntu1.11`) is an updates-pocket version the old pipeline could not produce. |

Also fixed, from the same reviews:

- **Task 0 added.** A manual spike gates all Go work. The only success criterion
  that tests the product promise (`wsl --import` boots) cannot be automated, so
  the unit suite can be fully green on a broken image — exactly what defect 1
  would have done. The spike is the only thing that makes the rest trustworthy.
- **`WriteProvisionFiles` demoted to test-only.** It was called in the real build
  *after* the hooks had already done the same work — two implementations that
  drift. Its test also passed vacuously: it asserted `/home/student` exists after
  an `os.MkdirAll` at uid 0, with no user, no group, no ownership.
- **`|| true` removed from hooks.** A failed `useradd` reported success and
  yielded a root-login image. Fail closed.
- **`[locale].lang` and `[locale].timezone` validated and shell-quoted** before
  being spliced into hook scripts.
- **`dpkg-query --root` dropped** in favour of parsing `/var/lib/dpkg/status`,
  retrieved with mmdebstrap's `download` hook. `--root` needs dpkg ≥ 1.21, which
  excludes Ubuntu 20.04 and Debian 11 build hosts.
- **`context.Context` on `Bootstrapper.Run` now**, not later, plus Ctrl-C → 130.
- **Work directory** defaults to `$XDG_CACHE_HOME/frostroot` or `/var/tmp`, never
  under `/mnt/` (9p is slow and unreliable for chown and device nodes).
- **Windows test constraint dropped.** The spec never asked for it — it says the
  CLI is Linux and Windows users run it inside WSL. It only bought `0440`-mode
  cleanup workarounds and symlink-test skips.
- **20.04 ships a loud warning.** old-releases carries focal frozen at EOL, so
  its CVEs are unfixable without Ubuntu Pro. Freezing an old distro is the point;
  handing a classroom an unpatchable image by surprise is not.
- **`--keep-rootfs` renamed `--keep-work`.** Under the new design there is no
  rootfs directory to keep; the work directory (tarball, dpkg status, mmdebstrap
  log) is what is useful.

## Global Constraints

- Language is Go; module path is `frostroot`; binary name is `frostroot`.
- v1 distros: Ubuntu 20.04 (`focal`, base `http://old-releases.ubuntu.com/ubuntu`), 22.04 (`jammy`) and 24.04 (`noble`) (base `http://archive.ubuntu.com/ubuntu`); arch `amd64` only; components `main universe`.
- Every build uses three pockets: `<suite>`, `<suite>-updates`, `<suite>-security`, all against one base URL. `--mirror` replaces the base URL for all three.
- v1 packages: apt names only. No PPAs, no pip/npm/cargo, no Fedora, no `.deb` vendoring, no `build --offline`.
- Recipe is source of truth (`frostroot.toml`); versions live only in `frostroot.lock`. `build` never prompts.
- The tarball is produced **by mmdebstrap, inside the namespace**. Go never walks, tars, or deletes a rootfs tree. This is not negotiable; it is defect 1 and 2 above.
- Tarball is the golden image. v1 does not reinstall from lock versions (`pkg=version`).
- Image profile: sudo user (passwordless when `sudo = true`), `/etc/wsl.conf` systemd + default user, locale/timezone. No password field.
- Provision essentials always included: `systemd`, `systemd-sysv`, `dbus`, `sudo`, `locales`, `tzdata`, `passwd`, `ca-certificates`.
- Recommends are **on** (`--aptopt='Apt::Install-Recommends "true"'`) so `include` behaves like `apt install` on stock Ubuntu.
- CLI verbs v1: `init`, `validate`, `build` only. Flags: `init --force`; `build --mirror URL` and `build --keep-work`.
- Exit codes: 0 success, 1 user error, 2 build error, 130 interrupt.
- Default `go test ./...` must pass **offline, without root, without mmdebstrap, on Linux**. Real bootstrap is Linux-only. Windows is not a test target.
- Lock `version = 1`; `frostroot_version = "0.1.0"`; `requested` is recipe include as written; `[[packages]]` is every installed package with `arch`; `sources` records the three `deb` lines.
- Tarball path: `dist/<image.name>-ubuntu-<release>-<arch>.tar.gz`.
- Hooks fail closed. No `|| true`. Every interpolated recipe value is validated and shell-quoted.
- Do not invent extra subcommands, distros, or recipe fields.

---

## File structure

| Path | Responsibility |
|------|----------------|
| `go.mod` | Module `frostroot` |
| `cmd/frostroot/main.go` | `os.Exit(cli.New().Run(os.Args[1:]))` |
| `internal/distro/ubuntu.go` | Release → suite, base URL, components, pocket `deb` lines, EOL flag |
| `internal/distro/ubuntu_test.go` | Distro table tests |
| `internal/recipe/recipe.go` | `Recipe` types, `Load`, `Save` |
| `internal/recipe/validate.go` | `Validate` → all problems |
| `internal/recipe/lock.go` | `Lockfile` types, `LoadLock`, `SaveLock` |
| `internal/recipe/recipe_test.go` | Parse/validate/lock tests |
| `internal/export/name.go` | `TarballRelPath`, `Place` (atomic move) |
| `internal/export/export_test.go` | Name + move tests |
| `internal/builder/provision.go` | `Essentials`, `MergeInclude`, `RenderWSLConf`, `RenderSudoers`, `CustomizeHooks` |
| `internal/builder/status.go` | `ParseDpkgStatus` |
| `internal/builder/builder.go` | `Builder.Build`, `Bootstrapper`, `BootstrapSpec` |
| `internal/builder/bootstrap.go` | Real mmdebstrap runner |
| `internal/builder/workdir.go` | Work directory selection |
| `internal/builder/*_test.go` | Fake bootstrapper tests |
| `internal/cli/app.go` | `App.Run`, flags, exit codes, signals |
| `internal/cli/initcmd.go` | `init` wizard |
| `internal/cli/app_test.go` | CLI tests |
| `testdata/*.toml` | Recipe fixtures |
| `README.md` | Replace design-stage stub with usage |

---

### Task 0: Manual spike — GATE. Write no Go until this passes.

Not optional, and not parallelisable with the Go work. Everything below assumes
mmdebstrap behaves as described; this is where that gets checked. Budget ~1 hour
on a real WSL host (you need Windows for the second half).

**Host prep**

```
sudo apt install mmdebstrap
```

That pulls `uidmap`, `fakeroot`, `fakechroot` and `arch-test`. Confirm
`unshare -Ur true` succeeds and `/etc/subuid` has a range for your user.

- [ ] **Step 1: One real build, by hand**

Run as your normal (non-root) user, not under `sudo`, so you exercise the
`--mode=unshare` path real users will hit. Put the work directory on a native
Linux filesystem — **not** under `/mnt/c` or `/mnt/d`.

```sh
W=/var/tmp/frostroot-spike
mkdir -p "$W/tmp"
printf '[boot]\nsystemd=true\n\n[user]\ndefault=student\n' > "$W/wsl.conf"
printf 'student ALL=(ALL) NOPASSWD:ALL\n' > "$W/sudoers"
BASE=http://archive.ubuntu.com/ubuntu

TMPDIR="$W/tmp" mmdebstrap \
  --mode=unshare \
  --architectures=amd64 \
  --components='main universe' \
  --variant=important \
  --keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg \
  --aptopt='Apt::Install-Recommends "true"' \
  --include=systemd,systemd-sysv,dbus,sudo,locales,tzdata,passwd,ca-certificates,git \
  --customize-hook="upload $W/wsl.conf /etc/wsl.conf" \
  --customize-hook="upload $W/sudoers /etc/sudoers.d/90-frostroot" \
  --customize-hook='chmod 0440 "$1/etc/sudoers.d/90-frostroot"' \
  --customize-hook='chroot "$1" useradd --create-home --shell /bin/bash --user-group student' \
  --customize-hook='chroot "$1" sh -c "echo en_US.UTF-8 UTF-8 >> /etc/locale.gen && locale-gen && update-locale LANG=en_US.UTF-8"' \
  --customize-hook='chroot "$1" sh -c "ln -sf /usr/share/zoneinfo/UTC /etc/localtime && echo UTC > /etc/timezone"' \
  --customize-hook='rm -f "$1/etc/resolv.conf" "$1/etc/hostname"' \
  --customize-hook="download /var/lib/dpkg/status $W/dpkg-status" \
  noble "$W/image.tar.gz" \
  "deb $BASE noble main universe" \
  "deb $BASE noble-updates main universe" \
  "deb $BASE noble-security main universe"
```

Record the wall-clock time and the tarball size. You will want them for the
README and for judging whether a classroom can rebuild on demand.

- [ ] **Step 2: Check the tarball before importing anything**

These four checks are the ones that catch defects 1, 2 and 4. Do not skip them
because the build exited 0.

```sh
# Symlinks must have non-empty targets (this is defect 1)
tar tvzf "$W/image.tar.gz" | grep ' -> ' | head
# Ownership must be sane: root-owned system files, not subuid-range numbers
tar tvzf "$W/image.tar.gz" | awk '{print $2}' | sort -u | head
# The lab user's home must belong to the lab user, not to root or 100000+
tar tvzf "$W/image.tar.gz" | grep 'home/student'
# Updates pocket must have been used: expect versions above release-day
grep -A2 '^Package: git$' "$W/dpkg-status" | grep ^Version:
```

If symlink targets are empty or ownership is in the 100000+ range, **stop** —
the design assumption is wrong and this plan needs revising before any Go.

- [ ] **Step 3: Import on Windows and actually log in**

```powershell
wsl --import spike C:\wsl\spike \\wsl$\Ubuntu\var\tmp\frostroot-spike\image.tar.gz
wsl -d spike
```

Inside, confirm every one of these:

```sh
whoami                      # student, not root
sudo id                     # no password prompt
systemctl is-system-running # running or degraded — NOT "offline"
systemctl --failed          # note anything failing; resolved/networkd are the usual suspects
cat /etc/resolv.conf        # WSL should have generated one
getent hosts archive.ubuntu.com   # DNS works
git clone https://github.com/git/git /tmp/g --depth 1   # TLS works (proves ca-certificates)
locale                      # LANG=en_US.UTF-8, no warnings
date                        # correct timezone
```

- [ ] **Step 4: Write down what you learned**

Append a short "spike results" section to
`reviews/2026-09-15-frostroot-feasibility.md`: build time, tarball size, whether
symlinks and ownership survived, which units failed, and anything that
contradicts this plan. If `systemd-resolved` fights WSL's generated
`resolv.conf`, decide now whether to mask it in a hook — that decision belongs
in Task 5, and finding it here is the whole point of the spike.

```
git add docs/superpowers/reviews
git commit -m "docs: record frostroot spike results"
```

---

### Task 1: Distro table with pockets

**Files:**
- Create: `go.mod`
- Create: `internal/distro/ubuntu.go`
- Test: `internal/distro/ubuntu_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `distro.Info`, `distro.Lookup(release, arch string) (Info, error)`, `(Info).Sources(baseOverride string) []string`, `distro.KnownReleases() []string`, `distro.ErrUnknownRelease`, `distro.ErrUnsupportedArch`

`Info.EOL` drives the 20.04 warning in Task 9. `Sources` is the only place pocket
lines are constructed — the builder must never hand-assemble a `deb` line.

- [ ] **Step 1: Create the module and a failing test**

```
go mod init frostroot
```

Set the current stable Go version in `go.mod` (≥1.23).

```go
package distro

import (
	"errors"
	"strings"
	"testing"
)

func TestLookupFocalOldReleases(t *testing.T) {
	info, err := Lookup("20.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if info.Suite != "focal" {
		t.Fatalf("suite: got %q", info.Suite)
	}
	if info.Base != "http://old-releases.ubuntu.com/ubuntu" {
		t.Fatalf("base: got %q", info.Base)
	}
	if !info.EOL {
		t.Fatal("20.04 must be flagged EOL so build can warn")
	}
	if len(info.Components) != 2 || info.Components[0] != "main" || info.Components[1] != "universe" {
		t.Fatalf("components: got %#v", info.Components)
	}
}

func TestLookupJammyAndNobleArchive(t *testing.T) {
	for _, tc := range []struct{ release, suite string }{{"22.04", "jammy"}, {"24.04", "noble"}} {
		info, err := Lookup(tc.release, "amd64")
		if err != nil {
			t.Fatalf("%s: %v", tc.release, err)
		}
		if info.Suite != tc.suite {
			t.Fatalf("%s suite: got %q", tc.release, info.Suite)
		}
		if info.Base != "http://archive.ubuntu.com/ubuntu" {
			t.Fatalf("%s base: got %q", tc.release, info.Base)
		}
		if info.EOL {
			t.Fatalf("%s must not be flagged EOL", tc.release)
		}
	}
}

func TestSourcesHasThreePockets(t *testing.T) {
	info, err := Lookup("22.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	got := info.Sources("")
	want := []string{
		"deb http://archive.ubuntu.com/ubuntu jammy main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
		"deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
	}
	if len(got) != 3 {
		t.Fatalf("want 3 pockets, got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

func TestSourcesMirrorOverrideReplacesAllThree(t *testing.T) {
	info, err := Lookup("24.04", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	got := info.Sources("http://mirror.example/ubuntu")
	if len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	for _, line := range got {
		if !strings.Contains(line, "http://mirror.example/ubuntu") {
			t.Fatalf("override missed: %q", line)
		}
		if strings.Contains(line, "archive.ubuntu.com") {
			t.Fatalf("default leaked: %q", line)
		}
	}
	if !strings.Contains(got[1], "noble-updates") || !strings.Contains(got[2], "noble-security") {
		t.Fatalf("pockets wrong: %#v", got)
	}
}

func TestLookupUnknownRelease(t *testing.T) {
	if _, err := Lookup("18.04", "amd64"); !errors.Is(err, ErrUnknownRelease) {
		t.Fatalf("got %v", err)
	}
}

func TestLookupUnsupportedArch(t *testing.T) {
	if _, err := Lookup("24.04", "arm64"); !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("got %v", err)
	}
}

func TestLookupEmptyArch(t *testing.T) {
	_, err := Lookup("24.04", "")
	if !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("got %v", err)
	}
	if !strings.Contains(err.Error(), "amd64") {
		t.Fatalf("message should name the supported arch: %v", err)
	}
}

func TestKnownReleases(t *testing.T) {
	got := KnownReleases()
	want := []string{"20.04", "22.04", "24.04"}
	if len(got) != 3 {
		t.Fatalf("got %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %#v", got)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/distro/ -v`

Expected: FAIL, undefined `Lookup`.

- [ ] **Step 3: Write minimal implementation**

```go
package distro

import (
	"errors"
	"fmt"
	"strings"
)

var (
	ErrUnknownRelease  = errors.New("unknown ubuntu release")
	ErrUnsupportedArch = errors.New("unsupported arch")
)

type Info struct {
	Suite      string
	Base       string
	Components []string
	EOL        bool // no security updates available without Ubuntu Pro
}

var table = map[string]Info{
	"20.04": {Suite: "focal", Base: "http://old-releases.ubuntu.com/ubuntu", Components: []string{"main", "universe"}, EOL: true},
	"22.04": {Suite: "jammy", Base: "http://archive.ubuntu.com/ubuntu", Components: []string{"main", "universe"}},
	"24.04": {Suite: "noble", Base: "http://archive.ubuntu.com/ubuntu", Components: []string{"main", "universe"}},
}

func Lookup(release, arch string) (Info, error) {
	if arch != "amd64" {
		return Info{}, fmt.Errorf("%w: %q (v1 supports amd64 only)", ErrUnsupportedArch, arch)
	}
	info, ok := table[release]
	if !ok {
		return Info{}, fmt.Errorf("%w: %q (known: %s)", ErrUnknownRelease, release, strings.Join(KnownReleases(), ", "))
	}
	out := info
	out.Components = append([]string{}, info.Components...)
	return out, nil
}

// Sources returns the three apt source lines for this release: the release
// pocket, -updates and -security. Without -updates and -security the image
// ships release-day packages and therefore years of unpatched CVEs.
func (i Info) Sources(baseOverride string) []string {
	base := i.Base
	if baseOverride != "" {
		base = baseOverride
	}
	comps := strings.Join(i.Components, " ")
	return []string{
		fmt.Sprintf("deb %s %s %s", base, i.Suite, comps),
		fmt.Sprintf("deb %s %s-updates %s", base, i.Suite, comps),
		fmt.Sprintf("deb %s %s-security %s", base, i.Suite, comps),
	}
}

func KnownReleases() []string { return []string{"20.04", "22.04", "24.04"} }
```

Note on 20.04: old-releases carries focal's `-updates` and `-security` pockets
frozen at end of standard support, so the three-line shape is still correct
there — it just cannot receive anything new. That is what `EOL` warns about.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/distro/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add go.mod internal/distro
git commit -m "feat: add Ubuntu LTS distro table with update pockets"
```

---

### Task 2: Recipe load and validate

**Files:**
- Create: `internal/recipe/recipe.go`
- Create: `internal/recipe/validate.go`
- Test: `internal/recipe/recipe_test.go`
- Create: `testdata/valid.toml`, `testdata/bad-release.toml`, `testdata/bad-user.toml`, `testdata/bad-locale.toml`, `testdata/unknown-field.toml`

**Interfaces:**
- Consumes: `distro.Lookup`
- Produces: `recipe.Recipe` and sub-structs, `Load`, `Save`, `Validate(Recipe) []string`, `DefaultUser(Recipe) string`

Two changes from the old Task 2, both security-relevant: `Load` rejects unknown
fields, and `[locale]` values are validated because Task 5 splices them into
shell.

- [ ] **Step 1: Write the failing tests**

```go
package recipe

import (
	"path/filepath"
	"strings"
	"testing"
)

func testdata(name string) string { return filepath.Join("..", "..", "testdata", name) }

func TestLoadValid(t *testing.T) {
	r, err := Load(testdata("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Image.Name != "cpp-lab" || r.Image.Release != "22.04" || r.Image.Arch != "amd64" {
		t.Fatalf("image: %+v", r.Image)
	}
	if r.User.Name != "student" || !r.User.Sudo {
		t.Fatalf("user: %+v", r.User)
	}
	if len(r.Packages.Include) != 3 {
		t.Fatalf("include: %#v", r.Packages.Include)
	}
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("unexpected problems: %v", probs)
	}
}

func TestLoadRejectsUnknownField(t *testing.T) {
	// A [package] typo must fail loudly, not silently yield an empty include.
	_, err := Load(testdata("unknown-field.toml"))
	if err == nil {
		t.Fatal("expected error for unknown field")
	}
}

func TestValidateProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Recipe)
		want string
	}{
		{"unknown release", func(r *Recipe) { r.Image.Release = "18.04" }, "unknown ubuntu release"},
		{"empty arch", func(r *Recipe) { r.Image.Arch = "" }, "amd64"},
		{"root user", func(r *Recipe) { r.User.Name = "root" }, "user name"},
		{"bad user chars", func(r *Recipe) { r.User.Name = "Student!" }, "user name"},
		{"long user", func(r *Recipe) { r.User.Name = strings.Repeat("a", 33) }, "user name"},
		{"empty image name", func(r *Recipe) { r.Image.Name = "" }, "image name"},
		{"bad image name", func(r *Recipe) { r.Image.Name = "../evil" }, "image name"},
		{"default_user mismatch", func(r *Recipe) { r.WSL.DefaultUser = "someone" }, "default_user"},
		{"bad package token", func(r *Recipe) { r.Packages.Include = []string{"git; rm -rf /"} }, "package name"},
		{"bad lang", func(r *Recipe) { r.Locale.Lang = "en_US.UTF-8; touch /pwned" }, "locale lang"},
		{"bad timezone", func(r *Recipe) { r.Locale.Timezone = "../../etc/shadow" }, "timezone"},
		{"absolute timezone", func(r *Recipe) { r.Locale.Timezone = "/etc/passwd" }, "timezone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Load(testdata("valid.toml"))
			if err != nil {
				t.Fatal(err)
			}
			tc.mut(&r)
			probs := Validate(r)
			if len(probs) == 0 {
				t.Fatal("expected a problem")
			}
			if !strings.Contains(strings.Join(probs, "\n"), tc.want) {
				t.Fatalf("want %q in %v", tc.want, probs)
			}
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	r := Recipe{}
	probs := Validate(r)
	if len(probs) < 3 {
		t.Fatalf("expected several problems, got %v", probs)
	}
}

func TestValidateAllowsEmptyInclude(t *testing.T) {
	r, err := Load(testdata("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	r.Packages.Include = nil
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("empty include is legal: %v", probs)
	}
}

func TestDefaultUserFallsBackToUserName(t *testing.T) {
	r := Recipe{User: User{Name: "student"}}
	if got := DefaultUser(r); got != "student" {
		t.Fatalf("got %q", got)
	}
	r.WSL.DefaultUser = "student"
	if got := DefaultUser(r); got != "student" {
		t.Fatalf("got %q", got)
	}
}

func TestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	in, err := Load(testdata("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "frostroot.toml")
	if err := Save(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Image.Name != in.Image.Name || out.User.Name != in.User.Name {
		t.Fatalf("got %+v", out)
	}
}
```

`testdata/valid.toml`:

```toml
[image]
name = "cpp-lab"
release = "22.04"
arch = "amd64"

[user]
name = "student"
sudo = true

[wsl]
systemd = true
default_user = "student"

[locale]
lang = "en_US.UTF-8"
timezone = "UTC"

[packages]
include = ["git", "build-essential", "cmake"]
```

`testdata/unknown-field.toml` is `valid.toml` with `[packages]` renamed
`[package]`. `bad-release.toml`, `bad-user.toml` and `bad-locale.toml` are
`valid.toml` with the one field broken; they back the `validate` CLI tests in
Task 7.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/recipe/ -v`

Expected: FAIL, undefined `Load`.

- [ ] **Step 3: Write minimal implementation**

`recipe.go` — types as in the spec, plus strict decoding:

```go
func Load(path string) (Recipe, error) {
	var r Recipe
	f, err := os.Open(path)
	if err != nil {
		return Recipe{}, err
	}
	defer f.Close()
	dec := toml.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return Recipe{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}
```

`validate.go`:

```go
package recipe

import (
	"fmt"
	"regexp"
	"strings"

	"frostroot/internal/distro"
)

var (
	imageNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	userNameRe  = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	pkgTokenRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
	// Locale and timezone are interpolated into shell hooks in
	// internal/builder. Keep these strict; they are a shell-injection
	// boundary, not a cosmetic check.
	langRe = regexp.MustCompile(`^[A-Za-z0-9_]+\.[A-Za-z0-9-]+$`)
	tzRe   = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+){0,2}$`)
)

func Validate(r Recipe) []string {
	var probs []string
	if !imageNameRe.MatchString(r.Image.Name) {
		probs = append(probs, fmt.Sprintf("invalid image name %q (letters, digits, dot, dash, underscore; must not be empty)", r.Image.Name))
	}
	if _, err := distro.Lookup(r.Image.Release, r.Image.Arch); err != nil {
		probs = append(probs, err.Error())
	}
	if r.User.Name == "root" || !userNameRe.MatchString(r.User.Name) || len(r.User.Name) > 32 {
		probs = append(probs, fmt.Sprintf("invalid user name %q (lowercase, 1-32 chars, not root)", r.User.Name))
	}
	if r.WSL.DefaultUser != "" && r.WSL.DefaultUser != r.User.Name {
		probs = append(probs, fmt.Sprintf("wsl.default_user %q must equal user.name %q", r.WSL.DefaultUser, r.User.Name))
	}
	if r.Locale.Lang != "" && !langRe.MatchString(r.Locale.Lang) {
		probs = append(probs, fmt.Sprintf("invalid locale lang %q (expected e.g. en_US.UTF-8)", r.Locale.Lang))
	}
	if r.Locale.Timezone != "" && (!tzRe.MatchString(r.Locale.Timezone) || strings.Contains(r.Locale.Timezone, "..")) {
		probs = append(probs, fmt.Sprintf("invalid timezone %q (expected e.g. UTC or Europe/Istanbul)", r.Locale.Timezone))
	}
	for _, p := range r.Packages.Include {
		if !pkgTokenRe.MatchString(p) {
			probs = append(probs, fmt.Sprintf("invalid package name %q", p))
		}
	}
	return probs
}

func DefaultUser(r Recipe) string {
	if r.WSL.DefaultUser != "" {
		return r.WSL.DefaultUser
	}
	return r.User.Name
}
```

The package regex needs two character classes (`+` in the second) so `g++` is
valid; a bare `a` is rejected, which matches Debian policy's two-character
minimum.

`Validate` does not check that the timezone *exists* — that needs the chroot.
Task 5 adds a hook that fails the build if `/usr/share/zoneinfo/<tz>` is absent.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/recipe/ ./internal/distro/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add go.mod go.sum internal/recipe testdata
git commit -m "feat: parse and validate frostroot.toml"
```

---

### Task 3: Lockfile round-trip

**Files:**
- Create: `internal/recipe/lock.go`
- Modify: `internal/recipe/recipe_test.go`

**Interfaces:**
- Produces: `recipe.Lockfile`, `recipe.LockPackage`, `LoadLock`, `SaveLock`

Two additions to the spec's shape, both for the vendoring follow-up: `sources`
records the three `deb` lines actually used, and each package records `arch`.
Adding them now costs nothing; adding them later is a lock-format migration.

- [ ] **Step 1: Write the failing test**

```go
func TestLockRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frostroot.lock")
	in := Lockfile{
		Version:          1,
		Distro:           "ubuntu",
		Release:          "22.04",
		Suite:            "jammy",
		Arch:             "amd64",
		Mirror:           "http://archive.ubuntu.com/ubuntu",
		Sources: []string{
			"deb http://archive.ubuntu.com/ubuntu jammy main universe",
			"deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
			"deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
		},
		FrostrootVersion: "0.1.0",
		Requested:        []string{"git", "build-essential", "cmake"},
		Packages: []LockPackage{
			{Name: "git", Version: "1:2.34.1-1ubuntu1.11", Arch: "amd64"},
			{Name: "libc6", Version: "2.35-0ubuntu3.8", Arch: "amd64"},
		},
	}
	if err := SaveLock(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if out.Version != 1 || out.Suite != "jammy" || out.FrostrootVersion != "0.1.0" {
		t.Fatalf("header: %+v", out)
	}
	if len(out.Sources) != 3 || !strings.Contains(out.Sources[2], "jammy-security") {
		t.Fatalf("sources: %#v", out.Sources)
	}
	if len(out.Requested) != 3 || out.Requested[0] != "git" {
		t.Fatalf("requested: %#v", out.Requested)
	}
	if len(out.Packages) != 2 || out.Packages[0].Name != "git" || out.Packages[0].Arch != "amd64" {
		t.Fatalf("packages: %#v", out.Packages)
	}
}

func TestSaveLockIsDeterministic(t *testing.T) {
	// Same input must produce byte-identical output, so a rebuild that
	// changes nothing produces an empty git diff.
	dir := t.TempDir()
	in := Lockfile{Version: 1, Distro: "ubuntu", Packages: []LockPackage{
		{Name: "b", Version: "1", Arch: "amd64"},
		{Name: "a", Version: "2", Arch: "amd64"},
	}}
	a := filepath.Join(dir, "a.lock")
	b := filepath.Join(dir, "b.lock")
	if err := SaveLock(a, in); err != nil {
		t.Fatal(err)
	}
	if err := SaveLock(b, in); err != nil {
		t.Fatal(err)
	}
	ba, _ := os.ReadFile(a)
	bb, _ := os.ReadFile(b)
	if string(ba) != string(bb) {
		t.Fatal("SaveLock is not deterministic")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/recipe/ -run TestLock -v`

Expected: FAIL, undefined `Lockfile`.

- [ ] **Step 3: Write minimal implementation**

```go
type Lockfile struct {
	Version          int           `toml:"version"`
	Distro           string        `toml:"distro"`
	Release          string        `toml:"release"`
	Suite            string        `toml:"suite"`
	Arch             string        `toml:"arch"`
	Mirror           string        `toml:"mirror"`
	Sources          []string      `toml:"sources"`
	FrostrootVersion string        `toml:"frostroot_version"`
	Requested        []string      `toml:"requested"`
	Packages         []LockPackage `toml:"packages"`
}

type LockPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
	Arch    string `toml:"arch"`
	// sha256 and filename land here when vendoring arrives; the
	// list-of-tables shape exists so that stays additive.
}
```

`LoadLock`/`SaveLock` mirror `Load`/`Save`. `SaveLock` writes `0o644`. The
caller sorts `Packages` by name before saving (Task 6) — that is what makes the
determinism test pass.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/recipe/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/recipe/lock.go internal/recipe/recipe_test.go
git commit -m "feat: round-trip frostroot.lock with sources and per-package arch"
```

---

### Task 4: Tarball name and atomic placement

**Files:**
- Create: `internal/export/name.go`
- Test: `internal/export/export_test.go`

**Interfaces:**
- Produces: `export.TarballRelPath(imageName, release, arch string) string`, `export.Place(src, dest string) error`

This is the task that used to contain a rootfs tar writer. **It does not any
more.** mmdebstrap writes the tarball from inside the user namespace; this
package only decides the name and moves the file into `dist/`.

If you are tempted to reintroduce `archive/tar` here, re-read defect 1 in the
changelog. `tar.FileInfoHeader(info, "")` gives every symlink an empty target
and the image will not boot, while every test in this package still passes.

`Place` must survive a cross-device move: the work directory is on `/var/tmp` or
`$XDG_CACHE_HOME` while `dist/` sits next to the recipe, which on WSL is often a
9p mount of a Windows drive. `os.Rename` returns `EXDEV` there.

- [ ] **Step 1: Write the failing test**

```go
package export

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTarballRelPath(t *testing.T) {
	got := TarballRelPath("cpp-lab", "22.04", "amd64")
	want := filepath.Join("dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestPlaceCreatesParentAndMoves(t *testing.T) {
	src := filepath.Join(t.TempDir(), "image.tar.gz")
	if err := os.WriteFile(src, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if err := Place(src, dest); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "payload" {
		t.Fatalf("content %q", got)
	}
	if _, err := os.Stat(src); !os.IsNotExist(err) {
		t.Fatalf("source should be gone: %v", err)
	}
}

func TestPlaceOverwritesExisting(t *testing.T) {
	// build overwrites a matching tarball without asking.
	dir := t.TempDir()
	dest := filepath.Join(dir, "dist", "out.tar.gz")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("stale"), 0o644); err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "image.tar.gz")
	if err := os.WriteFile(src, []byte("fresh"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Place(src, dest); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(dest)
	if string(got) != "fresh" {
		t.Fatalf("content %q", got)
	}
}

func TestPlaceLeavesNoTmpOnSuccess(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "dist", "out.tar.gz")
	src := filepath.Join(t.TempDir(), "image.tar.gz")
	if err := os.WriteFile(src, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Place(src, dest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(dest))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected only the tarball, got %v", entries)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/export/ -v`

Expected: FAIL, undefined symbols.

- [ ] **Step 3: Write minimal implementation**

```go
package export

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

func TarballRelPath(imageName, release, arch string) string {
	return filepath.Join("dist", fmt.Sprintf("%s-ubuntu-%s-%s.tar.gz", imageName, release, arch))
}

// Place moves src to dest atomically where it can, and falls back to
// copy+fsync+rename across filesystems (work dir on /var/tmp, dist/ on a 9p
// mount of a Windows drive is the normal case under WSL).
func Place(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Rename(src, dest); err == nil {
		return nil
	}
	tmp := dest + ".tmp"
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Sync(); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Remove(src)
}
```

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/export/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/export
git commit -m "feat: name and atomically place the image tarball"
```

---

### Task 5: Parse dpkg status

**Files:**
- Create: `internal/builder/status.go`
- Test: `internal/builder/status_test.go`

**Interfaces:**
- Produces: `builder.ParseDpkgStatus(r io.Reader) ([]recipe.LockPackage, error)`

This replaces `dpkg-query --root`, which needs dpkg ≥ 1.21 on the *host* and so
excludes Ubuntu 20.04 and Debian 11 build machines. mmdebstrap's
`download /var/lib/dpkg/status <path>` hook hands us the file; we parse it in Go
and depend on no host tool at all.

Status file format: RFC822-ish stanzas separated by blank lines. Keep only
`Status: install ok installed` — a status file lists removed-but-not-purged
packages too, and those are not in the image.

- [ ] **Step 1: Write the failing test**

```go
package builder

import (
	"strings"
	"testing"
)

const sampleStatus = `Package: bash
Status: install ok installed
Priority: required
Architecture: amd64
Version: 5.2.21-2ubuntu4
Description: GNU Bourne Again SHell

Package: gone
Status: deinstall ok config-files
Architecture: amd64
Version: 1.0-1

Package: git
Status: install ok installed
Architecture: amd64
Version: 1:2.43.0-1ubuntu7.1
Multi-Arch: foreign

Package: libc6
Status: install ok installed
Architecture: amd64
Version: 2.39-0ubuntu8.3
`

func TestParseDpkgStatusKeepsOnlyInstalled(t *testing.T) {
	pkgs, err := ParseDpkgStatus(strings.NewReader(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 3 {
		t.Fatalf("want 3 installed packages, got %#v", pkgs)
	}
	for _, p := range pkgs {
		if p.Name == "gone" {
			t.Fatal("deinstalled package must not be in the lock")
		}
	}
}

func TestParseDpkgStatusSortedByName(t *testing.T) {
	pkgs, err := ParseDpkgStatus(strings.NewReader(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"bash", "git", "libc6"}
	for i, w := range want {
		if pkgs[i].Name != w {
			t.Fatalf("order: got %#v", pkgs)
		}
	}
}

func TestParseDpkgStatusFields(t *testing.T) {
	pkgs, err := ParseDpkgStatus(strings.NewReader(sampleStatus))
	if err != nil {
		t.Fatal(err)
	}
	if pkgs[1].Name != "git" || pkgs[1].Version != "1:2.43.0-1ubuntu7.1" || pkgs[1].Arch != "amd64" {
		t.Fatalf("git entry: %#v", pkgs[1])
	}
}

func TestParseDpkgStatusHandlesTrailingStanzaWithoutBlankLine(t *testing.T) {
	in := "Package: only\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1"
	pkgs, err := ParseDpkgStatus(strings.NewReader(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 1 || pkgs[0].Name != "only" {
		t.Fatalf("got %#v", pkgs)
	}
}

func TestParseDpkgStatusEmptyIsError(t *testing.T) {
	// An empty status file means the bootstrap produced nothing; a lock with
	// zero packages must never be written.
	if _, err := ParseDpkgStatus(strings.NewReader("")); err == nil {
		t.Fatal("expected error for empty status")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/builder/ -run TestParseDpkgStatus -v`

Expected: FAIL, undefined `ParseDpkgStatus`.

- [ ] **Step 3: Write minimal implementation**

```go
package builder

import (
	"bufio"
	"errors"
	"io"
	"sort"
	"strings"

	"frostroot/internal/recipe"
)

func ParseDpkgStatus(r io.Reader) ([]recipe.LockPackage, error) {
	var out []recipe.LockPackage
	var cur map[string]string
	flush := func() {
		if cur == nil {
			return
		}
		if cur["Status"] == "install ok installed" && cur["Package"] != "" {
			out = append(out, recipe.LockPackage{
				Name:    cur["Package"],
				Version: cur["Version"],
				Arch:    cur["Architecture"],
			})
		}
		cur = nil
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024) // long Description fields
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue // continuation of a multi-line field
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		if cur == nil {
			cur = map[string]string{}
		}
		cur[key] = strings.TrimSpace(val)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	flush()
	if len(out) == 0 {
		return nil, errors.New("no installed packages found in dpkg status")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}
```

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/builder/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/builder/status.go internal/builder/status_test.go
git commit -m "feat: parse dpkg status into lock packages"
```

---

### Task 6: Provision files and mmdebstrap hooks

**Files:**
- Create: `internal/builder/provision.go`
- Test: `internal/builder/provision_test.go`

**Interfaces:**
- Consumes: `recipe.Recipe`, `recipe.DefaultUser`
- Produces: `builder.Essentials`, `builder.MergeInclude`, `builder.RenderWSLConf`, `builder.RenderSudoers`, `builder.Stage`, `builder.WriteStage`, `builder.CustomizeHooks`

Three rules for this task, all of them things the previous plan got wrong:

1. **Go renders file contents; hooks place them.** `RenderWSLConf` and
   `RenderSudoers` return strings that are unit-tested directly. The builder
   writes them into the work directory and mmdebstrap's `upload` special hook
   copies them in. There is exactly one code path that produces these files.
2. **No `|| true`.** A failed `useradd` used to report a successful build and
   ship an image that logs in as root. Every hook must fail the build.
3. **Every interpolated value is shell-quoted**, even though Task 2 validates
   them. Defence in depth at a shell boundary is not optional.

`Essentials` now carries systemd. Without it `[boot] systemd=true` is inert and
spec criteria 4 and 6 cannot be met. `ca-certificates` is there because
Recommends alone does not guarantee it early enough, and an image where
`git clone https://…` fails is a support ticket on day one.

- [ ] **Step 1: Write the failing test**

```go
package builder

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

func testRecipe() recipe.Recipe {
	return recipe.Recipe{
		Image:    recipe.Image{Name: "cpp-lab", Release: "22.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Europe/Istanbul"},
		Packages: recipe.Packages{Include: []string{"git", "build-essential", "cmake"}},
	}
}

func TestMergeIncludeAddsEssentialsIncludingSystemd(t *testing.T) {
	got := MergeInclude([]string{"git", "sudo"})
	for _, need := range []string{"git", "sudo", "systemd", "systemd-sysv", "dbus", "locales", "tzdata", "passwd", "ca-certificates"} {
		if !contains(got, need) {
			t.Fatalf("missing %s in %v", need, got)
		}
	}
	if count(got, "sudo") != 1 {
		t.Fatalf("sudo duplicated: %v", got)
	}
}

func TestRenderWSLConf(t *testing.T) {
	body := RenderWSLConf(testRecipe())
	if !strings.Contains(body, "[boot]") || !strings.Contains(body, "systemd=true") {
		t.Fatalf("wsl.conf: %s", body)
	}
	if !strings.Contains(body, "[user]") || !strings.Contains(body, "default=student") {
		t.Fatalf("wsl.conf: %s", body)
	}
}

func TestRenderWSLConfWithoutSystemd(t *testing.T) {
	r := testRecipe()
	r.WSL.Systemd = false
	body := RenderWSLConf(r)
	if strings.Contains(body, "systemd=true") {
		t.Fatalf("wsl.conf: %s", body)
	}
	if !strings.Contains(body, "default=student") {
		t.Fatalf("wsl.conf: %s", body)
	}
}

func TestRenderSudoers(t *testing.T) {
	if got := RenderSudoers(testRecipe()); !strings.Contains(got, "student ALL=(ALL) NOPASSWD:ALL") {
		t.Fatalf("sudoers: %q", got)
	}
	r := testRecipe()
	r.User.Sudo = false
	if got := RenderSudoers(r); got != "" {
		t.Fatalf("no sudo means no sudoers file, got %q", got)
	}
}

func TestWriteStageWritesRenderedFiles(t *testing.T) {
	dir := t.TempDir()
	st, err := WriteStage(dir, testRecipe())
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(st.WSLConf)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "systemd=true") {
		t.Fatalf("staged wsl.conf: %s", body)
	}
	if st.Sudoers == "" {
		t.Fatal("expected a staged sudoers file")
	}
}

func TestWriteStageNoSudo(t *testing.T) {
	r := testRecipe()
	r.User.Sudo = false
	st, err := WriteStage(t.TempDir(), r)
	if err != nil {
		t.Fatal(err)
	}
	if st.Sudoers != "" {
		t.Fatalf("sudoers should not be staged: %q", st.Sudoers)
	}
}

func TestCustomizeHooksNeverSwallowFailures(t *testing.T) {
	hooks := CustomizeHooks(testRecipe(), Stage{WSLConf: "/w/wsl.conf", Sudoers: "/w/sudoers", StatusOut: "/w/dpkg-status"})
	all := strings.Join(hooks, "\n")
	if strings.Contains(all, "|| true") {
		t.Fatalf("hooks must fail closed:\n%s", all)
	}
}

func TestCustomizeHooksContent(t *testing.T) {
	hooks := CustomizeHooks(testRecipe(), Stage{WSLConf: "/w/wsl.conf", Sudoers: "/w/sudoers", StatusOut: "/w/dpkg-status"})
	all := strings.Join(hooks, "\n")
	for _, need := range []string{
		"upload /w/wsl.conf /etc/wsl.conf",
		"upload /w/sudoers /etc/sudoers.d/",
		"chmod 0440",
		"useradd",
		"student",
		"en_US.UTF-8",
		"Europe/Istanbul",
		"zoneinfo",
		"download /var/lib/dpkg/status /w/dpkg-status",
	} {
		if !strings.Contains(all, need) {
			t.Fatalf("hooks missing %q:\n%s", need, all)
		}
	}
}

func TestCustomizeHooksVerifyTimezoneExists(t *testing.T) {
	// Validate() cannot check this; it needs the chroot. A bad timezone must
	// fail the build, not silently leave UTC.
	hooks := CustomizeHooks(testRecipe(), Stage{StatusOut: "/w/s"})
	all := strings.Join(hooks, "\n")
	if !strings.Contains(all, "test -e") || !strings.Contains(all, "zoneinfo/'Europe/Istanbul'") {
		t.Fatalf("expected a zoneinfo existence check:\n%s", all)
	}
}

func TestCustomizeHooksQuoteInterpolatedValues(t *testing.T) {
	r := testRecipe()
	r.User.Name = "student"
	hooks := CustomizeHooks(r, Stage{StatusOut: "/w/s"})
	all := strings.Join(hooks, "\n")
	if !strings.Contains(all, "'student'") {
		t.Fatalf("values must be single-quoted:\n%s", all)
	}
}

func TestCustomizeHooksRemoveHostArtifacts(t *testing.T) {
	// mmdebstrap copies the host's resolv.conf and hostname into the chroot
	// and leaves them there. A golden image must not carry them.
	all := strings.Join(CustomizeHooks(testRecipe(), Stage{StatusOut: "/w/s"}), "\n")
	if !strings.Contains(all, "/etc/resolv.conf") || !strings.Contains(all, "/etc/hostname") {
		t.Fatalf("expected host artifact cleanup:\n%s", all)
	}
}

func TestCustomizeHooksNoSudoOmitsSudoers(t *testing.T) {
	r := testRecipe()
	r.User.Sudo = false
	all := strings.Join(CustomizeHooks(r, Stage{StatusOut: "/w/s"}), "\n")
	if strings.Contains(all, "sudoers") {
		t.Fatalf("no sudo means no sudoers hook:\n%s", all)
	}
}

func contains(xs []string, w string) bool {
	for _, x := range xs {
		if x == w {
			return true
		}
	}
	return false
}

func count(xs []string, w string) int {
	n := 0
	for _, x := range xs {
		if x == w {
			n++
		}
	}
	return n
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/builder/ -v`

Expected: FAIL, undefined symbols.

- [ ] **Step 3: Write minimal implementation**

```go
package builder

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"frostroot/internal/recipe"
)

// Essentials are added to every build. systemd and systemd-sysv are here
// because --variant=important does not include them, and without systemd the
// "[boot] systemd=true" in wsl.conf does nothing. ca-certificates is here so
// "git clone https://..." works in the image.
var Essentials = []string{
	"systemd", "systemd-sysv", "dbus",
	"sudo", "locales", "tzdata", "passwd", "ca-certificates",
}

func MergeInclude(user []string) []string {
	seen := map[string]bool{}
	var out []string
	add := func(p string) {
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}
	for _, p := range user {
		add(p)
	}
	for _, p := range Essentials {
		add(p)
	}
	return out
}

func RenderWSLConf(r recipe.Recipe) string {
	var b strings.Builder
	if r.WSL.Systemd {
		b.WriteString("[boot]\nsystemd=true\n\n")
	}
	fmt.Fprintf(&b, "[user]\ndefault=%s\n", recipe.DefaultUser(r))
	return b.String()
}

func RenderSudoers(r recipe.Recipe) string {
	if !r.User.Sudo {
		return ""
	}
	return fmt.Sprintf("%s ALL=(ALL) NOPASSWD:ALL\n", r.User.Name)
}

// Stage holds host-side paths that hooks reference.
type Stage struct {
	WSLConf   string // staged /etc/wsl.conf, uploaded into the chroot
	Sudoers   string // staged sudoers drop-in; empty when sudo is false
	StatusOut string // where the dpkg status file is downloaded to
}

func WriteStage(dir string, r recipe.Recipe) (Stage, error) {
	var st Stage
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return st, err
	}
	st.WSLConf = filepath.Join(dir, "wsl.conf")
	if err := os.WriteFile(st.WSLConf, []byte(RenderWSLConf(r)), 0o644); err != nil {
		return st, err
	}
	if body := RenderSudoers(r); body != "" {
		st.Sudoers = filepath.Join(dir, "sudoers")
		if err := os.WriteFile(st.Sudoers, []byte(body), 0o644); err != nil {
			return st, err
		}
	}
	st.StatusOut = filepath.Join(dir, "dpkg-status")
	return st, nil
}

// sq single-quotes a value for safe interpolation into a hook script.
// Task 2 already validates these values; this is the second line of defence.
func sq(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

func CustomizeHooks(r recipe.Recipe, st Stage) []string {
	user := r.User.Name
	lang := r.Locale.Lang
	if lang == "" {
		lang = "en_US.UTF-8"
	}
	tz := r.Locale.Timezone
	if tz == "" {
		tz = "UTC"
	}

	var hooks []string
	if st.WSLConf != "" {
		hooks = append(hooks, fmt.Sprintf("upload %s /etc/wsl.conf", st.WSLConf))
	}
	if st.Sudoers != "" {
		drop := "/etc/sudoers.d/90-frostroot"
		hooks = append(hooks,
			fmt.Sprintf("upload %s %s", st.Sudoers, drop),
			fmt.Sprintf(`chmod 0440 "$1"%s`, sq(drop)),
		)
	}
	hooks = append(hooks,
		// Fail the build if the timezone does not exist in the chroot.
		fmt.Sprintf(`test -e "$1"/usr/share/zoneinfo/%s`, sq(tz)),
		fmt.Sprintf(`chroot "$1" useradd --create-home --shell /bin/bash --user-group %s`, sq(user)),
		fmt.Sprintf(`chroot "$1" sh -ec 'echo %s UTF-8 >> /etc/locale.gen; locale-gen; update-locale LANG=%s'`, sq(lang), sq(lang)),
		fmt.Sprintf(`chroot "$1" sh -ec 'ln -sf /usr/share/zoneinfo/%s /etc/localtime; echo %s > /etc/timezone'`, sq(tz), sq(tz)),
		// mmdebstrap copies these from the host and leaves them behind.
		`rm -f "$1"/etc/resolv.conf "$1"/etc/hostname`,
		fmt.Sprintf("download /var/lib/dpkg/status %s", st.StatusOut),
	)
	return hooks
}
```

Notes:

- Default cleanup already empties `/etc/machine-id` and removes apt lists and
  cache, so the old machine-id and apt-lists hooks are gone. Do not add them
  back.
- `useradd --user-group` gives the user their own group explicitly rather than
  relying on `USERGROUPS_ENAB`.
- The `download` hook must be last so the status file reflects everything
  installed.
- **If the Task 0 spike showed `systemd-resolved` fighting WSL's generated
  `/etc/resolv.conf`**, add the masking hook here and a test asserting it. Do
  not guess: only add it if the spike reproduced the problem.

There is no `WriteProvisionFiles` in production code. If you need rootfs files
in a test, write them in the test.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/builder/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/builder/provision.go internal/builder/provision_test.go
git commit -m "feat: render provision files and fail-closed customize hooks"
```

---

### Task 7: Builder orchestration with fakes

**Files:**
- Create: `internal/builder/builder.go`
- Create: `internal/builder/workdir.go`
- Test: `internal/builder/builder_test.go`, `internal/builder/workdir_test.go`

**Interfaces:**
- Consumes: `recipe`, `distro`, `export`, `MergeInclude`, `CustomizeHooks`, `WriteStage`, `ParseDpkgStatus`
- Produces: `builder.Version`, `builder.Bootstrapper`, `builder.BootstrapSpec`, `builder.Options`, `builder.Result`, `(*Builder).Build`, `ErrNotLinux`, `ErrNoMmdebstrap`, `builder.WorkRoot`

```go
type BootstrapSpec struct {
	Suite      string
	Sources    []string // full "deb URL suite components" lines, all three pockets
	Include    []string
	Hooks      []string
	TarPath    string // mmdebstrap writes the tarball here, inside the namespace
	WorkDir    string // becomes TMPDIR for the child
	Arch       string
	Recommends bool
	Keyring    string
}

type Bootstrapper interface {
	Run(ctx context.Context, spec BootstrapSpec) error
}
```

`context.Context` is on the interface from the start — adding it later is churn
across every fake. There is no `PackageQuerier`: the bootstrapper produces a
dpkg status file as a side effect of its hooks, and the builder parses it. The
fake produces exactly the two artifacts the real one does (tarball + status
file), which is what makes these tests meaningful rather than decorative.

**`ErrNoPrivilege` is deliberately not declared.** The previous plan declared it
and never returned it. mmdebstrap's own stderr explains a missing userns better
than we can; Task 11 streams it.

- [ ] **Step 1: Write the failing tests**

```go
package builder

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

type fakeBoot struct {
	err     error
	spec    BootstrapSpec
	status  string // written to spec's status path; default sample
	noTar   bool
}

func (f *fakeBoot) Run(ctx context.Context, spec BootstrapSpec) error {
	f.spec = spec
	if f.err != nil {
		return f.err
	}
	if !f.noTar {
		if err := os.WriteFile(spec.TarPath, []byte("tar-bytes"), 0o644); err != nil {
			return err
		}
	}
	body := f.status
	if body == "" {
		body = "Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:2.34.1-1ubuntu1.11\n\n" +
			"Package: libc6\nStatus: install ok installed\nArchitecture: amd64\nVersion: 2.35-0ubuntu3.8\n"
	}
	// The real hook downloads this out of the chroot; the path comes from
	// the hooks the builder generated, so derive it the same way.
	return os.WriteFile(filepath.Join(spec.WorkDir, "stage", "dpkg-status"), []byte(body), 0o644)
}

func TestBuildSuccessWritesLockAndTarball(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBoot{}
	b := Builder{Bootstrap: boot}
	res, err := b.Build(context.Background(), testRecipe(), Options{Dir: dir, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if boot.spec.Suite != "jammy" {
		t.Fatalf("suite %q", boot.spec.Suite)
	}
	if len(boot.spec.Sources) != 3 {
		t.Fatalf("expected three pockets, got %#v", boot.spec.Sources)
	}
	if !strings.Contains(boot.spec.Sources[1], "jammy-updates") || !strings.Contains(boot.spec.Sources[2], "jammy-security") {
		t.Fatalf("pockets: %#v", boot.spec.Sources)
	}
	if !boot.spec.Recommends {
		t.Fatal("Recommends must be on")
	}
	if boot.spec.Arch != "amd64" {
		t.Fatalf("arch %q", boot.spec.Arch)
	}
	for _, need := range []string{"git", "systemd", "systemd-sysv", "sudo", "ca-certificates"} {
		if !contains(boot.spec.Include, need) {
			t.Fatalf("include %v missing %s", boot.spec.Include, need)
		}
	}
	lock, err := recipe.LoadLock(filepath.Join(dir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Version != 1 || lock.Suite != "jammy" || lock.FrostrootVersion != Version {
		t.Fatalf("lock %+v", lock)
	}
	if len(lock.Sources) != 3 {
		t.Fatalf("lock must record the sources used: %#v", lock.Sources)
	}
	if len(lock.Requested) != 3 || lock.Requested[0] != "git" {
		t.Fatalf("requested %#v", lock.Requested)
	}
	if len(lock.Packages) != 2 || lock.Packages[0].Name != "git" {
		t.Fatalf("packages %#v", lock.Packages)
	}
	tarPath := filepath.Join(dir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if _, err := os.Stat(tarPath); err != nil {
		t.Fatal(err)
	}
	if res.TarballPath != tarPath {
		t.Fatalf("result path %q", res.TarballPath)
	}
	if res.WorkDir != "" {
		t.Fatalf("workdir should be removed on success: %q", res.WorkDir)
	}
}

func TestBuildRequestedExcludesEssentials(t *testing.T) {
	dir := t.TempDir()
	b := Builder{Bootstrap: &fakeBoot{}}
	if _, err := b.Build(context.Background(), testRecipe(), Options{Dir: dir, GOOS: "linux"}); err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(filepath.Join(dir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if contains(lock.Requested, "systemd") {
		t.Fatalf("requested is the recipe list as written: %#v", lock.Requested)
	}
}

func TestBuildKeepWork(t *testing.T) {
	dir := t.TempDir()
	b := Builder{Bootstrap: &fakeBoot{}}
	res, err := b.Build(context.Background(), testRecipe(), Options{Dir: dir, GOOS: "linux", KeepWork: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkDir == "" {
		t.Fatal("expected workdir")
	}
	if _, err := os.Stat(res.WorkDir); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFailureKeepsWorkdirAndWritesNoArtifacts(t *testing.T) {
	dir := t.TempDir()
	b := Builder{Bootstrap: &fakeBoot{err: errors.New("mmdebstrap exploded")}}
	res, err := b.Build(context.Background(), testRecipe(), Options{Dir: dir, GOOS: "linux"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "mmdebstrap exploded") {
		t.Fatalf("got %v", err)
	}
	if res.WorkDir == "" {
		t.Fatal("expected kept workdir")
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.lock")); !os.IsNotExist(err) {
		t.Fatalf("lock must be absent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("tarball must be absent: %v", err)
	}
}

func TestBuildLeavesNoTmpLockOnFailure(t *testing.T) {
	dir := t.TempDir()
	b := Builder{Bootstrap: &fakeBoot{noTar: true}} // status written, tarball missing
	if _, err := b.Build(context.Background(), testRecipe(), Options{Dir: dir, GOOS: "linux"}); err == nil {
		t.Fatal("expected error when the tarball is missing")
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.lock.tmp")); !os.IsNotExist(err) {
		t.Fatalf("tmp lock must be cleaned up: %v", err)
	}
}

func TestBuildMirrorOverrideReplacesAllPockets(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBoot{}
	b := Builder{Bootstrap: boot}
	_, err := b.Build(context.Background(), testRecipe(), Options{Dir: dir, GOOS: "linux", Mirror: "http://mirror.example/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range boot.spec.Sources {
		if !strings.Contains(line, "mirror.example") {
			t.Fatalf("override missed: %q", line)
		}
	}
}

func TestBuildNotLinux(t *testing.T) {
	b := Builder{Bootstrap: &fakeBoot{}}
	_, err := b.Build(context.Background(), testRecipe(), Options{Dir: t.TempDir(), GOOS: "windows"})
	if !errors.Is(err, ErrNotLinux) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildFocalUsesOldReleases(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBoot{}
	b := Builder{Bootstrap: boot}
	r := testRecipe()
	r.Image.Release = "20.04"
	if _, err := b.Build(context.Background(), r, Options{Dir: dir, GOOS: "linux"}); err != nil {
		t.Fatal(err)
	}
	if boot.spec.Suite != "focal" {
		t.Fatalf("suite %q", boot.spec.Suite)
	}
	for _, line := range boot.spec.Sources {
		if !strings.Contains(line, "old-releases.ubuntu.com") {
			t.Fatalf("focal must use old-releases: %q", line)
		}
	}
}

func TestBuildCancelledContextPropagates(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b := Builder{Bootstrap: &fakeBoot{err: context.Canceled}}
	_, err := b.Build(ctx, testRecipe(), Options{Dir: t.TempDir(), GOOS: "linux"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildEmptyStatusIsAnError(t *testing.T) {
	// A bootstrap that installs nothing must not produce a lock.
	dir := t.TempDir()
	b := Builder{Bootstrap: &fakeBoot{status: "\n"}}
	if _, err := b.Build(context.Background(), testRecipe(), Options{Dir: dir, GOOS: "linux"}); err == nil {
		t.Fatal("expected error for empty status")
	}
}
```

`workdir_test.go`:

```go
func TestWorkRootRefusesMnt(t *testing.T) {
	// /mnt/<drive> under WSL is 9p: slow, and unreliable for chown and
	// device nodes. Never bootstrap there.
	if _, err := WorkRoot("/mnt/d/projects/frostroot", func(string) string { return "" }); err == nil {
		t.Fatal("expected refusal for a /mnt path")
	}
}

func TestWorkRootPrefersXDGCache(t *testing.T) {
	cache := t.TempDir()
	got, err := WorkRoot("/home/u/proj", func(k string) string {
		if k == "XDG_CACHE_HOME" {
			return cache
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, cache) {
		t.Fatalf("got %q want under %q", got, cache)
	}
}

func TestWorkRootFallsBackToVarTmp(t *testing.T) {
	got, err := WorkRoot("/home/u/proj", func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	if got != "/var/tmp/frostroot" {
		t.Fatalf("got %q", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/builder/ -run "TestBuild|TestWorkRoot" -v`

Expected: FAIL, undefined `Builder`.

- [ ] **Step 3: Write minimal implementation**

`workdir.go`:

```go
package builder

import (
	"fmt"
	"path/filepath"
	"strings"
)

// WorkRoot picks where bootstrap work happens. Never under /mnt: on WSL that
// is a 9p mount of a Windows drive, which is slow and unreliable for the
// chown and device-node operations a bootstrap performs.
func WorkRoot(recipeDir string, getenv func(string) string) (string, error) {
	abs, err := filepath.Abs(recipeDir)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(filepath.ToSlash(abs), "/mnt/") {
		if cache := getenv("XDG_CACHE_HOME"); cache != "" {
			return filepath.Join(cache, "frostroot"), nil
		}
		if home := getenv("HOME"); home != "" && !strings.HasPrefix(home, "/mnt/") {
			return filepath.Join(home, ".cache", "frostroot"), nil
		}
		return "", fmt.Errorf("refusing to build under %s (9p mount); set XDG_CACHE_HOME to a Linux filesystem", abs)
	}
	if cache := getenv("XDG_CACHE_HOME"); cache != "" {
		return filepath.Join(cache, "frostroot"), nil
	}
	return "/var/tmp/frostroot", nil
}
```

Note the asymmetry the tests pin down: a `/mnt` recipe directory with no
`XDG_CACHE_HOME` and no usable `HOME` is an error with an actionable message,
not a silent fallback.

`builder.go`:

```go
const Version = "0.1.0"

var (
	ErrNotLinux     = errors.New("frostroot build requires Linux")
	ErrNoMmdebstrap = errors.New("mmdebstrap not found on PATH")
)

type Options struct {
	Dir      string // recipe directory; lock and dist/ go here
	Mirror   string
	KeepWork bool
	GOOS     string
	Getenv   func(string) string // default os.Getenv
}

type Result struct {
	LockPath    string
	TarballPath string
	WorkDir     string // set when kept: --keep-work, or any failure
}

type Builder struct {
	Bootstrap Bootstrapper
	MkdirTemp func(dir, pattern string) (string, error) // default os.MkdirTemp
}
```

`Build(ctx, r, opts)` in order:

1. `GOOS` must be `linux`, else `ErrNotLinux`.
2. `distro.Lookup(r.Image.Release, r.Image.Arch)`.
3. `WorkRoot(opts.Dir, getenv)`, `os.MkdirAll` it, then `MkdirTemp(root, "build-*")`.
4. `WriteStage(filepath.Join(work, "stage"), r)`.
5. `CustomizeHooks(r, stage)` and `MergeInclude(r.Packages.Include)`.
6. `Bootstrap.Run(ctx, BootstrapSpec{...TarPath: filepath.Join(work, "image.tar.gz"), WorkDir: work, Recommends: true, Arch: r.Image.Arch, Sources: info.Sources(opts.Mirror)})`.
7. Open `stage.StatusOut`, `ParseDpkgStatus` it.
8. Build the `Lockfile` (`Mirror` is the base URL actually used; `Sources` the three lines) and `SaveLock` to `frostroot.lock.tmp`.
9. `export.Place(work/image.tar.gz, opts.Dir/TarballRelPath(...))`. If the tarball is missing, that is an error — remove the tmp lock first.
10. `os.Rename` the tmp lock into place.
11. On success remove the work directory unless `KeepWork`; on any failure keep it and set `Result.WorkDir`.

Ordering rules, unchanged from the spec and load-bearing:

- The lock is renamed into place **after** the tarball lands, so a failed
  placement never leaves a lock describing an image that does not exist.
- Any failure removes `frostroot.lock.tmp`.
- A failure never deletes the work directory; it is the only debugging evidence.
- Sort `lock.Packages` by name (`ParseDpkgStatus` already does) so a no-op
  rebuild produces an empty git diff.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./... -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/builder/builder.go internal/builder/workdir.go internal/builder/builder_test.go internal/builder/workdir_test.go
git commit -m "feat: orchestrate build with fakeable bootstrapper"
```

---

### Task 8: CLI validate

**Files:**
- Create: `internal/cli/app.go`
- Create: `cmd/frostroot/main.go`
- Test: `internal/cli/app_test.go`

**Interfaces:**
- Consumes: `recipe.Load`, `recipe.Validate`
- Produces: `cli.App`, `cli.Prompt`, `(*App).Run(args []string) int`

```go
type Prompt interface {
	Ask(question, defaultValue string) (string, error)
}

type App struct {
	Stdin    io.Reader
	Stdout   io.Writer
	Stderr   io.Writer
	Dir      string
	GOOS     string
	Prompt   Prompt
	Builder  *builder.Builder
	LookPath func(file string) (string, error)
	Getenv   func(string) string
}
```

Defaults: `os.Std*`, `Dir` = cwd, `GOOS` = `runtime.GOOS`, `LookPath` =
`exec.LookPath`, `Getenv` = `os.Getenv`. Prompting goes through the interface so
tests never need a TTY.

Unchanged from the previous plan apart from the `Getenv` field and dropping the
Windows-specific test workarounds.

- [ ] **Step 1: Write the failing test**

```go
func TestValidateOK(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := App{Stdout: &out, Stderr: &errb, Dir: dir}
	if code := app.Run([]string{"validate"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("bad-user.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := App{Stdout: &out, Stderr: &errb, Dir: dir}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "user name") {
		t.Fatalf("stderr %s", errb.String())
	}
}

func TestValidateBadRelease(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("bad-release.toml"), filepath.Join(dir, "frostroot.toml"))
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: dir}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestValidateBadLocale(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("bad-locale.toml"), filepath.Join(dir, "frostroot.toml"))
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: dir}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestValidateMissingFile(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: t.TempDir()}
	if code := app.Run([]string{"validate"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "frostroot.toml") {
		t.Fatalf("stderr should name the file: %s", errb.String())
	}
}

func TestUnknownVerbAndNoArgs(t *testing.T) {
	for _, args := range [][]string{{}, {"frobnicate"}} {
		var errb bytes.Buffer
		app := App{Stdout: io.Discard, Stderr: &errb, Dir: t.TempDir()}
		if code := app.Run(args); code != 1 {
			t.Fatalf("args %v: code %d", args, code)
		}
		if !strings.Contains(errb.String(), "usage") {
			t.Fatalf("args %v: stderr %s", args, errb.String())
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -v`

Expected: FAIL, undefined `App`.

- [ ] **Step 3: Write minimal implementation**

`App.Run` switches on `args[0]`: `init`, `validate`, `build`. Anything else (and
the empty case) prints usage to stderr and returns 1. `cmdValidate` loads
`filepath.Join(a.Dir, "frostroot.toml")`, prints every problem from
`recipe.Validate` one per line to stderr, returns 1 if there are any, else 0.

Keep `cmdInit` and `cmdBuild` as stubs returning 1 with "not implemented" so the
switch is exhaustive and `main` compiles; Tasks 9 and 10 replace them entirely.

`cmd/frostroot/main.go`:

```go
func main() { os.Exit(cli.New().Run(os.Args[1:])) }
```

`cli.New()` fills the defaults. Do not leave `main` uncompilable at any commit.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./... && go build ./cmd/frostroot/`

Expected: PASS and a binary.

- [ ] **Step 5: Commit**

```
git add cmd internal/cli testdata
git commit -m "feat: add frostroot validate command"
```

---

### Task 9: CLI init wizard

**Files:**
- Create: `internal/cli/initcmd.go`
- Modify: `internal/cli/app_test.go`

**Interfaces:**
- Consumes: `Prompt`, `recipe.Save`, `recipe.Validate`
- Produces: working `init` and `init --force`

Presets live only in `init`; they are not a recipe feature.

```go
var presets = map[string][]string{
	"none":            {},
	"build-essential": {"build-essential", "git", "cmake", "pkg-config"},
	"python-lab":      {"python3", "python3-pip", "python3-venv", "git"},
}
```

Prompt order: image name (default `lab`), release (default `24.04`), username
(default `student`), timezone (default `UTC`), preset (default `none`), extra
comma-separated packages (appended to the preset).

Two changes from the previous plan:

- **Write the file from a text template, not `toml.Marshal`.** Marshal cannot
  emit the section comments the spec's example shows, and this file is meant to
  be read and hand-edited.
- **The result must validate.** `init` writing a recipe that `validate` then
  rejects is a bug; assert it in the test.

- [ ] **Step 1: Write the failing test**

```go
type scriptedPrompt struct {
	answers []string
	i       int
}

func (s *scriptedPrompt) Ask(question, defaultValue string) (string, error) {
	if s.i >= len(s.answers) {
		return defaultValue, nil
	}
	a := s.answers[s.i]
	s.i++
	if a == "" {
		return defaultValue, nil
	}
	return a, nil
}

func TestInitWritesValidRecipe(t *testing.T) {
	dir := t.TempDir()
	app := App{
		Stdout: io.Discard, Stderr: io.Discard, Dir: dir,
		Prompt: &scriptedPrompt{answers: []string{"cpp-lab", "22.04", "student", "Europe/Istanbul", "build-essential", "cmake"}},
	}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	r, err := recipe.Load(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Image.Name != "cpp-lab" || r.Image.Release != "22.04" || r.Image.Arch != "amd64" {
		t.Fatalf("image %+v", r.Image)
	}
	if r.Locale.Timezone != "Europe/Istanbul" {
		t.Fatalf("timezone %q", r.Locale.Timezone)
	}
	if !contains(r.Packages.Include, "build-essential") || !contains(r.Packages.Include, "cmake") {
		t.Fatalf("include %#v", r.Packages.Include)
	}
	if probs := recipe.Validate(r); len(probs) != 0 {
		t.Fatalf("init wrote a recipe that does not validate: %v", probs)
	}
}

func TestInitDefaults(t *testing.T) {
	dir := t.TempDir()
	app := App{Stdout: io.Discard, Stderr: io.Discard, Dir: dir, Prompt: &scriptedPrompt{}}
	if code := app.Run([]string{"init"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	r, err := recipe.Load(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Image.Release != "24.04" || r.User.Name != "student" || r.Locale.Timezone != "UTC" {
		t.Fatalf("defaults wrong: %+v", r)
	}
	if !r.User.Sudo || !r.WSL.Systemd {
		t.Fatalf("lab defaults wrong: %+v", r)
	}
}

func TestInitRefusesToClobber(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frostroot.toml")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: dir, Prompt: &scriptedPrompt{}}
	if code := app.Run([]string{"init"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "--force") {
		t.Fatalf("stderr should mention --force: %s", errb.String())
	}
	body, _ := os.ReadFile(path)
	if string(body) != "original" {
		t.Fatal("existing recipe must not be touched")
	}
}

func TestInitForceOverwrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frostroot.toml")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := App{Stdout: io.Discard, Stderr: io.Discard, Dir: dir, Prompt: &scriptedPrompt{}}
	if code := app.Run([]string{"init", "--force"}); code != 0 {
		t.Fatalf("code %d", code)
	}
	if _, err := recipe.Load(path); err != nil {
		t.Fatal(err)
	}
}

func TestInitRejectsBadAnswers(t *testing.T) {
	dir := t.TempDir()
	var errb bytes.Buffer
	app := App{
		Stdout: io.Discard, Stderr: &errb, Dir: dir,
		Prompt: &scriptedPrompt{answers: []string{"cpp-lab", "18.04", "student", "UTC", "none", ""}},
	}
	if code := app.Run([]string{"init"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.toml")); !os.IsNotExist(err) {
		t.Fatal("must not write an invalid recipe")
	}
}

func TestInitWritesComments(t *testing.T) {
	dir := t.TempDir()
	app := App{Stdout: io.Discard, Stderr: io.Discard, Dir: dir, Prompt: &scriptedPrompt{}}
	app.Run([]string{"init"})
	body, err := os.ReadFile(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "#") {
		t.Fatal("recipe is hand-edited; it should carry explanatory comments")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestInit -v`

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

`cmdInit` parses `--force`, refuses when `frostroot.toml` exists without it,
asks the six questions, builds a `recipe.Recipe` (`arch = "amd64"`,
`user.sudo = true`, `wsl.systemd = true`, `wsl.default_user` = the username,
`locale.lang = "en_US.UTF-8"`), runs `recipe.Validate` and prints problems and
returns 1 if any, then renders a `text/template` to `frostroot.toml`.

Template comments should at minimum say that versions belong in the lock, not
here, and list the legal releases. If the preset is `python-lab`, print a note
after writing: Ubuntu 24.04 enforces PEP 668, so `pip install` outside a
virtualenv fails — use `python3 -m venv`. That note heads off the first support
question; put the same line in the README.

`init` does not need root, writes no lock and no tarball, and touches no network.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./... -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/cli
git commit -m "feat: add frostroot init wizard"
```

---

### Task 10: CLI build, signals, and exit codes

**Files:**
- Modify: `internal/cli/app.go` (`cmdBuild`)
- Modify: `internal/cli/app_test.go`

**Interfaces:**
- Consumes: `recipe.Load`, `recipe.Validate`, `builder.Builder.Build`, `LookPath("mmdebstrap")`, `distro.Lookup`
- Produces: working `build`, `--mirror`, `--keep-work`, the 20.04 warning, the `wsl --import` line, and exit 130 on Ctrl-C

Error mapping:

| Condition | Exit |
|---|---|
| not Linux, missing or invalid `frostroot.toml`, mmdebstrap not on PATH | 1 |
| bootstrap, provision, lock or placement failure | 2 |
| Ctrl-C | 130 |

Three additions the previous plan lacked:

- **Ctrl-C handling.** The spec requires 130 and there was no task for it.
  `signal.NotifyContext(context.Background(), os.Interrupt)` in `cmdBuild`, pass
  the context to `Build`, and map `context.Canceled` to 130. Tmp artifacts are
  removed, the work directory is kept — same as any other failure.
- **The 20.04 warning.** `distro.Info.EOL` is true for focal. Print to stderr
  before building: the image will contain packages with known unfixed CVEs,
  because old-releases is frozen at end of standard support and security fixes
  need Ubuntu Pro. Freezing an old distro is the point of the tool; shipping one
  to a classroom without saying so is not.
- **A pasteable import line under WSL.** If `wslpath` is on PATH, print the
  Windows form of the tarball path so it can go straight into PowerShell.

- [ ] **Step 1: Write the failing tests**

```go
type stubBoot struct{ err error }

func (s stubBoot) Run(ctx context.Context, spec builder.BootstrapSpec) error {
	if s.err != nil {
		return s.err
	}
	if err := os.WriteFile(spec.TarPath, []byte("tar"), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(spec.WorkDir, "stage", "dpkg-status"),
		[]byte("Package: git\nStatus: install ok installed\nArchitecture: amd64\nVersion: 1:1\n"), 0o644)
}

func newApp(t *testing.T, dir string, boot builder.Bootstrapper, out, errb *bytes.Buffer) App {
	t.Helper()
	return App{
		Stdout: out, Stderr: errb, Dir: dir, GOOS: "linux",
		LookPath: func(string) (string, error) { return "/usr/bin/mmdebstrap", nil },
		Getenv:   func(string) string { return "" },
		Builder:  &builder.Builder{Bootstrap: boot},
	}
}

func TestBuildSuccess(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.lock")); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "wsl --import cpp-lab") {
		t.Fatalf("stdout %s", out.String())
	}
	if !strings.Contains(out.String(), "cpp-lab-ubuntu-22.04-amd64.tar.gz") {
		t.Fatalf("stdout %s", out.String())
	}
}

func TestBuildWarnsOnEOLRelease(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	mutateRelease(t, filepath.Join(dir, "frostroot.toml"), "20.04")
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	low := strings.ToLower(errb.String())
	if !strings.Contains(low, "20.04") || !strings.Contains(low, "security") {
		t.Fatalf("expected an EOL warning naming the release: %s", errb.String())
	}
}

func TestBuildDoesNotWarnOnSupportedRelease(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{}, &out, &errb)
	app.Run([]string{"build"})
	if strings.Contains(strings.ToLower(errb.String()), "unfixed") {
		t.Fatalf("no warning expected for 22.04: %s", errb.String())
	}
}

func TestBuildNotLinuxExit1(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{}, &out, &errb)
	app.GOOS = "windows"
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "WSL") {
		t.Fatalf("message should point Windows users at WSL: %s", errb.String())
	}
}

func TestBuildMissingMmdebstrapExit1(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{}, &out, &errb)
	app.LookPath = func(string) (string, error) { return "", os.ErrNotExist }
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "sudo apt install mmdebstrap") {
		t.Fatalf("stderr %s", errb.String())
	}
}

func TestBuildBootstrapFailureExit2(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{err: errors.New("mmdebstrap exploded")}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 2 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "mmdebstrap exploded") {
		t.Fatalf("stderr %s", errb.String())
	}
	if !strings.Contains(errb.String(), "workdir") {
		t.Fatalf("failure must print the kept workdir: %s", errb.String())
	}
}

func TestBuildInterruptedExit130(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{err: context.Canceled}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 130 {
		t.Fatalf("code %d", code)
	}
	if _, err := os.Stat(filepath.Join(dir, "frostroot.lock")); !os.IsNotExist(err) {
		t.Fatal("interrupt must not leave a lock")
	}
}

func TestBuildInvalidRecipeExit1(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("bad-user.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := newApp(t, dir, stubBoot{}, &out, &errb)
	if code := app.Run([]string{"build"}); code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestBuildMirrorFlagReachesBuilder(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	rec := &recordBoot{}
	var out, errb bytes.Buffer
	app := newApp(t, dir, rec, &out, &errb)
	if code := app.Run([]string{"build", "--mirror", "http://mirror.example/ubuntu"}); code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	for _, line := range rec.spec.Sources {
		if !strings.Contains(line, "mirror.example") {
			t.Fatalf("mirror not applied: %#v", rec.spec.Sources)
		}
	}
}
```

`recordBoot` is `stubBoot` that also captures the spec. `mutateRelease` rewrites
the release line in a fixture copy.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestBuild -v`

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

`cmdBuild`:

1. `flag.NewFlagSet` for `--mirror` and `--keep-work`.
2. Load and validate the recipe; print every problem, return 1.
3. If `a.GOOS != "linux"`: "frostroot build requires Linux; on Windows run it inside WSL", return 1.
4. `a.LookPath("mmdebstrap")`; on error print "mmdebstrap not found on PATH; install with: sudo apt install mmdebstrap", return 1.
5. `distro.Lookup`; if `info.EOL`, warn on stderr naming the release and saying security updates are unavailable without Ubuntu Pro.
6. `ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt); defer stop()`.
7. `a.Builder.Build(ctx, r, builder.Options{Dir: a.Dir, Mirror: mirror, KeepWork: keepWork, GOOS: a.GOOS, Getenv: a.Getenv})`.
8. Error mapping: `errors.Is(err, context.Canceled)` → 130; `ErrNotLinux` or `ErrNoMmdebstrap` → 1; anything else → 2. On 2 and 130 print `workdir kept at <path>` when `Result.WorkDir` is set.
9. On success print the import line, and the work directory too when `--keep-work`.

The import line uses the relative path from `export.TarballRelPath` with forward
slashes:

```
wsl --import cpp-lab <install-dir> dist/cpp-lab-ubuntu-22.04-amd64.tar.gz
```

If `wslpath` is on PATH, also print the `\\wsl$\...` form of the absolute
tarball path. A failure to run `wslpath` is not a build failure — skip the extra
line.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./... && go build ./cmd/frostroot/`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/cli
git commit -m "feat: add frostroot build command with signal handling"
```

---

### Task 11: Real mmdebstrap runner

**Files:**
- Create: `internal/builder/bootstrap.go`
- Test: `internal/builder/bootstrap_test.go`

**Interfaces:**
- Produces: `builder.Mmdebstrap` implementing `Bootstrapper`

This is the seam every blocker lived in. Command construction is unit-tested
through an injected runner so the assertions run offline; the behaviour itself
was checked by hand in Task 0.

```go
type Runner func(ctx context.Context, name string, args []string, env []string, stdout, stderr io.Writer) error

type Mmdebstrap struct {
	RunCmd  Runner // default: exec.CommandContext
	Mode    string // "unshare", "root", or "" to detect from uid
	Uid     func() int
	Keyring string // default /usr/share/keyrings/ubuntu-archive-keyring.gpg
	Stderr  io.Writer // progress passthrough; nil means io.Discard
}
```

Command shape:

```
mmdebstrap
  --mode=<unshare|root>
  --variant=important
  --architectures=amd64
  --components=main,universe
  --keyring=<keyring>
  --aptopt=Apt::Install-Recommends "true"     # only when spec.Recommends
  --include=<comma-joined>
  --customize-hook=<hook>                      # one flag per hook, in order
  <suite> <tarPath> <deb line> <deb line> <deb line>
```

with `TMPDIR=<spec.WorkDir>` in the child environment — mmdebstrap stages the
tarball there, and the default `/tmp` may be small or on tmpfs.

- [ ] **Step 1: Write the failing test**

```go
func TestMmdebstrapCommand(t *testing.T) {
	var got []string
	var gotEnv []string
	m := Mmdebstrap{
		Uid: func() int { return 1000 },
		RunCmd: func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			if name != "mmdebstrap" {
				t.Fatalf("name %q", name)
			}
			got = append([]string{}, args...)
			gotEnv = append([]string{}, env...)
			return nil
		},
	}
	spec := BootstrapSpec{
		Suite:      "jammy",
		Sources:    []string{"deb http://a jammy main universe", "deb http://a jammy-updates main universe", "deb http://a jammy-security main universe"},
		Include:    []string{"git", "systemd"},
		Hooks:      []string{"upload /w/wsl.conf /etc/wsl.conf", "download /var/lib/dpkg/status /w/dpkg-status"},
		TarPath:    "/w/image.tar.gz",
		WorkDir:    "/w",
		Arch:       "amd64",
		Recommends: true,
	}
	if err := m.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(got, "\x00")
	for _, need := range []string{
		"--mode=unshare", "--variant=important", "--architectures=amd64",
		"--components=main,universe", "--include=git,systemd",
		"--keyring=/usr/share/keyrings/ubuntu-archive-keyring.gpg",
	} {
		if !strings.Contains(joined, need) {
			t.Fatalf("missing %q in %v", need, got)
		}
	}
	if !strings.Contains(joined, `--aptopt=Apt::Install-Recommends "true"`) {
		t.Fatalf("Recommends not enabled: %v", got)
	}
	// Positional order matters: suite, target, then every source line.
	n := len(got)
	if got[n-4] != "jammy" || got[n-3] != "/w/image.tar.gz" {
		t.Fatalf("positional args wrong: %v", got[n-5:])
	}
	if !strings.Contains(got[n-1], "jammy-security") {
		t.Fatalf("source lines wrong: %v", got[n-3:])
	}
	if !contains(gotEnv, "TMPDIR=/w") {
		t.Fatalf("TMPDIR must point at the work dir: %v", gotEnv)
	}
}

func TestMmdebstrapHooksPassedInOrder(t *testing.T) {
	var got []string
	m := Mmdebstrap{
		Uid:    func() int { return 1000 },
		RunCmd: func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			got = args
			return nil
		},
	}
	hooks := []string{"first", "second", "third"}
	if err := m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64", Hooks: hooks}); err != nil {
		t.Fatal(err)
	}
	var seen []string
	for _, a := range got {
		if strings.HasPrefix(a, "--customize-hook=") {
			seen = append(seen, strings.TrimPrefix(a, "--customize-hook="))
		}
	}
	if len(seen) != 3 || seen[0] != "first" || seen[2] != "third" {
		t.Fatalf("hook order: %v", seen)
	}
}

func TestMmdebstrapRootMode(t *testing.T) {
	var joined string
	m := Mmdebstrap{
		Uid:    func() int { return 0 },
		RunCmd: func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			joined = strings.Join(args, " ")
			return nil
		},
	}
	if err := m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joined, "--mode=root") {
		t.Fatalf("got %q", joined)
	}
}

func TestMmdebstrapNoRecommendsFlagWhenOff(t *testing.T) {
	var joined string
	m := Mmdebstrap{
		Uid:    func() int { return 1000 },
		RunCmd: func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			joined = strings.Join(args, " ")
			return nil
		},
	}
	m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64"})
	if strings.Contains(joined, "Install-Recommends") {
		t.Fatalf("got %q", joined)
	}
}

func TestMmdebstrapErrorCarriesStderrTail(t *testing.T) {
	m := Mmdebstrap{
		Uid: func() int { return 1000 },
		RunCmd: func(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
			fmt.Fprintln(stderr, "E: Unable to locate package nosuchpkg")
			return errors.New("exit status 1")
		},
	}
	err := m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "nosuchpkg") {
		t.Fatalf("error must carry the stderr tail: %v", err)
	}
}

func TestMmdebstrapMissingKeyringIsClear(t *testing.T) {
	m := Mmdebstrap{
		Uid:     func() int { return 1000 },
		Keyring: filepath.Join(t.TempDir(), "absent.gpg"),
		RunCmd:  func(context.Context, string, []string, []string, io.Writer, io.Writer) error { return nil },
	}
	err := m.Run(context.Background(), BootstrapSpec{Suite: "noble", TarPath: "/t", WorkDir: "/w", Arch: "amd64"})
	if err == nil || !strings.Contains(err.Error(), "keyring") {
		t.Fatalf("want a keyring error, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/builder/ -run TestMmdebstrap -v`

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

Default `Runner`:

```go
func defaultRun(ctx context.Context, name string, args, env []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	// Never SIGKILL mmdebstrap: in root mode it has proc, sys and dev
	// mounted inside the chroot, and killing it hard can leave those
	// mounts live.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	return cmd.Run()
}
```

`Run` stats the keyring first and returns a clear error naming it (Debian hosts
need `ubuntu-keyring`) rather than letting mmdebstrap fail obscurely. Stream
stderr to `m.Stderr` through an `io.MultiWriter` that also keeps the last 4 KiB
in a ring buffer; wrap a non-nil error with that tail. A five-minute silent
build looks hung, and mmdebstrap's own stderr is the best explanation of a
missing user namespace — which is why `ErrNoPrivilege` does not exist.

Mode detection: `Mode` if set, else `root` when `Uid() == 0`, else `unshare`.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./... && go build ./cmd/frostroot/`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/builder/bootstrap.go internal/builder/bootstrap_test.go
git commit -m "feat: run real mmdebstrap with pockets, keyring and stderr streaming"
```

---

### Task 12: Integration test and README

**Files:**
- Create: `internal/builder/integration_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: the real `Builder` with `Mmdebstrap{}`
- Produces: `-tags=integration` test that skips without Linux or mmdebstrap; README usage

The integration test is the **only** automated check that the tarball is
structurally sound. Defect 1 passed every unit test in the old plan, so the
symlink assertion below is not optional decoration — it is the regression test
for the worst bug this plan fixes.

- [ ] **Step 1: Write the integration test**

```go
//go:build integration

package builder

func TestIntegrationNobleTiny(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("linux only")
	}
	if _, err := exec.LookPath("mmdebstrap"); err != nil {
		t.Skip("mmdebstrap not installed")
	}
	dir := t.TempDir()
	r := recipe.Recipe{
		Image:    recipe.Image{Name: "tiny", Release: "24.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{"bash"}},
	}
	b := Builder{Bootstrap: Mmdebstrap{}}
	res, err := b.Build(context.Background(), r, Options{Dir: dir, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}

	lock, err := recipe.LoadLock(res.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.Sources) != 3 {
		t.Fatalf("lock sources: %#v", lock.Sources)
	}
	assertLockHas(t, lock, "bash")
	assertLockHas(t, lock, "systemd") // essentials really were installed

	entries := readTar(t, res.TarballPath)

	// Regression test for the defect that motivated this plan: a symlink
	// with an empty Linkname means the image cannot boot.
	symlinks := 0
	for _, e := range entries {
		if e.Typeflag == tar.TypeSymlink {
			symlinks++
			if e.Linkname == "" {
				t.Fatalf("symlink %q has an empty target", e.Name)
			}
		}
	}
	if symlinks < 100 {
		t.Fatalf("a real rootfs has thousands of symlinks, found %d", symlinks)
	}

	// Ownership must be real, not remapped into the subuid range.
	for _, e := range entries {
		if e.Uid > 65535 {
			t.Fatalf("entry %q has subuid-range owner %d", e.Name, e.Uid)
		}
	}

	assertTarHas(t, entries, "etc/wsl.conf")
	assertTarHasPrefix(t, entries, "home/student")
	assertTarHasPrefix(t, entries, "etc/sudoers.d/")
	if body := tarFileBody(t, res.TarballPath, "etc/wsl.conf"); !strings.Contains(body, "systemd=true") || !strings.Contains(body, "default=student") {
		t.Fatalf("wsl.conf: %q", body)
	}
	// mmdebstrap copies the host's resolv.conf in; our hook removes it.
	assertTarLacks(t, entries, "etc/resolv.conf")
}
```

Helpers (`readTar`, `assertTarHas`, `assertTarHasPrefix`, `assertTarLacks`,
`tarFileBody`, `assertLockHas`) read the gzip stream once into a slice of
headers. Keep them in the same file behind the build tag.

Confirm the default run does **not** include this file:

Run: `go test ./internal/builder/ -count=1`

Expected: PASS with no `TestIntegrationNobleTiny`.

Run with it:

Run: `go test -tags=integration ./internal/builder/ -run TestIntegration -v`

Expected: PASS on a Linux host with mmdebstrap, network, and userns or root.
Several minutes and a few hundred MB of downloads.

- [ ] **Step 2: Replace README.md**

Cover, in this order: what frostroot does; install (`go build ./cmd/frostroot`);
host requirements (`sudo apt install mmdebstrap`, plus userns or root); the three
commands; a worked example from `init` to `wsl --import`; and these four notes:

1. **The tarball is the golden image.** v1 does not rebuild from lock versions;
   a second `build` hits current mirrors and may drift.
2. **Builds need network; consuming the tarball does not.**
3. **20.04 images contain packages with known unfixed CVEs.** old-releases is
   frozen at end of standard support; security fixes need Ubuntu Pro. Use 22.04
   or 24.04 unless you specifically need focal.
4. **PEP 668 on 24.04**: `pip install` outside a virtualenv fails by design; use
   `python3 -m venv`.

Document the manual WSL check as a manual check — it is not automated and will
not be.

- [ ] **Step 3: Commit**

```
git add internal/builder/integration_test.go README.md
git commit -m "test: add integration build and rewrite README"
```

---

## Self-review

**Spec coverage**

| Spec item | Task |
|-----------|------|
| Ubuntu 20.04 old-releases, 22.04/24.04 archive, amd64 | 1, 7 |
| Update and security pockets | 1, 7, 11 |
| `frostroot.toml` parse/validate | 2, 8 |
| `frostroot.lock` all packages + requested + sources | 3, 5, 7 |
| `init` prompts, presets, `--force` | 9 |
| `validate` | 8 |
| `build --mirror --keep-work` | 7, 10 |
| mmdebstrap include + customize-hook + unshare/root | 6, 11 |
| Provision wsl.conf, sudoers, locale/timezone, user | 6 |
| Tarball correctness (symlinks, ownership, xattrs) | 4 (by delegation), 11, 12 |
| Atomic artifacts, nothing written on failure | 4, 7 |
| Exit 1/2/130, missing mmdebstrap hint | 10 |
| `go test ./...` offline, rootless, no mmdebstrap | fakes throughout |
| Integration tag | 12 |
| Manual `wsl --import` | 0, 12 README |

**Deviations from the original 2026-09-14 spec.** The spec was revised on
2026-09-15 and now matches this plan; the table is kept so the reasoning stays
visible. Its revision history records the same changes:

| Spec says | Plan does | Why |
|---|---|---|
| `internal/export` turns a rootfs directory into `.tar.gz` | mmdebstrap writes the tarball; export names and moves it | Defect 1 and 2 |
| Essentials are `sudo, locales, tzdata, passwd` | adds `systemd`, `systemd-sysv`, `dbus`, `ca-certificates` | Defect 3 |
| One mirror per release | three pocket lines per release | Defect 4 |
| Work dir is `os.MkdirTemp("", ...)` | `$XDG_CACHE_HOME/frostroot` or `/var/tmp/frostroot`, never `/mnt` | I1 |
| `--keep-rootfs` | `--keep-work` | No rootfs directory exists under design A |
| Lock has no `sources`, packages have no `arch` | both present | Vendoring needs them; adding later is a format migration |
| `dpkg-query --root` | parse `/var/lib/dpkg/status` | Host dpkg ≥ 1.21 requirement |
| Prompting/testing implies Windows-friendly tests | Linux only | Spec itself says the CLI is Linux |

**Interface consistency:** `Bootstrapper.Run(ctx, BootstrapSpec)` is used
identically in Tasks 7, 10, 11 and 12. `Stage` flows Task 6 → 7. `Options`
gains `Getenv` in Task 7 and is populated in Task 10. `builder.Version` is the
single source of `frostroot_version`.

**No TBDs.** The one genuinely open item is whether `systemd-resolved` needs
masking in a hook, and Task 0 Step 4 decides it from evidence rather than
guesswork. If the spike says no, nothing changes.
