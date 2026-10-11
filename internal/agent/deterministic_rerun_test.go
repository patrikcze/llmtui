package agent

import "testing"

// TestDeterministicTestVerdictUsesLatestRunOfEachCommand is the regression
// for a live "run the tests, fix the bug, rerun them" cycle: go test failed,
// the executor fixed the code, go test passed — and the cycle was still
// judged "deterministic test failure" because any failed run counted, so a
// correct cycle was sent back for another one. Each test command is now
// judged by its latest run in the cycle.
func TestDeterministicTestVerdictUsesLatestRunOfEachCommand(t *testing.T) {
	fail := func(name string) TestResult { return TestResult{Name: name, Passed: false, Summary: "command failed"} }
	pass := func(name string) TestResult { return TestResult{Name: name, Passed: true, Summary: "command passed"} }
	tests := []struct {
		name       string
		runs       []TestResult
		conclusive bool
		failed     string
	}{
		{name: "fail then pass is not a failure", runs: []TestResult{fail("go test ./..."), pass("go test ./...")}},
		{name: "pass then fail is a failure", runs: []TestResult{pass("go test ./..."), fail("go test ./...")}, conclusive: true, failed: "go test ./..."},
		{name: "only failed runs are a failure", runs: []TestResult{fail("go test ./..."), fail("go test ./...")}, conclusive: true, failed: "go test ./..."},
		{name: "a different command passing does not clear a failure", runs: []TestResult{fail("go test ./..."), pass("go vet ./...")}, conclusive: true, failed: "go test ./..."},
		{name: "first command that is still failing is reported", runs: []TestResult{fail("go vet ./..."), fail("go test ./..."), pass("go vet ./...")}, conclusive: true, failed: "go test ./..."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			verdict, conclusive := EvaluateDeterministic(ExecutionResult{TestsRun: tc.runs})
			if conclusive != tc.conclusive {
				t.Fatalf("conclusive = %v (%+v), want %v", conclusive, verdict, tc.conclusive)
			}
			if tc.conclusive && (verdict.Verdict != VerificationFailed || verdict.Summary != "deterministic test failure: "+tc.failed) {
				t.Fatalf("verdict = %s %q, want a failure naming %q", verdict.Verdict, verdict.Summary, tc.failed)
			}
		})
	}
}
