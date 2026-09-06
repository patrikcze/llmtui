package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/personalapps"
	"github.com/patrikcze/llmtui/internal/provider"
)

// stubPersonalApps is a fake Service: it records the raw bytes it received
// and returns a caller-supplied Result, so tests never need a real adapter.
type stubPersonalApps struct {
	got []byte
	res personalapps.Result
}

func (s *stubPersonalApps) ExecuteRaw(_ context.Context, raw []byte) personalapps.Result {
	s.got = append([]byte(nil), raw...)
	return s.res
}

func personalAppsRunner(t *testing.T, svc PersonalAppsService) *Runner {
	t.Helper()
	r := NewRunner(t.TempDir(), 64)
	r.PersonalApps = svc
	return r
}

func TestPersonalAppsDisabledByDefault(t *testing.T) {
	r := NewRunner(t.TempDir(), 64)
	res := r.Execute(Call{Tool: ToolPersonalApps, Body: `{"operation":"status"}`})
	if res.Err == nil {
		t.Fatal("personal_apps ran with no Service wired")
	}
	if !errors.Is(res.Err, errPersonalAppsDisabled) {
		t.Fatalf("err = %v, want errPersonalAppsDisabled", res.Err)
	}
}

func TestPersonalAppsExecutesRawBodyUnmodified(t *testing.T) {
	stub := &stubPersonalApps{res: personalapps.Result{
		Version: personalapps.Version, Operation: personalapps.OpStatus, Status: personalapps.StatusOK,
	}}
	r := personalAppsRunner(t, stub)
	body := `{"operation":"status"}`
	res := r.Execute(Call{Tool: ToolPersonalApps, Body: body})
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if string(stub.got) != body {
		t.Fatalf("Service received %q, want the untouched call body %q", stub.got, body)
	}
}

func TestPersonalAppsResultIsFramedAndSanitized(t *testing.T) {
	stub := &stubPersonalApps{res: personalapps.Result{
		Version:   personalapps.Version,
		Operation: personalapps.OpMailSearch,
		Status:    personalapps.StatusOK,
		Data: map[string]any{"messages": []personalapps.MessageSummaryView{
			{Subject: "hi\x1b[31m there"}, // an embedded escape sequence
		}},
	}}
	r := personalAppsRunner(t, stub)
	res := r.Execute(Call{Tool: ToolPersonalApps, Body: `{"operation":"mail_search","arguments":{}}`})
	if res.Err != nil {
		t.Fatalf("Execute: %v", res.Err)
	}
	if !strings.Contains(res.Output, "LLMTUI_UNTRUSTED_BEGIN") || !strings.Contains(res.Output, "LLMTUI_UNTRUSTED_END") {
		t.Fatalf("output is not framed as untrusted: %s", res.Output)
	}
	if strings.Contains(res.Output, "\x1b[31m") {
		t.Fatalf("output still carries a raw ANSI escape: %q", res.Output)
	}
	// The JSON payload itself (subject text included) must still round-trip:
	// sanitizing the marshaled text must not corrupt the JSON syntax.
	start := strings.Index(res.Output, "\n") + 1
	end := strings.LastIndex(res.Output, "\n<<<LLMTUI_UNTRUSTED_END")
	var decoded map[string]any
	if err := json.Unmarshal([]byte(res.Output[start:end]), &decoded); err != nil {
		t.Fatalf("framed output is not valid JSON: %v\n%s", err, res.Output)
	}
}

func TestPersonalAppsResultNeverReturnsABareGoErrorForAServedRequest(t *testing.T) {
	// A domain-level failure (bad scope, disconnected adapter, malformed
	// request) is reported inside the Result, not as res.Err — the whole
	// point of the envelope is that the model reads Status/Error itself.
	stub := &stubPersonalApps{res: personalapps.Result{
		Version: personalapps.Version, Status: personalapps.StatusDenied,
		Error: &personalapps.ResultError{Code: personalapps.CodeScopeDenied, Message: "out of scope"},
	}}
	r := personalAppsRunner(t, stub)
	res := r.Execute(Call{Tool: ToolPersonalApps, Body: `{"operation":"mail_search","arguments":{}}`})
	if res.Err != nil {
		t.Fatalf("Execute returned a Go error for a served-but-denied request: %v", res.Err)
	}
	if !strings.Contains(res.Output, "scope_denied") {
		t.Fatalf("output does not carry the denial: %s", res.Output)
	}
}

