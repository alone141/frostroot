# Contributing to frostroot

frostroot is written to be read. Anyone opening a file should be able to tell
what every name means without scrolling, and the code should be a fair example
of idiomatic Go. This document is the standard; `golangci-lint` and CI enforce
the parts a machine can check.

If this document and the code disagree, fix one of them in the same change.

## Before you push

```sh
gofmt -l .                          # prints nothing
go vet ./...
go test -race ./...
GOOS=windows go build ./...         # the module must still compile off Linux
golangci-lint run ./...             # v2.13.2, configured by .golangci.yml
```

CI runs the same checks on every push and pull request. The integration test
needs mmdebstrap, network and user namespaces, so it runs only by hand:

```sh
go test -tags=integration -run TestIntegration -v -timeout 30m ./internal/builder/
```

## The base standard

In order of precedence:

1. The [Google Go Style Guide](https://google.github.io/styleguide/go/)
   (guide, decisions, best practices).
2. [Effective Go](https://go.dev/doc/effective_go).
3. [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments).

The rules below are the ones that come up most here, plus the few choices this
project makes where those documents leave room.

## Names

**A name says what the thing is.** `recipePath`, `workRoot`, `tarballInfo`,
`installedPackageCount`, not `p`, `wr`, `fi`, `n`. Functions say what they do:
`checkOutputWritable`, `renderProvisionScript`, `combinePackages`.

Only these stay short, because every Go reader knows them:

| Name | Meaning |
|---|---|
| `err` | the error just returned |
| `ctx` | a `context.Context` |
| `t`, `b` | `*testing.T`, `*testing.B` |
| `i`, `j` | an index in a loop of a few lines |
| method receivers | one or two letters from the type: `(a *App)`, `(b *Builder)`, `(r Release)` |

Other Go naming rules that surprise newcomers:

- **Exported means capitalized.** `Load` is visible outside the package,
  `decodeStrict` is not. There are no `public`/`private` keywords.
- **Initialisms keep one case:** `UID`, `URL`, `WSL`, `ID` in exported names,
  `uid`, `mirrorURL` otherwise. Never `Uid` or `Url`.
- **No stutter.** The package name is part of every use, so `recipe.Load`, not
  `recipe.LoadRecipe`; `export.Place`, not `export.ExportPlace`.
- **No `Get` prefix** on getters.
- **Don't shadow an imported package.** A variable named `recipe` hides the
  `recipe` package for the rest of the scope. This project uses
  `imageRecipe` for a `recipe.Recipe` everywhere, and revive's
  `import-shadowing` rule catches slips.
- **Sentinel errors are `ErrXxx`,** error types are `XxxError`.
- **Constants are MixedCaps,** not `ALL_CAPS`: `maxUserNameLength`.

File names are lowercase, one word where possible, named for what they hold
(`build.go`, `workdir.go`). Platform-specific files end in `_unix.go` or
`_other.go` with a matching build constraint.

## Errors

Go has no exceptions. A function that can fail returns an `error` last, and
the caller handles it immediately.

- **Never drop an error silently.** When ignoring one is really right, assign
  it to `_` and say why in a comment:

  ```go
  defer func() { _ = file.Close() }() // read-only: closing cannot lose data
  ```

- **Add context when passing an error up,** with `%w` so callers can still
  inspect it: `fmt.Errorf("reading %s: %w", path, err)`. Don't repeat what the
  wrapped error already says.
- **Compare with `errors.Is` and `errors.As`,** never `==` or a type switch on
  a wrapped error.
- **Error strings are lowercase with no final period,** because they get
  embedded in other messages: `keyring for the Ubuntu archive not found`.
- **Several independent problems:** collect them with `errors.Join`, so the
  user sees all of them at once.
- **No `panic`** for anything a user, a file or the host can cause. Panics are
  for broken invariants inside the program, and even then an error is usually
  better.
- User-facing text is printed in exactly one place, the `cli` package. Library
  packages return errors; they never print or exit.

## Design

- **Accept interfaces, return concrete types.** Interfaces are small and live
  with the code that uses them (`builder.Bootstrapper` is declared by the
  builder, which consumes it, and implemented by `Mmdebstrap`).
- **No mutable package-level state.** Package-level `var`s hold values that
  never change after initialization: tables, compiled regular expressions,
  parsed templates. Anything a test would want to replace is passed in as a
  field or parameter (`Options.Getenv`, `App.WSLPath`) instead.
- **Zero values are useful.** `App{}` and `Mmdebstrap{}` work; unset fields
  fall back to the real environment.
- **`context.Context` is the first parameter** of anything that can block or be
  cancelled, and is never stored in a struct.
- **Keep the happy path unindented.** Handle the error and return early; don't
  nest the rest of the function in an `else`.
- **Standard library first.** A new dependency needs a reason that would
  convince a reviewer.

## Comments

- **Every exported identifier has a doc comment** that starts with its name
  and is a full sentence: `// Load reads and strictly decodes a recipe file.`
- Every package has one package comment, in its main file.
- Comments explain **why**, or what is not obvious from the code: the drvfs
  chmod failure, the reason mmdebstrap is never sent SIGKILL. They do not
  narrate what the next line does.
- American spelling in code and comments, as in the Go standard library.

## Tests

- Standard library `testing` only; no assertion libraries.
- **Table-driven** when several inputs exercise the same behavior, with
  `t.Run` subtests named after the case.
- Failure messages say what was wrong, as `got, want`:
  `t.Errorf("packageCount(%d) = %q, want %q", count, got, want)`.
- `t.Fatal` when the rest of the test cannot run, `t.Error` otherwise, so one
  run reports every failure.
- **Helpers call `t.Helper()`** first, so failures point at the caller.
- Use `t.TempDir()` for files; tests never touch the real home directory or
  `/var/tmp`.
- Fakes implement the same interface as the real thing and live in the test
  file that uses them. Name them for what they fake: `fakeBootstrapper`,
  `scriptedPrompt`.
- Tests run offline, without root and without mmdebstrap. Anything else goes
  behind the `integration` build tag.

## Formatting and imports

`gofmt` decides formatting; there is nothing to discuss. Imports come in two
groups separated by a blank line: the standard library, then everything else,
with frostroot's own packages last (`goimports -local frostroot`).

## Commits and pull requests

- [Conventional Commits](https://www.conventionalcommits.org/):
  `feat:`, `fix:`, `docs:`, `test:`, `refactor:`, `chore:`, `ci:`. The subject
  says what changed, in the imperative, lowercase, without a final period.
  The body says why.
- One logical change per commit; every commit builds and passes the tests.
- Pull requests merge into `master` only when CI is green.

## Coming from C++

A few Go habits that differ from C++ and show up throughout this code:

| C++ habit | Go equivalent here |
|---|---|
| exceptions, RAII | returned `error` values; `defer` for cleanup |
| `const&` parameters everywhere | pass small structs by value; pointers when the callee mutates or the struct is large |
| class hierarchies | small interfaces satisfied implicitly, plus struct embedding |
| constructors | a useful zero value, or a `NewXxx` function when there are invariants |
| header/source split, `public:` | one package per directory; capitalized names are exported |
| `std::optional<T>` | a second return value: `value, found := m[key]` |
| templates | generics exist, but concrete code is preferred until duplication hurts |
| `snake_case` / `m_member` | `mixedCaps`, no prefixes; the receiver name says whose field it is |
