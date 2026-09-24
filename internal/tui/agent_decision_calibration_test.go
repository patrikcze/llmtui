package tui

import (
	"os"
	"runtime"
	"testing"

	"github.com/patrikcze/llmtui/internal/eval"
)

// TestLayaCalibrationHarness is the opt-in, reproducible successor to manual
// calibration testing (see the memory notes referenced in
// docs/decision-engine.md's "Shadow advisor" section). It drives a handful
// of scripted agent scenarios — the same shape already used throughout
// agent_loop_test.go and agent_decision_shadow_test.go — through the real
// agent loop, collects one eval.AgentTrial per scenario with the Laya*
// fields populated, and logs a threshold-sweep confusion-matrix report via
// computeThresholdSweep. Normal CI runs this against a fake decision.Engine
// with fixed, known probabilities, which proves the report's arithmetic is
// correct against known inputs — not real calibration data. Set
// LLMTUI_TEST_LAYA_MLX=1 (plus LLMTUI_TEST_LAYA_MODEL_ROOT and
// LLMTUI_TEST_LAYA_PYTHON, matching internal/decision/mlx_integration_test.go's
// existing convention) to run it against a real, installed Laya checkpoint
// instead — still with a scripted, deterministic executor, so the real
// checkpoint's judgment can be isolated from executor variability.
func TestLayaCalibrationHarness(t *testing.T) {
	type scenario struct {
		name  string
		task  string
		steps []agentScriptStep
		// fakeVerifierNeeded seeds the fake engine's pre-verifier answer in
		// the default (non-real) path only, so the threshold-sweep report
		// has a known, varied distribution to compute over — it never
		// influences agent behavior, only what the fake predicts.
		fakeVerifierNeeded float64
	}
	scenarios := []scenario{
		{
			name: "trivial_single_cycle",
			task: "create a note",
			steps: []agentScriptStep{
				{text: "Implemented the bounded change and observed success."},
				{text: verifierJSON("passed", "observable criteria passed", "", false, false)},
			},
			fakeVerifierNeeded: 0.1,
		},
		{
			name: "coverage_gap_multi_tool",
			task: "create two files",
			steps: []agentScriptStep{
				{text: "First attempt completed."},
				{text: verifierJSON("failed", "still incomplete", "finish the remaining part", true, true)},
				{text: "Second attempt completed."},
				{text: verifierJSON("passed", "now complete", "", false, false)},
			},
			fakeVerifierNeeded: 0.6,
		},
		{
			name: "ambiguous_needs_semantic_review",
			task: "improve the wording",
			steps: []agentScriptStep{
				{text: "Reworded the paragraph."},
				{text: verifierJSON("passed", "reads more professionally", "", false, false)},
			},
			fakeVerifierNeeded: 0.8,
		},
	}

	real := os.Getenv("LLMTUI_TEST_LAYA_MLX") == "1"
	if real && (runtime.GOOS != "darwin" || runtime.GOARCH != "arm64") {
		t.Fatal("real Laya calibration requires macOS arm64")
	}

	var trials []eval.AgentTrial
	var samples []preVerifierSample

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			m, _ := configureAgentTestModel(t, sc.steps...)
			var layaModel string
			if real {
				m.cfg.DecisionEngine.Enabled = true
				m.cfg.DecisionEngine.Laya.ModelDir = os.Getenv("LLMTUI_TEST_LAYA_MODEL_ROOT")
				m.cfg.DecisionEngine.Laya.MLXPython = os.Getenv("LLMTUI_TEST_LAYA_PYTHON")
				m.cfg.DecisionEngine.Laya.DefaultModel = "typed-decisions-mlx"
				svc, err := newDecisionShadowService(m.cfg)
				if err != nil {
					t.Fatalf("real decision service: %v", err)
				}
				t.Cleanup(func() { _ = svc.Close() })
				m.decisionShadow = svc
				layaModel = svc.model
			} else {
				preVerifier := fakePreVerifierResult(sc.fakeVerifierNeeded, 1-sc.fakeVerifierNeeded)
				fake := &fakeDecisionEngine{
					result:            fakeCycleActionResult("finish"),
					preVerifierResult: &preVerifier,
				}
				wireFakeDecisionShadow(m, fake)
				layaModel = "fake-calibration-engine"
			}

			driveAgentCommands(t, m, m.startVerifiedRun(sc.task, nil))

			trial := eval.AgentTrial{
				Scenario:                         sc.name,
				Trial:                            1,
				ObservedAction:                   string(m.agentLoop.run.Status),
				FinalResult:                      string(m.agentLoop.run.Status),
				Cycles:                           m.agentLoop.run.Cycle,
				LayaModel:                        layaModel,
				LayaPostCycleAction:              m.lastDebug.DecisionShadowCycleAction,
				LayaPostCycleActionProbability:   m.lastDebug.DecisionShadowCycleActionProbability,
				LayaPreVerifierAvailable:         m.lastDebug.DecisionShadowPreVerifierUnavailableReason == "",
				LayaPreVerifierNeededProbability: m.lastDebug.DecisionShadowPreVerifierNeededProbability,
			}
			trials = append(trials, trial)
			samples = append(samples, m.preVerifierShadowSamples...)
			t.Logf("trial: %+v", trial)
		})
	}

	if len(trials) != len(scenarios) {
		t.Fatalf("collected %d trials, want %d", len(trials), len(scenarios))
	}

	rows := computeThresholdSweep(samples, []float64{0.50, 0.60, 0.70, 0.80, 0.90})
	t.Log("threshold  TP  FP  TN  FN")
	for _, row := range rows {
		t.Logf("%.2f       %d   %d   %d   %d", row.Threshold, row.TruePositive, row.FalsePositive, row.TrueNegative, row.FalseNegative)
	}
}

