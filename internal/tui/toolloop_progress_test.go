package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/web"
)

func TestMixedBatchApprovesAndExecutesOnlyFreshCalls(t *testing.T) {
	m := newTestModel(t)
	root := t.TempDir()
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = false
	m.cfg.Tools.NoProgress.Enabled = true
	m.progress = newProgressLedger(1)
	m.toolRunner = tools.NewRunner(root, 64)
	stuck := tools.Call{ID: "stuck", Tool: tools.ToolReadFile, Path: "stuck.txt"}
	fresh := tools.Call{ID: "fresh", Tool: tools.ToolWriteFile, Path: "fresh.txt", Body: "new evidence"}
	m.progress.observeResults([]tools.Result{{Call: stuck, Output: "unchanged"}})

	if cmd := m.startToolBatch([]tools.Call{stuck, fresh}); cmd != nil {
		t.Fatal("fresh write should wait for approval")
	}
	if len(m.pendingCalls) != 1 || m.pendingCalls[0].ID != "fresh" {
		t.Fatalf("pending calls = %+v, want only the fresh call", m.pendingCalls)
	}
	if m.pendingToolPlan == nil || m.pendingToolPlan.blockedCount() != 1 {
		t.Fatalf("pending plan = %+v, want one blocked slot", m.pendingToolPlan)
	}
	cmd := m.resolveApproval(approvalYes)
	if cmd == nil {
		t.Fatal("approved mixed plan did not start")
	}
	msg, ok := cmd().(mcpToolResultsMsg)
	if !ok {
		t.Fatal("mixed plan returned the wrong message type")
	}
	if len(msg.results) != 2 || msg.results[0].Call.ID != "stuck" || msg.results[0].Err == nil ||
		msg.results[1].Call.ID != "fresh" || msg.results[1].Err != nil {
		t.Fatalf("merged results = %+v", msg.results)
	}
	if len(msg.observed) != 1 || msg.observed[0].Call.ID != "fresh" {
		t.Fatalf("observed results = %+v", msg.observed)
	}
	data, err := os.ReadFile(filepath.Join(root, "fresh.txt"))
	if err != nil || string(data) != "new evidence" {
		t.Fatalf("fresh write = %q, %v", data, err)
	}
}

