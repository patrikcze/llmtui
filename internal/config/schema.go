package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// SchemaField describes one key a config.yaml can set, for tools that edit
// the file (the macOS setup app reads it through `llmtui config schema`).
//
// Key is a dotted path; a "*" segment stands for a user-chosen name inside a
// map (providers.*.type). Runtime-only overrides that are never read from
// YAML (provider, model, base_url, api_key, debug, …) are not listed.
type SchemaField struct {
	Key string `json:"key"`
	// Type is bool, int, float, string, duration, list or map. A duration is
	// a string in Go duration syntax ("30s", "24h").
	Type string `json:"type"`
	// Default is llmtui's built-in value, absent when it has none.
	Default any `json:"default,omitempty"`
	// Enum lists the accepted values when the key takes a fixed set.
	Enum []string `json:"enum,omitempty"`
	// Secret marks values that must not be written by an editor or shown
	// (API keys, MCP server environment values); use the *_env indirection.
	Secret bool `json:"secret,omitempty"`
}

// schemaEnums are the accepted values of keys that take a fixed set. They
// are literals because the packages that consume them (prompt, contextmgr,
// tui styles) sit above config; internal/cli's schema test checks each list
// against those packages so the two cannot drift apart.
var schemaEnums = map[string][]string{
	"chat.reasoning":                {"auto", "on", "off", "low", "medium", "high"},
	"ui.theme":                      {"claude_inspired", "midnight", "forest"},
	"entities.output_storage":       {"memory", "disk", "off"},
	"prompt.mode":                   {"minimal", "balanced", "coding", "strict"},
	"prompt.fresh_runtime_context":  {FreshRuntimeContextAuto, FreshRuntimeContextMessage, FreshRuntimeContextSystem},
	"context.strategy":              {"auto", "none", "truncate", "summarize"},
	"agent.verifier.mode":           {VerifierModeOff, VerifierModeDeterministic, VerifierModeAdaptive, VerifierModeAlways},
	"decision_engine.mode":          {DecisionEngineModeShadow, DecisionEngineModeGuardedAssist, DecisionEngineModeCriterionShadow, DecisionEngineModeCriterionAssist},
	"tools.approve":                 {"ask", "auto"},
	"tools.native":                  {"auto", "off"},
	"mcp.servers.*.transport":       {"stdio"},
	"mcp.servers.*.approve":         {"ask", "auto"},
	"model_profiles.*.prompt_style": {"direct", "coding_assistant"},
	"providers.*.type":              {"ollama", "openai_compatible", "embedded", "mock"},
	"templates.*.prompt_mode":       {"minimal", "balanced", "coding", "strict"},
}

// Schema lists every key config.yaml can set, sorted by key, with its type,
// default, accepted values and whether it holds a secret.
func Schema() []SchemaField {
	v := viper.New()
	setDefaults(v)

	var fields []SchemaField
	walkSchema(reflect.TypeOf(Config{}), "", func(key string, t reflect.Type) {
		f := SchemaField{Key: key, Type: schemaType(key, t)}
		if !strings.Contains(key, "*") && v.IsSet(key) {
			if def := v.Get(key); !isEmptyDefault(def) {
				f.Default = def
			}
		}
		f.Enum = schemaEnums[key]
		f.Secret = isSecretKey(key)
		fields = append(fields, f)
	})
	sort.Slice(fields, func(i, j int) bool { return fields[i].Key < fields[j].Key })
	return fields
}

// walkSchema calls leaf for every scalar, list and scalar-map field below t,
// following mapstructure tags and skipping runtime-only (yaml:"-") fields.
func walkSchema(t reflect.Type, prefix string, leaf func(string, reflect.Type)) {
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.Tag.Get("yaml") == "-" {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("mapstructure"), ",")
		if name == "" || name == "-" {
			continue
		}
		key := prefix + name
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.Struct:
			walkSchema(ft, key+".", leaf)
		case reflect.Map:
			el := ft.Elem()
			for el.Kind() == reflect.Pointer {
				el = el.Elem()
			}
			if el.Kind() == reflect.Struct {
				walkSchema(el, key+".*.", leaf)
			} else {
				leaf(key, ft)
			}
		default:
			leaf(key, ft)
		}
	}
}

func schemaType(key string, t reflect.Type) string {
	switch t.Kind() {
	case reflect.Bool:
		return "bool"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "int"
	case reflect.Float32, reflect.Float64:
		return "float"
	case reflect.Slice, reflect.Array:
		return "list"
	case reflect.Map:
		return "map"
	}
	leafName := key[strings.LastIndex(key, ".")+1:]
	switch {
	case leafName == "timeout", strings.HasSuffix(leafName, "_timeout"),
		leafName == "ttl", leafName == "max_elapsed", leafName == "backoff":
		return "duration"
	}
	return "string"
}

func isSecretKey(key string) bool {
	leafName := key[strings.LastIndex(key, ".")+1:]
	return leafName == "api_key" || key == "mcp.servers.*.env"
}

func isEmptyDefault(v any) bool {
	switch x := v.(type) {
	case nil:
		return true
	case string:
		return x == ""
	case []string:
		return len(x) == 0
	}
	return false
}

// Problem is one finding of Check. An error stops llmtui from starting a
// chat or is rejected when used; a warning is a value llmtui ignores or
// replaces with its default.
type Problem struct {
	Key     string `json:"key"`
	Message string `json:"message"`
	Error   bool   `json:"error,omitempty"`
}

