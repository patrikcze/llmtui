// Package config loads llmtui configuration with the precedence
// CLI flags > environment variables > YAML config file > built-in defaults.
package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/spf13/viper"
)

// EnvPrefix is the prefix for all llmtui environment variables.
const EnvPrefix = "LLMTUI"

// ProviderConfig describes one configured backend.
type ProviderConfig struct {
	Type         string `mapstructure:"type" yaml:"type"`
	BaseURL      string `mapstructure:"base_url" yaml:"base_url"`
	APIKey       string `mapstructure:"api_key" yaml:"api_key"`
	APIKeyEnv    string `mapstructure:"api_key_env" yaml:"api_key_env"`
	DefaultModel string `mapstructure:"default_model" yaml:"default_model"`
}

// ResolveAPIKey returns the API key, preferring an env var reference so
// secrets never have to live in the YAML file.
func (p ProviderConfig) ResolveAPIKey() string {
	if p.APIKeyEnv != "" {
		if v := os.Getenv(p.APIKeyEnv); v != "" {
			return v
		}
	}
	return p.APIKey
}

// ChatConfig holds generation and history settings.
type ChatConfig struct {
	SystemPrompt string  `mapstructure:"system_prompt" yaml:"system_prompt"`
	Temperature  float64 `mapstructure:"temperature" yaml:"temperature"`
	TopP         float64 `mapstructure:"top_p" yaml:"top_p"`
	MaxTokens    int     `mapstructure:"max_tokens" yaml:"max_tokens"`
	Stream       bool    `mapstructure:"stream" yaml:"stream"`
	SaveHistory  bool    `mapstructure:"save_history" yaml:"save_history"`
	HistoryDir   string  `mapstructure:"history_dir" yaml:"history_dir"`
	// ForceVision allows image attachments even when the model ID is not
	// recognized as a vision model by the built-in heuristic.
	ForceVision bool `mapstructure:"force_vision" yaml:"force_vision"`
	// ModelProfile pins a model profile ("auto" matches by model ID).
	ModelProfile string `mapstructure:"model_profile" yaml:"model_profile"`
}

// UIConfig holds appearance settings.
type UIConfig struct {
	Theme          string `mapstructure:"theme" yaml:"theme"`
	UseNerdFont    string `mapstructure:"use_nerd_font" yaml:"use_nerd_font"`
	Animations     bool   `mapstructure:"animations" yaml:"animations"`
	ShowUsageChart bool   `mapstructure:"show_usage_chart" yaml:"show_usage_chart"`
	ShowTokenStats bool   `mapstructure:"show_token_stats" yaml:"show_token_stats"`
	Markdown       bool   `mapstructure:"markdown" yaml:"markdown"`
	CompactMode    bool   `mapstructure:"compact_mode" yaml:"compact_mode"`
}

// PrivacyConfig holds local-first privacy settings.
type PrivacyConfig struct {
	LocalFirst          bool `mapstructure:"local_first" yaml:"local_first"`
	RedactAPIKeysInLogs bool `mapstructure:"redact_api_keys_in_logs" yaml:"redact_api_keys_in_logs"`
	StorePrompts        bool `mapstructure:"store_prompts" yaml:"store_prompts"`
}

// CacheConfig configures the local response cache.
type CacheConfig struct {
	Enabled                bool   `mapstructure:"enabled" yaml:"enabled"`
	Path                   string `mapstructure:"path" yaml:"path"`
	TTL                    string `mapstructure:"ttl" yaml:"ttl"`
	MaxSizeMB              int    `mapstructure:"max_size_mb" yaml:"max_size_mb"`
	CacheStreamedResponses bool   `mapstructure:"cache_streamed_responses" yaml:"cache_streamed_responses"`
}

// MemoryConfig configures local memory snippets (disabled by default).
type MemoryConfig struct {
	Enabled     bool   `mapstructure:"enabled" yaml:"enabled"`
	Path        string `mapstructure:"path" yaml:"path"`
	MaxSnippets int    `mapstructure:"max_snippets" yaml:"max_snippets"`
	AutoExtract bool   `mapstructure:"auto_extract" yaml:"auto_extract"`
}