func TestTerminalFallbackNoProgressAppendsStructuredResults(t *testing.T) {
	m := newTestModel(t)
	call := tools.Call{Tool: tools.ToolWebSearch, Body: "stuck query"}
	before := len(m.session.Messages)
	if cmd := m.handleBlockedProgress(
		[]tools.Call{call},
		"repeated tool call blocked: no new evidence",
		true,
	); cmd != nil {
		t.Fatal("ordinary terminal no-progress should not dispatch another request")
	}
	if len(m.session.Messages) != before+1 {
		t.Fatalf("messages = %d, want one terminal fallback result appended", len(m.session.Messages))
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleUser || !strings.HasPrefix(last.Content, tools.ResultsPrefix) ||
		!strings.Contains(last.Content, "no new evidence") {
		t.Fatalf("terminal fallback result = %+v", last)
	}
}

func TestMixedBatchChargesOnlyExecutedCallsToAgentBudget(t *testing.T) {
	m, _ := configureAgentTestModel(t, agentScriptStep{text: "unused"})
	_ = m.startVerifiedRun("inspect two resources", nil)
	blocked := tools.Result{
		Call: tools.Call{ID: "blocked", Tool: tools.ToolReadFile, Path: "stuck.txt"},
		Err:  errors.New("repeated tool call blocked: no new evidence"),
	}
	executed := tools.Result{
		Call:   tools.Call{ID: "fresh", Tool: tools.ToolListDir},
		Output: "README.md",
	}

	m.recordAgentToolResultsCount([]tools.Result{blocked, executed}, false, 1)
	if m.agentLoop.liveToolCalls != 1 {
		t.Fatalf("live tool calls = %d, want only the one executed call", m.agentLoop.liveToolCalls)
	}
	if len(m.agentLoop.execution.ToolCalls) != 2 {
		t.Fatalf("evidence records = %d, want one correlated result per accepted call", len(m.agentLoop.execution.ToolCalls))
	}
}

// scriptedWeb is a controllable tools.WebClient stub. Search/Fetch results
// come from queued responses so a test can script "the same result every
// time" (a stuck loop) or "a different result every time" (legitimate
// polling/freshness) and count how many real calls actually happened.
type scriptedWeb struct {
	searchResults [][]web.SearchResult
	fetchPages    []web.Page
	searchCalls   int
	fetchCalls    int
}

func (s *scriptedWeb) Search(ctx context.Context, query string, max int) ([]web.SearchResult, error) {
	i := s.searchCalls
	s.searchCalls++
	if i >= len(s.searchResults) {
		i = len(s.searchResults) - 1
	}
	return s.searchResults[i], nil
}

func (s *scriptedWeb) Fetch(ctx context.Context, rawURL string) (web.Page, error) {
	i := s.fetchCalls
	s.fetchCalls++
	if i >= len(s.fetchPages) {
		i = len(s.fetchPages) - 1
	}
	return s.fetchPages[i], nil
}

// weatherToolCall builds the native tool-call step for one round: odd
// rounds search, even rounds fetch — mirroring the master prompt's §2
// failure report (search, fetch, search, fetch, ...).
func weatherToolCall(round int) provider.ToolCall {
	if round%2 == 1 {
		return provider.ToolCall{ID: "call-search", Name: tools.ToolWebSearch, Arguments: `{"query":"weather forecast Brno-Bystrc Czech Republic"}`}
	}
	return provider.ToolCall{ID: "call-fetch", Name: tools.ToolWebFetch, Arguments: `{"url":"https://meteoblue.example/forecast/brno-bystrc"}`}
}

// TestRepeatedWebSearchFetchLoopIsBoundedInOrdinaryToolMode is the
// deterministic regression fixture required by master-prompt §9.2: a real
// run entered a near-unbounded loop repeating web_search/web_fetch for a
// weather query, burning ~110k tokens with no final answer. The audit
// (docs/architecture/v1-audit.md §4.1) found this path — ordinary
// tool-enabled chat, not /agent on — had zero repeated-call protection at
// baseline, so this fixture is built against ordinary chat first, per
// docs/architecture/v1-agent-runtime.md §6.
func TestRepeatedWebSearchFetchLoopIsBoundedInOrdinaryToolMode(t *testing.T) {
	// 12 scripted tool-call rounds is well past where blocking must kick
	// in (the 4th occurrence of each fingerprint, given the default
	// progress-ledger threshold of 3) — the model in this fixture never
	// stops requesting the same search/fetch, exactly like the reported
	// run, so if the fix works, execution ends long before round 12.
	const rounds = 12
	steps := make([]agentScriptStep, 0, rounds)
	for i := 1; i <= rounds; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{weatherToolCall(i)}})
	}
	m, prov := configureAgentTestModel(t, steps...)
	m.agentOn = false // exercise the ordinary tool loop, not /agent on
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	stub := &scriptedWeb{
		// Every search returns the same three results; every fetch returns
		// the same page. "Materially unchanged output" is the master
		// prompt's own description of the failure (§2, §9.2).
		searchResults: [][]web.SearchResult{{
			{Title: "Meteoblue — Brno-Bystrc", URL: "https://meteoblue.example/forecast/brno-bystrc", Snippet: "7-day forecast"},
		}},
		fetchPages: []web.Page{{URL: "https://meteoblue.example/forecast/brno-bystrc", Status: 200, Content: "Brno-Bystrc: 18C, partly cloudy"}},
	}
	m.toolRunner.Web = stub
	m.toolRunner.WebMaxResults = 5
	m.webOn = true

	m.session.AddUser("what's the detailed weather forecast for Brno-Bystrc right now?")
	driveAgentCommands(t, m, m.dispatch("what's the detailed weather forecast for Brno-Bystrc right now?", nil))

	// The load-bearing assertion: real network calls must stop well short
	// of all 8 scripted rounds. Pre-fix, every round would have executed
	// for real (search=4, fetch=4) because nothing recognized the repeat.
	if stub.searchCalls >= 4 {
		t.Errorf("web_search executed %d times, want it capped below the round count (no-progress detection did not activate)", stub.searchCalls)
	}
	if stub.fetchCalls >= 4 {
		t.Errorf("web_fetch executed %d times, want it capped below the round count (no-progress detection did not activate)", stub.fetchCalls)
	}
	if stub.searchCalls == 0 || stub.fetchCalls == 0 {
		t.Fatalf("searchCalls=%d fetchCalls=%d: at least one legitimate call of each should have run before blocking", stub.searchCalls, stub.fetchCalls)
	}
	// The run must not have silently ground on for all 8 scripted rounds:
	// termination should have cut the provider round-trips short too, so
	// the token-burn complaint (not just the tool-execution complaint) is
	// actually addressed.
	if len(prov.requests) >= rounds {
		t.Errorf("provider requests = %d, want fewer than the full %d-round script (the run should stop, not just stop executing tools)", len(prov.requests), rounds)
	}
	if m.errText == "" || !strings.Contains(strings.ToLower(m.errText), "blocked") {
		t.Errorf("errText = %q, want a visible explanation that repetition was blocked", m.errText)
	}
}

