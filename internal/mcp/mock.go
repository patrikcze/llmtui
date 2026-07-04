package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
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

// Connect marks the client connected.
func (m *MockClient) Connect(ctx context.Context) error {
	if m.ConnectErr != nil {
		return m.ConnectErr
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.connected = true
	return nil
}

// ListTools returns the canned tools.
func (m *MockClient) ListTools(ctx context.Context) ([]Tool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.connected {
		return nil, fmt.Errorf("mock client not connected")
	}
	return m.CannedTools, nil
}

// CallTool returns CallFunc's result or an echo.
func (m *MockClient) CallTool(ctx context.Context, name string, input json.RawMessage) (Result, error) {
	m.mu.Lock()
	connected := m.connected
	m.mu.Unlock()
	if !connected {
		return Result{}, fmt.Errorf("mock client not connected")
	}
	if m.CallFunc != nil {
		return m.CallFunc(name, input)
	}
	return Result{Content: fmt.Sprintf("%s(%s)", name, string(input))}, nil
}

// Close marks the client closed; safe to call repeatedly.
func (m *MockClient) Close() error {
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
