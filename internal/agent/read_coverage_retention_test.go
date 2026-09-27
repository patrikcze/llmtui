package agent

import "testing"

func lines(n int64) *int64 { return &n }

// TestReadCoverageSurvivesUnrelatedReads is the audit P2-5 overlay scenario:
// ten windows fully cover a 1000-line file, then more than 32 unrelated
// reads arrive. The old append-and-trim evicted the first window and
// re-opened a gap at line 1; whole-target eviction must keep the covered
// file intact while it is still the most recently read target.
func TestReadCoverageSurvivesUnrelatedReads(t *testing.T) {
	var execution ExecutionResult
	for i := int64(0); i < 10; i++ {
		AppendReadObservation(&execution, ReadObservation{Target: "big.txt", StartLine: i*100 + 1, EndLine: i*100 + 100, TotalLines: lines(1000)})
	}
	if len(execution.ReadObservations) != 1 {
		t.Fatalf("adjacent windows stored as %d entries, want 1 merged window", len(execution.ReadObservations))
	}
	for i := 0; i < 50; i++ {
		AppendReadObservation(&execution, ReadObservation{Target: "other.txt", StartLine: 1, EndLine: 10, TotalLines: lines(10)})
	}
	if !readCoverageComplete("big.txt", execution.ReadObservations) {
		t.Fatalf("coverage of big.txt lost after unrelated reads: %+v", execution.ReadObservations)
	}
}

func TestReadCoverageMergesOverlapsAndSupersedesOldVersions(t *testing.T) {
	var execution ExecutionResult
	AppendReadObservation(&execution, ReadObservation{Target: "a.txt", SourceDigest: "v1", StartLine: 1, EndLine: 50, TotalLines: lines(100)})
	AppendReadObservation(&execution, ReadObservation{Target: "a.txt", SourceDigest: "v1", StartLine: 40, EndLine: 100, TotalLines: lines(100)})
	if len(execution.ReadObservations) != 1 || !readCoverageComplete("a.txt", execution.ReadObservations) {
		t.Fatalf("overlapping windows = %+v, want one complete window", execution.ReadObservations)
	}
	// The file changed: the old version's coverage no longer describes it.
	AppendReadObservation(&execution, ReadObservation{Target: "a.txt", SourceDigest: "v2", StartLine: 1, EndLine: 10, TotalLines: lines(120)})
	if len(execution.ReadObservations) != 1 || execution.ReadObservations[0].SourceDigest != "v2" {
		t.Fatalf("after new version = %+v, want only the v2 window", execution.ReadObservations)
	}
	if readCoverageComplete("a.txt", execution.ReadObservations) {
		t.Fatal("stale v1 coverage still proves the changed file")
	}
}

// TestReadCoverageMatchesAcrossSpellings is the second audit overlay
// scenario: "./src/main.go" never matched a criterion naming src/main.go.
func TestReadCoverageMatchesAcrossSpellings(t *testing.T) {
	target, ok := ExactReadCriterionTarget("Read ./SRC/main.go.")
	if !ok || target != "src/main.go" {
		t.Fatalf("criterion target = %q/%v, want src/main.go", target, ok)
	}
	var execution ExecutionResult
	AppendReadObservation(&execution, ReadObservation{Target: "./src/main.go", StartLine: 1, EndLine: 5, TotalLines: lines(5)})
	if !readCoverageComplete(target, execution.ReadObservations) {
		t.Fatalf("coverage via ./ spelling not matched: %+v", execution.ReadObservations)
	}
	// Legacy observations stored in raw spelling still compare canonically.
	legacy := []ReadObservation{{Target: "./src/main.go", StartLine: 1, EndLine: 5, TotalLines: lines(5)}}
	if !readCoverageComplete("src/main.go", legacy) {
		t.Fatal("legacy raw-spelled observation not matched")
	}
}

func TestCanonicalTarget(t *testing.T) {
	for in, want := range map[string]string{
		"src/main.go":      "src/main.go",
		"./src/main.go":    "src/main.go",
		" SRC/Main.go ":    "src/main.go",
		"src/../src/a.go":  "src/a.go",
		`dir\sub\file.txt`: "dir/sub/file.txt",
		"../outside.txt":   "",
		"..":               "",
		"":                 "",
		".":                "",
		"/abs/path.txt":    "/abs/path.txt",
	} {
		if got := CanonicalTarget(in); got != want {
			t.Errorf("CanonicalTarget(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSingleTargetOverBoundDropsOnlyOldestWindow(t *testing.T) {
	var execution ExecutionResult
	for i := 0; i < MaxReadObservations+3; i++ {
		line := int64(3*i + 1)
		AppendReadObservation(&execution, ReadObservation{Target: "a.txt", StartLine: line, EndLine: line})
	}
	if len(execution.ReadObservations) != MaxReadObservations {
		t.Fatalf("len = %d, want %d", len(execution.ReadObservations), MaxReadObservations)
	}
	if first := execution.ReadObservations[0].StartLine; first != 10 {
		t.Fatalf("oldest kept window starts at %d, want 10 (three oldest dropped)", first)
	}
}

func TestValidateEpisodeCheckpointCoverageMarks(t *testing.T) {
	if err := validateEpisodeCheckpoint(&EpisodeCheckpoint{PolicyVersion: 1, CoverageHighWater: map[string]int64{"c1": -1}}); err == nil {
		t.Error("negative coverage mark accepted")
	}
	marks := map[string]int64{}
	for i := 0; i <= MaxCriteria; i++ {
		marks[string(rune('a'+i))] = 1
	}
	if err := validateEpisodeCheckpoint(&EpisodeCheckpoint{PolicyVersion: 1, CoverageHighWater: marks}); err == nil {
		t.Error("too many coverage marks accepted")
	}
}
