# frostroot v1 Implementation Plan

> **SUPERSEDED — do not execute this plan.**
> Replaced by [`2026-09-15-frostroot-v1.md`](2026-09-15-frostroot-v1.md).
>
> Four defects in this plan would ship a tarball that does not boot, and its
> tests pass anyway: a pure-Go tar writer that gives every symlink an empty
> target, host-side tar and cleanup of an unshare-mode rootfs, `minbase` with no
> systemd, and a single-pocket `sources.list`. See the changelog at the top of
> the replacement, plus
> [`../reviews/2026-09-14-frostroot-plan-review.md`](../reviews/2026-09-14-frostroot-plan-review.md)
> and
> [`../reviews/2026-09-15-frostroot-feasibility.md`](../reviews/2026-09-15-frostroot-feasibility.md).
>
> Kept for history only.

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship a Linux CLI `frostroot` that reads `frostroot.toml`, bootstraps Ubuntu 20.04/22.04/24.04 with mmdebstrap, and writes `frostroot.lock` plus a WSL-importable rootfs tarball.

**Architecture:** One Go module, one binary. `internal/recipe` owns toml/lock, `internal/distro` owns Ubuntu mirrors, `internal/builder` orchestrates mmdebstrap + provision + lock, `internal/export` writes tar.gz, `internal/cli` is `init`/`validate`/`build`. Real mmdebstrap is behind a `Bootstrapper` interface so default tests run offline on Windows.

**Tech Stack:** Go 1.22, stdlib `flag`/`archive/tar`/`compress/gzip`/`os/exec`, `github.com/pelletier/go-toml/v2`. Host tools (Linux build only): `mmdebstrap`, `dpkg-query`.

## Global Constraints

- Language is Go; module path is `frostroot`; binary name is `frostroot`.
- v1 distros: Ubuntu 20.04 (`focal`, mirror `http://old-releases.ubuntu.com/ubuntu`), 22.04 (`jammy`, `http://archive.ubuntu.com/ubuntu`), 24.04 (`noble`, `http://archive.ubuntu.com/ubuntu`); arch `amd64` only; components `main universe`.
- v1 packages: apt names only. No PPAs, no pip/npm/cargo, no Fedora, no `.deb` vendoring, no `build --offline`.
- Recipe is source of truth (`frostroot.toml`); versions live only in `frostroot.lock`. `build` never prompts.
- Tarball is the golden image. v1 does not reinstall from lock versions (`pkg=version`).
- Image profile: sudo user (passwordless when `sudo = true`), `/etc/wsl.conf` systemd + default user, locale/timezone. No password field.
- CLI verbs v1: `init`, `validate`, `build` only. Flags: `init --force`; `build --mirror URL` and `build --keep-rootfs`.
- Exit codes: 0 success, 1 user error, 2 build error, 130 interrupt.
- Default `go test ./...` must pass **offline, without root, without mmdebstrap, on Windows**. Real bootstrap is Linux-only.
- Lock `version = 1`; `frostroot_version = "0.1.0"`; `requested` is recipe include as written; `[[packages]]` is every installed dpkg.
- Tarball path: `dist/<image.name>-ubuntu-<release>-<arch>.tar.gz`.
- Provision essentials always included: `sudo`, `locales`, `tzdata`, `passwd`.
- Do not invent extra subcommands, distros, or recipe fields.

---

## File structure

Create these files over the tasks below. Do not add others unless a task says so.

| Path | Responsibility |
|------|----------------|
| `go.mod` | Module `frostroot`, Go 1.22 |
| `cmd/frostroot/main.go` | `os.Exit(cli.App{}.Run(os.Args[1:]))` |
| `internal/distro/ubuntu.go` | Release → suite/mirror/components |
| `internal/distro/ubuntu_test.go` | Distro table tests |
| `internal/recipe/recipe.go` | `Recipe` types, `Load`, `Save` |
| `internal/recipe/validate.go` | `Validate` → all problems |
| `internal/recipe/lock.go` | `Lockfile` types, `LoadLock`, `SaveLock` |
| `internal/recipe/recipe_test.go` | Parse/validate/lock tests |
| `internal/export/name.go` | Tarball relative path |
| `internal/export/tar.go` | Rootfs directory → `.tar.gz` |
| `internal/export/export_test.go` | Name + tar tests |
| `internal/builder/provision.go` | `WriteProvisionFiles`, `CustomizeHooks`, `MergeInclude` |
| `internal/builder/builder.go` | `Builder.Build`, `Bootstrapper`, `PackageQuerier` |
| `internal/builder/bootstrap.go` | Real mmdebstrap + `dpkg-query` |
| `internal/builder/builder_test.go` | Fake bootstrapper tests |
| `internal/builder/provision_test.go` | Provision file/hook tests |
| `internal/cli/app.go` | `App.Run`, flags, exit codes |
| `internal/cli/initcmd.go` | `init` wizard |
| `internal/cli/app_test.go` | CLI tests |
| `testdata/valid.toml` | Valid recipe fixture |
| `testdata/bad-release.toml` | Unknown release fixture |
| `testdata/bad-user.toml` | Invalid user fixture |
| `README.md` | Replace design-stage stub with usage |

---

### Task 1: Distro table

**Files:**
- Create: `go.mod`
- Create: `internal/distro/ubuntu.go`
- Test: `internal/distro/ubuntu_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `distro.Info`, `distro.Lookup(release, arch string) (Info, error)`, `distro.KnownReleases() []string`, `distro.ErrUnknownRelease`, `distro.ErrUnsupportedArch`

- [ ] **Step 1: Create the module and a failing test**

```
go mod init frostroot
```

Set `go 1.22` in `go.mod`.

```go
package distro

import (
	"errors"
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
	if info.Mirror != "http://old-releases.ubuntu.com/ubuntu" {
		t.Fatalf("mirror: got %q", info.Mirror)
	}
	if len(info.Components) != 2 || info.Components[0] != "main" || info.Components[1] != "universe" {
		t.Fatalf("components: got %#v", info.Components)
	}
}

func TestLookupJammyAndNobleArchive(t *testing.T) {
	for _, tc := range []struct {
		release, suite string
	}{{"22.04", "jammy"}, {"24.04", "noble"}} {
		info, err := Lookup(tc.release, "amd64")
		if err != nil {
			t.Fatalf("%s: %v", tc.release, err)
		}
		if info.Suite != tc.suite {
			t.Fatalf("%s suite: got %q", tc.release, info.Suite)
		}
		if info.Mirror != "http://archive.ubuntu.com/ubuntu" {
			t.Fatalf("%s mirror: got %q", tc.release, info.Mirror)
		}
	}
}

func TestLookupUnknownRelease(t *testing.T) {
	_, err := Lookup("18.04", "amd64")
	if !errors.Is(err, ErrUnknownRelease) {
		t.Fatalf("got %v", err)
	}
}

func TestLookupUnsupportedArch(t *testing.T) {
	_, err := Lookup("24.04", "arm64")
	if !errors.Is(err, ErrUnsupportedArch) {
		t.Fatalf("got %v", err)
	}
}

