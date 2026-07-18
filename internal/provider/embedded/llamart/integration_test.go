package llamart

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
	llmtools "github.com/patrikcze/llmtui/internal/tools"
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
		MaxTokens:   96,
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
		MaxTokens:   192,
	})
	if reused.text == "" {
		t.Errorf("runtime was not reusable after cancellation: %+v", reused)
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
			MaxTokens:   192,
			Stream:      true,
		})
		if err != nil {
			t.Fatalf("Chat() error = %v", err)
		}

		var text strings.Builder
		var reasoning strings.Builder
		var usage *provider.Usage
		for event := range stream {
			switch event.Type {
			case provider.EventDelta:
				text.WriteString(event.Delta)
			case provider.EventReasoning:
				reasoning.WriteString(event.Delta)
			case provider.EventDone:
				usage = event.Usage
			case provider.EventError:
				t.Fatalf("Chat() stream error = %v", event.Err)
			}
		}
		if text.Len() == 0 || usage == nil || usage.Estimated {
			t.Errorf("Chat() text = %q, reasoning = %q, usage = %+v; want streamed text and exact usage", text.String(), reasoning.String(), usage)
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

func TestRuntimeVisionIntegration(t *testing.T) {
	opts := visionIntegrationOptions(t)
	runtime := New()
	meta, err := runtime.Load(context.Background(), opts, nil)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	if !meta.HasTemplate {
		t.Fatal("Gemma vision model has no metadata chat template")
	}

	t.Run("metadata template text regression", func(t *testing.T) {
		text := generateIntegration(t, runtime, embedded.GenRequest{
			Messages:    []provider.Message{{Role: provider.RoleUser, Content: "Reply with exactly: Hi"}},
			Temperature: 0,
			TopP:        0.9,
			MaxTokens:   12,
		})
		if !strings.Contains(strings.ToLower(text.text), "hi") {
			t.Fatalf("plain Gemma response = %q, want Hi", text.text)
		}
	})

	red := solidPNG(t, color.RGBA{R: 255, A: 255})
	blue := solidPNG(t, color.RGBA{B: 255, A: 255})
	t.Run("single image and exact usage", func(t *testing.T) {
		vision := generateIntegration(t, runtime, embedded.GenRequest{
			Messages: []provider.Message{{
				Role:    provider.RoleUser,
				Content: "What is the dominant color? Reply with one word.",
				Images:  []provider.Image{{Data: red, MIME: "image/png"}},
			}},
			Temperature: 0,
			TopP:        0.9,
			MaxTokens:   24,
		})
		if !strings.Contains(strings.ToLower(vision.text), "red") || vision.result.PromptTokens == 0 || vision.result.CompletionTokens == 0 {
			t.Fatalf("vision generation = %+v, want a red answer and exact nonzero usage", vision)
		}
		t.Logf("single image: %d prompt + %d completion tokens in %s", vision.result.PromptTokens, vision.result.CompletionTokens, vision.elapsed.Round(time.Millisecond))
	})

	t.Run("multiple image order", func(t *testing.T) {
		vision := generateIntegration(t, runtime, embedded.GenRequest{
			Messages: []provider.Message{{
				Role:    provider.RoleUser,
				Content: "Name the dominant color of the first image, then the second image, in that order.",
				Images: []provider.Image{
					{Data: red, MIME: "image/png"},
					{Data: blue, MIME: "image/png"},
				},
			}},
			Temperature: 0,
			TopP:        0.9,
			MaxTokens:   48,
		})
		answer := strings.ToLower(vision.text)
		redAt, blueAt := strings.Index(answer, "red"), strings.Index(answer, "blue")
		if redAt < 0 || blueAt < 0 || redAt >= blueAt {
			t.Fatalf("ordered image response = %q, want red before blue", vision.text)
		}
	})

	t.Run("reasoning modes isolate thoughts", func(t *testing.T) {
		for _, test := range []struct {
			name string
			mode string
		}{
			{name: "on", mode: "on"},
			{name: "off", mode: "off"},
			{name: "auto", mode: ""},
		} {
			t.Run(test.name, func(t *testing.T) {
				generation := generateIntegration(t, runtime, embedded.GenRequest{
					Messages:    []provider.Message{{Role: provider.RoleUser, Content: "What is 17 multiplied by 19? Give only the final number in the answer."}},
					Reasoning:   test.mode,
					Temperature: 0,
					TopP:        0.9,
					MaxTokens:   96,
				})
				if generation.text == "" {
					t.Fatal("reasoning-mode generation returned no answer")
				}
				if test.mode == "off" && generation.reasoning != "" {
					t.Fatalf("reasoning-off produced answer=%q reasoning=%q", generation.text, generation.reasoning)
				}
				combined := strings.ToLower(generation.text + generation.reasoning)
				if strings.Contains(combined, "<think") || strings.Contains(combined, "</think") || strings.Contains(combined, "<|channel>") {
					t.Fatalf("reasoning markup leaked: answer=%q reasoning=%q", generation.text, generation.reasoning)
				}
				t.Logf("mode=%q answer=%q reasoning_bytes=%d", test.mode, generation.text, len(generation.reasoning))
			})
		}
	})

	t.Run("native tool request and continuation", func(t *testing.T) {
		toolFormat, ok := embedded.ResolveToolFormat(opts.ToolFormat, opts.ModelPath)
		if !ok {
			t.Fatalf("Gemma model path %q has no tool format", opts.ModelPath)
		}
		tool := provider.ToolSpec{
			Name:        "weather",
			Description: "Get current weather for a city",
			Parameters:  []byte(`{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`),
		}
		question := provider.Message{Role: provider.RoleUser, Content: "Use the weather tool for Prague. Do not answer from memory."}
		requested := generateIntegration(t, runtime, embedded.GenRequest{
			Messages:    []provider.Message{question},
			Tools:       []provider.ToolSpec{tool},
			ToolFormat:  toolFormat,
			Temperature: 0,
			TopP:        0.9,
			MaxTokens:   96,
		})
		if len(requested.result.ToolCalls) != 1 || requested.result.ToolCalls[0].Name != tool.Name {
			t.Fatalf("tool request = %+v, visible=%q", requested.result.ToolCalls, requested.text)
		}
		call := requested.result.ToolCalls[0]
		continued := generateIntegration(t, runtime, embedded.GenRequest{
			Messages: []provider.Message{
				question,
				{Role: provider.RoleAssistant, ToolCalls: requested.result.ToolCalls},
				{Role: provider.RoleTool, ToolCallID: call.ID, ToolName: call.Name, Content: `{"temperature_c":12,"condition":"sunny"}`},
			},
			Tools:       []provider.ToolSpec{tool},
			ToolFormat:  toolFormat,
			Temperature: 0,
			TopP:        0.9,
			MaxTokens:   64,
		})
		if len(continued.result.ToolCalls) != 0 || !strings.Contains(continued.text, "12") {
			t.Fatalf("tool continuation calls=%+v answer=%q", continued.result.ToolCalls, continued.text)
		}
	})

	t.Run("cancel image prompt and reuse", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		_, err := runtime.Generate(ctx, embedded.GenRequest{
			Messages: []provider.Message{{
				Role:    provider.RoleUser,
				Content: "Describe this image in detail.",
				Images:  []provider.Image{{Data: red, MIME: "image/png"}},
			}},
			Temperature: 0,
			TopP:        0.9,
			MaxTokens:   96,
			Progress: func(message string) {
				if strings.Contains(message, "processing multimodal prompt") {
					cancel()
				}
			},
		}, func(embedded.GenDelta) {})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled image error = %v, want context.Canceled", err)
		}
		reused := generateIntegration(t, runtime, embedded.GenRequest{
			Messages: []provider.Message{{
				Role:    provider.RoleUser,
				Content: "What is the dominant color? Reply with one word.",
				Images:  []provider.Image{{Data: blue, MIME: "image/png"}},
			}},
			Temperature: 0,
			TopP:        0.9,
			MaxTokens:   24,
		})
		if !strings.Contains(strings.ToLower(reused.text), "blue") {
			t.Fatalf("image generation after cancel = %q, want blue", reused.text)
		}
	})

	// A text request after image embeddings must clear contaminated KV state
	// and remain usable.
	text := generateIntegration(t, runtime, embedded.GenRequest{
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: "Reply with OK."}},
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   8,
	})
	if text.text == "" {
		t.Fatal("text generation after vision returned no output")
	}
}

