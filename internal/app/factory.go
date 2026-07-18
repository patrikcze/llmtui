// Package app wires configuration to concrete provider implementations.
package app

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
	"github.com/patrikcze/llmtui/internal/provider/embedded/llamart"
	"github.com/patrikcze/llmtui/internal/provider/mock"
	"github.com/patrikcze/llmtui/internal/provider/ollama"
	"github.com/patrikcze/llmtui/internal/provider/openai"
)

// ADR-defined sampling defaults for the embedded provider, applied whenever
// a provider config omits the sampling block or leaves a field at its Go
// zero value.
const (
	defaultSamplingTopK          = 40
	defaultSamplingMinP          = 0.05
	defaultSamplingRepeatPenalty = 1.1
	defaultSamplingRepeatLastN   = 64
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

// NewEmbeddedRuntime constructs the native inference runtime backing the
// embedded provider. It remains a package variable so tests can replace the
// native boundary at the single application-level construction point.
var NewEmbeddedRuntime = func() embedded.Runtime { return llamart.New() }

// ActiveOverrides carries runtime overrides (CLI flags, environment
// variables, or an in-session change) that apply on top of a provider's
// static configuration. Only the embedded provider consumes these fields
// today; other provider types ignore them.
type ActiveOverrides struct {
	// Model overrides the model path/name (the --model flag, LLMTUI_MODEL,
	// or a session override). Empty means "no override".
	Model string
	// ContextSize and GPULayers override the embedded provider's context
	// window and GPU offload. nil means "no override".
	ContextSize *int
	GPULayers   *int
}

// BuildProvider constructs a provider from its configuration. overrides is
// optional (variadic so existing call sites that only ever build a single
// named provider — the provider/model picker, /doctor, etc. — need no
// changes); when present, only its first element is used.
func BuildProvider(name string, pc config.ProviderConfig, netCfg config.NetworkConfig, overrides ...ActiveOverrides) (provider.Provider, error) {
	var ov ActiveOverrides
	if len(overrides) > 0 {
		ov = overrides[0]
	}
	client := httpClient(netCfg)
	switch pc.Type {
	case "ollama":
		return ollama.New(pc.BaseURL, ollama.WithHTTPClient(client), ollama.WithName(name)), nil
	case "openai_compatible":
		return openai.New(name, pc.BaseURL, pc.ResolveAPIKey(), openai.WithHTTPClient(client)), nil
	case "mock":
		return mock.New(), nil
	case "embedded":
		opts, err := buildEmbeddedOptions(pc, ov)
		if err != nil {
			return nil, fmt.Errorf("configure embedded provider %q: %w", name, err)
		}
		return embedded.New(name, opts, NewEmbeddedRuntime), nil
	default:
		return nil, fmt.Errorf("unknown provider type %q for provider %q", pc.Type, name)
	}
}

// BuildActiveProvider resolves and constructs the currently active provider,
// applying the current run's flag/env/session overrides (model, context
// size, GPU layers) on top of its static configuration.
func BuildActiveProvider(cfg *config.Config) (provider.Provider, error) {
	name, pc, ok := cfg.ActiveProvider()
	if !ok {
		return nil, fmt.Errorf("provider %q is not configured", name)
	}
	ov := ActiveOverrides{
		Model:       cfg.Model,
		ContextSize: cfg.ContextSize,
		GPULayers:   cfg.GPULayers,
	}
	return BuildProvider(name, pc, cfg.Network, ov)
}

// buildEmbeddedOptions resolves embedded.Options from a provider's static
// configuration plus the current run's overrides.
//
// Model path precedence: an override that looks like a model path (or that
// simply exists on disk) wins, then the provider's configured model_path,
// then its default_model. GPULayers precedence: override, then the
// provider's configured value, then -1 (offload all layers). ContextSize:
// override, then the provider's configured value (0 = model default).
func buildEmbeddedOptions(pc config.ProviderConfig, ov ActiveOverrides) (embedded.Options, error) {
	modelPath := pc.ModelPath
	if pc.ModelPath == "" {
		modelPath = pc.DefaultModel
	}
	if ov.Model != "" {
		modelPath = ov.Model
	}
	expanded, err := history.ExpandHome(modelPath)
	if err != nil {
		return embedded.Options{}, fmt.Errorf("expand model path %q: %w", modelPath, err)
	}
	modelPath = expanded

	libraryPath, err := history.ExpandHome(pc.LibraryPath)
	if err != nil {
		return embedded.Options{}, fmt.Errorf("expand library path %q: %w", pc.LibraryPath, err)
	}
	mmprojPath, err := history.ExpandHome(pc.MMProjPath)
	if err != nil {
		return embedded.Options{}, fmt.Errorf("expand vision projector path %q: %w", pc.MMProjPath, err)
	}
	toolFormat, err := embedded.ParseToolFormat(pc.ToolFormat)
	if err != nil {
		return embedded.Options{}, fmt.Errorf("embedded provider configuration: %w", err)
	}

	contextSize := pc.ContextSize
	if ov.ContextSize != nil {
		contextSize = *ov.ContextSize
	}

	gpuLayers := -1
	if pc.GPULayers != nil {
		gpuLayers = *pc.GPULayers
	}
	if ov.GPULayers != nil {
		gpuLayers = *ov.GPULayers
	}

	sampling := embedded.Sampling{
		TopK:          defaultSamplingTopK,
		MinP:          defaultSamplingMinP,
		RepeatPenalty: defaultSamplingRepeatPenalty,
		RepeatLastN:   defaultSamplingRepeatLastN,
	}
	if sc := pc.Sampling; sc != nil {
		if sc.TopK != 0 {
			sampling.TopK = sc.TopK
		}
		if sc.MinP != 0 {
			sampling.MinP = sc.MinP
		}
		if sc.RepeatPenalty != 0 {
			sampling.RepeatPenalty = sc.RepeatPenalty
		}
		if sc.RepeatLastN != 0 {
			sampling.RepeatLastN = sc.RepeatLastN
		}
		sampling.Seed = sc.Seed
		sampling.Stop = sc.Stop
	}

	return embedded.Options{
		ModelPath:    modelPath,
		MMProjPath:   mmprojPath,
		LibraryPath:  libraryPath,
		ContextSize:  contextSize,
		GPULayers:    gpuLayers,
		Threads:      pc.Threads,
		BatchSize:    pc.BatchSize,
		ChatTemplate: pc.ChatTemplate,
		ToolFormat:   toolFormat,
		Sampling:     sampling,
	}, nil
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
