package config

// EmbeddedKVCacheConfig configures the embedded llama.cpp K/V cache. Empty
// type fields defer to the legacy kv_cache_type setting (or its f16 default).
// Offload is a pointer because llama.cpp's native default must remain intact
// when the key is omitted.
type EmbeddedKVCacheConfig struct {
	TypeK   string `mapstructure:"type_k" yaml:"type_k,omitempty"`
	TypeV   string `mapstructure:"type_v" yaml:"type_v,omitempty"`
	Offload *bool  `mapstructure:"offload" yaml:"offload,omitempty"`
}

// EmbeddedReasoningConfig controls optional template-level reasoning features.
// These settings are intentionally independent from chat.reasoning: the latter
// selects on/off mode for a request, while this block supplies model-template
// defaults and preservation behavior for the embedded runtime.
type EmbeddedReasoningConfig struct {
	Effort   string `mapstructure:"effort" yaml:"effort,omitempty"`
	Preserve bool   `mapstructure:"preserve" yaml:"preserve,omitempty"`
}

// EmbeddedSpeculativeConfig reserves the embedded-only speculative-decoding
// configuration. The runtime validates availability before initialization.
type EmbeddedSpeculativeConfig struct {
	Type      string                 `mapstructure:"type" yaml:"type,omitempty"`
	DraftNMax int                    `mapstructure:"draft_n_max" yaml:"draft_n_max,omitempty"`
	KVCache   *EmbeddedKVCacheConfig `mapstructure:"kv_cache" yaml:"kv_cache,omitempty"`
}
