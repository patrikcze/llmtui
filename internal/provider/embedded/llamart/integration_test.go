package llamart

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestRuntimeIntegration(t *testing.T) {
	opts := integrationOptions(t)
	runtime := New()
	progress := []string{}

	meta, err := runtime.Load(context.Background(), opts, func(message string) {
		progress = append(progress, message)
	})
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if meta.Name == "" || meta.ContextSize != opts.ContextSize || meta.SizeBytes <= 0 || meta.Parameters == 0 {
		t.Errorf("Load() metadata = %+v, want populated bounded metadata", meta)
	}
	if len(progress) == 0 {
		t.Error("Load() emitted no progress")
	}

	first := generateIntegration(t, runtime, embedded.GenRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "Follow the user's formatting request exactly."},
			{Role: provider.RoleUser, Content: "Reply with one short sentence."},
		},
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   24,
	})
	if first.text == "" || first.result.PromptTokens == 0 || first.result.CompletionTokens == 0 {
		t.Errorf("first Generate() = %+v, want streamed text and real usage", first)
	}

	multiTurn := generateIntegration(t, runtime, embedded.GenRequest{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "Name one primary color."},
			{Role: provider.RoleAssistant, Content: first.text},
			{Role: provider.RoleUser, Content: "Now name a different primary color in one word."},
		},
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   16,
	})
	if multiTurn.text == "" {
		t.Error("multi-turn Generate() streamed no text")
	}

	unicodeOutput := generateIntegration(t, runtime, embedded.GenRequest{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "Reply exactly with: café 🙂"},
		},
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   16,
	})
	if !utf8.ValidString(unicodeOutput.text) {
		t.Errorf("unicode Generate() returned invalid UTF-8: %q", unicodeOutput.text)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	pieces := 0
	_, err = runtime.Generate(cancelCtx, embedded.GenRequest{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "Write a long numbered list with detailed explanations."},
		},
		Temperature: 0.7,
		TopP:        0.9,
		MaxTokens:   128,
	}, func(string) {
		pieces++
		cancel()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() after cancel error = %v, want context.Canceled", err)
	}
	if pieces == 0 {
		t.Fatal("Generate() was canceled before streaming a piece; test did not exercise mid-generation cancellation")
	}

	reused := generateIntegration(t, runtime, embedded.GenRequest{
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: "Reply with OK."}},
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   8,
	})
	if reused.text == "" {
		t.Error("runtime was not reusable after cancellation")
	}

	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, err := runtime.Generate(context.Background(), embedded.GenRequest{}, func(string) {}); err == nil {
		t.Error("Generate() after Close() error = nil, want unloaded-runtime error")
	}

	t.Run("provider end to end", func(t *testing.T) {
		prov := embedded.New("embedded-integration", opts, func() embedded.Runtime { return New() })
		defer func() {
			if err := prov.Close(); err != nil {
				t.Errorf("provider Close() error = %v", err)
			}
		}()

		if err := prov.HealthCheck(context.Background()); err != nil {
			t.Fatalf("HealthCheck() error = %v", err)
		}
		stream, err := prov.Chat(context.Background(), provider.ChatRequest{
			Model:       opts.ModelPath,
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
	})
}

type integrationGeneration struct {
	text   string
	result embedded.GenResult
}

func generateIntegration(t *testing.T, runtime *Runtime, req embedded.GenRequest) integrationGeneration {
	t.Helper()
	var text strings.Builder
	result, err := runtime.Generate(context.Background(), req, func(piece string) {
		text.WriteString(piece)
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	return integrationGeneration{text: text.String(), result: result}
}

func integrationOptions(t *testing.T) embedded.Options {
	t.Helper()
	libraryPath := os.Getenv("YZMA_LIB")
	modelPath := os.Getenv("LLMTUI_TEST_GGUF")
	if libraryPath == "" || modelPath == "" {
		t.Skip("set YZMA_LIB and LLMTUI_TEST_GGUF to run llama.cpp integration tests")
	}
	return embedded.Options{
		ModelPath:   modelPath,
		LibraryPath: libraryPath,
		ContextSize: 2048,
		GPULayers:   -1,
		BatchSize:   512,
		Sampling: embedded.Sampling{
			TopK:          40,
			MinP:          0.05,
			RepeatPenalty: 1.1,
			RepeatLastN:   64,
			Seed:          1,
		},
	}
}
