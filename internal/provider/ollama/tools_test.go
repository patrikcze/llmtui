package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestChatRequestEncodesToolsAndToolMessages(t *testing.T) {
	var got chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		fmt.Fprint(w, `{"message":{"content":"ok"},"done":true}`)
	}))
	defer srv.Close()

	p := New(srv.URL)
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model: "m",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "list files"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{Name: "list_dir", Arguments: `{"path":""}`},
			}},
			{Role: provider.RoleTool, Content: "a.txt", ToolName: "list_dir"},
		},
		Tools: []provider.ToolSpec{{
			Name:        "list_dir",
			Description: "list a directory",
			Parameters:  json.RawMessage(`{"type":"object"}`),
		}},
	})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	for range events {
	}

	if len(got.Tools) != 1 || got.Tools[0].Type != "function" || got.Tools[0].Function.Name != "list_dir" {
		t.Errorf("tools = %+v", got.Tools)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(got.Messages))
	}
	assistant := got.Messages[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Function.Name != "list_dir" ||
		string(assistant.ToolCalls[0].Function.Arguments) != `{"path":""}` {
		t.Errorf("assistant tool_calls = %+v", assistant.ToolCalls)
	}
	toolMsg := got.Messages[2]
	if toolMsg.Role != "tool" || toolMsg.ToolName != "list_dir" || toolMsg.Content != "a.txt" {
		t.Errorf("tool message = %+v", toolMsg)
	}
}

func TestChatParsesNativeToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Ollama sends arguments as a JSON object, not a string.
		fmt.Fprintln(w, `{"message":{"content":"","tool_calls":[{"function":{"name":"read_file","arguments":{"path":"a.txt"}}}]},"done":false}`)
		fmt.Fprintln(w, `{"message":{"content":""},"done":true,"prompt_eval_count":8,"eval_count":4}`)
	}))
	defer srv.Close()

	p := New(srv.URL)
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Stream: true,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "read a.txt"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	var calls []provider.ToolCall
	for ev := range events {
		if ev.Type == provider.EventError {
			t.Fatalf("stream error: %v", ev.Err)
		}
		if ev.Type == provider.EventDone {
			calls = ev.ToolCalls
		}
	}
	if len(calls) != 1 || calls[0].Name != "read_file" {
		t.Fatalf("calls = %+v", calls)
	}
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(calls[0].Arguments), &args); err != nil || args.Path != "a.txt" {
		t.Errorf("arguments = %q (err %v)", calls[0].Arguments, err)
	}
}
