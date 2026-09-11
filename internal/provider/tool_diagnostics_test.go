package provider

import "testing"

func TestObserveToolCallResponse(t *testing.T) {
	tests := []struct {
		name           string
		model, content string
		calls          []ToolCall
		truncated      bool
		want           ToolCallClassification
	}{
		{name: "ordinary prose", content: "I can use the read_file tool if you want.", want: ToolCallNoIntentObserved},
		{name: "fenced JSON is not a marker", content: "```json\n{\"name\":\"read_file\"}\n```", want: ToolCallNoIntentObserved},
		{name: "native call", calls: []ToolCall{{ID: "c1", Name: "read_file", Arguments: `{}`}}, want: ToolCallNativeReceived},
		{name: "qwen envelope", content: "<function=read_file><parameter=path>a.txt", want: ToolCallSuspectedCensored},
		{name: "tools control envelope", content: "<|tools>{\"name\":\"read_file\"}", want: ToolCallSuspectedCensored},
		{name: "incomplete tool envelope", content: "<tool_call>{\"name\":\"read_file\"", truncated: true, want: ToolCallIncompleteStream},
		{name: "harmony recipient", model: "openai/gpt-oss-20b", content: "to=functions.read_file<|message|>{}", want: ToolCallSuspectedCensored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ObserveToolCallResponse(tt.model, true, tt.content, tt.calls, tt.truncated, false)
			if len(got) < 2 {
				t.Fatalf("diagnostics = %#v, want response and outcome", got)
			}
			if got[len(got)-1].Classification != tt.want {
				t.Fatalf("classification = %q, want %q", got[len(got)-1].Classification, tt.want)
			}
		})
	}
}

func TestObserveToolCallResponseMalformedIsMetadataOnly(t *testing.T) {
	got := ObserveToolCallResponse("gemma", true, "", nil, false, true)
	if got[len(got)-1].Classification != ToolCallProviderParseError {
		t.Fatalf("classification = %q", got[len(got)-1].Classification)
	}
	for _, event := range got {
		if event.ToolName != "" || event.ToolCallID != "" {
			t.Fatalf("unexpected reconstructed identity: %#v", event)
		}
	}
}
