// Package app wires configuration to concrete provider implementations.
package app

import (
	"fmt"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
	"github.com/patrikcze/llmtui/internal/provider/ollama"
	"github.com/patrikcze/llmtui/internal/provider/openai"
)

// BuildProvider constructs a provider from its configuration.
func BuildProvider(name string, pc config.ProviderConfig) (provider.Provider, error) {
	switch pc.Type {
	case "ollama":
		return ollama.New(pc.BaseURL), nil
	case "openai_compatible":
		return openai.New(name, pc.BaseURL, pc.ResolveAPIKey()), nil
	case "mock":
		return mock.New(), nil
	default:
		return nil, fmt.Errorf("unknown provider type %q for provider %q", pc.Type, name)
	}
}

// BuildActiveProvider resolves and constructs the currently active provider.
func BuildActiveProvider(cfg *config.Config) (provider.Provider, error) {
	name, pc, ok := cfg.ActiveProvider()
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", name)
	}
	return BuildProvider(name, pc)
}