func TestKnownReleases(t *testing.T) {
	got := KnownReleases()
	want := []string{"20.04", "22.04", "24.04"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("got %#v", got)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/distro/ -v`

Expected: FAIL compile or undefined `Lookup`.

- [ ] **Step 3: Write minimal implementation**

```go
package distro

import "fmt"

var ErrUnknownRelease = fmt.Errorf("unknown ubuntu release")
var ErrUnsupportedArch = fmt.Errorf("unsupported arch")

type Info struct {
	Suite      string
	Mirror     string
	Components []string
}

var table = map[string]Info{
	"20.04": {Suite: "focal", Mirror: "http://old-releases.ubuntu.com/ubuntu", Components: []string{"main", "universe"}},
	"22.04": {Suite: "jammy", Mirror: "http://archive.ubuntu.com/ubuntu", Components: []string{"main", "universe"}},
	"24.04": {Suite: "noble", Mirror: "http://archive.ubuntu.com/ubuntu", Components: []string{"main", "universe"}},
}

func Lookup(release, arch string) (Info, error) {
	if arch != "amd64" {
		return Info{}, fmt.Errorf("%w: %s", ErrUnsupportedArch, arch)
	}
	info, ok := table[release]
	if !ok {
		return Info{}, fmt.Errorf("%w: %s", ErrUnknownRelease, release)
	}
	out := info
	out.Components = append([]string{}, info.Components...)
	return out, nil
}

func KnownReleases() []string {
	return []string{"20.04", "22.04", "24.04"}
}
```

Use `errors.New` plus `fmt.Errorf("%w")` if you prefer; tests use `errors.Is`.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/distro/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add go.mod internal/distro/ubuntu.go internal/distro/ubuntu_test.go
git commit -m "feat: add Ubuntu LTS distro table"
```

---

### Task 2: Recipe load and validate

**Files:**
- Create: `internal/recipe/recipe.go`
- Create: `internal/recipe/validate.go`
- Create: `testdata/valid.toml`
- Create: `testdata/bad-release.toml`
- Create: `testdata/bad-user.toml`
- Test: `internal/recipe/recipe_test.go`

**Interfaces:**
- Consumes: `distro.Lookup`, `distro.ErrUnknownRelease`, `distro.ErrUnsupportedArch`
- Produces: `recipe.Recipe` (and nested structs), `Load(path string) (Recipe, error)`, `Save(path string, r Recipe) error`, `Validate(r Recipe) []string`

- [ ] **Step 1: Add toml dependency and failing tests**

```
go get github.com/pelletier/go-toml/v2
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

`testdata/bad-release.toml`: same as valid but `release = "18.04"`.

`testdata/bad-user.toml`: same as valid but `name = "Root"` under `[user]` (uppercase, invalid).

```go
package recipe

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testdata(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", name)
}

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
	if !r.WSL.Systemd || r.WSL.DefaultUser != "student" {
		t.Fatalf("wsl: %+v", r.WSL)
	}
	if r.Locale.Lang != "en_US.UTF-8" || r.Locale.Timezone != "UTC" {
		t.Fatalf("locale: %+v", r.Locale)
	}
	if len(r.Packages.Include) != 3 || r.Packages.Include[0] != "git" {
		t.Fatalf("packages: %#v", r.Packages.Include)
	}
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("validate: %v", probs)
	}
}

func TestValidateUnknownRelease(t *testing.T) {
	r, err := Load(testdata("bad-release.toml"))
	if err != nil {
		t.Fatal(err)
	}
	probs := Validate(r)
	if len(probs) == 0 {
		t.Fatal("expected problems")
	}
	joined := strings.Join(probs, "\n")
	if !strings.Contains(joined, "18.04") {
		t.Fatalf("got %v", probs)
	}
}

func TestValidateBadUser(t *testing.T) {
	r, err := Load(testdata("bad-user.toml"))
	if err != nil {
		t.Fatal(err)
	}
	probs := Validate(r)
	if len(probs) == 0 {
		t.Fatal("expected problems")
	}
}

func TestValidateEmptyIncludeOK(t *testing.T) {
	r := validRecipe(t)
	r.Packages.Include = nil
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("got %v", probs)
	}
}

func TestValidateDefaultUserMustMatch(t *testing.T) {
	r := validRecipe(t)
	r.WSL.DefaultUser = "other"
	probs := Validate(r)
	if len(probs) == 0 {
		t.Fatal("expected problems")
	}
}

func TestValidateDefaultUserEmptyOK(t *testing.T) {
	r := validRecipe(t)
	r.WSL.DefaultUser = ""
	if probs := Validate(r); len(probs) != 0 {
		t.Fatalf("got %v", probs)
	}
}

func TestValidateImageName(t *testing.T) {
	r := validRecipe(t)
	r.Image.Name = "My Lab"
	if len(Validate(r)) == 0 {
		t.Fatal("expected problems")
	}
}

func TestValidatePackageToken(t *testing.T) {
	r := validRecipe(t)
	r.Packages.Include = []string{"Git"}
	if len(Validate(r)) == 0 {
		t.Fatal("expected problems")
	}
}

