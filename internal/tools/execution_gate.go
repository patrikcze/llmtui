package tools

import (
	"context"
	"sync"
)

// ConcurrentSafe reports whether c may run at the same time as other
// concurrent-safe calls. Only built-in calls that never change the
// workspace or anything outside the app qualify: workspace reads and the
// web tools. Writes, edits, commands, MCP calls, personal apps, skills and
// controller-handled tools always run alone, in model order.
func ConcurrentSafe(c Call) bool {
	if c.MCPServer != "" || c.InputErr != "" {
		return false
	}
	switch c.Tool {
	case ToolListDir, ToolReadFile, ToolGlob, ToolGrep, ToolWebSearch, ToolWebFetch, ToolWebResearch:
		return true
	default:
		return false
	}
}

// executionGate is a cancellable shared/exclusive lock over one runner.
// Concurrent-safe calls share it; every other call holds it alone, so a
// write or command never overlaps any other call — including a leftover
// goroutine from a cancelled batch. A waiting exclusive call blocks new
// shared calls, so a stream of reads cannot starve a write.
type executionGate struct {
	mu             sync.Mutex
	shared         int
	exclusive      bool
	waitingWriters int
	changed        chan struct{}
}

func newExecutionGate() *executionGate {
	return &executionGate{changed: make(chan struct{})}
}

// acquire waits for the gate in shared or exclusive mode and returns its
// release function, or ctx's error if ctx ends first.
func (g *executionGate) acquire(ctx context.Context, shared bool) (func(), error) {
	g.mu.Lock()
	if !shared {
		g.waitingWriters++
	}
	for {
		if shared && !g.exclusive && g.waitingWriters == 0 {
			g.shared++
			g.mu.Unlock()
			return g.releaseShared, nil
		}
		if !shared && !g.exclusive && g.shared == 0 {
			g.waitingWriters--
			g.exclusive = true
			g.mu.Unlock()
			return g.releaseExclusive, nil
		}
		changed := g.changed
		g.mu.Unlock()
		select {
		case <-changed:
			g.mu.Lock()
		case <-ctx.Done():
			if !shared {
				g.mu.Lock()
				g.waitingWriters--
				g.broadcastLocked()
				g.mu.Unlock()
			}
			return nil, ctx.Err()
		}
	}
}

func (g *executionGate) releaseShared() {
	g.mu.Lock()
	g.shared--
	g.broadcastLocked()
	g.mu.Unlock()
}

func (g *executionGate) releaseExclusive() {
	g.mu.Lock()
	g.exclusive = false
	g.broadcastLocked()
	g.mu.Unlock()
}

// broadcastLocked wakes every waiter; callers hold g.mu.
func (g *executionGate) broadcastLocked() {
	close(g.changed)
	g.changed = make(chan struct{})
}
