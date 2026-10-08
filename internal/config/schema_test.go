package config

import (
	"strings"
	"testing"
	"time"
)

func schemaByKey(t *testing.T) map[string]SchemaField {
	t.Helper()
	byKey := map[string]SchemaField{}
	for _, f := range Schema() {
		if _, dup := byKey[f.Key]; dup {
			t.Fatalf("duplicate schema key %q", f.Key)
		}
		byKey[f.Key] = f
	}
	return byKey
}

func TestSchemaDescribesKeysTypesAndDefaults(t *testing.T) {
	byKey := schemaByKey(t)
	tests := []struct {
		key, typ string
		def      any
	}{
		{"default_provider", "string", "ollama"},
		{"chat.temperature", "float", 0.7},
		{"chat.max_tokens", "int", 4096},
		{"chat.stream", "bool", true},
		{"cache.ttl", "duration", "24h"},
		{"rag.workspace.include", "list", nil},
		{"providers.*.base_url", "string", nil},
		{"mcp.servers.*.env", "map", nil},
		{"model_profiles.*.match", "list", nil},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			f, ok := byKey[tt.key]
			if !ok {
				t.Fatalf("schema has no %q", tt.key)
			}
			if f.Type != tt.typ {
				t.Errorf("type = %q, want %q", f.Type, tt.typ)
			}
			if tt.def != nil && f.Default != tt.def {
				t.Errorf("default = %#v, want %#v", f.Default, tt.def)
			}
		})
	}
}

func TestSchemaOmitsRuntimeOnlyKeysAndMarksSecrets(t *testing.T) {
	byKey := schemaByKey(t)
	for key := range runtimeOnlyKeys {
		if _, ok := byKey[key]; ok {
			t.Errorf("runtime-only key %q is in the schema", key)
		}
	}
	for _, key := range []string{"providers.*.api_key", "mcp.servers.*.env"} {
		if !byKey[key].Secret {
			t.Errorf("%q is not marked secret", key)
		}
	}
	if byKey["providers.*.api_key_env"].Secret {
		t.Error("api_key_env names a variable and is not itself a secret")
	}
}

func TestSchemaEnumsAndDurationsAreConsistent(t *testing.T) {
	byKey := schemaByKey(t)
	for key, values := range schemaEnums {
		f, ok := byKey[key]
		if !ok {
			t.Errorf("enum for unknown key %q", key)
			continue
		}
		if def, ok := f.Default.(string); ok && def != "" && !containsString(values, def) {
			t.Errorf("%s default %q is not among its values %v", key, def, values)
		}
	}
	for _, f := range byKey {
		if f.Type != "duration" || f.Default == nil {
			continue
		}
		if _, err := time.ParseDuration(f.Default.(string)); err != nil {
			t.Errorf("%s default %q does not parse: %v", f.Key, f.Default, err)
		}
	}
	// The enum literals mirror config's own resolvers.
	for _, mode := range schemaEnums["agent.verifier.mode"] {
		if got := (AgentVerifierConfig{Mode: mode, Enabled: true}).ResolvedMode(); got != mode {
			t.Errorf("verifier mode %q resolves to %q", mode, got)
		}
	}
	for _, mode := range schemaEnums["decision_engine.mode"] {
		if got := (DecisionEngineConfig{Mode: mode}).ResolvedMode(); got != mode {
			t.Errorf("decision engine mode %q resolves to %q", mode, got)
		}
	}
}

func TestCheckReportsProblemsLoadAccepts(t *testing.T) {
	base := func() *Config {
		return &Config{
			DefaultProvider: "lmstudio",
			Providers:       map[string]ProviderConfig{"lmstudio": {Type: "openai_compatible"}},
			Chat:            ChatConfig{Reasoning: "auto"},
			Cache:           CacheConfig{TTL: "24h"},
		}
	}
	tests := []struct {
		name    string
		mutate  func(*Config)
		keys    []string
		wantKey string
		wantErr bool
	}{
		{"missing default provider", func(c *Config) { c.DefaultProvider = "mlxserve" }, nil, "default_provider", true},
		{"unknown provider type", func(c *Config) { c.Providers["odd"] = ProviderConfig{Type: "openai"} }, nil, "providers.odd.type", true},
		{"value outside enum", func(c *Config) { c.Chat.Reasoning = "maybe" }, nil, "chat.reasoning", false},
		{"bad duration", func(c *Config) { c.Cache.TTL = "forever" }, nil, "cache.ttl", false},
		{"unknown key", func(*Config) {}, []string{"chat.temprature"}, "chat.temprature", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base()
			tt.mutate(cfg)
			problems := Check(cfg, tt.keys)
			if len(problems) != 1 {
				t.Fatalf("problems = %+v, want one for %s", problems, tt.wantKey)
			}
			if problems[0].Key != tt.wantKey || problems[0].Error != tt.wantErr {
				t.Errorf("problem = %+v, want key %s error %v", problems[0], tt.wantKey, tt.wantErr)
			}
		})
	}
}

func TestCheckAcceptsKnownAndMapKeys(t *testing.T) {
	cfg := &Config{
		DefaultProvider: "lmstudio",
		Providers:       map[string]ProviderConfig{"lmstudio": {Type: "openai_compatible"}},
	}
	keys := []string{
		"default_provider", "providers.lmstudio.type", "providers.lmstudio.sampling.top_k",
		"mcp.servers.files.env.token", "chat.temperature", "api_key", "model",
	}
	if problems := Check(cfg, keys); len(problems) != 0 {
		t.Fatalf("unexpected problems: %+v", problems)
	}
	if got := schemaPattern("mcp.servers.files.env.token", schemaByKey(t)); !strings.HasPrefix(got, "mcp.servers.*.env") {
		t.Errorf("env key maps to %q", got)
	}
}
