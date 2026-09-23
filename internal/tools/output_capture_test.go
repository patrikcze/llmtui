package tools

import (
	"bytes"
	"runtime"
	"sync"
	"testing"
	"time"
)

// Unit tests for boundedCapture (output_capture.go) — Phase 2a of the
// next-generation tool runtime plan, §31 checklist bullet 4. These exercise
// the writer directly, with no real subprocess. run_command-level
// integration tests (failed start, timed-out child) follow below.

func TestBoundedCaptureChunkBoundary(t *testing.T) {
	// cap=10, two writes of 6 bytes each cross the cap mid-call: the first
	// write fills the remaining room (10 bytes total), the second is
	// entirely past the cap.
	c := newBoundedCapture(10)
	n1, err1 := c.Write([]byte("abcdef")) // 6 bytes, all retained
	if n1 != 6 || err1 != nil {
		t.Fatalf("first write = (%d, %v), want (6, nil)", n1, err1)
	}
	n2, err2 := c.Write([]byte("ghijkl")) // 6 bytes, only 4 fit under the cap
	if n2 != 6 || err2 != nil {
		t.Fatalf("second write = (%d, %v), want (6, nil)", n2, err2)
	}
	if got := c.Observed(); got != 12 {
		t.Fatalf("Observed() = %d, want 12 (true total across both writes)", got)
	}
	if got := c.Retained(); got != 10 {
		t.Fatalf("Retained() = %d, want 10 (the configured cap)", got)
	}
	if got := string(c.Bytes()); got != "abcdefghij" {
		t.Fatalf("Bytes() = %q, want %q", got, "abcdefghij")
	}
}

func TestBoundedCaptureHugeSingleWrite(t *testing.T) {
	// A single Write larger than the entire cap in one shot (not
	// accumulated across many small writes), the same way a subprocess can
	// hand os/exec's io.Copy one large buffered chunk.
	c := newBoundedCapture(10)
	payload := bytes.Repeat([]byte("x"), 1000)
	n, err := c.Write(payload)
	if n != len(payload) || err != nil {
		t.Fatalf("Write() = (%d, %v), want (%d, nil)", n, err, len(payload))
	}
	if got := c.Observed(); got != 1000 {
		t.Fatalf("Observed() = %d, want 1000", got)
	}
	if got := c.Retained(); got != 10 {
		t.Fatalf("Retained() = %d, want 10", got)
	}
	if got := len(c.Bytes()); got != 10 {
		t.Fatalf("len(Bytes()) = %d, want 10", got)
	}
}

// TestBoundedCaptureConcurrentWrites simulates cmd.Stdout and cmd.Stderr
// racing into the same writer instance (os/exec serializes this in
// production when Stdout == Stderr, but the writer's own mutex is defense
// in depth — see the boundedCapture doc comment). Run with `go test -race`:
// this must be race-clean and Observed() must equal the true sum of every
// byte written regardless of goroutine interleaving.
func TestBoundedCaptureConcurrentWrites(t *testing.T) {
	const goroutines = 16
	const writesPerGoroutine = 200
	const chunkSize = 17

	c := newBoundedCapture(64) // small cap: most writes are discarded, on purpose
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			chunk := bytes.Repeat([]byte{byte('A' + id%26)}, chunkSize)
			for i := 0; i < writesPerGoroutine; i++ {
				n, err := c.Write(chunk)
				if n != chunkSize || err != nil {
					t.Errorf("goroutine %d write %d: (%d, %v), want (%d, nil)", id, i, n, err, chunkSize)
				}
			}
		}(g)
	}
	wg.Wait()

	wantObserved := int64(goroutines * writesPerGoroutine * chunkSize)
	if got := c.Observed(); got != wantObserved {
		t.Fatalf("Observed() = %d, want %d (deterministic regardless of interleaving)", got, wantObserved)
	}
	if got := c.Retained(); got != 64 {
		t.Fatalf("Retained() = %d, want 64 (the cap)", got)
	}
	if len(c.Digest()) != 64 { // hex-encoded SHA-256 is 64 chars
		t.Fatalf("Digest() length = %d, want 64", len(c.Digest()))
	}
}

func TestBoundedCaptureNeverShortWritesOrErrors(t *testing.T) {
	c := newBoundedCapture(5)
	for _, size := range []int{0, 1, 5, 6, 1000} {
		n, err := c.Write(make([]byte, size))
		if n != size || err != nil {
			t.Fatalf("Write(%d bytes) = (%d, %v), want (%d, nil)", size, n, err, size)
		}
	}
}