func TestValidateRootUserRejected(t *testing.T) {
	r := validRecipe(t)
	r.User.Name = "root"
	r.WSL.DefaultUser = "root"
	if len(Validate(r)) == 0 {
		t.Fatal("expected problems")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "frostroot.toml")
	in := validRecipe(t)
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

func validRecipe(t *testing.T) Recipe {
	t.Helper()
	r, err := Load(testdata("valid.toml"))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/recipe/ -v`

Expected: FAIL undefined `Load`/`Validate`.

- [ ] **Step 3: Write minimal implementation**

`recipe.go`:

```go
package recipe

import (
	"os"

	toml "github.com/pelletier/go-toml/v2"
)

type Recipe struct {
	Image    Image    `toml:"image"`
	User     User     `toml:"user"`
	WSL      WSL      `toml:"wsl"`
	Locale   Locale   `toml:"locale"`
	Packages Packages `toml:"packages"`
}

type Image struct {
	Name    string `toml:"name"`
	Release string `toml:"release"`
	Arch    string `toml:"arch"`
}

type User struct {
	Name string `toml:"name"`
	Sudo bool   `toml:"sudo"`
}

type WSL struct {
	Systemd     bool   `toml:"systemd"`
	DefaultUser string `toml:"default_user"`
}

type Locale struct {
	Lang     string `toml:"lang"`
	Timezone string `toml:"timezone"`
}

type Packages struct {
	Include []string `toml:"include"`
}

func Load(path string) (Recipe, error) {
	var r Recipe
	data, err := os.ReadFile(path)
	if err != nil {
		return Recipe{}, err
	}
	if err := toml.Unmarshal(data, &r); err != nil {
		return Recipe{}, err
	}
	return r, nil
}

func Save(path string, r Recipe) error {
	data, err := toml.Marshal(r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
```

`validate.go`:

```go
package recipe

import (
	"fmt"
	"regexp"

	"frostroot/internal/distro"
)

var (
	imageNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	userNameRe  = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)
	pkgTokenRe  = regexp.MustCompile(`^[a-z0-9][a-z0-9+.-]+$`)
)

func Validate(r Recipe) []string {
	var probs []string
	if r.Image.Name == "" || !imageNameRe.MatchString(r.Image.Name) {
		probs = append(probs, fmt.Sprintf("invalid image name %q", r.Image.Name))
	}
	if _, err := distro.Lookup(r.Image.Release, r.Image.Arch); err != nil {
		probs = append(probs, err.Error())
	}
	if r.User.Name == "root" || r.User.Name == "" || !userNameRe.MatchString(r.User.Name) || len(r.User.Name) > 32 {
		probs = append(probs, fmt.Sprintf("invalid user name %q", r.User.Name))
	}
	if r.WSL.DefaultUser != "" && r.WSL.DefaultUser != r.User.Name {
		probs = append(probs, "wsl.default_user must equal user.name")
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

Package regex: one extra character class after the first char (`+` at the end of the second class) so `g++` is valid and `a` alone is invalid. Empty `include` skips the loop.

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
- Modify: `internal/recipe/recipe_test.go` (append lock tests)

**Interfaces:**
- Consumes: nothing new
- Produces: `recipe.Lockfile`, `recipe.LockPackage`, `LoadLock(path string) (Lockfile, error)`, `SaveLock(path string, l Lockfile) error`

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
		FrostrootVersion: "0.1.0",
		Requested:        []string{"git", "build-essential", "cmake"},
		Packages: []LockPackage{
			{Name: "git", Version: "1:2.34.1-1ubuntu1.11"},
			{Name: "libc6", Version: "2.35-0ubuntu3.8"},
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
	if len(out.Requested) != 3 || out.Requested[0] != "git" {
		t.Fatalf("requested: %#v", out.Requested)
	}
	if len(out.Packages) != 2 || out.Packages[0].Name != "git" || out.Packages[0].Version != "1:2.34.1-1ubuntu1.11" {
		t.Fatalf("packages: %#v", out.Packages)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/recipe/ -run TestLockRoundTrip -v`

Expected: FAIL undefined `Lockfile`/`SaveLock`.

- [ ] **Step 3: Write minimal implementation**

```go
package recipe

import (
	"os"

	toml "github.com/pelletier/go-toml/v2"
)

type Lockfile struct {
	Version          int           `toml:"version"`
	Distro           string        `toml:"distro"`
	Release          string        `toml:"release"`
	Suite            string        `toml:"suite"`
	Arch             string        `toml:"arch"`
	Mirror           string        `toml:"mirror"`
	FrostrootVersion string        `toml:"frostroot_version"`
	Requested        []string      `toml:"requested"`
	Packages         []LockPackage `toml:"packages"`
}

type LockPackage struct {
	Name    string `toml:"name"`
	Version string `toml:"version"`
}

func LoadLock(path string) (Lockfile, error) {
	var l Lockfile
	data, err := os.ReadFile(path)
	if err != nil {
		return Lockfile{}, err
	}
	if err := toml.Unmarshal(data, &l); err != nil {
		return Lockfile{}, err
	}
	return l, nil
}

func SaveLock(path string, l Lockfile) error {
	data, err := toml.Marshal(l)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}
```

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/recipe/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/recipe/lock.go internal/recipe/recipe_test.go
git commit -m "feat: round-trip frostroot.lock"
```

---

### Task 4: Tarball name and writer

**Files:**
- Create: `internal/export/name.go`
- Create: `internal/export/tar.go`
- Test: `internal/export/export_test.go`

**Interfaces:**
- Consumes: nothing
- Produces: `export.TarballRelPath(imageName, release, arch string) string`, `export.WriteTarball(rootfsDir, destPath string) error`

- [ ] **Step 1: Write the failing test**

```go
package export

import (
	"archive/tar"
	"compress/gzip"
	"io"
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

func TestWriteTarballSkipsProcSysDev(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "etc", "wsl.conf"), "[boot]\nsystemd=true\n")
	mustWrite(t, filepath.Join(root, "proc", "cpuinfo"), "fake")
	mustWrite(t, filepath.Join(root, "sys", "x"), "fake")
	mustWrite(t, filepath.Join(root, "dev", "null"), "fake")

	dest := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := WriteTarball(root, dest); err != nil {
		t.Fatal(err)
	}

	names := tarNames(t, dest)
	if !names["etc/wsl.conf"] {
		t.Fatalf("missing etc/wsl.conf in %v", names)
	}
	if names["proc/cpuinfo"] || names["sys/x"] || names["dev/null"] {
		t.Fatalf("junk included: %v", names)
	}
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func tarNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	out := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		out[h.Name] = true
	}
	return out
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/export/ -v`

Expected: FAIL undefined symbols.

- [ ] **Step 3: Write minimal implementation**

`name.go`:

```go
package export

import "path/filepath"

func TarballRelPath(imageName, release, arch string) string {
	return filepath.Join("dist", imageName+"-ubuntu-"+release+"-"+arch+".tar.gz")
}
```

`tar.go`:

```go
package export

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func WriteTarball(rootfsDir, destPath string) error {
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}
	tmp := destPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(tmp)
		}
	}()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	err = filepath.Walk(rootfsDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rootfsDir, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return nil
		}
		top, _, _ := strings.Cut(rel, "/")
		if top == "proc" || top == "sys" || top == "dev" {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		name := rel
		if info.IsDir() {
			name = rel + "/"
		}
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			in, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(tw, in)
			in.Close()
			return copyErr
		}
		return nil
	})
	if err != nil {
		tw.Close()
		gw.Close()
		return err
	}
	if err := tw.Close(); err != nil {
		gw.Close()
		return err
	}
	if err := gw.Close(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, destPath); err != nil {
		return err
	}
	ok = true
	return nil
}
```

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/export/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/export
git commit -m "feat: write rootfs tarball and skip proc/sys/dev"
```

---

### Task 5: Provision files and mmdebstrap hooks

**Files:**
- Create: `internal/builder/provision.go`
- Test: `internal/builder/provision_test.go`

**Interfaces:**
- Consumes: `recipe.Recipe`, `recipe.DefaultUser`
- Produces: `builder.Essentials`, `builder.MergeInclude(user []string) []string`, `builder.WriteProvisionFiles(rootfs string, r recipe.Recipe) error`, `builder.CustomizeHooks(r recipe.Recipe) []string`

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

func TestMergeIncludeAddsEssentials(t *testing.T) {
	got := MergeInclude([]string{"git", "sudo"})
	joined := strings.Join(got, ",")
	for _, need := range []string{"git", "sudo", "locales", "tzdata", "passwd"} {
		if !contains(got, need) {
			t.Fatalf("missing %s in %s", need, joined)
		}
	}
	if count(got, "sudo") != 1 {
		t.Fatalf("sudo should be unique: %v", got)
	}
}

func TestWriteProvisionFiles(t *testing.T) {
	root := t.TempDir()
	r := recipe.Recipe{
		User:   recipe.User{Name: "student", Sudo: true},
		WSL:    recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale: recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
	}
	if err := WriteProvisionFiles(root, r); err != nil {
		t.Fatal(err)
	}
	wsl, err := os.ReadFile(filepath.Join(root, "etc", "wsl.conf"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(wsl)
	if !strings.Contains(body, "systemd=true") || !strings.Contains(body, "default=student") {
		t.Fatalf("wsl.conf: %s", body)
	}
	sudoers, err := os.ReadFile(filepath.Join(root, "etc", "sudoers.d", "student"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(sudoers), "NOPASSWD:ALL") {
		t.Fatalf("sudoers: %s", sudoers)
	}
	if st, err := os.Stat(filepath.Join(root, "home", "student")); err != nil || !st.IsDir() {
		t.Fatalf("home: %v", err)
	}
}

func TestWriteProvisionFilesNoSudo(t *testing.T) {
	root := t.TempDir()
	r := recipe.Recipe{User: recipe.User{Name: "student", Sudo: false}}
	if err := WriteProvisionFiles(root, r); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "sudoers.d", "student")); !os.IsNotExist(err) {
		t.Fatalf("sudoers should be absent: %v", err)
	}
}

func TestCustomizeHooksContainUserAndLocale(t *testing.T) {
	r := recipe.Recipe{
		User:   recipe.User{Name: "student", Sudo: true},
		WSL:    recipe.WSL{Systemd: true},
		Locale: recipe.Locale{Lang: "en_US.UTF-8", Timezone: "Europe/Istanbul"},
	}
	hooks := CustomizeHooks(r)
	all := strings.Join(hooks, "\n")
	for _, need := range []string{"useradd", "student", "en_US.UTF-8", "Europe/Istanbul", "wsl.conf"} {
		if !strings.Contains(all, need) {
			t.Fatalf("hooks missing %q:\n%s", need, all)
		}
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

Expected: FAIL undefined symbols.

- [ ] **Step 3: Write minimal implementation**

```go
package builder

import (
	"fmt"
	"os"
	"path/filepath"

	"frostroot/internal/recipe"
)

var Essentials = []string{"sudo", "locales", "tzdata", "passwd"}

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

func WriteProvisionFiles(rootfs string, r recipe.Recipe) error {
	user := r.User.Name
	def := recipe.DefaultUser(r)
	wsl := ""
	if r.WSL.Systemd {
		wsl += "[boot]\nsystemd=true\n"
	}
	wsl += "[user]\ndefault=" + def + "\n"
	if err := writeFile(filepath.Join(rootfs, "etc", "wsl.conf"), wsl, 0o644); err != nil {
		return err
	}
	if r.User.Sudo {
		line := user + " ALL=(ALL) NOPASSWD:ALL\n"
		if err := writeFile(filepath.Join(rootfs, "etc", "sudoers.d", user), line, 0o440); err != nil {
			return err
		}
	}
	return os.MkdirAll(filepath.Join(rootfs, "home", user), 0o755)
}

func CustomizeHooks(r recipe.Recipe) []string {
	user := r.User.Name
	def := recipe.DefaultUser(r)
	lang := r.Locale.Lang
	if lang == "" {
		lang = "en_US.UTF-8"
	}
	tz := r.Locale.Timezone
	if tz == "" {
		tz = "UTC"
	}
	wslBody := fmt.Sprintf("[user]\ndefault=%s\n", def)
	if r.WSL.Systemd {
		wslBody = "[boot]\nsystemd=true\n" + wslBody
	}
	hooks := []string{
		fmt.Sprintf(`chroot "$1" useradd -m -s /bin/bash %s || true`, user),
		fmt.Sprintf(`echo %q >> "$1/etc/locale.gen"`, lang+" UTF-8"),
		`chroot "$1" locale-gen || true`,
		fmt.Sprintf(`echo "LANG=%s" > "$1/etc/default/locale"`, lang),
		fmt.Sprintf(`chroot "$1" ln -sf /usr/share/zoneinfo/%s /etc/localtime`, tz),
		fmt.Sprintf("cat > \"$1/etc/wsl.conf\" <<'EOF'\n%sEOF", wslBody),
		`rm -f "$1/var/lib/dbus/machine-id" "$1/etc/machine-id"; touch "$1/etc/machine-id"`,
		`rm -rf "$1/var/lib/apt/lists/"*`,
	}
	if r.User.Sudo {
		hooks = append(hooks, fmt.Sprintf(`echo "%s ALL=(ALL) NOPASSWD:ALL" > "$1/etc/sudoers.d/%s"`, user, user))
		hooks = append(hooks, fmt.Sprintf(`chmod 440 "$1/etc/sudoers.d/%s"`, user))
	}
	return hooks
}

func writeFile(path, body string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(body), mode)
}
```

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/builder/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/builder/provision.go internal/builder/provision_test.go
git commit -m "feat: provision wsl.conf, sudoers, and customize hooks"
```

---

### Task 6: Builder orchestration with fakes

**Files:**
- Create: `internal/builder/builder.go`
- Test: `internal/builder/builder_test.go`

**Interfaces:**
- Consumes: `recipe.Recipe`, `recipe.SaveLock`, `distro.Lookup`, `export.TarballRelPath`, `export.WriteTarball`, `MergeInclude`, `CustomizeHooks`, `WriteProvisionFiles`
- Produces: `builder.Version` (`"0.1.0"`), `builder.Bootstrapper`, `builder.PackageQuerier`, `builder.Options`, `builder.Result`, `(*Builder).Build`, `ErrNotLinux`, `ErrNoMmdebstrap`, `ErrNoPrivilege`

```go
type Bootstrapper interface {
	Run(suite, mirror string, include []string, hooks []string, destDir string) error
}

type PackageQuerier interface {
	Query(rootfs string) ([]recipe.LockPackage, error)
}

type Options struct {
	Dir         string // recipe directory (lock + dist/ written here)
	Mirror      string // optional override
	KeepRootfs  bool
	GOOS        string // default runtime.GOOS
}

type Result struct {
	LockPath     string
	TarballPath  string
	WorkDir      string // set when kept (success --keep-rootfs or failure)
}

type Builder struct {
	Bootstrap Bootstrapper
	Query     PackageQuerier
	MkdirTemp func(dir, pattern string) (string, error) // default os.MkdirTemp
}
```

- [ ] **Step 1: Write the failing tests**

```go
package builder

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"frostroot/internal/recipe"
)

type fakeBoot struct {
	err      error
	suite    string
	mirror   string
	include  []string
	hooks    []string
	dest     string
	writeWSL bool
}

func (f *fakeBoot) Run(suite, mirror string, include []string, hooks []string, destDir string) error {
	f.suite, f.mirror, f.include, f.hooks, f.dest = suite, mirror, include, hooks, destDir
	if f.err != nil {
		return f.err
	}
	if err := os.MkdirAll(filepath.Join(destDir, "etc"), 0o755); err != nil {
		return err
	}
	if f.writeWSL {
		return os.WriteFile(filepath.Join(destDir, "etc", "wsl.conf"), []byte("from-hook\n"), 0o644)
	}
	return nil
}

type fakeQuery struct {
	pkgs []recipe.LockPackage
	err  error
}

func (f fakeQuery) Query(rootfs string) ([]recipe.LockPackage, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.pkgs, nil
}

func testRecipe() recipe.Recipe {
	return recipe.Recipe{
		Image:    recipe.Image{Name: "cpp-lab", Release: "22.04", Arch: "amd64"},
		User:     recipe.User{Name: "student", Sudo: true},
		WSL:      recipe.WSL{Systemd: true, DefaultUser: "student"},
		Locale:   recipe.Locale{Lang: "en_US.UTF-8", Timezone: "UTC"},
		Packages: recipe.Packages{Include: []string{"git", "build-essential", "cmake"}},
	}
}

func TestBuildSuccessWritesLockAndTar(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBoot{writeWSL: true}
	b := Builder{
		Bootstrap: boot,
		Query: fakeQuery{pkgs: []recipe.LockPackage{
			{Name: "git", Version: "1:2.34.1-1ubuntu1.11"},
			{Name: "libc6", Version: "2.35-0ubuntu3.8"},
		}},
	}
	res, err := b.Build(testRecipe(), Options{Dir: dir, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if boot.suite != "jammy" {
		t.Fatalf("suite %q", boot.suite)
	}
	if boot.mirror != "http://archive.ubuntu.com/ubuntu" {
		t.Fatalf("mirror %q", boot.mirror)
	}
	if !contains(boot.include, "git") || !contains(boot.include, "sudo") {
		t.Fatalf("include %v", boot.include)
	}
	lock, err := recipe.LoadLock(filepath.Join(dir, "frostroot.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if lock.Version != 1 || lock.Suite != "jammy" || lock.FrostrootVersion != "0.1.0" {
		t.Fatalf("lock %+v", lock)
	}
	if len(lock.Requested) != 3 || lock.Requested[0] != "git" {
		t.Fatalf("requested %#v", lock.Requested)
	}
	if len(lock.Packages) != 2 {
		t.Fatalf("packages %#v", lock.Packages)
	}
	tar := filepath.Join(dir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")
	if _, err := os.Stat(tar); err != nil {
		t.Fatal(err)
	}
	if res.TarballPath != tar {
		t.Fatalf("result path %q", res.TarballPath)
	}
	if res.WorkDir != "" {
		t.Fatalf("workdir should be removed: %q", res.WorkDir)
	}
}

func TestBuildKeepRootfs(t *testing.T) {
	dir := t.TempDir()
	b := Builder{Bootstrap: &fakeBoot{writeWSL: true}, Query: fakeQuery{pkgs: []recipe.LockPackage{{Name: "bash", Version: "5"}}}}
	res, err := b.Build(testRecipe(), Options{Dir: dir, GOOS: "linux", KeepRootfs: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.WorkDir == "" {
		t.Fatal("expected workdir")
	}
	if _, err := os.Stat(filepath.Join(res.WorkDir, "rootfs")); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFailureKeepsWorkdirNoArtifacts(t *testing.T) {
	dir := t.TempDir()
	b := Builder{Bootstrap: &fakeBoot{err: errors.New("mmdebstrap exploded")}, Query: fakeQuery{}}
	res, err := b.Build(testRecipe(), Options{Dir: dir, GOOS: "linux"})
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
		t.Fatalf("lock should be absent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dist", "cpp-lab-ubuntu-22.04-amd64.tar.gz")); !os.IsNotExist(err) {
		t.Fatalf("tar should be absent: %v", err)
	}
}

func TestBuildMirrorOverride(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBoot{writeWSL: true}
	b := Builder{Bootstrap: boot, Query: fakeQuery{pkgs: []recipe.LockPackage{{Name: "bash", Version: "5"}}}}
	_, err := b.Build(testRecipe(), Options{Dir: dir, GOOS: "linux", Mirror: "http://example.invalid/ubuntu"})
	if err != nil {
		t.Fatal(err)
	}
	if boot.mirror != "http://example.invalid/ubuntu" {
		t.Fatalf("got %q", boot.mirror)
	}
}

func TestBuildNotLinux(t *testing.T) {
	b := Builder{Bootstrap: &fakeBoot{}, Query: fakeQuery{}}
	_, err := b.Build(testRecipe(), Options{Dir: t.TempDir(), GOOS: "windows"})
	if !errors.Is(err, ErrNotLinux) {
		t.Fatalf("got %v", err)
	}
}

func TestBuildFocalUsesOldReleases(t *testing.T) {
	dir := t.TempDir()
	boot := &fakeBoot{writeWSL: true}
	b := Builder{Bootstrap: boot, Query: fakeQuery{pkgs: []recipe.LockPackage{{Name: "bash", Version: "5"}}}}
	r := testRecipe()
	r.Image.Release = "20.04"
	r.Image.Name = "oldlab"
	_, err := b.Build(r, Options{Dir: dir, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	if boot.suite != "focal" || boot.mirror != "http://old-releases.ubuntu.com/ubuntu" {
		t.Fatalf("suite=%q mirror=%q", boot.suite, boot.mirror)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/builder/ -run TestBuild -v`

Expected: FAIL undefined `Builder`/`Build`.

- [ ] **Step 3: Write minimal implementation**

```go
package builder

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"frostroot/internal/distro"
	"frostroot/internal/export"
	"frostroot/internal/recipe"
)

const Version = "0.1.0"

var (
	ErrNotLinux      = errors.New("frostroot build requires Linux")
	ErrNoMmdebstrap  = errors.New("mmdebstrap not found on PATH")
	ErrNoPrivilege   = errors.New("need user namespaces or root")
)

type Bootstrapper interface {
	Run(suite, mirror string, include []string, hooks []string, destDir string) error
}

type PackageQuerier interface {
	Query(rootfs string) ([]recipe.LockPackage, error)
}

type Options struct {
	Dir        string
	Mirror     string
	KeepRootfs bool
	GOOS       string
}

type Result struct {
	LockPath    string
	TarballPath string
	WorkDir     string
}

type Builder struct {
	Bootstrap Bootstrapper
	Query     PackageQuerier
	MkdirTemp func(string, string) (string, error)
}

func (b *Builder) Build(r recipe.Recipe, opts Options) (res Result, err error) {
	goos := opts.GOOS
	if goos == "" {
		goos = runtime.GOOS
	}
	if goos != "linux" {
		return res, ErrNotLinux
	}
	mkdir := b.MkdirTemp
	if mkdir == nil {
		mkdir = os.MkdirTemp
	}
	work, err := mkdir("", "frostroot-*")
	if err != nil {
		return res, err
	}
	keep := false
	defer func() {
		if keep || opts.KeepRootfs {
			res.WorkDir = work
			return
		}
		os.RemoveAll(work)
	}()
	info, err := distro.Lookup(r.Image.Release, r.Image.Arch)
	if err != nil {
		keep = true
		return res, err
	}
	mirror := info.Mirror
	if opts.Mirror != "" {
		mirror = opts.Mirror
	}
	rootfs := filepath.Join(work, "rootfs")
	if err := os.MkdirAll(rootfs, 0o755); err != nil {
		keep = true
		return res, err
	}
	include := MergeInclude(r.Packages.Include)
	hooks := CustomizeHooks(r)
	if err := b.Bootstrap.Run(info.Suite, mirror, include, hooks, rootfs); err != nil {
		keep = true
		return res, err
	}
	if err := WriteProvisionFiles(rootfs, r); err != nil {
		keep = true
		return res, err
	}
	pkgs, err := b.Query.Query(rootfs)
	if err != nil {
		keep = true
		return res, err
	}
	lock := recipe.Lockfile{
		Version:          1,
		Distro:           "ubuntu",
		Release:          r.Image.Release,
		Suite:            info.Suite,
		Arch:             r.Image.Arch,
		Mirror:           mirror,
		FrostrootVersion: Version,
		Requested:        append([]string{}, r.Packages.Include...),
		Packages:         pkgs,
	}
	lockPath := filepath.Join(opts.Dir, "frostroot.lock")
	tarRel := export.TarballRelPath(r.Image.Name, r.Image.Release, r.Image.Arch)
	tarPath := filepath.Join(opts.Dir, tarRel)
	lockTmp := lockPath + ".tmp"
	if err := recipe.SaveLock(lockTmp, lock); err != nil {
		keep = true
		return res, err
	}
	if err := export.WriteTarball(rootfs, tarPath); err != nil {
		os.Remove(lockTmp)
		keep = true
		return res, err
	}
	if err := os.Rename(lockTmp, lockPath); err != nil {
		keep = true
		return res, err
	}
	res.LockPath = lockPath
	res.TarballPath = tarPath
	if opts.KeepRootfs {
		keep = true
	}
	return res, nil
}
```

If `fmt` is unused, omit the import.

On bootstrap failure, `WriteTarball` must not have been called. `SaveLock` only after query succeeds; rename lock only after tar succeeds so a failed tar does not leave a lock.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/builder/ -v`

Expected: PASS

If `TestBuildFailureKeepsWorkdirNoArtifacts` sees a lock, you renamed too early — keep the tmp+rename order above.

- [ ] **Step 5: Commit**

```
git add internal/builder/builder.go internal/builder/builder_test.go
git commit -m "feat: orchestrate build with fakeable bootstrapper"
```

---

### Task 7: CLI validate

**Files:**
- Create: `internal/cli/app.go`
- Create: `cmd/frostroot/main.go`
- Test: `internal/cli/app_test.go`

**Interfaces:**
- Consumes: `recipe.Load`, `recipe.Validate`
- Produces: `cli.App`, `cli.Prompt`, `(App).Run(args []string) int`, exit `0`/`1`

```go
type Prompt interface {
	Ask(question, defaultValue string) (string, error)
}

type App struct {
	Stdin   io.Reader
	Stdout  io.Writer
	Stderr  io.Writer
	Dir     string
	GOOS    string
	Prompt  Prompt
	Builder *builder.Builder
	LookPath func(file string) (string, error)
}
```

Defaults: stdin/stdout/stderr = os.Std*, Dir = cwd, GOOS = runtime.GOOS, LookPath = exec.LookPath.

- [ ] **Step 1: Write the failing test**

```go
package cli

import (
	"bytes"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func testdata(name string) string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata", name)
}

func copyFile(t *testing.T, src, dest string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOK(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := App{Stdout: &out, Stderr: &errb, Dir: dir}
	code := app.Run([]string{"validate"})
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
}

func TestValidateBadRelease(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("bad-release.toml"), filepath.Join(dir, "frostroot.toml"))
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: dir}
	code := app.Run([]string{"validate"})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "18.04") {
		t.Fatalf("stderr %s", errb.String())
	}
}

func TestValidateMissingRecipe(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: t.TempDir()}
	code := app.Run([]string{"validate"})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestUnknownCommand(t *testing.T) {
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: t.TempDir()}
	code := app.Run([]string{"explode"})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
}
```

Add `"io"` and `"os"` to the import list.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -v`

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

`app.go` (validate + usage only; `init`/`build` can return `1` with "not implemented" **only if tests for those do not exist yet** — they will in the next tasks. For this task, switch on `validate` and default usage):

```go
package cli

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"

	"frostroot/internal/builder"
	"frostroot/internal/recipe"
)

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
	LookPath func(string) (string, error)
}

func (a *App) Run(args []string) int {
	a.defaults()
	if len(args) < 1 {
		fmt.Fprintln(a.Stderr, "usage: frostroot <init|validate|build>")
		return 1
	}
	switch args[0] {
	case "validate":
		return a.cmdValidate()
	case "init":
		return a.cmdInit(args[1:])
	case "build":
		return a.cmdBuild(args[1:])
	default:
		fmt.Fprintf(a.Stderr, "unknown command %q\n", args[0])
		return 1
	}
}

func (a *App) defaults() {
	if a.Stdin == nil {
		a.Stdin = os.Stdin
	}
	if a.Stdout == nil {
		a.Stdout = os.Stdout
	}
	if a.Stderr == nil {
		a.Stderr = os.Stderr
	}
	if a.Dir == "" {
		wd, err := os.Getwd()
		if err == nil {
			a.Dir = wd
		}
	}
	if a.GOOS == "" {
		a.GOOS = runtime.GOOS
	}
	if a.LookPath == nil {
		a.LookPath = execLookPath
	}
}

func execLookPath(file string) (string, error) {
	return lookPath(file)
}
```

Put `lookPath` as a variable assigned from `exec.LookPath` in this file (`import "os/exec"`). Implement `cmdValidate` now. Implement `cmdInit`/`cmdBuild` as `return 1` stubs **only if** you also skip calling them in tests this task — cleaner: implement `cmdInit`/`cmdBuild` in later tasks in the same file; for this task add:

```go
func (a *App) cmdInit(args []string) int {
	fmt.Fprintln(a.Stderr, "init not implemented")
	return 1
}

func (a *App) cmdBuild(args []string) int {
	fmt.Fprintln(a.Stderr, "build not implemented")
	return 1
}

func (a *App) cmdValidate() int {
	path := filepath.Join(a.Dir, "frostroot.toml")
	r, err := recipe.Load(path)
	if err != nil {
		fmt.Fprintln(a.Stderr, err.Error())
		return 1
	}
	probs := recipe.Validate(r)
	if len(probs) == 0 {
		return 0
	}
	for _, p := range probs {
		fmt.Fprintln(a.Stderr, p)
	}
	return 1
}
```

`cmd/frostroot/main.go`:

```go
package main

import (
	"os"

	"frostroot/internal/cli"
)

func main() {
	os.Exit((&cli.App{}).Run(os.Args[1:]))
}
```

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/cli/ -v`

Expected: PASS

Also: `go build ./cmd/frostroot/`

Expected: builds.

- [ ] **Step 5: Commit**

```
git add cmd/frostroot/main.go internal/cli/app.go internal/cli/app_test.go
git commit -m "feat: add frostroot validate command"
```

---

### Task 8: CLI init wizard

**Files:**
- Modify: `internal/cli/app.go` (`cmdInit`)
- Create: `internal/cli/initcmd.go` if you split; otherwise keep in `app.go`
- Modify: `internal/cli/app_test.go`

**Interfaces:**
- Consumes: `Prompt`, `recipe.Save`, `recipe.Validate`
- Produces: working `init` and `init --force`

Presets (only in init):

```go
var presets = map[string][]string{
	"none":            {},
	"build-essential": {"build-essential", "git", "cmake", "pkg-config"},
	"python-lab":      {"python3", "python3-pip", "python3-venv", "git"},
}
```

Prompt order:

1. Image name (default `lab`)
2. Release (default `24.04`)
3. Username (default `student`)
4. Timezone (default `UTC`)
5. Preset (`none` / `build-essential` / `python-lab`, default `none`)
6. Extra packages comma-separated (optional, appended)

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

func TestInitWritesRecipe(t *testing.T) {
	dir := t.TempDir()
	var errb bytes.Buffer
	app := App{
		Stdout: io.Discard,
		Stderr: &errb,
		Dir:    dir,
		Prompt: &scriptedPrompt{answers: []string{"cpp-lab", "22.04", "student", "UTC", "build-essential", ""}},
	}
	code := app.Run([]string{"init"})
	if code != 0 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	r, err := recipe.Load(filepath.Join(dir, "frostroot.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Image.Name != "cpp-lab" || r.Image.Release != "22.04" || r.Image.Arch != "amd64" {
		t.Fatalf("image %+v", r.Image)
	}
	if r.User.Name != "student" || !r.User.Sudo {
		t.Fatalf("user %+v", r.User)
	}
	if !containsStr(r.Packages.Include, "git") || !containsStr(r.Packages.Include, "cmake") {
		t.Fatalf("include %#v", r.Packages.Include)
	}
}

func TestInitRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "frostroot.toml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	var errb bytes.Buffer
	app := App{Stdout: io.Discard, Stderr: &errb, Dir: dir, Prompt: &scriptedPrompt{}}
	code := app.Run([]string{"init"})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestInitForce(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "frostroot.toml")
	if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	app := App{
		Stdout: io.Discard,
		Stderr: io.Discard,
		Dir:    dir,
		Prompt: &scriptedPrompt{answers: []string{"lab2", "24.04", "student", "UTC", "none", "git"}},
	}
	code := app.Run([]string{"init", "--force"})
	if code != 0 {
		t.Fatalf("code %d", code)
	}
	r, err := recipe.Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Image.Name != "lab2" || !containsStr(r.Packages.Include, "git") {
		t.Fatalf("%+v", r)
	}
}

func containsStr(xs []string, w string) bool {
	for _, x := range xs {
		if x == w {
			return true
		}
	}
	return false
}
```

Add `frostroot/internal/recipe` to the test imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestInit -v`

Expected: FAIL (`init not implemented` or code 1).

- [ ] **Step 3: Write minimal implementation**

Replace `cmdInit` with: parse `--force` from args (stdlib `flag.NewFlagSet("init", flag.ContinueOnError)`). If `frostroot.toml` exists and not force, print a message and return 1.

If `Prompt` is nil, use a `stdioPrompt` that prints `question [default]: ` to stdout and reads a line from stdin.

Build the `recipe.Recipe` from answers: `Arch: "amd64"`, `Sudo: true`, `WSL.Systemd: true`, `WSL.DefaultUser: username`, `Locale.Lang: "en_US.UTF-8"`. Split extra packages on commas, trim space, drop empties, append to preset list. Run `Validate`; if problems, print them and return 1. `recipe.Save`.

Unknown preset → exit 1.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./internal/cli/ -v`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/cli
git commit -m "feat: add frostroot init wizard"
```

---

### Task 9: CLI build

**Files:**
- Modify: `internal/cli/app.go` (`cmdBuild`)
- Modify: `internal/cli/app_test.go`

**Interfaces:**
- Consumes: `recipe.Load`, `recipe.Validate`, `builder.Builder.Build`, `LookPath("mmdebstrap")`
- Produces: working `build`, `--mirror`, `--keep-rootfs`; prints `wsl --import ...`

Map errors:

- `ErrNotLinux`, missing toml, validate problems, `ErrNoMmdebstrap` → exit **1**
- `ErrNoPrivilege`, bootstrap/provision/tar failures → exit **2**

Missing mmdebstrap stderr must contain `sudo apt install mmdebstrap`.

Success stdout must contain:

```
wsl --import cpp-lab <install-dir> dist/cpp-lab-ubuntu-22.04-amd64.tar.gz
```

Use the relative path from `export.TarballRelPath` (forward slashes in the printed example are OK; printing `filepath.ToSlash(rel)` is fine).

- [ ] **Step 1: Write the failing tests**

Reuse `fakeBoot`/`fakeQuery` from builder tests **or** duplicate small fakes in `cli` tests (do not import `_test` from builder). Duplicate a tiny fake in `app_test.go`:

```go
type stubBoot struct{}

func (stubBoot) Run(suite, mirror string, include []string, hooks []string, destDir string) error {
	return os.MkdirAll(filepath.Join(destDir, "etc"), 0o755)
}

type stubQuery struct{}

func (stubQuery) Query(rootfs string) ([]recipe.LockPackage, error) {
	return []recipe.LockPackage{{Name: "git", Version: "1:1"}}, nil
}

func TestBuildSuccess(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var out, errb bytes.Buffer
	app := App{
		Stdout:   &out,
		Stderr:   &errb,
		Dir:      dir,
		GOOS:     "linux",
		LookPath: func(string) (string, error) { return "/usr/bin/mmdebstrap", nil },
		Builder:  &builder.Builder{Bootstrap: stubBoot{}, Query: stubQuery{}},
	}
	code := app.Run([]string{"build"})
	if code != 0 {
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

func TestBuildNotLinux(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var errb bytes.Buffer
	app := App{
		Stdout:   io.Discard,
		Stderr:   &errb,
		Dir:      dir,
		GOOS:     "windows",
		LookPath: func(string) (string, error) { return "/usr/bin/mmdebstrap", nil },
		Builder:  &builder.Builder{Bootstrap: stubBoot{}, Query: stubQuery{}},
	}
	code := app.Run([]string{"build"})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
}

func TestBuildMissingMmdebstrap(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var errb bytes.Buffer
	app := App{
		Stdout:   io.Discard,
		Stderr:   &errb,
		Dir:      dir,
		GOOS:     "linux",
		LookPath: func(string) (string, error) { return "", os.ErrNotExist },
		Builder:  &builder.Builder{Bootstrap: stubBoot{}, Query: stubQuery{}},
	}
	code := app.Run([]string{"build"})
	if code != 1 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errb.String(), "sudo apt install mmdebstrap") {
		t.Fatalf("stderr %s", errb.String())
	}
}

func TestBuildBootstrapFailureExit2(t *testing.T) {
	dir := t.TempDir()
	copyFile(t, testdata("valid.toml"), filepath.Join(dir, "frostroot.toml"))
	var errb bytes.Buffer
	app := App{
		Stdout:   io.Discard,
		Stderr:   &errb,
		Dir:      dir,
		GOOS:     "linux",
		LookPath: func(string) (string, error) { return "/usr/bin/mmdebstrap", nil },
		Builder: &builder.Builder{
			Bootstrap: failBoot{err: errors.New("mmdebstrap exploded")},
			Query:     stubQuery{},
		},
	}
	code := app.Run([]string{"build"})
	if code != 2 {
		t.Fatalf("code %d stderr %s", code, errb.String())
	}
	if !strings.Contains(errb.String(), "mmdebstrap exploded") {
		t.Fatalf("stderr %s", errb.String())
	}
}

type failBoot struct{ err error }

func (f failBoot) Run(string, string, []string, []string, string) error { return f.err }
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/cli/ -run TestBuild -v`

Expected: FAIL.

- [ ] **Step 3: Write minimal implementation**

`cmdBuild`: `flag` for `--mirror` and `--keep-rootfs`. Load+validate recipe (exit 1). If `a.GOOS != "linux"` print `ErrNotLinux` exit 1. `LookPath("mmdebstrap")`; on error print `mmdebstrap not found on PATH; install with: sudo apt install mmdebstrap` exit 1. If `a.Builder == nil`, construct one with real bootstrapper (Task 10 — for this task, if Builder is nil and tests always inject it, `main.go` must set it; see Step 3b).

Call `Builder.Build` with `Options{Dir: a.Dir, Mirror, KeepRootfs, GOOS: a.GOOS}`. On `ErrNotLinux`/`ErrNoMmdebstrap` exit 1; otherwise non-nil error exit 2 and print `WorkDir` if set: `workdir kept at <path>`. On success print the wsl import line. If `KeepRootfs`, also print the workdir path.

- [ ] **Step 3b: Wire `main.go`** so a real user can run build after Task 10. For now:

```go
func main() {
	app := &cli.App{
		Builder: &builder.Builder{
			Bootstrap: builder.Mmdebstrap{},
			Query:     builder.DpkgQuery{},
		},
	}
	os.Exit(app.Run(os.Args[1:]))
}
```

`Mmdebstrap` and `DpkgQuery` are added in Task 10. If Task 9 is committed first, either combine 9+10 or add stub types in `bootstrap.go` that return `fmt.Errorf("not implemented")` so `main` compiles. **Do not leave main uncompilable.** Add empty structs in `internal/builder/bootstrap.go`:

```go
type Mmdebstrap struct{}

func (Mmdebstrap) Run(suite, mirror string, include []string, hooks []string, destDir string) error {
	return fmt.Errorf("mmdebstrap runner not implemented")
}

type DpkgQuery struct{}

func (DpkgQuery) Query(rootfs string) ([]recipe.LockPackage, error) {
	return nil, fmt.Errorf("dpkg query not implemented")
}
```

Tests inject fakes, so they still pass.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./...`

Expected: PASS

`go build ./cmd/frostroot/`

Expected: builds.

- [ ] **Step 5: Commit**

```
git add cmd/frostroot/main.go internal/cli internal/builder/bootstrap.go
git commit -m "feat: add frostroot build command"
```

---

### Task 10: Real mmdebstrap and dpkg-query

**Files:**
- Modify: `internal/builder/bootstrap.go`
- Modify: `internal/builder/builder_test.go` (command-construction tests via a fake runner)

**Interfaces:**
- Consumes: `os/exec`
- Produces: working `Mmdebstrap.Run` and `DpkgQuery.Query` on Linux; tests use an `exec` hook so they pass on Windows

Add to `Mmdebstrap`:

```go
type Runner func(name string, args []string, stdout, stderr io.Writer) error

type Mmdebstrap struct {
	RunCmd Runner // default: exec.Command
	Mode   string // "unshare", "root", or "" to detect
	Uid    func() int
}
```

Default `Uid` is `os.Getuid`. If `Uid() == 0`, mode `root`, else `unshare`. (CLI already refused non-Linux. If userns is missing, mmdebstrap fails and the user sees stderr — acceptable for v1. Do not probe `/proc` in unit tests.)

Command:

```
mmdebstrap --mode=<mode> --variant=minbase --components=main,universe --include=<comma-joined> --customize-hook=<hook>... <suite> <destDir> <mirror>
```

One `--customize-hook` flag per hook string.

`DpkgQuery`:

```
dpkg-query --root <rootfs> -W -f ${Package}\t${Version}\n
```

Parse lines into `[]recipe.LockPackage`. Skip empty lines.

- [ ] **Step 1: Write the failing test**

```go
func TestMmdebstrapCommand(t *testing.T) {
	var gotName string
	var gotArgs []string
	m := Mmdebstrap{
		Uid: func() int { return 1000 },
		RunCmd: func(name string, args []string, stdout, stderr io.Writer) error {
			gotName, gotArgs = name, append([]string{}, args...)
			return nil
		},
	}
	hooks := []string{`echo hi`}
	err := m.Run("jammy", "http://archive.ubuntu.com/ubuntu", []string{"git", "sudo"}, hooks, "/tmp/rootfs")
	if err != nil {
		t.Fatal(err)
	}
	if gotName != "mmdebstrap" {
		t.Fatalf("name %q", gotName)
	}
	joined := strings.Join(gotArgs, " ")
	for _, need := range []string{"--mode=unshare", "--variant=minbase", "--components=main,universe", "--include=git,sudo", "jammy", "/tmp/rootfs", "http://archive.ubuntu.com/ubuntu"} {
		if !strings.Contains(joined, need) {
			t.Fatalf("missing %q in %q", need, joined)
		}
	}
	if !contains(gotArgs, "--customize-hook=echo hi") && !containsHook(gotArgs, "echo hi") {
		t.Fatalf("hook missing: %v", gotArgs)
	}
}

func TestMmdebstrapRootMode(t *testing.T) {
	var joined string
	m := Mmdebstrap{
		Uid: func() int { return 0 },
		RunCmd: func(name string, args []string, stdout, stderr io.Writer) error {
			joined = strings.Join(args, " ")
			return nil
		},
	}
	if err := m.Run("noble", "http://archive.ubuntu.com/ubuntu", nil, nil, "/x"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(joined, "--mode=root") {
		t.Fatalf("got %q", joined)
	}
}

func TestDpkgQueryParse(t *testing.T) {
	q := DpkgQuery{
		RunCmd: func(name string, args []string, stdout, stderr io.Writer) error {
			if name != "dpkg-query" {
				t.Fatalf("name %q", name)
			}
			fmt.Fprint(stdout, "git\t1:2.34.1-1ubuntu1.11\nlibc6\t2.35-0ubuntu3.8\n")
			return nil
		},
	}
	pkgs, err := q.Query("/tmp/rootfs")
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 || pkgs[0].Name != "git" || pkgs[1].Version != "2.35-0ubuntu3.8" {
		t.Fatalf("%#v", pkgs)
	}
}

func containsHook(args []string, hook string) bool {
	for _, a := range args {
		if strings.HasPrefix(a, "--customize-hook=") && strings.Contains(a, hook) {
			return true
		}
	}
	return false
}
```

Add `fmt` and `io` imports to `builder_test.go` as needed.

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/builder/ -run "TestMmdebstrap|TestDpkgQuery" -v`

Expected: FAIL (stubs return "not implemented").

- [ ] **Step 3: Write minimal implementation**

Replace stubs in `bootstrap.go`. Default `RunCmd`:

```go
func defaultRun(name string, args []string, stdout, stderr io.Writer) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
```

On mmdebstrap failure, if `stderr` was captured, wrap the error with the tail of stderr (last 4 KiB). Tests that inject `RunCmd` can ignore this.

- [ ] **Step 4: Run tests and make sure they pass**

Run: `go test ./...`

Expected: PASS

- [ ] **Step 5: Commit**

```
git add internal/builder/bootstrap.go internal/builder/builder_test.go
git commit -m "feat: run mmdebstrap and dpkg-query"
```

---

### Task 11: Integration test (optional tag) and README

**Files:**
- Create: `internal/builder/integration_test.go`
- Modify: `README.md`

**Interfaces:**
- Consumes: the real `Builder` with `Mmdebstrap{}` and `DpkgQuery{}`
- Produces: `-tags=integration` test that skips without Linux/mmdebstrap; README usage

- [ ] **Step 1: Write the integration test**

```go
//go:build integration

package builder

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"frostroot/internal/recipe"
)

func TestIntegrationNobleBash(t *testing.T) {
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
	b := Builder{Bootstrap: Mmdebstrap{}, Query: DpkgQuery{}}
	res, err := b.Build(r, Options{Dir: dir, GOOS: "linux"})
	if err != nil {
		t.Fatal(err)
	}
	lock, err := recipe.LoadLock(res.LockPath)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, p := range lock.Packages {
		if p.Name == "bash" && p.Version != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("bash missing in lock: %+v", lock.Packages)
	}
	if !tarHas(t, res.TarballPath, "etc/wsl.conf") {
		t.Fatal("tarball missing etc/wsl.conf")
	}
	if !tarHasPrefix(t, res.TarballPath, "home/student") {
		t.Fatal("tarball missing home/student")
	}
}

func tarHas(t *testing.T, path, name string) bool {
	t.Helper()
	names := map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		names[h.Name] = true
	}
	return names[name]
}

func tarHasPrefix(t *testing.T, path, prefix string) bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer gr.Close()
	tr := tar.NewReader(gr)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if len(h.Name) >= len(prefix) && h.Name[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}
```

Confirm default tests **do not** run this file:

Run: `go test ./internal/builder/ -count=1`

Expected: PASS, no `TestIntegrationNobleBash`.

- [ ] **Step 2: Replace README.md** with:

```markdown
# frostroot

Freeze an Ubuntu LTS root filesystem into a recipe, an apt lockfile, and a WSL-importable tarball.

v1: Ubuntu 20.04 / 22.04 / 24.04, amd64, apt packages only. Run the CLI on Linux (including WSL).

## Spec

See [docs/superpowers/specs/2026-09-14-frostroot-design.md](docs/superpowers/specs/2026-09-14-frostroot-design.md).

## Build the CLI

```
go build -o frostroot ./cmd/frostroot
```

## Host tools

```
sudo apt install mmdebstrap
```

You need user namespaces or root.

## Usage

```
frostroot init
frostroot validate
frostroot build
```

`build` writes `frostroot.lock` and `dist/<name>-ubuntu-<release>-amd64.tar.gz`. Import on Windows:

```
wsl --import <name> <install-dir> dist/<name>-ubuntu-<release>-amd64.tar.gz
```

Ubuntu 20.04 uses `old-releases.ubuntu.com` by default. Override with `frostroot build --mirror URL`.

## Tests

```
go test ./...
```

Offline, no root, no mmdebstrap. Optional live bootstrap:

```
go test -tags=integration ./internal/builder/ -count=1
```
```

Do not nest fences incorrectly in the real README — use normal markdown code blocks.

- [ ] **Step 3: Run default tests**

Run: `go test ./...`

Expected: PASS (integration file ignored)

- [ ] **Step 4: Commit**

```
git add internal/builder/integration_test.go README.md
git commit -m "test: add optional mmdebstrap integration and README"
```

---

## Self-review (plan author)

**Spec coverage**

| Spec item | Task |
|-----------|------|
| Ubuntu 20.04 old-releases, 22.04/24.04 archive, amd64 | 1, 6 |
| `frostroot.toml` parse/validate (name, user, packages, default_user) | 2, 7 |
| `frostroot.lock` all dpkgs + requested include | 3, 6 |
| `init` prompts, presets, `--force` | 8 |
| `validate` | 7 |
| `build --mirror --keep-rootfs` | 6, 9 |
| mmdebstrap include + customize-hook + unshare/root | 5, 10 |
| Provision wsl.conf, sudoers, locale/timezone, user | 5 |
| Tar.gz skip proc/sys/dev, atomic tmp | 4, 6 |
| Exit 1/2, missing mmdebstrap hint | 9 |
| Not Linux | 6, 9 |
| `go test ./...` offline on Windows | 1–10 fakes |
| Integration tag | 11 |
| No Fedora/PPA/pip/vendor in v1 | Global constraints |
| Manual `wsl --import` | 11 README |

**Types used later match earlier tasks:** `Bootstrapper.Run(suite, mirror, include, hooks, destDir)`, `PackageQuerier.Query`, `recipe.Recipe` fields, `Lockfile` / `LockPackage`, `export.TarballRelPath`, `builder.Version = "0.1.0"`, `Options.GOOS`, `Result.WorkDir`.

**No TBD / "add validation later" / "similar to Task N".** If `cmdInit` in Task 7 is a stub, Task 8 replaces it completely (the stub is only to keep `switch` exhaustive).
