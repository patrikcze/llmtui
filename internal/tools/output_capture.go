package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
	"sync"
)

// This file is Phase 2a of the next-generation tool runtime plan
// (.claude/tasks/plans/next-generation-tool-runtime.md §14/§29/§31, evidence
// row L6). It replaces run_command's unbounded bytes.Buffer capture
// (runCommandContext, tools.go) with boundedCapture: an io.Writer that never
// retains more than its configured cap regardless of how much output a
// command produces, while still draining everything the command writes so
// the command itself is never blocked or errored by this writer being
// "full." Nothing about run_command's schema, timeout, cancellation,
// classification, guardrails, or truncation marker text changes — only how
// bytes are held in memory while the command runs.

// boundedCapture is an io.Writer that retains at most capBytes of the total
// bytes written to it. It always reports a full, short-write-free
// (len(p), nil) to its caller — a command's own stdout/stderr pipe must
// never see backpressure or an error because this writer decided it was
// full — and silently discards everything past the cap after hashing it.
//
// cmd.Stdout and cmd.Stderr are set to the SAME *boundedCapture instance in
// runCommandContext (unchanged from the previous bytes.Buffer wiring), so
// os/exec's own documented behavior ("If Stdout and Stderr are the same
// writer, and have a type that can be compared with ==, at most one
// goroutine at a time will call Write") already serializes concurrent
// stdout/stderr copies into it. mu is defense in depth on top of that
// stdlib guarantee, not a substitute for passing the identical writer to
// both fields — see TestBoundedCaptureConcurrentWrites for a -race-clean
// proof that concurrent Write calls (as if stdout and stderr were racing
// through two independently-scheduled goroutines) still produce a correct,
// deterministic Observed() total.
type boundedCapture struct {
	capBytes int

	mu       sync.Mutex
	buf      []byte
	observed int64
	digest   hash.Hash
}

// newBoundedCapture returns a writer that retains at most capBytes bytes. A
// non-positive capBytes retains nothing (every byte is hashed then
// discarded) but still tracks Observed() correctly.
func newBoundedCapture(capBytes int) *boundedCapture {
	if capBytes < 0 {
		capBytes = 0
	}
	return &boundedCapture{
		capBytes: capBytes,
		// Pre-allocated at the cap, never grown past it — this is the fix for
		// the unbounded bytes.Buffer growth Phase 0 characterized.
		buf:    make([]byte, 0, capBytes),
		digest: sha256.New(),
	}
}

// Write implements io.Writer. It never returns a short write or a non-nil
// error: every byte is accounted for in Observed() and folded into the
// full-stream digest, and only the portion that still fits under capBytes is
// copied into the retained buffer.
func (b *boundedCapture) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.observed += int64(len(p))
	// Hash every byte written, retained or not: two results whose retained
	// prefixes are identical but whose true full output differs (e.g. one
	// run produced 10 MiB of the same repeating prefix and another produced
	// 200 MiB of it) must not carry the same ContentDigest, or a later
	// phase's repeat-detection/dedup logic would wrongly treat genuinely
	// different command output as identical.
	b.digest.Write(p)
	if room := b.capBytes - len(b.buf); room > 0 {
		if room > len(p) {
			room = len(p)
		}
		b.buf = append(b.buf, p[:room]...)
	}
	return len(p), nil
}

// Observed returns the total number of bytes ever written to this writer,
// retained or discarded.
func (b *boundedCapture) Observed() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.observed
}

// Retained returns the number of bytes actually held in the writer's
// buffer; always <= capBytes and <= Observed().
func (b *boundedCapture) Retained() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return int64(len(b.buf))
}

// Bytes returns a copy of the retained bytes. Callers should only read this
// after the writer has stopped receiving concurrent writes (i.e. after
// cmd.Wait() has returned) for a stable snapshot; the copy itself is always
// race-safe to take.
func (b *boundedCapture) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]byte, len(b.buf))
	copy(out, b.buf)
	return out
}

// Digest returns the lowercase hex SHA-256 digest of every byte ever
// written to this writer (retained or discarded past the cap).
func (b *boundedCapture) Digest() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return hex.EncodeToString(b.digest.Sum(nil))
}
