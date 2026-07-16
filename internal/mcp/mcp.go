// Package mcp provides the configuration, interfaces, and registry for
// Model Context Protocol servers. It is MCP-ready scaffolding: servers are
// declared in config, disabled by default, and never started without an
// explicit user action. This package defines the transport-agnostic Client
// interface and a registry that tracks server state; a concrete stdio
// transport is layered on separately.
//
// Nothing here runs a subprocess on its own. Starting an MCP server is a
// potentially dangerous action (it executes a user-configured command) and
// follows the same approval posture as the workspace tools.
package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Transport names the wire protocol for a server. Only stdio is defined for
// now; others (sse, http) can be added without touching the registry.
const (
	TransportStdio = "stdio"
)

// Approval modes for MCP tool calls.
const (
	ApproveAsk  = "ask"
	ApproveAuto = "auto"
)

// ServerConfig declares one MCP server. It is populated from config and is
// inert until the user enables and connects the server.
type ServerConfig struct {
	Name      string
	Enabled   bool
	Transport string
	Command   string
	Args      []string
	Env       map[string]string
	Timeout   time.Duration
	Approve   string // "ask" (default) or "auto"
}

// Tool is one capability advertised by an MCP server.
type Tool struct {
	Server      string          `json:"server"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema,omitempty"`
}

// Result is the outcome of an MCP tool call.
type Result struct {
	Content string
	IsError bool
}

// Client is a transport-agnostic connection to one MCP server. Implementations
// must be safe to Close more than once.
type Client interface {
	// Connect establishes the transport and performs any handshake.
	Connect(ctx context.Context) error
	// ListTools returns the server's advertised tools.
	ListTools(ctx context.Context) ([]Tool, error)
	// CallTool invokes one tool with JSON arguments.
	CallTool(ctx context.Context, name string, input json.RawMessage) (Result, error)
	// Close tears down the transport (and any subprocess).
	Close() error
}

// ClientFactory builds a Client for a server config. A registry with a nil
// factory is config-only: it can validate and describe servers but cannot
// connect. A concrete transport supplies a factory to enable connections.
type ClientFactory func(ServerConfig) (Client, error)

// Validate checks a single server config. It is only meaningful for servers
// the user intends to run; callers should skip disabled servers unless MCP
// itself is enabled.
func (c ServerConfig) Validate() error {
	if c.Name == "" {
		return fmt.Errorf("mcp server has no name")
	}
	if strings.Contains(c.Name, "__") {
		// "__" separates server from tool in the model-visible tool name
		// ("mcp__<server>__<tool>"); a server name containing it would make
		// the split ambiguous and could route a call to the wrong server.
		return fmt.Errorf("mcp server %q: name must not contain %q (reserved as the tool-name separator)", c.Name, "__")
	}
	switch c.Transport {
	case TransportStdio:
		if c.Command == "" {
			return fmt.Errorf("mcp server %q: stdio transport needs a command", c.Name)
		}
	case "":
		return fmt.Errorf("mcp server %q: no transport set", c.Name)
	default:
		return fmt.Errorf("mcp server %q: unsupported transport %q", c.Name, c.Transport)
	}
	if c.Approve != "" && c.Approve != ApproveAsk && c.Approve != ApproveAuto {
		return fmt.Errorf("mcp server %q: approve must be %q or %q", c.Name, ApproveAsk, ApproveAuto)
	}
	return nil
}

// RedactedEnv returns the env with values masked, for display. MCP server
// environments frequently carry credentials, which must never be shown or
// logged in cleartext.
func (c ServerConfig) RedactedEnv() map[string]string {
	if len(c.Env) == 0 {
		return nil
	}
	out := make(map[string]string, len(c.Env))
	for k := range c.Env {
		out[k] = "***"
	}
	return out
}

// ApproveMode normalizes the approval mode, defaulting to "ask".
func (c ServerConfig) ApproveMode() string {
	if c.Approve == ApproveAuto {
		return ApproveAuto
	}
	return ApproveAsk
}
