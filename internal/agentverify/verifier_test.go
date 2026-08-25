package agentverify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
)

type recordingClient struct {
	mu       sync.Mutex
	requests []provider.ChatRequest
	caps     provider.Capabilities
	reply    string
	replies  []string
	err      error
	block    bool
	// rejectConstrained fails only requests carrying a ResponseConstraint,
	// simulating a backend that 400s on response_format rather than
	// returning malformed control JSON.
	rejectConstrained bool
}

func (c *recordingClient) Capabilities() provider.Capabilities { return c.caps }

func (c *recordingClient) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	reply := c.reply
	if len(c.replies) > 0 {
		reply = c.replies[0]
		c.replies = c.replies[1:]
	}
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	if c.rejectConstrained && req.ResponseConstraint != nil {
		return nil, errors.New("400 bad request: response_format not supported")
	}
	events := make(chan provider.ChatEvent, 2)
	go func() {
		defer close(events)
		if c.block {
			<-ctx.Done()
			provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
			return
		}
		events <- provider.ChatEvent{Type: provider.EventDelta, Delta: reply}
		events <- provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{TotalTokens: 10}}
	}()
	return events, nil
}

func TestVerifyAdmissionStopsBeforeProviderAndBeforeRepair(t *testing.T) {
	t.Run("initial request", func(t *testing.T) {
		client := &recordingClient{}
		_, err := Verify(context.Background(), client, Config{
			Model: "test", MaxTokens: 64,
			AdmitRequest: func(int, int) error {
				return agent.NewError(agent.ErrorBudget, "admit", agent.ErrBudgetExhausted)
			},
		}, Input{})
		if !errors.Is(err, agent.ErrBudgetExhausted) {
			t.Fatalf("error = %v, want budget exhaustion", err)
		}
		if len(client.requests) != 0 {
			t.Fatalf("provider requests = %d, want 0", len(client.requests))
		}
	})

	t.Run("repair request", func(t *testing.T) {
		client := &recordingClient{reply: `not json`}
		calls := 0
		_, err := Verify(context.Background(), client, Config{
			Model: "test", MaxTokens: 64,
			AdmitRequest: func(int, int) error {
				calls++
				if calls == 2 {
					return agent.NewError(agent.ErrorBudget, "admit repair", agent.ErrBudgetExhausted)
				}
				return nil
			},
		}, Input{})
		if !errors.Is(err, agent.ErrBudgetExhausted) {
			t.Fatalf("error = %v, want budget exhaustion", err)
		}
		if len(client.requests) != 1 {
			t.Fatalf("provider requests = %d, want only the admitted initial request", len(client.requests))
		}
	})
}

// validReply builds a complete verifier envelope: all 15 fields
// verifierJSONSchema declares required, plus atomic_task, all present. Tests
// that need to exercise a specific field's value should start from this and
// override, rather than hand-writing a sparse object — Parse now rejects any
// envelope missing a required field.
func validReply(verdict string) string {
	return `{"verdict":"` + verdict + `","summary":"checked evidence","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"","retryable":false,"confidence":0.8,"new_evidence":false,` +
		`"strategy_changed":false,"transient_failure":false,"needs_user_input":false,"user_options":[],` +
		`"criteria":[],"proposed_criteria":[],"atomic_task":false}`
}

func TestVerifierUsesFreshIsolatedContext(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	input := Input{RunID: "r", Cycle: 2, Task: "original task", Objective: "bounded work", Execution: agent.ExecutionResult{Summary: "done"}}
	out, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Verdict != agent.VerificationPassed {
		t.Fatalf("result = %+v", out.Result)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	req := client.requests[0]
	if len(req.Messages) != 2 || req.Messages[0].Role != provider.RoleSystem || req.Messages[1].Role != provider.RoleUser {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if len(req.Tools) != 0 || req.Stream || req.Reasoning != "off" || req.Temperature != 0 {
		t.Fatalf("request = %+v", req)
	}
	if strings.Contains(req.Messages[1].Content, "unrelated conversation history") {
		t.Fatal("verifier received executor conversation history")
	}
}

func TestVerifierRequestsStructuredOutputWhenSupported(t *testing.T) {
	client := &recordingClient{
		reply: validReply("passed"),
		caps:  provider.Capabilities{StructuredOutput: provider.CapabilitySupported},
	}
	if _, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, Input{}); err != nil {
		t.Fatal(err)
	}
	constraint := client.requests[0].ResponseConstraint
	if constraint == nil || constraint.Name != "llmtui_verification" || !constraint.Strict {
		t.Fatalf("ResponseConstraint = %+v", constraint)
	}
	if constraint.Grammar == "" || constraint.GrammarRoot != "root" || !json.Valid(constraint.JSONSchema) {
		t.Fatalf("invalid dual response constraint: %+v", constraint)
	}
}

// TestVerifierRetriesUnconstrainedOnProviderRejection guards against a
// backend that self-reports StructuredOutput support but actually rejects
// response_format as a request error (not malformed control JSON) — the
// only retry path that existed before checked errors.Is(err,
// agent.ErrMalformedControl) and never saw this failure, so verification
// hard-failed on every cycle.
func TestVerifierRetriesUnconstrainedOnProviderRejection(t *testing.T) {
	client := &recordingClient{
		reply:             validReply("passed"),
		caps:              provider.Capabilities{StructuredOutput: provider.CapabilitySupported},
		rejectConstrained: true,
	}
	out, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, Input{})
	if err != nil {
		t.Fatalf("Verify() error = %v, want the unconstrained retry to succeed", err)
	}
	if out.Result.Verdict != agent.VerificationPassed {
		t.Fatalf("result = %+v", out.Result)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want 2 (constrained then unconstrained)", len(client.requests))
	}
	if client.requests[0].ResponseConstraint == nil {
		t.Fatal("first request should have carried a ResponseConstraint")
	}
	if client.requests[1].ResponseConstraint != nil {
		t.Fatal("retry should have dropped the ResponseConstraint")
	}
}