// PromptConfig configures prompt composition.
type PromptConfig struct {
	Mode                   string `mapstructure:"mode" yaml:"mode"`
	ShowDebug              bool   `mapstructure:"show_debug" yaml:"show_debug"`
	IncludeSessionSummary  bool   `mapstructure:"include_session_summary" yaml:"include_session_summary"`
	IncludeLocalMemory     bool   `mapstructure:"include_local_memory" yaml:"include_local_memory"`
	IncludeModelHints      bool   `mapstructure:"include_model_hints" yaml:"include_model_hints"`
	IncludeFormattingHints bool   `mapstructure:"include_formatting_hints" yaml:"include_formatting_hints"`
	HelperText             string `mapstructure:"helper_text" yaml:"helper_text,omitempty"`
}

// ContextConfig configures context-window management.
type ContextConfig struct {
	Strategy               string `mapstructure:"strategy" yaml:"strategy"`
	MaxContextTokens       int    `mapstructure:"max_context_tokens" yaml:"max_context_tokens"` // 0 = auto from profile
	ReserveResponseTokens  int    `mapstructure:"reserve_response_tokens" yaml:"reserve_response_tokens"`
	SummarizeAfterMessages int    `mapstructure:"summarize_after_messages" yaml:"summarize_after_messages"`
	KeepLastMessages       int    `mapstructure:"keep_last_messages" yaml:"keep_last_messages"`
	SummaryMaxTokens       int    `mapstructure:"summary_max_tokens" yaml:"summary_max_tokens"`
}

// ToolsConfig configures workspace tools for the chat (the model can list,
// read, and write files and run commands under the directory llmtui was
// started from).
type ToolsConfig struct {
	Enabled       bool `mapstructure:"enabled" yaml:"enabled"`
	MaxIterations int  `mapstructure:"max_iterations" yaml:"max_iterations"`
	MaxFileKB     int  `mapstructure:"max_file_kb" yaml:"max_file_kb"`
	// Approve gates mutating actions (writes, non-read-only commands):
	// "ask" prompts in the TUI, "auto" runs them without asking.
	Approve        string `mapstructure:"approve" yaml:"approve"`
	CommandTimeout string `mapstructure:"command_timeout" yaml:"command_timeout"`
}

// RetryConfig configures request retries.
type RetryConfig struct {
	Enabled     bool   `mapstructure:"enabled" yaml:"enabled"`
	MaxAttempts int    `mapstructure:"max_attempts" yaml:"max_attempts"`
	Backoff     string `mapstructure:"backoff" yaml:"backoff"`
}

// NetworkConfig configures timeouts and retries.
type NetworkConfig struct {
	Timeout        string      `mapstructure:"timeout" yaml:"timeout"`
	ConnectTimeout string      `mapstructure:"connect_timeout" yaml:"connect_timeout"`
	Retry          RetryConfig `mapstructure:"retry" yaml:"retry"`
}

// TemplateConfig is one reusable conversation template.
type TemplateConfig struct {
	Description  string  `mapstructure:"description" yaml:"description"`
	SystemPrompt string  `mapstructure:"system_prompt" yaml:"system_prompt"`
	PromptMode   string  `mapstructure:"prompt_mode" yaml:"prompt_mode"`
	Temperature  float64 `mapstructure:"temperature" yaml:"temperature"`
}

// ModelProfileConfig is one user-defined model profile.
type ModelProfileConfig struct {
	Match                []string `mapstructure:"match" yaml:"match"`
	ContextWindow        int      `mapstructure:"context_window" yaml:"context_window"`
	PreferredTemperature float64  `mapstructure:"preferred_temperature" yaml:"preferred_temperature"`
	SupportsJSONMode     bool     `mapstructure:"supports_json_mode" yaml:"supports_json_mode"`
	PromptStyle          string   `mapstructure:"prompt_style" yaml:"prompt_style"`
	ReasoningHint        bool     `mapstructure:"reasoning_hint" yaml:"reasoning_hint"`
}

