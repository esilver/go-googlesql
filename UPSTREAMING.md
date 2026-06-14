# Upstreaming notes: bridge memory-safety fixes

This fork carries correctness fixes to the generated GoogleSQL bridge
(`googlesql.go`). They are **not** specific to any downstream use. They fix the
shared wasm2go bridge and reproduce on upstream `goccy/go-googlesql`, which
consumes the identical `googlesqlwasm2go` module. The fixes currently live as
edits to the "DO NOT EDIT" generated file. To upstream them cleanly they should
be emitted by the generator (`protoc-gen-wasmify-go` / the wasm2go templates)
so they survive regeneration.

Commits: `26ec027` (fixes 1-3), `3bdff21` (fix 4).

The bulk of `26ec027`'s diff (~1441 hunks) is one mechanical substitution
applied to every generated `free()` body. The genuinely reviewable logic is
about 7 hunks in `googlesql.go` (the invoke request copy, the two
`handleCallback` copies, `resolveTypeName`, the `pendingFrees` queue and its
drain, and the GC nudge) plus the 8 lines in `3bdff21`. Those ~15 hunks are the
whole review surface, and the remaining ~3,000 lines are the mechanical
`free()`-body substitution.

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

## How to present this upstream

Lead with Fix 1: an unconditional correctness bug with a deterministic repro
that reproduces on upstream's current bridge with no regression test guarding
it. Treat Fixes 2-4 as a labeled follow-up. The ask is to emit these from the
generator so they regenerate cleanly and stay out of the hand-edited generated
file.