// TestRepeatedWebSearchFetchLoopIsBoundedInAgentMode is the /agent on
// variant of the fixture above. Ordinary tool chat was the confirmed gap
// (v1-audit.md §4.1: zero repeated-call protection existed there at
// baseline), but the progress ledger is kernel-owned (ADR 0001), not
// /agent-only, so the same fingerprint blocking must also apply to a
// verified run — independent of, and in addition to, the live tool-call
// budget fix (v1-audit.md §4.2) covered by
// TestVerifiedAgentLiveToolBudgetStopsExecutionBeforeCycleBoundary.
func TestRepeatedWebSearchFetchLoopIsBoundedInAgentMode(t *testing.T) {
	const rounds = 12
	steps := make([]agentScriptStep, 0, rounds)
	for i := 1; i <= rounds; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{weatherToolCall(i)}})
	}
	m, prov := configureAgentTestModel(t, steps...)
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	stub := &scriptedWeb{
		searchResults: [][]web.SearchResult{{
			{Title: "Meteoblue — Brno-Bystrc", URL: "https://meteoblue.example/forecast/brno-bystrc", Snippet: "7-day forecast"},
		}},
		fetchPages: []web.Page{{URL: "https://meteoblue.example/forecast/brno-bystrc", Status: 200, Content: "Brno-Bystrc: 18C, partly cloudy"}},
	}
	m.toolRunner.Web = stub
	m.toolRunner.WebMaxResults = 5
	m.webOn = true

	driveAgentCommands(t, m, m.startVerifiedRun("what's the detailed weather forecast for Brno-Bystrc right now?", nil))

	if stub.searchCalls >= 4 || stub.fetchCalls >= 4 {
		t.Errorf("searchCalls=%d fetchCalls=%d, want both capped below the round count", stub.searchCalls, stub.fetchCalls)
	}
	if m.agentLoop.run.Status != agent.DecisionNoProgress {
		t.Fatalf("run status = %q, want %q", m.agentLoop.run.Status, agent.DecisionNoProgress)
	}
	if !strings.Contains(strings.ToLower(m.agentLoop.run.StopReason), "blocked") {
		t.Errorf("stop reason = %q, want it to reflect the no-progress block", m.agentLoop.run.StopReason)
	}
	if len(prov.requests) >= rounds {
		t.Errorf("provider requests = %d, want fewer than the full %d-round script", len(prov.requests), rounds)
	}
}

