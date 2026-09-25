package tui

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/eval"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/ollama"
	"github.com/patrikcze/llmtui/internal/provider/openai"
	"github.com/patrikcze/llmtui/internal/tools"
)

// liveAgentCase describes one synthetic fixture plus the independent,
// fixture-owned postcondition oracle that decides whether the run actually
// did the right thing — never DecisionDone or the semantic verifier's
// verdict alone. expectedAnswer/expectedTargetFile/expectedContent are
// content-free in the JSONL report (they never leave this test file); they
// exist only so the oracle can check bytes on disk / substrings in the
// final visible answer.
type liveAgentCase struct {
	id, request, expectedAction, answer, approval string
	seedFiles                                     map[string]string

	// postcondition is the independent oracle for this fixture. It receives
	// the finished model, the run, and (for confirm_write) whether a
	// mutation was observed to exist before approval was granted.
	postcondition func(m *Model, run *agent.AgentRun, mutatedBeforeApproval bool) postconditionResult
}

// postconditionResult is the content-free outcome of an independent
// fixture oracle. It never carries raw paths, arguments, or answer text —
// only classification, matching eval.AgentTrial's privacy contract.
type postconditionResult struct {
	passed               bool
	failure              string // one of eval.AgentTrial's PostconditionFailure values, or ""
	unexpectedSideEffect bool
	duplicateEffect      bool
	mutationCount        int
}

// finalAnswer returns the most recent assistant message's visible text, or
// "" if none exists yet.
func finalAnswer(m *Model) string {
	for i := len(m.session.Messages) - 1; i >= 0; i-- {
		if m.session.Messages[i].Role == provider.RoleAssistant {
			return m.session.Messages[i].Content
		}
	}
	return ""
}

// executedCalls returns, in chronological (cycle then call) order, every
// tool call in run that matches name and actually executed.
func executedCalls(run *agent.AgentRun, name string) []agent.ToolCallRecord {
	if run == nil {
		return nil
	}
	var calls []agent.ToolCallRecord
	for _, cycle := range run.Cycles {
		if cycle.Execution == nil {
			continue
		}
		for _, call := range cycle.Execution.ToolCalls {
			if call.Name == name && call.Status == agent.ActionExecuted {
				calls = append(calls, call)
			}
		}
	}
	return calls
}

// firstCallIndex returns the chronological position (0-based, across all
// cycles) of the first tool call matching match, or -1 if none matched.
func firstCallIndex(run *agent.AgentRun, match func(agent.ToolCallRecord) bool) int {
	if run == nil {
		return -1
	}
	idx := 0
	for _, cycle := range run.Cycles {
		if cycle.Execution == nil {
			continue
		}
		for _, call := range cycle.Execution.ToolCalls {
			if match(call) {
				return idx
			}
			idx++
		}
	}
	return -1
}

// hasMutation reports whether run executed any write_file or edit_file
// call — used by read-only fixtures to detect an unexpected side effect.
func hasMutation(run *agent.AgentRun) bool {
	return len(executedCalls(run, tools.ToolWriteFile)) > 0 || len(executedCalls(run, tools.ToolEditFile)) > 0
}

// readKnownFilePostcondition verifies notes.txt was read and the final
// answer actually contains its known first line, independent of
// DecisionDone or the verifier's verdict.
func readKnownFilePostcondition(m *Model, run *agent.AgentRun, _ bool) postconditionResult {
	if hasMutation(run) {
		return postconditionResult{unexpectedSideEffect: true}
	}
	reads := executedCalls(run, tools.ToolReadFile)
	if len(reads) == 0 {
		return postconditionResult{failure: "no_read_observed"}
	}
	sawExpectedPath := false
	for _, r := range reads {
		if r.Detail == "notes.txt" {
			sawExpectedPath = true
		}
	}
	if !sawExpectedPath {
		return postconditionResult{failure: "wrong_path_read"}
	}
	if !strings.Contains(finalAnswer(m), "hello from live fixture") {
		return postconditionResult{failure: "answer_missing_evidence"}
	}
	return postconditionResult{passed: true}
}

