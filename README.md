# go-googlesql

> A fork of [goccy/go-googlesql](https://github.com/goccy/go-googlesql), the ZetaSQL/GoogleSQL analyzer bridge, carrying downstream memory-safety patches to the generated wasm2go bridge (see [UPSTREAMING.md](UPSTREAMING.md)). The original library is by [@goccy](https://github.com/goccy).

[![CI](https://github.com/esilver/go-googlesql/actions/workflows/ci.yml/badge.svg)](https://github.com/esilver/go-googlesql/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/goccy/go-googlesql.svg)](https://pkg.go.dev/github.com/goccy/go-googlesql)

Pure-Go bindings for [GoogleSQL](https://github.com/google/googlesql), the SQL dialect behind BigQuery, Spanner, and other Google Cloud databases. No cgo, no native toolchain, no embedded wasm runtime.

## Why this library

`go-googlesql` is the pure-Go successor to [`goccy/go-zetasql`](https://github.com/goccy/go-zetasql), which exposed the same engine through cgo. The cgo dependency made cross-compilation, static linking, and portability painful. This library compiles GoogleSQL to WebAssembly and transpiles that wasm to Go source via [`goccy/wasm2go`](https://github.com/goccy/wasm2go), so `CGO_ENABLED=0` builds, static linking, and cross-compilation all work with no extra setup.

## Features

- **Pure Go, no cgo, no wasm runtime.** The engine is transpiled wasm-to-Go ahead of time and shipped as the [`googlesqlwasm2go`](https://github.com/goccy/googlesqlwasm2go) module, with no embedded interpreter or JIT.
- **Generated engine, patched bridge.** This fork carries a downstream patch set in `googlesql.go` for bridge lifetime and large-memory behavior, published as unified diffs under `patches/`. The diff is dominated by one mechanical `free()`-body substitution, so the reviewable logic is small. See [UPSTREAMING.md](UPSTREAMING.md).
- **Verified provenance.** Every upstream generated artifact is signed with [GitHub artifact attestations](https://docs.github.com/en/actions/security-for-github-actions/using-artifact-attestations). `make googlesql` downloads and verifies the upstream bridge against the signed release (see [Verifying provenance](#verifying-provenance)).

## Status

Tracks GoogleSQL revision [`36dd14a`](https://github.com/google/googlesql/commit/36dd14aa0657ea299725504bc0f938732f58f380) (2026-01-31), and new upstream revisions are picked up as they land. The module path stays `github.com/goccy/go-googlesql` for compatibility. This fork is consumed by [`esilver/googlesqlite`](https://github.com/esilver/googlesqlite) and [`esilver/bigquery-emulator`](https://github.com/esilver/bigquery-emulator) through replace directives while the bridge-lifetime and large-memory fixes remain downstream.

## Installation

```sh
go get github.com/goccy/go-googlesql
```

The first build is heavy: the engine ships as transpiled Go source plus a large generated bridge in `googlesql.go`, so a cold build needs several gigabytes of RAM and a couple of minutes. See [Resource footprint](#resource-footprint) for measured numbers.

## Synopsis

`Init` initializes the transpiled engine. Call it once per process before any other API. It is `sync.Once`-guarded, takes no arguments, and needs no `Close` (there is no runtime to tear down).

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

Parse a statement (the `package`/`import`/`main` lines from above are elided, error handling on option setters too):

```go
opts, _ := googlesql.NewParserOptions()
out, _ := googlesql.ParseStatement("SELECT * FROM Samples WHERE id = 1", opts)
stmt, _ := out.Statement()

// Type-assert to reach concrete AST node types.
queryStmt := stmt.(*googlesql.ASTQueryStatement)
fmt.Printf("parsed query: %T\n", queryStmt)
```

Analyze a statement against a catalog:

```go
catalog, _ := googlesql.NewSimpleCatalog("catalog", nil)
langOpts, _ := googlesql.NewLanguageOptions()
_ = langOpts.EnableMaximumLanguageFeaturesForDevelopment()
_ = langOpts.SetSupportsAllStatementKinds()
_ = catalog.AddBuiltinFunctionsAndTypes(&googlesql.BuiltinFunctionOptions{LanguageOptions: langOpts})

opts, _ := googlesql.NewAnalyzerOptions2()
_ = opts.SetLanguage(langOpts)
tf, _ := googlesql.NewTypeFactory()

out, _ := googlesql.AnalyzeStatement("SELECT 1 AS col1, 'hi' AS col2", opts, catalog, tf)
resolved, _ := out.ResolvedStatement()
fmt.Printf("resolved statement: %T\n", resolved)
```

## Verifying provenance

Two distinct checks cover different files. `make verify-upstream` attests a freshly downloaded upstream artifact against GitHub's signed release. `make verify-patch` proves this fork's checked-in bridge is exactly that upstream baseline plus the published patches. The patched `googlesql.go` is not a signed release artifact, so it only passes the second check.

- **`make verify-upstream`.** `make googlesql` downloads the upstream bridge into `.upstream/` (leaving the patched `googlesql.go` untouched), fetches the SLSA build-attestation bundle from the public `/repos/.../attestations/sha256:<digest>` API, and hands it to `gh attestation verify --bundle`. The `--signer-workflow` flag pins the trusted signer to `goccy/googlesql-wasm/.github/workflows/build.yml`. Both steps run unauthenticated, with no `gh auth login` and no `GH_TOKEN` / `GITHUB_TOKEN` ([technique](https://zenn.dev/shunsuke_suzuki/articles/gh-at-verify-without-access-token)).
- **`make verify-patch`.** Re-derives `googlesql.go` from the upstream baseline plus `patches/` and diffs it against the checked-in file. Needs the `upstream` git remote, see [UPSTREAMING.md](UPSTREAMING.md) for setup. CI runs the Go test suite rather than the attestation check.

## Caveats

- **Heavy first build.** The generated bridge needs several gigabytes of RAM and a couple of minutes for a cold compile (see [Resource footprint](#resource-footprint)). The cost is Go compile time, not runtime: `Init` is fast and the steady-state heap is small.
- **Large-memory regression test.** One regression test intentionally allocates past 2 GiB to guard the large-linear-memory bridge behavior. Use `go test -short ./...` for a low-memory smoke run. CI runs the large-memory regression as a separate resource-capped job. Run the full suite on a host with enough disk and memory when validating broader large-memory changes.

## Resource footprint

GoogleSQL ships as ahead-of-time transpiled Go, so the cost shifts from process startup to the Go toolchain. All numbers below were measured on the same host:

- Hardware: Apple M5, 10 cores, 32 GB RAM
- OS: macOS 26.2 (Darwin 25.2.0, arm64)
- Toolchain: Go 1.26.2 darwin/amd64 (x86_64 via Rosetta, a native arm64 toolchain should be somewhat faster)

**Build.** Cold-cache `go test -c` against this package (`GOCACHE` pointed at an empty directory, measured with `/usr/bin/time -l`):

| Phase            | Wall time | Peak RSS | Binary size |
|------------------|-----------|----------|-------------|
| `go test -c .`   | ~76 s     | ~5.6 GB  | ~63 MB      |

Warm-cache builds finish in a few hundred milliseconds. The peak RSS spike is the linker, so expect a build host with at least 8 GB of RAM.

**Runtime.** `Init` is called once per process. Wall and CPU were captured around `Init` via `syscall.Getrusage`, heap via `runtime.MemStats` after a `runtime.GC()`, and process peak RSS via `/usr/bin/time -l`:

| Metric                | Value    |
|-----------------------|----------|
| Init wall time        | ~150 ms  |
| Init CPU time         | ~250 ms  |
| Steady-state Go heap  | ~70 MiB  |
| Process peak RSS      | ~87 MiB  |

Pay the one-time ~150 ms `Init` latency, then expect a roughly 70 MiB resident heap for the process lifetime.

## License

[MIT](LICENSE).
