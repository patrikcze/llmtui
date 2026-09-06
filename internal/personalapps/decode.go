package personalapps

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

// envelope is the wire shape of every personal_apps call. Both the native
// tool-call protocol and the fenced-block fallback decode through this one
// function, so the two protocols cannot drift into different validation.
type envelope struct {
	Operation string          `json:"operation"`
	Arguments json.RawMessage `json:"arguments,omitempty"`
}

// ParseRequest decodes and validates one raw personal_apps payload.
//
// It is deliberately strict. A duplicate key, trailing JSON after the
// object, an unknown field, a field belonging to a different operation,
// excessive nesting, an oversized payload or an invalid enum is rejected
// before any adapter is consulted, because a lenient decoder is how a
// request ends up meaning something other than what the human reviewed.
func ParseRequest(raw []byte, l Limits) (Request, error) {
	full := l.withDefaults()
	if len(raw) == 0 {
		return Request{}, Errorf(CodeInvalidRequest, "the request is empty")
	}
	if len(raw) > full.MaxRequestBytes {
		return Request{}, Errorf(CodeInvalidRequest, "the request is %d bytes, the maximum is %d", len(raw), full.MaxRequestBytes)
	}
	if !utf8.Valid(raw) {
		return Request{}, Errorf(CodeInvalidRequest, "the request is not valid UTF-8")
	}
	if err := scanStrict(raw, full.MaxRequestDepth); err != nil {
		return Request{}, err
	}

	var env envelope
	if err := strictUnmarshal(raw, &env); err != nil {
		return Request{}, err
	}
	op := Operation(strings.TrimSpace(env.Operation))
	if op == "" {
		return Request{}, Errorf(CodeInvalidRequest, "operation is required")
	}
	args, err := newArguments(op)
	if err != nil {
		return Request{}, err
	}
	if body := bytes.TrimSpace(env.Arguments); len(body) > 0 && !bytes.Equal(body, []byte("null")) {
		if err := strictUnmarshal(body, args); err != nil {
			return Request{}, err
		}
	}
	return NewRequest(args, full)
}

// strictUnmarshal decodes exactly one JSON value into target, rejecting
// unknown fields and any trailing value.
func strictUnmarshal(raw []byte, target any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return decodeError(err)
	}
	if dec.More() {
		return Errorf(CodeInvalidRequest, "the payload carries more than one JSON value")
	}
	return nil
}

// decodeError converts an encoding/json failure into a model-safe *Error,
// preserving an *Error a custom UnmarshalJSON already produced.
func decodeError(err error) error {
	var typed *Error
	if errors.As(err, &typed) {
		return typed
	}
	var unmarshalType *json.UnmarshalTypeError
	if errors.As(err, &unmarshalType) {
		if unmarshalType.Field != "" {
			return Errorf(CodeInvalidRequest, "field %q has the wrong type; expected %s", clip(unmarshalType.Field, 64), unmarshalType.Type)
		}
		return Errorf(CodeInvalidRequest, "a field has the wrong type; expected %s", unmarshalType.Type)
	}
	if msg := err.Error(); strings.HasPrefix(msg, "json: unknown field ") {
		return Errorf(CodeInvalidRequest, "unknown field %s is not part of this operation", clip(strings.TrimPrefix(msg, "json: unknown field "), 64))
	}
	return Errorf(CodeInvalidRequest, "the payload is not valid JSON for this operation")
}

// scanStrict walks the raw payload once to enforce the structural rules
// encoding/json does not: no duplicate object keys, no nesting past the
// limit, and exactly one top-level value.
//
// Duplicate keys matter because different JSON parsers disagree on which
// wins. A payload where a human reviews one destination and the executor
// reads another must never be accepted.
func scanStrict(raw []byte, maxDepth int) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := scanValue(dec, 1, maxDepth); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return Errorf(CodeInvalidRequest, "the payload carries trailing content after the request")
	}
	return nil
}

func scanValue(dec *json.Decoder, depth, maxDepth int) error {
	if depth > maxDepth {
		return Errorf(CodeInvalidRequest, "the payload nests deeper than the maximum of %d levels", maxDepth)
	}
	tok, err := dec.Token()
	if err != nil {
		return jsonSyntaxError(err)
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]bool)
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return jsonSyntaxError(err)
			}
			key, ok := keyTok.(string)
			if !ok {
				return Errorf(CodeInvalidRequest, "the payload has a non-string object key")
			}
			if seen[key] {
				return Errorf(CodeInvalidRequest, "the payload repeats the key %q", clip(key, 64))
			}
			seen[key] = true
			if err := scanValue(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanValue(dec, depth+1, maxDepth); err != nil {
				return err
			}
		}
	}
	// Consume the closing delimiter.
	if _, err := dec.Token(); err != nil {
		return jsonSyntaxError(err)
	}
	return nil
}

func jsonSyntaxError(err error) error {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return Errorf(CodeInvalidRequest, "the payload ends before the JSON value is complete")
	}
	return Errorf(CodeInvalidRequest, "the payload is not valid JSON")
}

// jsonString decodes a JSON string literal, used by the strict scalar types.
func jsonString(b []byte) (string, error) {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return "", fmt.Errorf("decode json string: %w", err)
	}
	return s, nil
}

// PeekOperation extracts the operation field from a raw payload for display
// purposes only — an approval prompt or a one-line call summary that needs
// to know roughly what a call does before it can run ParseRequest's full
// validation. It never rejects malformed input; a payload that fails to
// parse or names an unknown operation returns "". Nothing here may be used
// to decide whether a call executes or what it is allowed to do — that
// remains ParseRequest and Service.Execute's job alone.
func PeekOperation(raw []byte) Operation {
	var env envelope
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	op := Operation(strings.TrimSpace(env.Operation))
	if !op.Valid() {
		return ""
	}
	return op
}

// PeekPlanID extracts the plan_id argument from a raw change_apply payload
// for display purposes only, with the same non-authoritative caveat as
// PeekOperation. It returns "" for anything else, including a well-formed
// payload whose operation is not change_apply.
func PeekPlanID(raw []byte) string {
	if PeekOperation(raw) != OpChangeApply {
		return ""
	}
	var env envelope
	if json.Unmarshal(raw, &env) != nil {
		return ""
	}
	var args ChangeApplyArgs
	if json.Unmarshal(env.Arguments, &args) != nil {
		return ""
	}
	return strings.TrimSpace(args.PlanID)
}
