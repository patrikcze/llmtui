package tools

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// This file characterizes CURRENT run_command output-capture behavior (Phase
// 0 of the next-generation-tool-runtime plan, evidence row L6): output is
// accumulated in an unbounded bytes.Buffer (runCommandContext, tools.go)
// before any cap is applied. It adds new tests beside tools_test.go without
// changing any existing assertion there.
//
// The unit test below deliberately stays in the low single-digit MiB range —
// large enough to demonstrate the unbounded-buffer path runs to completion
// before truncation, small enough that `go test ./...` never risks CI
// memory. BenchmarkCommandCapture (only executed with `go test -bench`)
// measures the same path at the larger sizes the plan calls for (64 KiB / 4
// MiB / 256 MiB) and reports the growth as a number.

// TestRunCommandOutputAccumulatesUnboundedBeforeTruncation documents that a
// command producing several MiB of output completes normally and is
// truncated only at the very end, evidence that the full output passed
// through memory first.
func TestRunCommandOutputAccumulatesUnboundedBeforeTruncation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	root := t.TempDir()
	r := NewRunner(root, 8)  // 8 KB truncation cap
	const produced = 8 << 20 // 8 MiB: many multiples of the 8 KB cap, still a
	// safe allocation for a unit test run under plain `go test ./...`.

	res := r.Execute(Call{Tool: ToolRunCommand, Body: fmt.Sprintf("yes | head -c %d", produced)})
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if !strings.Contains(res.Output, "truncated") {
		t.Fatalf("expected the 8 MiB output to be truncated to the runner cap, got %d bytes", len(res.Output))
	}
	// PHASE 0: current gap, closed in Phase 2a (§L6). This test can only
	// observe the outcome (a successful, correctly truncated result); it
	// cannot observe the intervening unbounded allocation from inside a unit
	// test. See BenchmarkCommandCapture for the measured number.
	if len(res.Output) > 8*1024+64 {
		t.Fatalf("final result exceeds the runner cap: %d bytes", len(res.Output))
	}
}

// BenchmarkCommandCapture measures the current runCommandContext code path
// capturing 64 KiB / 4 MiB / 256 MiB of command output into its unbounded
// bytes.Buffer, so the present unbounded-memory behavior is a number, not
// just a code-reading claim. It characterizes BASELINE behavior only; it
// does not add any bounded-capture implementation. Only runs with
// `go test -bench`, never under plain `go test ./...`.
func BenchmarkCommandCapture(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("unix shell benchmark")
	}
	sizes := []struct {
		name  string
		bytes int
	}{
		{"64KiB", 64 << 10},
		{"4MiB", 4 << 20},
		{"256MiB", 256 << 20},
	}
	for _, sz := range sizes {
		b.Run(sz.name, func(b *testing.B) {
			root := b.TempDir()
			// A cap far above every produced size: measure the pre-cap
			// buffer growth, not the truncation path.
			r := NewRunner(root, 999_999)
			body := fmt.Sprintf("yes | head -c %d", sz.bytes)

			b.ReportAllocs()
			b.ResetTimer()
			var captured int
			for i := 0; i < b.N; i++ {
				out, err := r.runCommand(body)
				if err != nil {
					b.Fatal(err)
				}
				captured = len(out)
			}
			b.ReportMetric(float64(captured), "captured_bytes")
		})
	}
}
