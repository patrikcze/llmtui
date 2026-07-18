package embedded

import "testing"

func TestStopScannerNoStops(t *testing.T) {
	s := NewStopScanner(nil)
	emit, stopped := s.Push("hello world")
	if stopped {
		t.Fatal("passthrough scanner should never stop")
	}
	if emit != "hello world" {
		t.Fatalf("emit = %q, want %q", emit, "hello world")
	}
	if got := s.Flush(); got != "" {
		t.Fatalf("Flush = %q, want empty", got)
	}
}

func TestStopScannerMatchWithinOnePiece(t *testing.T) {
	s := NewStopScanner([]string{"STOP"})
	emit, stopped := s.Push("hello STOP world")
	if !stopped {
		t.Fatal("expected stop")
	}
	if emit != "hello " {
		t.Fatalf("emit = %q, want %q", emit, "hello ")
	}
}

func TestStopScannerMatchSplitAcrossPieces(t *testing.T) {
	s := NewStopScanner([]string{"STOP"})
	emit1, stopped1 := s.Push("hello ST")
	if stopped1 {
		t.Fatal("should not stop yet: match not complete")
	}
	if emit1 != "hello " {
		t.Fatalf("emit1 = %q, want %q", emit1, "hello ")
	}
	emit2, stopped2 := s.Push("OP world")
	if !stopped2 {
		t.Fatal("expected stop once split match completes")
	}
	if emit2 != "" {
		t.Fatalf("emit2 = %q, want empty (stop string starts immediately)", emit2)
	}
}

func TestStopScannerPartialPrefixThenRelease(t *testing.T) {
	s := NewStopScanner([]string{"STOP"})
	emit1, stopped1 := s.Push("abc S")
	if stopped1 {
		t.Fatal("should not stop")
	}
	if emit1 != "abc " {
		t.Fatalf("emit1 = %q, want %q", emit1, "abc ")
	}
	// "S" was held back as a potential prefix of "STOP"; feeding something
	// that can't extend it into a match must release it.
	emit2, stopped2 := s.Push("XYZ")
	if stopped2 {
		t.Fatal("should not stop")
	}
	if emit2 != "SXYZ" {
		t.Fatalf("emit2 = %q, want %q", emit2, "SXYZ")
	}
}

func TestStopScannerFlushReturnsHeldBackText(t *testing.T) {
	s := NewStopScanner([]string{"STOP"})
	if _, stopped := s.Push("almost ST"); stopped {
		t.Fatal("should not stop")
	}
	if got := s.Flush(); got != "ST" {
		t.Fatalf("Flush = %q, want %q", got, "ST")
	}
	// Flush drains, a second Flush returns nothing.
	if got := s.Flush(); got != "" {
		t.Fatalf("second Flush = %q, want empty", got)
	}
}

func TestStopScannerNoStopsHitDuringWholeGeneration(t *testing.T) {
	s := NewStopScanner([]string{"STOP", "END"})
	var out string
	for _, piece := range []string{"the ", "quick ", "brown ", "fox"} {
		emit, stopped := s.Push(piece)
		if stopped {
			t.Fatalf("unexpected stop on piece %q", piece)
		}
		out += emit
	}
	out += s.Flush()
	if out != "the quick brown fox" {
		t.Fatalf("out = %q, want %q", out, "the quick brown fox")
	}
}

func TestStopScannerMultipleStopStringsFirstMatchWins(t *testing.T) {
	s := NewStopScanner([]string{"END", "STOP"})
	emit, stopped := s.Push("go STOP now END later")
	if !stopped {
		t.Fatal("expected stop")
	}
	if emit != "go " {
		t.Fatalf("emit = %q, want %q (earliest match should win, not first-in-list)", emit, "go ")
	}
}

func TestStopScannerEmptyStopStringsIgnored(t *testing.T) {
	s := NewStopScanner([]string{"", "STOP"})
	emit, stopped := s.Push("hello STOP")
	if !stopped {
		t.Fatal("expected stop from the non-empty stop string")
	}
	if emit != "hello " {
		t.Fatalf("emit = %q, want %q", emit, "hello ")
	}
}
