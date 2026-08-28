package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
)

func writeProviderConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := `
default_provider: ollama
providers:
  remote:
    type: openai_compatible
    base_url: http://localhost:8080/v1
    api_key_env: LLMTUI_REMOTE_KEY
    default_model: remote-model
chat:
  temperature: 0.3
`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func runProviderCommand(t *testing.T, path string, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd("test", "test", "test", func(*Root, string, bool) error {
		t.Fatal("provider command must not launch chat")
		return nil
	})
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(append([]string{"--config", path}, args...))
	err := cmd.Execute()
	return output.String(), err
}

func TestProviderSwitchPersistsDefaultProvider(t *testing.T) {
	path := writeProviderConfig(t)
	out, err := runProviderCommand(t, path, "provider", "switch", "remote")
	if err != nil {
		t.Fatalf("provider switch: %v", err)
	}
	if !strings.Contains(out, `default provider set to "remote"`) {
		t.Fatalf("provider switch output = %q", out)
	}

	v, err := config.NewViper(path)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := config.Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProvider != "remote" {
		t.Errorf("DefaultProvider = %q, want remote", cfg.DefaultProvider)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	if !strings.Contains(string(data), "api_key_env: LLMTUI_REMOTE_KEY") {
		t.Fatalf("provider switch did not preserve secret indirection:\n%s", data)
	}
}

func TestProviderSwitchUnknownProviderDoesNotChangeConfig(t *testing.T) {
	path := writeProviderConfig(t)
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config before switch: %v", err)
	}
	_, err = runProviderCommand(t, path, "provider", "switch", "missing")
	if err == nil || !strings.Contains(err.Error(), `unknown provider "missing"`) {
		t.Fatalf("provider switch error = %v, want unknown provider", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config after switch: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("unknown provider switch changed config")
	}
}

func TestProviderSwitchCreatesConfigForBuiltinProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config", "config.yaml")
	if _, err := runProviderCommand(t, path, "provider", "switch", "lmstudio"); err != nil {
		t.Fatalf("provider switch: %v", err)
	}
	v, err := config.NewViper(path)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := config.Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.DefaultProvider != "lmstudio" {
		t.Errorf("DefaultProvider = %q, want lmstudio", cfg.DefaultProvider)
	}
}

func TestProviderSwitchRequiresProviderName(t *testing.T) {
	path := writeProviderConfig(t)
	_, err := runProviderCommand(t, path, "provider", "switch")
	if err == nil || !strings.Contains(err.Error(), "accepts 1 arg(s), received 0") {
		t.Fatalf("provider switch error = %v, want argument error", err)
	}
}
