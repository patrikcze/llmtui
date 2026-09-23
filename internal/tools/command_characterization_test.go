package tools

import (
	"fmt"
	"runtime"
	"strings"
	"testing"
)

// This file characterized CURRENT run_command output-capture behavior as of
// Phase 0 of the next-generation-tool-runtime plan (evidence row L6): output
// was accumulated in an unbounded bytes.Buffer (runCommandContext, tools.go)
// before any cap was applied. Phase 2a (this phase, output_capture.go)
// closed that gap by replacing the buffer with boundedCapture, which never
// retains more than the runner's configured cap regardless of how much a
// command produces. This file now characterizes the CLOSED behavior; the
// unit tests for the writer itself (chunk boundaries, huge single writes,
// concurrent -race safety) live in output_capture_test.go.
//
// The unit test below deliberately stays in the low single-digit MiB range —
// large enough to demonstrate a command producing much more than the cap
// still comes back correctly truncated, small enough that `go test ./...`
// never risks CI memory. BenchmarkCommandCapture (only executed with `go
// test -bench`) measures the same path at the larger sizes the plan calls
// for (64 KiB / 4 MiB / 256 MiB) and reports retained-memory growth (or, now,
// the lack of it) as a number.

// TestRunCommandOutputCapturedBoundedByRetentionCap documents that a command
// producing several MiB of output completes normally and comes back
// truncated to the runner's cap — and that the capture path reports this
// through Result.Meta.Coverage: ObservedBytes reflects everything the
// command actually produced, RetainedBytes/CaptureComplete reflect what the
// bounded writer kept, and the two disagree exactly because retention was
// capped during capture, not because anything was lost after the fact.
func TestRunCommandOutputCapturedBoundedByRetentionCap(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	root := t.TempDir()
	r := NewRunner(root, 8)  // 8 KB retention cap
	const produced = 8 << 20 // 8 MiB: many multiples of the 8 KB cap, still a
	// safe allocation for a unit test run under plain `go test ./...`.

	res := r.Execute(Call{Tool: ToolRunCommand, Body: fmt.Sprintf("yes | head -c %d", produced)})
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if !strings.Contains(res.Output, "truncated") {
		t.Fatalf("expected the 8 MiB output to be truncated to the runner cap, got %d bytes", len(res.Output))
	}
	if len(res.Output) > 8*1024+64 {
		t.Fatalf("final result exceeds the runner cap: %d bytes", len(res.Output))
	}
	// This is the Phase 2a closure: the writer's own counters, not just the
	// final string, prove retention was bounded during capture.
	if res.Meta.Coverage.ObservedBytes != produced {
		t.Fatalf("ObservedBytes = %d, want the true produced total %d", res.Meta.Coverage.ObservedBytes, produced)
	}
	if res.Meta.Coverage.RetainedBytes > 8*1024+64 {
		t.Fatalf("RetainedBytes exceeds the runner cap: %d", res.Meta.Coverage.RetainedBytes)
	}
	if res.Meta.Coverage.CaptureComplete {
		t.Fatalf("CaptureComplete = true for output beyond the cap, want false")
	}
	if res.Meta.ContentDigest == "" {
		t.Fatalf("ContentDigest is empty, want the full-stream SHA-256 hex digest")
	}
}

// BenchmarkCommandCapture measures the CURRENT (Phase 2a) bounded-capture
// path retaining 64 KiB / 4 MiB / 256 MiB of command output at a realistic
// fixed 64 KiB cap, so the bounded-memory behavior this phase adds is a
// number, not just a code-reading claim: -benchmem's B/op for the 256 MiB
// case should be O(cap), not O(produced bytes) — Phase 0's baseline
// established the latter as the problem. Only runs with `go test -bench`,
// never under plain `go test ./...`.
func BenchmarkCommandCapture(b *testing.B) {
	if runtime.GOOS == "windows" {
		b.Skip("unix shell benchmark")
	}
	const capKB = 64 // 64 KiB: a realistic run_command retention cap, held
	// fixed across every produced size below so the 4 MiB/256 MiB cases
	// exercise real truncation, not an oversized cap that never engages.
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
			r := NewRunner(root, capKB)
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