// TestVerifierPromptDoesNotModelPrematureFailureAsTheExample guards against a
// regression to the schema-example wording that small/quantized models (used
// as their own verifier when agent.verifier.model is unset) have been
// observed to echo verbatim: a system prompt whose JSON template shows
// retryable:false / confidence:0.0 as the example biases exactly the field
// that turns "incomplete work" into a hard, unretryable run failure. The
// prompt must instead show a safe-to-echo example and explain the field's
// actual semantics.
func TestVerifierPromptDoesNotModelPrematureFailureAsTheExample(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	input := Input{RunID: "r", Cycle: 1, Task: "task", Objective: "objective", Execution: agent.ExecutionResult{Summary: "done"}}
	if _, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, input); err != nil {
		t.Fatal(err)
	}
	system := client.requests[0].Messages[0].Content
	if strings.Contains(system, `"retryable":false`) || strings.Contains(system, `"confidence":0.0`) {
		t.Fatalf("system prompt still shows a false/0.0 example a small model could copy verbatim: %q", system)
	}
	if !strings.Contains(system, "own judgment") {
		t.Fatalf("system prompt lost the guidance to judge retryable/confidence rather than copy the example: %q", system)
	}
}

func TestVerifierRepairsMalformedCriteriaShapeWithoutExecutorCycle(t *testing.T) {
	malformedProposedCriteria := `{"verdict":"passed","summary":"checked","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"","retryable":false,"confidence":0.8,"new_evidence":false,` +
		`"strategy_changed":false,"transient_failure":false,"needs_user_input":false,"user_options":[],` +
		`"criteria":[],"atomic_task":false,"proposed_criteria":[{"criterion":"write report"}]}`
	repairedProposedCriteria := `{"verdict":"passed","summary":"checked","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"","retryable":false,"confidence":0.8,"new_evidence":false,` +
		`"strategy_changed":false,"transient_failure":false,"needs_user_input":false,"user_options":[],` +
		`"criteria":[],"atomic_task":false,"proposed_criteria":["write report"]}`
	client := &recordingClient{replies: []string{malformedProposedCriteria, repairedProposedCriteria}}
	out, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, Input{
		Task: "write a report", EstablishCriteria: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want one initial verifier request and one verifier-only repair", len(client.requests))
	}
	if !strings.Contains(client.requests[1].Messages[0].Content, "FORMAT REPAIR") {
		t.Fatalf("repair request lacks corrective schema guidance: %q", client.requests[1].Messages[0].Content)
	}
	if len(out.Result.ProposedCriteria) != 1 || out.Result.ProposedCriteria[0] != "write report" {
		t.Fatalf("result = %+v", out.Result)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 20 {
		t.Fatalf("usage = %+v, want both verifier attempts accounted", out.Usage)
	}
}

func TestVerifierRepairsEstablishingPassMissingCriteria(t *testing.T) {
	invalid := `{"verdict":"passed","summary":"all checks passed","recommended_next":"","retryable":false,` +
		`"needs_user_input":false,"criteria":[{"id":"c1","status":"satisfied"},{"id":"c2","status":"satisfied"}],` +
		`"proposed_criteria":[],"atomic_task":false}`
	repaired := `{"verdict":"passed","summary":"all checks passed","recommended_next":"","retryable":false,` +
		`"needs_user_input":false,"criteria":[{"id":"c1","status":"satisfied"},{"id":"c2","status":"satisfied"}],` +
		`"proposed_criteria":["workspace root inspected","workspace file read"],"atomic_task":false}`
	client := &recordingClient{replies: []string{invalid, repaired}}

	out, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, Input{
		Task: "inspect the workspace and read a file", EstablishCriteria: true,
		Execution: agent.ExecutionResult{Summary: "completed both checks"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want initial verifier request plus one repair", len(client.requests))
	}
	repairSystem := client.requests[1].Messages[0].Content
	for _, want := range []string{"ESTABLISHING REPAIR", "no pinned criterion IDs", "passed + proposed_criteria:[] + atomic_task:false is invalid"} {
		if !strings.Contains(repairSystem, want) {
			t.Fatalf("repair prompt missing %q: %q", want, repairSystem)
		}
	}
	if len(out.Result.ProposedCriteria) != 2 || len(out.Result.CriteriaUpdates) != 2 {
		t.Fatalf("result = %+v, want two proposed criteria and matching updates", out.Result)
	}
	if out.Usage == nil || out.Usage.TotalTokens != 20 {
		t.Fatalf("usage = %+v, want both verifier calls accounted", out.Usage)
	}
}

func TestVerifierExamplesMatchEstablishingMode(t *testing.T) {
	const laterCycleExample = `"criteria":[{"id":"c1","status":"satisfied"}],"proposed_criteria":[],"atomic_task":false`
	const establishingExample = `"criteria":[{"id":"c1","status":"satisfied"}],"proposed_criteria":["first independently checkable requirement"],"atomic_task":false`

	later := verifierMessages(`{"EstablishCriteria":false}`, false)[0].Content
	if !strings.Contains(later, laterCycleExample) || strings.Contains(later, "ESTABLISHING REPAIR") {
		t.Fatalf("non-establishing prompt changed its example or gained repair guidance: %q", later)
	}
	if strings.Contains(later, establishingExample) {
		t.Fatalf("non-establishing prompt contains establishing example: %q", later)
	}

	establishing := verifierMessages(`{"EstablishCriteria":true}`, true)[0].Content
	if !strings.Contains(establishing, establishingExample) {
		t.Fatalf("establishing prompt lacks valid criteria proposal example: %q", establishing)
	}
	if strings.Contains(establishing, laterCycleExample) {
		t.Fatalf("establishing prompt still contains passed + empty proposals + non-atomic example: %q", establishing)
	}
}

// TestVerifierEvidenceCarriesAvailableToolsAndPromptUsesThem guards a real
// observed failure: a run asking for live weather/events data (no such tool
// exists, only web_search/web_fetch) got marked retryable forever because
// the verifier had no way to know that capability was never on offer. It
// kept recommending a "weather API" that doesn't exist in llmtui, spinning
// cycles until agent.max_cycles. Input.Tools must reach the verifier's
// evidence, and the system prompt must instruct it to check requests
// against that list before ever calling something merely "retryable".
func TestVerifierEvidenceCarriesAvailableToolsAndPromptUsesThem(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	input := Input{
		RunID: "r", Cycle: 1, Task: "task", Objective: "objective",
		Execution: agent.ExecutionResult{Summary: "done"},
		Tools:     []string{"web_search", "web_fetch", "read_file"},
	}
	if _, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, input); err != nil {
		t.Fatal(err)
	}
	req := client.requests[0]
	system := req.Messages[0].Content
	if !strings.Contains(system, "Tools") {
		t.Fatalf("system prompt does not reference the Tools evidence field: %q", system)
	}
	evidence := req.Messages[1].Content
	for _, want := range []string{"web_search", "web_fetch", "read_file"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("evidence missing tool %q: %s", want, evidence)
		}
	}
}

