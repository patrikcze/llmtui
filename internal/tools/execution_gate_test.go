package tools

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConcurrentSafe(t *testing.T) {
	tests := []struct {
		name string
		call Call
		want bool
	}{
		{name: "read_file", call: Call{Tool: ToolReadFile, Path: "a.go"}, want: true},
		{name: "grep", call: Call{Tool: ToolGrep, Body: "x"}, want: true},
		{name: "glob", call: Call{Tool: ToolGlob, Body: "*.go"}, want: true},
		{name: "list_dir", call: Call{Tool: ToolListDir}, want: true},
		{name: "web_search", call: Call{Tool: ToolWebSearch, Body: "q"}, want: true},
		{name: "web_fetch", call: Call{Tool: ToolWebFetch, Path: "https://example.com"}, want: true},
		{name: "web_research", call: Call{Tool: ToolWebResearch, Body: "q"}, want: true},
		{name: "write_file", call: Call{Tool: ToolWriteFile, Path: "a.go"}},
		{name: "edit_file", call: Call{Tool: ToolEditFile, Path: "a.go"}},
		{name: "read-only run_command still runs alone", call: Call{Tool: ToolRunCommand, Body: "ls"}},
		{name: "personal_apps", call: Call{Tool: ToolPersonalApps}},
		{name: "skill_load", call: Call{Tool: ToolSkillLoad}},
		{name: "local_context", call: Call{Tool: ToolLocalContext}},
		{name: "mcp", call: Call{Tool: ToolReadFile, MCPServer: "srv", MCPTool: "read"}},
		{name: "invalid input", call: Call{Tool: ToolReadFile, InputErr: "bad"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ConcurrentSafe(tt.call); got != tt.want {
				t.Fatalf("ConcurrentSafe = %v, want %v", got, tt.want)
			}
		})
	}
}

// acquireAsync starts an acquire and reports its release once granted.
func acquireAsync(ctx context.Context, g *executionGate, shared bool) <-chan func() {
	out := make(chan func(), 1)
	go func() {
		release, err := g.acquire(ctx, shared)
		if err == nil {
			out <- release
		}
	}()
	return out
}

func mustGrant(t *testing.T, ch <-chan func(), what string) func() {
	t.Helper()
	select {
	case release := <-ch:
		return release
	case <-time.After(5 * time.Second):
		t.Fatalf("%s was not granted", what)
		return nil
	}
}

func mustWait(t *testing.T, ch <-chan func(), what string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatalf("%s was granted while it must wait", what)
	case <-time.After(50 * time.Millisecond):
	}
}

// waitForWaitingWriters blocks until n exclusive acquires are queued.
func waitForWaitingWriters(t *testing.T, g *executionGate, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		g.mu.Lock()
		waiting := g.waitingWriters
		g.mu.Unlock()
		if waiting == n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("waiting writers = %d, want %d", waiting, n)
		}
		time.Sleep(time.Millisecond)
	}
}

func TestExecutionGateSharedCallsOverlap(t *testing.T) {
	g := newExecutionGate()
	ctx := context.Background()
	first := mustGrant(t, acquireAsync(ctx, g, true), "first read")
	second := mustGrant(t, acquireAsync(ctx, g, true), "second read while the first holds the gate")
	first()
	second()
}

func TestExecutionGateExclusiveRunsAlone(t *testing.T) {
	g := newExecutionGate()
	ctx := context.Background()
	read := mustGrant(t, acquireAsync(ctx, g, true), "read")
	write := acquireAsync(ctx, g, false)
	waitForWaitingWriters(t, g, 1)
	mustWait(t, write, "write during a read")
	// A waiting write blocks new reads, so reads cannot starve it.
	lateRead := acquireAsync(ctx, g, true)
	mustWait(t, lateRead, "read queued behind a waiting write")
	read()
	releaseWrite := mustGrant(t, write, "write after the read finished")
	mustWait(t, lateRead, "read during a write")
	releaseWrite()
	mustGrant(t, lateRead, "read after the write finished")()

	writeA := mustGrant(t, acquireAsync(ctx, g, false), "first write")
	writeB := acquireAsync(ctx, g, false)
	mustWait(t, writeB, "second write during the first")
	writeA()
	mustGrant(t, writeB, "second write after the first")()
}

func TestExecutionGateCancelledWriterUnblocksReaders(t *testing.T) {
	g := newExecutionGate()
	read := mustGrant(t, acquireAsync(context.Background(), g, true), "read")
	ctx, cancel := context.WithCancel(context.Background())
	errs := make(chan error, 1)
	go func() {
		_, err := g.acquire(ctx, false)
		errs <- err
	}()
	waitForWaitingWriters(t, g, 1)
	lateRead := acquireAsync(context.Background(), g, true)
	mustWait(t, lateRead, "read queued behind a waiting write")
	cancel()
	if err := <-errs; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled write err = %v", err)
	}
	mustGrant(t, lateRead, "read after the waiting write was cancelled")()
	read()
}
