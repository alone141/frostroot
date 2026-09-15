# frostroot

Freeze a Linux root filesystem. v1: Ubuntu LTS recipe → apt lockfile + WSL-importable tarball.

This repo is in the design stage. Read these in order before writing code:

1. [Design spec](docs/superpowers/specs/2026-09-14-frostroot-design.md) — what v1 is and is not.
2. [Implementation plan](docs/superpowers/plans/2026-09-15-frostroot-v1.md) — **execute this one.** Starts with a manual spike that gates all Go work.
3. [Plan review](docs/superpowers/reviews/2026-09-14-frostroot-plan-review.md) and [feasibility analysis](docs/superpowers/reviews/2026-09-15-frostroot-feasibility.md) — why the first plan was replaced.

The [2026-09-14 plan](docs/superpowers/plans/2026-09-14-frostroot.md) is superseded and must not be executed.

Known deviations from the spec are listed in the new plan's self-review; the spec
needs updating to match (essentials list, update pockets, work directory,
`--keep-work`, lock `sources`).
