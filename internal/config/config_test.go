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

func TestNestedEnvOverrides(t *testing.T) {
	t.Setenv("LLMTUI_NETWORK_TIMEOUT", "600s")
	t.Setenv("LLMTUI_CHAT_MAX_TOKENS", "8192")
	t.Setenv("LLMTUI_AGENT_MAX_CYCLES", "5")

	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Network.Timeout != "600s" {
		t.Errorf("Network.Timeout = %q, want 600s from env", cfg.Network.Timeout)
	}
	if cfg.Chat.MaxTokens != 8192 {
		t.Errorf("Chat.MaxTokens = %d, want 8192 from env", cfg.Chat.MaxTokens)
	}
	if cfg.Agent.MaxCycles != 5 {
		t.Errorf("Agent.MaxCycles = %d, want 5 from env", cfg.Agent.MaxCycles)
	}
}

func TestAgentDefaultsAreBoundedAndOptIn(t *testing.T) {
	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent.Enabled {
		t.Fatal("verified agent loop must be disabled by default")
	}
	if cfg.Agent.MaxCycles != 8 || cfg.Agent.MaxToolCalls != 32 || cfg.Agent.MaxTokens != 100000 || cfg.Agent.MaxRepeatedFailures != 3 {
		t.Fatalf("agent limits = %+v", cfg.Agent)
	}
	if cfg.Agent.MaxElapsed != "30m" || !cfg.Agent.Persist || !cfg.Agent.Verifier.Enabled {
		t.Fatalf("agent defaults = %+v", cfg.Agent)
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
	tests := []struct {
		name     string
		provider string
		model    string
		pc       ProviderConfig
		want     string
	}{
		{
			name:     "embedded model path without default",
			provider: "embedded",
			pc:       ProviderConfig{Type: "embedded", ModelPath: "/models/local.gguf"},
			want:     "/models/local.gguf",
		},
		{
			name:     "embedded model path before provider default",
			provider: "embedded",
			pc: ProviderConfig{
				Type:         "embedded",
				ModelPath:    "/models/local.gguf",
				DefaultModel: "provider-model",
			},
			want: "/models/local.gguf",
		},
		{
			name:     "explicit model override before embedded path",
			provider: "embedded",
			model:    "/models/override.gguf",
			pc:       ProviderConfig{Type: "embedded", ModelPath: "/models/local.gguf"},
			want:     "/models/override.gguf",
		},
		{
			name:     "remote provider default unchanged",
			provider: "ollama",
			pc:       ProviderConfig{Type: "ollama", DefaultModel: "provider-model"},
			want:     "provider-model",
		},
		{
			name:     "global default remains final fallback",
			provider: "ollama",
			pc:       ProviderConfig{Type: "ollama"},
			want:     "global-model",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := &Config{
				DefaultProvider: tt.provider,
				DefaultModel:    "global-model",
				Model:           tt.model,
				Providers:       map[string]ProviderConfig{tt.provider: tt.pc},
			}
			if got := cfg.ActiveModel(); got != tt.want {
				t.Errorf("ActiveModel() = %q, want %q", got, tt.want)
			}
		})
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

func TestRedactedCopyMasksProviderAndMCPSecretsWithoutMutation(t *testing.T) {
	cfg := &Config{
		Providers: map[string]ProviderConfig{"remote": {APIKey: "provider-marker-secret"}},
		MCP: MCPConfig{Servers: map[string]MCPServerConfig{
			"jira": {Env: map[string]string{"TOKEN": "mcp-marker-secret"}},
		}},
	}
	shown := RedactedCopy(cfg)
	if shown.Providers["remote"].APIKey == "provider-marker-secret" {
		t.Fatal("provider API key was not redacted")
	}
	if shown.MCP.Servers["jira"].Env["TOKEN"] != "***" {
		t.Fatalf("MCP secret = %q, want redacted", shown.MCP.Servers["jira"].Env["TOKEN"])
	}
	if cfg.Providers["remote"].APIKey != "provider-marker-secret" || cfg.MCP.Servers["jira"].Env["TOKEN"] != "mcp-marker-secret" {
		t.Fatal("RedactedCopy mutated the live configuration")
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

// TestWriteDefaultProducesLoadableConfig guards the annotated embedded
// example block added to DefaultYAML: even though it is commented out, a
// stray syntax mistake (unbalanced quote, bad indentation, a literal
// backtick breaking the Go raw string) must not go unnoticed.
func TestWriteDefaultProducesLoadableConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := WriteDefault(path); err != nil {
		t.Fatalf("WriteDefault: %v", err)
	}
	v, err := NewViper(path)
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
	if _, ok := cfg.Providers["embedded"]; !ok {
		t.Error("embedded provider should still be present (builtin), even though its YAML example is commented out")
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
		{"abc", "********"},
		{"secret", "********"},    // short keys reveal nothing
		{"lm-studio", "********"}, // 9 chars: 2+2 visible would leak half
		{"sk-verysecretkey", "sk******ey"},
	}
	for _, tt := range tests {
		if got := Redact(tt.in); got != tt.want {
			t.Errorf("Redact(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestWebToolsDefaults(t *testing.T) {
	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	w := cfg.Tools.Web
	if w.Enabled {
		t.Error("tools.web must be disabled by default (local-first)")
	}
	if w.MaxResults != 5 {
		t.Errorf("max_results = %d, want 5", w.MaxResults)
	}
	if w.MaxPageKB != 128 {
		t.Errorf("max_page_kb = %d, want 128", w.MaxPageKB)
	}
	if w.Timeout != "20s" {
		t.Errorf("timeout = %q, want 20s", w.Timeout)
	}
}

func TestEmbeddedProviderFieldsParseFromYAML(t *testing.T) {
	path := writeConfig(t, `
providers:
  embedded:
    type: embedded
    model_path: "~/models/model.gguf"
    mmproj_path: "~/models/mmproj-model.gguf"
    library_path: "/opt/llama/lib"
    context_size: 8192
    gpu_layers: 0
    threads: 4
    batch_size: 512
    chat_template: "custom template"
    tool_format: gemma
    sampling:
      top_k: 50
      min_p: 0.1
      repeat_penalty: 1.2
      repeat_last_n: 128
      seed: 42
      stop: ["</s>", "STOP"]
`)
	v, err := NewViper(path)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pc, ok := cfg.Providers["embedded"]
	if !ok {
		t.Fatal("embedded provider missing from parsed config")
	}
	if pc.Type != "embedded" {
		t.Errorf("Type = %q, want embedded", pc.Type)
	}
	if pc.ModelPath != "~/models/model.gguf" {
		t.Errorf("ModelPath = %q", pc.ModelPath)
	}
	if pc.MMProjPath != "~/models/mmproj-model.gguf" {
		t.Errorf("MMProjPath = %q", pc.MMProjPath)
	}
	if pc.LibraryPath != "/opt/llama/lib" {
		t.Errorf("LibraryPath = %q", pc.LibraryPath)
	}
	if pc.ContextSize != 8192 {
		t.Errorf("ContextSize = %d, want 8192", pc.ContextSize)
	}
	if pc.GPULayers == nil || *pc.GPULayers != 0 {
		t.Errorf("GPULayers = %v, want pointer to 0", pc.GPULayers)
	}
	if pc.Threads != 4 {
		t.Errorf("Threads = %d, want 4", pc.Threads)
	}
	if pc.BatchSize != 512 {
		t.Errorf("BatchSize = %d, want 512", pc.BatchSize)
	}
	if pc.ChatTemplate != "custom template" {
		t.Errorf("ChatTemplate = %q", pc.ChatTemplate)
	}
	if pc.ToolFormat != "gemma" {
		t.Errorf("ToolFormat = %q, want gemma", pc.ToolFormat)
	}
	if pc.Sampling == nil {
		t.Fatal("Sampling should not be nil")
	}
	if pc.Sampling.TopK != 50 || pc.Sampling.MinP != 0.1 || pc.Sampling.RepeatPenalty != 1.2 ||
		pc.Sampling.RepeatLastN != 128 || pc.Sampling.Seed != 42 {
		t.Errorf("Sampling = %+v", pc.Sampling)
	}
	if len(pc.Sampling.Stop) != 2 || pc.Sampling.Stop[0] != "</s>" || pc.Sampling.Stop[1] != "STOP" {
		t.Errorf("Sampling.Stop = %v", pc.Sampling.Stop)
	}
}

func TestEmbeddedBuiltinProviderExistsWithZeroConfig(t *testing.T) {
	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pc, ok := cfg.Providers["embedded"]
	if !ok {
		t.Fatal("builtin embedded provider missing")
	}
	if pc.Type != "embedded" {
		t.Errorf("Type = %q, want embedded", pc.Type)
	}
	// The embedded provider must not be active by default.
	if cfg.DefaultProvider == "embedded" {
		t.Error("embedded must not be the default provider")
	}
}

func TestContextSizeAndGPULayersEnvOverrides(t *testing.T) {
	t.Setenv("LLMTUI_CONTEXT_SIZE", "16384")
	t.Setenv("LLMTUI_GPU_LAYERS", "0")

	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContextSize == nil || *cfg.ContextSize != 16384 {
		t.Errorf("ContextSize = %v, want pointer to 16384", cfg.ContextSize)
	}
	if cfg.GPULayers == nil || *cfg.GPULayers != 0 {
		t.Errorf("GPULayers = %v, want pointer to 0", cfg.GPULayers)
	}
}

func TestContextSizeAndGPULayersFlagOverridesEnv(t *testing.T) {
	t.Setenv("LLMTUI_CONTEXT_SIZE", "16384")
	t.Setenv("LLMTUI_GPU_LAYERS", "0")

	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	// Simulate bound flags: viper.Set has flag-level (highest) precedence.
	v.Set("context_size", 4096)
	v.Set("gpu_layers", 20)

	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContextSize == nil || *cfg.ContextSize != 4096 {
		t.Errorf("ContextSize = %v, want pointer to 4096 (flag should win over env)", cfg.ContextSize)
	}
	if cfg.GPULayers == nil || *cfg.GPULayers != 20 {
		t.Errorf("GPULayers = %v, want pointer to 20 (flag should win over env)", cfg.GPULayers)
	}
}

func TestContextSizeAndGPULayersUnsetStayNil(t *testing.T) {
	v, err := NewViper(filepath.Join(t.TempDir(), "missing.yaml"))
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ContextSize != nil {
		t.Errorf("ContextSize = %v, want nil when unset", cfg.ContextSize)
	}
	if cfg.GPULayers != nil {
		t.Errorf("GPULayers = %v, want nil when unset", cfg.GPULayers)
	}
}

func TestGPULayersNilVsZeroDistinguished(t *testing.T) {
	// A provider config that never mentions gpu_layers must decode to a nil
	// pointer (meaning "auto/all"), not a pointer to the zero value 0
	// (meaning "CPU only") — these have opposite runtime meanings.
	pathAuto := writeConfig(t, `
providers:
  embedded:
    type: embedded
    model_path: "/models/a.gguf"
`)
	vAuto, err := NewViper(pathAuto)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfgAuto, err := Load(vAuto)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if pc := cfgAuto.Providers["embedded"]; pc.GPULayers != nil {
		t.Errorf("GPULayers = %v, want nil (auto) when omitted from YAML", pc.GPULayers)
	}

	pathCPU := writeConfig(t, `
providers:
  embedded:
    type: embedded
    model_path: "/models/a.gguf"
    gpu_layers: 0
`)
	vCPU, err := NewViper(pathCPU)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfgCPU, err := Load(vCPU)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	pc := cfgCPU.Providers["embedded"]
	if pc.GPULayers == nil {
		t.Fatal("GPULayers = nil, want pointer to 0 (explicit CPU-only) when set in YAML")
	}
	if *pc.GPULayers != 0 {
		t.Errorf("GPULayers = %d, want 0", *pc.GPULayers)
	}
}

// TestOldConfigFileStillParsesIdentically is a regression guard: config
// files written before the embedded provider fields existed must still
// parse with identical values for the pre-existing fields, and the new
// fields must all be their Go zero values (empty/nil), never accidentally
// populated by cross-talk between providers or defaults.
func TestOldConfigFileStillParsesIdentically(t *testing.T) {
	path := writeConfig(t, `
default_provider: lmstudio
default_model: qwen3

providers:
  ollama:
    type: ollama
    base_url: http://localhost:11434
    api_key: ""
    default_model: qwen3

  lmstudio:
    type: openai_compatible
    base_url: http://localhost:1234/v1
    api_key: lm-studio
    default_model: local-model

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
	ollama := cfg.Providers["ollama"]
	if ollama.BaseURL != "http://localhost:11434" || ollama.DefaultModel != "qwen3" {
		t.Errorf("ollama provider = %+v, unexpected values", ollama)
	}
	if ollama.ModelPath != "" || ollama.MMProjPath != "" || ollama.LibraryPath != "" || ollama.ContextSize != 0 ||
		ollama.GPULayers != nil || ollama.Threads != 0 || ollama.BatchSize != 0 ||
		ollama.ChatTemplate != "" || ollama.ToolFormat != "" || ollama.Sampling != nil {
		t.Errorf("ollama provider should have all-zero embedded-only fields, got %+v", ollama)
	}
	lmstudio := cfg.Providers["lmstudio"]
	if lmstudio.APIKey != "lm-studio" || lmstudio.DefaultModel != "local-model" {
		t.Errorf("lmstudio provider = %+v, unexpected values", lmstudio)
	}
	if cfg.ContextSize != nil || cfg.GPULayers != nil {
		t.Errorf("top-level runtime overrides should be nil for an old config with no flags/env, got context_size=%v gpu_layers=%v", cfg.ContextSize, cfg.GPULayers)
	}
}

func TestEmbeddedMemoryOptionsParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
providers:
  embedded:
    type: embedded
    model_path: /tmp/m.gguf
    context_size: 32768
    swa_full: true
    kv_cache_type: q8_0
    flash_attention: "off"
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewViper(path)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	pc := cfg.Providers["embedded"]
	if !pc.SWAFull {
		t.Errorf("SWAFull = %v, want true", pc.SWAFull)
	}
	if pc.KVCacheType != "q8_0" {
		t.Errorf("KVCacheType = %q, want q8_0", pc.KVCacheType)
	}
	if pc.FlashAttention != "off" {
		t.Errorf("FlashAttention = %q, want off", pc.FlashAttention)
	}
	if pc.ContextSize != 32768 {
		t.Errorf("ContextSize = %d, want 32768", pc.ContextSize)
	}
}

func TestEmbeddedMemoryOptionsDefaultUnset(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := `
providers:
  embedded:
    type: embedded
    model_path: /tmp/m.gguf
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	v, err := NewViper(path)
	if err != nil {
		t.Fatalf("NewViper: %v", err)
	}
	cfg, err := Load(v)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	pc := cfg.Providers["embedded"]
	if pc.SWAFull {
		t.Errorf("SWAFull = %v, want false (unset)", pc.SWAFull)
	}
	if pc.KVCacheType != "" || pc.FlashAttention != "" {
		t.Errorf("KVCacheType/FlashAttention = %q/%q, want empty", pc.KVCacheType, pc.FlashAttention)
	}
}