// TestLegitimateRepeatedWebSearchIsNotBlocked is the companion fixture
// master-prompt §9.2 explicitly requires: "also test a legitimate polling
// or freshness scenario so the protection does not ban valid repeated
// calls." Same query, same URL, same round count — but each call returns
// materially different evidence, simulating results that genuinely change
// between checks (freshness) rather than a stuck loop.
func TestLegitimateRepeatedWebSearchIsNotBlocked(t *testing.T) {
	const rounds = 8
	steps := make([]agentScriptStep, 0, rounds+1)
	for i := 1; i <= rounds; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{weatherToolCall(i)}})
	}
	// A plain final answer after the scripted rounds: this fixture only
	// asserts that changing evidence is never blocked, not that the
	// executor eventually stops asking for tools on its own.
	steps = append(steps, agentScriptStep{text: "The forecast has stabilized around 17-19C."})
	m, _ := configureAgentTestModel(t, steps...)
	m.agentOn = false
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	stub := &scriptedWeb{
		searchResults: [][]web.SearchResult{
			{{Title: "Meteoblue", URL: "https://meteoblue.example/forecast/brno-bystrc", Snippet: "updated 5 min ago: 18C"}},
			{{Title: "Meteoblue", URL: "https://meteoblue.example/forecast/brno-bystrc", Snippet: "updated 10 min ago: 17C"}},
			{{Title: "Meteoblue", URL: "https://meteoblue.example/forecast/brno-bystrc", Snippet: "updated 15 min ago: 19C"}},
			{{Title: "Meteoblue", URL: "https://meteoblue.example/forecast/brno-bystrc", Snippet: "updated 20 min ago: 16C"}},
		},
		fetchPages: []web.Page{
			{URL: "https://meteoblue.example/forecast/brno-bystrc", Status: 200, Content: "18C, partly cloudy, updated 5 min ago"},
			{URL: "https://meteoblue.example/forecast/brno-bystrc", Status: 200, Content: "17C, cloudy, updated 10 min ago"},
			{URL: "https://meteoblue.example/forecast/brno-bystrc", Status: 200, Content: "19C, sunny, updated 15 min ago"},
			{URL: "https://meteoblue.example/forecast/brno-bystrc", Status: 200, Content: "16C, rain, updated 20 min ago"},
		},
	}
	m.toolRunner.Web = stub
	m.toolRunner.WebMaxResults = 5
	m.webOn = true

	m.session.AddUser("keep checking the Brno-Bystrc forecast until it stabilizes")
	driveAgentCommands(t, m, m.dispatch("keep checking the Brno-Bystrc forecast until it stabilizes", nil))

	wantSearch, wantFetch := (rounds+1)/2, rounds/2
	if stub.searchCalls != wantSearch {
		t.Errorf("web_search executed %d times, want all %d (changing evidence must never be blocked)", stub.searchCalls, wantSearch)
	}
	if stub.fetchCalls != wantFetch {
		t.Errorf("web_fetch executed %d times, want all %d (changing evidence must never be blocked)", stub.fetchCalls, wantFetch)
	}
	if m.errText != "" {
		t.Errorf("errText = %q, want empty: legitimate repetition must not be reported as a block", m.errText)
	}
}

// TestNoProgressDetectionCanBeDisabledViaConfig proves tools.no_progress.enabled
// actually reverts to pass-through behavior, per the rollback story in
// docs/architecture/v1-migration-plan.md: the same stuck scenario that
// TestRepeatedWebSearchFetchLoopIsBoundedInOrdinaryToolMode proves gets
// blocked must run to completion unblocked once the toggle is off.
func TestNoProgressDetectionCanBeDisabledViaConfig(t *testing.T) {
	const rounds = 8
	steps := make([]agentScriptStep, 0, rounds+1)
	for i := 1; i <= rounds; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{weatherToolCall(i)}})
	}
	steps = append(steps, agentScriptStep{text: "Consistently 18C, partly cloudy."})
	m, _ := configureAgentTestModel(t, steps...)
	m.agentOn = false
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.cfg.Tools.NoProgress.Enabled = false
	stub := &scriptedWeb{
		searchResults: [][]web.SearchResult{{
			{Title: "Meteoblue — Brno-Bystrc", URL: "https://meteoblue.example/forecast/brno-bystrc", Snippet: "7-day forecast"},
		}},
		fetchPages: []web.Page{{URL: "https://meteoblue.example/forecast/brno-bystrc", Status: 200, Content: "Brno-Bystrc: 18C, partly cloudy"}},
	}
	m.toolRunner.Web = stub
	m.toolRunner.WebMaxResults = 5
	m.webOn = true

	driveAgentCommands(t, m, m.dispatch("what's the detailed weather forecast for Brno-Bystrc right now?", nil))

	wantSearch, wantFetch := (rounds+1)/2, rounds/2
	if stub.searchCalls != wantSearch || stub.fetchCalls != wantFetch {
		t.Errorf("searchCalls=%d fetchCalls=%d, want %d/%d (disabled — nothing should be blocked)", stub.searchCalls, stub.fetchCalls, wantSearch, wantFetch)
	}
	if m.errText != "" {
		t.Errorf("errText = %q, want empty: no_progress is disabled", m.errText)
	}
}
