# frostroot

**Freeze an Ubuntu root filesystem into a recipe, a lockfile, and a golden image you can hand to anyone.**

> ### Status: design stage — not implemented yet
>
> There is no Go code in this repository. Nothing is installable and nothing
> runs. What exists is a reviewed design, a corrected implementation plan, and
> a feasibility analysis.
>
> The build pipeline is **unverified**: it is validated on paper, not by a real
> build. [Task 0 of the plan](docs/superpowers/plans/2026-09-15-frostroot-v1.md)
> is a manual spike that gates all coding work.
>
> If you want to understand the project, read the
> [design spec](docs/superpowers/specs/2026-09-14-frostroot-design.md).

---

## The problem

You are setting up a programming lab for thirty students. Everyone needs the
same compiler, the same tools, the same versions. "Run these apt commands"
does not work: a student installing today gets different package versions than
one who installed last month, and some lab machines have no internet at all.

frostroot builds the environment **once**, freezes it, and gives you a file.
Everyone imports that same file and gets an identical machine.

## How it works

You write about fifteen lines of TOML:

```toml
[image]
name = "cpp-lab"
release = "22.04"     # 20.04 | 22.04 | 24.04
arch = "amd64"

[user]
name = "student"
sudo = true

[wsl]
systemd = true

[locale]
lang = "en_US.UTF-8"
timezone = "UTC"

[packages]
include = ["git", "build-essential", "cmake"]
```

Run `frostroot build`, and you get two things:

**`dist/cpp-lab-ubuntu-22.04-amd64.tar.gz`** — the frozen machine, a few
hundred megabytes. This is what you hand out. On Windows:

```powershell
wsl --import cpp-lab C:\wsl\cpp-lab dist\cpp-lab-ubuntu-22.04-amd64.tar.gz
wsl -d cpp-lab
```

No internet needed on the receiving end.

**`frostroot.lock`** — a receipt listing every package that ended up inside,
with exact versions. You asked for three packages; installing them pulled in
several hundred dependencies, and the lock records all of them. Commit it to
git and you can see exactly what changed between builds.

```toml
version = 1
distro = "ubuntu"
suite = "jammy"
sources = [
  "deb http://archive.ubuntu.com/ubuntu jammy main universe",
  "deb http://archive.ubuntu.com/ubuntu jammy-updates main universe",
  "deb http://archive.ubuntu.com/ubuntu jammy-security main universe",
]
requested = ["git", "build-essential", "cmake"]

[[packages]]
name = "git"
version = "1:2.34.1-1ubuntu1.11"
arch = "amd64"
```

The recipe is **intent** and you edit it. The lock is **fact** and the build
writes it. Versions never appear in the recipe.

## Commands

| Command | What it does |
|---|---|
| `frostroot init` | Asks a few questions, writes `frostroot.toml` |
| `frostroot validate` | Checks the recipe. No network, no root |
| `frostroot build` | Recipe → lockfile + tarball |

Three verbs. `build` never prompts; everything it needs is in the recipe.

## Requirements

frostroot is a **Linux** CLI. Windows users run it inside WSL — WSL is an
export target, not a host.

```sh
sudo apt install mmdebstrap
```

Plus either user namespaces (normal on modern distros) or root. Building needs
network access; consuming the resulting tarball does not.

## Pipeline

```
frostroot.toml ──▶ validate ──▶ mmdebstrap ──▶ image.tar.gz ──▶ dist/*.tar.gz
                                     │              │
                                customize      dpkg status ──▶ frostroot.lock
                                  hooks
```

`mmdebstrap` bootstraps the base system, customize hooks provision it (user,
sudo, locale, timezone, `/etc/wsl.conf`), and mmdebstrap writes the tarball
itself — from inside the user namespace, which is the only place ownership,
symlinks and file capabilities come out correct.

## Scope

**In v1:** Ubuntu 20.04 / 22.04 / 24.04, amd64, apt packages by name, a sudo
user, WSL-ready images.

**Deliberately not in v1:** Fedora or any non-Ubuntu family · PPAs and extra apt
sources · pip / npm / cargo lockfiles · vendoring `.deb` files and offline
builds · bit-identical rebuilds · bare-metal disk or ISO images · a package
picker TUI · a native Windows binary · architectures other than amd64 ·
capturing an existing machine (`frostroot capture`, planned for after v1).

Each exclusion has a door left open in the design. Adding Fedora means a new
`internal/distro` implementation, not a rewrite. The v1 job is to prove one
narrow case works properly.

## Known limitations

**"Freeze" has a limit.** The tarball is genuinely frozen — import it in five
years and it is identical. But *rebuilding* from the same recipe next month may
produce slightly different versions, because the build fetches whatever the
Ubuntu archive currently holds. Guaranteeing byte-identical rebuilds means
vendoring the package files, which is the planned next step, not this one. The
file you hand out is reproducible; the act of building is not, yet.

**20.04 images ship known unfixed CVEs.** Focal's standard support ended in May
2025. Its packages are still on the archive, but security fixes since then go
to Ubuntu Pro, not to `focal-security`. Pinning an old release is the whole point of the tool, but
`build` warns you, and you should prefer 22.04 or 24.04 unless you specifically
need focal.

**Python on 24.04.** PEP 668 makes `pip install` outside a virtualenv fail by
design. Use `python3 -m venv`.

**Redistribution.** A golden image contains Ubuntu binaries. frostroot's own
MIT licence covers frostroot, not the packages it bundles into an image.
Redistributing unmodified archive packages is fine; Canonical's trademark
policy constrains calling a modified image "Ubuntu". Worth a look before
publishing images publicly.

**The WSL boot check is manual.** No CI runner can `wsl --import`, so the one
test that proves the product actually works is a human at a Windows machine.

## Documentation

| Document | What it is |
|---|---|
| [Design spec](docs/superpowers/specs/2026-09-14-frostroot-design.md) | Source of truth for v1. Start here. |
| [Implementation plan](docs/superpowers/plans/2026-09-15-frostroot-v1.md) | 13 tasks, test-first. **Execute this one.** |
| [Plan review](docs/superpowers/reviews/2026-09-14-frostroot-plan-review.md) | Found four defects that would have shipped a non-booting image |
| [Feasibility analysis](docs/superpowers/reviews/2026-09-15-frostroot-feasibility.md) | Independent assessment; confirmed the defects and added five more |
| [Original plan](docs/superpowers/plans/2026-09-14-frostroot.md) | **Superseded — do not execute.** Kept for history. |

The two reviews are dated records of what the spec and plan said on those dates;
the spec has since been revised to match the corrected plan.

## Planned layout

```
cmd/frostroot/      main
internal/cli/       init, validate, build
internal/recipe/    toml + lock parsing and validation
internal/distro/    Ubuntu releases, mirrors, update pockets
internal/builder/   orchestration, mmdebstrap, hooks, dpkg status
internal/export/    artifact naming and placement
testdata/           recipe fixtures
```

One Go module, one binary, roughly 2,000 lines including tests.

## License

[MIT](LICENSE). Note that this covers frostroot itself — the packages it
bootstraps into an image carry their own licences from the Ubuntu archive.
