// Package app wires configuration to concrete provider implementations.
package app

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/mock"
	"github.com/patrikcze/llmtui/internal/provider/ollama"
	"github.com/patrikcze/llmtui/internal/provider/openai"
)

// httpClient builds a client with a connect timeout but no overall request
// timeout — streams are long-lived; request deadlines come from contexts.
func httpClient(netCfg config.NetworkConfig) *http.Client {
	connectTimeout := 10 * time.Second
	if d, err := time.ParseDuration(netCfg.ConnectTimeout); err == nil && d > 0 {
		connectTimeout = d
	}
	return &http.Client{
		Transport: &http.Transport{
			DialContext:         (&net.Dialer{Timeout: connectTimeout}).DialContext,
			TLSHandshakeTimeout: connectTimeout,
		},
	}
}

// BuildProvider constructs a provider from its configuration.
func BuildProvider(name string, pc config.ProviderConfig, netCfg config.NetworkConfig) (provider.Provider, error) {
	client := httpClient(netCfg)
	switch pc.Type {
	case "ollama":
		return ollama.New(pc.BaseURL, ollama.WithHTTPClient(client), ollama.WithName(name)), nil
	case "openai_compatible":
		return openai.New(name, pc.BaseURL, pc.ResolveAPIKey(), openai.WithHTTPClient(client)), nil
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
	return BuildProvider(name, pc, cfg.Network)
}

// RequestTimeout parses the configured overall request timeout.
func RequestTimeout(netCfg config.NetworkConfig) time.Duration {
	if d, err := time.ParseDuration(netCfg.Timeout); err == nil && d > 0 {
		return d
	}
	return 120 * time.Second
}

// RetryBackoff parses the configured retry backoff.
func RetryBackoff(netCfg config.NetworkConfig) time.Duration {
	if d, err := time.ParseDuration(netCfg.Retry.Backoff); err == nil && d > 0 {
		return d
	}
	return 750 * time.Millisecond
}
