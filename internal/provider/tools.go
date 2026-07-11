package provider

import "encoding/json"

var emptyObjectToolParameters = json.RawMessage(`{"type":"object","properties":{}}`)

// NormalizeToolParameters makes object-shaped function schemas acceptable to
// strict OpenAI-compatible backends. JSON Schema allows an object schema to
// omit properties, but some backends require an explicit empty object.
func NormalizeToolParameters(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return append(json.RawMessage(nil), emptyObjectToolParameters...)
	}

	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		return raw
	}
	if schema["type"] != "object" {
		return raw
	}
	if properties, ok := schema["properties"]; ok && properties != nil {
		return raw
	}
	schema["properties"] = map[string]any{}
	normalized, err := json.Marshal(schema)
	if err != nil {
		return raw
	}
	return normalized
}
