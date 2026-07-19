package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
)

func TestConfigShowRedactsMCPEnvironmentValues(t *testing.T) {
	r := &Root{cfg: &config.Config{
		Providers: map[string]config.ProviderConfig{"remote": {APIKey: "provider-secret-marker"}},
		MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"jira": {Env: map[string]string{"TOKEN": "mcp-secret-marker"}},
		}},
	}}
	cmd := newConfigShowCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}
	for _, secret := range []string{"provider-secret-marker", "mcp-secret-marker"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("config show leaked %q:\n%s", secret, out.String())
		}
	}
	if !strings.Contains(out.String(), "TOKEN: '***'") && !strings.Contains(out.String(), "TOKEN: \"***\"") {
		t.Fatalf("config show omitted the redacted MCP env key:\n%s", out.String())
	}
}