// TestBoundedCaptureDigestDistinguishesTruncatedOutputs proves the
// full-stream digest choice: two writers with an identical retained prefix
// but different true output must not produce the same digest, or a later
// phase's repeat-detection/dedup logic would wrongly treat genuinely
// different command output as identical.
func TestBoundedCaptureDigestDistinguishesTruncatedOutputs(t *testing.T) {
	const capBytes = 8
	a := newBoundedCapture(capBytes)
	b := newBoundedCapture(capBytes)

	prefix := []byte("abcdefgh") // exactly the cap, identical for both
	if _, err := a.Write(prefix); err != nil {
		t.Fatalf("a.Write: %v", err)
	}
	if _, err := b.Write(prefix); err != nil {
		t.Fatalf("b.Write: %v", err)
	}

	// a's stream ends here; b's stream continues with more bytes past the
	// retained prefix.
	if _, err := b.Write([]byte("more-bytes-that-get-discarded")); err != nil {
		t.Fatalf("b.Write: %v", err)
	}

	if string(a.Bytes()) != string(b.Bytes()) {
		t.Fatalf("retained prefixes differ, test setup is broken: %q vs %q", a.Bytes(), b.Bytes())
	}
	if a.Digest() == b.Digest() {
		t.Fatalf("Digest() collided for genuinely different full output (identical retained prefix)")
	}
}

func TestBoundedCaptureZeroCap(t *testing.T) {
	c := newBoundedCapture(0)
	n, err := c.Write([]byte("hello"))
	if n != 5 || err != nil {
		t.Fatalf("Write() = (%d, %v), want (5, nil)", n, err)
	}
	if got := c.Retained(); got != 0 {
		t.Fatalf("Retained() = %d, want 0", got)
	}
	if got := c.Observed(); got != 5 {
		t.Fatalf("Observed() = %d, want 5", got)
	}
}

// run_command-level integration tests.

// TestRunCommandFailedStartLeavesMetaUntouched is a regression check: a
// cmd.Start() failure must behave exactly as before the bounded writer was
// introduced — the writer is constructed but never receives a byte, and the
// function returns before touching Coverage/ContentDigest at all.
func TestRunCommandFailedStartLeavesMetaUntouched(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	// A workspace root that does not exist makes cmd.Start() fail at chdir,
	// before the shell (or this package's own guardrail/classification
	// logic) ever runs.
	root := t.TempDir() + "/does-not-exist"
	r := NewRunner(root, 64)

	res := r.Execute(Call{Tool: ToolRunCommand, Body: "echo hi"})
	if res.Err == nil {
		t.Fatal("expected cmd.Start() to fail against a missing workspace root")
	}
	if res.Output != "" {
		t.Fatalf("Output = %q, want empty on a start failure", res.Output)
	}
	if res.Meta.ContentDigest != "" {
		t.Fatalf("ContentDigest = %q, want empty: the writer must never be touched on a start failure", res.Meta.ContentDigest)
	}
	zero := Coverage{}
	got := res.Meta.Coverage
	if got.SourceComplete != zero.SourceComplete || got.CaptureComplete != zero.CaptureComplete ||
		got.PreviewComplete != zero.PreviewComplete || got.ObservedBytes != zero.ObservedBytes ||
		got.RetainedBytes != zero.RetainedBytes || got.TotalBytes != nil || got.TotalLines != nil ||
		len(got.Reasons) != 0 {
		t.Fatalf("Coverage = %+v, want the zero value on a start failure", got)
	}
}

// TestRunCommandTimeoutDrainsBeyondCapWithoutBlocking confirms that a
// command producing output far beyond the retention cap, killed by a short
// configured timeout, still returns promptly — the bounded writer keeps
// draining (never blocking cmd.Wait()) past the cap, and the existing
// timeout classification is unaffected by the writer swap.
func TestRunCommandTimeoutDrainsBeyondCapWithoutBlocking(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shell test")
	}
	r := NewRunner(t.TempDir(), 8) // 8 KB cap
	r.CommandTimeout = 100 * time.Millisecond

	start := time.Now()
	// `yes` alone never terminates on its own (no `head -c` bound), so this
	// is guaranteed to still be running — and to have produced far more
	// than the 8 KB cap — when the 100ms timeout fires and kills it.
	res := r.Execute(Call{Tool: ToolRunCommand, Body: "yes"})
	elapsed := time.Since(start)

	if res.Err == nil || res.Meta.Outcome != OutcomeTimeout {
		t.Fatalf("expected OutcomeTimeout, got outcome=%v err=%v", res.Meta.Outcome, res.Err)
	}
	if res.Meta.Error == nil || res.Meta.Error.Code != "timeout" {
		t.Fatalf("expected ErrorInfo.Code=timeout, got %+v", res.Meta.Error)
	}
	// Generous bound: WaitDelay is 1s and the configured timeout is 100ms,
	// so a healthy kill path returns in low single-digit seconds even under
	// CI load. A regression that blocks cmd.Wait() on full drain would
	// instead take as long as `yes | head -c 100MiB` needs to finish.
	if elapsed > 5*time.Second {
		t.Fatalf("run_command took %s after a 100ms timeout; the writer may be blocking cmd.Wait()", elapsed)
	}
}
