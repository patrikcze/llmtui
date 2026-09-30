package tui

// Diagnostic fixture for the 2026-09-30 agent latency audit
// (.claude/tasks/plans/llmtui-agent-improvement-plan.md). It does not assert
// performance; it records, per scripted scenario, how many model requests
// the default agent flow issues, of which kind, and how much of each
// executor request's serialized prefix survives from the previous executor
// request. Run with:
//
//	LLMTUI_AGENT_AUDIT_TRACE=1 go test ./internal/tui -run TestAgentAuditTrace -v -count=1
//
// Skipped otherwise so the ordinary suite is unaffected. Byte counts are
// exact for the serialized request; token figures are not computed here
// (no tokenizer) — real prefill numbers come from the live runs.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

type auditRequestKind string

const (
	auditContract auditRequestKind = "contract"
	auditExecutor auditRequestKind = "executor"
	auditVerifier auditRequestKind = "verifier"
)

type auditRecord struct {
	kind       auditRequestKind
	serialized string
	system     string
	messages   int
	tools      int
	toolBytes  int
	request    provider.ChatRequest
}

// auditTraceProvider answers contract and verifier requests itself and
// feeds executor requests from a script, recording every request.
type auditTraceProvider struct {
	mu        sync.Mutex
	contract  string
	criteria  int
	executor  []agentScriptStep
	records   []auditRecord
	exhausted int
}

func (p *auditTraceProvider) Name() string                      { return "audit-trace" }
func (p *auditTraceProvider) HealthCheck(context.Context) error { return nil }
func (p *auditTraceProvider) ListModels(context.Context) ([]provider.ModelInfo, error) {
	return []provider.ModelInfo{{ID: "test-model"}}, nil
}

func serializeAuditRequest(req provider.ChatRequest) (string, string) {
	var b strings.Builder
	system := ""
	for i, msg := range req.Messages {
		if i == 0 && msg.Role == provider.RoleSystem {
			system = msg.Content
			b.WriteString("<system>" + msg.Content + "</system>")
			// Chat templates render tool specs with the system block.
			if len(req.Tools) > 0 {
				specs, _ := json.Marshal(req.Tools)
				b.Write(specs)
			}
			continue
		}
		fmt.Fprintf(&b, "<%s id=%q>%s", msg.Role, msg.ToolCallID, msg.Content)
		for _, call := range msg.ToolCalls {
			fmt.Fprintf(&b, "<call %s %s %s>", call.ID, call.Name, call.Arguments)
		}
		fmt.Fprintf(&b, "</%s>", msg.Role)
	}
	return b.String(), system
}

func (p *auditTraceProvider) Chat(_ context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	serialized, system := serializeAuditRequest(req)
	specs, _ := json.Marshal(req.Tools)
	kind := auditExecutor
	reply := ""
	switch {
	case strings.Contains(system, "You establish a task contract"):
		kind, reply = auditContract, p.contract
	case strings.Contains(system, "You are an independent verifier"):
		ids := make([]string, 0, p.criteria)
		for i := 1; i <= p.criteria; i++ {
			ids = append(ids, fmt.Sprintf("c%d", i))
		}
		kind, reply = auditVerifier, verifierJSONSatisfying("observed evidence satisfies the criteria", ids...)
	}
	p.mu.Lock()
	p.records = append(p.records, auditRecord{kind: kind, serialized: serialized, system: system, messages: len(req.Messages), tools: len(req.Tools), toolBytes: len(specs), request: req})
	var step agentScriptStep
	if kind == auditExecutor {
		if len(p.executor) == 0 {
			p.exhausted++
			p.mu.Unlock()
			return nil, errors.New("audit script exhausted")
		}
		step = p.executor[0]
		p.executor = p.executor[1:]
	}
	p.mu.Unlock()
	events := make(chan provider.ChatEvent, 3)
	if kind != auditExecutor {
		events <- provider.ChatEvent{Type: provider.EventDelta, Delta: reply}
		events <- provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
		close(events)
		return events, nil
	}
	if step.text != "" {
		events <- provider.ChatEvent{Type: provider.EventDelta, Delta: step.text}
	}
	if step.streamErr != nil {
		events <- provider.ChatEvent{Type: provider.EventError, Err: step.streamErr}
		close(events)
		return events, nil
	}
	events <- provider.ChatEvent{Type: provider.EventDone, ToolCalls: step.toolCalls, Usage: &provider.Usage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}}
	close(events)
	return events, nil
}

