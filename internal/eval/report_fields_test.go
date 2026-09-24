package eval

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestReportFieldsAreAdditiveAndZeroValueSafe guards the Phase 0 additive
// extension (plan §28 Phase 8 report template): the new Metadata and
// AgentTrial fields must default to their zero value and stay absent from
// JSON output (omitempty) when unset, so every existing caller of
// WriteJSONL is unaffected until something actually populates them.
func TestReportFieldsAreAdditiveAndZeroValueSafe(t *testing.T) {
	var meta Metadata
	if meta.BaselineSHA != "" || meta.CandidateSHA != "" || meta.FixtureHash != "" {
		t.Fatalf("Metadata zero value = %+v, want empty new fields", meta)
	}
	metaJSON, err := json.Marshal(meta)
	if err != nil {
		t.Fatalf("marshal Metadata: %v", err)
	}
	for _, key := range []string{"baseline_sha", "candidate_sha", "fixture_hash"} {
		if strings.Contains(string(metaJSON), key) {
			t.Errorf("zero-value Metadata JSON unexpectedly carries %q: %s", key, metaJSON)
		}
	}

	var trial AgentTrial
	if trial.Status != "" || trial.NetworkCalls != 0 {
		t.Fatalf("AgentTrial zero value = %+v, want empty new fields", trial)
	}
	if trial.LayaPreVerifierAvailability != "" || trial.LayaIndependentNeed != nil || trial.LayaLabelSource != "" || trial.LayaCensorReason != "" {
		t.Fatalf("AgentTrial zero value = %+v, want empty Phase 0a fields", trial)
	}
	trialJSON, err := json.Marshal(trial)
	if err != nil {
		t.Fatalf("marshal AgentTrial: %v", err)
	}
	for _, key := range []string{
		"\"status\"", "network_calls",
		"laya_pre_verifier_availability", "laya_independent_need", "laya_label_source", "laya_censor_reason",
	} {
		if strings.Contains(string(trialJSON), key) {
			t.Errorf("zero-value AgentTrial JSON unexpectedly carries %q: %s", key, trialJSON)
		}
	}
}

func boolPtr(b bool) *bool { return &b }

// TestReportFieldsRoundTripWhenPopulated guards the actual wiring contract:
// once a caller sets the new fields, WriteJSONL must carry them through
// unmodified — this phase reserves the fields but does not build the harness
// that populates them.
func TestReportFieldsRoundTripWhenPopulated(t *testing.T) {
	report := Report{
		Metadata: Metadata{
			Provider: "mock", Model: "fixture",
			BaselineSHA: "abc1234", CandidateSHA: "def5678", FixtureHash: "sha256:deadbeef",
		},
		Agent: []AgentTrial{
			{Scenario: "s1", Trial: 1, ObservedAction: "read", FinalResult: "ok",
				Status: AgentTrialStatusCompleted, NetworkCalls: 2},
			{Scenario: "s1", Trial: 2, ObservedAction: "read", FinalResult: "error",
				Status: AgentTrialStatusProviderFailed},
			{Scenario: "s1", Trial: 3, ObservedAction: "read", FinalResult: "ok",
				LayaPreVerifierAvailability: "late", LayaIndependentNeed: boolPtr(true),
				LayaLabelSource: "fixture", LayaCensorReason: "config_reload"},
		},
	}
	var buf strings.Builder
	if err := WriteJSONL(&buf, report); err != nil {
		t.Fatalf("WriteJSONL: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`"baseline_sha":"abc1234"`, `"candidate_sha":"def5678"`, `"fixture_hash":"sha256:deadbeef"`,
		`"status":"completed"`, `"network_calls":2`, `"status":"provider_failed"`,
		`"laya_pre_verifier_availability":"late"`, `"laya_independent_need":true`,
		`"laya_label_source":"fixture"`, `"laya_censor_reason":"config_reload"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteJSONL output missing %q:\n%s", want, out)
		}
	}
}
