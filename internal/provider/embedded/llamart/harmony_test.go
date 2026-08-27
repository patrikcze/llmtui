package llamart

import (
	"errors"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestHarmonyDecoderAnalysisThenFinalStreaming(t *testing.T) {
	t.Parallel()
	decoder := newHarmonyDecoder(nil)
	parts := []string{
		"<|chan", "nel|>analysis<|message|>private rea", "soning<|end|>",
		"<|start|>assistant<|channel|>final<|message|>visible answer<|ret", "urn|>",
	}
	var visible, reasoning strings.Builder
	for _, part := range parts {
		deltas, err := decoder.Push(part)
		if err != nil {
			t.Fatal(err)
		}
		for _, delta := range deltas {
			switch delta.Kind {
			case embedded.DeltaText:
				visible.WriteString(delta.Text)
			case embedded.DeltaReasoning:
				reasoning.WriteString(delta.Text)
			}
		}
	}
	turn, err := decoder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if visible.String() != "visible answer" || reasoning.String() != "private reasoning" {
		t.Fatalf("visible/reasoning = %q/%q", visible.String(), reasoning.String())
	}
	if turn.FinalContent != "visible answer" || turn.Reasoning != "private reasoning" || !turn.Completed || turn.Continuation != nil {
		t.Fatalf("turn = %+v", turn)
	}
}

func TestHarmonyDecoderToolCallPreservesReasoning(t *testing.T) {
	t.Parallel()
	decoder := newHarmonyDecoder([]provider.ToolSpec{{Name: "read_file", Parameters: []byte(`{"type":"object","properties":{"path":{"type":"string"}},"required":["path"]}`)}})
	raw := "<|channel|>analysis<|message|>private plan<|end|>" +
		"<|start|>assistant<|channel|>commentary to=functions.read_file <|constrain|>json<|message|>{\"path\":\"a.txt\"}<|call|>"
	if _, err := decoder.Push(raw); err != nil {
		t.Fatal(err)
	}
	turn, err := decoder.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(turn.ToolCalls) != 1 || turn.ToolCalls[0].Name != "read_file" || turn.ToolCalls[0].Arguments != `{"path":"a.txt"}` {
		t.Fatalf("tool calls = %+v", turn.ToolCalls)
	}
	if turn.Continuation == nil || turn.Continuation.Reasoning != "private plan" || turn.Completed {
		t.Fatalf("continuation = %+v, completed=%v", turn.Continuation, turn.Completed)
	}
}

func TestHarmonyDecoderRejectsMalformedOrTruncatedOutput(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{
		"plain untyped answer",
		"<|channel|>analysis<|message|>unfinished",
		"<|channel|>final<|message|>answer<|call|>",
		"<|channel|>commentary to=functions.unknown<|message|>{}<|call|>",
	} {
		decoder := newHarmonyDecoder(nil)
		_, pushErr := decoder.Push(raw)
		_, finishErr := decoder.Finish()
		if pushErr == nil && finishErr == nil {
			t.Fatalf("malformed completion accepted: %q", raw)
		}
		if pushErr != nil && !errors.Is(pushErr, errMalformedHarmony) && !strings.Contains(pushErr.Error(), "unknown tool") {
			t.Fatalf("unexpected error: %v", pushErr)
		}
	}
}
