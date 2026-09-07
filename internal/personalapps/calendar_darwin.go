//go:build darwin

package personalapps

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/patrikcze/llmtui/internal/procutil"
)

const defaultCalendarBridgeTimeout = 15 * time.Second

// CalendarBackendOptions identifies the separately installed, signed EventKit
// companion. The companion is intentionally not compiled or downloaded at
// runtime. An empty path leaves Calendar unsupported rather than searching
// PATH or guessing a location.
type CalendarBackendOptions struct {
	HelperPath string
	Timeout    time.Duration
}

// NewCalendarBackend returns a read-only EventKit CalendarBackend when an
// explicit companion path is configured. Constructing it performs no I/O and
// starts no process; the helper is launched only for an authorized calendar
// operation after the person has explicitly connected Calendar.
func NewCalendarBackend(opts CalendarBackendOptions) CalendarBackend {
	if opts.HelperPath == "" || !filepath.IsAbs(opts.HelperPath) {
		return nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultCalendarBridgeTimeout
	}
	return newEventKitCalendarBackend(&eventKitRunner{path: opts.HelperPath, timeout: timeout})
}

// NewCalendarMutator returns a Mutator that applies Calendar changes
// (create_event, update_event) through the same signed EventKit companion
// NewCalendarBackend uses, over its own eventKitRunner. Constructing it
// performs no I/O and starts no process; the helper launches only for an
// authorized calendar operation after Calendar has been explicitly
// connected. Deliberately independent of NewCalendarBackend so a deployment
// can wire reads without ever wiring the ability to write.
func NewCalendarMutator(opts CalendarBackendOptions) Mutator {
	if opts.HelperPath == "" || !filepath.IsAbs(opts.HelperPath) {
		return nil
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = defaultCalendarBridgeTimeout
	}
	return newEventKitCalendarMutator(newEventKitCalendarBackend(&eventKitRunner{path: opts.HelperPath, timeout: timeout}))
}

// eventKitRunner invokes only the configured absolute companion executable.
// It uses a length-framed stdin/stdout protocol, never a shell, arguments
// derived from model input, a socket, or environment payloads.
type eventKitRunner struct {
	path    string
	timeout time.Duration
}

func (r *eventKitRunner) run(parent context.Context, payload []byte) ([]byte, error) {
	frame, err := frameCalendarBridgePayload(payload)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(parent, r.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, r.path)
	cmd.Stdin = bytes.NewReader(frame)
	procutil.SetupProcAttr(cmd)
	cmd.WaitDelay = time.Second
	stdout := &boundedBuffer{max: maxCalendarBridgeFrameBytes + 4, onOverflow: cancel}
	stderr := &boundedBuffer{max: maxBridgeStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start calendar helper: %w", err)
	}
	if err := procutil.TrackProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, fmt.Errorf("contain calendar helper process tree: %w", err)
	}
	waitErr := cmd.Wait()
	procutil.KillGroup(cmd)
	if stdout.hasOverflowed() {
		return nil, errCalendarBridgeOutputTooLarge
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil {
		return nil, fmt.Errorf("calendar helper exited with an error (stderr: %s): %w", stderr.summary(), waitErr)
	}
	response, err := unframeCalendarBridgePayload(stdout.bytes())
	if err != nil {
		return nil, fmt.Errorf("decode calendar helper response (stderr: %s): %w", stderr.summary(), err)
	}
	return response, nil
}