// Config is the fully merged configuration plus runtime overrides.
type Config struct {
	DefaultProvider string                        `mapstructure:"default_provider" yaml:"default_provider"`
	DefaultModel    string                        `mapstructure:"default_model" yaml:"default_model"`
	Providers       map[string]ProviderConfig     `mapstructure:"providers" yaml:"providers"`
	Chat            ChatConfig                    `mapstructure:"chat" yaml:"chat"`
	UI              UIConfig                      `mapstructure:"ui" yaml:"ui"`
	Privacy         PrivacyConfig                 `mapstructure:"privacy" yaml:"privacy"`
	Cache           CacheConfig                   `mapstructure:"cache" yaml:"cache"`
	Memory          MemoryConfig                  `mapstructure:"memory" yaml:"memory"`
	Prompt          PromptConfig                  `mapstructure:"prompt" yaml:"prompt"`
	Context         ContextConfig                 `mapstructure:"context" yaml:"context"`
	Tools           ToolsConfig                   `mapstructure:"tools" yaml:"tools"`
	Network         NetworkConfig                 `mapstructure:"network" yaml:"network"`
	Templates       map[string]TemplateConfig     `mapstructure:"templates" yaml:"templates,omitempty"`
	ModelProfiles   map[string]ModelProfileConfig `mapstructure:"model_profiles" yaml:"model_profiles,omitempty"`

	// Runtime overrides sourced from flags/env (not persisted to YAML).
	Provider string `mapstructure:"provider" yaml:"-"`
	Model    string `mapstructure:"model" yaml:"-"`
	BaseURL  string `mapstructure:"base_url" yaml:"-"`
	APIKey   string `mapstructure:"api_key" yaml:"-"`
	Debug    bool   `mapstructure:"debug" yaml:"-"`
	NoStream bool   `mapstructure:"no_stream" yaml:"-"`
}

// ActiveProviderName resolves which provider to use, applying overrides.
func (c *Config) ActiveProviderName() string {
	if c.Provider != "" {
		return c.Provider
	}
	return c.DefaultProvider
}

// ActiveProvider returns the effective provider config with base URL, API key
// and model overrides applied. ok is false when the name is not configured.
func (c *Config) ActiveProvider() (name string, pc ProviderConfig, ok bool) {
	name = c.ActiveProviderName()
	pc, ok = c.Providers[name]
	if !ok {
		return name, pc, false
	}
	if c.BaseURL != "" {
		pc.BaseURL = c.BaseURL
	}
	if c.APIKey != "" {
		pc.APIKey = c.APIKey
		pc.APIKeyEnv = ""
	}
	return name, pc, true
}

// ActiveBaseURL returns the effective base URL of the active provider.
func (c *Config) ActiveBaseURL() string {
	if _, pc, ok := c.ActiveProvider(); ok {
		return pc.BaseURL
	}
	return ""
}

// ActiveModel resolves the model with precedence:
// --model flag/env > provider default_model > global default_model.
func (c *Config) ActiveModel() string {
	if c.Model != "" {
		return c.Model
	}
	if _, pc, ok := c.ActiveProvider(); ok && pc.DefaultModel != "" {
		return pc.DefaultModel
	}
	return c.DefaultModel
}

// StreamEnabled resolves streaming, honoring the --no-stream override.
func (c *Config) StreamEnabled() bool {
	if c.NoStream {
		return false
	}
	return c.Chat.Stream
}

// Dir returns the directory holding the config file.
func Dir() (string, error) {
	if runtime.GOOS == "windows" {
		appData := os.Getenv("APPDATA")
		if appData == "" {
			return "", fmt.Errorf("APPDATA is not set")
		}
		return filepath.Join(appData, "llmtui"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".config", "llmtui"), nil
}

// DefaultPath returns the default config file path.
func DefaultPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// NewViper builds a viper instance with defaults, env binding, and the config
// file wired up. cfgFile may be empty to use the default location.
func NewViper(cfgFile string) (*viper.Viper, error) {
	v := viper.New()
	setDefaults(v)

	v.SetEnvPrefix(EnvPrefix)
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	// Explicit env bindings. Override keys (provider, model, …) have no
	// defaults, so Unmarshal only sees them when bound. Nested keys are bound
	// too so common tuning works without a config file, e.g.
	// LLMTUI_NETWORK_TIMEOUT=600s or LLMTUI_CHAT_MAX_TOKENS=8192.
	for _, key := range []string{
		"provider", "model", "base_url", "api_key", "no_stream", "debug",
		"network.timeout", "network.connect_timeout",
		"chat.max_tokens", "chat.temperature", "chat.top_p", "chat.system_prompt",
	} {
		if err := v.BindEnv(key); err != nil {
			return nil, fmt.Errorf("bind env %s: %w", key, err)
		}
	}

	if cfgFile != "" {
		v.SetConfigFile(cfgFile)
	} else {
		path, err := DefaultPath()
		if err != nil {
			return nil, err
		}
		v.SetConfigFile(path)
	}

	if err := v.ReadInConfig(); err != nil {
		// A missing config file is fine; defaults and env still apply.
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return nil, fmt.Errorf("read config: %w", err)
		}
	}
	return v, nil
}

