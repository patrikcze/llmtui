package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func intPtr(v int) *int { return &v }

func TestBuildProviderEmbeddedConstructs(t *testing.T) {
	pc := config.ProviderConfig{Type: "embedded", ModelPath: "/models/model.gguf"}
	prov, err := BuildProvider("embedded", pc, config.NetworkConfig{})
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if _, ok := prov.(*embedded.Provider); !ok {
		t.Fatalf("BuildProvider returned %T, want *embedded.Provider", prov)
	}
	if prov.Name() != "embedded" {
		t.Errorf("Name() = %q, want embedded", prov.Name())
	}
}

func TestBuildProviderUnknownTypeErrors(t *testing.T) {
	pc := config.ProviderConfig{Type: "does-not-exist"}
	_, err := BuildProvider("x", pc, config.NetworkConfig{})
	if err == nil {
		t.Fatal("expected error for unknown provider type")
	}
}

func TestBuildProviderEmbeddedWithoutOverridesStillConstructs(t *testing.T) {
	// No ActiveOverrides argument at all: the variadic parameter must default
	// cleanly (this is the call shape used by the provider/model picker and
	// /doctor, which build a specific named provider, not necessarily the
	// active one).
	pc := config.ProviderConfig{Type: "embedded", DefaultModel: "/models/fallback.gguf"}
	prov, err := BuildProvider("embedded", pc, config.NetworkConfig{})
	if err != nil {
		t.Fatalf("BuildProvider: %v", err)
	}
	if prov == nil {
		t.Fatal("expected non-nil provider")
	}
}

func TestBuildEmbeddedOptionsModelPathPrecedence(t *testing.T) {
	pc := config.ProviderConfig{
		Type:         "embedded",
		ModelPath:    "/configured/model.gguf",
		DefaultModel: "/fallback/model.gguf",
	}

	// Override wins over everything.
	opts, err := buildEmbeddedOptions(pc, ActiveOverrides{Model: "/override/model.gguf"})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	if opts.ModelPath != "/override/model.gguf" {
		t.Errorf("ModelPath = %q, want override to win", opts.ModelPath)
	}

	// No override: configured model_path wins over default_model.
	opts, err = buildEmbeddedOptions(pc, ActiveOverrides{})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	if opts.ModelPath != "/configured/model.gguf" {
		t.Errorf("ModelPath = %q, want configured model_path", opts.ModelPath)
	}

	// Neither override nor model_path: falls back to default_model.
	pcNoPath := config.ProviderConfig{Type: "embedded", DefaultModel: "/fallback/model.gguf"}
	opts, err = buildEmbeddedOptions(pcNoPath, ActiveOverrides{})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	if opts.ModelPath != "/fallback/model.gguf" {
		t.Errorf("ModelPath = %q, want default_model fallback", opts.ModelPath)
	}
}

func TestBuildEmbeddedOptionsTildeExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}
	pc := config.ProviderConfig{
		Type:        "embedded",
		ModelPath:   "~/models/model.gguf",
		MMProjPath:  "~/models/mmproj-model.gguf",
		LibraryPath: "~/lib",
		ToolFormat:  "gemma",
	}
	opts, err := buildEmbeddedOptions(pc, ActiveOverrides{})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	wantModel := filepath.Join(home, "models", "model.gguf")
	if opts.ModelPath != wantModel {
		t.Errorf("ModelPath = %q, want %q", opts.ModelPath, wantModel)
	}
	wantLib := filepath.Join(home, "lib")
	if opts.LibraryPath != wantLib {
		t.Errorf("LibraryPath = %q, want %q", opts.LibraryPath, wantLib)
	}
	wantMMProj := filepath.Join(home, "models", "mmproj-model.gguf")
	if opts.MMProjPath != wantMMProj {
		t.Errorf("MMProjPath = %q, want %q", opts.MMProjPath, wantMMProj)
	}
	if opts.ToolFormat != embedded.ToolFormatGemma {
		t.Errorf("ToolFormat = %q, want gemma", opts.ToolFormat)
	}
}

func TestBuildEmbeddedOptionsRejectsUnknownToolFormat(t *testing.T) {
	_, err := buildEmbeddedOptions(config.ProviderConfig{
		Type:       "embedded",
		ModelPath:  "model.gguf",
		ToolFormat: "mystery",
	}, ActiveOverrides{})
	if err == nil || !strings.Contains(err.Error(), "tool_format") {
		t.Fatalf("buildEmbeddedOptions error = %v, want tool_format validation", err)
	}
}

