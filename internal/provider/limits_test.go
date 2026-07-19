package provider

import (
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

func TestValidateToolCallsRejectsMissingNameAndArgumentFlood(t *testing.T) {
	if err := ValidateToolCalls([]ToolCall{{Arguments: `{}`}}); err == nil {
		t.Fatal("tool call without a name was accepted")
	}
	if err := ValidateToolCalls([]ToolCall{{Name: "read_file", Arguments: strings.Repeat("x", MaxToolCallArgumentBytes+1)}}); err == nil {
		t.Fatal("oversized tool-call arguments were accepted")
	}
}
