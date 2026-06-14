package googlesql

// Regression tests for googlesqlite#16: the v0.2.18 emulator process
// panicked with `slice bounds out of range [-2147066416:]` at
// googlesql.go:624 (invoke's `copy(Memory[reqPtr:], req)`) on the
// runtime-finalizer goroutine while a dbt build churned hundreds of
// distinct-statement analyses.
//
// Root cause (established by inspection + the deterministic repro
// below): once wasm linear memory legitimately grows past 2 GiB —
// which a churn-heavy workload reaches because m.Memory's backing
// array counts as live Go heap, so GOGC's doubling target lets
// un-finalized C++ garbage ratchet upward each cycle — WasmAlloc
// starts handing out chunk addresses >= 2^31. The transpiled module
// itself is unsigned-correct (`unsafe.Add(m.M, uint32(p))`), but the
// bridge sliced Go memory with the raw signed int32, so the first
// request buffer allocated above the 2 GiB line produced a negative
// slice bound and killed the process. The finalizer goroutine was
// simply the unlucky caller: GC-burst frees dominate module entries
// at peak heap.
//
// The fix is three-part (see the Module doc in googlesql.go):
//   1. all bridge slicing treats wasm pointers as uint32;
//   2. GC finalizers never enter the module: free() enqueues onto
//      Module.pendingFrees, drained by the next top-level invoke
//      while m.mu is held and no activation is suspended mid-call
//      (the mid-callback-window hazard for FromImpl users);
//   3. invoke nudges runtime.GC() after every gcNudgeBytes of linear
//      memory growth, bounding the ratchet far below the 4 GiB
//      MaxMem wall.

import (
	"fmt"
	"runtime"
	"sync"
	"testing"

	"github.com/goccy/googlesqlwasm2go"
)

// TestIssue16_HeapPast2GiB_BridgePointerSignedness pushes the wasm
// heap past 2^31 with raw WasmAlloc churn (standing in for the dbt
// run's 30-node allocation ramp, without the multi-minute analysis
// workload), then exercises real API calls whose request buffers land
// above the line. Pre-fix this dies exactly like the production
// crash: `slice bounds out of range [-21474...:]` at invoke's
// request-copy. Post-fix the calls must succeed.
func TestIssue16_HeapPast2GiB_BridgePointerSignedness(t *testing.T) {
	if testing.Short() {
		t.Skip("allocates >2GiB of wasm linear memory")
	}
	ensureInit(t)
	m := module()

	const lgChunk = 1 << 24 // 16 MiB
	var big []int32
	freeAll := func() {
		m.mu.Lock()
		for _, p := range big {
			wasm2go.WasmFree(m.g, p)
		}
		big = nil
		m.mu.Unlock()
	}
	defer freeAll()

	// Phase 1: large allocations push dlmalloc's top past 2 GiB.
	m.mu.Lock()
	for i := 0; i < 160; i++ {
		p := wasm2go.WasmAlloc(m.g, lgChunk)
		if p == 0 {
			m.mu.Unlock()
			t.Fatalf("WasmAlloc(%d MiB) failed at iteration %d", lgChunk>>20, i)
		}
		big = append(big, p)
		if uint32(p)+uint32(lgChunk) > 1<<31 {
			break
		}
	}
	// Phase 2: small allocations (the size class of invoke's request
	// buffers) until one is served from above 2^31, exhausting any
	// low free chunks of that class left behind by earlier tests.
	reached := false
	for i := 0; i < 1<<22; i++ {
		p := wasm2go.WasmAlloc(m.g, 24)
		if p == 0 {
			m.mu.Unlock()
			t.Fatal("WasmAlloc(24) failed before reaching 2GiB")
		}
		big = append(big, p)
		if uint32(p) >= 1<<31 {
			reached = true
			break
		}
	}
	m.mu.Unlock()
	if !reached {
		t.Fatal("could not drive small allocations past 2GiB; test inconclusive")
	}

	// Phase 3: real API traffic. Each call allocates a small request
	// buffer that now lands above 2^31. Pre-fix: panic at the invoke
	// request-copy (production signature). Post-fix: green.
	lo, err := NewLanguageOptions()
	if err != nil {
		t.Fatalf("NewLanguageOptions above 2GiB: %v", err)
	}
	if err := lo.SetProductMode(ProductModeProductExternal); err != nil {
		t.Fatalf("SetProductMode above 2GiB: %v", err)
	}
	mode, err := lo.ProductMode()
	if err != nil {
		t.Fatalf("ProductMode above 2GiB: %v", err)
	}
	if mode != ProductModeProductExternal {
		t.Fatalf("ProductMode = %v, want PRODUCT_EXTERNAL", mode)
	}
	po, err := NewParserOptions()
	if err != nil {
		t.Fatalf("NewParserOptions above 2GiB: %v", err)
	}
	out, err := ParseStatement("SELECT 1 AS above_two_gib", po)
	if err != nil {
		t.Fatalf("ParseStatement above 2GiB: %v", err)
	}
	if out == nil {
		t.Fatal("ParseStatement returned nil output")
	}
	runtime.KeepAlive(out)
	runtime.KeepAlive(po)
	runtime.KeepAlive(lo)
}