// TestVerifierPromptTeachesNeedsUserInput guards the instruction and schema
// wiring for detecting a clarifying question addressed to the user: without
// this, an executor that asks "which source should I check first?" and
// hedges instead of proceeding has no way to stop the run with the question
// surfaced, and the run just grinds through retries to DecisionFailed.
func TestVerifierPromptTeachesNeedsUserInput(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	input := Input{RunID: "r", Cycle: 1, Task: "task", Objective: "objective", Execution: agent.ExecutionResult{Summary: "done"}}
	if _, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, input); err != nil {
		t.Fatal(err)
	}
	system := client.requests[0].Messages[0].Content
	if !strings.Contains(system, `"needs_user_input"`) {
		t.Fatalf("system prompt does not mention needs_user_input: %q", system)
	}
	if !strings.Contains(verifierJSONSchema, `"needs_user_input": {"type": "boolean"}`) {
		t.Fatal("verifier JSON schema is missing needs_user_input")
	}
	if !strings.Contains(verifierJSONSchema, `"required": [`) || !strings.Contains(verifierJSONSchema[strings.Index(verifierJSONSchema, `"required"`):], `"needs_user_input"`) {
		t.Fatal("verifier JSON schema must declare needs_user_input as required")
	}
}

// TestParseNeedsUserInputRoundTrips confirms a raw verifier response setting
// needs_user_input survives Parse into the agent.VerificationResult.
func TestParseNeedsUserInputRoundTrips(t *testing.T) {
	raw := `{"verdict":"inconclusive","summary":"Which source would you like me to check first?","evidence":[],` +
		`"failed_criteria":[],"remaining_criteria":[],"recommended_next":"","retryable":false,"confidence":0.5,` +
		`"new_evidence":false,"strategy_changed":false,"transient_failure":false,"needs_user_input":true,` +
		`"user_options":[],"criteria":[],"proposed_criteria":[],"atomic_task":false}`
	result, err := Parse(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.NeedsUserInput {
		t.Fatalf("result = %+v, want NeedsUserInput true", result)
	}
}

// TestApplyDeterministicEvidencePreservesNeedsUserInput locks in that a
// conclusive deterministic failure (e.g. a failed tool call) does not erase
// the verifier's own signal that the executor also asked a genuine question
// this same cycle — both were true in the real run this fix addresses.
func TestApplyDeterministicEvidencePreservesNeedsUserInput(t *testing.T) {
	result := agent.VerificationResult{
		Verdict: agent.VerificationPassed, Summary: "claims done", NeedsUserInput: true,
	}
	execution := agent.ExecutionResult{TestsRun: []agent.TestResult{{Name: "go test", Passed: false}}}
	clamped := ApplyDeterministicEvidence(result, execution)
	if clamped.Verdict != agent.VerificationFailed {
		t.Fatalf("clamped verdict = %v, want failed", clamped.Verdict)
	}
	if !clamped.NeedsUserInput {
		t.Fatal("ApplyDeterministicEvidence must not clear NeedsUserInput")
	}
}

// TestVerifierPromptTeachesUserOptions guards the instruction and schema
// wiring for extracting discrete choices from the executor's question: a
// real run showed the executor writing numbered options as plain prose
// ("1. Prague / 2. Humpolec / 3. Brno") — without this, the TUI has no way
// to present them as a pickable overlay and always falls back to free text.
func TestVerifierPromptTeachesUserOptions(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	input := Input{RunID: "r", Cycle: 1, Task: "task", Objective: "objective", Execution: agent.ExecutionResult{Summary: "done"}}
	if _, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, input); err != nil {
		t.Fatal(err)
	}
	system := client.requests[0].Messages[0].Content
	if !strings.Contains(system, `"user_options"`) {
		t.Fatalf("system prompt does not mention user_options: %q", system)
	}
	if !strings.Contains(verifierJSONSchema, `"user_options": {"type": "array", "items": {"type": "string"}}`) {
		t.Fatal("verifier JSON schema is missing user_options")
	}
	if strings.Contains(verifierJSONSchema[strings.Index(verifierJSONSchema, `"required"`):], `"user_options"`) {
		t.Fatal("verifier JSON schema must keep user_options optional")
	}
}

