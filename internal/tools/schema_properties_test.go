package tools

import (
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/personalapps"
	"github.com/patrikcze/llmtui/internal/provider"
)

// TestBuiltinToolSchemasDeclareObjectProperties guards against an object
// schema with no "properties" key anywhere in a built-in tool. Strict
// schema-to-grammar servers (Apple's Foundation Models `fm serve`) reject the
// whole request with a 400 when any one tool has such a node, which
// disabled every tool for that provider while change_prepare declared its
// items as a bare {"type":"object"}.
func TestBuiltinToolSchemasDeclareObjectProperties(t *testing.T) {
	var specs []provider.ToolSpec
	specs = append(specs, Specs()...)
	specs = append(specs, WebSpecs()...)
	specs = append(specs, PersonalAppsSpecs()...)
	specs = append(specs, SkillSpecs()...)
	specs = append(specs, ThinkingSpecs()...)
	for _, s := range specs {
		var schema any
		if err := json.Unmarshal(s.Parameters, &schema); err != nil {
			t.Fatalf("%s: parameters are not valid JSON: %v", s.Name, err)
		}
		for _, path := range objectsWithoutProperties(schema, "$") {
			t.Errorf("%s: object schema at %s has no properties", s.Name, path)
		}
	}
}

func objectsWithoutProperties(node any, path string) []string {
	var out []string
	switch v := node.(type) {
	case map[string]any:
		if v["type"] == "object" {
			if _, ok := v["properties"]; !ok {
				out = append(out, path)
			}
		}
		for k, child := range v {
			out = append(out, objectsWithoutProperties(child, path+"."+k)...)
		}
	case []any:
		for _, child := range v {
			out = append(out, objectsWithoutProperties(child, path+"[]")...)
		}
	}
	return out
}

// TestChangePrepareItemSchemaMirrorsChangeVariants checks that every field
// of every change variant struct is declared on change_prepare's items, with
// "type" restricted to the supported change types. A server that turns the
// schema into a grammar only lets the model emit declared fields.
func TestChangePrepareItemSchemaMirrorsChangeVariants(t *testing.T) {
	var spec provider.ToolSpec
	for _, s := range PersonalAppsSpecs() {
		if s.Name == string(personalapps.OpChangePrepare) {
			spec = s
		}
	}
	var schema struct {
		Properties struct {
			Changes struct {
				Items struct {
					Type                 string                    `json:"type"`
					Properties           map[string]map[string]any `json:"properties"`
					Required             []string                  `json:"required"`
					AdditionalProperties *bool                     `json:"additionalProperties"`
				} `json:"items"`
			} `json:"changes"`
		} `json:"properties"`
	}
	if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
		t.Fatalf("change_prepare parameters: %v", err)
	}
	items := schema.Properties.Changes.Items
	if items.Type != "object" {
		t.Fatalf("changes items type = %q, want object", items.Type)
	}
	if !slices.Equal(items.Required, []string{"type"}) {
		t.Errorf("changes items required = %v, want [type]", items.Required)
	}
	if items.AdditionalProperties == nil || *items.AdditionalProperties {
		t.Error("changes items allow additional properties")
	}

	var enum []string
	if raw, ok := items.Properties["type"]["enum"].([]any); ok {
		for _, e := range raw {
			enum = append(enum, e.(string))
		}
	}
	var want []string
	for _, ct := range personalapps.ChangeTypes() {
		want = append(want, string(ct))
	}
	if !slices.Equal(enum, want) {
		t.Errorf("changes items type enum = %v, want %v", enum, want)
	}

	variants := []any{
		personalapps.MailMoveChange{},
		personalapps.MailSetReadChange{},
		personalapps.MailSetFlagChange{},
		personalapps.MailSaveDraftChange{},
		personalapps.CalendarCreateEventChange{},
		personalapps.CalendarUpdateEventChange{},
	}
	for _, v := range variants {
		rt := reflect.TypeOf(v)
		for i := range rt.NumField() {
			f := rt.Field(i)
			if f.Anonymous {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			prop, ok := items.Properties[name]
			if !ok {
				t.Errorf("%s.%s: field %q is not declared on changes items", rt.Name(), f.Name, name)
				continue
			}
			if got, want := prop["type"], jsonSchemaTypeOf(f.Type); got != want {
				t.Errorf("%s.%s: declared type %v, want %s", rt.Name(), f.Name, got, want)
			}
		}
	}
}

func jsonSchemaTypeOf(t reflect.Type) string {
	if t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Bool:
		return "boolean"
	case reflect.Slice:
		return "array"
	default:
		// Handle, string, Timestamp (RFC3339) and DateOnly (YYYY-MM-DD) all
		// travel as JSON strings.
		return "string"
	}
}