func TestRuntimeVisionRejectsWrongProjectorIntegration(t *testing.T) {
	opts := visionIntegrationOptions(t)
	opts.MMProjPath = t.TempDir() + "/not-a-projector.gguf"
	if err := os.WriteFile(opts.MMProjPath, []byte("not a GGUF projector"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := New()
	_, err := runtime.Load(context.Background(), opts, nil)
	if err == nil || !strings.Contains(err.Error(), "load vision projector") {
		t.Fatalf("Load() wrong projector error = %v", err)
	}
}

// TestRuntimeLargePromptIntegration mirrors a real TUI request with the normal
// 8192-token context and several native tool schemas. Small acceptance prompts
// do not exercise llama.Decode with multiple full-sized prompt chunks.
func TestRuntimeLargePromptIntegration(t *testing.T) {
	opts := visionIntegrationOptions(t)
	opts.ContextSize = 8192
	runtime := New()
	if _, err := runtime.Load(context.Background(), opts, nil); err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	defer func() {
		if err := runtime.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()

	generation := generateIntegration(t, runtime, embedded.GenRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "You are a helpful local coding assistant."},
			{Role: provider.RoleUser, Content: strings.Repeat("context ", 300) + "\nReply with OK."},
		},
		Tools:       append(llmtools.Specs(), llmtools.WebSpecs()...),
		ToolFormat:  embedded.ToolFormatGemma,
		Temperature: 0,
		TopP:        0.9,
		MaxTokens:   32,
	})
	if generation.result.PromptTokens <= opts.BatchSize {
		t.Fatalf("prompt tokens = %d, want more than batch size %d", generation.result.PromptTokens, opts.BatchSize)
	}
}