// askMissingPathPostcondition verifies the agent asked before any
// speculative read, then read exactly the file the user supplied, and the
// final answer reflects that file's real content.
func askMissingPathPostcondition(m *Model, run *agent.AgentRun, _ bool) postconditionResult {
	if hasMutation(run) {
		return postconditionResult{unexpectedSideEffect: true}
	}
	askIdx := firstCallIndex(run, func(c agent.ToolCallRecord) bool { return c.Name == tools.ToolAskUser })
	readIdx := firstCallIndex(run, func(c agent.ToolCallRecord) bool { return c.Name == tools.ToolReadFile })
	if readIdx != -1 && (askIdx == -1 || readIdx < askIdx) {
		return postconditionResult{failure: "read_before_ask"}
	}
	reads := executedCalls(run, tools.ToolReadFile)
	if len(reads) == 0 {
		return postconditionResult{failure: "no_read_observed"}
	}
	sawExpectedPath := false
	for _, r := range reads {
		if r.Detail == "report.md" {
			sawExpectedPath = true
		}
	}
	if !sawExpectedPath {
		return postconditionResult{failure: "wrong_path_read"}
	}
	if !strings.Contains(finalAnswer(m), "Live fixture") {
		return postconditionResult{failure: "answer_missing_evidence"}
	}
	return postconditionResult{passed: true}
}

// confirmWritePostcondition verifies no mutation happened before approval,
// exactly one write landed at the right path with the right bytes, and
// counts duplicate/no-op rewrites instead of silently ignoring them.
func confirmWritePostcondition(m *Model, run *agent.AgentRun, mutatedBeforeApproval bool) postconditionResult {
	if mutatedBeforeApproval {
		return postconditionResult{failure: "mutation_before_approval"}
	}
	writes := executedCalls(run, tools.ToolWriteFile)
	result := postconditionResult{mutationCount: len(writes)}
	if len(executedCalls(run, tools.ToolEditFile)) > 0 {
		result.unexpectedSideEffect = true
	}
	switch {
	case len(writes) == 0:
		result.failure = "no_mutation"
		return result
	case len(writes) > 1:
		result.duplicateEffect = true
		result.failure = "content_mismatch" // more than one attempt is itself a wrong outcome
		return result
	}
	write := writes[0]
	if write.Detail != "result.txt" {
		result.failure = "wrong_target_path"
		return result
	}
	got, err := os.ReadFile(filepath.Join(m.toolRunner.Root(), "result.txt"))
	if err != nil || string(got) != "approved" {
		result.failure = "content_mismatch"
		return result
	}
	result.passed = true
	return result
}