// TestComputeNeedThresholdSweepExcludesUnknownAndUnavailableLabels covers
// Phase 0a's ground-truth confusion matrix (computeNeedThresholdSweep),
// distinct from computeThresholdSweep's policy-agreement one: it must
// compute against IndependentNeed (an externally supplied label), and must
// exclude any sample without a legitimate, labeled probability rather than
// silently treating it as a known negative. Each case below names the
// measurement-integrity scenario it stands in for.
func TestComputeNeedThresholdSweepExcludesUnknownAndUnavailableLabels(t *testing.T) {
	need := func(b bool) *bool { return &b }
	samples := []preVerifierSample{
		// Independently labeled would-skip false completion: the deployed
		// policy did not run the verifier and Laya's own probability was
		// also low, but ground truth says it actually was needed — a real
		// miss against ground truth regardless of what the policy did.
		{Probability: 0.2, ActualRan: false, Availability: "available", IndependentNeed: need(true), LabelSource: "fixture", Cycle: 1},
		// Would-run unnecessary call: the policy ran the verifier and Laya
		// predicted high, but ground truth says it was not necessary.
		{Probability: 0.9, ActualRan: true, Availability: "available", IndependentNeed: need(false), LabelSource: "fixture", Cycle: 1},
		// Unknown truth: no ground-truth label at all — must be excluded,
		// never silently counted as a known negative.
		{Probability: 0.95, ActualRan: true, Availability: "available", IndependentNeed: nil, Cycle: 2},
		// Missing model / unavailable prediction: no legitimate probability
		// was ever obtained, even though a label happens to be attached —
		// Availability, not the label, gates inclusion.
		{Probability: 0, ActualRan: false, Availability: "unavailable", IndependentNeed: need(true), LabelSource: "fixture", Cycle: 2},
		// Verifier failure: the semantic verifier itself errored this
		// cycle, leaving ground truth unknown — excluded like any other
		// unlabeled sample.
		{Probability: 0.5, ActualRan: true, Availability: "available", IndependentNeed: nil, Cycle: 3},
		// Mixed-cycle outcome: labeled, available samples from different
		// cycles must both still count toward the same sweep.
		{Probability: 0.85, ActualRan: true, Availability: "available", IndependentNeed: need(true), LabelSource: "fixture", Cycle: 4},
		{Probability: 0.15, ActualRan: false, Availability: "late", IndependentNeed: need(false), LabelSource: "fixture", Cycle: 5},
		// Zero legitimate probability: a real, available, confidently-zero
		// probability must be included as a legitimate negative, never
		// treated as if it were an unavailable/missing prediction.
		{Probability: 0, ActualRan: false, Availability: "available", IndependentNeed: need(false), LabelSource: "fixture", Cycle: 6},
	}

	rows := computeNeedThresholdSweep(samples, []float64{0.5})
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.TruePositive != 1 || row.FalsePositive != 1 || row.TrueNegative != 2 || row.FalseNegative != 1 {
		t.Fatalf("row = %+v, want TP=1 FP=1 TN=2 FN=1", row)
	}
	total := row.TruePositive + row.FalsePositive + row.TrueNegative + row.FalseNegative
	if total != 5 {
		t.Fatalf("labeled+available samples counted = %d, want 5 (3 of 8 excluded: 2 unknown-truth, 1 unavailable)", total)
	}
}
