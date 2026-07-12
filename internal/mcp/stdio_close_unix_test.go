//go:build !windows

package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestTerminateProcessIsIdempotentViaClose(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	c := &StdioClient{cfg: ServerConfig{Name: "x", Transport: TransportStdio, Command: sh, Args: []string{"-c", "sleep 30"}}, pending: map[int]chan rpcResponse{}, closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.Connect(ctx); err == nil {
		t.Fatal("Connect should fail once ctx expires")
	}
	pid := c.cmd.Process.Pid
	if err := c.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if syscall.Kill(pid, 0) != nil {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("process pid %d still alive after Close", pid)
}

// A descendant that leaves the process group (setsid) survives the group
// kill and can hold the inherited stderr pipe open indefinitely. Without
// cmd.WaitDelay, cmd.Wait blocks on that pipe forever — wedging Close, and
// with it Connect's failure path and Registry.Close on app exit.
func TestCloseReturnsWhenDescendantEscapesProcessGroup(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not available")
	}
	perl, err := exec.LookPath("perl")
	if err != nil {
		t.Skip("perl not available (needed for setsid)")
	}
	pidFile := filepath.Join(t.TempDir(), "pid")
	script := fmt.Sprintf(
		`%s -MPOSIX -e 'POSIX::setsid(); open my $f, ">", shift; print $f $$; close $f; sleep 30' "%s" & sleep 30`,
		perl, pidFile)
	t.Cleanup(func() {
		b, err := os.ReadFile(pidFile)
		if err != nil {
			return
		}
		if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil && pid > 1 {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
	})
	c := &StdioClient{cfg: ServerConfig{Name: "x", Transport: TransportStdio, Command: sh, Args: []string{"-c", script}}, pending: map[int]chan rpcResponse{}, closed: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- c.Connect(ctx) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("Connect should fail once ctx expires")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Connect blocked on an escaped descendant's stderr pipe — WaitDelay regression")
	}
}
