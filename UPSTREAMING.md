# Upstreaming notes: bridge memory-safety fixes

This fork carries correctness fixes to the generated GoogleSQL bridge
(`googlesql.go`). They are **not** specific to any downstream use. They fix the
shared wasm2go bridge and reproduce on upstream `goccy/go-googlesql`, which
consumes the identical `googlesqlwasm2go` module. The fixes currently live as
edits to the "DO NOT EDIT" generated file. To upstream them cleanly they should
be emitted by the generator (`protoc-gen-wasmify-go` / the wasm2go templates)
so they survive regeneration.

Commits: `26ec027` (fixes 1-3), `3bdff21` (fix 4).

The diff splits into three parts. The two logic rows are the whole review
surface. The mechanical row is a single substitution repeated across every
generated `free()` body.

| Part            | Size           | What it is                                                                                                  |
|-----------------|----------------|-------------------------------------------------------------------------------------------------------------|
| Mechanical      | ~1441 hunks    | One substitution applied to every generated `free()` body (`enqueueFree` for the synchronous destructor call) |
| Reviewable      | ~7 hunks       | The invoke request copy, the two `handleCallback` copies, `resolveTypeName`, the `pendingFrees` queue and its drain, and the GC nudge |
| TVF keep-alive  | 8 lines (`3bdff21`) | The `setKeepAlive` wiring in the TVF constructors                                                          |

## Fix 1 - linear memory past 2GiB crashes the bridge (highest priority)

- **Symptom:** `panic: slice bounds out of range [-2147066416:]` at
  `googlesql.go:624` on the runtime-finalizer goroutine under distinct-statement
  churn.
- **Root cause:** wasm pointers are unsigned and linear memory legitimately
  grows past 2GiB (MaxMem is 4GiB). The transpiled module is unsigned-correct,
  but the bridge sliced Go linear memory with the raw signed `int32` pointer. The
  first request buffer `WasmAlloc`'d above 2^31 produces a negative slice bound.
- **Fix:** every linear-memory slice in the bridge converts the pointer through
  `uint32` before slicing (the invoke request copy, both `handleCallback`
  request/response copies, and `resolveTypeName`).
- **Deterministic repro:** `TestIssue16_HeapPast2GiB_BridgePointerSignedness`
  reproduces the exact production signature pre-fix in about 0.2s. A dedicated CI
  job runs it (it is `testing.Short()`-gated, so the main `-short` job skips it).
- **Generator change:** emit `uint32(ptr)` conversion at every
  `Memory(...)[ptr:...]` slice site in the bridge template.
- **Reproduces upstream:** the raw-`int32` slices on the `int32`-signature
  `handleCallback` path in current `goccy/go-googlesql` (around
  `googlesql.go:542`, `:564`, `:624`) crash the same way, with no regression test
  guarding them.

## Fix 2 - GC finalizers must never enter the transpiled module

- **Symptom:** re-entrancy hazard. Upstream calls the C++ destructor
  synchronously on the GC finalizer goroutine via `module().invoke(...)`, which
  can block behind a long analysis and can re-enter the module while a C++
  activation is suspended in a `handleCallback` mu-release window.
- **Fix:** all generated `free()` bodies enqueue onto `Module.pendingFrees`
  under a separate `freeMu` (a finalizer never blocks behind analysis), drained
  by the next top-level `invoke` (depth 1, `mu` held, no activation suspended).
  The number of `SetFinalizer` registrations is unchanged, so reclamation
  semantics are identical. Only *when* frees run changes.
- **Generator change:** emit `enqueueFree(...)` instead of
  `module().invoke(...)` in `free()` bodies.

## Fix 3 - bound the heap growth (the "ratchet")

- **Symptom:** un-finalized C++ garbage ratchets the Go heap toward the 4GiB
  MaxMem wall. The `Memory` backing array counts as live Go heap, so GOGC's
  doubling target lets it climb each cycle.
- **Fix:** `invoke` forces `runtime.GC()` every 256MiB of observed
  linear-memory growth, bounding the ratchet far below the wall.
- **Note:** this is a heuristic and a tradeoff (a stop-the-world GC on the hot
  path) bought for ratchet containment in a long-lived process. It is a
  self-contained follow-up, independent of Fix 1.
- **Stress:** `TestIssue16_FinalizerOptionChurnStress` (8 goroutines, each
  running 120 distinct-text analyses, with forced GC) passes.

## Fix 4 - keep TVF wrappers alive through base handles (`3bdff21`)

- **Symptom:** use-after-free. A base handle can be GC-freed while a typed
  `TableValuedFunction` wrapper still references it.
- **Fix:** insert `h.TableValuedFunction.setKeepAlive(h)` in the TVF
  constructors (8 insertions across the subclass types and their NoFinalizer
  variants). `setKeepAlive` already exists upstream structurally, so this fix
  only wires up existing lifetime machinery.
- **Guard:** `binding_e2e_test.go` forces GC on the extract-base path.

## Regenerating the bridge

Until the fixes above are emitted by the generator, `googlesql.go` is the
upstream wasm2go bridge with the patch set re-applied on top. The engine itself
is consumed via `github.com/goccy/googlesqlwasm2go`, and `googlesql.go` is only
the generated Go bridge around it.

### Layout

Two different "upstream" baselines are in play and they are not the same ref:
the release tag `goccy/googlesql-wasm@v0.3.1` is the attestation baseline (the
signed artifact `verify-upstream` checks), while the `upstream/main` git ref of
`goccy/go-googlesql` is the patch baseline (what `patches/` and `verify-patch`
diff against).

- The canonical upstream bridge is the `googlesql_wasm2go.go` asset from the
  `goccy/googlesql-wasm` release (currently `v0.3.1`), fetched by
  `make download-upstream` into `.upstream/googlesql.go` and attestation-verified
  by `make verify-upstream`.
- `patches/` holds the downstream edits as unified diffs against the upstream
  baseline (the `upstream/main` git ref the patches were generated against):
  - `0001-bridge-uint32-signedness-and-pendingFrees.patch`
  - `0002-tvf-setKeepAlive.patch`
- `make verify-patch` proves `googlesql.go` equals baseline plus patches, byte
  for byte. It needs the `upstream` git remote:
  `git remote add upstream https://github.com/goccy/go-googlesql && git fetch upstream`.

### Bumping to a new upstream bridge version

1. The patches are already extracted in `patches/`, so start from there.
2. Bump `GOOGLESQL_WASM_VERSION` in the `Makefile`, then `make googlesql`
   (downloads and attestation-verifies the new upstream bridge into `.upstream/`).
3. Apply the patches onto the new baseline. The patches target stable, named
   functions, so `git apply --recount` usually absorbs small line drift:
   ```sh
   cp .upstream/googlesql.go googlesql.go
   git apply --recount patches/0001-*.patch patches/0002-*.patch
   ```
   Resolve any rejects by hand against the named functions in the fix sections
   above.
4. Regenerate the patches against the new baseline (so `patches/` stays a clean
   diff vs the new upstream), then run `make verify-patch`.
5. Run the test suite, including the non-short large-memory regression that the
   main `-short` CI job skips:
   ```sh
   go test -run TestIssue16_HeapPast2GiB_BridgePointerSignedness .
   ```

## How to present this upstream

Lead with Fix 1: an unconditional correctness bug with a deterministic repro
that reproduces on upstream's current bridge with no regression test guarding
it. Treat Fixes 2-4 as a labeled follow-up. The ask is to emit these from the
generator so they regenerate cleanly and stay out of the hand-edited generated
file.