// TestParseUserOptionsRoundTrips confirms a raw verifier response setting
// user_options survives Parse into the agent.VerificationResult.
func TestParseUserOptionsRoundTrips(t *testing.T) {
	raw := `{"verdict":"inconclusive","summary":"Which city first?","evidence":[],"failed_criteria":[],` +
		`"remaining_criteria":[],"recommended_next":"","retryable":false,"confidence":0.5,"new_evidence":false,` +
		`"strategy_changed":false,"transient_failure":false,"needs_user_input":true,` +
		`"user_options":["Prague","Humpolec","Brno"],"criteria":[],"proposed_criteria":[],"atomic_task":false}`
	result, err := Parse(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Prague", "Humpolec", "Brno"}
	if len(result.UserOptions) != len(want) {
		t.Fatalf("result = %+v, want UserOptions %v", result, want)
	}
	for i := range want {
		if result.UserOptions[i] != want[i] {
			t.Fatalf("UserOptions[%d] = %q, want %q", i, result.UserOptions[i], want[i])
		}
	}
}

// TestApplyDeterministicEvidencePreservesUserOptions mirrors the existing
// NeedsUserInput preservation test: a conclusive deterministic failure must
// not erase the verifier's extracted options for the same reason it must
// not erase NeedsUserInput itself.
func TestApplyDeterministicEvidencePreservesUserOptions(t *testing.T) {
	result := agent.VerificationResult{
		Verdict: agent.VerificationPassed, Summary: "claims done",
		NeedsUserInput: true, UserOptions: []string{"Prague", "Humpolec", "Brno"},
	}
	execution := agent.ExecutionResult{TestsRun: []agent.TestResult{{Name: "go test", Passed: false}}}
	clamped := ApplyDeterministicEvidence(result, execution)
	if len(clamped.UserOptions) != 3 {
		t.Fatalf("ApplyDeterministicEvidence must not clear UserOptions: %+v", clamped)
	}
}

func TestParseCriteriaProposalsAndUpdates(t *testing.T) {
	raw := `{"verdict":"passed","summary":"ok","evidence":[],"failed_criteria":[],"remaining_criteria":[],
"recommended_next":"","retryable":false,"confidence":0.8,"new_evidence":false,"strategy_changed":false,
"transient_failure":false,"needs_user_input":false,"user_options":[],"atomic_task":false,
"proposed_criteria":["current time determined","forecast covers six hours"],
"criteria":[{"id":"c1","status":"satisfied","note":"time from tool output"},{"id":"c2","status":"failed","note":""}]}`
	result, err := Parse(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ProposedCriteria) != 2 || len(result.CriteriaUpdates) != 2 {
		t.Fatalf("result = %+v", result)
	}
	if result.CriteriaUpdates[0].Status != agent.CriterionSatisfied || result.CriteriaUpdates[1].Status != agent.CriterionFailed {
		t.Fatalf("criteria updates = %+v", result.CriteriaUpdates)
	}
}

func TestParseRejectsInvalidCriterionUpdates(t *testing.T) {
	for _, raw := range []string{
		`{"verdict":"passed","summary":"ok","confidence":0.5,"criteria":[{"id":"c1","status":"probably"}]}`,
		`{"verdict":"passed","summary":"ok","confidence":0.5,"criteria":[{"id":"","status":"satisfied"}]}`,
	} {
		if _, err := Parse(raw, false); !errors.Is(err, agent.ErrMalformedControl) {
			t.Errorf("Parse(%q) error = %v, want malformed control", raw, err)
		}
	}
}

// The verifier must receive the cumulative bounded run state — pinned
// criteria with statuses, the evidence ledger, prior-cycle summaries — not
// only the current cycle, and the system prompt must explain those fields.
func TestVerifierEvidenceCarriesCumulativeRunState(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	input := Input{
		RunID: "r", Cycle: 3, Task: "task", Objective: "objective",
		Criteria: []agent.Criterion{
			{ID: "c1", Text: "gather data", Status: agent.CriterionSatisfied},
			{ID: "c2", Text: "produce report", Status: agent.CriterionPending},
		},
		Evidence: []agent.EvidenceItem{
			{Cycle: 1, Kind: agent.EvidenceTest, Source: "go test ./...", Success: true},
		},
		PriorCycles: []agent.MemoryEntry{
			{Cycle: 1, Objective: "first objective", Verdict: agent.VerificationPassed},
		},
		Execution: agent.ExecutionResult{Summary: "done"},
	}
	if _, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, input); err != nil {
		t.Fatal(err)
	}
	req := client.requests[0]
	evidence := req.Messages[1].Content
	for _, want := range []string{"gather data", "produce report", "go test ./...", "first objective", "c1", "c2"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("evidence missing cumulative state %q: %s", want, evidence)
		}
	}
	system := req.Messages[0].Content
	for _, want := range []string{"Criteria", "proposed_criteria", "PriorCycles"} {
		if !strings.Contains(system, want) {
			t.Fatalf("system prompt does not explain %q: %s", want, system)
		}
	}
}

