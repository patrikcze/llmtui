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

	upperPath := progressFingerprint(tools.Call{Tool: tools.ToolWebFetch, Path: "https://example.com/Case"})
	lowerPath := progressFingerprint(tools.Call{Tool: tools.ToolWebFetch, Path: "https://example.com/case"})
	if upperPath == lowerPath {
		t.Fatal("case-sensitive URL paths collapsed to one fingerprint")
	}

	upperCommand := progressFingerprint(tools.Call{Tool: tools.ToolRunCommand, Body: "cat README.md"})
	lowerCommand := progressFingerprint(tools.Call{Tool: tools.ToolRunCommand, Body: "cat readme.md"})
	if upperCommand == lowerCommand {
		t.Fatal("case-sensitive command arguments collapsed to one fingerprint")
	}
	spacedCommand := progressFingerprint(tools.Call{Tool: tools.ToolRunCommand, Body: `printf '%s' "a  b"`})
	compactCommand := progressFingerprint(tools.Call{Tool: tools.ToolRunCommand, Body: `printf '%s' "a b"`})
	if spacedCommand == compactCommand {
		t.Fatal("semantically significant quoted command whitespace collapsed")
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

	plan, terminal := l.planBatch([]tools.Call{stuck, fresh})
	if terminal {
		t.Fatal("mixed batch must not terminate while one call can produce fresh evidence")
	}
	if plan.blockedCount() != 1 {
		t.Fatalf("blocked calls = %d, want only the stuck call blocked", plan.blockedCount())
	}
	runnable := plan.runnableCalls()
	if len(runnable) != 1 || progressFingerprint(runnable[0]) != progressFingerprint(fresh) {
		t.Fatalf("runnable calls = %+v, want only the fresh call", runnable)
	}
}

func TestProgressFingerprintIncludesStateChangingArguments(t *testing.T) {
	writeA := progressFingerprint(tools.Call{Tool: tools.ToolWriteFile, Path: "same.txt", Body: "alpha"})
	writeB := progressFingerprint(tools.Call{Tool: tools.ToolWriteFile, Path: "same.txt", Body: "bravo"})
	if writeA == writeB {
		t.Fatal("different write_file content collapsed to one fingerprint")
	}

	searchA := progressFingerprint(tools.Call{Tool: tools.ToolWebSearch, Body: "weather", Max: 2})
	searchB := progressFingerprint(tools.Call{Tool: tools.ToolWebSearch, Body: "weather", Max: 8})
	if searchA == searchB {
		t.Fatal("different web_search max_results collapsed to one fingerprint")
	}

	mcpA := progressFingerprint(tools.Call{MCPServer: "srv", MCPTool: "lookup", MCPArgs: `{"name":"CaseSensitive","page":1}`})
	mcpB := progressFingerprint(tools.Call{MCPServer: "srv", MCPTool: "lookup", MCPArgs: `{"page":1,"name":"CaseSensitive"}`})
	if mcpA != mcpB {
		t.Fatal("semantically identical MCP JSON arguments did not canonicalize")
	}
	mcpC := progressFingerprint(tools.Call{MCPServer: "srv", MCPTool: "lookup", MCPArgs: `{"name":"casesensitive","page":1}`})
	if mcpA == mcpC {
		t.Fatal("case-sensitive MCP argument values were incorrectly collapsed")
	}
	mcpInvalidA := progressFingerprint(tools.Call{MCPServer: "srv", MCPTool: "lookup", MCPArgs: `{"page":1} trailing`})
	mcpInvalidB := progressFingerprint(tools.Call{MCPServer: "srv", MCPTool: "lookup", MCPArgs: `{"page":1}`})
	if mcpInvalidA == mcpInvalidB {
		t.Fatal("invalid trailing MCP argument data was silently discarded")
	}
	invalidA := progressFingerprint(tools.Call{Tool: tools.ToolReadFile, InputErr: "invalid path field"})
	invalidB := progressFingerprint(tools.Call{Tool: tools.ToolReadFile, InputErr: "path must be a string"})
	if invalidA == invalidB {
		t.Fatal("distinct malformed calls collapsed to one fingerprint")
	}
}

func TestBlockedSyntheticResultDoesNotResetFingerprint(t *testing.T) {
	l := newProgressLedger(1)
	stuck := tools.Call{ID: "stuck", Tool: tools.ToolWebSearch, Body: "weather"}
	fresh := tools.Call{ID: "fresh", Tool: tools.ToolListDir}
	l.observeResults([]tools.Result{{Call: stuck, Output: "unchanged"}})

	plan, _ := l.planBatch([]tools.Call{stuck, fresh})
	merged, observed := plan.mergeResults([]tools.Result{{Call: fresh, Output: "README.md"}})
	if len(merged) != 2 || merged[0].Err == nil || len(observed) != 1 {
		t.Fatalf("merged=%+v observed=%+v", merged, observed)
	}
	l.observeResults(observed)
	if !l.wouldBlock(progressFingerprint(stuck)) {
		t.Fatal("synthetic blocked result reset the stuck fingerprint")
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