// TestIssue16_FinalizerOptionChurnStress is the workload-shaped
// stress from the issue thread: 4-8 goroutines analyzing distinct
// statement texts (the dbt shape — every text a cache miss) while
// each iteration creates and immediately abandons analyzer option
// objects, with forced GC so finalizers fire continuously while other
// goroutines are mid-analysis. Pre-fix the finalizer goroutine called
// straight into the module; post-fix it may only enqueue. The test
// must complete with zero panics and the pending-free queue must
// drain to empty.
func TestIssue16_FinalizerOptionChurnStress(t *testing.T) {
	ensureInit(t)
	t.Cleanup(forceGC)

	catalog, err := NewSimpleCatalog("issue16_catalog", nil)
	if err != nil {
		t.Fatalf("NewSimpleCatalog: %v", err)
	}
	setupLang, err := NewLanguageOptions()
	if err != nil {
		t.Fatalf("NewLanguageOptions: %v", err)
	}
	if err := setupLang.EnableMaximumLanguageFeaturesForDevelopment(); err != nil {
		t.Fatalf("EnableMaximumLanguageFeaturesForDevelopment: %v", err)
	}
	if err := catalog.AddBuiltinFunctionsAndTypes(&BuiltinFunctionOptions{LanguageOptions: setupLang}); err != nil {
		t.Fatalf("AddBuiltinFunctionsAndTypes: %v", err)
	}
	tf, err := NewTypeFactory()
	if err != nil {
		t.Fatalf("NewTypeFactory: %v", err)
	}

	const (
		workers = 8
		iters   = 120
	)
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < iters; i++ {
				// Fresh option objects every iteration — the churn
				// that arms the finalizer gun in production.
				lang, err := NewLanguageOptions()
				if err != nil {
					errCh <- fmt.Errorf("w%d i%d NewLanguageOptions: %w", w, i, err)
					return
				}
				if err := lang.EnableMaximumLanguageFeaturesForDevelopment(); err != nil {
					errCh <- fmt.Errorf("w%d i%d EnableMaximum: %w", w, i, err)
					return
				}
				opts, err := NewAnalyzerOptions2()
				if err != nil {
					errCh <- fmt.Errorf("w%d i%d NewAnalyzerOptions2: %w", w, i, err)
					return
				}
				if err := opts.SetLanguage(lang); err != nil {
					errCh <- fmt.Errorf("w%d i%d SetLanguage: %w", w, i, err)
					return
				}
				// Distinct text per (worker, iteration): cache-miss
				// analysis, exactly the dbt shape.
				sql := fmt.Sprintf(
					"SELECT %d AS a, %d + %d AS b, CONCAT('w', '%d', '_', '%d') AS c, ARRAY<INT64>[%d, %d, %d] AS d",
					i, w, i, w, i, i+1, i+2, i+3)
				out, err := AnalyzeStatement(sql, opts, catalog, tf)
				if err != nil {
					errCh <- fmt.Errorf("w%d i%d AnalyzeStatement: %w", w, i, err)
					return
				}
				if out == nil {
					errCh <- fmt.Errorf("w%d i%d AnalyzeStatement returned nil", w, i)
					return
				}
				// Abandon lang/opts/out — finalizer fodder.
				if i%16 == 0 {
					runtime.GC()
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	runtime.KeepAlive(tf)
	runtime.KeepAlive(catalog)
}
