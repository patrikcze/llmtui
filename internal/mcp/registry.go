package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Status is a server's runtime connection state.
type Status string

const (
	StatusDisabled    Status = "disabled"     // not enabled in config/session
	StatusConfigured  Status = "configured"   // enabled but not connected
	StatusConnected   Status = "connected"    // handshake complete, tools listed
	StatusError       Status = "error"        // last connect/list attempt failed
	StatusNoTransport Status = "no_transport" // enabled, but no transport implemented
)

// Server is one registered MCP server: its config plus runtime state.
type Server struct {
	Config  ServerConfig
	Status  Status
	Tools   []Tool
	LastErr error

	client     Client
	connecting bool // true while a Connect call is dialing/handshaking, guarded by Registry.mu
}

func (r *Registry) serverLocked(name string) (*Server, bool) {
	if s, ok := r.servers[name]; ok {
		return s, true
	}
	for configured, s := range r.servers {
		if strings.EqualFold(configured, name) {
			return s, true
		}
	}
	return nil, false
}

// Registry tracks configured MCP servers and their state. It is safe for
// concurrent use. With a nil factory it is config-only (no connections).
type Registry struct {
	mu      sync.Mutex
	factory ClientFactory
	servers map[string]*Server
	order   []string
}

// NewRegistry builds a registry from server configs. Servers start
// Configured (if enabled) or Disabled; nothing connects until Connect is
// called. A nil factory marks every enabled server StatusNoTransport.
func NewRegistry(configs []ServerConfig, factory ClientFactory) *Registry {
	r := &Registry{factory: factory, servers: map[string]*Server{}}
	for _, c := range configs {
		s := &Server{Config: c, Status: StatusDisabled}
		if c.Enabled {
			s.Status = r.enabledStatus()
		}
		r.servers[c.Name] = s
		r.order = append(r.order, c.Name)
	}
	sort.Strings(r.order)
	return r
}

func (r *Registry) enabledStatus() Status {
	if r.factory == nil {
		return StatusNoTransport
	}
	return StatusConfigured
}

// List returns servers in name order.
func (r *Registry) List() []*Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*Server, 0, len(r.order))
	for _, name := range r.order {
		out = append(out, r.servers[name])
	}
	return out
}

// Get returns one server by name.
func (r *Registry) Get(name string) (*Server, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.serverLocked(name)
	return s, ok
}

// Enable marks a server as intended-to-run. It does not connect; call
// Connect to establish the transport.
func (r *Registry) Enable(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.serverLocked(name)
	if !ok {
		return fmt.Errorf("no MCP server named %q", name)
	}
	s.Config.Enabled = true
	if s.Status == StatusDisabled {
		s.Status = r.enabledStatus()
	}
	return nil
}

// Disable closes any connection and marks the server disabled.
func (r *Registry) Disable(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.serverLocked(name)
	if !ok {
		return fmt.Errorf("no MCP server named %q", name)
	}
	if s.client != nil {
		_ = s.client.Close()
		s.client = nil
	}
	s.Config.Enabled = false
	s.Status = StatusDisabled
	s.Tools = nil
	s.LastErr = nil
	return nil
}

// Connect establishes the transport for one enabled server and lists its
// tools. It requires a factory (a transport implementation); without one it
// returns an error and leaves the server StatusNoTransport.
//
// Connect refuses to run twice concurrently (or on top of an already-live
// client) for the same server: without that guard a second call would either
// leak the first client's subprocess (overwritten, never closed) or race a
// concurrent Disable, which could be clobbered by this call's success path.
func (r *Registry) Connect(ctx context.Context, name string) error {
	r.mu.Lock()
	s, ok := r.serverLocked(name)
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("no MCP server named %q", name)
	}
	if s.client != nil {
		r.mu.Unlock()
		return fmt.Errorf("MCP server %q is already connected", name)
	}
	if s.connecting {
		r.mu.Unlock()
		return fmt.Errorf("MCP server %q: connect already in progress", name)
	}
	if !s.Config.Enabled {
		r.mu.Unlock()
		return fmt.Errorf("MCP server %q is disabled", name)
	}
	factory := r.factory
	s.connecting = true
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		s.connecting = false
		r.mu.Unlock()
	}()

	if factory == nil {
		r.setError(s, StatusNoTransport, fmt.Errorf("no MCP transport is available in this build"))
		return s.LastErr
	}
	client, err := factory(s.Config)
	if err != nil {
		r.setError(s, StatusError, err)
		return err
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		r.setError(s, StatusError, err)
		return err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		r.setError(s, StatusError, err)
		return err
	}

	r.mu.Lock()
	if !s.Config.Enabled {
		// Disabled while we were dialing/handshaking: don't resurrect the
		// server as connected out from under Disable. Close the fresh
		// client and keep the disabled state Disable already put in place.
		r.mu.Unlock()
		_ = client.Close()
		return fmt.Errorf("MCP server %q was disabled during connect", name)
	}
	if s.client != nil {
		// Belt and braces: shouldn't happen given the connecting guard
		// above, but never leak a client we're about to replace.
		_ = s.client.Close()
	}
	s.client = client
	s.Tools = tools
	s.Status = StatusConnected
	s.LastErr = nil
	r.mu.Unlock()
	return nil
}

func (r *Registry) setError(s *Server, status Status, err error) {
	r.mu.Lock()
	s.Status = status
	s.LastErr = err
	r.mu.Unlock()
}

// Tools returns all tools across connected servers.
func (r *Registry) Tools() []Tool {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Tool
	for _, name := range r.order {
		out = append(out, r.servers[name].Tools...)
	}
	return out
}

// CallTool routes a tool call to the server that advertised it.
func (r *Registry) CallTool(ctx context.Context, server, tool string, input json.RawMessage) (Result, error) {
	r.mu.Lock()
	s, ok := r.serverLocked(server)
	r.mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("no MCP server named %q", server)
	}
	if s.client == nil {
		return Result{}, fmt.Errorf("MCP server %q is not connected", server)
	}
	return s.client.CallTool(ctx, tool, input)
}

// Close tears down every open connection. It is safe to call on a nil
// Registry (a no-op) and safe to call more than once.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, s := range r.servers {
		if s.client != nil {
			_ = s.client.Close()
			s.client = nil
		}
	}
}
