# go-googlesql

[![CI](https://github.com/esilver/go-googlesql/actions/workflows/ci.yml/badge.svg)](https://github.com/esilver/go-googlesql/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/goccy/go-googlesql.svg)](https://pkg.go.dev/github.com/goccy/go-googlesql)

Pure-Go bindings for [GoogleSQL](https://github.com/google/googlesql),
the SQL dialect that powers BigQuery, Spanner, and other Google Cloud
databases.

## Why this library

I previously maintained
[`goccy/go-zetasql`](https://github.com/goccy/go-zetasql), which
exposed the same engine through cgo. The cgo dependency made
cross-compilation, static linking, and overall portability painful.
`go-googlesql` solves that by compiling GoogleSQL down to WebAssembly
and then transpiling that wasm to pure Go source via
[`goccy/wasm2go`](https://github.com/goccy/wasm2go) — no cgo, no
native toolchain, no embedded wasm runtime, just a regular Go library.

## Features

- **Pure Go, no cgo, no wasm runtime.** GoogleSQL is transpiled from
  WebAssembly directly to Go source by
  [`goccy/wasm2go`](https://github.com/goccy/wasm2go), so there is no
  embedded interpreter or JIT — the engine is just Go code the toolchain
  compiles ahead of time. `CGO_ENABLED=0` builds, static linking, and
  cross-compilation all work without extra setup.
- **Generated engine, patched bridge.** The transpiled engine is
  consumed through
  [`github.com/goccy/googlesqlwasm2go`](https://github.com/goccy/googlesqlwasm2go).
  This fork carries a small downstream patch set in `googlesql.go`
  for bridge lifetime and large-memory behavior until those changes
  can move upstream into the generator or release artifact.
- **End-to-end provenance for upstream release artifacts.** Every
  upstream generated artifact is signed with
  [GitHub artifact attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations).
  The `make verify` target can re-verify an unmodified bridge file
  against the signed upstream release (see
  [Verifying provenance](#verifying-provenance)).

## Status

Tracks GoogleSQL revision
[`36dd14aa0657ea299725504bc0f938732f58f380`](https://github.com/google/googlesql/commit/36dd14aa0657ea299725504bc0f938732f58f380)
(2026-01-31). New upstream revisions are picked up here as they land.

The module path remains `github.com/goccy/go-googlesql` for compatibility.
This fork is consumed by
[`esilver/googlesqlite`](https://github.com/esilver/googlesqlite) and
[`esilver/bigquery-emulator`](https://github.com/esilver/bigquery-emulator)
through replace directives while the bridge-lifetime and large-memory fixes
remain downstream.

## Installation

```sh
go get github.com/goccy/go-googlesql
```

The first build is heavy: the engine is shipped as transpiled Go
source via `github.com/goccy/googlesqlwasm2go` plus a large generated bridge in
`googlesql.go`. Expect the Go compiler to need several gigabytes of
RAM and a couple of minutes for a cold build. See
[Resource footprint](#resource-footprint) for measured numbers and
runtime cost.

## Synopsis

### Initialise the engine

`Init` initialises the transpiled wasm2go engine. Call it once per
process before using any other API; it is `sync.Once`-guarded so
calling more than once is a no-op. There is no runtime to tear down,
so no `Close` is needed.

```go
package main

import "github.com/goccy/go-googlesql"

func main() {
    if err := googlesql.Init(); err != nil {
        panic(err)
    }

    // ...use the parser / analyzer APIs here...
}
```

### Parse a SQL statement

```go
opts, err := googlesql.NewParserOptions()
if err != nil {
    panic(err)
}
out, err := googlesql.ParseStatement("SELECT * FROM Samples WHERE id = 1", opts)
if err != nil {
    panic(err)
}
stmt, err := out.Statement()
if err != nil {
    panic(err)
}

// Use a type assertion to reach concrete AST node types.
queryStmt, ok := stmt.(*googlesql.ASTQueryStatement)
_ = queryStmt
_ = ok
```

### Analyze a SQL statement against a catalog

```go
catalog, err := googlesql.NewSimpleCatalog("catalog", nil)
if err != nil {
    panic(err)
}

langOpts, err := googlesql.NewLanguageOptions()
if err != nil {
    panic(err)
}
_ = langOpts.EnableMaximumLanguageFeaturesForDevelopment()
_ = langOpts.SetSupportsAllStatementKinds()

if err := catalog.AddBuiltinFunctionsAndTypes(
    &googlesql.BuiltinFunctionOptions{LanguageOptions: langOpts},
); err != nil {
    panic(err)
}

opts, err := googlesql.NewAnalyzerOptions2()
if err != nil {
    panic(err)
}
_ = opts.SetLanguage(langOpts)

tf, err := googlesql.NewTypeFactory()
if err != nil {
    panic(err)
}

out, err := googlesql.AnalyzeStatement(
    "SELECT 1 AS col1, 'hi' AS col2",
    opts, catalog, tf,
)
if err != nil {
    panic(err)
}
resolved, err := out.ResolvedStatement()
if err != nil {
    panic(err)
}
_ = resolved
```

## Verifying provenance

You can re-run the upstream attestation check yourself, locally and
**without a GitHub access token**, when `googlesql.go` is byte-for-byte
the upstream release artifact:

```sh
make googlesql
```

The Makefile target downloads the upstream bridge into `.upstream/`
without replacing this fork's patched `googlesql.go`, then fetches the
upstream SLSA build-attestation bundle from the public
`/repos/.../attestations/sha256:<digest>` API (anonymously) and hands it
to `gh attestation verify --bundle`. The `--signer-workflow` flag pins
the trusted signer to
`goccy/googlesql-wasm/.github/workflows/build.yml`, so only files
produced by that workflow verify successfully.

Both checks run unauthenticated — no `gh auth login`, no `GH_TOKEN` /
`GITHUB_TOKEN` (technique from
<https://zenn.dev/shunsuke_suzuki/articles/gh-at-verify-without-access-token>).
This fork patches `googlesql.go`, so the checked-in file cannot pass
that upstream attestation check: its digest is intentionally not one of
the signed release artifacts. `make verify-upstream` remains useful when
checking a freshly downloaded upstream artifact. CI for this fork runs
the Go test suite instead.

One regression test intentionally allocates past 2 GiB to protect the
large-linear-memory bridge behavior. Use `go test -short ./...` for a
low-memory local smoke run; default CI uses that short path. Run the
full suite manually on a runner with enough disk and memory for the
generated bridge when validating large-memory changes.

## Resource footprint

Because GoogleSQL ships as ahead-of-time transpiled Go (through the
`github.com/goccy/googlesqlwasm2go` module) instead of a wasm module
plus a runtime, the cost shifts from process startup to the Go
toolchain: compilation is heavier than a typical dependency, but in
exchange `googlesql.Init` is fast and the steady-state heap is small.

All numbers below were measured on the same host:

- Hardware: Apple M5, 10 cores, 32 GB RAM
- OS: macOS 26.2 (Darwin 25.2.0, arm64)
- Toolchain: Go 1.26.2 darwin/amd64 (x86_64 binary via Rosetta — a
  native arm64 toolchain should be somewhat faster)

### Build

Cold-cache `go test -c` against this package (`GOCACHE` pointed at an
empty directory, measured with `/usr/bin/time -l`).

| Phase            | Wall time | Peak RSS | Binary size |
|------------------|-----------|----------|-------------|
| `go test -c .`   | ~76 s     | ~5.6 GB  | ~63 MB      |

Subsequent builds with a warm cache complete in a few hundred
milliseconds. The peak RSS spike is the linker; expect a build host
with at least 8 GB of RAM available.

### Runtime — `googlesql.Init`

`Init` is called once per process. Wall and CPU were captured around
the `Init` call via `syscall.Getrusage`; heap was sampled via
`runtime.MemStats` after a `runtime.GC()` so transient init
allocations are excluded; process peak RSS came from
`/usr/bin/time -l` over the whole process.

| Metric                    | Value    |
|---------------------------|----------|
| Init wall time            | ~150 ms  |
| Init CPU time             | ~250 ms  |
| Steady-state Go heap      | ~70 MiB  |
| Process peak RSS          | ~87 MiB  |

There are no execution-mode knobs — `Init()` takes no arguments and
there is no `Close`. Pay the one-time ~150 ms latency, then expect a
roughly 70 MiB resident heap for the lifetime of the process.

## License

[MIT](LICENSE).