func TestBuildEmbeddedOptionsContextSizeAndGPULayersPrecedence(t *testing.T) {
	// No config, no override: GPULayers defaults to -1 (offload all), context
	// size defaults to 0 (model default).
	opts, err := buildEmbeddedOptions(config.ProviderConfig{Type: "embedded", ModelPath: "m.gguf"}, ActiveOverrides{})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	if opts.GPULayers != -1 {
		t.Errorf("GPULayers = %d, want -1 default", opts.GPULayers)
	}
	if opts.ContextSize != 0 {
		t.Errorf("ContextSize = %d, want 0 default", opts.ContextSize)
	}

	// Provider config sets both; no override.
	pc := config.ProviderConfig{Type: "embedded", ModelPath: "m.gguf", ContextSize: 4096, GPULayers: intPtr(10)}
	opts, err = buildEmbeddedOptions(pc, ActiveOverrides{})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	if opts.GPULayers != 10 {
		t.Errorf("GPULayers = %d, want 10 from provider config", opts.GPULayers)
	}
	if opts.ContextSize != 4096 {
		t.Errorf("ContextSize = %d, want 4096 from provider config", opts.ContextSize)
	}

	// Override wins over provider config.
	opts, err = buildEmbeddedOptions(pc, ActiveOverrides{ContextSize: intPtr(8192), GPULayers: intPtr(0)})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	if opts.GPULayers != 0 {
		t.Errorf("GPULayers = %d, want override 0 (CPU only) to win over configured 10", opts.GPULayers)
	}
	if opts.ContextSize != 8192 {
		t.Errorf("ContextSize = %d, want override 8192 to win", opts.ContextSize)
	}
}

func TestBuildEmbeddedOptionsSamplingDefaults(t *testing.T) {
	// No sampling block configured: ADR defaults apply.
	opts, err := buildEmbeddedOptions(config.ProviderConfig{Type: "embedded", ModelPath: "m.gguf"}, ActiveOverrides{})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	want := embedded.Sampling{TopK: 40, MinP: 0.05, RepeatPenalty: 1.1, RepeatLastN: 64}
	if opts.Sampling.TopK != want.TopK || opts.Sampling.MinP != want.MinP ||
		opts.Sampling.RepeatPenalty != want.RepeatPenalty || opts.Sampling.RepeatLastN != want.RepeatLastN ||
		len(opts.Sampling.Stop) != 0 {
		t.Errorf("Sampling = %+v, want ADR defaults %+v", opts.Sampling, want)
	}

	// Partial sampling block: unset (zero) fields still get ADR defaults,
	// explicitly set fields are preserved.
	pc := config.ProviderConfig{
		Type:      "embedded",
		ModelPath: "m.gguf",
		Sampling: &config.SamplingConfig{
			TopK: 100,
			Stop: []string{"</s>"},
		},
	}
	opts, err = buildEmbeddedOptions(pc, ActiveOverrides{})
	if err != nil {
		t.Fatalf("buildEmbeddedOptions: %v", err)
	}
	if opts.Sampling.TopK != 100 {
		t.Errorf("TopK = %d, want explicit 100", opts.Sampling.TopK)
	}
	if opts.Sampling.MinP != 0.05 {
		t.Errorf("MinP = %v, want ADR default 0.05 for an unset field", opts.Sampling.MinP)
	}
	if opts.Sampling.RepeatPenalty != 1.1 {
		t.Errorf("RepeatPenalty = %v, want ADR default 1.1", opts.Sampling.RepeatPenalty)
	}
	if opts.Sampling.RepeatLastN != 64 {
		t.Errorf("RepeatLastN = %d, want ADR default 64", opts.Sampling.RepeatLastN)
	}
	if len(opts.Sampling.Stop) != 1 || opts.Sampling.Stop[0] != "</s>" {
		t.Errorf("Stop = %v, want [</s>]", opts.Sampling.Stop)
	}
}

func TestBuildActiveProviderAppliesOverridesForEmbedded(t *testing.T) {
	cfg := &config.Config{
		DefaultProvider: "embedded",
		Providers: map[string]config.ProviderConfig{
			"embedded": {Type: "embedded", ModelPath: "/configured/model.gguf"},
		},
		Model:       "/session-override/model.gguf",
		ContextSize: intPtr(2048),
		GPULayers:   intPtr(0),
	}
	prov, err := BuildActiveProvider(cfg)
	if err != nil {
		t.Fatalf("BuildActiveProvider: %v", err)
	}
	ep, ok := prov.(*embedded.Provider)
	if !ok {
		t.Fatalf("BuildActiveProvider returned %T, want *embedded.Provider", prov)
	}
	models, err := ep.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) == 0 || models[0].ID != "/session-override/model.gguf" {
		t.Fatalf("ListModels()[0] = %+v, want the session-overridden model path", models)
	}
}

func TestBuildActiveProviderUnconfiguredProviderErrors(t *testing.T) {
	cfg := &config.Config{DefaultProvider: "nope", Providers: map[string]config.ProviderConfig{}}
	if _, err := BuildActiveProvider(cfg); err == nil {
		t.Fatal("expected error for unconfigured active provider")
	}
}
