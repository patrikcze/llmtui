package tools

import "testing"

// TestGrepCappedZeroMatchIsPartialNotExhaustive is Phase 1a's §31 Phase 1a
// counters/receipt requirement: "partial search is not exhaustive proof."
// One file is skipped for exceeding the per-file size cap, and the pattern
// matches nothing in the files that were scanned. The old text-only output
// already said "no matches" in this situation (see the Phase 0
// characterization fixture in search_characterization_test.go); the typed
// contract must additionally make that zero-match result OutcomePartial with
// Coverage.SourceComplete=false, not the OutcomeOK a caller could mistake
// for an exhaustive negative.
func TestGrepCappedZeroMatchIsPartialNotExhaustive(t *testing.T) {
	root := t.TempDir()
	// Scanned and does not match.
	writeSearchFixture(t, root, "small.go", "package main\n")
	// Skipped for exceeding the 64 KB per-file cap (NewRunner(root, 64)
	// below) — and, if it were scanned, WOULD match "needle", so a coverage
	// bug that silently treated this as scanned would still report zero
	// matches, masking the false-exhaustive-negative this test guards.
	big := make([]byte, 70*1024)
	for i := range big {
		big[i] = 'x'
	}
	writeSearchFixture(t, root, "big.txt", string(big)+"\nneedle\n")

	r := NewRunner(root, 64)
	res := r.Execute(Call{Tool: ToolGrep, Body: "needle"})
	if res.Err != nil {
		t.Fatalf("grep: %v", res.Err)
	}
	if res.Meta.Outcome != OutcomePartial {
		t.Fatalf("Outcome = %q, want %q (a capped scan must never report OutcomeOK on a zero-match result)", res.Meta.Outcome, OutcomePartial)
	}
	if res.Meta.Coverage.SourceComplete {
		t.Fatal("Coverage.SourceComplete = true, want false: the skipped large file means the scan did not inspect all intended source")
	}
	found := false
	for _, reason := range res.Meta.Coverage.Reasons {
		if reason == "large" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Coverage.Reasons = %v, want it to include \"large\"", res.Meta.Coverage.Reasons)
	}
}

// TestGrepExhaustiveZeroMatchIsOKNotPartial is the companion positive case:
// when every eligible file was actually scanned, a genuine zero-match result
// is a clean negative (OutcomeOK), not manufactured as a failure or a
// partial result — see the ResultMeta.Coverage doc comment and result.go's
// "grep is OutcomeOK for an exhaustive scan (including zero matches...)".
func TestGrepExhaustiveZeroMatchIsOKNotPartial(t *testing.T) {
	root := t.TempDir()
	writeSearchFixture(t, root, "small.go", "package main\n")

	r := NewRunner(root, 64)
	res := r.Execute(Call{Tool: ToolGrep, Body: "needle"})
	if res.Err != nil {
		t.Fatalf("grep: %v", res.Err)
	}
	if res.Meta.Outcome != OutcomeOK {
		t.Fatalf("Outcome = %q, want %q (an exhaustive zero-match scan is a clean negative, not a failure)", res.Meta.Outcome, OutcomeOK)
	}
	if !res.Meta.Coverage.SourceComplete {
		t.Fatal("Coverage.SourceComplete = false, want true: every eligible file was actually scanned")
	}
}
