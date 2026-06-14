package googlesql_test

import (
	"fmt"
	"os"
	"runtime"
	"testing"

	"github.com/goccy/go-googlesql"
)

// TestMain initialises the transpiled engine once before any test runs.
// The engine is ahead-of-time transpiled pure-Go via googlesqlwasm2go
// (pulled in via go.mod), so Init takes no arguments and there is no
// Close to call.
func TestMain(m *testing.M) {
	if err := googlesql.Init(); err != nil {
		fmt.Fprintf(os.Stderr, "wasm init failed: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// forceGC forces garbage collection so wasm-side handles released via
// runtime.SetFinalizer are freed between tests sharing the module.
// Duplicated as googlesql.forceGC because internal-package tests cannot reach this external-package (googlesql_test) copy.
func forceGC() {
	runtime.GC()
	runtime.GC()
}
