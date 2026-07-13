package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
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

func TestChatStreamingNDJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			t.Errorf("path = %s, want /api/chat", r.URL.Path)
		}
		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if req.Model != "qwen3" {
			t.Errorf("model = %q, want qwen3", req.Model)
		}
		if req.Options.Temperature != 0.5 {
			t.Errorf("temperature = %v, want 0.5", req.Options.Temperature)
		}

		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"message":{"content":"Hi"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":" there"},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":""},"done":true,"prompt_eval_count":10,"eval_count":4}`)
	}))
	defer srv.Close()

	p := New(srv.URL)
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model:       "qwen3",
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		Temperature: 0.5,
		Stream:      true,
	})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	text, usage, err := collect(t, events)
	if err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if text != "Hi there" {
		t.Errorf("text = %q, want %q", text, "Hi there")
	}
	if usage == nil {
		t.Fatal("usage missing")
	}
	if usage.PromptTokens != 10 || usage.CompletionTokens != 4 || usage.Estimated {
		t.Errorf("usage = %+v, want prompt 10 completion 4 not estimated", usage)
	}
}

func TestChatStreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"error":"model not loaded"}`)
	}))
	defer srv.Close()

	p := New(srv.URL)
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Stream: true})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	_, _, err = collect(t, events)
	if err == nil || !strings.Contains(err.Error(), "model not loaded") {
		t.Errorf("err = %v, want in-stream error surfaced", err)
	}
}

func TestChatHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"error":"no such model"}`, http.StatusNotFound)
	}))
	defer srv.Close()

	p := New(srv.URL)
	_, err := p.Chat(context.Background(), provider.ChatRequest{Model: "missing"})
	if err == nil {
		t.Fatal("expected error for 404 response")
	}
}

func TestListModels(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			t.Errorf("path = %s, want /api/tags", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "qwen3:latest", "details": map[string]string{"parameter_size": "8B"}},
			},
		})
	}))
	defer srv.Close()

	p := New(srv.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 1 || models[0].ID != "qwen3:latest" || models[0].Description != "8B" {
		t.Errorf("models = %+v", models)
	}
	if models[0].Vision != nil {
		t.Errorf("Vision = %v, want nil when the server omits capabilities (older Ollama)", models[0].Vision)
	}
}

func TestListModelsUsesCapabilities(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{
			"models": []map[string]any{
				{"name": "qwythos-9b", "capabilities": []string{"completion", "vision"}},
				{"name": "qwen3:8b", "capabilities": []string{"completion", "tools"}},
			},
		})
	}))
	defer srv.Close()

	p := New(srv.URL)
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	byID := map[string]*bool{}
	for _, m := range models {
		byID[m.ID] = m.Vision
	}
	if v := byID["qwythos-9b"]; v == nil || !*v {
		t.Errorf("qwythos-9b Vision = %v, want true", v)
	}
	if v := byID["qwen3:8b"]; v == nil || *v {
		t.Errorf("qwen3:8b Vision = %v, want false", v)
	}
}

func TestHealthCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "Ollama is running")
	}))
	defer srv.Close()

	if err := New(srv.URL).HealthCheck(context.Background()); err != nil {
		t.Errorf("HealthCheck: %v", err)
	}
	if err := New("http://127.0.0.1:1").HealthCheck(context.Background()); err == nil {
		t.Error("expected failure for unreachable server")
	}
}

func TestDefaultBaseURL(t *testing.T) {
	p := New("")
	if p.baseURL != "http://localhost:11434" {
		t.Errorf("baseURL = %q, want default", p.baseURL)
	}
}

// captureChatBody sends req through a Chat call to a stub server and returns
// the raw JSON request body, so tests can assert on exact wire shape
// (presence or absence of a field) rather than a decoded struct's zero value.
func captureChatBody(t *testing.T, req provider.ChatRequest) string {
	t.Helper()
	var body string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
		}
		body = string(raw)
		fmt.Fprint(w, `{"done":true}`)
	}))
	defer srv.Close()

	events, err := New(srv.URL).Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	for range events {
	}
	return body
}

func TestChatSendsThinkWhenReasoningSet(t *testing.T) {
	for _, tc := range []struct {
		reasoning string
		want      string // substring of the raw body, or "" for absent
	}{
		{"", ""},
		{"on", `"think":true`},
		{"off", `"think":false`},
	} {
		body := captureChatBody(t, provider.ChatRequest{Model: "m", Reasoning: tc.reasoning})
		if tc.want == "" {
			if strings.Contains(body, `"think"`) {
				t.Fatalf("reasoning=%q: body must omit think: %s", tc.reasoning, body)
			}
			continue
		}
		if !strings.Contains(body, tc.want) {
			t.Fatalf("reasoning=%q: body missing %s: %s", tc.reasoning, tc.want, body)
		}
	}
}
