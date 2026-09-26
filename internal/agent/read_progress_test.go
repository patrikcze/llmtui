package agent

import "testing"

func TestExactReadCriterionPreservesFullReadQualifier(t *testing.T) {
	for _, text := range []string{"Read the file a.txt in full.", "Read `a.txt` completely.", "Read a.txt fully."} {
		target, ok := ExactReadCriterionTarget(text)
		if !ok || target != "a.txt" {
			t.Fatalf("target for %q = %q, %v; want a.txt", text, target, ok)
		}
	}
}

func TestNextReadOffsetStopsAtFirstGap(t *testing.T) {
	total := int64(1500)
	obs := []ReadObservation{
		{Target: "a.txt", StartLine: 1, EndLine: 200, TotalLines: &total, SourceDigest: "v1"},
		{Target: "a.txt", StartLine: 801, EndLine: 1300, TotalLines: &total, SourceDigest: "v1"},
	}
	offset, limit, ok := NextReadOffset("a.txt", obs)
	if !ok || offset != 201 || limit != 600 {
		t.Fatalf("next = %d, %d, %v; want first gap 201-800", offset, limit, ok)
	}
}

func TestReadCoverageProgressDigestTracksCoverageNotReceipts(t *testing.T) {
	total := int64(1500)
	criteria := []Criterion{{ID: "c1", Text: "Read the file a.txt"}}
	first := ReadObservation{Target: "a.txt", StartLine: 1, EndLine: 200, TotalLines: &total, SourceDigest: "v1"}
	digest := ReadCoverageProgressDigest(criteria, []ReadObservation{first})
	if digest == "" {
		t.Fatal("known coverage must produce a digest")
	}
	duplicate := first
	duplicate.Sequence = 5
	if got := ReadCoverageProgressDigest(criteria, []ReadObservation{first, duplicate}); got != digest {
		t.Fatal("duplicate receipt counted as progress")
	}
	second := first
	second.StartLine, second.EndLine = 801, 900
	advanced := ReadCoverageProgressDigest(criteria, []ReadObservation{first, second})
	if advanced == digest || advanced == "" {
		t.Fatal("new coverage beyond a gap must count as progress")
	}
	if got := ReadCoverageProgressDigest(criteria, []ReadObservation{second, first}); got != advanced {
		t.Fatal("receipt order changed coverage digest")
	}
	second.SourceDigest = "v2"
	if got := ReadCoverageProgressDigest(criteria, []ReadObservation{first, second}); got != "" {
		t.Fatal("mixed source versions counted as progress")
	}
	first.TotalLines = nil
	if got := ReadCoverageProgressDigest(criteria, []ReadObservation{first}); got != "" {
		t.Fatal("unknown total counted as known coverage")
	}
}
