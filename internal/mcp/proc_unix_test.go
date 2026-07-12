//go:build !windows

package mcp

import (
	"bufio"
	"context"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// alive reports whether pid still exists, using signal 0 (no-op signal used
// purely to probe existence/permission per kill(2)).
func alive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil
}

// TestTerminateProcessReapsGrandchild covers Bug 3: killing only the direct
// child (the old behavior) orphans grandchildren spawned by wrapper commands
// like npx/uvx/sh -c. This starts a shell that backgrounds a sleep (a
// grandchild relative to the test, and a child of the shell we launch), then
// calls terminateProcess and verifies the grandchild is gone too, not just
// the shell.
func TestTerminateProcessReapsGrandchild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	cmd := exec.Command(sh, "-c", "sleep 30 & echo $!; wait")
	setupProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Read the backgrounded sleep's PID, printed by echo $! before wait
	// blocks the shell.
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("read grandchild pid: %v", err)
	}
	pidStr := strings.TrimSpace(line)
	grandchildPID, err := strconv.Atoi(pidStr)
	if err != nil {
		_ = cmd.Process.Kill()
		t.Fatalf("parse grandchild pid %q: %v", pidStr, err)
	}

	if !alive(grandchildPID) {
		t.Fatalf("grandchild pid %d not observed alive before termination", grandchildPID)
	}

	terminateProcess(cmd)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(grandchildPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still alive after terminateProcess", grandchildPID)
}

// TestTerminateProcessReapsStubbornGrandchild covers the sweep in
// terminateProcess's early-return branch: the wrapper (direct child) exits
// promptly on SIGTERM, but the grandchild ignores SIGTERM. Without the final
// group SIGKILL after Wait returns, that grandchild would survive — the exact
// shape of an MCP server that ignores termination signals behind an npx-style
// wrapper.
func TestTerminateProcessReapsStubbornGrandchild(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}

	// The grandchild traps (ignores) TERM and prints "ready" once the trap is
	// installed, so the test can't race the trap setup. The wrapper prints the
	// grandchild's PID and then blocks in wait, dying on the group's SIGTERM.
	cmd := exec.Command(sh, "-c", `sh -c 'trap "" TERM; echo ready; sleep 30' & echo $!; wait`)
	setupProcAttr(cmd)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start: %v", err)
	}

	// Expect two lines in either order: the grandchild PID and "ready".
	r := bufio.NewReader(stdout)
	grandchildPID := 0
	ready := false
	for i := 0; i < 2; i++ {
		line, err := r.ReadString('\n')
		if err != nil {
			_ = cmd.Process.Kill()
			t.Fatalf("read startup line: %v", err)
		}
		s := strings.TrimSpace(line)
		if s == "ready" {
			ready = true
			continue
		}
		if grandchildPID, err = strconv.Atoi(s); err != nil {
			_ = cmd.Process.Kill()
			t.Fatalf("parse grandchild pid %q: %v", s, err)
		}
	}
	if !ready || grandchildPID == 0 {
		_ = cmd.Process.Kill()
		t.Fatalf("startup incomplete: ready=%v pid=%d", ready, grandchildPID)
	}

	terminateProcess(cmd)

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(grandchildPID) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("SIGTERM-ignoring grandchild pid %d still alive after terminateProcess", grandchildPID)
}

// TestTerminateProcessIsIdempotentViaClose exercises the same path
// StdioClient.Close takes, confirming Close's closeOnce guard still applies
// (Close must not double-call terminateProcess/Wait on a second Close, which
// would be a race on cmd.Process) and that the real subprocess is actually
// reaped. The fake server never answers the handshake, so Connect times out
// via ctx and — per its existing error path — calls Close itself; the test
// then calls Close again directly to confirm idempotency and checks the
// process is gone.
func TestTerminateProcessIsIdempotentViaClose(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	c := &StdioClient{cfg: ServerConfig{Name: "x", Transport: TransportStdio, Command: sh, Args: []string{"-c", "sleep 30"}}, pending: map[int]chan rpcResponse{}, closed: make(chan struct{})}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Connect(ctx); err == nil {
		t.Fatal("Connect against a non-responding process should fail once ctx expires")
	}
	pid := c.cmd.Process.Pid

	if err := c.Close(); err != nil { // idempotent: Connect's error path already called Close
		t.Fatalf("second close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !alive(pid) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process pid %d still alive after Close", pid)
}
