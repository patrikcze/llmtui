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

func collectToolCalls(t *testing.T, events <-chan provider.ChatEvent) []provider.ToolCall {
	t.Helper()
	var calls []provider.ToolCall
	for ev := range events {
		switch ev.Type {
		case provider.EventDone:
			calls = ev.ToolCalls
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	return calls
}

func TestChatRequestEncodesToolsAndToolMessages(t *testing.T) {
	var got chatCompletionRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer srv.Close()

	p := New("test", srv.URL+"/v1", "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{
		Model: "m",
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "list files"},
			{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "list_dir", Arguments: `{"path":""}`},
			}},
			{Role: provider.RoleTool, Content: "a.txt", ToolCallID: "call_1", ToolName: "list_dir"},
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
	var parameters map[string]any
	if err := json.Unmarshal(got.Tools[0].Function.Parameters, &parameters); err != nil {
		t.Fatalf("decode tool parameters: %v", err)
	}
	if _, ok := parameters["properties"].(map[string]any); !ok {
		t.Fatalf("tool parameters lack an object properties field: %s", got.Tools[0].Function.Parameters)
	}
	if len(got.Messages) != 3 {
		t.Fatalf("messages = %d, want 3", len(got.Messages))
	}
	assistant := got.Messages[1]
	if len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].ID != "call_1" ||
		assistant.ToolCalls[0].Function.Name != "list_dir" ||
		assistant.ToolCalls[0].Function.Arguments != `{"path":""}` {
		t.Errorf("assistant tool_calls = %+v", assistant.ToolCalls)
	}
	toolMsg := got.Messages[2]
	if toolMsg.Role != "tool" || toolMsg.ToolCallID != "call_1" {
		t.Errorf("tool message = %+v", toolMsg)
	}
}

func TestChatStreamingAccumulatesToolCallFragments(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		// The id and name arrive on the first fragment; the JSON arguments in
		// pieces across chunks, keyed by index.
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"call_9\",\"function\":{\"name\":\"read_file\",\"arguments\":\"{\\\"pa\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"th\\\":\\\"a.txt\\\"}\"}}]}}]}\n\n")
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\n")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	p := New("test", srv.URL+"/v1", "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m", Stream: true,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "read a.txt"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	calls := collectToolCalls(t, events)
	if len(calls) != 1 {
		t.Fatalf("tool calls = %d, want 1", len(calls))
	}
	want := provider.ToolCall{ID: "call_9", Name: "read_file", Arguments: `{"path":"a.txt"}`}
	if calls[0] != want {
		t.Errorf("call = %+v, want %+v", calls[0], want)
	}
}

func TestToolCallAccumulatorRejectsUnboundedAndNamelessCalls(t *testing.T) {
	var tooMany toolCallAccumulator
	for i := 0; i <= provider.MaxToolCalls; i++ {
		var fragment streamToolCall
		fragment.Index = i
		fragment.Function.Name = "read_file"
		if err := tooMany.add([]streamToolCall{fragment}); err != nil {
			if i != provider.MaxToolCalls {
				t.Fatalf("rejected at %d calls: %v", i, err)
			}
			break
		}
	}

	var oversized toolCallAccumulator
	var fragment streamToolCall
	fragment.Index = 0
	fragment.Function.Name = "read_file"
	fragment.Function.Arguments = strings.Repeat("x", provider.MaxToolCallArgumentBytes+1)
	if err := oversized.add([]streamToolCall{fragment}); err == nil {
		t.Fatal("oversized arguments were accepted")
	}

	var nameless toolCallAccumulator
	if err := nameless.add([]streamToolCall{{Index: 0}}); err != nil {
		t.Fatalf("add nameless fragment: %v", err)
	}
	if _, err := nameless.result(); err == nil {
		t.Fatal("nameless tool call was accepted at stream completion")
	}
}

func TestChatNonStreamingToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":null,"tool_calls":[
			{"id":"call_2","type":"function","function":{"name":"list_dir","arguments":"{}"}}
		]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`)
	}))
	defer srv.Close()

	p := New("test", srv.URL+"/v1", "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "list"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	calls := collectToolCalls(t, events)
	if len(calls) != 1 || calls[0].ID != "call_2" || calls[0].Name != "list_dir" {
		t.Errorf("calls = %+v", calls)
	}
}

// TestChatNonStreamingPreservesReasoningAlongsideToolCalls guards a real
// regression: a reasoning-capable model (e.g. gemma-4-e4b via LM Studio)
// deciding to call a tool still emits reasoning_content explaining why. The
// non-streaming path only ever read reasoning_content as a content
// fallback when there were NO tool calls, so on every ordinary
// think-then-call-a-tool turn the reasoning was silently dropped — never
// reaching the transcript, history, or the thoughts header.
func TestChatNonStreamingPreservesReasoningAlongsideToolCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"content":"","reasoning_content":"I should list the directory first.","tool_calls":[
			{"id":"call_3","type":"function","function":{"name":"list_dir","arguments":"{}"}}
		]},"finish_reason":"tool_calls"}]}`)
	}))
	defer srv.Close()

	p := New("test", srv.URL+"/v1", "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "m",
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "list the files"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	var reasoning string
	var done provider.ChatEvent
	for ev := range events {
		switch ev.Type {
		case provider.EventReasoning:
			reasoning += ev.Delta
		case provider.EventDone:
			done = ev
		case provider.EventError:
			t.Fatalf("stream error: %v", ev.Err)
		}
	}
	if reasoning != "I should list the directory first." {
		t.Errorf("reasoning = %q, want the model's reasoning_content preserved even though tool_calls were present", reasoning)
	}
	if len(done.ToolCalls) != 1 || done.ToolCalls[0].ID != "call_3" || done.ToolCalls[0].Name != "list_dir" {
		t.Errorf("ToolCalls = %+v, want the list_dir call untouched", done.ToolCalls)
	}
}
