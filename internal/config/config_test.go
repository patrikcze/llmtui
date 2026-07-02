package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func TestDefaultsApplyWithoutConfigFile(t *testing.T) {
	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultProvider != "ollama" {
		t.Errorf("DefaultProvider = %q, want ollama", cfg.DefaultProvider)
	}
	if cfg.Chat.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", cfg.Chat.Temperature)
	}
	if !cfg.Chat.Stream {
		t.Error("Stream should default to true")
	}
	if _, ok := cfg.Providers["ollama"]; !ok {
		t.Error("built-in ollama provider missing")
	}
	if _, ok := cfg.Providers["mock"]; !ok {
		t.Error("built-in mock provider missing")
	}
}

func TestConfigFileOverridesDefaults(t *testing.T) {
	path := writeConfig(t, `
default_provider: lmstudio
chat:
  temperature: 0.3
`)
	v, err := NewViper(path)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultProvider != "lmstudio" {
		t.Errorf("DefaultProvider = %q, want lmstudio", cfg.DefaultProvider)
	}
	if cfg.Chat.Temperature != 0.3 {
		t.Errorf("Temperature = %v, want 0.3", cfg.Chat.Temperature)
	}
	// Untouched keys keep defaults.
	if cfg.Chat.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d, want default 4096", cfg.Chat.MaxTokens)
	}
}

func TestEnvOverridesConfigFile(t *testing.T) {
	path := writeConfig(t, "default_provider: lmstudio\n")
	t.Setenv("LLMTUI_DEFAULT_PROVIDER", "openai_compatible")
	t.Setenv("LLMTUI_MODEL", "env-model")

	v, err := NewViper(path)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.DefaultProvider != "openai_compatible" {
		t.Errorf("DefaultProvider = %q, want env override openai_compatible", cfg.DefaultProvider)
	}
	if cfg.Model != "env-model" {
		t.Errorf("Model = %q, want env-model", cfg.Model)
	}
}

func TestFlagOverridesEnv(t *testing.T) {
	t.Setenv("LLMTUI_PROVIDER", "from-env")

	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	// Simulate a bound flag: viper.Set has flag-level (highest) precedence.
	v.Set("provider", "from-flag")

	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Provider != "from-flag" {
		t.Errorf("Provider = %q, want from-flag", cfg.Provider)
	}
	if cfg.ActiveProviderName() != "from-flag" {
		t.Errorf("ActiveProviderName = %q, want from-flag", cfg.ActiveProviderName())
	}
}

func TestActiveModelPrecedence(t *testing.T) {
	cfg := &Config{
		DefaultProvider: "ollama",
		DefaultModel:    "global-model",
		Providers: map[string]ProviderConfig{
			"ollama": {Type: "ollama", DefaultModel: "provider-model"},
		},
	}

	if got := cfg.ActiveModel(); got != "provider-model" {
		t.Errorf("ActiveModel = %q, want provider-model", got)
	}
	cfg.Model = "flag-model"
	if got := cfg.ActiveModel(); got != "flag-model" {
		t.Errorf("ActiveModel = %q, want flag-model", got)
	}
	cfg.Model = ""
	cfg.Providers["ollama"] = ProviderConfig{Type: "ollama"}
	if got := cfg.ActiveModel(); got != "global-model" {
		t.Errorf("ActiveModel = %q, want global-model", got)
	}
}

func TestBaseURLAndAPIKeyOverrides(t *testing.T) {
	cfg := &Config{
		DefaultProvider: "ollama",
		Providers: map[string]ProviderConfig{
			"ollama": {Type: "ollama", BaseURL: "http://localhost:11434", APIKey: "orig"},
		},
		BaseURL: "http://elsewhere:9999",
		APIKey:  "override",
	}
	_, pc, ok := cfg.ActiveProvider()
	if !ok {
		t.Fatal("provider not found")
	}
	if pc.BaseURL != "http://elsewhere:9999" {
		t.Errorf("BaseURL = %q, want override", pc.BaseURL)
	}
	if pc.ResolveAPIKey() != "override" {
		t.Errorf("APIKey = %q, want override", pc.ResolveAPIKey())
	}
}

func TestResolveAPIKeyPrefersEnv(t *testing.T) {
	t.Setenv("MY_SECRET_KEY", "from-env")
	pc := ProviderConfig{APIKey: "from-yaml", APIKeyEnv: "MY_SECRET_KEY"}
	if got := pc.ResolveAPIKey(); got != "from-env" {
		t.Errorf("ResolveAPIKey = %q, want from-env", got)
	}

	pc.APIKeyEnv = "UNSET_VAR_FOR_TEST"
	if got := pc.ResolveAPIKey(); got != "from-yaml" {
		t.Errorf("ResolveAPIKey = %q, want from-yaml fallback", got)
	}
}

func TestWriteDefaultRefusesOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteDefault(path); err != nil {
		t.Fatalf("first WriteDefault: %v", err)
	}
	if err := WriteDefault(path); err == nil {
		t.Error("second WriteDefault should refuse to overwrite")
	}
}

func TestStreamEnabled(t *testing.T) {
	cfg := &Config{Chat: ChatConfig{Stream: true}}
	if !cfg.StreamEnabled() {
		t.Error("StreamEnabled should be true")
	}
	cfg.NoStream = true
	if cfg.StreamEnabled() {
		t.Error("NoStream override should disable streaming")
	}
}

func TestRedact(t *testing.T) {
	tests := []struct{ in, want string }{
		{"", ""},
		{"abc", "****"},
		{"sk-verysecretkey", "sk******ey"},
	}
	for _, tt := range tests {
		if got := Redact(tt.in); got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
