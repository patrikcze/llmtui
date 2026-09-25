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
	if trial.LayaGuardedAssistEligible || trial.LayaGuardedAssistProfile != "" || trial.LayaGuardedAssistProbability != 0 ||
		trial.LayaGuardedAssistThreshold != 0 || trial.LayaGuardedAssistEscalated || trial.LayaGuardedAssistReason != "" {
		t.Fatalf("AgentTrial zero value = %+v, want empty Phase 1 fields", trial)
	}
	if trial.LayaCriterionAssessmentMode != "" || trial.LayaCriterionAssessmentTotal != 0 ||
		trial.LayaCriterionAssessmentAvailability != "" || trial.LayaCriterionAssessmentSpecFingerprint != "" {
		t.Fatalf("AgentTrial zero value = %+v, want empty Phase 3 fields", trial)
	}
	trialJSON, err := json.Marshal(trial)
	if err != nil {
		t.Fatalf("marshal AgentTrial: %v", err)
	}
	for _, key := range []string{
		"\"status\"", "network_calls",
		"laya_pre_verifier_availability", "laya_independent_need", "laya_label_source", "laya_censor_reason",
		"laya_guarded_assist_eligible", "laya_guarded_assist_profile", "laya_guarded_assist_probability",
		"laya_guarded_assist_threshold", "laya_guarded_assist_escalated", "laya_guarded_assist_reason",
		"laya_criterion_assessment_mode", "laya_criterion_assessment_total", "laya_criterion_assessment_availability",
		"laya_criterion_assessment_spec_fingerprint",
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
			{Scenario: "s1", Trial: 4, ObservedAction: "read", FinalResult: "ok",
				LayaGuardedAssistEligible: true, LayaGuardedAssistProfile: "english-mlx",
				LayaGuardedAssistProbability: 0.82, LayaGuardedAssistThreshold: 0.5,
				LayaGuardedAssistEscalated: true, LayaGuardedAssistReason: "escalated"},
			{Scenario: "s1", Trial: 5, ObservedAction: "read", FinalResult: "ok",
				LayaCriterionAssessmentMode: "criterion_shadow", LayaCriterionAssessmentModel: "english-mlx",
				LayaCriterionAssessmentTotal: 2, LayaCriterionAssessmentAvailable: 1,
				LayaCriterionAssessmentAbstained: 1, LayaCriterionAssessmentAvailability: "available",
				LayaCriterionAssessmentSignal: "support", LayaCriterionAssessmentSpecFingerprint: "spec-hash",
				LayaCriterionAssessmentEvidenceFingerprint: "evidence-hash", LayaCriterionAssessmentModelRevision: "rev-1",
				LayaCriterionAssessmentSupportProbability: 0.82, LayaCriterionAssessmentContradictionProbability: 0.04},
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
		`"laya_guarded_assist_eligible":true`, `"laya_guarded_assist_profile":"english-mlx"`,
		`"laya_guarded_assist_probability":0.82`, `"laya_guarded_assist_threshold":0.5`,
		`"laya_guarded_assist_escalated":true`, `"laya_guarded_assist_reason":"escalated"`,
		`"laya_criterion_assessment_mode":"criterion_shadow"`, `"laya_criterion_assessment_total":2`,
		`"laya_criterion_assessment_available":1`, `"laya_criterion_assessment_abstained":1`,
		`"laya_criterion_assessment_signal":"support"`, `"laya_criterion_assessment_spec_fingerprint":"spec-hash"`,
		`"laya_criterion_assessment_evidence_fingerprint":"evidence-hash"`, `"laya_criterion_assessment_model_revision":"rev-1"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("WriteJSONL output missing %q:\n%s", want, out)
		}
	}
}
