package provider

import (
	"encoding/json"
	"testing"
)

func TestNormalizeToolParameters(t *testing.T) {
	tests := []struct {
		name string
		in   json.RawMessage
		want string
	}{
		{name: "empty schema", want: `{"type":"object","properties":{}}`},
		{name: "null schema", in: json.RawMessage(`null`), want: `{"type":"object","properties":{}}`},
		{name: "object without properties", in: json.RawMessage(`{"type":"object","additionalProperties":false}`), want: `{"additionalProperties":false,"properties":{},"type":"object"}`},
		{name: "object with null properties", in: json.RawMessage(`{"type":"object","properties":null}`), want: `{"properties":{},"type":"object"}`},
		{name: "existing properties", in: json.RawMessage(`{"type":"object","properties":{"value":{"type":"string"}}}`), want: `{"type":"object","properties":{"value":{"type":"string"}}}`},
		{name: "non-object schema", in: json.RawMessage(`{"type":"string"}`), want: `{"type":"string"}`},
		{name: "invalid schema", in: json.RawMessage(`{`), want: `{`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := NormalizeToolParameters(tt.in)
			if string(got) != tt.want {
				t.Fatalf("NormalizeToolParameters() = %s, want %s", got, tt.want)
			}
		})
	}
}