// A deterministic failure must also void the semantic verdict's criterion
// status updates: a cycle whose mechanical outcome is failure cannot newly
// satisfy anything.
func TestDeterministicOverrideDropsCriteriaUpdates(t *testing.T) {
	result := agent.VerificationResult{
		Verdict: agent.VerificationPassed, Summary: "claims done",
		CriteriaUpdates: []agent.CriterionUpdate{{ID: "c1", Status: agent.CriterionSatisfied}},
	}
	execution := agent.ExecutionResult{TestsRun: []agent.TestResult{{Name: "go test", Passed: false}}}
	clamped := ApplyDeterministicEvidence(result, execution)
	if clamped.Verdict != agent.VerificationFailed || len(clamped.CriteriaUpdates) != 0 {
		t.Fatalf("clamped = %+v, want failed verdict with no criterion updates", clamped)
	}
}

func TestMalformedControlOutput(t *testing.T) {
	for _, raw := range []string{"not json", `{"verdict":"maybe","summary":"x"}`, `{"verdict":"passed"}`, validReply("passed") + validReply("failed")} {
		if _, err := Parse(raw, false); !errors.Is(err, agent.ErrMalformedControl) {
			t.Errorf("Parse(%q) error = %v", raw, err)
		}
	}
	result, err := Parse("```json\n"+validReply("passed")+"\n```", false)
	if err != nil || result.Verdict != agent.VerificationPassed {
		t.Fatalf("fenced parse = %+v, %v", result, err)
	}
}

func TestParseRejectsMalformedProposedCriteriaShapes(t *testing.T) {
	for _, raw := range []string{
		`{"verdict":"passed","summary":"ok","retryable":false,"confidence":0.8,"proposed_criteria":"write report"}`,
		`{"verdict":"passed","summary":"ok","retryable":false,"confidence":0.8,"proposed_criteria":{"criterion":"write report"}}`,
		`{"verdict":"passed","summary":"ok","retryable":false,"confidence":0.8,"proposed_criteria":[{"criterion":"write report"}]}`,
	} {
		if _, err := Parse(raw, false); !errors.Is(err, agent.ErrMalformedControl) {
			t.Errorf("Parse(%q) error = %v, want malformed control", raw, err)
		}
	}
}

func TestDeterministicFailureOverridesOptimisticModel(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	exec := agent.ExecutionResult{
		Summary:  "looks good",
		TestsRun: []agent.TestResult{{Name: "go test ./...", Passed: false, Summary: "failed"}},
	}
	out, err := Verify(context.Background(), client, Config{Timeout: time.Second}, Input{Task: "fix", Objective: "test", Execution: exec})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Verdict != agent.VerificationFailed || !out.Result.Retryable {
		t.Fatalf("result = %+v", out.Result)
	}
}

func TestDeterministicFailureOverridesTruncatedExecutorReply(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	exec := agent.ExecutionResult{
		Summary: "wrote the file successfully",
		Errors:  []agent.RunError{agent.NewError(agent.ErrorTruncated, "executor", errors.New("response was cut off by max_tokens"))},
	}
	out, err := Verify(context.Background(), client, Config{Timeout: time.Second}, Input{Task: "fix", Objective: "test", Execution: exec})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Verdict != agent.VerificationFailed || !out.Result.Retryable || !out.Result.TransientFailure {
		t.Fatalf("result = %+v, want deterministic retryable transient failure despite an optimistic model verdict", out.Result)
	}
}

func TestVerifierTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		client := &recordingClient{block: true}
		_, err := Verify(context.Background(), client, Config{Timeout: 10 * time.Millisecond}, Input{Task: "x"})
		var runErr agent.RunError
		if !errors.As(err, &runErr) || runErr.Kind != agent.ErrorTimeout {
			t.Fatalf("error = %#v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		client := &recordingClient{block: true}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Verify(ctx, client, Config{Timeout: time.Second}, Input{Task: "x"})
		var runErr agent.RunError
		if !errors.As(err, &runErr) || runErr.Kind != agent.ErrorCancelled {
			t.Fatalf("error = %#v", err)
		}
	})
}

// verifierRequiredFieldOrder mirrors verifier.go's minimal required fields —
// kept as an independent literal (not a reference to the unexported package
// var) so these tests would themselves fail loudly if a required field were
// ever added to the schema without a matching entry here.
var verifierRequiredFieldOrder = []string{
	"verdict", "summary", "recommended_next", "retryable", "needs_user_input",
	"criteria", "proposed_criteria", "atomic_task",
}

var verifierEnvelopeFieldOrder = []string{
	"verdict", "summary", "evidence", "failed_criteria", "remaining_criteria",
	"recommended_next", "retryable", "confidence", "new_evidence", "strategy_changed",
	"transient_failure", "needs_user_input", "user_options", "criteria",
	"proposed_criteria", "atomic_task",
}

