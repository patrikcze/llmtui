package openai

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/testutil"
)

func streamSSE(t *testing.T, body string) []provider.ToolCall {
	t.Helper()
	srv := testutil.NewHTTPServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}))
	defer srv.Close()
	p := New("test", srv.URL+"/v1", "")
	events, err := p.Chat(context.Background(), provider.ChatRequest{Model: "google/gemma-4-e4b", Stream: true,
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "read both files"}}})
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	return collectToolCalls(t, events)
}

// TestLMStudioParallelToolCallsFixture replays a real LM Studio
// (google/gemma-4-e4b) stream captured 2026-09-27 for a response with two
// parallel read_file calls: distinct index per call, id and name on each
// call's first fragment only, arguments split across many chunks, plus
// reasoning_content. It pins that the production format reassembles into
// exactly the two calls the model made.
func TestLMStudioParallelToolCallsFixture(t *testing.T) {
	body, err := os.ReadFile("testdata/lmstudio_gemma4_parallel_tool_calls.sse")
	if err != nil {
		t.Fatal(err)
	}
	calls := streamSSE(t, string(body))
	want := []provider.ToolCall{
		{ID: "CUux58PZnDjaRMFwk68QnitmuJdYc6pI", Name: "read_file", Arguments: `{"path":"readings_a.txt"}`},
		{ID: "OkYyaz115944SEMAs8NBqqIyyVruQEez", Name: "read_file", Arguments: `{"path":"readings_b.txt"}`},
	}
	if len(calls) != len(want) {
		t.Fatalf("calls = %+v, want %d", calls, len(want))
	}
	for i := range want {
		if calls[i] != want[i] {
			t.Errorf("call %d = %+v, want %+v", i, calls[i], want[i])
		}
	}
}

func chunk(fragment string) string {
	return `data: {"choices":[{"delta":{"tool_calls":[` + fragment + `]}}]}` + "\n\n"
}

const finish = "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"tool_calls\"}]}\n\ndata: [DONE]\n\n"

// TestStreamedToolCallsSplitOnConflictingIdentity is the regression for
// audit P3-7: servers that reuse (or omit, decoding as 0) one index for
// every call in a parallel batch used to have both calls' arguments
// concatenated into one invalid call.
func TestStreamedToolCallsSplitOnConflictingIdentity(t *testing.T) {
	cases := map[string]string{
		"reused index, new id": chunk(`{"index":0,"id":"a","function":{"name":"read_file","arguments":"{\"path\":"}}`) +
			chunk(`{"index":0,"function":{"arguments":"\"a.txt\"}"}}`) +
			chunk(`{"index":0,"id":"b","function":{"name":"read_file","arguments":"{\"path\":\"b.txt\"}"}}`),
		"omitted index, new id": chunk(`{"id":"a","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}`) +
			chunk(`{"id":"b","function":{"name":"read_file","arguments":"{\"path\":\"b.txt\"}"}}`),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			calls := streamSSE(t, body+finish)
			if len(calls) != 2 {
				t.Fatalf("calls = %+v, want 2 separate calls", calls)
			}
			if calls[0].Arguments != `{"path":"a.txt"}` || calls[1].Arguments != `{"path":"b.txt"}` {
				t.Fatalf("arguments = %q, %q", calls[0].Arguments, calls[1].Arguments)
			}
		})
	}

	t.Run("reused index, different function without ids", func(t *testing.T) {
		calls := streamSSE(t, chunk(`{"index":0,"function":{"name":"list_dir","arguments":"{}"}}`)+
			chunk(`{"index":0,"function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}`)+finish)
		if len(calls) != 2 || calls[0].Name != "list_dir" || calls[1].Name != "read_file" {
			t.Fatalf("calls = %+v, want list_dir then read_file", calls)
		}
	})
}

// TestStreamedToolCallsDoNotSplitOnRepeatedIdentity guards the other side:
// servers that resend the same id and/or name on every fragment of one call
// must still produce a single call.
func TestStreamedToolCallsDoNotSplitOnRepeatedIdentity(t *testing.T) {
	body := chunk(`{"index":0,"id":"a","function":{"name":"read_file","arguments":"{\"pa"}}`) +
		chunk(`{"index":0,"id":"a","function":{"name":"read_file","arguments":"th\":\"a"}}`) +
		chunk(`{"index":0,"id":"a","function":{"name":"read_file","arguments":".txt\"}"}}`) + finish
	calls := streamSSE(t, body)
	if len(calls) != 1 || calls[0].Arguments != `{"path":"a.txt"}` {
		t.Fatalf("calls = %+v, want one call", calls)
	}
	if !strings.HasPrefix(calls[0].ID, "a") {
		t.Fatalf("id = %q", calls[0].ID)
	}
}
