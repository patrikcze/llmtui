package agent

import (
	"strings"
	"testing"
)

// TestDecodeRunNormalizesLegacyTypedCriteria keeps runs persisted by builds
// that still had typed criteria resolvable: a legacy command_exit /
// file_state / test_result / user_input kind becomes semantic on load, so
// the verifier — the only remaining resolver — sees it (audit P2-6).
func TestDecodeRunNormalizesLegacyTypedCriteria(t *testing.T) {
	raw := `{"version":1,"id":"legacy-typed","request":"run the tests","status":"parked","stage":"stop_check",` +
		`"limits":{"max_cycles":8,"max_tool_calls":32,"max_tokens":100000,"max_elapsed":1800000000000,"max_repeated_failures":3},` +
		`"criteria":[{"id":"c1","text":"tests pass","status":"pending","kind":"test_result","target":"go test ./..."},` +
		`{"id":"c2","text":"choose a target","status":"pending","kind":"user_input"},` +
		`{"id":"c3","text":"explain","status":"satisfied","kind":"semantic"}]}`
	run, err := decodeRun([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	for _, criterion := range run.Criteria {
		if criterion.Kind != CriterionSemantic {
			t.Fatalf("criterion %s kind = %q, want semantic", criterion.ID, criterion.Kind)
		}
	}
	if got := len(run.UnresolvedSemanticCriteria()); got != 2 {
		t.Fatalf("unresolved semantic criteria = %d, want the two legacy typed ones", got)
	}
}

func TestCriterionReadFacts(t *testing.T) {
	run := &AgentRun{Criteria: []Criterion{
		{ID: "c1", Text: "Read the file a.txt", Status: CriterionPending, Kind: CriterionSemantic},
		{ID: "c2", Text: "Read the file b.txt", Status: CriterionPending, Kind: CriterionSemantic},
		{ID: "c3", Text: "Read the file c.txt", Status: CriterionPending, Kind: CriterionSemantic},
		{ID: "c4", Text: "Summarize the findings", Status: CriterionPending, Kind: CriterionSemantic},
		{ID: "c5", Text: "Read the file d.txt", Status: CriterionSatisfied, Kind: CriterionSemantic},
	}}
	total := int64(100)
	execution := ExecutionResult{ReadObservations: []ReadObservation{
		{Target: "a.txt", StartLine: 1, EndLine: 40, TotalLines: &total},
		{Target: "b.txt", StartLine: 1, EndLine: 100, TotalLines: &total},
	}}
	facts := map[string]string{}
	for _, fact := range run.CriterionReadFacts(execution) {
		facts[fact.CriterionID] = fact.Fact
	}
	want := map[string]string{
		"c1": "lines 1-40 of 100",
		"c2": "all 100 lines",
		"c3": "no delivered read",
	}
	for id, fragment := range want {
		if !strings.Contains(facts[id], fragment) {
			t.Errorf("fact for %s = %q, want it to contain %q", id, facts[id], fragment)
		}
	}
	if _, ok := facts["c4"]; ok {
		t.Error("free-text criterion got a fact; only exact-read criteria may")
	}
	if _, ok := facts["c5"]; ok {
		t.Error("resolved criterion got a fact")
	}
}
