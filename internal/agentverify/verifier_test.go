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

func validReply(verdict string) string {
	return `{"verdict":"` + verdict + `","summary":"checked evidence","retryable":false,"confidence":0.8}`
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
	client := &recordingClient{replies: []string{
		`{"verdict":"passed","summary":"checked","retryable":false,"confidence":0.8,"proposed_criteria":[{"criterion":"write report"}]}`,
		`{"verdict":"passed","summary":"checked","retryable":false,"confidence":0.8,"proposed_criteria":["write report"]}`,
	}}
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

func TestParseCriteriaProposalsAndUpdates(t *testing.T) {
	raw := `{"verdict":"passed","summary":"ok","retryable":false,"confidence":0.8,
"proposed_criteria":["current time determined","forecast covers six hours"],
"criteria":[{"id":"c1","status":"satisfied","note":"time from tool output"},{"id":"c2","status":"failed"}]}`
	result, err := Parse(raw)
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
		if _, err := Parse(raw); !errors.Is(err, agent.ErrMalformedControl) {
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
		if _, err := Parse(raw); !errors.Is(err, agent.ErrMalformedControl) {
			t.Errorf("Parse(%q) error = %v", raw, err)
		}
	}
	result, err := Parse("```json\n" + validReply("passed") + "\n```")
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
		if _, err := Parse(raw); !errors.Is(err, agent.ErrMalformedControl) {
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