// runtimeOnlyKeys are set by flags or LLMTUI_* variables, never by YAML, but
// may appear among a viper's keys.
var runtimeOnlyKeys = map[string]bool{
	"provider": true, "model": true, "base_url": true, "api_key": true,
	"debug": true, "no_stream": true, "context_size": true, "gpu_layers": true,
}

// Check reports problems in a loaded configuration that Load itself accepts
// but llmtui would fail on or silently ignore later: a default_provider with
// no provider block, an unknown provider type, a value outside a key's
// accepted set, a malformed duration, and keys llmtui does not know (usually
// typos). keys are the dotted keys present (viper's AllKeys).
func Check(cfg *Config, keys []string) []Problem {
	var problems []Problem
	add := func(key, msg string, isErr bool) {
		problems = append(problems, Problem{Key: key, Message: msg, Error: isErr})
	}

	if _, ok := cfg.Providers[strings.ToLower(strings.TrimSpace(cfg.DefaultProvider))]; !ok {
		add("default_provider", fmt.Sprintf("provider %q is not defined under providers, so llmtui cannot start a chat", cfg.DefaultProvider), true)
	}

	fields := Schema()
	byKey := make(map[string]SchemaField, len(fields))
	for _, f := range fields {
		byKey[f.Key] = f
	}
	values := flattenSettings(cfg)
	for _, key := range sortedKeys(values) {
		pattern := schemaPattern(key, byKey)
		f, ok := byKey[pattern]
		if !ok {
			continue
		}
		value := values[key]
		if value == "" {
			continue
		}
		if len(f.Enum) > 0 && !containsString(f.Enum, value) {
			add(key, fmt.Sprintf("%q is not one of %s", value, strings.Join(f.Enum, ", ")), pattern == "providers.*.type")
		}
		if f.Type == "duration" {
			if _, err := time.ParseDuration(value); err != nil {
				add(key, fmt.Sprintf("%q is not a duration such as 30s or 5m", value), false)
			}
		}
	}

	sorted := append([]string(nil), keys...)
	sort.Strings(sorted)
	for _, key := range sorted {
		if runtimeOnlyKeys[key] {
			continue
		}
		if _, ok := byKey[schemaPattern(key, byKey)]; !ok {
			add(key, "unknown key; llmtui ignores it", false)
		}
	}
	return problems
}

// schemaPattern maps a concrete key (providers.ollama.type) to its schema
// key (providers.*.type). Keys inside a free-form map field (an MCP server's
// env) map to that field.
func schemaPattern(key string, byKey map[string]SchemaField) string {
	parts := strings.Split(key, ".")
	var build func(i int, acc []string) string
	build = func(i int, acc []string) string {
		candidate := strings.Join(acc, ".")
		if i == len(parts) {
			if _, ok := byKey[candidate]; ok {
				return candidate
			}
			return ""
		}
		if f, ok := byKey[candidate]; ok && f.Type == "map" && len(acc) > 0 {
			return candidate
		}
		if found := build(i+1, append(append([]string(nil), acc...), parts[i])); found != "" {
			return found
		}
		return build(i+1, append(append([]string(nil), acc...), "*"))
	}
	if found := build(0, nil); found != "" {
		return found
	}
	return key
}

// flattenSettings returns the string values of the loaded keys whose
// accepted values Check verifies (enums and durations), including those in
// named maps (providers, MCP servers, model profiles, templates).
func flattenSettings(cfg *Config) map[string]string {
	out := map[string]string{
		"chat.reasoning":                        cfg.Chat.Reasoning,
		"ui.theme":                              cfg.UI.Theme,
		"prompt.mode":                           cfg.Prompt.Mode,
		"prompt.fresh_runtime_context":          cfg.Prompt.FreshRuntimeContext,
		"context.strategy":                      cfg.Context.Strategy,
		"agent.verifier.mode":                   cfg.Agent.Verifier.Mode,
		"decision_engine.mode":                  cfg.DecisionEngine.Mode,
		"tools.approve":                         cfg.Tools.Approve,
		"tools.native":                          cfg.Tools.Native,
		"cache.ttl":                             cfg.Cache.TTL,
		"agent.max_elapsed":                     cfg.Agent.MaxElapsed,
		"agent.verifier.timeout":                cfg.Agent.Verifier.Timeout,
		"tools.command_timeout":                 cfg.Tools.CommandTimeout,
		"tools.web.timeout":                     cfg.Tools.Web.Timeout,
		"network.timeout":                       cfg.Network.Timeout,
		"network.connect_timeout":               cfg.Network.ConnectTimeout,
		"network.retry.backoff":                 cfg.Network.Retry.Backoff,
		"tool_registry.shutdown_timeout":        cfg.ToolRegistry.ShutdownTimeout,
		"personal_apps.limits.read_timeout":     cfg.PersonalApps.Limits.ReadTimeout,
		"personal_apps.limits.mutation_timeout": cfg.PersonalApps.Limits.MutationTimeout,
	}
	for name, p := range cfg.Providers {
		out["providers."+name+".type"] = p.Type
	}
	for name, s := range cfg.MCP.Servers {
		out["mcp.servers."+name+".transport"] = s.Transport
		out["mcp.servers."+name+".approve"] = s.Approve
		out["mcp.servers."+name+".timeout"] = s.Timeout
	}
	for name, p := range cfg.ModelProfiles {
		out["model_profiles."+name+".prompt_style"] = p.PromptStyle
	}
	for name, t := range cfg.Templates {
		out["templates."+name+".prompt_mode"] = t.PromptMode
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func containsString(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}
