package openai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/testutil"
)

// ollamaV1InvalidArgumentsBody is the exact response Ollama v0.34.4's
// /v1/chat/completions middleware returned (captured 2026-09-27) for a
// history tool call whose arguments string is not valid JSON.
const ollamaV1InvalidArgumentsBody = `{"error":{"message":"invalid tool call arguments","type":"invalid_request_error","param":null,"code":null}}`

// TestChatReplaysInvalidHistoryToolArgumentsAsEmptyObject is the audit P3-8
// regression. The stub parses every history tool call's arguments the way
// Ollama's /v1 middleware (and llama.cpp's server) do and rejects the whole
// request when one does not parse, so before the fix one malformed or empty
// call failed every later request in the session.
func TestChatReplaysInvalidHistoryToolArgumentsAsEmptyObject(t *testing.T) {
	tests := []struct {
		name, args, want string
	}{
		{"valid", `{"path":"a.txt"}`, `{"path":"a.txt"}`},
		{"truncated", `{"path":"a.txt"`, `{}`},
		{"empty", ``, `{}`},
		{"whitespace", "  ", `{}`},
		{"not json", `path=a.txt`, `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req chatCompletionRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Errorf("decode request: %v", err)
				}
				for _, msg := range req.Messages {
					for _, call := range msg.ToolCalls {
						got = call.Function.Arguments
						var parsed map[string]any
						if err := json.Unmarshal([]byte(call.Function.Arguments), &parsed); err != nil {
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprint(w, ollamaV1InvalidArgumentsBody)
							return
						}
					}
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
			}))
			defer srv.Close()

			p := New("test", srv.URL+"/v1", "")
			events, err := p.Chat(context.Background(), provider.ChatRequest{
				Model: "m",
				Messages: []provider.Message{
					{Role: provider.RoleUser, Content: "read a.txt"},
					{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: "read_file", Arguments: tt.args}}},
					{Role: provider.RoleTool, Content: "invalid arguments for read_file", ToolCallID: "c1", ToolName: "read_file"},
				},
			})
			if err != nil {
				t.Fatalf("chat: %v", err)
			}
			for ev := range events {
				if ev.Type == provider.EventError {
					t.Fatalf("stream error: %v", ev.Err)
				}
			}
			if got != tt.want {
				t.Fatalf("replayed arguments = %q, want %q", got, tt.want)
			}
		})
	}
}
