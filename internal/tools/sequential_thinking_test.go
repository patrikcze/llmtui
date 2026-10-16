package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func thinkingCall(t *testing.T, args string) Call {
	t.Helper()
	c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolSequentialThinking, Arguments: args}})[0]
	if c.InputErr != "" {
		t.Fatalf("decode %s: %s", args, c.InputErr)
	}
	return c
}

func thinkingRunner(t *testing.T) *Runner {
	t.Helper()
	r := NewRunner(t.TempDir(), 64)
	r.SequentialThinking = true
	return r
}

func TestSequentialThinkingStatusAndBranches(t *testing.T) {
	r := thinkingRunner(t)
	steps := []string{
		`{"thought":"understand the bug","thought_number":1,"total_thoughts":3,"next_thought_needed":true}`,
		`{"thought":"try approach A","thought_number":2,"total_thoughts":3,"next_thought_needed":true,"branch_from_thought":1,"branch_id":"a"}`,
		`{"thoughtNumber":3,"totalThoughts":2,"nextThoughtNeeded":false,"thought":"actually step 1 missed X","isRevision":true,"revisesThought":1}`,
	}
	var last thinkingStatus
	for _, args := range steps {
		res := r.Execute(thinkingCall(t, args))
		if res.Err != nil {
			t.Fatalf("step %s: %v", args, res.Err)
		}
		if err := json.Unmarshal([]byte(res.Output), &last); err != nil {
			t.Fatalf("status is not JSON: %v (%s)", err, res.Output)
		}
		if res.Meta.Effect != EffectNone {
			t.Fatalf("thinking must have no effect, got %q", res.Meta.Effect)
		}
		if strings.Contains(res.Output, "thought\":\"") || strings.Contains(res.Output, "missed X") {
			t.Fatalf("thought text echoed back: %s", res.Output)
		}
	}
	if last.ThoughtNumber != 3 || last.TotalThoughts != 3 || last.NextThoughtNeeded || last.ThoughtHistoryLength != 3 {
		t.Fatalf("status = %+v (total must grow to the thought number)", last)
	}
	if len(last.Branches) != 1 || last.Branches[0] != "a" {
		t.Fatalf("branches = %v", last.Branches)
	}

	r.ResetThinking()
	res := r.Execute(thinkingCall(t, steps[0]))
	if err := json.Unmarshal([]byte(res.Output), &last); err != nil || last.ThoughtHistoryLength != 1 || len(last.Branches) != 0 {
		t.Fatalf("reset did not clear the log: %s", res.Output)
	}
}

func TestSequentialThinkingIsBounded(t *testing.T) {
	r := thinkingRunner(t)
	step := `{"thought":"t","thought_number":1,"total_thoughts":1,"next_thought_needed":true}`
	for i := 0; i < MaxThoughts; i++ {
		if res := r.Execute(thinkingCall(t, step)); res.Err != nil {
			t.Fatalf("thought %d: %v", i+1, res.Err)
		}
	}
	res := r.Execute(thinkingCall(t, step))
	if res.Err == nil || res.Meta.Error == nil || res.Meta.Error.Code != "limit_reached" {
		t.Fatalf("expected limit_reached after %d thoughts, got %v", MaxThoughts, res.Err)
	}

	b := thinkingRunner(t)
	for i := 0; i < MaxThinkingBranches; i++ {
		args := `{"thought":"t","thought_number":2,"total_thoughts":2,"next_thought_needed":true,"branch_from_thought":1,"branch_id":"b` + string(rune('a'+i)) + `"}`
		if res := b.Execute(thinkingCall(t, args)); res.Err != nil {
			t.Fatalf("branch %d: %v", i, res.Err)
		}
	}
	extra := `{"thought":"t","thought_number":2,"total_thoughts":2,"next_thought_needed":true,"branch_from_thought":1,"branch_id":"overflow"}`
	if res := b.Execute(thinkingCall(t, extra)); res.Err == nil {
		t.Fatal("expected the branch limit to apply")
	}
	known := `{"thought":"t","thought_number":3,"total_thoughts":3,"next_thought_needed":true,"branch_from_thought":1,"branch_id":"ba"}`
	if res := b.Execute(thinkingCall(t, known)); res.Err != nil {
		t.Fatalf("continuing a known branch at the limit: %v", res.Err)
	}
}

func TestSequentialThinkingRequiresEnablement(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	c := thinkingCall(t, `{"thought":"t","thought_number":1,"total_thoughts":1,"next_thought_needed":false}`)
	if res := r.Execute(c); res.Err == nil {
		t.Fatal("sequential_thinking ran while disabled")
	}
	if r.NeedsApproval(c) {
		t.Fatal("sequential_thinking must never ask")
	}
}

func TestSequentialThinkingValidation(t *testing.T) {
	for name, args := range map[string]string{
		"missing next":         `{"thought":"t","thought_number":1,"total_thoughts":1}`,
		"empty thought":        `{"thought":" ","thought_number":1,"total_thoughts":1,"next_thought_needed":true}`,
		"zero number":          `{"thought":"t","thought_number":0,"total_thoughts":1,"next_thought_needed":true}`,
		"unknown key":          `{"thought":"t","thought_number":1,"total_thoughts":1,"next_thought_needed":true,"path":"x"}`,
		"both spellings":       `{"thought":"t","thought_number":1,"thoughtNumber":1,"total_thoughts":1,"next_thought_needed":true}`,
		"revision of future":   `{"thought":"t","thought_number":2,"total_thoughts":2,"next_thought_needed":true,"is_revision":true,"revises_thought":2}`,
		"revises without flag": `{"thought":"t","thought_number":2,"total_thoughts":2,"next_thought_needed":true,"revises_thought":1}`,
		"branch without id":    `{"thought":"t","thought_number":2,"total_thoughts":2,"next_thought_needed":true,"branch_from_thought":1}`,
		"bad branch id":        `{"thought":"t","thought_number":2,"total_thoughts":2,"next_thought_needed":true,"branch_from_thought":1,"branch_id":"a b\u001b[31m"}`,
		"oversized thought":    `{"thought":"` + strings.Repeat("x", MaxThoughtBytes+1) + `","thought_number":1,"total_thoughts":1,"next_thought_needed":true}`,
		"not an object":        `["t"]`,
	} {
		t.Run(name, func(t *testing.T) {
			c := CallsFromNative([]provider.ToolCall{{ID: "1", Name: ToolSequentialThinking, Arguments: args}})[0]
			if c.InputErr == "" {
				t.Fatalf("expected an input error for %s", args)
			}
		})
	}
}

func TestSequentialThinkingFencedForm(t *testing.T) {
	c := Parse("```tool sequential_thinking\n{\"thought\":\"plan\",\"thought_number\":1,\"total_thoughts\":2,\"next_thought_needed\":true}\n```\n")[0]
	if c.InputErr != "" || c.Body != "plan" || c.Thinking.TotalThoughts != 2 {
		t.Fatalf("fenced decode: %+v", c)
	}
	if got := c.Describe(); got != "sequential_thinking: thought 1/2" {
		t.Fatalf("describe = %q", got)
	}
}
