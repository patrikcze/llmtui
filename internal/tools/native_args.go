package tools

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// nativePropertyTypes maps each generic native tool (the ones decoded into
// nativeArgs) to its declared argument names and JSON Schema types, parsed
// once from the same Specs/WebSpecs/SkillSpecs definitions offered to the
// model — so the decoder can never disagree with the schema the model saw.
var nativePropertyTypes = sync.OnceValue(func() map[string]map[string]string {
	out := make(map[string]map[string]string)
	specs := append(append(Specs(), WebSpecs()...), SkillSpecs()...)
	for _, spec := range specs {
		var schema struct {
			Properties map[string]struct {
				Type string `json:"type"`
			} `json:"properties"`
		}
		if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
			continue
		}
		props := make(map[string]string, len(schema.Properties))
		for name, prop := range schema.Properties {
			props[name] = prop.Type
		}
		out[spec.Name] = props
	}
	return out
})

// decodeNativeArgs decodes one generic native tool call's argument object
// against that tool's declared schema. It exists because a plain
// json.Unmarshal into the shared nativeArgs union was strict where small
// models most often slip — "limit":"200" rejected the whole call with a Go
// decoder message — and silent where it matters most: an undeclared key
// such as "start_line" was dropped, so the call could
// succeed with a different meaning than the model intended.
//
// It (1) rejects any key the tool does not declare, naming the keys it
// does accept; (2) coerces only unambiguous scalar spellings — a string
// holding a base-10 integer, or an integral JSON number such as 200.0, for
// an integer property, and "true"/"false" for a boolean property — and
// reports each coercion in notes; and (3) turns remaining type mismatches
// into schema-oriented messages. Apart from the fixed argumentAliases
// (file_path for path), nothing is renamed or guessed. Notes carry
// only the declared property name and target type ("limit=integer"), never
// an argument value, so they are safe for content-free diagnostics. A tool
// with no declared schema here keeps the previous plain decode.
func decodeNativeArgs(tool, raw string) (args nativeArgs, notes []string, err error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return args, nil, nil
	}
	props, known := nativePropertyTypes()[tool]
	if !known {
		if err := json.Unmarshal([]byte(trimmed), &args); err != nil {
			return args, nil, err
		}
		return args, nil, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &fields); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) {
			return args, nil, fmt.Errorf("arguments must be a JSON object, got %s", typeErr.Value)
		}
		return args, nil, err // malformed JSON: keep the parser's own position detail
	}
	if fields == nil {
		return args, nil, fmt.Errorf("arguments must be a JSON object, got null")
	}
	aliasNotes, err := applyArgumentAliases(tool, props, fields)
	if err != nil {
		return args, nil, err
	}
	notes = append(notes, aliasNotes...)
	var unknown []string
	for key, value := range fields {
		kind, ok := props[key]
		if !ok {
			unknown = append(unknown, key)
			continue
		}
		fixed, note, ok := coerceScalar(key, kind, value)
		if ok {
			fields[key] = fixed
			notes = append(notes, note)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		allowed := make([]string, 0, len(props))
		for key := range props {
			allowed = append(allowed, key)
		}
		sort.Strings(allowed)
		quoted := make([]string, len(unknown))
		for i, key := range unknown {
			quoted[i] = strconv.Quote(key)
		}
		return args, nil, fmt.Errorf("unknown argument(s) %s; %s accepts only: %s",
			strings.Join(quoted, ", "), tool, strings.Join(allowed, ", "))
	}
	sort.Strings(notes)
	normalized, err := json.Marshal(fields)
	if err != nil {
		return args, nil, err
	}
	if err := json.Unmarshal(normalized, &args); err != nil {
		var typeErr *json.UnmarshalTypeError
		if errors.As(err, &typeErr) && typeErr.Field != "" {
			want := props[typeErr.Field]
			if want == "" {
				want = typeErr.Type.String()
			}
			return args, nil, fmt.Errorf("%q must be %s %s, got %s", typeErr.Field, article(want), want, typeErr.Value)
		}
		return args, nil, err
	}
	return args, notes, nil
}

// argumentAliases maps a commonly produced argument name to the declared
// one it unambiguously means. Only names models actually emit belong here;
// every other undeclared key is still rejected.
var argumentAliases = map[string]string{
	"file_path": "path",
}

// applyArgumentAliases renames alias keys in fields to their declared name,
// for tools that declare the target and not the alias itself. The value is
// untouched, so the renamed path goes through exactly the same confinement
// as one sent as "path". A call that sends both spellings is rejected
// rather than guessed. Each rename is noted as "alias=target" (names only,
// never a value).
func applyArgumentAliases(tool string, props map[string]string, fields map[string]json.RawMessage) ([]string, error) {
	var notes []string
	for alias, target := range argumentAliases {
		value, present := fields[alias]
		if !present {
			continue
		}
		if _, declared := props[alias]; declared {
			continue
		}
		if _, declared := props[target]; !declared {
			continue
		}
		if _, both := fields[target]; both {
			return nil, fmt.Errorf("both %q and %q given; %s accepts only %q", alias, target, tool, target)
		}
		fields[target] = value
		delete(fields, alias)
		notes = append(notes, alias+"="+target)
	}
	return notes, nil
}

// coerceScalar rewrites one argument value only when its intended scalar is
// unambiguous. ok reports that a rewrite happened; note names the property
// and target type only.
func coerceScalar(key, kind string, value json.RawMessage) (fixed json.RawMessage, note string, ok bool) {
	value = bytes.TrimSpace(value)
	switch kind {
	case "integer":
		var text string
		if json.Unmarshal(value, &text) == nil {
			n, err := strconv.ParseInt(strings.TrimSpace(text), 10, 64)
			if err != nil {
				return nil, "", false
			}
			return json.RawMessage(strconv.FormatInt(n, 10)), key + "=integer", true
		}
		var number float64
		if json.Unmarshal(value, &number) == nil && !bytes.ContainsAny(value, "\"") {
			if _, err := strconv.ParseInt(string(value), 10, 64); err == nil {
				return nil, "", false // already an integer literal
			}
			if number == math.Trunc(number) && math.Abs(number) < 1<<53 {
				n := int64(number)
				return json.RawMessage(strconv.FormatInt(n, 10)), key + "=integer", true
			}
		}
	case "boolean":
		var text string
		if json.Unmarshal(value, &text) == nil {
			switch strings.ToLower(strings.TrimSpace(text)) {
			case "true":
				return json.RawMessage("true"), key + "=boolean", true
			case "false":
				return json.RawMessage("false"), key + "=boolean", true
			}
		}
	}
	return nil, "", false
}

func article(kind string) string {
	if kind != "" && strings.ContainsRune("aeiou", rune(kind[0])) {
		return "an"
	}
	return "a"
}