type integrationGeneration struct {
	text      string
	reasoning string
	result    embedded.GenResult
	elapsed   time.Duration
}

func generateIntegration(t *testing.T, runtime *Runtime, req embedded.GenRequest) integrationGeneration {
	t.Helper()
	var text strings.Builder
	var reasoning strings.Builder
	started := time.Now()
	result, err := runtime.Generate(context.Background(), req, func(delta embedded.GenDelta) {
		if delta.Kind == embedded.DeltaReasoning {
			reasoning.WriteString(delta.Text)
			return
		}
		text.WriteString(delta.Text)
	})
	elapsed := time.Since(started)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	return integrationGeneration{text: text.String(), reasoning: reasoning.String(), result: result, elapsed: elapsed}
}

func solidPNG(t *testing.T, fill color.RGBA) []byte {
	t.Helper()
	bitmap := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := range 32 {
		for x := range 32 {
			bitmap.Set(x, y, fill)
		}
	}
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, bitmap); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func integrationOptions(t *testing.T) embedded.Options {
	t.Helper()
	libraryPath := os.Getenv("YZMA_LIB")
	modelPath := os.Getenv("LLMTUI_TEST_GGUF")
	if libraryPath == "" || modelPath == "" {
		t.Skip("set YZMA_LIB and LLMTUI_TEST_GGUF to run llama.cpp integration tests")
	}
	gpuLayers := -1
	if os.Getenv("LLMTUI_TEST_CPU") == "1" {
		gpuLayers = 0
	}
	return embedded.Options{
		ModelPath:   modelPath,
		LibraryPath: libraryPath,
		ContextSize: 2048,
		GPULayers:   gpuLayers,
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

func visionIntegrationOptions(t *testing.T) embedded.Options {
	t.Helper()
	libraryPath := os.Getenv("YZMA_LIB")
	modelPath := os.Getenv("LLMTUI_TEST_VISION_GGUF")
	projectorPath := os.Getenv("LLMTUI_TEST_MMPROJ")
	if libraryPath == "" || modelPath == "" || projectorPath == "" {
		t.Skip("set YZMA_LIB, LLMTUI_TEST_VISION_GGUF, and LLMTUI_TEST_MMPROJ to run Gemma vision integration tests")
	}
	gpuLayers := -1
	if os.Getenv("LLMTUI_TEST_CPU") == "1" {
		gpuLayers = 0
	}
	return embedded.Options{
		ModelPath:   modelPath,
		MMProjPath:  projectorPath,
		LibraryPath: libraryPath,
		ContextSize: 2048,
		GPULayers:   gpuLayers,
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
