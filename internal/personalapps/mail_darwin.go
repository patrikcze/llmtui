package personalapps

import (
	"bytes"
	"context"
	_ "embed"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/patrikcze/llmtui/internal/procutil"
)

// mailBridgeScript is the fixed, reviewed JXA program described in the
// architecture doc's "Mail JXA transport" section. It is embedded verbatim
// at build time and never modified, templated or concatenated with request
// data at runtime — the only variable input reaching osascript is the
// bounded JSON this file writes to the child process's stdin.
//
//go:embed scripts/mail_bridge.js
var mailBridgeScript string

const (
	osascriptPath = "/usr/bin/osascript"
	// maxBridgeOutputBytes is defense-in-depth, independent of the
	// per-message body caps the script itself enforces: a script defect
	// must not turn one call into an unbounded read.
	maxBridgeOutputBytes = 8 * 1024 * 1024
	maxBridgeStderrBytes = 64 * 1024
	defaultBridgeTimeout = 15 * time.Second
)

// MailBackendOptions configures the real Mail adapter.
type MailBackendOptions struct {
	// Timeout bounds one bridge round trip end to end. Zero takes the
	// package default. The composition root maps Limits.ReadTimeout onto
	// this — see internal/tui's personalapps wiring.
	Timeout time.Duration
}

// NewMailBackend returns a MailBackend that talks to Apple Mail through the
// embedded JXA bridge script. Constructing it performs no I/O, launches no
// process and requests no permission — the first real call does, and only
// after a human has explicitly connected the adapter (Service.Connect).
func NewMailBackend(opts MailBackendOptions) MailBackend {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultBridgeTimeout
	}
	return newJXAMailBackend(&osascriptRunner{timeout: timeout, script: mailBridgeScript})
}

// osascriptRunner invokes a fixed script through the absolute system
// interpreter without a shell, exactly as the architecture specifies:
// script source in argv carries no request data, containment and
// cancellation reuse the same procutil primitives run_command already
// relies on, and output is bounded while it is being read rather than
// collected unbounded and truncated afterward.
//
// script is a field rather than always reading the package-level embed
// directly so tests can exercise this exact transport (timeouts, bounded
// output, process containment) against a small deterministic script,
// without invoking Mail.app or requiring a GUI permission prompt.
type osascriptRunner struct {
	timeout time.Duration
	script  string
}

func (r *osascriptRunner) run(parent context.Context, request []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, osascriptPath, "-l", "JavaScript", "-e", r.script)
	cmd.Stdin = bytes.NewReader(request)
	procutil.SetupProcAttr(cmd)
	// A descendant retaining stdout/stderr must not keep Wait blocked
	// indefinitely after the context kills the direct interpreter process.
	cmd.WaitDelay = time.Second

	stdout := &boundedBuffer{max: maxBridgeOutputBytes, onOverflow: cancel}
	stderr := &boundedBuffer{max: maxBridgeStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start mail bridge: %w", err)
	}
	if err := procutil.TrackProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("contain mail bridge process tree: %w", err)
	}

	waitErr := cmd.Wait()
	// Killing the interpreter does not kill Mail or roll back an Apple
	// Event; a caller mid-mutation would need to treat this as
	// OutcomeUnknown. Every operation this file supports today is
	// read-only, so that concern does not yet apply here — it will when a
	// mutation bridge is added.
	procutil.KillGroup(cmd)

	if stdout.hasOverflowed() {
		return nil, errBridgeOutputTooLarge
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil {
		return nil, fmt.Errorf("mail bridge exited with an error (stderr: %s): %w", stderr.summary(), waitErr)
	}
	return stdout.bytes(), nil
}

// boundedBuffer caps how much a subprocess may write before its output is
// rejected outright and, for stdout, before the process is canceled. It
// never silently truncates and continues: an oversized response is a hard
// error, never a best-effort partial read.
type boundedBuffer struct {
	mu         sync.Mutex
	buf        bytes.Buffer
	max        int
	overflowed bool
	// onOverflow is called at most once, the first time this buffer
	// exceeds max. osascriptRunner wires it to the request's cancel func so
	// exec's context machinery kills the process rather than leaving it
	// running attached to a buffer that has stopped accepting output.
	onOverflow func()
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	firstOverflow := false
	if !b.overflowed && b.buf.Len()+len(p) > b.max {
		b.overflowed = true
		firstOverflow = true
	}
	if b.overflowed {
		b.mu.Unlock()
		if firstOverflow && b.onOverflow != nil {
			b.onOverflow()
		}
		// Report success to the writer (the child process) so it does not
		// see a write error and retry; the process is being canceled and
		// its output is already known to be discarded.
		return len(p), nil
	}
	n, err := b.buf.Write(p)
	b.mu.Unlock()
	return n, err
}

func (b *boundedBuffer) hasOverflowed() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.overflowed
}

func (b *boundedBuffer) bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...)
}

func (b *boundedBuffer) summary() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	s := b.buf.String()
	if len(s) > 500 {
		s = s[:500] + "…"
	}
	return s
}
