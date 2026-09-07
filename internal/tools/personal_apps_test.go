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

// TestCallsFromNativePersonalAppsSynthesizesEnvelope covers the actual bug
// report this schema shape was built to fix: a native call for one
// personal_apps operation (e.g. "mail_read", called with a flat
// {"message_ids":[...]} argument object, exactly as every other native tool
// in the catalog is called) must produce a Call whose Tool is still the
// single ToolPersonalApps identity and whose Body is the combined
// {"operation","arguments"} envelope internal/personalapps.ParseRequest
// requires — reassembled from the operation name, never from guessing at
// the shape of what the model sent.
func TestCallsFromNativePersonalAppsSynthesizesEnvelope(t *testing.T) {
	calls := CallsFromNative([]provider.ToolCall{
		{ID: "c1", Name: "mail_read", Arguments: `{"message_ids":["msg_1"]}`},
		{ID: "c2", Name: "status", Arguments: ""},
	})
	if len(calls) != 2 {
		t.Fatalf("CallsFromNative() returned %d calls, want 2", len(calls))
	}
	if calls[0].Tool != ToolPersonalApps {
		t.Fatalf("calls[0].Tool = %q, want %q", calls[0].Tool, ToolPersonalApps)
	}
	wantBody := `{"operation":"mail_read","arguments":{"message_ids":["msg_1"]}}`
	if calls[0].Body != wantBody {
		t.Fatalf("Body = %q, want %q", calls[0].Body, wantBody)
	}
	if calls[0].InputErr != "" {
		t.Fatalf("unexpected InputErr: %s", calls[0].InputErr)
	}
	// Empty native arguments (no fields needed, e.g. "status") must still
	// produce a well-formed envelope with an empty arguments object, not an
	// empty/missing "arguments" that ParseRequest would choke on.
	if calls[1].Tool != ToolPersonalApps {
		t.Fatalf("calls[1].Tool = %q, want %q", calls[1].Tool, ToolPersonalApps)
	}
	wantEmpty := `{"operation":"status","arguments":{}}`
	if calls[1].Body != wantEmpty {
		t.Fatalf("Body = %q, want %q", calls[1].Body, wantEmpty)
	}
}

func TestCallsFromNativePersonalAppsRejectsOversizedArguments(t *testing.T) {
	huge := strings.Repeat("a", MaxPersonalAppsPayloadBytes+1)
	raw := `{"subject":"` + huge + `"}`
	calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: "mail_search", Arguments: raw}})
	if calls[0].InputErr == "" {
		t.Fatal("CallsFromNative accepted arguments over the size limit")
	}
	if calls[0].Body != "" {
		t.Fatalf("expected no Body for a rejected call, got %q", calls[0].Body)
	}
}

func TestCallsFromNativePersonalAppsRejectsMalformedArguments(t *testing.T) {
	calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: "mail_search", Arguments: `not json`}})
	if calls[0].InputErr == "" {
		t.Fatal("CallsFromNative accepted non-JSON arguments")
	}
}

