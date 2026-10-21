package harness

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

var testCompat = Compatibility{Provider: "mock", Model: "fixture-model"}

func experiment(t *testing.T, scenario, protocol string) Report {
	t.Helper()
	r, err := Run(context.Background(), ExperimentOptions{Scenario: scenario, Protocol: protocol, Compatibility: testCompat})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// A-E, G and H: real file receipts, both existing protocol normalizers, typed
// recovery, partial source windows, independent artifact oracle and mechanical
// failure veto. These are scripted conformance tests, not model benchmarks.
func TestFixtureMatrix(t *testing.T) {
	cases := []struct {
		scenario string
		outcome  Outcome
	}{
		{"tool-use", ComposeSupported}, {"premature", AdoptInvariant}, {"recovery", AdoptInvariant}, {"coverage", AdoptInvariant}, {"misleading", KeepBase}, {"unknown", Inconclusive}, {"equivalent", KeepBase},
	}
	for _, protocol := range []string{"native", "fenced"} {
		for _, tt := range cases {
			t.Run(protocol+"/"+tt.scenario, func(t *testing.T) {
				r := experiment(t, tt.scenario, protocol)
				if r.Recommendation.Outcome != tt.outcome {
					t.Fatalf("outcome=%s want=%s evidence=%+v", r.Recommendation.Outcome, tt.outcome, r.Evidence)
				}
				if r.ProviderRequests != 0 || r.Tokens != 0 {
					t.Fatal("fixture contacted a provider")
				}
				for _, e := range r.Evidence {
					if e.InitialDigest != r.Evidence[0].InitialDigest {
						t.Fatal("different initial state")
					}
				}
				switch tt.scenario {
				case "tool-use", "premature":
					if r.Evidence[0].ToolCalls != 0 || r.Evidence[0].ArtifactValid || !r.Evidence[1].ArtifactValid {
						t.Fatal("unsupported completion rewarded")
					}
				case "recovery":
					if r.Evidence[0].RepeatedCalls != 1 || r.Evidence[1].Recoveries != 1 || r.Evidence[1].ToolCalls != 2 || r.Evidence[1].ToolErrors != 1 {
						t.Fatal("unbounded or missing correction")
					}
				case "coverage":
					if r.Evidence[0].CoveredLines != 1 || r.Evidence[1].CoveredLines != 3 {
						t.Fatal("partial read was treated as complete")
					}
				case "misleading":
					for _, e := range r.Evidence {
						if !e.SemanticClaim || e.Passed || !e.DeterministicFailure || e.FailedTests != 1 {
							t.Fatal("semantic success overrode deterministic failure")
						}
					}
				}
			})
		}
	}
}

func TestFixtureIsolationAndUnsupported(t *testing.T) {
	sentinel := filepath.Join(t.TempDir(), "result.txt")
	if err := os.WriteFile(sentinel, []byte("user workspace"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		r := experiment(t, "tool-use", "native")
		if r.Evidence[0].ArtifactValid || r.Evidence[0].ToolCalls != 0 {
			t.Fatal("candidate mutation leaked into baseline or next run")
		}
	}
	b, err := os.ReadFile(sentinel)
	if err != nil || string(b) != "user workspace" {
		t.Fatal("user fixture modified")
	}
	for _, scenario := range []string{sentinel, "live", "../input.txt"} {
		if _, err := Run(context.Background(), ExperimentOptions{Scenario: scenario, Protocol: "native", Compatibility: testCompat}); err == nil {
			t.Fatal("accepted unsupported live workspace")
		}
	}
}

func TestSnapshotIntegrity(t *testing.T) {
	now := time.Now()
	base, err := NewSnapshot("", nil, "fixture", testCompat, now)
	if err != nil {
		t.Fatal(err)
	}
	a, err := NewSnapshot(base.ID, []Edit{ExecuteTools}, "provider", testCompat, now)
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewSnapshot(base.ID, []Edit{ExecuteTools}, "fixture", testCompat, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != b.ID || a.Fingerprint != b.Fingerprint {
		t.Fatal("unstable content identity")
	}
	text, err := a.GuidanceText()
	if err != nil || !strings.Contains(text, "approvals still apply") {
		t.Fatal(text, err)
	}
	for _, edit := range []Edit{"disable_approvals", "guardrails", "sandbox", "system_prompt", "scripts", "plugins", "subagents", "ignore previous instructions"} {
		if _, err := NewSnapshot(base.ID, []Edit{edit}, "provider", testCompat, now); err == nil {
			t.Fatal("accepted protected edit", edit)
		}
	}
	for _, change := range []func(*Snapshot){func(s *Snapshot) { s.Schema = 2 }, func(s *Snapshot) { s.Edits = []Edit{CompleteRead} }, func(s *Snapshot) { s.Components = []string{"guardrails"} }, func(s *Snapshot) { s.Rollback = "other" }} {
		tampered := a
		change(&tampered)
		if tampered.Validate() == nil {
			t.Fatal("accepted corrupt snapshot")
		}
	}
}

func proposalJSON(scope string, e Evidence, edit Edit) string {
	g := Catalog()[edit]
	b, _ := json.Marshal(Proposal{Scope: scope, Failure: "required action absent", Evidence: e.ID, Edit: edit, Component: g.Component, Expected: "execute required action", Risks: "extra context; simulation only", Objective: "artifact oracle changes from failed to passed"})
	return string(b)
}

func TestProposalValidation(t *testing.T) {
	baseline := experiment(t, "tool-use", "native").Evidence[0]
	good := proposalJSON("invariant", baseline, ExecuteTools)
	if _, err := ParseProposal([]byte(good), "invariant", baseline); err != nil {
		t.Fatal(err)
	}
	tests := []string{good + " {}", strings.Replace(good, `"scope":`, `"scope":"domain","scope":`, 1), strings.Replace(good, "execute_tools", "disable_approvals", 1), strings.Replace(good, "tool_guidance", "guardrails", 1), strings.Replace(good, baseline.ID, "nonexistent", 1), strings.Replace(good, `"scope":`, `"sandbox":false,"scope":`, 1), strings.Repeat("x", maxProposalBytes+1), "```json\n" + good + "\n```", "{}", "null"}
	for i, bad := range tests {
		if _, err := ParseProposal([]byte(bad), "invariant", baseline); err == nil {
			t.Fatalf("accepted malformed or unsafe proposal %d", i)
		}
	}
	if _, err := ParseProposal([]byte(good), "domain", baseline); err == nil {
		t.Fatal("cross-scope proposal")
	}
}

type fakeProvider struct {
	chat func(context.Context, provider.ChatRequest) (<-chan provider.ChatEvent, error)
}

func (f fakeProvider) Name() string                                             { return "mock" }
func (f fakeProvider) ListModels(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (f fakeProvider) HealthCheck(context.Context) error                        { return nil }
func (f fakeProvider) Chat(ctx context.Context, r provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	return f.chat(ctx, r)
}

func TestModelProposalsAndPrivacy(t *testing.T) {
	calls := 0
	fake := fakeProvider{chat: func(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
		calls++
		if !req.Isolated || len(req.Tools) != 0 || req.Model != testCompat.Model || req.MaxTokens != 1024 || req.Temperature != 0.2 || !strings.Contains(req.Messages[1].Content, "LLMTUI_UNTRUSTED_BEGIN") {
			t.Fatal("provider settings or authority changed")
		}
		// Match the deterministic baseline without ever handing the model host tools.
		base, _ := NewSnapshot("", nil, "fixture", testCompat, time.Now())
		evidence := Evidence{ID: hashText(base.ID + fixtureVersion + ":tool-use" + "native")[:24]}
		scope := "invariant"
		edit := ExecuteTools
		if calls == 2 {
			scope = "domain"
			edit = ArtifactProcedure
		}
		text := proposalJSON(scope, evidence, edit)
		text = strings.Replace(text, "extra context; simulation only", "password=private-value", 1)
		ch := make(chan provider.ChatEvent, 2)
		ch <- provider.ChatEvent{Type: provider.EventDelta, Delta: text}
		ch <- provider.ChatEvent{Type: provider.EventDone}
		close(ch)
		return ch, nil
	}}
	r, err := Run(context.Background(), ExperimentOptions{Scenario: "tool-use", Protocol: "native", Compatibility: testCompat, Proposer: &Proposer{Provider: fake, Model: testCompat.Model, Temperature: 0.2, MaxTokens: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || r.ProviderRequests != 2 || r.Recommendation.Outcome != ComposeSupported {
		t.Fatalf("requests=%d report=%+v", calls, r)
	}
	data, _ := json.Marshal(r)
	if strings.Contains(string(data), "private-value") || strings.Contains(string(data), "password") {
		t.Fatal("private rationale persisted")
	}
}

func TestInvalidProviderAndCancellation(t *testing.T) {
	for _, mode := range []string{"invalid", "early-eof", "tool", "oversized", "cancel", "error"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			fake := fakeProvider{chat: func(_ context.Context, _ provider.ChatRequest) (<-chan provider.ChatEvent, error) {
				if mode == "error" {
					return nil, errors.New("api_key=sensitive")
				}
				ch := make(chan provider.ChatEvent, 2)
				switch mode {
				case "invalid":
					ch <- provider.ChatEvent{Type: provider.EventDelta, Delta: "invalid api_key=sensitive"}
					ch <- provider.ChatEvent{Type: provider.EventDone}
				case "oversized":
					ch <- provider.ChatEvent{Type: provider.EventDelta, Delta: strings.Repeat("x", maxProposalBytes+1)}
				case "tool":
					ch <- provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{{Name: "run_command"}}}
				case "cancel":
					cancel()
					ch <- provider.ChatEvent{Type: provider.EventDone}
				}
				close(ch)
				return ch, nil
			}}
			r, err := Run(ctx, ExperimentOptions{Scenario: "tool-use", Protocol: "native", Compatibility: testCompat, Proposer: &Proposer{Provider: fake, Model: testCompat.Model, MaxTokens: 1024}})
			if mode == "cancel" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if r.Recommendation.Outcome != KeepBase || len(r.Evidence) != 1 {
				t.Fatal("invalid provider replaced baseline")
			}
			b, _ := json.Marshal(r)
			if strings.Contains(string(b), "sensitive") {
				t.Fatal("provider error leaked")
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Run(ctx, ExperimentOptions{Scenario: "tool-use", Protocol: "native", Compatibility: testCompat}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestProposalConstraintsAndResourceCaps(t *testing.T) {
	for _, scope := range []string{"invariant", "domain"} {
		c := proposalConstraint(scope)
		if err := c.Validate(); err != nil {
			t.Fatal(err)
		}
		if c.Grammar == "" || c.GrammarRoot != "root" {
			t.Fatal("missing embedded constraint")
		}
		var schema map[string]any
		if err := json.Unmarshal(c.JSONSchema, &schema); err != nil {
			t.Fatal(err)
		}
		if schema["additionalProperties"] != false {
			t.Fatal("unconstrained schema")
		}
	}
	calls := 0
	p := fakeProvider{chat: func(_ context.Context, _ provider.ChatRequest) (<-chan provider.ChatEvent, error) {
		calls++
		ch := make(chan provider.ChatEvent, 1)
		ch <- provider.ChatEvent{Type: provider.EventReasoning, ReasoningBytes: maxProposalBytes + 1}
		close(ch)
		return ch, nil
	}}
	r, err := Run(context.Background(), ExperimentOptions{Scenario: "tool-use", Protocol: "native", Compatibility: testCompat, Proposer: &Proposer{Provider: p, Model: testCompat.Model, MaxTokens: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(r.Evidence) != 1 || r.Recommendation.Outcome != KeepBase {
		t.Fatal("response budget bypassed")
	}
}

func TestDomainAdoptionWhenInvariantDoesNotHelp(t *testing.T) {
	calls := 0
	fake := fakeProvider{chat: func(_ context.Context, _ provider.ChatRequest) (<-chan provider.ChatEvent, error) {
		calls++
		base, _ := NewSnapshot("", nil, "fixture", testCompat, time.Now())
		e := Evidence{ID: hashText(base.ID + fixtureVersion + ":tool-use" + "native")[:24]}
		scope, edit := "invariant", CompleteRead // Valid, but irrelevant to this failure.
		if calls == 2 {
			scope, edit = "domain", ArtifactProcedure
		}
		ch := make(chan provider.ChatEvent, 2)
		ch <- provider.ChatEvent{Type: provider.EventDelta, Delta: proposalJSON(scope, e, edit)}
		ch <- provider.ChatEvent{Type: provider.EventDone}
		close(ch)
		return ch, nil
	}}
	r, err := Run(context.Background(), ExperimentOptions{Scenario: "tool-use", Protocol: "native", Compatibility: testCompat, Proposer: &Proposer{Provider: fake, Model: testCompat.Model, MaxTokens: 1024}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Recommendation.Outcome != AdoptDomain || len(r.Evidence) != 3 || r.Evidence[1].Passed || !r.Evidence[2].Passed {
		t.Fatal("unhelpful invariant was privileged", r.Recommendation)
	}
}