// Load unmarshals the merged configuration.
func Load(v *viper.Viper) (*Config, error) {
	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if cfg.Providers == nil {
		cfg.Providers = map[string]ProviderConfig{}
	}
	// Guarantee built-in providers exist so the app always runs.
	for name, pc := range builtinProviders() {
		if _, exists := cfg.Providers[name]; !exists {
			cfg.Providers[name] = pc
		}
	}
	return &cfg, nil
}

func builtinProviders() map[string]ProviderConfig {
	return map[string]ProviderConfig{
		"ollama": {
			Type:         "ollama",
			BaseURL:      "http://localhost:11434",
			DefaultModel: "qwen3",
		},
		"lmstudio": {
			Type:         "openai_compatible",
			BaseURL:      "http://localhost:1234/v1",
			APIKey:       "lm-studio",
			DefaultModel: "local-model",
		},
		"openai_compatible": {
			Type:         "openai_compatible",
			BaseURL:      "http://localhost:8080/v1",
			DefaultModel: "local-model",
		},
		"mock": {
			Type:         "mock",
			DefaultModel: "demo-model",
		},
	}
}

func setDefaults(v *viper.Viper) {
	v.SetDefault("default_provider", "ollama")
	v.SetDefault("default_model", "qwen3")

	v.SetDefault("chat.system_prompt", "You are a helpful local assistant.")
	v.SetDefault("chat.temperature", 0.7)
	v.SetDefault("chat.top_p", 0.9)
	v.SetDefault("chat.max_tokens", 4096)
	v.SetDefault("chat.stream", true)
	v.SetDefault("chat.force_vision", false)
	v.SetDefault("chat.save_history", true)
	v.SetDefault("chat.history_dir", "~/.local/share/llmtui/history")

	v.SetDefault("ui.theme", "claude_inspired")
	v.SetDefault("ui.use_nerd_font", "auto")
	v.SetDefault("ui.animations", true)
	v.SetDefault("ui.show_usage_chart", true)
	v.SetDefault("ui.show_token_stats", true)
	v.SetDefault("ui.markdown", true)
	v.SetDefault("ui.compact_mode", false)

	v.SetDefault("privacy.local_first", true)
	v.SetDefault("privacy.redact_api_keys_in_logs", true)
	v.SetDefault("privacy.store_prompts", true)

	v.SetDefault("cache.enabled", true)
	v.SetDefault("cache.path", "~/.cache/llmtui/responses")
	v.SetDefault("cache.ttl", "24h")
	v.SetDefault("cache.max_size_mb", 256)
	v.SetDefault("cache.cache_streamed_responses", true)

	v.SetDefault("memory.enabled", false)
	v.SetDefault("memory.path", "~/.local/share/llmtui/memory.yaml")
	v.SetDefault("memory.max_snippets", 100)
	v.SetDefault("memory.auto_extract", false)

	v.SetDefault("prompt.mode", "balanced")
	v.SetDefault("prompt.show_debug", false)
	v.SetDefault("prompt.include_session_summary", true)
	v.SetDefault("prompt.include_local_memory", true)
	v.SetDefault("prompt.include_model_hints", true)
	v.SetDefault("prompt.include_formatting_hints", true)

	v.SetDefault("context.strategy", "auto")
	v.SetDefault("context.max_context_tokens", 0)
	v.SetDefault("context.reserve_response_tokens", 2048)
	v.SetDefault("context.summarize_after_messages", 12)
	v.SetDefault("context.keep_last_messages", 8)
	v.SetDefault("context.summary_max_tokens", 1200)

	v.SetDefault("tools.enabled", false)
	v.SetDefault("tools.max_iterations", 4)
	v.SetDefault("tools.max_file_kb", 512)
	v.SetDefault("tools.approve", "ask")
	v.SetDefault("tools.command_timeout", "30s")

	v.SetDefault("network.timeout", "120s")
	v.SetDefault("network.connect_timeout", "10s")
	v.SetDefault("network.retry.enabled", true)
	v.SetDefault("network.retry.max_attempts", 2)
	v.SetDefault("network.retry.backoff", "750ms")
}

