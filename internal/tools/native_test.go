package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestSpecsAreValidJSONSchemas(t *testing.T) {
	specs := Specs()
	if len(specs) != 4 {
		t.Fatalf("specs = %d, want 4", len(specs))
	}
	names := map[string]bool{}
	for _, s := range specs {
		names[s.Name] = true
		var schema map[string]any
		if err := json.Unmarshal(s.Parameters, &schema); err != nil {
			t.Errorf("%s: parameters are not valid JSON: %v", s.Name, err)
		}
		if schema["type"] != "object" {
			t.Errorf("%s: schema type = %v, want object", s.Name, schema["type"])
		}
		if s.Description == "" {
			t.Errorf("%s: missing description", s.Name)
		}
	}
	for _, want := range []string{ToolListDir, ToolReadFile, ToolWriteFile, ToolRunCommand} {
		if !names[want] {
			t.Errorf("missing spec for %s", want)
		}
	}
}

func TestCallsFromNative(t *testing.T) {
	tests := []struct {
		name string
		in   provider.ToolCall
		want Call
	}{
		{
			name: "read_file",
			in:   provider.ToolCall{ID: "c1", Name: ToolReadFile, Arguments: `{"path":"a.txt"}`},
			want: Call{ID: "c1", Tool: ToolReadFile, Path: "a.txt"},
		},
		{
			name: "write_file",
			in:   provider.ToolCall{ID: "c2", Name: ToolWriteFile, Arguments: `{"path":"b.txt","content":"data"}`},
			want: Call{ID: "c2", Tool: ToolWriteFile, Path: "b.txt", Body: "data"},
		},
		{
			name: "run_command",
			in:   provider.ToolCall{ID: "c3", Name: ToolRunCommand, Arguments: `{"command":"ls"}`},
			want: Call{ID: "c3", Tool: ToolRunCommand, Body: "ls"},
		},
		{
			name: "missing id is filled",
			in:   provider.ToolCall{Name: ToolListDir, Arguments: `{}`},
			want: Call{ID: "call_0", Tool: ToolListDir},
		},
		{
			name: "malformed arguments still produce a call",
			in:   provider.ToolCall{ID: "c4", Name: ToolReadFile, Arguments: `{not json`},
			want: Call{ID: "c4", Tool: ToolReadFile},
		},
		{
			name: "unknown tool passes through for the runner to report",
			in:   provider.ToolCall{ID: "c5", Name: "delete_everything", Arguments: `{}`},
			want: Call{ID: "c5", Tool: "delete_everything"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CallsFromNative([]provider.ToolCall{tc.in})
			if len(got) != 1 {
				t.Fatalf("calls = %d, want 1", len(got))
			}
			if got[0] != tc.want {
				t.Errorf("call = %+v, want %+v", got[0], tc.want)
			}
		})
	}
}

func TestNativeResults(t *testing.T) {
	results := []Result{
		{Call: Call{ID: "c1", Tool: ToolListDir}, Output: "a.txt\nb.txt"},
		{Call: Call{ID: "c2", Tool: ToolReadFile, Path: "x"}, Err: ErrDenied},
	}
	msgs := NativeResults(results)
	if len(msgs) != 2 {
		t.Fatalf("messages = %d, want 2", len(msgs))
	}
	if msgs[0].Role != provider.RoleTool || msgs[0].ToolCallID != "c1" ||
		msgs[0].ToolName != ToolListDir || msgs[0].Content != "a.txt\nb.txt" {
		t.Errorf("msg[0] = %+v", msgs[0])
	}
	if msgs[1].ToolCallID != "c2" || !strings.Contains(msgs[1].Content, "denied by the user") {
		t.Errorf("msg[1] = %+v", msgs[1])
	}
}

func TestLimitResults(t *testing.T) {
	calls := []Call{{ID: "c1", Tool: ToolListDir}, {ID: "c2", Tool: ToolReadFile}}
	results := LimitResults(calls, 10)
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	for _, res := range results {
		if res.Err == nil || !strings.Contains(res.Err.Error(), "iteration limit") {
			t.Errorf("result err = %v, want iteration-limit explanation", res.Err)
		}
		if !strings.Contains(res.Err.Error(), "final answer") {
			t.Errorf("result err = %v, want wrap-up instruction", res.Err)
		}
	}
}

func TestNativeInstructionsMentionRootAndRules(t *testing.T) {
	s := NativeInstructions("/work/proj")
	for _, want := range []string{"/work/proj", "relative", "approval", "final answer"} {
		if !strings.Contains(s, want) {
			t.Errorf("native instructions missing %q", want)
		}
	}
	if strings.Contains(s, "```") {
		t.Error("native instructions must not teach the fenced protocol")
	}
}
