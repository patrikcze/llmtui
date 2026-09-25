package eval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/provider"
)

// Metadata identifies one explicitly configured evaluation run. Callers
// should populate only synthetic, non-secret values.
type Metadata struct {
	Commit         string  `json:"commit,omitempty"`
	Provider       string  `json:"provider"`
	EndpointType   string  `json:"endpoint_type,omitempty"`
	Model          string  `json:"model"`
	BackendVersion string  `json:"backend_version,omitempty"`
	Quantization   string  `json:"quantization,omitempty"`
	ContextTokens  int     `json:"context_tokens,omitempty"`
	ToolSchema     string  `json:"tool_schema_fingerprint,omitempty"`
	ChatTemplate   string  `json:"chat_template,omitempty"`
	Temperature    float64 `json:"temperature,omitempty"`
	MaxTokens      int     `json:"max_tokens,omitempty"`
	Seed           *int64  `json:"seed,omitempty"`
	VerifierMode   string  `json:"verifier_mode,omitempty"`
	AssistanceMode string  `json:"assistance_mode,omitempty"`
	WarmModel      bool    `json:"warm_model"`

	// BaselineSHA and CandidateSHA identify the two git commits a Phase 8
	// A/B comparison run measures; FixtureHash identifies the exact fixture
	// set used. All three are additive and zero-value safe so synthetic
	// callers can continue to omit them.
	BaselineSHA  string `json:"baseline_sha,omitempty"`
	CandidateSHA string `json:"candidate_sha,omitempty"`
	FixtureHash  string `json:"fixture_hash,omitempty"`
}

// RunConfig controls repetition and the bounded provider request settings.
// Trials defaults to five. Live callers should supply a context deadline.
type RunConfig struct {
	Trials    int
	MaxTokens int
	Timeout   time.Duration
}

func (c RunConfig) normalized() RunConfig {
	if c.Trials <= 0 {
		c.Trials = 5
	}
	if c.MaxTokens <= 0 {
		c.MaxTokens = 4096
	}
	if c.Timeout <= 0 {
		c.Timeout = 2 * time.Minute
	}
	return c
}

// ContractCase is a synthetic task-contract fixture. WantAsk and WantCriteria
// are optional gold labels; an empty value records the observed outcome but
// does not force a correctness claim.
type ContractCase struct {
	ID           string
	Task         string
	WantAsk      *bool
	WantCriteria []string
	Capabilities agentverify.CapabilityCapsule
}

