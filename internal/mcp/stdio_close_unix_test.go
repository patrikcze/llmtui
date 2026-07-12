//go:build !windows

package mcp

import (
	"context"
	"os/exec"
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
