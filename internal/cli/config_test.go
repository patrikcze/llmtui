package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
)

func TestConfigInitWritesStarterConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	r := &Root{cfgFile: path}
	cmd := newConfigInitCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if string(data) != config.DefaultYAML {
		t.Fatal("config init output differs from the tested starter config")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat generated config: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("generated config mode = %o, want 600", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), path) {
		t.Fatalf("config init confirmation omits path: %q", out.String())
	}
}

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
