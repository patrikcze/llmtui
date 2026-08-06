package tui

import (
	"errors"
	"testing"

	"github.com/patrikcze/llmtui/internal/tools"
)

func TestProgressFingerprintNormalizesIncidentalDifferences(t *testing.T) {
	a := progressFingerprint(tools.Call{Tool: tools.ToolWebSearch, Body: "  Weather Brno-Bystrc  "})
	b := progressFingerprint(tools.Call{Tool: tools.ToolWebSearch, Body: "weather   brno-bystrc"})
	if a != b {
		t.Fatalf("fingerprints differ for cosmetically identical queries: %q vs %q", a, b)
	}

	c := progressFingerprint(tools.Call{Tool: tools.ToolWebFetch, Path: "https://Example.com/page/"})
	d := progressFingerprint(tools.Call{Tool: tools.ToolWebFetch, Path: "https://example.com/page"})
	if c != d {
		t.Fatalf("URL fingerprints differ for trailing-slash/case variation: %q vs %q", c, d)
	}

	e := progressFingerprint(tools.Call{Tool: tools.ToolWebSearch, Body: "a different query"})
	if a == e {
		t.Fatal("distinct queries collided to the same fingerprint")
	}
}

func TestProgressLedgerBlocksOnlyAfterThreshold(t *testing.T) {
	l := newProgressLedger(3)
	call := tools.Call{Tool: tools.ToolWebSearch, Body: "weather Brno-Bystrc"}
	unchanged := tools.Result{Call: call, Output: "same 3 results, same snippets"}

	for i := 0; i < 3; i++ {
		if blocked, _, _ := l.blockBatch([]tools.Call{call}); blocked {
			t.Fatalf("blocked before the threshold was reached (round %d)", i+1)
		}
		l.observeResults([]tools.Result{unchanged})
	}
	blocked, _, reason := l.blockBatch([]tools.Call{call})
	if !blocked {
		t.Fatal("expected the 4th identical call to be blocked")
	}
	if reason == "" {
		t.Error("blocked batch must explain why")
	}
}

func TestProgressLedgerDoesNotBlockWhenEvidenceChanges(t *testing.T) {
	l := newProgressLedger(3)
	call := tools.Call{Tool: tools.ToolWebSearch, Body: "weather Brno-Bystrc"}

	// Simulate a legitimate freshness/polling scenario: each search
	// returns materially different results, so the repeat streak never
	// builds up even though the same query is issued many times.
	for i := 0; i < 10; i++ {
		if blocked, _, _ := l.blockBatch([]tools.Call{call}); blocked {
			t.Fatalf("round %d: blocked despite changing evidence each time", i+1)
		}
		l.observeResults([]tools.Result{{Call: call, Output: differentEachTime(i)}})
	}
}

func differentEachTime(i int) string {
	out := "result set #"
	for j := 0; j <= i; j++ {
		out += "x"
	}
	return out
}

func TestProgressLedgerMixedBatchIsNotBlocked(t *testing.T) {
	l := newProgressLedger(1)
	stuck := tools.Call{Tool: tools.ToolWebSearch, Body: "weather Brno-Bystrc"}
	fresh := tools.Call{Tool: tools.ToolWebFetch, Path: "https://meteoblue.example/forecast"}
	l.observeResults([]tools.Result{{Call: stuck, Output: "same result"}})

	// stuck alone is over the threshold (threshold=1), but paired with a
	// call the ledger has never seen, the batch must not be blocked — a
	// stuck call travelling alongside a genuinely new one is not yet
	// evidence the whole batch is a no-progress loop.
	blocked, _, _ := l.blockBatch([]tools.Call{stuck, fresh})
	if blocked {
		t.Fatal("mixed batch (one stuck, one fresh) was blocked; only all-stuck batches should block")
	}
}

func TestProgressLedgerTerminalAfterRepeatedBlockedStreak(t *testing.T) {
	l := newProgressLedger(1)
	call := tools.Call{Tool: tools.ToolWebSearch, Body: "weather Brno-Bystrc"}
	l.observeResults([]tools.Result{{Call: call, Output: "same result"}})

	blocked, terminal, _ := l.blockBatch([]tools.Call{call})
	if !blocked || terminal {
		t.Fatalf("first block: blocked=%v terminal=%v, want blocked and non-terminal (a forcing function, not a hard stop)", blocked, terminal)
	}
	// The model tries the exact same blocked call again instead of
	// adapting — this is the case that must not loop forever.
	blocked, terminal, _ = l.blockBatch([]tools.Call{call})
	if !blocked || !terminal {
		t.Fatalf("second consecutive block: blocked=%v terminal=%v, want blocked and terminal", blocked, terminal)
	}
}

func TestProgressLedgerRepeatedErrorIsNoProgressButChangingErrorIsNot(t *testing.T) {
	l := newProgressLedger(2)
	call := tools.Call{Tool: tools.ToolRunCommand, Body: "flaky-test"}

	l.observeResults([]tools.Result{{Call: call, Err: errors.New("exit status 1: assertion failed at line 10")}})
	l.observeResults([]tools.Result{{Call: call, Err: errors.New("exit status 1: assertion failed at line 10")}})
	if blocked, _, _ := l.blockBatch([]tools.Call{call}); !blocked {
		t.Fatal("identical repeated command failure should count as no new evidence")
	}

	l2 := newProgressLedger(2)
	l2.observeResults([]tools.Result{{Call: call, Err: errors.New("exit status 1: assertion failed at line 10")}})
	l2.observeResults([]tools.Result{{Call: call, Err: errors.New("exit status 1: assertion failed at line 42")}})
	if blocked, _, _ := l2.blockBatch([]tools.Call{call}); blocked {
		t.Fatal("a different failure (progress toward the bug, even if still failing) should not be blocked")
	}
}
