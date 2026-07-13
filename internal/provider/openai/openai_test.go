package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func collect(t *testing.T, events <-chan provider.ChatEvent) (string, *provider.Usage, error) {
	t.Helper()
	var (
		text  strings.Builder
		usage *provider.Usage
	)
	for ev := range events {
		switch ev.Type {
		case provider.EventDelta:
			text.WriteString(ev.Delta)
		case provider.EventDone:
			usage = ev.Usage
		case provider.EventError:
			return text.String(), usage, ev.Err
		}
	}
	return text.String(), usage, nil
}

func TestChatStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("path = %s, want /v1/chat/completions", r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); auth != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", auth)
		}
		var req chatCompletionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !req.Stream {
			t.Error("expected stream=true in request")
		}
		if req.Model != "test-model" {
			t.Errorf("model = %q, want test-model", req.Model)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n")
		fmt.Fprint(w, ": keep-alive comment\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("test", srv.URL+"/v1", "test-key")
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:    "test-model",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	text, usage, err := collect(t, events)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if text != "Hello world" {
		t.Errorf("text = %q, want %q", text, "Hello world")
	}
	if usage == nil {
		t.Fatal("usage missing")
	}
	if usage.TotalTokens != 7 || usage.Estimated {
		t.Errorf("usage = %+v, want total 7 not estimated", usage)
	}
}

func TestChatStreamingEstimatesUsageWhenAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"response text here\"}}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("test", srv.URL, "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:    "m",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello there"}},
		Stream:   true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	_, usage, err := collect(t, events)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if usage == nil || !usage.Estimated {
		t.Fatalf("usage = %+v, want estimated usage", usage)
	}
	if usage.TotalTokens == 0 {
		t.Error("estimated total tokens should be > 0")
	}
}

func TestChatNonStreaming(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{{"message": map[string]any{"content": "full reply"}}},
			"usage":   map[string]int{"prompt_tokens": 3, "completion_tokens": 4, "total_tokens": 7},
		})
	}))
	defer srv.Close()

	p := New("test", srv.URL, "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Stream: false})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	text, usage, err := collect(t, events)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if text != "full reply" {
		t.Errorf("text = %q, want full reply", text)
	}
	if usage == nil || usage.TotalTokens != 7 {
		t.Errorf("usage = %+v, want total 7", usage)
	}
}

func TestChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"model not found"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	p := New("test", srv.URL, "")
	_, err := p.Chat(context.Background(), provider.ChatRequest{Model: "missing"})
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error %q should mention status 404", err)
	}
}

func TestChatMalformedStreamChunk(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {not json}\n\n")
	}))
	defer srv.Close()

	p := New("test", srv.URL, "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	_, _, err = collect(t, events)
	if err == nil {
		t.Fatal("expected error for malformed chunk")
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "model-a"}, {"id": "model-b"}},
			})
		case "/api/v0/models":
			// Generic OpenAI-compatible servers (vLLM, llama.cpp, ...) don't
			// implement LM Studio's native models endpoint.
			w.WriteHeader(http.StatusNotFound)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New("test", srv.URL+"/v1", "")
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 2 || models[0].ID != "model-a" {
		t.Errorf("models = %+v, want model-a and model-b", models)
	}
	if models[0].Vision != nil || models[1].Vision != nil {
		t.Errorf("Vision = %+v, want nil (no LM Studio endpoint) so callers fall back to the heuristic", models)
	}
}

func TestListModelsUsesLMStudioVisionCapability(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/models":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{{"id": "qwen/qwen3.6-27b"}, {"id": "openai/gpt-oss-20b"}},
			})
		case "/api/v0/models":
			json.NewEncoder(w).Encode(map[string]any{
				"data": []map[string]string{
					{"id": "qwen/qwen3.6-27b", "type": "vlm"},
					{"id": "openai/gpt-oss-20b", "type": "llm"},
				},
			})
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	p := New("test", srv.URL+"/v1", "")
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	byID := map[string]*bool{}
	for _, m := range models {
		byID[m.ID] = m.Vision
	}
	if v := byID["qwen/qwen3.6-27b"]; v == nil || !*v {
		t.Errorf("qwen/qwen3.6-27b Vision = %v, want true (heuristic alone would miss this model)", v)
	}
	if v := byID["openai/gpt-oss-20b"]; v == nil || *v {
		t.Errorf("openai/gpt-oss-20b Vision = %v, want false", v)
	}
}

func TestHealthCheckUnreachable(t *testing.T) {
	p := New("test", "http://127.0.0.1:1", "")
	if err := p.HealthCheck(context.Background()); err == nil {
		t.Error("expected health check failure for unreachable server")
	}
}
