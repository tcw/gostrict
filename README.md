# gostrict

Stricter Go, aimed at code written or edited by an AI model and reviewed by
a person. It turns the mistakes models make most often into compile-time or
check-time errors, and gives the model one tight feedback loop.

gostrict runs **alongside [golangci-lint](https://golangci-lint.run) and
[NilAway](https://github.com/uber-go/nilaway)**, not instead of them. Those
already catch ignored errors, missed enum cases, dead code, leftover TODOs
and nil dereferences; gostrict covers what they do not. Run all three; see
[Alongside golangci-lint and NilAway](#alongside-golangci-lint-and-nilaway).

It has four parts:

| Part | What it does |
|---|---|
| `cmd/strictcheck` | Runs every check (compile, analyzers, tests) and prints **one JSON report** in one shape |
| `analyzer/*` | Five custom analyzers: `argswap`, `units`, `purity`, `contracts`, `directives` |
| `contract` | `contract.Require` / `Ensure` / `Invariant`, compiled out with `-tags nocontracts` |
| `cmd/gosym` | Read and edit code **one declaration at a time, by name**, with imports fixed automatically |

## Setup

gostrict requires Go 1.27.3+ and nothing else. If your installed Go is
older, the `go` command downloads 1.27.3 for you (unless
`GOTOOLCHAIN=local` is set). golangci-lint and NilAway are installed
separately, [below](#alongside-golangci-lint-and-nilaway).

```sh
# in gostrict itself
go test ./...

# in a module you want to check
go get -tool github.com/tcw/gostrict/cmd/strictcheck@latest
go get -tool github.com/tcw/gostrict/cmd/gosym@latest
go get github.com/tcw/gostrict/contract@latest   # if you use contracts

go tool strictcheck              # JSON report
go tool strictcheck -format text # the same, for people
```

`go get -tool` records the tools in your `go.mod`, so everyone on the
project runs the same version. It also raises your `go` line to 1.27.3. To
keep `go.mod` untouched, install the binaries instead and drop the
`go tool` prefix:

```sh
go install github.com/tcw/gostrict/cmd/strictcheck@latest
go install github.com/tcw/gostrict/cmd/gosym@latest
```

## Alongside golangci-lint and NilAway

Each tool covers a different kind of mistake:

| Tool | Catches |
|---|---|
| golangci-lint | Ignored errors (`errcheck`), switches missing an enum case (`exhaustive`), dead code (`unused`), leftover `TODO`/`FIXME` (`godox`), unchecked type assertions, and `staticcheck`/`go vet` bugs |
| NilAway | Nil dereferences, traced across functions and packages |
| gostrict | Swapped arguments, raw floats where unit types belong, I/O in the pure core, unstated preconditions, `//strict:` comments that do nothing; one JSON report and per-declaration editing |

Install both with a Go at least as new as your module's `go` line:

```sh
GOTOOLCHAIN=go1.27.3 go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
GOTOOLCHAIN=go1.27.3 go install go.uber.org/nilaway/cmd/nilaway@latest
```

Without `GOTOOLCHAIN`, `go install` may build them with an older Go, and
then they refuse your module: golangci-lint stops with "the Go language
version ... used to build golangci-lint is lower than the targeted Go
version", and NilAway fails with "failed prerequisites".
`golangci-lint version` shows which Go built it.

Run them from the module root:

```sh
golangci-lint run ./...
nilaway -include-pkgs=example.com/yourmodule ./...   # your module path
```

This repository's [`.golangci.yml`](.golangci.yml) is a starting point;
copy it to your module root. It turns on bug-catching linters, including
the ones above, and `nolintlint`, which makes every `//nolint` name the
linter and give a reason (as `//strict:allow` does). Style linters are left
off: they produce churn, and churn crowds out the findings that matter.

The config runs the full `go vet` suite, as `strictcheck` does. Pass
`-vet off` to `strictcheck` to get each vet finding once.

## strictcheck

```sh
go tool strictcheck [flags] [packages]   # default ./...
```

Steps run in order; if `compile` fails, the rest are skipped, because type
errors make every other report noise.

| Step | What runs |
|---|---|
| `compile` | Loads and type-checks every package, **tests included** |
| `analyze` | The five gostrict analyzers in process, plus the `go vet` suite (and `nilness`, `unusedwrite`) unless `-vet off` |
| `review` | Compares with the default branch and flags changes a person should look at; advisory, see [below](#the-review-step) |
| `test` | `go test -json -count=1`; one finding per failing leaf test, stack traces trimmed to frames inside the module |

Output (abridged):

```json
{
  "ok": false,
  "root": "/home/me/project",
  "steps": [
    {"name": "compile", "status": "pass", "detail": "3 packages", "count": 0, "ms": 780},
    {"name": "analyze", "status": "fail", "detail": "5 analyzers", "count": 2, "ms": 3},
    {"name": "review", "status": "warn", "detail": "against origin/main, 4 files changed", "count": 1, "ms": 21, "advisory": true},
    {"name": "test", "status": "fail", "count": 1, "ms": 236}
  ],
  "counts": {"argswap": 1, "purity": 1, "test-skip": 1, "test-failure": 1},
  "diagnostics": [
    {"step": "analyze", "rule": "argswap", "file": "core/core.go", "line": 36, "col": 20,
     "message": "parameters dx, dy of Move share type float64, so swapped arguments compile silently; ..."},
    {"step": "test", "rule": "test-failure", "file": "core/core_test.go", "line": 13,
     "message": "TestSamePoint failed:\npanic: core/core.go:30: precondition failed in core.Distance: a and b must differ\n..."}
  ]
}
```

Paths are relative to the module root, findings are sorted and
de-duplicated, and `counts` covers everything even when `-max` truncates the
list.

| Flag | Default | Meaning |
|---|---|---|
| `-C dir` | `.` | module root to run in |
| `-steps` | all | e.g. `-steps compile,analyze` for a fast inner loop |
| `-format` | `json` | `json` or `text` |
| `-max` | `200` | cap on printed findings (0 = all) |
| `-short` | off | pass `-short` to `go test` |
| `-vet` | `on` | run the vet suite in `analyze`: `on`, `off` |
| `-base` | default branch | git ref `review` compares with; default `origin/HEAD`, else `origin/main`, `origin/master`, `main`, `master` |
| `-review` | `warn` | `warn`: review findings are reported but leave `ok` true; `fail`: they fail the run |
| `-timeout` | `10m` | overall limit |
| `-<analyzer>.<flag>` | | analyzer flags, e.g. `-purity.allow=github.com/paulmach/orb` |

Exit codes: `0` all passed, `1` findings or a failed step, `2` usage error.

### The review step

A linter sees one snapshot, so it cannot tell that a check passes because
it was weakened. `review` compares the working tree (uncommitted and
untracked files included) with the merge base of the default branch, and
reports changes that make checks pass without fixing anything:

| Rule | Reports |
|---|---|
| `suppression` | new `//nolint`, `//strict:allow`, `//lint:ignore` or `//lint:file-ignore` comments |
| `test-removed` | `Test`, `Benchmark`, `Fuzz` and `Example` functions that are gone from their directory (moving one to another file is fine) |
| `test-skip` | new `t.Skip`, `Skipf` or `SkipNow` calls in test files |
| `marker-removed` | a `//strict:pure`, `//strict:units` or `//strict:contracts` marker the package no longer has |
| `generated-edit` | changes to files marked `Code generated ... DO NOT EDIT`, which the next run of the generator discards unless it produced them |
| `lint-config` | changes to `.golangci.yml` (or `.yaml`, `.toml`, `.json`) |

Each of these is sometimes right, so by default the step is advisory: its
status is `warn` and `ok` stays `true`. The findings are for the person
reviewing the change; `-review fail` makes them fail the run instead.
`testdata` directories are skipped. Outside a git repository, or with no
default branch to compare with, the step is skipped.

## The analyzers

Three analyzers are opt-in per package with a marker line above the package
clause, so you can make the core strict and leave the shell alone:

```go
// Package route plans routes over terrain.
//
//strict:pure
//strict:units
//strict:contracts
package route
```

| Analyzer | Applies to | Reports |
|---|---|---|
| `argswap` | every package (not tests) | Adjacent parameters sharing a predeclared type: `Move(x, y, dist float64)`. Swapped arguments compile silently. Fix with defined types (`Meters`, `Degrees`) or a struct parameter. Methods named `Less`/`Swap` are skipped (`-argswap.ignore`). |
| `units` | `//strict:units` | Raw `float32`/`float64` in exported signatures and exported struct fields, also inside slices, maps and pointers. `-units.ints` adds integer types, except byte slices and arrays (`[]byte`, `[32]byte`), which are data. |
| `purity` | `//strict:pure` | Imports that do I/O or add nondeterminism or concurrency (`os`, `net`, `net/http`, `log`, `context`, `sync`, `math/rand`, ...); imports of non-standard packages not themselves marked pure; `time.Now`, `time.Sleep`, `fmt.Println` and friends; `go` statements; package-level variables other than error sentinels. |
| `contracts` | `//strict:contracts` | Exported functions with parameters whose body does not start with `contract.Require`; contract calls whose condition is constant `true` or whose message is empty or not a constant string. |
| `directives` | every package, tests included | `//strict:` comments that have no effect: unknown directives (`//strict:puer`), unknown rules in `//strict:allow`, markers below the package clause or with text after them, `// strict:pure` (the space makes it an ordinary comment), and `//strict:allow` directives that suppress no finding. |

`purity` works across packages: each pure package exports a fact, so a pure
package importing an unmarked package from your module is reported.
Third-party packages you trust to be pure go in `-purity.allow`.

### Suppressing a finding

Put a directive with a **reason** in the declaration's doc comment. A
directive without a reason still suppresses, but is itself reported.

```go
// Max returns the larger value.
//
//strict:allow argswap commutative, order does not matter
func Max(a, b int) int
```

Several rules can share one directive: `//strict:allow argswap,units <reason>`.

A suppression that no longer suppresses anything is reported by
`directives`, so exceptions do not outlive the code they excused. That
includes one in the wrong place: it must be in the doc comment of the
declaration with the finding (or, for struct fields and imports, on the
field or import itself).

### Running the analyzers alone

```sh
go run github.com/tcw/gostrict/cmd/strictvet ./...
go vet -vettool=$(command -v strictvet) ./...
```

## contract

```go
func Distance(a, b Point) Meters {
	contract.Require(a != b, "a and b must differ")
	d := ...
	contract.Ensure(d > 0, "distinct points are a positive distance apart")
	return d
}
```

A failed contract is a programmer error, so it panics with a
`*contract.Violation` that names the function, file and line. Expected
failures (bad input from outside, I/O) should still be returned as errors.
Build with `-tags nocontracts` to make the calls no-ops; the condition is
still evaluated, so keep it cheap and side-effect free.

## gosym

```sh
gosym list    [-json] <file|dir>
gosym show    [-n] <file|dir> <name>
gosym replace [-f file] [-rename] <file|dir> <name>   # new source on stdin
gosym add     [-f file] [-after name] <file>          # new source on stdin
gosym delete  <file|dir> <name>
```

Names are `Func`, `Type`, `Var`, `Const` or `Type.Method`; `(*T).M` and
`T[K].M` work too. Given a directory, gosym finds the file that declares the
name, and refuses if several do.

Every edit is checked before the file is touched: the new source must parse,
`replace` must define the name it replaces (unless `-rename`), `add` and
`-rename` refuse names the package already declares, and import declarations
in the new source are rejected. After the edit the file is formatted and
imports are added or removed as goimports would, then written atomically.
A declaration inside a grouped `type ( ... )` or `var ( ... )` block is
replaced in place and the group is kept.

```sh
$ gosym replace core Move <<'EOF'
// Move shifts a point by a planar offset.
func Move(p Point, dx, dy Meters) Point {
	contract.Require(!math.IsNaN(float64(dx+dy)), "offset must be a number")
	return Point{p.X + dx, p.Y + dy}
}
EOF
replaced func Move in core/core.go (lines 35-39)
```

## Instructions for an AI agent

Paste into the module's `CLAUDE.md` (or equivalent):

```markdown
## Checking and editing Go code

- After every change, run `go tool strictcheck -steps compile,analyze` and fix
  everything it reports before continuing. Treat `"ok": false` as not done.
- Before saying a task is done, run all three and fix what they report:
  `go tool strictcheck`, `golangci-lint run ./...` and
  `nilaway -include-pkgs=<module path> ./...`.
- List every finding of strictcheck's `review` step in your summary to the
  user, each with one line on why the change is right. Do not leave them out
  because `ok` is `true`.
- Read and edit by declaration: `go tool gosym show <dir> <Name>` to read,
  `go tool gosym replace <dir> <Name>` with the complete new declaration on
  stdin to change it, `gosym add`/`delete` for new or removed declarations.
  Do not rewrite whole files to change one function.
- Do not add `//strict:allow` or `//nolint` to make a finding go away
  unless the finding is wrong for that code; the reason must say why.
- In `//strict:pure` packages, pass time, randomness and I/O results in as
  parameters. In `//strict:units` packages, use unit types, not float64.
```

## Limitations

- **Approximations, not guarantees.** Go has no sum types, named arguments
  or effect system; these checks enforce conventions that stand in for them.
- `purity` treats an import path as standard library when its first element
  has no dot. A module whose path has no dot (`module myapp`) has its own
  packages treated as standard library, so cross-package purity checks miss
  them; use a dotted module path.
- `purity` judges packages, not functions: a function in a pure package is
  pure, and a function elsewhere is not checked.
- `contracts` checks that preconditions are stated, not that they are
  sufficient. The checks run at runtime.
- `argswap` only looks at predeclared types, so `from, to Point` is not
  reported. That is intentional: defined types are the remedy, and flagging
  them too would make the rule too noisy to keep on.
- The gostrict analyzers run in `strictcheck`/`strictvet`, not inside
  golangci-lint. Wiring them in would need golangci-lint's module plugin
  system and a custom build.

## Development

```sh
go test ./...
go run ./cmd/strictcheck -format text   # the repository checks itself
golangci-lint run ./...
nilaway -include-pkgs=github.com/tcw/gostrict ./...
```

CI runs all of these on every pull request.
