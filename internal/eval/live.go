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
