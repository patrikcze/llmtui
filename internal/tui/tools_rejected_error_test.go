package tui

import (
	"errors"
	"testing"
)

func TestToolsRejectedError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ollama model without tools", errors.New(`chat request: status 400: {"error":"registry.ollama.ai/library/gemma:2b does not support tools"}`), true},
		{"tools not supported", errors.New("chat request: status 400: tools are not supported for this model"), true},
		{"vllm auto tool choice", errors.New(`chat request: status 400: "auto" tool choice requires --enable-auto-tool-choice and --tool-call-parser to be set`), true},
		{"unrelated 400", errors.New("chat request: status 400: context length exceeded"), false},
		// Audit P3-8: history argument errors are request-content errors.
		// Dropping tool specs cannot fix them, so they must not downgrade
		// the session to the fenced protocol.
		{"ollama v1 history arguments", errors.New(`chat request: status 400: {"error":{"message":"invalid tool call arguments","type":"invalid_request_error","param":null,"code":null}}`), false},
		{"llama.cpp history arguments", errors.New(`chat request: status 500: {"error":{"code":500,"message":"Failed to parse tool call arguments as JSON: [json.exception.parse_error.101] parse error at line 1, column 2: syntax error while parsing value - invalid literal; last read: 'p'","type":"server_error"}}`), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolsRejectedError(tt.err); got != tt.want {
				t.Fatalf("toolsRejectedError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
