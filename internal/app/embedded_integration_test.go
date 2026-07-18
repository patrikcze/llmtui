package app

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider"
)

func TestEmbeddedProviderIntegration(t *testing.T) {
	libraryPath := os.Getenv("YZMA_LIB")
	modelPath := os.Getenv("LLMTUI_TEST_GGUF")
	if libraryPath == "" || modelPath == "" {
		t.Skip("set YZMA_LIB and LLMTUI_TEST_GGUF to run the embedded factory integration test")
	}

	gpuLayers := -1
	cfg := &config.Config{
		DefaultProvider: "embedded",
		Providers: map[string]config.ProviderConfig{
			"embedded": {
				Type:        "embedded",
				ModelPath:   modelPath,
				LibraryPath: libraryPath,
				ContextSize: 2048,
				GPULayers:   &gpuLayers,
				BatchSize:   512,
			},
		},
		Network: config.NetworkConfig{},
	}

	prov, err := BuildActiveProvider(cfg)
	if err != nil {
		t.Fatalf("BuildActiveProvider() error = %v", err)
	}
	defer func() {
		if err := provider.CloseProvider(prov); err != nil {
			t.Errorf("CloseProvider() error = %v", err)
		}
	}()

	if err := prov.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck() error = %v", err)
	}
	stream, err := prov.Chat(context.Background(), provider.ChatRequest{
		Model:       modelPath,
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: "Reply with OK."}},
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   8,
		Stream:      true,
	})
	if err != nil {
		t.Fatalf("Chat() error = %v", err)
	}

	var text strings.Builder
	var usage *provider.Usage
	for event := range stream {
		switch event.Type {
		case provider.EventDelta:
			text.WriteString(event.Delta)
		case provider.EventDone:
			usage = event.Usage
		case provider.EventError:
			t.Fatalf("Chat() stream error = %v", event.Err)
		}
	}
	if text.Len() == 0 || usage == nil || usage.Estimated {
		t.Errorf("Chat() text = %q, usage = %+v; want streamed text and exact usage", text.String(), usage)
	}
}