// completeEnvelopeFields holds one valid raw-JSON value per required field —
// the canonical "everything present, everything well-typed" envelope that
// buildEnvelope starts from for every strict-validation test below.
var completeEnvelopeFields = map[string]string{
	"verdict":            `"passed"`,
	"summary":            `"checked evidence"`,
	"evidence":           `["ran go test"]`,
	"failed_criteria":    `[]`,
	"remaining_criteria": `[]`,
	"recommended_next":   `""`,
	"retryable":          `false`,
	"confidence":         `0.8`,
	"new_evidence":       `false`,
	"strategy_changed":   `false`,
	"transient_failure":  `false`,
	"needs_user_input":   `false`,
	"user_options":       `[]`,
	"criteria":           `[{"id":"c1","status":"satisfied","note":"done"}]`,
	"proposed_criteria":  `["criterion one"]`,
	"atomic_task":        `false`,
}

// buildEnvelope renders a complete verifier envelope from
// completeEnvelopeFields, applying overrides (by field name) and omitting
// any field named in omit — the shared fixture builder for every strict
// presence/type/null test below, so each test only spells out the one field
// it is actually exercising.
func buildEnvelope(overrides map[string]string, omit ...string) string {
	fields := make(map[string]string, len(completeEnvelopeFields))
	for k, v := range completeEnvelopeFields {
		fields[k] = v
	}
	for k, v := range overrides {
		fields[k] = v
	}
	omitSet := make(map[string]bool, len(omit))
	for _, k := range omit {
		omitSet[k] = true
	}
	var b strings.Builder
	b.WriteByte('{')
	first := true
	for _, key := range verifierEnvelopeFieldOrder {
		if omitSet[key] {
			continue
		}
		if !first {
			b.WriteByte(',')
		}
		first = false
		b.WriteByte('"')
		b.WriteString(key)
		b.WriteString(`":`)
		b.WriteString(fields[key])
	}
	b.WriteByte('}')
	return b.String()
}

// TestParseRejectsEachMissingRequiredField is the table-driven guard for
// verifierJSONSchema's minimal "required" list: omitting any required key
// must fail Parse, and the error must name that field so
// the repair-prompt round-trip has something concrete to act on.
func TestParseRejectsEachMissingRequiredField(t *testing.T) {
	for _, field := range verifierRequiredFieldOrder {
		t.Run(field, func(t *testing.T) {
			raw := buildEnvelope(nil, field)
			_, err := Parse(raw, false)
			if !errors.Is(err, agent.ErrMalformedControl) {
				t.Fatalf("Parse() with %q omitted: err = %v, want malformed control", field, err)
			}
			if !strings.Contains(err.Error(), field) {
				t.Fatalf("Parse() with %q omitted: error %v does not name the missing field", field, err)
			}
		})
	}
}

func TestParseAcceptsMinimalAndLegacyVerifierEnvelopes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
	}{
		{
			name: "minimal atomic pass",
			raw:  `{"verdict":"passed","summary":"done","recommended_next":"","retryable":false,"needs_user_input":false,"user_options":[],"criteria":[],"proposed_criteria":[],"atomic_task":true}`,
		},
		{
			name: "legacy full envelope",
			raw:  buildEnvelope(nil),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.raw, true)
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != agent.VerificationPassed {
				t.Fatalf("verdict = %q", result.Verdict)
			}
		})
	}
}

func TestVerifierSchemaExposesOnlyMinimalContract(t *testing.T) {
	for _, legacy := range []string{"evidence", "failed_criteria", "remaining_criteria", "confidence", "new_evidence", "strategy_changed", "transient_failure"} {
		if strings.Contains(verifierJSONSchema, `"`+legacy+`"`) {
			t.Fatalf("schema still asks new verifiers for legacy field %q", legacy)
		}
	}
}

func TestWeakModelMinimalJSONCorpus(t *testing.T) {
	tests := []struct {
		name         string
		raw          string
		establishing bool
		verdict      agent.VerificationVerdict
	}{
		{
			name:         "markdown fenced atomic pass",
			raw:          "```json\n" + `{"verdict":"passed","summary":"done","recommended_next":"","retryable":false,"needs_user_input":false,"user_options":[],"criteria":[],"proposed_criteria":[],"atomic_task":true}` + "\n```",
			establishing: true,
			verdict:      agent.VerificationPassed,
		},
		{
			name:         "proposal and update without optional note",
			raw:          `{"summary":"done","verdict":"passed","retryable":false,"recommended_next":"","criteria":[{"id":"c1","status":"satisfied"}],"needs_user_input":false,"user_options":[],"atomic_task":false,"proposed_criteria":["tests pass"]}`,
			establishing: true,
			verdict:      agent.VerificationPassed,
		},
		{
			name:         "open ended user question with null options",
			raw:          `{"verdict":"inconclusive","summary":"Which target?","recommended_next":"","retryable":false,"needs_user_input":true,"user_options":null,"criteria":[],"proposed_criteria":[],"atomic_task":false}`,
			establishing: false,
			verdict:      agent.VerificationInconclusive,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := Parse(tt.raw, tt.establishing)
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != tt.verdict {
				t.Fatalf("verdict = %q, want %q", result.Verdict, tt.verdict)
			}
		})
	}
}

