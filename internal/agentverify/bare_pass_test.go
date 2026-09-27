package agentverify

import (
	"context"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
)

const (
	barePass = `{"verdict":"passed","summary":"looks done","recommended_next":"","retryable":false,` +
		`"needs_user_input":false,"criteria":[],"proposed_criteria":[],"atomic_task":false}`
	perIDPass = `{"verdict":"passed","summary":"looks done","recommended_next":"","retryable":false,` +
		`"needs_user_input":false,"criteria":[{"id":"c1","status":"satisfied"},{"id":"c2","status":"pending"}],` +
		`"proposed_criteria":[],"atomic_task":false}`
)

func twoCriteriaInput() Input {
	return Input{Task: "do two things", Criteria: []agent.Criterion{
		{ID: "c1", Text: "first thing", Status: agent.CriterionPending, Kind: agent.CriterionSemantic},
		{ID: "c2", Text: "second thing", Status: agent.CriterionPending, Kind: agent.CriterionSemantic},
	}}
}

// TestVerifyRepairsBareMultiCriterionPass is the regression for the
// verifier half of audit P2-6: a "passed" with no per-criterion statuses
// over several criteria used to satisfy all of them. It now triggers one
// repair request naming the pinned IDs, and the repaired per-ID statuses win.
func TestVerifyRepairsBareMultiCriterionPass(t *testing.T) {
	client := &recordingClient{replies: []string{barePass, perIDPass}}
	out, err := Verify(context.Background(), client, Config{}, twoCriteriaInput())
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 2 {
		t.Fatalf("requests = %d, want the original plus one criteria repair", len(client.requests))
	}
	repairPrompt := client.requests[1].Messages[0].Content
	if !strings.Contains(repairPrompt, "CRITERIA REPAIR") || !strings.Contains(repairPrompt, "c1, c2") {
		t.Fatalf("repair prompt does not ask for per-ID statuses:\n%s", repairPrompt)
	}
	if len(out.Result.CriteriaUpdates) != 2 || out.Result.CriteriaUpdates[1].Status != agent.CriterionPending {
		t.Fatalf("result updates = %+v, want the repaired per-ID statuses", out.Result.CriteriaUpdates)
	}
}

// TestVerifyKeepsBarePassWhenRepairFails keeps a failed repair from turning
// a valid verdict into a verifier failure: the original bare pass comes
// back, and the controller then satisfies nothing implicitly.
func TestVerifyKeepsBarePassWhenRepairFails(t *testing.T) {
	client := &recordingClient{replies: []string{barePass, "not json at all"}}
	out, err := Verify(context.Background(), client, Config{}, twoCriteriaInput())
	if err != nil {
		t.Fatalf("Verify err = %v, want the original bare pass kept", err)
	}
	if out.Result.Verdict != agent.VerificationPassed || len(out.Result.CriteriaUpdates) != 0 {
		t.Fatalf("result = %+v, want the bare pass unchanged", out.Result)
	}
}

func TestVerifyDoesNotRepairUnambiguousBarePass(t *testing.T) {
	input := twoCriteriaInput()
	input.Criteria = input.Criteria[:1]
	client := &recordingClient{reply: barePass}
	if _, err := Verify(context.Background(), client, Config{}, input); err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d, want no repair for a single criterion", len(client.requests))
	}
}

func TestVerifierEvidenceCarriesCriterionFacts(t *testing.T) {
	input := twoCriteriaInput()
	input.CriterionFacts = []string{`c1: all 12 lines of "a.txt" delivered`}
	data, err := marshalVerifierEvidence(input)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `all 12 lines`) {
		t.Fatalf("verifier evidence lacks criterion facts: %s", data)
	}
}
