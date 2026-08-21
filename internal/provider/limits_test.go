package provider

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestDecodeJSONLimitedRejectsOversizedResponse(t *testing.T) {
	payload := `{"value":"` + strings.Repeat("x", MaxResponseBytes) + `"}`
	var out map[string]string
	err := DecodeJSONLimited(strings.NewReader(payload), &out)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("DecodeJSONLimited error = %v, want ErrResponseTooLarge", err)
	}
}

func TestValidateResponseConstraint(t *testing.T) {
	valid := ChatRequest{ResponseConstraint: &ResponseConstraint{
		Grammar: `root ::= "ok"`, JSONSchema: json.RawMessage(`{"type":"object"}`),
	}}
	if err := ValidateResponseConstraint(valid); err != nil {
		t.Fatalf("valid constraint rejected: %v", err)
	}
	for name, req := range map[string]ChatRequest{
		"empty":          {ResponseConstraint: &ResponseConstraint{}},
		"invalid schema": {ResponseConstraint: &ResponseConstraint{JSONSchema: json.RawMessage(`[]`)}},
		"null schema":    {ResponseConstraint: &ResponseConstraint{JSONSchema: json.RawMessage(`null`)}},
		"invalid name":   {ResponseConstraint: &ResponseConstraint{Name: "bad name", Grammar: `root ::= "ok"`}},
		"tools conflict": {
			Tools:              []ToolSpec{{Name: "read_file"}},
			ResponseConstraint: &ResponseConstraint{Grammar: `root ::= "ok"`},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateResponseConstraint(req); err == nil {
				t.Fatal("ValidateResponseConstraint() error = nil")
			}
		})
	}
}

func TestValidateToolCallsRejectsMissingNameAndArgumentFlood(t *testing.T) {
	if err := ValidateToolCalls([]ToolCall{{Arguments: `{}`}}); err == nil {
		t.Fatal("tool call without a name was accepted")
	}
	if err := ValidateToolCalls([]ToolCall{{Name: "read_file", Arguments: strings.Repeat("x", MaxToolCallArgumentBytes+1)}}); err == nil {
		t.Fatal("oversized tool-call arguments were accepted")
	}
}
