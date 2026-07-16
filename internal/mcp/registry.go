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
	StatusConnecting  Status = "connecting"   // dial/handshake in flight
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

	client        Client
	connectCancel context.CancelFunc // set while Connect is dialing; guarded by Registry.mu
	connectGen    int                // bumped per connect attempt; a stale attempt must not touch state
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
	mu       sync.Mutex
	factory  ClientFactory
	servers  map[string]*Server
	order    []string
	closed   bool
	inflight sync.WaitGroup
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
		out = append(out, cloneServer(r.servers[name]))
	}
	return out
}

// Get returns one server by name.
func (r *Registry) Get(name string) (*Server, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.serverLocked(name)
	if !ok {
		return nil, false
	}
	return cloneServer(s), true
}

func cloneServer(s *Server) *Server {
	clone := *s
	clone.Tools = append([]Tool(nil), s.Tools...)
	return &clone
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

// Disable closes any connection and marks the server disabled. The client is
// closed outside the registry lock because process teardown may block.
func (r *Registry) Disable(name string) error {
	r.mu.Lock()
	s, ok := r.serverLocked(name)
	if !ok {
		r.mu.Unlock()
		return fmt.Errorf("no MCP server named %q", name)
	}
	client := s.client
	s.client = nil
	s.Config.Enabled = false
	s.Status = StatusDisabled
	s.Tools = nil
	s.LastErr = nil
	if s.connectCancel != nil {
		s.connectCancel()
	}
	r.mu.Unlock()
	if client != nil {
		_ = client.Close()
	}
	return nil
}

// beginConnect validates and registers a connection attempt under one lock.
// The returned generation identifies this attempt: after a disable→enable
// cycle a newer attempt may own the server, and this (now stale) attempt must
// not commit a client, record an error, or cancel the newer attempt's context.
func (r *Registry) beginConnect(ctx context.Context, name string) (context.Context, *Server, int, ServerConfig, ClientFactory, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, 0, ServerConfig{}, nil, fmt.Errorf("mcp: registry is closed")
	}
	s, ok := r.serverLocked(name)
	if !ok {
		return nil, nil, 0, ServerConfig{}, nil, fmt.Errorf("no MCP server named %q", name)
	}
	if err := s.Config.Validate(); err != nil {
		// Refuse to connect a misconfigured server (e.g. a name containing
		// "__", which would make tool-call routing ambiguous) instead of
		// leaving the problem to surface as a misrouted call later.
		return nil, nil, 0, ServerConfig{}, nil, err
	}
	switch {
	case s.client != nil:
		return nil, nil, 0, ServerConfig{}, nil, fmt.Errorf("MCP server %q is already connected", name)
	case s.Status == StatusConnecting:
		return nil, nil, 0, ServerConfig{}, nil, fmt.Errorf("MCP server %q: connect already in progress", name)
	case !s.Config.Enabled:
		return nil, nil, 0, ServerConfig{}, nil, fmt.Errorf("MCP server %q is disabled", name)
	}
	connectCtx, cancel := context.WithCancel(ctx)
	s.Status = StatusConnecting
	s.connectCancel = cancel
	s.connectGen++
	r.inflight.Add(1)
	return connectCtx, s, s.connectGen, s.Config, r.factory, nil
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
	ctx, s, gen, config, factory, err := r.beginConnect(ctx, name)
	if err != nil {
		return err
	}
	defer func() {
		r.mu.Lock()
		// After disable→enable→connect, connectCancel belongs to the newer
		// attempt; cancelling it here would abort that attempt spuriously.
		if s.connectGen == gen && s.connectCancel != nil {
			s.connectCancel()
			s.connectCancel = nil
		}
		r.mu.Unlock()
		r.inflight.Done()
	}()

	if factory == nil {
		err := fmt.Errorf("no MCP transport is available in this build")
		r.setError(s, gen, StatusNoTransport, err)
		return err
	}
	client, err := factory(config)
	if err != nil {
		r.setError(s, gen, StatusError, err)
		return err
	}
	if err := client.Connect(ctx); err != nil {
		_ = client.Close()
		r.setError(s, gen, StatusError, err)
		return err
	}
	tools, err := client.ListTools(ctx)
	if err != nil {
		_ = client.Close()
		r.setError(s, gen, StatusError, err)
		return err
	}

	r.mu.Lock()
	if s.connectGen != gen {
		// A newer attempt owns the server now; committing here would
		// overwrite (and leak) its client. Leave all state to it.
		r.mu.Unlock()
		_ = client.Close()
		return fmt.Errorf("MCP server %q: connect superseded by a newer attempt", name)
	}
	if r.closed || !s.Config.Enabled {
		reason := "was disabled during connect"
		if r.closed {
			reason = "registry closed during connect"
			s.Status = StatusConfigured
		} else {
			s.Status = StatusDisabled
		}
		r.mu.Unlock()
		_ = client.Close()
		return fmt.Errorf("MCP server %q %s", name, reason)
	}
	s.client = client
	s.Tools = tools
	s.Status = StatusConnected
	s.LastErr = nil
	r.mu.Unlock()
	return nil
}

func (r *Registry) setError(s *Server, gen int, status Status, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if s.connectGen != gen {
		return // a newer attempt owns the server's status now
	}
	if !s.Config.Enabled {
		s.Status = StatusDisabled
		s.LastErr = nil
		return
	}
	s.Status = status
	s.LastErr = err
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
	var client Client
	if ok {
		client = s.client
	}
	r.mu.Unlock()
	if !ok {
		return Result{}, fmt.Errorf("no MCP server named %q", server)
	}
	if client == nil {
		return Result{}, fmt.Errorf("MCP server %q is not connected", server)
	}
	return client.CallTool(ctx, tool, input)
}

// Close refuses future connects, cancels in-flight ones, tears down every
// open connection, and waits for all connection attempts to unwind.
func (r *Registry) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closed = true
	var clients []Client
	for _, s := range r.servers {
		if s.connectCancel != nil {
			s.connectCancel()
		}
		if s.client != nil {
			clients = append(clients, s.client)
			s.client = nil
		}
	}
	r.mu.Unlock()
	for _, client := range clients {
		_ = client.Close()
	}
	r.inflight.Wait()
}
