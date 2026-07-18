package llamart

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestRuntimeIntegration(t *testing.T) {
	opts := integrationOptions(t)
	runtime := New()
	progress := []string{}

	loadStarted := time.Now()
	meta, err := runtime.Load(context.Background(), opts, func(message string) {
		progress = append(progress, message)
	})
	loadElapsed := time.Since(loadStarted)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if meta.Name == "" || meta.ContextSize != opts.ContextSize || meta.SizeBytes <= 0 || meta.Parameters == 0 {
		t.Errorf("Load() metadata = %+v, want populated bounded metadata", meta)
	}
	if len(progress) == 0 {
		t.Error("Load() emitted no progress")
	}
	t.Logf("load: %s; model: %.2f GiB; parameters: %.2fB", loadElapsed.Round(time.Millisecond), float64(meta.SizeBytes)/(1<<30), float64(meta.Parameters)/1e9)

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
	t.Logf("first generation: %d prompt + %d completion tokens in %s (%.2f completion tok/s)",
		first.result.PromptTokens,
		first.result.CompletionTokens,
		first.elapsed.Round(time.Millisecond),
		float64(first.result.CompletionTokens)/first.elapsed.Seconds(),
	)

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
	t.Logf("multi-turn/KV reuse: %d prompt + %d completion tokens in %s (%.2f completion tok/s)",
		multiTurn.result.PromptTokens,
		multiTurn.result.CompletionTokens,
		multiTurn.elapsed.Round(time.Millisecond),
		float64(multiTurn.result.CompletionTokens)/multiTurn.elapsed.Seconds(),
	)

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
	var canceledAt time.Time
	_, err = runtime.Generate(cancelCtx, embedded.GenRequest{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "Write a long numbered list with detailed explanations."},
		},
		Temperature: 0.7,
		TopP:        0.9,
		MaxTokens:   128,
	}, func(embedded.GenDelta) {
		pieces++
		canceledAt = time.Now()
		cancel()
	})
	cancelLatency := time.Since(canceledAt)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Generate() after cancel error = %v, want context.Canceled", err)
	}
	if pieces == 0 {
		t.Fatal("Generate() was canceled before streaming a piece; test did not exercise mid-generation cancellation")
	}
	t.Logf("mid-generation cancel latency: %s", cancelLatency.Round(time.Millisecond))

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
	if _, err := runtime.Generate(context.Background(), embedded.GenRequest{}, func(embedded.GenDelta) {}); err == nil {
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

func TestRuntimeToolIntegration(t *testing.T) {
	opts := integrationOptions(t)
	toolFormat, ok := embedded.ResolveToolFormat(opts.ToolFormat, opts.ModelPath)
	if !ok {
		t.Skipf("model path %q has no recognized tool format", opts.ModelPath)
	}
	runtime := New()
	if _, err := runtime.Load(context.Background(), opts, nil); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	var visible strings.Builder
	result, err := runtime.Generate(context.Background(), embedded.GenRequest{
		Messages: []provider.Message{{
			Role:    provider.RoleUser,
			Content: "Use the weather tool for Prague. Do not answer from memory.",
		}},
		Tools: []provider.ToolSpec{{
			Name:        "weather",
			Description: "Get current weather for a city",
			Parameters:  []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}},
		ToolFormat:  toolFormat,
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   96,
	}, func(delta embedded.GenDelta) {
		if delta.Kind == embedded.DeltaText {
			visible.WriteString(delta.Text)
		}
	})
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "weather" {
		t.Fatalf("ToolCalls = %+v, visible = %q", result.ToolCalls, visible.String())
	}
	if strings.Contains(visible.String(), "toolcall") || strings.Contains(visible.String(), "call:weather") {
		t.Errorf("tool markup leaked into visible text: %q", visible.String())
	}
	var arguments map[string]any
	if err := json.Unmarshal([]byte(result.ToolCalls[0].Arguments), &arguments); err != nil || arguments["city"] == "" {
		t.Errorf("arguments = %q, error = %v", result.ToolCalls[0].Arguments, err)
	}
}

type integrationGeneration struct {
	text    string
	result  embedded.GenResult
	elapsed time.Duration
}

func generateIntegration(t *testing.T, runtime *Runtime, req embedded.GenRequest) integrationGeneration {
	t.Helper()
	var text strings.Builder
	started := time.Now()
	result, err := runtime.Generate(context.Background(), req, func(delta embedded.GenDelta) {
		text.WriteString(delta.Text)
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	return integrationGeneration{text: text.String(), result: result, elapsed: elapsed}
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
