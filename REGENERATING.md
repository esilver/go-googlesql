# Regenerating the bridge

This fork's `googlesql.go` is the upstream wasm2go bridge plus a small set of
correctness patches (see [UPSTREAMING.md](UPSTREAMING.md)). The engine itself is
consumed via `github.com/goccy/googlesqlwasm2go`; `googlesql.go` is the generated
Go bridge around it.

## Layout

- The canonical upstream bridge is the `googlesql_wasm2go.go` asset from the
  `goccy/googlesql-wasm` release (currently `v0.3.1`), fetched by
  `make download-upstream` into `.upstream/googlesql.go` and attestation-verified
  by `make verify-upstream`.
- `patches/` holds the downstream edits as unified diffs against the upstream
  baseline (the `upstream/main` git ref the patches were generated against):
  - `0001-bridge-uint32-signedness-and-pendingFrees.patch`
  - `0002-tvf-setKeepAlive.patch`
- `make verify-patch` proves `googlesql.go` equals baseline + patches, byte for
  byte. It needs the `upstream` git remote:
  `git remote add upstream https://github.com/goccy/go-googlesql && git fetch upstream`.

## Bumping to a new upstream bridge version

1. The patches are already extracted in `patches/`, so start from there.
2. Bump `GOOGLESQL_WASM_VERSION` in the `Makefile`, then `make googlesql`
   (downloads and attestation-verifies the new upstream bridge into `.upstream/`).
3. Apply the patches onto the new baseline. The patches target stable, named
   functions, so `git apply --recount` usually absorbs small line drift:
   ```sh
   cp .upstream/googlesql.go googlesql.go
   git apply --recount patches/0001-*.patch patches/0002-*.patch
   ```
   Resolve any rejects by hand against the named functions in UPSTREAMING.md.
4. Regenerate the patches against the new baseline (so `patches/` stays a clean
   diff vs the new upstream), then run `make verify-patch`.
5. Run the test suite, including the non-short large-memory regression that the
   main `-short` CI job skips:
   ```sh
   go test -run TestIssue16_HeapPast2GiB_BridgePointerSignedness .
   ```

## The durable fix

Re-applying patches on every bump is the interim state. The real fix is to move
these changes into the generator (`protoc-gen-wasmify-go` / the wasm2go
templates) so they are emitted rather than re-applied. UPSTREAMING.md gives the
specific generator change for each fix.
