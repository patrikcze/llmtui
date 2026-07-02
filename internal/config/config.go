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

// Config is the fully merged configuration plus runtime overrides.
type Config struct {
	DefaultProvider string                    `mapstructure:"default_provider" yaml:"default_provider"`
	DefaultModel    string                    `mapstructure:"default_model" yaml:"default_model"`
	Providers       map[string]ProviderConfig `mapstructure:"providers" yaml:"providers"`
	Chat            ChatConfig                `mapstructure:"chat" yaml:"chat"`
	UI              UIConfig                  `mapstructure:"ui" yaml:"ui"`
	Privacy         PrivacyConfig             `mapstructure:"privacy" yaml:"privacy"`

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

	// Override keys have no defaults, so Unmarshal only sees their env
	// values (LLMTUI_PROVIDER, LLMTUI_MODEL, ...) with an explicit binding.
	for _, key := range []string{"provider", "model", "base_url", "api_key", "no_stream", "debug"} {
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

// Redact masks a secret for safe display.
func Redact(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "****"
	}
	return s[:2] + strings.Repeat("*", 6) + s[len(s)-2:]
}
