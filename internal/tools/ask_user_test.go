package tools

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestAskUserNativeDecodingAndValidation(t *testing.T) {
	call := CallsFromNative([]provider.ToolCall{{
		ID:        "ask-1",
		Name:      ToolAskUser,
		Arguments: `{"question":" Which environment? ","choices":[" development ","staging"],"allow_text":true}`,
	}})[0]
	if call.InputErr != "" {
		t.Fatalf("InputErr = %q", call.InputErr)
	}
	choices := call.AskUserChoices()
	if call.Question != "Which environment?" || len(choices) != 2 || choices[0] != "development" || !call.AllowText {
		t.Fatalf("call = %+v", call)
	}

	tests := []struct {
		name string
		args string
	}{
		{name: "empty question", args: `{"question":"   "}`},
		{name: "long question", args: `{"question":"` + strings.Repeat("q", MaxAskUserQuestionRunes+1) + `"}`},
		{name: "too many choices", args: `{"question":"pick","choices":["1","2","3","4","5"]}`},
		{name: "blank choice", args: `{"question":"pick","choices":["ok"," "]}`},
		{name: "long choice", args: `{"question":"pick","choices":["` + strings.Repeat("x", MaxAskUserChoiceRunes+1) + `"]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := CallsFromNative([]provider.ToolCall{{Name: ToolAskUser, Arguments: test.args}})[0]
			if got.InputErr == "" {
				t.Fatalf("call accepted invalid arguments: %+v", got)
			}
		})
	}
}

func TestAskUserFencedDecoding(t *testing.T) {
	calls := Parse("```tool ask_user\n{\"question\":\"PostgreSQL version?\"}\n```")
	if len(calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(calls))
	}
	if calls[0].Question != "PostgreSQL version?" || !calls[0].AllowText || calls[0].InputErr != "" {
		t.Fatalf("call = %+v", calls[0])
	}
}

func TestAskUserSpecIsSmallAndExplicit(t *testing.T) {
	for _, spec := range Specs() {
		if spec.Name != ToolAskUser {
			continue
		}
		if !strings.Contains(spec.Description, "Do not use this for tool approval") || !strings.Contains(string(spec.Parameters), `"maxItems": 4`) {
			t.Fatalf("ask_user spec = %+v", spec)
		}
		return
	}
	t.Fatal("ask_user spec missing")
}
