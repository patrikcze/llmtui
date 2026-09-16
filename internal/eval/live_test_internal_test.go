package eval

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

type fixtureProvider struct {
	contract  string
	failProbe bool
	requests  int
}

func (p *fixtureProvider) Name() string                                             { return "fixture" }
func (p *fixtureProvider) ListModels(context.Context) ([]provider.ModelInfo, error) { return nil, nil }
func (p *fixtureProvider) HealthCheck(context.Context) error                        { return nil }
func (p *fixtureProvider) Capabilities() provider.Capabilities {
	return provider.Capabilities{NativeTools: provider.CapabilitySupported}
}
func (p *fixtureProvider) Chat(_ context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	p.requests++
	events := make(chan provider.ChatEvent, 2)
	if len(req.Tools) > 0 {
		if p.failProbe {
			p.failProbe = false
			events <- provider.ChatEvent{Type: provider.EventDone}
		} else if len(req.Messages) > 1 {
			events <- provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{PromptTokens: 3, CompletionTokens: 1, TotalTokens: 4}}
		} else {
			events <- provider.ChatEvent{Type: provider.EventDone, ToolCalls: []provider.ToolCall{{ID: "fixture-1", Name: "conformance_echo", Arguments: `{"probe_token":"llmtui-tool-conformance-v1"}`}}, Usage: &provider.Usage{PromptTokens: 2, CompletionTokens: 1, TotalTokens: 3}}
		}
	} else {
		events <- provider.ChatEvent{Type: provider.EventDelta, Delta: p.contract}
		events <- provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{PromptTokens: 4, CompletionTokens: 2, TotalTokens: 6}}
	}
	close(events)
	return events, nil
}

func TestRunContractMatrixRepeatsAndDoesNotStoreTask(t *testing.T) {
	p := &fixtureProvider{contract: `{"criteria":["finish it"],"needs_user_input":false,"question":"","user_options":[]}`}
	wantAsk := false
	trials := RunContractMatrix(context.Background(), p, "fixture-model", RunConfig{Trials: 3}, []ContractCase{{ID: "case/1", Task: "synthetic secret-like task", WantAsk: &wantAsk}})
	if len(trials) != 3 || p.requests != 3 {
		t.Fatalf("trials=%d requests=%d", len(trials), p.requests)
	}
	for _, trial := range trials {
		if trial.Outcome != "decompose" || trial.Correct == nil || !*trial.Correct || trial.Requests != 1 {
			t.Fatalf("trial=%+v", trial)
		}
	}
	var out strings.Builder
	if err := WriteJSONL(&out, Report{Metadata: Metadata{Provider: "fixture", Model: "fixture-model"}, Contract: trials}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "synthetic secret-like task") {
		t.Fatal("report contains fixture task text")
	}
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		var value map[string]any
		if err := json.Unmarshal([]byte(line), &value); err != nil {
			t.Fatalf("invalid JSONL %q: %v", line, err)
		}
	}
}

func TestRunConformanceMatrixRecordsBoundedRecovery(t *testing.T) {
	p := &fixtureProvider{failProbe: true}
	trials := RunConformanceMatrix(context.Background(), p, "fixture-model", true, RunConfig{Trials: 1})
	if len(trials) != 1 || !trials[0].RecoveryRequired || !trials[0].RecoverySucceeded {
		t.Fatalf("trials=%+v", trials)
	}
	summary := SummarizeConformance(trials)
	if summary.NativeSuccess != 0 || summary.RecoverySucceeded != 1 || summary.RecoveryRequired != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestSummarizeContractPreservesOutcomeDenominators(t *testing.T) {
	correct, incorrect := true, false
	summary := SummarizeContract([]ContractTrial{
		{Correct: &correct}, {Correct: &incorrect}, {RepairRequired: true},
	})
	if summary.ContractTrials != 3 || summary.ContractCorrect != 1 || summary.ContractIncorrect != 1 ||
		summary.ContractUnknown != 1 || summary.ContractRepairs != 1 {
		t.Fatalf("summary=%+v", summary)
	}
}

func TestValidateMetadataRejectsCredentialLikeValues(t *testing.T) {
	if err := ValidateMetadata(Metadata{Provider: "Bearer secret"}); err == nil {
		t.Fatal("credential-like metadata was accepted")
	}
	if err := ValidateMetadata(Metadata{Provider: "local\nendpoint"}); err == nil {
		t.Fatal("line-broken metadata was accepted")
	}
	var out strings.Builder
	if err := WriteJSONL(&out, Report{Metadata: Metadata{Model: "authorization: secret"}}); err == nil {
		t.Fatal("WriteJSONL bypassed metadata validation")
	}
}
