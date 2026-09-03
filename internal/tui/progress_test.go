package tui

import (
	"errors"
	"os"
	"path/filepath"
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

func TestProgressIdentityCanonicalizesWorkspaceResources(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "target.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias.txt")
	if err := os.Symlink("target.txt", alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	tests := []struct {
		name string
		a    tools.Call
		b    tools.Call
	}{
		{name: "lexical path alias", a: tools.Call{Tool: tools.ToolReadFile, Path: "sub/../target.txt"}, b: tools.Call{Tool: tools.ToolReadFile, Path: "target.txt"}},
		{name: "resolved symlink alias", a: tools.Call{Tool: tools.ToolReadFile, Path: "alias.txt"}, b: tools.Call{Tool: tools.ToolReadFile, Path: "target.txt"}},
		{name: "read-only command whitespace", a: tools.Call{Tool: tools.ToolRunCommand, Body: "git   status"}, b: tools.Call{Tool: tools.ToolRunCommand, Body: "git status"}},
		{name: "read-only command path", a: tools.Call{Tool: tools.ToolRunCommand, Body: "cat ./target.txt"}, b: tools.Call{Tool: tools.ToolRunCommand, Body: "cat target.txt"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if gotA, gotB := progressFingerprintAtRoot(root, tt.a), progressFingerprintAtRoot(root, tt.b); gotA != gotB {
				t.Fatalf("identities differ:\n%s\n%s", gotA, gotB)
			}
		})
	}

	opaqueA := tools.Call{Tool: tools.ToolRunCommand, Body: `printf '%s' "a  b"`}
	opaqueB := tools.Call{Tool: tools.ToolRunCommand, Body: `printf '%s' "a b"`}
	if progressFingerprintAtRoot(root, opaqueA) == progressFingerprintAtRoot(root, opaqueB) {
		t.Fatal("approved opaque commands lost exact identity")
	}
}

func TestProgressIdentityUsesExplicitFreshnessAndPagination(t *testing.T) {
	base := tools.Call{Tool: tools.ToolWebSearch, Body: "weather", Max: 5, Freshness: "poll-1"}
	same := base
	nextPoll := base
	nextPoll.Freshness = "poll-2"
	if progressFingerprint(base) != progressFingerprint(same) {
		t.Fatal("same polling token changed identity")
	}
	if progressFingerprint(base) == progressFingerprint(nextPoll) {
		t.Fatal("explicit new polling epoch did not change identity")
	}

	page1 := tools.Call{MCPServer: "srv", MCPTool: "list", MCPArgs: `{"page":1,"filter":"open"}`}
	page2 := tools.Call{MCPServer: "srv", MCPTool: "list", MCPArgs: `{"filter":"open","page":2}`}
	if progressFingerprint(page1) == progressFingerprint(page2) {
		t.Fatal("pagination state collapsed")
	}
}

// TestProgressFingerprintReadRangeAndEdit checks that paginating through a
// file with read_file, and issuing distinct edits, each produce distinct
// fingerprints — while semantically identical calls still collide.
func TestProgressFingerprintReadRangeAndEdit(t *testing.T) {
	whole := tools.Call{Tool: tools.ToolReadFile, Path: "big.go"}
	p1 := tools.Call{Tool: tools.ToolReadFile, Path: "big.go", Offset: 1, Limit: 200}
	p2 := tools.Call{Tool: tools.ToolReadFile, Path: "big.go", Offset: 201, Limit: 200}
	if progressFingerprint(p1) == progressFingerprint(p2) {
		t.Fatal("successive read_file pages collapsed to one fingerprint")
	}
	if progressFingerprint(whole) == progressFingerprint(p1) {
		t.Fatal("a whole-file read and a ranged read collapsed")
	}
	// offset omitted + limit only == offset 1 + same limit: same operation.
	if progressFingerprint(tools.Call{Tool: tools.ToolReadFile, Path: "big.go", Limit: 200}) != progressFingerprint(p1) {
		t.Fatal("equivalent read ranges produced different fingerprints")
	}

	editA := tools.Call{Tool: tools.ToolEditFile, Path: "f.go", OldText: "x", NewText: "y"}
	editB := tools.Call{Tool: tools.ToolEditFile, Path: "f.go", OldText: "x", NewText: "z"}
	editC := tools.Call{Tool: tools.ToolEditFile, Path: "f.go", OldText: "q", NewText: "y"}
	if progressFingerprint(editA) == progressFingerprint(editB) ||
		progressFingerprint(editA) == progressFingerprint(editC) {
		t.Fatal("different edit replacements collapsed to one fingerprint")
	}
	if progressFingerprint(editA) != progressFingerprint(tools.Call{Tool: tools.ToolEditFile, Path: "f.go", OldText: "x", NewText: "y"}) {
		t.Fatal("identical edits produced different fingerprints")
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

func TestProgressDigestIncludesDetailedErrorOutput(t *testing.T) {
	call := tools.Call{MCPServer: "playwright", MCPTool: "browser_type", MCPArgs: `{"target":"wrong"}`}
	first := tools.Result{Call: call, Err: errors.New("mcp server reported an error: invalid target"), Output: "snapshot expected ref=e42"}
	changed := tools.Result{Call: call, Err: errors.New("mcp server reported an error: invalid target"), Output: "snapshot expected ref=e99"}
	if progressDigest(first) == progressDigest(changed) {
		t.Fatal("different MCP error detail produced the same progress digest")
	}
	identical := tools.Result{Call: call, Err: errors.New("mcp server reported an error: invalid target"), Output: "snapshot expected ref=e42"}
	if progressDigest(first) != progressDigest(identical) {
		t.Fatal("identical MCP failure produced an unstable progress digest")
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