func TestVerifierPromptTokenSnapshot(t *testing.T) {
	messages := verifierMessages(`{"Task":"bounded task","EstablishCriteria":true}`, true)
	if tokens := provider.EstimateMessagesTokens(messages); tokens > 1300 {
		t.Fatalf("minimal verifier prompt snapshot = %d tokens, want <= 1300", tokens)
	}
}

// TestParseRejectsSparseREL002Repro is the exact regression this task
// exists to close: a minimal {"verdict":"passed","summary":"ok"} object
// (missing the other controller-required fields) must never again parse
// successfully — under the old plain json.Unmarshal, Go zero-filled every
// omitted field and this object completed a run's establishing cycle having
// pinned zero acceptance criteria.
func TestParseRejectsSparseREL002Repro(t *testing.T) {
	raw := `{"verdict":"passed","summary":"ok"}`
	for _, establishing := range []bool{true, false} {
		if _, err := Parse(raw, establishing); !errors.Is(err, agent.ErrMalformedControl) {
			t.Errorf("Parse(%q, establishing=%v) err = %v, want malformed control", raw, establishing, err)
		}
	}
}

// TestParseRejectsUnknownField enforces "additionalProperties": false at the
// application layer: an otherwise-complete envelope carrying one extra key
// must be rejected, and the error must name it.
func TestParseRejectsUnknownField(t *testing.T) {
	valid := buildEnvelope(nil)
	raw := valid[:len(valid)-1] + `,"unexpected_field":"surprise"}`
	_, err := Parse(raw, false)
	if !errors.Is(err, agent.ErrMalformedControl) {
		t.Fatalf("err = %v, want malformed control", err)
	}
	if !strings.Contains(err.Error(), "unexpected_field") {
		t.Fatalf("error does not name the unexpected field: %v", err)
	}
}

// TestParseRejectsNullRequiredScalars covers non-nullable scalar fields in
// the minimal contract: a key present but set to
// JSON null is not the same as a real value and must be rejected, not
// silently zero-valued.
func TestParseRejectsNullRequiredScalars(t *testing.T) {
	for _, field := range []string{
		"verdict", "summary", "retryable", "needs_user_input", "atomic_task",
	} {
		t.Run(field, func(t *testing.T) {
			raw := buildEnvelope(map[string]string{field: "null"})
			if _, err := Parse(raw, false); !errors.Is(err, agent.ErrMalformedControl) {
				t.Fatalf("field %q = null: err = %v, want malformed control", field, err)
			}
		})
	}
}

// TestParseAcceptsNullArraysAsEmpty covers both minimal and legacy arrays:
// some backends emit null for an empty array, so null must be accepted and
// normalized to empty rather than rejected like a null scalar.
func TestParseAcceptsNullRequiredArraysAsEmpty(t *testing.T) {
	for _, field := range []string{
		"evidence", "failed_criteria", "remaining_criteria", "user_options", "criteria", "proposed_criteria",
	} {
		t.Run(field, func(t *testing.T) {
			raw := buildEnvelope(map[string]string{field: "null"})
			result, err := Parse(raw, false)
			if err != nil {
				t.Fatalf("field %q = null: err = %v, want accepted", field, err)
			}
			var length int
			switch field {
			case "evidence":
				length = len(result.Evidence)
			case "failed_criteria":
				length = len(result.FailedCriteria)
			case "remaining_criteria":
				length = len(result.RemainingCriteria)
			case "user_options":
				length = len(result.UserOptions)
			case "criteria":
				length = len(result.CriteriaUpdates)
			case "proposed_criteria":
				length = len(result.ProposedCriteria)
			}
			if length != 0 {
				t.Fatalf("field %q = null: length = %d, want 0", field, length)
			}
		})
	}
}

// TestParseAcceptsNullRecommendedNextAsEmptyString covers recommended_next,
// the one required scalar documented as "one changed bounded objective or
// empty" — unlike the other 8 required scalars, an explicit null here is
// treated the same as "", not rejected.
func TestParseAcceptsNullRecommendedNextAsEmptyString(t *testing.T) {
	raw := buildEnvelope(map[string]string{"recommended_next": "null"})
	result, err := Parse(raw, false)
	if err != nil {
		t.Fatalf("err = %v, want accepted", err)
	}
	if result.RecommendedNext != "" {
		t.Fatalf("RecommendedNext = %q, want empty", result.RecommendedNext)
	}
}

// TestParseNonEstablishingEmptyProposedCriteriaAccepted confirms the
// atomic-task requirement is scoped to establishing passes only: a complete,
// non-establishing "passed" envelope with empty proposed_criteria and
// atomic_task:false is a perfectly normal later-cycle verification and must
// be accepted.
func TestParseNonEstablishingEmptyProposedCriteriaAccepted(t *testing.T) {
	raw := buildEnvelope(map[string]string{"verdict": `"passed"`, "proposed_criteria": `[]`, "atomic_task": "false"})
	if _, err := Parse(raw, false); err != nil {
		t.Fatalf("err = %v, want accepted (establishing=false)", err)
	}
}

// TestParseEstablishingPassedEmptyCriteriaWithoutAtomicTaskRejected is the
// exact false-completion scenario REL-002 describes: an establishing pass
// reporting "passed" with nothing proposed and atomic_task left false must
// be rejected, not allowed to silently pin zero acceptance criteria.
func TestParseEstablishingPassedEmptyCriteriaWithoutAtomicTaskRejected(t *testing.T) {
	raw := buildEnvelope(map[string]string{"verdict": `"passed"`, "proposed_criteria": `[]`, "atomic_task": "false"})
	if _, err := Parse(raw, true); !errors.Is(err, agent.ErrMalformedControl) {
		t.Fatalf("err = %v, want malformed control", err)
	}
}

