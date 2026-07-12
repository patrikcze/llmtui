package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// MockClient is an in-memory Client for tests and for exercising the registry
// without a real subprocess. It records lifecycle calls and returns canned
// tools and results.
type MockClient struct {
	ServerName  string
	CannedTools []Tool
	// CallFunc, if set, produces the result for CallTool; otherwise a simple
	// echo result is returned.
	CallFunc func(name string, input json.RawMessage) (Result, error)
	// ConnectErr, if set, makes Connect fail (to test error paths).
	ConnectErr error
	// Delay, if set, makes CallTool block for this long (or until ctx is
	// done, whichever comes first) before producing its result — lets tests
	// exercise real timeout/cancellation behavior instead of asserting it
	// structurally.
	Delay time.Duration
	// ConnectGate, if set, makes Connect block until the channel is closed
	// (or ctx is done, whichever comes first) — lets tests hold a Connect
	// call open to exercise concurrent-Connect and disable-during-connect
	// races deterministically instead of relying on timing.
	ConnectGate <-chan struct{}
	// CloseGate, if set, makes Close block until the channel is closed.
	CloseGate <-chan struct{}
	// ListGate, if set, makes ListTools block until the channel is closed,
	// deliberately ignoring ctx: a real in-flight response can win the race
	// against cancellation, which is how a stale connect attempt reaches the
	// registry's commit step after the server was disabled and re-enabled.
	ListGate <-chan struct{}
	// ListErr, if set, makes ListTools fail (after ListGate, if both are set).
	ListErr error

	mu        sync.Mutex
	connected bool
	closed    bool
}

// NewMockFactory returns a ClientFactory that builds a MockClient per server,
// advertising one echo tool named "<server>_echo".
func NewMockFactory() ClientFactory {
	return func(c ServerConfig) (Client, error) {
		return &MockClient{
			ServerName: c.Name,
			CannedTools: []Tool{{
				Server:      c.Name,
				Name:        c.Name + "_echo",
				Description: "Echo the input back (mock MCP tool).",
				Schema:      json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`),
			}},
		}, nil
	}
}

// Connect marks the client connected, after first waiting on ConnectGate (if
// set) to let tests hold the call open.
func (m *MockClient) Connect(ctx context.Context) error {
	if m.ConnectGate != nil {
		select {
		case <-m.ConnectGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if m.ConnectErr != nil {
		return m.ConnectErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

// ListTools returns the canned tools, after waiting on ListGate (if set).
func (m *MockClient) ListTools(ctx context.Context) ([]Tool, error) {
	if m.ListGate != nil {
		<-m.ListGate
	}
	if m.ListErr != nil {
		return nil, m.ListErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return nil, fmt.Errorf("mock client not connected")
	}
	return m.CannedTools, nil
}

// Connected reports whether Connect completed (for tests).
func (m *MockClient) Connected() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected
}

// CallTool returns CallFunc's result or an echo, after respecting Delay
// against ctx.
func (m *MockClient) CallTool(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	m.mu.Lock()
	connected := m.connected
	delay := m.Delay
	m.mu.Unlock()
	if !connected {
		return Result{}, fmt.Errorf("mock client not connected")
	}
	if delay > 0 {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(delay):
		}
	}
	if m.CallFunc != nil {
		return m.CallFunc(name, input)
	}
	return Result{Content: fmt.Sprintf("%s(%s)", name, string(input))}, nil
}

// Close marks the client closed; safe to call repeatedly.
func (m *MockClient) Close() error {
	if m.CloseGate != nil {
		<-m.CloseGate
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closed = true
	m.connected = false
	return nil
}

// Closed reports whether Close was called (for tests).
func (m *MockClient) Closed() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed
}