func TestParsePersonalAppsCallPreservesRawBytes(t *testing.T) {
	reply := "```tool personal_apps\n" + `{"operation":"mail_search","arguments":{"a":1,"a":2}}` + "\n```"
	calls := Parse(reply)
	if len(calls) != 1 {
		t.Fatalf("Parse() returned %d calls, want 1", len(calls))
	}
	// Duplicate keys must survive Parse untouched — the tools package must
	// not pre-decode this JSON, or personalapps.ParseRequest's own
	// duplicate-key detection would never see the duplicate.
	if !strings.Contains(calls[0].Body, `"a":1,"a":2`) {
		t.Fatalf("Parse rewrote the JSON body: %q", calls[0].Body)
	}
	if calls[0].InputErr != "" {
		t.Fatalf("Parse rejected a well-formed (if duplicate-keyed) body: %s", calls[0].InputErr)
	}
}

func TestParsePersonalAppsCallRejectsOversizedBody(t *testing.T) {
	huge := strings.Repeat("a", MaxPersonalAppsPayloadBytes+1)
	reply := "```tool personal_apps\n" + `{"operation":"mail_search","arguments":{"subject":"` + huge + `"}}` + "\n```"
	calls := Parse(reply)
	if len(calls) != 1 {
		t.Fatalf("Parse() returned %d calls, want 1", len(calls))
	}
	if calls[0].InputErr == "" {
		t.Fatal("Parse accepted a body over the size limit")
	}
}

func TestCallsFromNativePersonalAppsPassesArgumentsThrough(t *testing.T) {
	raw := `{"operation":"change_apply","arguments":{"plan_id":"plan_1"}}`
	calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: ToolPersonalApps, Arguments: raw}})
	if len(calls) != 1 {
		t.Fatalf("CallsFromNative() returned %d calls, want 1", len(calls))
	}
	if calls[0].Body != raw {
		t.Fatalf("Body = %q, want the native arguments passed through verbatim: %q", calls[0].Body, raw)
	}
	if calls[0].InputErr != "" {
		t.Fatalf("unexpected InputErr: %s", calls[0].InputErr)
	}
}

func TestCallsFromNativePersonalAppsRejectsOversizedArguments(t *testing.T) {
	huge := strings.Repeat("a", MaxPersonalAppsPayloadBytes+1)
	raw := `{"operation":"mail_search","arguments":{"subject":"` + huge + `"}}`
	calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: ToolPersonalApps, Arguments: raw}})
	if calls[0].InputErr == "" {
		t.Fatal("CallsFromNative accepted arguments over the size limit")
	}
}

func TestDescribePersonalAppsCall(t *testing.T) {
	tests := map[string]string{
		`{"operation":"status"}`: "personal_apps: status",
		`{"operation":"change_apply","arguments":{"plan_id":"plan_1"}}`: "personal_apps: apply plan_1",
		`not json`: "personal_apps: malformed request",
	}
	for body, want := range tests {
		c := Call{Tool: ToolPersonalApps, Body: body}
		if got := c.Describe(); got != want {
			t.Errorf("Describe(%s) = %q, want %q", body, got, want)
		}
	}
}

func TestPersonalAppsSpecsEnumMatchesOperationVocabulary(t *testing.T) {
	specs := PersonalAppsSpecs()
	if len(specs) != 1 {
		t.Fatalf("PersonalAppsSpecs() returned %d specs, want 1", len(specs))
	}
	var schema struct {
		Properties struct {
			Operation struct {
				Enum []string `json:"enum"`
			} `json:"operation"`
		} `json:"properties"`
		Required             []string `json:"required"`
		AdditionalProperties bool     `json:"additionalProperties"`
	}
	if err := json.Unmarshal(specs[0].Parameters, &schema); err != nil {
		t.Fatalf("Parameters is not valid JSON: %v", err)
	}
	if schema.AdditionalProperties {
		t.Fatal("personal_apps schema allows additional top-level properties")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "operation" {
		t.Fatalf("required = %v, want just [\"operation\"]", schema.Required)
	}
	want := personalapps.Operations()
	if len(schema.Properties.Operation.Enum) != len(want) {
		t.Fatalf("schema enum has %d operations, personalapps.Operations() has %d", len(schema.Properties.Operation.Enum), len(want))
	}
	for i, op := range want {
		if schema.Properties.Operation.Enum[i] != string(op) {
			t.Errorf("schema enum[%d] = %q, want %q", i, schema.Properties.Operation.Enum[i], op)
		}
	}
}
