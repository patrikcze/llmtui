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