func auditCommonPrefix(a, b string) int {
	n := min(len(a), len(b))
	for i := range n {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

type auditScenario struct {
	name     string
	request  string
	criteria []string
	setup    func(t *testing.T, root string)
	executor []agentScriptStep
	// followUp, when set, starts a second verified run in the same session
	// after the first finishes; followUpNeedle is checked for in that run's
	// first executor request to show whether prior evidence is still visible.
	followUp       string
	followUpNeedle string
}

func auditReadCall(id, args string) agentScriptStep {
	return agentScriptStep{toolCalls: []provider.ToolCall{{ID: id, Name: tools.ToolReadFile, Arguments: args}}}
}

func writeAuditLines(t *testing.T, path string, n int) {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %04d: the quick brown fox jumps over the lazy dog\n", i)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAgentAuditTrace(t *testing.T) {
	if os.Getenv("LLMTUI_AGENT_AUDIT_TRACE") == "" {
		t.Skip("diagnostic trace; set LLMTUI_AGENT_AUDIT_TRACE=1")
	}
	scenarios := []auditScenario{
		{
			name: "S8 trivial answer", request: "Say hello in one word.",
			criteria: []string{"Reply with a one-word greeting"},
			executor: []agentScriptStep{{text: "Hello."}},
		},
		{
			name: "S1 last line of 1500-line file", request: "What is the last line of big.log?",
			criteria: []string{"Report the last line of big.log"},
			setup:    func(t *testing.T, root string) { writeAuditLines(t, root+"/big.log", 1500) },
			executor: []agentScriptStep{
				auditReadCall("r1", `{"path":"big.log","offset":1500,"limit":1}`),
				{text: "The last line is: line 1500: the quick brown fox jumps over the lazy dog"},
			},
		},
		{
			name: "S2 malformed ranged read recovery", request: "What is the last line of big.log?",
			criteria: []string{"Report the last line of big.log"},
			setup:    func(t *testing.T, root string) { writeAuditLines(t, root+"/big.log", 1500) },
			executor: []agentScriptStep{
				auditReadCall("r1", `{"path":"big.log","offset":"end","limit":1}`),
				auditReadCall("r2", `{"path":"big.log","offset":1600,"limit":1}`),
				auditReadCall("r3", `{"path":"big.log","offset":1500,"limit":1}`),
				{text: "The last line is: line 1500: the quick brown fox jumps over the lazy dog"},
			},
		},
		{
			name: "S4 three independent files", request: "Summarize a.txt, b.txt and c.txt.",
			criteria: []string{"Summarize a.txt", "Summarize b.txt", "Summarize c.txt"},
			setup: func(t *testing.T, root string) {
				for _, name := range []string{"a", "b", "c"} {
					writeAuditLines(t, root+"/"+name+".txt", 40)
				}
			},
			executor: []agentScriptStep{
				{toolCalls: []provider.ToolCall{
					{ID: "a", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`},
					{ID: "b", Name: tools.ToolReadFile, Arguments: `{"path":"b.txt"}`},
					{ID: "c", Name: tools.ToolReadFile, Arguments: `{"path":"c.txt"}`},
				}},
				{text: "a.txt, b.txt and c.txt each contain 120 numbered pangram lines."},
			},
		},
		{
			name: "S4b three 120-line files on the 8k fallback window", request: "Summarize a.txt, b.txt and c.txt.",
			criteria: []string{"Summarize a.txt", "Summarize b.txt", "Summarize c.txt"},
			setup: func(t *testing.T, root string) {
				for _, name := range []string{"a", "b", "c"} {
					writeAuditLines(t, root+"/"+name+".txt", 120)
				}
			},
			executor: []agentScriptStep{
				{toolCalls: []provider.ToolCall{
					{ID: "a", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`},
					{ID: "b", Name: tools.ToolReadFile, Arguments: `{"path":"b.txt"}`},
					{ID: "c", Name: tools.ToolReadFile, Arguments: `{"path":"c.txt"}`},
				}},
				{text: "a.txt, b.txt and c.txt each contain 120 numbered pangram lines."},
			},
		},
		{
			name: "S3 follow-up answered from prior evidence", request: "What is the last line of big.log?",
			criteria: []string{"Report the last line of big.log"},
			setup:    func(t *testing.T, root string) { writeAuditLines(t, root+"/big.log", 1500) },
			executor: []agentScriptStep{
				auditReadCall("r1", `{"path":"big.log","offset":1500,"limit":1}`),
				{text: "The last line is: line 1500: the quick brown fox jumps over the lazy dog"},
				{text: "It is line 1500."},
			},
			followUp:       "Which line number was that?",
			followUpNeedle: "line 1500: the quick brown fox",
		},
		{
			name: "S5 edit plus verification", request: "Change greeting.txt to say goodbye and confirm.",
			criteria: []string{"greeting.txt says goodbye", "The change is confirmed by reading the file"},
			setup: func(t *testing.T, root string) {
				if err := os.WriteFile(root+"/greeting.txt", []byte("hello\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			executor: []agentScriptStep{
				auditReadCall("r1", `{"path":"greeting.txt"}`),
				{toolCalls: []provider.ToolCall{{ID: "e1", Name: tools.ToolEditFile, Arguments: `{"path":"greeting.txt","old_text":"hello","new_text":"goodbye"}`}}},
				auditReadCall("r2", `{"path":"greeting.txt"}`),
				{text: "greeting.txt now reads goodbye (confirmed by reading it back)."},
			},
		},
		{
			name: "S6 interrupted stream after tool call", request: "What is the last line of big.log?",
			criteria: []string{"Report the last line of big.log"},
			setup:    func(t *testing.T, root string) { writeAuditLines(t, root+"/big.log", 1500) },
			executor: []agentScriptStep{
				auditReadCall("r1", `{"path":"big.log","offset":1500,"limit":1}`),
				{text: "The last line is", streamErr: fmt.Errorf("mid-stream: %w", provider.ErrStreamInterrupted)},
				{text: "The last line is: line 1500: the quick brown fox jumps over the lazy dog"},
			},
		},
	}
	for _, variant := range []struct{ embedded, plain bool }{{false, false}, {true, false}, {false, true}, {true, true}} {
		embedded := variant.embedded
		for _, sc := range scenarios {
			label := sc.name + map[bool]string{false: " [http", true: " [embedded"}[embedded] + map[bool]string{false: "]", true: " plain-chat]"}[variant.plain]
			t.Run(label, func(t *testing.T) {
				m := newTestModel(t)
				criteria, _ := json.Marshal(sc.criteria)
				prov := &auditTraceProvider{
					contract: `{"criteria":` + string(criteria) + `,"needs_user_input":false,"question":"","user_options":[]}`,
					criteria: len(sc.criteria),
					executor: append([]agentScriptStep(nil), sc.executor...),
				}
				m.prov = prov
				m.model = "test-model"
				m.agentOn = true
				m.cfg.Agent.Verifier.Enabled = true
				m.cfg.Agent.Verifier.Mode = "" // production default resolves to adaptive
				m.cfg.Agent.Verifier.Timeout = "1s"
				m.cfg.Agent.Verifier.MaxTokens = 256
				m.cfg.Agent.Verifier.MaxAttempts = 2
				m.cfg.Agent.Yield.Enabled = true
				m.cfg.Agent.Yield.MaxEpisodeRequests = 64
				m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 2
				m.cfg.Tools.NoProgress.Enabled = true
				m.cfg.Agent.Persist = false
				m.agentLoop.store = nil
				if embedded {
					if m.cfg.Providers == nil {
						m.cfg.Providers = map[string]config.ProviderConfig{}
					}
					m.cfg.Provider = "emb"
					m.cfg.Providers["emb"] = config.ProviderConfig{Type: "embedded"}
				}
				root := t.TempDir()
				if sc.setup != nil {
					sc.setup(t, root)
				}
				m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
				m.toolRunner = tools.NewRunner(root, 64)
				if variant.plain {
					m.agentOn = false
					driveAgentCommands(t, m, m.dispatch(sc.request, nil))
				} else {
					driveAgentCommands(t, m, m.startVerifiedRun(sc.request, nil))
				}
				firstRunRequests := len(prov.records)
				followUpNote := ""
				if sc.followUp != "" && !variant.plain {
					firstStatus := m.agentLoop.run.Status
					driveAgentCommands(t, m, m.startVerifiedRun(sc.followUp, nil))
					visible := false
					for _, rec := range prov.records[firstRunRequests:] {
						if rec.kind == auditExecutor {
							visible = strings.Contains(rec.serialized, sc.followUpNeedle)
							break
						}
					}
					followUpNote = fmt.Sprintf(" first_run=%s first_run_requests=%d follow_up_requests=%d prior_evidence_visible_to_follow_up=%t",
						firstStatus, firstRunRequests, len(prov.records)-firstRunRequests, visible)
				}

				counts := map[auditRequestKind]int{}
				var prevExec *auditRecord
				var lines []string
				for i := range prov.records {
					rec := &prov.records[i]
					counts[rec.kind]++
					detail := ""
					if rec.kind == auditExecutor {
						if prevExec != nil {
							lcp := auditCommonPrefix(prevExec.serialized, rec.serialized)
							// Copies of one observed file line: >1 means the same
							// evidence is sent both as a tool result and again in
							// the runtime directive.
							copies := max(strings.Count(rec.serialized, "line 0040: the quick"), strings.Count(rec.serialized, "line 1500: the quick"))
							detail = fmt.Sprintf("reused_prefix=%d/%d bytes (%.0f%%) new=%d system_changed=%t copies_of_one_observed_line=%d",
								lcp, len(rec.serialized), 100*float64(lcp)/float64(len(rec.serialized)),
								len(rec.serialized)-lcp, prevExec.system != rec.system, copies)
						} else {
							detail = fmt.Sprintf("first executor request: %d bytes (system=%d, %d tool specs=%d)", len(rec.serialized), len(rec.system), rec.tools, rec.toolBytes)
						}
						prevExec = rec
					} else {
						detail = fmt.Sprintf("%d bytes (fresh context; displaces a single-sequence KV cache)", len(rec.serialized))
					}
					lines = append(lines, fmt.Sprintf("  #%d %-8s msgs=%-2d %s", i+1, rec.kind, rec.messages, detail))
				}
				if dir := os.Getenv("LLMTUI_AGENT_AUDIT_DUMP"); dir != "" {
					// Replay input for the embedded prefill measurement
					// (llamart TestAgentAuditPrefillReplay). Scripted fixture
					// content only; no user data.
					type dumped struct {
						Kind     auditRequestKind    `json:"kind"`
						Messages []provider.Message  `json:"messages"`
						Tools    []provider.ToolSpec `json:"tools,omitempty"`
					}
					out := make([]dumped, 0, len(prov.records))
					for _, rec := range prov.records {
						out = append(out, dumped{Kind: rec.kind, Messages: rec.request.Messages, Tools: rec.request.Tools})
					}
					data, err := json.MarshalIndent(out, "", " ")
					if err != nil {
						t.Fatal(err)
					}
					name := strings.NewReplacer(" ", "_", "[", "", "]", "", "/", "_").Replace(label) + ".json"
					if err := os.WriteFile(dir+"/"+name, data, 0o600); err != nil {
						t.Fatal(err)
					}
				}
				status, stop, cycles := "plain-chat", m.errText, 0
				if run := m.agentLoop.run; run != nil && !variant.plain {
					status, stop, cycles = string(run.Status), run.StopReason, run.Cycle
				}
				t.Logf("status=%s stop=%q cycles=%d requests=%d (contract=%d executor=%d verifier=%d) unconsumed_script=%d exhausted=%d%s\n%s",
					status, stop, cycles, len(prov.records), counts[auditContract], counts[auditExecutor], counts[auditVerifier],
					len(prov.executor), prov.exhausted, followUpNote, strings.Join(lines, "\n"))
			})
		}
	}
}

// TestAgentAuditCancelAfterCompletedMutation is scenario S7: the user
// cancels a tool batch after its mutation already ran but before the result
// reached the model, then asks the agent to continue. It records what the
// continuation request can see of the completed mutation.
func TestAgentAuditCancelAfterCompletedMutation(t *testing.T) {
	if os.Getenv("LLMTUI_AGENT_AUDIT_TRACE") == "" {
		t.Skip("diagnostic trace; set LLMTUI_AGENT_AUDIT_TRACE=1")
	}
	m := newTestModel(t)
	prov := &auditTraceProvider{
		contract: `{"criteria":["greeting.txt says goodbye"],"needs_user_input":false,"question":"","user_options":[]}`,
		criteria: 1,
		executor: []agentScriptStep{
			{toolCalls: []provider.ToolCall{{ID: "e1", Name: tools.ToolEditFile, Arguments: `{"path":"greeting.txt","old_text":"hello","new_text":"goodbye"}`}}},
			{text: "Continuing."},
		},
	}
	m.prov, m.model, m.agentOn = prov, "test-model", true
	m.cfg.Agent.Verifier.Enabled = true
	m.cfg.Agent.Verifier.Timeout, m.cfg.Agent.Verifier.MaxTokens, m.cfg.Agent.Verifier.MaxAttempts = "1s", 256, 2
	m.cfg.Agent.Yield.Enabled = true
	m.cfg.Agent.Yield.MaxEpisodeRequests, m.cfg.Agent.Yield.MaxNudgesWithoutProgress = 64, 2
	m.cfg.Agent.Persist = false
	m.agentLoop.store = nil
	root := t.TempDir()
	if err := os.WriteFile(root+"/greeting.txt", []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)

	queue := []tea.Cmd{m.startVerifiedRun("Change greeting.txt to say goodbye.", nil)}
	cancelled := false
	for steps := 0; len(queue) > 0 && steps < 200; steps++ {
		cmd := queue[0]
		queue = queue[1:]
		if cmd == nil {
			continue
		}
		batchRunning := m.mcpBatchCancel != nil
		msg := cmd()
		if b, ok := msg.(tea.BatchMsg); ok {
			queue = append(queue, b...)
			continue
		}
		if _, isResults := msg.(mcpToolResultsMsg); isResults && batchRunning && !cancelled {
			// The batch finished its side effect; the user's cancel lands
			// before the result is delivered.
			m.handleCtrlC()
			cancelled = true
		}
		_, next := m.Update(msg)
		if next != nil {
			queue = append(queue, next)
		}
	}
	onDisk, _ := os.ReadFile(root + "/greeting.txt")
	firstStatus, firstStop := m.agentLoop.run.Status, m.agentLoop.run.StopReason
	sessionEditResult := false
	for _, msg := range m.session.Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "e1" {
			sessionEditResult = true
		}
	}
	partialChanged, partialExecuted := 0, 0
	if cycle := m.agentLoop.run.LatestCycle(); cycle != nil && cycle.Execution != nil && cycle.Execution.Partial {
		partialChanged = len(cycle.Execution.ChangedFiles)
		partialExecuted = agent.ExecutedToolCalls(cycle.Execution.ToolCalls)
	}
	t.Logf("first run: session_edit_result=%t run_partial_changed_files=%d run_partial_executed_calls=%d run_tool_calls=%d",
		sessionEditResult, partialChanged, partialExecuted, m.agentLoop.run.ToolCalls)
	before := len(prov.records)
	driveAgentCommands(t, m, m.startVerifiedRun("Continue the previous task.", nil))
	var cont *auditRecord
	for i := before; i < len(prov.records); i++ {
		if prov.records[i].kind == auditExecutor {
			cont = &prov.records[i]
			break
		}
	}
	if cont == nil {
		t.Fatalf("no continuation executor request; records=%d", len(prov.records))
	}
	unanswered := 0
	answered := map[string]bool{}
	for _, msg := range cont.request.Messages {
		if msg.Role == provider.RoleTool {
			answered[msg.ToolCallID] = true
		}
	}
	for _, msg := range cont.request.Messages {
		for _, call := range msg.ToolCalls {
			if !answered[call.ID] {
				unanswered++
			}
		}
	}
	t.Logf("cancelled=%t first_run=%s (%q) file_on_disk=%q continuation: edit_call_visible=%t edit_result_visible=%t unanswered_tool_calls=%d mentions_goodbye=%t",
		cancelled, firstStatus, firstStop, strings.TrimSpace(string(onDisk)),
		strings.Contains(cont.serialized, `"new_text":"goodbye"`), answered["e1"], unanswered,
		strings.Contains(cont.serialized, "goodbye"))
}