// ContractTrial contains content-safe contract-stage measurements.
type ContractTrial struct {
	Scenario         string        `json:"scenario"`
	Trial            int           `json:"trial"`
	Outcome          string        `json:"outcome"` // ask, decompose, park, error
	Correct          *bool         `json:"correct,omitempty"`
	RepairRequired   bool          `json:"repair_required"`
	Requests         int           `json:"requests"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	Elapsed          time.Duration `json:"elapsed_ns"`
	ErrorCategory    string        `json:"error_category,omitempty"`
}

// RunContractMatrix repeats every contract case against the same provider.
// Model output is classified, not copied, into the returned report.
func RunContractMatrix(ctx context.Context, p provider.Provider, model string, cfg RunConfig, cases []ContractCase) []ContractTrial {
	cfg = cfg.normalized()
	var trials []ContractTrial
	for _, fixture := range cases {
		for trial := 1; trial <= cfg.Trials; trial++ {
			started := time.Now()
			callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
			out, err := agentverify.EstablishContract(callCtx, p, agentverify.Config{
				Model: model, MaxTokens: cfg.MaxTokens, Timeout: cfg.Timeout,
			}, agentverify.ContractInput{Task: fixture.Task, Capabilities: fixture.Capabilities})
			cancel()
			row := ContractTrial{
				Scenario: fixture.ID, Trial: trial, RepairRequired: out.RepairRequired,
				Requests: 1, Elapsed: time.Since(started),
			}
			if out.RepairRequired {
				row.Requests = 2
			}
			if out.Usage != nil {
				row.PromptTokens = out.Usage.PromptTokens
				row.CompletionTokens = out.Usage.CompletionTokens
			}
			row.Outcome = contractOutcome(out, err)
			row.Correct, row.ErrorCategory = contractCorrectness(fixture, out, err)
			trials = append(trials, row)
		}
	}
	return trials
}

func contractOutcome(out agentverify.ContractOutput, err error) string {
	if err != nil {
		if errors.Is(err, agent.ErrMalformedControl) {
			return "park"
		}
		return "error"
	}
	if out.Contract.NeedsUserInput {
		return "ask"
	}
	return "decompose"
}

func contractCorrectness(fixture ContractCase, out agentverify.ContractOutput, err error) (*bool, string) {
	if err != nil {
		if errors.Is(err, agent.ErrMalformedControl) {
			return nil, "malformed_control"
		}
		return nil, "provider_or_runtime"
	}
	if fixture.WantAsk != nil && out.Contract.NeedsUserInput != *fixture.WantAsk {
		value := false
		return &value, ""
	}
	if len(fixture.WantCriteria) > 0 && !slices.Equal(out.Contract.Criteria, fixture.WantCriteria) {
		value := false
		return &value, ""
	}
	value := true
	return &value, ""
}

// ConformanceTrial contains one harmless native-tool probe. No call is sent
// to the host executor. A failed primary probe gets one repeated recovery
// probe, making recovery-required and recovery-succeeded measurable without
// guessing how to repair model output.
type ConformanceTrial struct {
	Trial             int                        `json:"trial"`
	NativeCall        provider.ConformanceStatus `json:"native_call"`
	ToolName          provider.ConformanceStatus `json:"tool_name"`
	Arguments         provider.ConformanceStatus `json:"arguments"`
	CallID            provider.ConformanceStatus `json:"call_id"`
	Streaming         provider.ConformanceStatus `json:"streaming"`
	Correlation       provider.ConformanceStatus `json:"result_correlation"`
	RequiredSelection provider.ConformanceStatus `json:"required_selection"`
	Censored          bool                       `json:"suspected_censoring"`
	RecoveryRequired  bool                       `json:"recovery_required"`
	RecoverySucceeded bool                       `json:"recovery_succeeded"`
	Requests          int                        `json:"requests"`
	PromptTokens      int                        `json:"prompt_tokens"`
	CompletionTokens  int                        `json:"completion_tokens"`
	Elapsed           time.Duration              `json:"elapsed_ns"`
	ErrorCategory     string                     `json:"error_category,omitempty"`
}

// AgentTrialStatus is the closed-vocabulary trial outcome the plan's Phase 8
// report template records (§28, §4 validation table). It is additive:
// AgentTrial.Status is zero-value safe (empty string, omitted from JSON) and
// nothing in this package sets it yet — a future harness populates it, this
// phase only reserves the field and its vocabulary.
type AgentTrialStatus string

const (
	AgentTrialStatusCompleted      AgentTrialStatus = "completed"
	AgentTrialStatusFailed         AgentTrialStatus = "failed"
	AgentTrialStatusTimedOut       AgentTrialStatus = "timed_out"
	AgentTrialStatusProviderFailed AgentTrialStatus = "provider_failed"
	AgentTrialStatusSkipped        AgentTrialStatus = "skipped"
)

// AgentTrial is a content-safe summary emitted by a full controller harness.
// The TUI live test supplies these fields after running synthetic tasks in a
// disposable workspace; no prompt, tool argument, result, or reasoning is
// retained.
type AgentTrial struct {
	Scenario         string        `json:"scenario"`
	Trial            int           `json:"trial"`
	ExpectedAction   string        `json:"expected_action,omitempty"`
	ObservedAction   string        `json:"observed_action"`
	FinalResult      string        `json:"final_result"`
	VerifierVerdict  string        `json:"verifier_verdict,omitempty"`
	ToolCalls        int           `json:"tool_calls"`
	ToolExecuted     int           `json:"tool_executed"`
	ToolSucceeded    int           `json:"tool_succeeded"`
	ToolFailed       int           `json:"tool_failed"`
	ToolDenied       int           `json:"tool_denied"`
	ToolBlocked      int           `json:"tool_blocked"`
	ToolUnknown      int           `json:"tool_unknown"`
	RecoveryRequests int           `json:"recovery_requests"`
	ProviderRequests int           `json:"provider_requests"`
	PromptTokens     int           `json:"prompt_tokens"`
	CompletionTokens int           `json:"completion_tokens"`
	Cycles           int           `json:"cycles"`
	NeedsUserInput   bool          `json:"needs_user_input"`
	FalseSuccess     bool          `json:"false_success"`
	ErrorCategory    string        `json:"error_category,omitempty"`
	Elapsed          time.Duration `json:"elapsed_ns"`

	// Status is the Phase 8 closed-vocabulary trial outcome (see
	// AgentTrialStatus). Empty means the producing caller predates Phase 8
	// and has not been updated to set it — existing consumers see no
	// behavior change. PromptTokens/CompletionTokens above already carry the
	// per-trial token totals the Phase 8 template calls for, so this field
	// only adds what was actually missing: a closed-vocabulary status and a
	// network-call count.
	Status AgentTrialStatus `json:"status,omitempty"`
	// NetworkCalls counts outbound network calls made during the trial
	// (provider requests plus any tool-initiated network access such as
	// web_search, web_fetch, or an MCP server call). Additive and
	// zero-value safe; nothing in this package populates it yet.
	NetworkCalls int `json:"network_calls,omitempty"`

	// The fields below are independent, fixture-defined postconditions —
	// deterministic filesystem/answer checks the driver performs itself,
	// never derived from DecisionDone or the semantic verifier's verdict.
	// They stay content-free: a classification/count, never a raw path,
	// argument, or excerpt.
	PostconditionChecked bool `json:"postcondition_checked,omitempty"`
	PostconditionPassed  bool `json:"postcondition_passed,omitempty"`
	// PostconditionFailure is one of: "answer_missing_evidence",
	// "no_read_observed", "read_before_ask", "wrong_path_read",
	// "mutation_before_approval", "wrong_target_path", "content_mismatch",
	// "no_mutation", or "" when the postcondition passed.
	PostconditionFailure string `json:"postcondition_failure,omitempty"`
	UnexpectedSideEffect bool   `json:"unexpected_side_effect,omitempty"`
	DuplicateEffect      bool   `json:"duplicate_effect,omitempty"`
	MutationCount        int    `json:"mutation_count,omitempty"`

	// Laya* fields carry the optional pre-verifier/post-cycle shadow advisor
	// calibration data for one trial (internal/tui/agent_decision_shadow.go).
	// Additive and zero-value safe, matching Status/NetworkCalls above:
	// nothing in this package populates them, and no consumer here reads
	// them — a caller (the opt-in calibration harness in
	// internal/tui/agent_decision_calibration_test.go) sets them from its
	// own trial run. Never a raw prompt, tool argument, or reasoning
	// excerpt — probabilities and identifiers only.
	LayaModel                        string  `json:"laya_model,omitempty"`
	LayaPreVerifierAvailable         bool    `json:"laya_pre_verifier_available,omitempty"`
	LayaPreVerifierNeededProbability float64 `json:"laya_pre_verifier_needed_probability,omitempty"`
	LayaPostCycleAction              string  `json:"laya_post_cycle_action,omitempty"`
	LayaPostCycleActionProbability   float64 `json:"laya_post_cycle_action_probability,omitempty"`
	// LayaCriterionAssessment* are bounded, content-free Phase 3 shadow
	// diagnostics. Counts describe the batch; the last fingerprints/probability
	// fields bind the most recently recorded measurement without exporting its
	// proposition or observation text.
	LayaCriterionAssessmentMode                     string  `json:"laya_criterion_assessment_mode,omitempty"`
	LayaCriterionAssessmentModel                    string  `json:"laya_criterion_assessment_model,omitempty"`
	LayaCriterionAssessmentTotal                    int     `json:"laya_criterion_assessment_total,omitempty"`
	LayaCriterionAssessmentAvailable                int     `json:"laya_criterion_assessment_available,omitempty"`
	LayaCriterionAssessmentAbstained                int     `json:"laya_criterion_assessment_abstained,omitempty"`
	LayaCriterionAssessmentErrors                   int     `json:"laya_criterion_assessment_errors,omitempty"`
	LayaCriterionAssessmentLate                     int     `json:"laya_criterion_assessment_late,omitempty"`
	LayaCriterionAssessmentAvailability             string  `json:"laya_criterion_assessment_availability,omitempty"`
	LayaCriterionAssessmentSignal                   string  `json:"laya_criterion_assessment_signal,omitempty"`
	LayaCriterionAssessmentSpecFingerprint          string  `json:"laya_criterion_assessment_spec_fingerprint,omitempty"`
	LayaCriterionAssessmentEvidenceFingerprint      string  `json:"laya_criterion_assessment_evidence_fingerprint,omitempty"`
	LayaCriterionAssessmentModelRevision            string  `json:"laya_criterion_assessment_model_revision,omitempty"`
	LayaCriterionAssessmentSupportProbability       float64 `json:"laya_criterion_assessment_support_probability,omitempty"`
	LayaCriterionAssessmentContradictionProbability float64 `json:"laya_criterion_assessment_contradiction_probability,omitempty"`
	// LayaCriterionAssist* are bounded Phase 4b gate diagnostics. They are
	// populated only when a separately approved G2 profile actually ran; the
	// shipped profile set is empty, so zero-value trials remain unchanged.
	LayaCriterionAssistEligible  bool   `json:"laya_criterion_assist_eligible,omitempty"`
	LayaCriterionAssistProfile   string `json:"laya_criterion_assist_profile,omitempty"`
	LayaCriterionAssistEscalated bool   `json:"laya_criterion_assist_escalated,omitempty"`
	LayaCriterionAssistReason    string `json:"laya_criterion_assist_reason,omitempty"`
	// LayaToolRanking* are bounded, offline Phase 5 comparison measurements.
	// The production discovery pipeline never populates them; the opt-in test
	// harness may write them to the existing AgentTrial report.
	LayaToolRankingMode            string `json:"laya_tool_ranking_mode,omitempty"`
	LayaToolRankingCandidateCount  int    `json:"laya_tool_ranking_candidate_count,omitempty"`
	LayaToolRankingLexicalTotal    int    `json:"laya_tool_ranking_lexical_total,omitempty"`
	LayaToolRankingOptionCost      int    `json:"laya_tool_ranking_option_cost,omitempty"`
	LayaToolRankingNecessaryRecall *bool  `json:"laya_tool_ranking_necessary_recall,omitempty"`
	LayaToolRankingRerankedRecall  *bool  `json:"laya_tool_ranking_reranked_recall,omitempty"`
	LayaToolRankingLexicalRank     int    `json:"laya_tool_ranking_lexical_rank,omitempty"`
	LayaToolRankingRerankedRank    int    `json:"laya_tool_ranking_reranked_rank,omitempty"`
	LayaToolRankingRankGain        int    `json:"laya_tool_ranking_rank_gain,omitempty"`
	LayaToolRankingFailure         string `json:"laya_tool_ranking_failure,omitempty"`

	// The fields below are Phase 0a's measurement-integrity additions
	// (see internal/tui/agent_decision_shadow.go's preVerifierSample and
	// .claude/tasks/plans/laya-decision-architecture.md §11.0a). Additive
	// and zero-value safe like the Laya* fields above: nothing in this
	// package populates them, and no consumer here reads them — only an
	// opt-in calibration harness with an independent label source sets
	// them, on its own local copy of the trial, after the run completes.
	//
	// LayaPreVerifierAvailability is the closed-vocabulary outcome of the
	// trial's final pre-verifier observation: "available", "unavailable",
	// "late", "dropped", or "cancelled" — see preVerifierSample.Availability.
	LayaPreVerifierAvailability string `json:"laya_pre_verifier_availability,omitempty"`
	// LayaIndependentNeed is an externally supplied ground-truth label for
	// whether semantic verification was actually necessary, distinct from
	// whichever route the policy actually took. nil (omitted) means
	// unlabeled — never interpreted as a known negative.
	LayaIndependentNeed *bool `json:"laya_independent_need,omitempty"`
	// LayaLabelSource names where LayaIndependentNeed came from (e.g.
	// "fixture", "manual_review"); always empty when it is nil.
	LayaLabelSource string `json:"laya_label_source,omitempty"`
	// LayaCensorReason is non-empty only when the trial's final pre-verifier
	// correlation was invalidated by run cancellation or a decision-engine
	// config reload before it could resolve — see
	// censorPendingPreVerifierCorrelations.
	LayaCensorReason string `json:"laya_censor_reason,omitempty"`

	// The fields below are Phase 1's guarded-assist additions (see
	// internal/tui/agent_decision_policy.go and ADR 0012). Additive and
	// zero-value safe like every Laya* field above: nothing in this
	// package populates them, and production ships with
	// decisionCalibrationProfiles empty, so LayaGuardedAssistEligible is
	// false in every real deployment until a future, separately reviewed
	// change adds an approved profile. Only an opt-in calibration harness
	// sets them, on its own local copy of a trial.
	//
	// LayaGuardedAssistEligible is true only when the trial's final cycle
	// was both guard-eligible (an adaptive-mode synthetic-PASS candidate)
	// and guarded_assist was actually active (mode, wiring, and a resolved
	// profile all present) — never merely "guarded_assist was configured."
	LayaGuardedAssistEligible bool `json:"laya_guarded_assist_eligible,omitempty"`
	// LayaGuardedAssistProfile names the model alias the resolved
	// calibration profile applied to; empty whenever Eligible is false.
	LayaGuardedAssistProfile string `json:"laya_guarded_assist_profile,omitempty"`
	// LayaGuardedAssistProbability/Threshold are the exact values the
	// escalation decision compared — never rendered as a correctness
	// claim, only a recorded comparison.
	LayaGuardedAssistProbability float64 `json:"laya_guarded_assist_probability,omitempty"`
	LayaGuardedAssistThreshold   float64 `json:"laya_guarded_assist_threshold,omitempty"`
	// LayaGuardedAssistEscalated is true only when guarded_assist actually
	// forced the semantic verifier this cycle would otherwise have
	// skipped — the one behavioral effect Phase 1 permits.
	LayaGuardedAssistEscalated bool `json:"laya_guarded_assist_escalated,omitempty"`
	// LayaGuardedAssistReason is the bounded, closed-vocabulary outcome
	// code — see guardedAssistReason in agent_decision_policy.go.
	LayaGuardedAssistReason string `json:"laya_guarded_assist_reason,omitempty"`
}

// RunConformanceMatrix repeats the existing harmless provider probe. A
// recovery probe is intentionally a fresh observation; it never turns a
// visible marker into an executable ToolCall.
func RunConformanceMatrix(ctx context.Context, p provider.Provider, model string, streaming bool, cfg RunConfig) []ConformanceTrial {
	cfg = cfg.normalized()
	trials := make([]ConformanceTrial, 0, cfg.Trials)
	for trial := 1; trial <= cfg.Trials; trial++ {
		started := time.Now()
		callCtx, cancel := context.WithTimeout(ctx, cfg.Timeout)
		primary, err := provider.ProbeNativeToolCalls(callCtx, p, model, streaming)
		cancel()
		row := conformanceTrial(primary, trial, time.Since(started), err)
		if err != nil || !conformanceSucceeded(primary) {
			row.RecoveryRequired = true
			recoveryCtx, recoveryCancel := context.WithTimeout(ctx, cfg.Timeout)
			recovered, recoveryErr := provider.ProbeNativeToolCalls(recoveryCtx, p, model, streaming)
			recoveryCancel()
			row.RecoverySucceeded = recoveryErr == nil && conformanceSucceeded(recovered)
			row.Requests += recovered.Requests
			row.PromptTokens += recovered.PromptTokens
			row.CompletionTokens += recovered.CompletionTokens
			if recoveryErr != nil && row.ErrorCategory == "" {
				row.ErrorCategory = "recovery_provider_or_runtime"
			}
		}
		trials = append(trials, row)
	}
	return trials
}

func conformanceTrial(report provider.ToolCallConformance, trial int, elapsed time.Duration, err error) ConformanceTrial {
	row := ConformanceTrial{
		Trial: trial, NativeCall: report.NativeCall, ToolName: report.ToolName,
		Arguments: report.Arguments, CallID: report.CallID, Streaming: report.Streaming,
		Correlation: report.ResultCorrelation, RequiredSelection: report.RequiredSelection,
		Censored: report.SuspectedCensoring, Requests: report.Requests,
		PromptTokens: report.PromptTokens, CompletionTokens: report.CompletionTokens,
		Elapsed: elapsed,
	}
	if err != nil {
		row.ErrorCategory = "provider_or_runtime"
	}
	return row
}

func conformanceSucceeded(report provider.ToolCallConformance) bool {
	return report.NativeCall == provider.ConformancePass &&
		report.ToolName == provider.ConformancePass &&
		report.Arguments == provider.ConformancePass &&
		report.CallID == provider.ConformancePass &&
		report.ResultCorrelation == provider.ConformancePass
}

// Summary is a denominator-preserving aggregate for a conformance matrix.
type Summary struct {
	ContractTrials    int `json:"contract_trials"`
	ContractCorrect   int `json:"contract_correct"`
	ContractIncorrect int `json:"contract_incorrect"`
	ContractUnknown   int `json:"contract_unknown"`
	ContractRepairs   int `json:"contract_repairs"`
	Trials            int `json:"trials"`
	NativeSuccess     int `json:"native_success"`
	Malformed         int `json:"malformed_or_censored"`
	RecoveryRequired  int `json:"recovery_required"`
	RecoverySucceeded int `json:"recovery_succeeded"`
	CorrelationFailed int `json:"correlation_failed"`
}

// SummarizeContract aggregates labeled contract outcomes while preserving
// unknown/error denominators. A nil Correct value is not counted as correct or
// incorrect because the fixture may intentionally omit a gold label.
func SummarizeContract(trials []ContractTrial) Summary {
	var summary Summary
	summary.ContractTrials = len(trials)
	for _, trial := range trials {
		switch {
		case trial.Correct == nil:
			summary.ContractUnknown++
		case *trial.Correct:
			summary.ContractCorrect++
		default:
			summary.ContractIncorrect++
		}
		if trial.RepairRequired {
			summary.ContractRepairs++
		}
	}
	return summary
}

// SummarizeConformance aggregates statuses without interpreting a missing
// native call as a model refusal.
func SummarizeConformance(trials []ConformanceTrial) Summary {
	var summary Summary
	summary.Trials = len(trials)
	for _, trial := range trials {
		if trial.NativeCall == provider.ConformancePass && trial.ToolName == provider.ConformancePass &&
			trial.Arguments == provider.ConformancePass && trial.CallID == provider.ConformancePass &&
			trial.Correlation == provider.ConformancePass {
			summary.NativeSuccess++
		}
		if trial.Censored {
			summary.Malformed++
		}
		if trial.RecoveryRequired {
			summary.RecoveryRequired++
		}
		if trial.RecoverySucceeded {
			summary.RecoverySucceeded++
		}
		if trial.Correlation == provider.ConformanceFail {
			summary.CorrelationFailed++
		}
	}
	return summary
}

// Report is the complete bounded machine-readable evaluation output.
type Report struct {
	Metadata    Metadata           `json:"metadata"`
	Contract    []ContractTrial    `json:"contract,omitempty"`
	Conformance []ConformanceTrial `json:"conformance,omitempty"`
	Agent       []AgentTrial       `json:"agent,omitempty"`
	Summary     Summary            `json:"conformance_summary,omitempty"`
}

// WriteJSONL writes one header, one record per trial, and one summary. The
// records deliberately contain no fixture task text or provider response.
func WriteJSONL(w io.Writer, report Report) error {
	if err := ValidateMetadata(report.Metadata); err != nil {
		return fmt.Errorf("write evaluation report: %w", err)
	}
	encoder := json.NewEncoder(w)
	if err := encoder.Encode(struct {
		Kind     string   `json:"kind"`
		Metadata Metadata `json:"metadata"`
	}{Kind: "metadata", Metadata: report.Metadata}); err != nil {
		return fmt.Errorf("write evaluation metadata: %w", err)
	}
	for _, trial := range report.Contract {
		if err := encoder.Encode(struct {
			Kind     string        `json:"kind"`
			Metadata Metadata      `json:"metadata"`
			Trial    ContractTrial `json:"trial"`
		}{Kind: "contract", Metadata: report.Metadata, Trial: trial}); err != nil {
			return fmt.Errorf("write contract trial: %w", err)
		}
	}
	for _, trial := range report.Conformance {
		if err := encoder.Encode(struct {
			Kind     string           `json:"kind"`
			Metadata Metadata         `json:"metadata"`
			Trial    ConformanceTrial `json:"trial"`
		}{Kind: "conformance", Metadata: report.Metadata, Trial: trial}); err != nil {
			return fmt.Errorf("write conformance trial: %w", err)
		}
	}
	for _, trial := range report.Agent {
		if err := encoder.Encode(struct {
			Kind     string     `json:"kind"`
			Metadata Metadata   `json:"metadata"`
			Trial    AgentTrial `json:"trial"`
		}{Kind: "agent", Metadata: report.Metadata, Trial: trial}); err != nil {
			return fmt.Errorf("write agent trial: %w", err)
		}
	}
	return encoder.Encode(struct {
		Kind     string   `json:"kind"`
		Metadata Metadata `json:"metadata"`
		Summary  Summary  `json:"summary"`
	}{Kind: "summary", Metadata: report.Metadata, Summary: report.Summary})
}

// ValidateMetadata rejects the fields most likely to accidentally carry
// credentials into a report. It is intentionally conservative; callers can
// leave optional metadata empty rather than bypassing the check.
func ValidateMetadata(metadata Metadata) error {
	for name, value := range map[string]string{
		"commit": metadata.Commit, "provider": metadata.Provider, "model": metadata.Model,
		"endpoint_type": metadata.EndpointType, "backend_version": metadata.BackendVersion,
		"quantization": metadata.Quantization, "tool_schema_fingerprint": metadata.ToolSchema,
		"chat_template": metadata.ChatTemplate, "verifier_mode": metadata.VerifierMode,
		"assistance_mode": metadata.AssistanceMode, "baseline_sha": metadata.BaselineSHA,
		"candidate_sha": metadata.CandidateSHA, "fixture_hash": metadata.FixtureHash,
	} {
		if strings.ContainsAny(value, "\r\n") {
			return fmt.Errorf("metadata %s contains a line break", name)
		}
		if strings.Contains(strings.ToLower(value), "authorization:") || strings.Contains(strings.ToLower(value), "bearer ") {
			return fmt.Errorf("metadata %s looks like a credential", name)
		}
	}
	return nil
}
