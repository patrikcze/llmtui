package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/mcp"
)

func TestMcpServerConfigsConversion(t *testing.T) {
	c := config.MCPConfig{
		Enabled: true,
		Servers: map[string]config.MCPServerConfig{
			"files": {Enabled: true, Transport: "stdio", Command: "mcp-fs", Timeout: "15s"},
			"off":   {Enabled: false, Transport: "stdio", Command: "mcp-x"},
		},
	}
	got := mcpServerConfigs(c)
	if len(got) != 2 {
		t.Fatalf("got %d servers, want 2", len(got))
	}
	// Sorted by name: "files" before "off".
	if got[0].Name != "files" || !got[0].Enabled {
		t.Errorf("files config wrong: %+v", got[0])
	}
	if got[0].Timeout.String() != "15s" {
		t.Errorf("timeout = %s, want 15s", got[0].Timeout)
	}
	// A disabled server stays disabled even though mcp.enabled is true.
	if got[1].Name != "off" || got[1].Enabled {
		t.Errorf("off config wrong: %+v", got[1])
	}
}

func TestMcpServerDisabledWhenMcpDisabled(t *testing.T) {
	c := config.MCPConfig{
		Enabled: false, // master switch off
		Servers: map[string]config.MCPServerConfig{
			"files": {Enabled: true, Transport: "stdio", Command: "mcp-fs"},
		},
	}
	got := mcpServerConfigs(c)
	if got[0].Enabled {
		t.Error("server enabled despite mcp.enabled=false")
	}
}

func TestCmdMcpStatusOverlay(t *testing.T) {
	m := newTestModel(t)
	m.mcpRegistry = mcp.NewRegistry(nil, nil)
	if cmd := cmdMcp(m, "status"); cmd != nil {
		t.Fatalf("status returned command: %v", cmd)
	}
	if !m.overlayOpen {
		t.Fatal("status did not open overlay")
	}
}

func TestCmdMcpEnableDisable(t *testing.T) {
	m := newTestModel(t)
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "files", Transport: "stdio", Command: "mcp-fs"},
	}, nil)
	cmdMcp(m, "enable files")
	s, _ := m.mcpRegistry.Get("files")
	if !s.Config.Enabled {
		t.Error("enable did not set the server enabled")
	}
	cmdMcp(m, "disable files")
	s, _ = m.mcpRegistry.Get("files")
	if s.Config.Enabled {
		t.Error("disable did not clear the server enabled")
	}
}

func TestCmdMcpEnableUnknownServerFails(t *testing.T) {
	m := newTestModel(t)
	m.mcpRegistry = mcp.NewRegistry(nil, nil)
	cmdMcp(m, "enable ghost")
	if m.errText == "" {
		t.Error("enabling an unknown server should report an error")
	}
}

func TestDoctorMcpValidatesConfig(t *testing.T) {
	m := newTestModel(t)
	m.cfg.MCP.Enabled = false
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "good", Transport: "stdio", Command: "srv"},
		{Name: "bad", Transport: "stdio", Command: ""}, // invalid: no command
	}, nil)
	out := m.doctorMcpOverlay()
	if !strings.Contains(out, "good") || !strings.Contains(out, "bad") {
		t.Errorf("doctor mcp missing servers:\n%s", out)
	}
	// The invalid server must be flagged.
	if !strings.Contains(out, "✗") {
		t.Errorf("invalid server not flagged:\n%s", out)
	}
}

func TestMcpInspectRedactsEnv(t *testing.T) {
	m := newTestModel(t)
	m.mcpRegistry = mcp.NewRegistry([]mcp.ServerConfig{
		{Name: "s", Transport: "stdio", Command: "srv", Env: map[string]string{"SECRET_TOKEN": "abc123"}},
	}, nil)
	out := m.mcpInspectOverlay("s")
	if strings.Contains(out, "abc123") {
		t.Error("inspect leaked a secret env value")
	}
	if !strings.Contains(out, "SECRET_TOKEN") {
		t.Error("inspect should still list the env var name")
	}
}