// TestParseEstablishingPassedEmptyCriteriaWithAtomicTaskAccepted confirms
// the escape hatch: the same envelope as above, but with atomic_task:true,
// is a deliberate verifier judgment that the task doesn't decompose, and
// must be accepted.
func TestParseEstablishingPassedEmptyCriteriaWithAtomicTaskAccepted(t *testing.T) {
	raw := buildEnvelope(map[string]string{"verdict": `"passed"`, "proposed_criteria": `[]`, "atomic_task": "true"})
	result, err := Parse(raw, true)
	if err != nil {
		t.Fatalf("err = %v, want accepted", err)
	}
	if !result.AtomicTask {
		t.Fatalf("AtomicTask = %v, want true", result.AtomicTask)
	}
}

// TestParseEstablishingNonEmptyProposedCriteriaAcceptedRegardlessOfAtomicTask
// confirms atomic_task is only consulted when proposed_criteria is empty: a
// non-empty proposal satisfies the establishing-pass requirement on its own,
// whatever atomic_task says.
func TestParseEstablishingNonEmptyProposedCriteriaAcceptedRegardlessOfAtomicTask(t *testing.T) {
	for _, atomic := range []string{"true", "false"} {
		t.Run("atomic_task="+atomic, func(t *testing.T) {
			raw := buildEnvelope(map[string]string{
				"verdict":           `"passed"`,
				"proposed_criteria": `["criterion one","criterion two"]`,
				"atomic_task":       atomic,
			})
			if _, err := Parse(raw, true); err != nil {
				t.Fatalf("err = %v, want accepted", err)
			}
		})
	}
}

// TestParseFullEnvelopeRoundTrip is the positive-path safety net: every
// field type appears once with a distinguishable, non-default value, and
// every one of them must map correctly into agent.VerificationResult — not
// merely "Parse returns no error" — so the strict validation above cannot
// have accidentally started rejecting (or mis-mapping) a well-formed real
// verifier response.
func TestParseFullEnvelopeRoundTrip(t *testing.T) {
	raw := `{"verdict":"failed","summary":"go test failed","evidence":["ran go test","observed failure"],` +
		`"failed_criteria":["tests pass"],"remaining_criteria":["tests pass","docs updated"],` +
		`"recommended_next":"fix the failing test","retryable":true,"confidence":0.42,"new_evidence":true,` +
		`"strategy_changed":true,"transient_failure":false,"needs_user_input":false,` +
		`"user_options":["retry","abort"],"criteria":[{"id":"c1","status":"failed","note":"go test ./... failed"}],` +
		`"proposed_criteria":["tests pass","docs updated"],"atomic_task":false}`
	result, err := Parse(raw, false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != agent.VerificationFailed {
		t.Errorf("Verdict = %q, want failed", result.Verdict)
	}
	if result.Summary != "go test failed" {
		t.Errorf("Summary = %q, want %q", result.Summary, "go test failed")
	}
	if len(result.Evidence) != 2 || result.Evidence[0] != "ran go test" || result.Evidence[1] != "observed failure" {
		t.Errorf("Evidence = %#v", result.Evidence)
	}
	if len(result.FailedCriteria) != 1 || result.FailedCriteria[0] != "tests pass" {
		t.Errorf("FailedCriteria = %#v", result.FailedCriteria)
	}
	if len(result.RemainingCriteria) != 2 || result.RemainingCriteria[0] != "tests pass" || result.RemainingCriteria[1] != "docs updated" {
		t.Errorf("RemainingCriteria = %#v", result.RemainingCriteria)
	}
	if result.RecommendedNext != "fix the failing test" {
		t.Errorf("RecommendedNext = %q", result.RecommendedNext)
	}
	if !result.Retryable {
		t.Error("Retryable = false, want true")
	}
	if result.Confidence != 0.42 {
		t.Errorf("Confidence = %v, want 0.42", result.Confidence)
	}
	if !result.NewEvidence {
		t.Error("NewEvidence = false, want true")
	}
	if !result.StrategyChanged {
		t.Error("StrategyChanged = false, want true")
	}
	if result.TransientFailure {
		t.Error("TransientFailure = true, want false")
	}
	if result.NeedsUserInput {
		t.Error("NeedsUserInput = true, want false")
	}
	if len(result.UserOptions) != 2 || result.UserOptions[0] != "retry" || result.UserOptions[1] != "abort" {
		t.Errorf("UserOptions = %#v", result.UserOptions)
	}
	if len(result.CriteriaUpdates) != 1 || result.CriteriaUpdates[0].ID != "c1" ||
		result.CriteriaUpdates[0].Status != agent.CriterionFailed || result.CriteriaUpdates[0].Note != "go test ./... failed" {
		t.Errorf("CriteriaUpdates = %#v", result.CriteriaUpdates)
	}
	if len(result.ProposedCriteria) != 2 || result.ProposedCriteria[0] != "tests pass" || result.ProposedCriteria[1] != "docs updated" {
		t.Errorf("ProposedCriteria = %#v", result.ProposedCriteria)
	}
	if result.AtomicTask {
		t.Error("AtomicTask = true, want false")
	}
}