func TestCallsFromNativeUnknownNameIsNotTreatedAsPersonalApps(t *testing.T) {
	// A name that merely resembles an operation string but is not one of
	// the twelve must fall through to ordinary (non-personal_apps) native
	// handling, not be swallowed here.
	calls := CallsFromNative([]provider.ToolCall{{ID: "c1", Name: "mail_delete", Arguments: `{}`}})
	if calls[0].Tool == ToolPersonalApps {
		t.Fatalf("unknown name %q was misrouted to personal_apps", "mail_delete")
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

// TestPersonalAppsSpecsOneToolPerOperation guards the shape this whole
// schema redesign depends on: one native tool per operation, named exactly
// the operation string (so CallsFromNative's dispatch — checking
// personalapps.Operation(tc.Name).Valid() — always finds them), matching
// personalapps.Operations() exactly so the advertised set can never drift
// from the package's actual closed vocabulary.
func TestPersonalAppsSpecsOneToolPerOperation(t *testing.T) {
	specs := PersonalAppsSpecs()
	want := personalapps.Operations()
	if len(specs) != len(want) {
		t.Fatalf("PersonalAppsSpecs() returned %d specs, want %d", len(specs), len(want))
	}
	for i, op := range want {
		if specs[i].Name != string(op) {
			t.Errorf("specs[%d].Name = %q, want %q", i, specs[i].Name, op)
		}
		if specs[i].Description == "" {
			t.Errorf("specs[%d] (%s) has an empty description", i, op)
		}
	}
}

// personalAppsSchemaOf finds one operation's schema among PersonalAppsSpecs
// and decodes it, failing the test if the operation is missing.
func personalAppsSchemaOf(t *testing.T, op string) struct {
	Type                 string         `json:"type"`
	Properties           map[string]any `json:"properties"`
	Required             []string       `json:"required"`
	AdditionalProperties bool           `json:"additionalProperties"`
} {
	t.Helper()
	for _, s := range PersonalAppsSpecs() {
		if s.Name != op {
			continue
		}
		var schema struct {
			Type                 string         `json:"type"`
			Properties           map[string]any `json:"properties"`
			Required             []string       `json:"required"`
			AdditionalProperties bool           `json:"additionalProperties"`
		}
		if err := json.Unmarshal(s.Parameters, &schema); err != nil {
			t.Fatalf("%s Parameters is not valid JSON: %v", op, err)
		}
		return schema
	}
	t.Fatalf("PersonalAppsSpecs() has no entry named %q", op)
	panic("unreachable")
}

// TestPersonalAppsSchemasNameRealFieldsPerOperation guards against the
// schema silently regressing toward an opaque object with no property
// names (what let a native tool-calling model invent an "account_id" field
// for mail_read instead of the "message_ids" it actually takes), and
// specifically against the reported failure mode itself: mail_read's schema
// must not even look like it accepts account_id, and mail_mailboxes' schema
// must require it.
func TestPersonalAppsSchemasNameRealFieldsPerOperation(t *testing.T) {
	mailRead := personalAppsSchemaOf(t, "mail_read")
	if mailRead.AdditionalProperties {
		t.Error("mail_read schema allows additional properties")
	}
	if _, ok := mailRead.Properties["message_ids"]; !ok {
		t.Error("mail_read schema is missing message_ids")
	}
	if _, ok := mailRead.Properties["account_id"]; ok {
		t.Error("mail_read schema exposes account_id — that field belongs to mail_mailboxes only, and its presence here is exactly the reported bug")
	}
	if len(mailRead.Required) != 1 || mailRead.Required[0] != "message_ids" {
		t.Errorf("mail_read required = %v, want [\"message_ids\"]", mailRead.Required)
	}

	mailMailboxes := personalAppsSchemaOf(t, "mail_mailboxes")
	if _, ok := mailMailboxes.Properties["account_id"]; !ok {
		t.Error("mail_mailboxes schema is missing account_id")
	}
	if len(mailMailboxes.Required) != 1 || mailMailboxes.Required[0] != "account_id" {
		t.Errorf("mail_mailboxes required = %v, want [\"account_id\"]", mailMailboxes.Required)
	}

	status := personalAppsSchemaOf(t, "status")
	if len(status.Properties) != 0 || len(status.Required) != 0 {
		t.Errorf("status schema = %+v, want no properties and nothing required", status)
	}

	changeApply := personalAppsSchemaOf(t, "change_apply")
	if _, ok := changeApply.Properties["plan_id"]; !ok {
		t.Error("change_apply schema is missing plan_id")
	}
	if _, ok := changeApply.Properties["changes"]; ok {
		t.Error("change_apply schema exposes changes — that field belongs to change_prepare only")
	}
}

// TestPersonalAppsInstructionsDocumentReadFlowAndChangeShapes guards the
// system-prompt text a fenced-protocol model (and every native model, as
// supplementary guidance) actually reads: the operation sequencing and the
// six change_prepare variant shapes that the flat arguments schema cannot
// express on its own.
func TestPersonalAppsInstructionsDocumentReadFlowAndChangeShapes(t *testing.T) {
	for _, want := range []string{
		"mail_accounts", "mail_mailboxes", "mail_search", "mail_read",
		"calendar_list", "calendar_events", "calendar_free_slots", "calendar_event",
		"mail_move", "mail_set_read", "mail_set_flag", "mail_save_draft",
		"calendar_create_event", "calendar_update_event",
		"never invent",
	} {
		if !strings.Contains(PersonalAppsInstructions, want) {
			t.Errorf("PersonalAppsInstructions does not mention %q", want)
		}
	}
}