// TestLiveAgentMatrix runs the real bounded contract -> executor -> tool ->
// verifier loop. It is deliberately skipped without an explicit endpoint and
// model, and every mutation is confined to a fresh temporary directory.
func TestLiveAgentMatrix(t *testing.T) {
	baseURL := os.Getenv("LLMTUI_EVAL_BASE_URL")
	model := os.Getenv("LLMTUI_EVAL_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LLMTUI_EVAL_BASE_URL and LLMTUI_EVAL_MODEL to run the live agent matrix")
	}

	endpointType := strings.ToLower(os.Getenv("LLMTUI_EVAL_ENDPOINT_TYPE"))
	if endpointType == "" {
		endpointType = "openai_compatible"
	}
	var raw provider.Provider
	switch endpointType {
	case "openai_compatible", "lmstudio", "vllm", "llama.cpp":
		raw = openai.New("eval", baseURL, os.Getenv("LLMTUI_EVAL_API_KEY"))
	case "ollama":
		raw = ollama.New(baseURL, ollama.WithName("eval"))
	default:
		t.Fatalf("unsupported endpoint type %q", endpointType)
	}
	defer func() { _ = provider.CloseProvider(raw) }()
	counted := &countingProvider{Provider: raw}

	trials := 5
	if value := os.Getenv("LLMTUI_EVAL_TRIALS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			t.Fatalf("LLMTUI_EVAL_TRIALS must be a positive integer: %q", value)
		}
		trials = parsed
	}
	cases := []liveAgentCase{
		{
			id: "read_known_file", expectedAction: "TOOL_CALL",
			request:       "Read notes.txt and tell me its first line. The file path is exact; do not ask me for another path.",
			seedFiles:     map[string]string{"notes.txt": "hello from live fixture\nsecond line\n"},
			postcondition: readKnownFilePostcondition,
		},
		{
			id: "ask_missing_path", expectedAction: "ASK",
			request: "Read the file I mentioned and give me its heading. Ask me for the filename before reading anything.",
			answer:  "report.md", seedFiles: map[string]string{"report.md": "# Live fixture\nbody\n"},
			postcondition: askMissingPathPostcondition,
		},
		{
			id: "confirm_write", expectedAction: "CONFIRM",
			request:       "Create result.txt containing exactly approved. Ask for confirmation before writing it.",
			approval:      "y",
			postcondition: confirmWritePostcondition,
		},
	}

	metadata := eval.Metadata{
		Commit: os.Getenv("LLMTUI_EVAL_COMMIT"), Provider: counted.Name(),
		EndpointType: endpointType, Model: model, ToolSchema: "workspace-tools:v1",
		MaxTokens: 4096, VerifierMode: "always", AssistanceMode: "shadow",
		WarmModel:    os.Getenv("LLMTUI_EVAL_WARM") == "true",
		BaselineSHA:  os.Getenv("LLMTUI_EVAL_BASELINE_SHA"),
		CandidateSHA: os.Getenv("LLMTUI_EVAL_CANDIDATE_SHA"),
		FixtureHash:  os.Getenv("LLMTUI_EVAL_FIXTURE_HASH"),
	}
	if err := eval.ValidateMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	report := eval.Report{Metadata: metadata}
	var driverFailures []string
	for _, fixture := range cases {
		for trial := 1; trial <= trials; trial++ {
			started := time.Now()
			m := newTestModel(t)
			m.prov = counted
			m.model = model
			m.agentOn = true
			m.cfg.Agent.Verifier.Enabled = true
			m.cfg.Agent.Verifier.Mode = "always"
			m.cfg.Agent.Verifier.Timeout = "2m"
			m.cfg.Agent.Verifier.MaxTokens = 4096
			m.cfg.Agent.Verifier.MaxAttempts = 2
			m.cfg.Agent.MaxCycles = 2
			m.cfg.Agent.MaxToolCalls = 8
			m.cfg.Agent.MaxElapsed = "5m"
			m.cfg.Agent.MaxTokens = 20000
			m.toolsOn = true
			m.toolsNative = true
			m.toolRunner = tools.NewRunner(t.TempDir(), 64)
			for name, body := range fixture.seedFiles {
				path := filepath.Join(m.toolRunner.Root(), name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			beforeRequests := counted.requestCount()
			var mutatedBeforeApproval bool
			var beforeApproval func()
			if fixture.id == "confirm_write" {
				beforeApproval = func() {
					if _, err := os.Stat(filepath.Join(m.toolRunner.Root(), "result.txt")); err == nil {
						mutatedBeforeApproval = true
					}
				}
			}
			driverError := driveLiveAgent(t, m, m.startVerifiedRun(fixture.request, nil), fixture.answer, fixture.approval, beforeApproval)
			row := liveAgentTrial(fixture, trial, m, counted.requestCount()-beforeRequests, time.Since(started), mutatedBeforeApproval)
			row.ErrorCategory = driverError
			report.Agent = append(report.Agent, row)
			if driverError != "" {
				driverFailures = append(driverFailures, fixture.id+"/"+strconv.Itoa(trial)+": "+driverError)
			}
			t.Logf("scenario=%s trial=%d action=%s final=%s tools=%d requests=%d elapsed=%s",
				fixture.id, trial, row.ObservedAction, row.FinalResult, row.ToolCalls, row.ProviderRequests, time.Duration(row.Elapsed).Round(time.Millisecond))
		}
	}

	path := os.Getenv("LLMTUI_EVAL_OUTPUT")
	if path == "" {
		path = filepath.Join(t.TempDir(), "llmtui-live-agent.jsonl")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create evaluation report: %v", err)
	}
	if err := eval.WriteJSONL(file, report); err != nil {
		_ = file.Close()
		t.Fatalf("write evaluation report: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("live agent matrix: trials=%d report=%s", len(report.Agent), path)
	if len(driverFailures) > 0 {
		t.Fatalf("live agent driver failures (report retained): %s", strings.Join(driverFailures, "; "))
	}
}

type countingProvider struct {
	provider.Provider
	mu       sync.Mutex
	requests int
}

func (p *countingProvider) Chat(ctx context.Context, request provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
	return p.Provider.Chat(ctx, request)
}

func (p *countingProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

// driveLiveAgent pumps the Bubble Tea update loop to completion. beforeApproval,
// when non-nil, is invoked exactly once, synchronously, immediately before the
// first approval keypress is sent — this is the only point at which a test can
// observe filesystem state as it stood strictly before approval, to verify a
// mutation never lands ahead of it.
func driveLiveAgent(t *testing.T, m *Model, first tea.Cmd, answer, approval string, beforeApproval func()) string {
	t.Helper()
	queue := []tea.Cmd{first}
	for steps := 0; steps < 300; steps++ {
		if len(queue) > 0 {
			cmd := queue[0]
			queue = queue[1:]
			if cmd == nil {
				continue
			}
			msg := cmd()
			if batch, ok := msg.(tea.BatchMsg); ok {
				queue = append(queue, batch...)
				continue
			}
			_, next := m.Update(msg)
			if next != nil {
				queue = append(queue, next)
			}
			continue
		}
		if m.pendingAsk != nil {
			if answer == "" {
				return "unresolved_user_input"
			}
			m.input.SetValue(answer)
			_, next := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if next != nil {
				queue = append(queue, next)
			}
			answer = ""
			continue
		}
		if len(m.pendingCalls) > 0 {
			if approval == "" {
				return "unresolved_approval"
			}
			if beforeApproval != nil {
				beforeApproval()
				beforeApproval = nil
			}
			code := rune(approval[0])
			_, next := m.Update(tea.KeyPressMsg{Code: code, Text: approval})
			if next != nil {
				queue = append(queue, next)
			}
			approval = ""
			continue
		}
		if !m.agentRunActive() && !m.thinking {
			return ""
		}
		return "driver_idle_while_run_active"
	}
	return "driver_event_limit"
}

func liveAgentTrial(fixture liveAgentCase, trial int, m *Model, requests int, elapsed time.Duration, mutatedBeforeApproval bool) eval.AgentTrial {
	row := eval.AgentTrial{
		Scenario: fixture.id, Trial: trial, ExpectedAction: fixture.expectedAction,
		FinalResult: "unknown", ProviderRequests: requests, Elapsed: elapsed,
	}
	if m.agentLoop == nil || m.agentLoop.run == nil {
		row.ObservedAction = "OTHER"
		return row
	}
	run := m.agentLoop.run
	if m.cfg.DecisionEngine.ResolvedMode() == config.DecisionEngineModeCriterionShadow {
		metrics := m.criterionAssessmentMetrics
		row.LayaCriterionAssessmentMode = config.DecisionEngineModeCriterionShadow
		row.LayaCriterionAssessmentModel = m.lastDebug.DecisionCriterionAssessmentModel
		row.LayaCriterionAssessmentTotal = metrics.Total
		row.LayaCriterionAssessmentAvailable = metrics.Available
		row.LayaCriterionAssessmentAbstained = metrics.Abstained
		row.LayaCriterionAssessmentErrors = metrics.Errors
		row.LayaCriterionAssessmentLate = metrics.Late
		row.LayaCriterionAssessmentAvailability = m.lastDebug.DecisionCriterionAssessmentAvailability
		row.LayaCriterionAssessmentSignal = m.lastDebug.DecisionCriterionAssessmentSignal
		row.LayaCriterionAssessmentSpecFingerprint = m.lastDebug.DecisionCriterionAssessmentSpecFingerprint
		row.LayaCriterionAssessmentEvidenceFingerprint = m.lastDebug.DecisionCriterionAssessmentEvidenceFingerprint
		row.LayaCriterionAssessmentModelRevision = m.lastDebug.DecisionCriterionAssessmentModelRevision
		row.LayaCriterionAssessmentSupportProbability = m.lastDebug.DecisionCriterionAssessmentSupportProbability
		row.LayaCriterionAssessmentContradictionProbability = m.lastDebug.DecisionCriterionAssessmentContradictionProbability
	}
	if m.cfg.DecisionEngine.ResolvedMode() == config.DecisionEngineModeCriterionAssist {
		row.LayaCriterionAssistEligible = m.lastDebug.DecisionCriterionAssistEligible
		row.LayaCriterionAssistProfile = m.lastDebug.DecisionCriterionAssistProfile
		row.LayaCriterionAssistEscalated = m.lastDebug.DecisionCriterionAssistEscalated
		row.LayaCriterionAssistReason = m.lastDebug.DecisionCriterionAssistReason
	}
	row.FinalResult = string(run.Status)
	row.PromptTokens = run.PromptTokens
	row.CompletionTokens = run.CompletionTokens
	row.Cycles = len(run.Cycles)
	row.NeedsUserInput = m.agentNeedsUserInput()
	for _, event := range m.toolCallDiagnostics {
		if event.Stage == provider.ToolCallStageRecovery {
			row.RecoveryRequests++
		}
	}
	for _, cycle := range run.Cycles {
		if cycle.Verification != nil {
			row.VerifierVerdict = string(cycle.Verification.Verdict)
		}
		if cycle.Execution == nil {
			continue
		}
		row.ToolCalls += len(cycle.Execution.ToolCalls)
		for _, call := range cycle.Execution.ToolCalls {
			switch call.Status {
			case agent.ActionExecuted:
				row.ToolExecuted++
				if call.Succeeded {
					row.ToolSucceeded++
				} else {
					row.ToolFailed++
				}
			case agent.ActionDenied:
				row.ToolDenied++
			case agent.ActionBlocked:
				row.ToolBlocked++
			default:
				row.ToolUnknown++
			}
			if row.ObservedAction == "" {
				switch call.Name {
				case tools.ToolAskUser:
					row.ObservedAction = "ASK"
				case tools.ToolWriteFile, tools.ToolEditFile:
					row.ObservedAction = "CONFIRM"
				default:
					row.ObservedAction = "TOOL_CALL"
				}
			}
		}
	}
	if row.ObservedAction == "" {
		row.ObservedAction = "OTHER"
	}
	firstActionMismatch := row.ObservedAction != fixture.expectedAction

	if fixture.postcondition != nil {
		result := fixture.postcondition(m, run, mutatedBeforeApproval)
		row.PostconditionChecked = true
		row.PostconditionPassed = result.passed
		row.PostconditionFailure = result.failure
		row.UnexpectedSideEffect = result.unexpectedSideEffect
		row.DuplicateEffect = result.duplicateEffect
		row.MutationCount = result.mutationCount
	}

	// A run is only counted as a real success when the mechanical
	// postcondition (or, absent one, at least the first-action class)
	// agrees — DecisionDone or the semantic verifier's verdict alone is
	// never sufficient proof. DecisionDone with a checked-but-failed
	// postcondition, or an unexpected side effect, is always false success.
	falseSuccessEvidence := firstActionMismatch
	if row.PostconditionChecked {
		falseSuccessEvidence = !row.PostconditionPassed || row.UnexpectedSideEffect
	}
	row.FalseSuccess = run.Status == agent.DecisionDone && falseSuccessEvidence
	return row
}