// DefaultYAML is the annotated config written by `llmtui config init`.
const DefaultYAML = `# llmtui configuration
default_provider: ollama
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

  openai_compatible:
    type: openai_compatible
    base_url: http://localhost:8080/v1
    # Prefer api_key_env over writing secrets into this file:
    # api_key_env: LLMTUI_API_KEY
    api_key: ""
    default_model: local-model

chat:
  system_prompt: "You are a helpful local assistant."
  temperature: 0.7
  top_p: 0.9
  max_tokens: 4096
  stream: true
  # Allow pasting images even when the model is not recognized as vision-capable:
  force_vision: false
  save_history: true
  history_dir: "~/.local/share/llmtui/history"

ui:
  theme: claude_inspired
  use_nerd_font: auto
  animations: true
  show_usage_chart: true
  show_token_stats: true
  markdown: true
  compact_mode: false

privacy:
  local_first: true
  redact_api_keys_in_logs: true
  store_prompts: true

# Local response cache: repeated prompts answer instantly.
cache:
  enabled: true
  path: "~/.cache/llmtui/responses"
  ttl: "24h"
  max_size_mb: 256
  cache_streamed_responses: true

# Local memory snippets (opt-in; never store secrets here).
memory:
  enabled: false
  path: "~/.local/share/llmtui/memory.yaml"
  max_snippets: 100
  auto_extract: false

# Prompt composition: helpers are visible via /prompt composed.
prompt:
  mode: balanced # minimal | balanced | coding | strict
  show_debug: false
  include_session_summary: true
  include_local_memory: true
  include_model_hints: true
  include_formatting_hints: true

# Context-window management for small local models.
context:
  strategy: auto # none | truncate | summarize | auto
  max_context_tokens: 0 # 0 = from model profile
  reserve_response_tokens: 2048
  summarize_after_messages: 12
  keep_last_messages: 8
  summary_max_tokens: 1200

# Workspace tools: lets the model list, read, and write files and run
# commands under the directory llmtui was started from, via
# "tool <name> <path>" fenced blocks in replies. Off by default — enable
# here or per session with /tools on. Reads and read-only commands (ls,
# grep, git status, …) run automatically; writes and other commands ask
# for your approval first unless approve is set to auto.
tools:
  enabled: false
  approve: ask # ask | auto
  max_iterations: 4 # tool rounds per user message before the loop stops
  max_file_kb: 512  # per-file read/write and command output size cap
  command_timeout: "30s"

network:
  # Inactivity timeout: how long to wait for the *next* streamed token
  # before giving up. It resets on every token and on reasoning activity, so
  # a model that keeps producing output (or is actively thinking) is never
  # cut off — only a genuine stall trips it. Raise it only if your model
  # pauses a long time before its first token (a cold load). You can also set
  # this without editing the file: LLMTUI_NETWORK_TIMEOUT=600s
  timeout: "120s"
  connect_timeout: "10s"
  retry:
    enabled: true
    max_attempts: 2
    backoff: "750ms"

# Conversation templates (/template use <name>).
templates:
  golang:
    description: "Go development assistant"
    system_prompt: "You are an expert Go developer. Prefer idiomatic, tested Go code."
    prompt_mode: coding
    temperature: 0.25
  coding:
    description: "General coding assistant"
    system_prompt: "You are a precise coding assistant. Prefer practical working solutions."
    prompt_mode: coding
    temperature: 0.3

# Custom model profiles are matched before built-ins (/profile list).
model_profiles: {}
`

// WriteDefault writes the default config file, refusing to overwrite.
func WriteDefault(path string) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("config already exists at %s", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(DefaultYAML), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Redact masks a secret for safe display. Short keys are masked entirely:
// revealing 4 characters of a 6-character key is most of the secret.
func Redact(s string) string {
	if s == "" {
		return ""
	}
	if len(s) < 12 {
		return "********"
	}
	return s[:2] + strings.Repeat("*", 6) + s[len(s)-2:]
}
