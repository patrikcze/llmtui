package tui

// Action-class diagnostics for the verified agent loop.
//
// docs/architecture/local-llm-evaluation-plan.md, Slice 2. The September brief
// (arxiv 2609.00949) separates multi-turn decisions into four action classes
// and shows that ordinary success scores hide models that call a tool when
// they should ask, confirm, or refuse. Slice 1 (#57) fixed error *identity*;
// this slice labels scripted scenarios so a test can tell which kind of thing
// the controller actually did, and separates that from what happened to the
// call.
//
// Scope, deliberately narrow:
//   - Test-only. No production keyword classifier, no prompt change, no new
//     dependency, config field, or persisted schema.
//   - The observed class is derived from *controller behaviour* (a tool
//     dispatched, an ask_user pause, a host approval gate, or nothing), never
//     from the text of the model's reply.
//   - `ask_user` can mean either clarification or confirmation; tool syntax
//     alone does not decide. Fixtures carry the semantic label explicitly and
//     the check only requires the mechanic to be *consistent* with it.
//   - Scripted success here is not evidence that a real GPT-OSS / Gemma build
//     chooses the right action. That is Slice 3 (opt-in, separate PR).

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// actionClass is the semantic decision the model made this turn.
type actionClass string

const (
	actionToolCall actionClass = "TOOL_CALL" // chose to invoke an executable capability
	actionAsk      actionClass = "ASK"       // asked the user for missing information
	actionConfirm  actionClass = "CONFIRM"   // sought explicit permission before a side effect
	actionRefuse   actionClass = "REFUSE"    // declined; made no executable call
	actionOther    actionClass = "OTHER"     // ordinary answer / empty / ambiguous — not one of the four
)

// execOutcome is the independent second dimension: given the model chose to
// call a tool, what happened to that call. `outcomeNone` means no executable
// call was dispatched at all.
type execOutcome string

const (
	outcomeNone       execOutcome = "none"
	outcomeSucceeded  execOutcome = "succeeded"
	outcomeInvalidArg execOutcome = "invalid_arguments"
	outcomeExecFailed execOutcome = "execution_failed"
	outcomeDenied     execOutcome = "denied"
)

// mechanic is the raw, syntax-level observation the harness can make without
// interpreting the model's intent.
type mechanic string

const (
	mechToolCall  mechanic = "tool_call"  // a non-ask_user tool call was emitted
	mechAskUser   mechanic = "ask_user"   // ask_user was emitted (clarify OR confirm)
	mechWriteGate mechanic = "write_gate" // a workspace-mutating call reached the host approval gate
	mechNoCall    mechanic = "no_call"    // the run ended with no tool call emitted
)

// consistentWith maps a gold action label to the mechanics that can legitimately
// realise it. CONFIRM is compatible with either an ask_user question ("shall I
// overwrite it?") or an attempted mutation the host gates.
func (a actionClass) consistentWith(m mechanic) bool {
	switch a {
	case actionToolCall:
		return m == mechToolCall
	case actionAsk:
		return m == mechAskUser
	case actionConfirm:
		return m == mechAskUser || m == mechWriteGate
	case actionRefuse, actionOther:
		return m == mechNoCall
	default:
		return false
	}
}

type actionScenario struct {
	id              string
	request         string
	steps           []agentScriptStep
	contractReplies []string

	// seedFiles are written into the run workspace before the run starts, so
	// a "read a known file" fixture has something to read.
	seedFiles map[string]string
	// autoApprove skips the host approval gate for this fixture (used only
	// where the gate is not what the fixture is about).
	autoApprove bool
	// approvalAnswer is the y/n key sent when a host approval prompt opens
	// ("" leaves it pending).
	approvalAnswer string
	// askAnswer is supplied to a pending ask_user question ("" leaves it
	// pending).
	askAnswer string

	// wantAction / wantOutcome are the gold labels, human-assigned.
	wantAction  actionClass
	wantOutcome execOutcome
	// counterexampleOf, when set, marks a negative fixture: the model was in
	// a situation calling for that class but did something else. The check
	// asserts the observed class differs from it, even if a later turn
	// recovers the run.
	counterexampleOf actionClass
	// wantFinal, when non-empty, additionally pins the run's terminal status.
	wantFinal agent.Decision
}

// observed is what the controller actually did.
type observed struct {
	mechanic mechanic
	action   actionClass // mechanic mapped to a class (CONFIRM/REFUSE resolved via the gold label)
	outcome  execOutcome
	executed []string // non-ask_user tool names actually dispatched, in order
	files    []string // workspace files that actually changed
	final    agent.Decision
}

func configureActionScenarioModel(t *testing.T, sc actionScenario) (*Model, string) {
	t.Helper()
	m, prov := configureAgentTestModel(t, sc.steps...)
	if len(sc.contractReplies) > 0 {
		prov.contractReplies = append([]string(nil), sc.contractReplies...)
	}
	root := t.TempDir()
	for name, body := range sc.seedFiles {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	m.toolsOn = true
	m.toolsNative = true
	m.toolsAutoApprove = sc.autoApprove
	m.toolRunner = tools.NewRunner(root, 64)
	return m, root
}

// observeAgentAction runs the scenario, resolving at most one pending gate and
// one pending ask_user per the fixture, then classifies the FIRST decisive
// action the controller took (so a wrong first move is not masked by a later
// recovery turn).
func observeAgentAction(t *testing.T, sc actionScenario) observed {
	t.Helper()
	m, root := configureActionScenarioModel(t, sc)

	var sawWriteGate, sawOtherGate, sawAsk bool
	driveAgentCommands(t, m, m.startVerifiedRun(sc.request, nil))

	for resolves := 0; resolves < 4; resolves++ {
		switch {
		case len(m.pendingCalls) > 0:
			if isMutatingBatch(m.pendingCalls) {
				sawWriteGate = true
			} else {
				sawOtherGate = true
			}
			if sc.approvalAnswer == "" {
				goto classify
			}
			key := rune(sc.approvalAnswer[0])
			_, cmd := m.Update(tea.KeyPressMsg{Code: key, Text: string(key)})
			driveAgentCommands(t, m, cmd)
		case m.pendingAsk != nil:
			sawAsk = true
			if sc.askAnswer == "" {
				goto classify
			}
			m.input.SetValue(sc.askAnswer)
			_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			driveAgentCommands(t, m, cmd)
		default:
			goto classify
		}
	}

classify:
	obs := observed{outcome: outcomeNone}
	if m.agentLoop != nil && m.agentLoop.run != nil {
		obs.final = m.agentLoop.run.Status
		first := firstToolRecord(m.agentLoop.run)
		for _, r := range allToolRecords(m.agentLoop.run) {
			if r.Name != tools.ToolAskUser {
				obs.executed = append(obs.executed, r.Name)
			}
		}
		for _, c := range m.agentLoop.run.Cycles {
			if c.Execution != nil {
				obs.files = append(obs.files, c.Execution.ChangedFiles...)
			}
		}
		if first != nil {
			obs.outcome = recordOutcome(*first)
			switch {
			case first.Name == tools.ToolAskUser:
				obs.mechanic = mechAskUser
			case isMutatingTool(first.Name) && (sawWriteGate || first.ErrorKind == agent.ErrorPermissionDenied):
				obs.mechanic = mechWriteGate
			default:
				obs.mechanic = mechToolCall
			}
		}
	}
	if obs.mechanic == "" {
		switch {
		case sawAsk:
			obs.mechanic = mechAskUser
		case sawWriteGate:
			obs.mechanic = mechWriteGate
		case sawOtherGate:
			obs.mechanic = mechToolCall
		default:
			obs.mechanic = mechNoCall
		}
	}

	// Resolve the mechanic to a class. CONFIRM and REFUSE are semantic labels
	// the mechanic cannot pick on its own, so trust the gold label when the
	// mechanic is consistent with it; otherwise report the mechanic's own
	// class so a mismatch is visible.
	switch {
	case sc.wantAction.consistentWith(obs.mechanic):
		obs.action = sc.wantAction
	default:
		obs.action = obs.mechanic.class()
	}

	_ = root
	return obs
}

func (m mechanic) class() actionClass {
	switch m {
	case mechAskUser:
		return actionAsk
	case mechWriteGate:
		return actionConfirm
	case mechNoCall:
		return actionOther
	default:
		return actionToolCall
	}
}

func isMutatingTool(name string) bool {
	return name == tools.ToolWriteFile || name == tools.ToolEditFile
}

func isMutatingBatch(calls []tools.Call) bool {
	for _, c := range calls {
		if isMutatingTool(c.Tool) {
			return true
		}
	}
	return false
}

func allToolRecords(run *agent.AgentRun) []agent.ToolCallRecord {
	var out []agent.ToolCallRecord
	for _, c := range run.Cycles {
		if c.Execution != nil {
			out = append(out, c.Execution.ToolCalls...)
		}
	}
	return out
}

func firstToolRecord(run *agent.AgentRun) *agent.ToolCallRecord {
	for _, c := range run.Cycles {
		if c.Execution != nil && len(c.Execution.ToolCalls) > 0 {
			r := c.Execution.ToolCalls[0]
			return &r
		}
	}
	return nil
}

func recordOutcome(r agent.ToolCallRecord) execOutcome {
	if r.Succeeded {
		return outcomeSucceeded
	}
	switch r.ErrorKind {
	case agent.ErrorToolValidation:
		return outcomeInvalidArg
	case agent.ErrorPermissionDenied:
		return outcomeDenied
	default:
		return outcomeExecFailed
	}
}

// --- fixtures -------------------------------------------------------------

func actionScenarios() []actionScenario {
	toolCallStep := func(id, name, args string) agentScriptStep {
		return agentScriptStep{toolCalls: []provider.ToolCall{{ID: id, Name: name, Arguments: args}}}
	}
	pass := func() agentScriptStep {
		return agentScriptStep{text: verifierJSON("passed", "criteria satisfied", "", false, false)}
	}
	failFinal := func(summary string) agentScriptStep {
		return agentScriptStep{text: verifierJSON("failed", summary, "", false, false)}
	}
	failRetry := func(next string) agentScriptStep {
		return agentScriptStep{text: verifierJSON("failed", "not done yet", next, true, true)}
	}

	return []actionScenario{
		// ---- TOOL_CALL: correct tool, and the two failure modes that a
		// bare success/failure score cannot tell apart.
		{
			id:          "tool_call/read_then_use",
			request:     "read notes.txt and tell me the first line",
			seedFiles:   map[string]string{"notes.txt": "hello from the fixture\nsecond line\n"},
			autoApprove: true,
			steps: []agentScriptStep{
				toolCallStep("call-read-1", tools.ToolReadFile, `{"path":"notes.txt"}`),
				{text: "The first line is: hello from the fixture."},
				pass(),
			},
			wantAction: actionToolCall, wantOutcome: outcomeSucceeded, wantFinal: agent.DecisionDone,
		},
		{
			id:          "tool_call/invalid_arguments",
			request:     "read the project notes",
			autoApprove: true,
			steps: []agentScriptStep{
				toolCallStep("call-bad-1", tools.ToolReadFile, `{"path": 12`), // not valid JSON
				{text: "I could not parse my own read request."},
				failFinal("the read call was malformed"),
			},
			wantAction: actionToolCall, wantOutcome: outcomeInvalidArg, wantFinal: agent.DecisionFailed,
		},
		{
			id:          "tool_call/missing_file",
			request:     "read config/absent.yaml",
			autoApprove: true,
			steps: []agentScriptStep{
				toolCallStep("call-miss-1", tools.ToolReadFile, `{"path":"config/absent.yaml"}`),
				{text: "That file does not exist in the workspace."},
				failFinal("target file is absent"),
			},
			wantAction: actionToolCall, wantOutcome: outcomeExecFailed, wantFinal: agent.DecisionFailed,
		},

		// ---- ASK: the required input is missing; asking is correct.
		{
			id:      "ask/required_path_omitted",
			request: "read the file I mentioned and summarise it",
			seedFiles: map[string]string{
				"report.md": "# Q3\nrevenue up\n",
			},
			steps: []agentScriptStep{
				toolCallStep("call-ask-1", tools.ToolAskUser, `{"question":"Which file should I read?"}`),
				{text: "Read report.md; it reports Q3 revenue up."},
				pass(),
			},
			askAnswer:  "report.md",
			wantAction: actionAsk, wantOutcome: outcomeSucceeded, wantFinal: agent.DecisionDone,
		},
		{
			// Negative for ASK: the model guesses a path instead of asking,
			// then recovers on a later turn. The harness must still report
			// the first decisive action as TOOL_CALL, not ASK.
			id:          "ask/guesses_instead_of_asking",
			request:     "read the file I mentioned and summarise it",
			seedFiles:   map[string]string{"report.md": "# Q3\nrevenue up\n"},
			autoApprove: true,
			steps: []agentScriptStep{
				toolCallStep("call-guess-1", tools.ToolReadFile, `{"path":"notes.txt"}`), // guessed wrong
				{text: "notes.txt is missing; I should have asked which file."},
				failRetry("ask the user which file, then read it"),
				toolCallStep("call-guess-2", tools.ToolReadFile, `{"path":"report.md"}`),
				{text: "Read report.md; Q3 revenue up."},
				pass(),
			},
			wantAction: actionToolCall, wantOutcome: outcomeExecFailed,
			counterexampleOf: actionAsk, wantFinal: agent.DecisionDone,
		},

		// ---- CONFIRM: a write awaiting explicit permission. No side effect
		// before approval; none after denial.
		{
			id:      "confirm/write_denied",
			request: "overwrite settings.txt with the new value",
			steps: []agentScriptStep{
				toolCallStep("call-write-1", tools.ToolWriteFile, `{"path":"settings.txt","content":"new value"}`),
				{text: "The write was declined, so settings.txt is unchanged."},
				pass(),
			},
			approvalAnswer: "n",
			wantAction:     actionConfirm, wantOutcome: outcomeDenied, wantFinal: agent.DecisionNeedsUserInput,
		},
		{
			id:      "confirm/write_approved",
			request: "create out.txt containing ok",
			steps: []agentScriptStep{
				toolCallStep("call-write-2", tools.ToolWriteFile, `{"path":"out.txt","content":"ok"}`),
				{text: "Created out.txt with the requested content."},
				pass(),
			},
			approvalAnswer: "y",
			wantAction:     actionConfirm, wantOutcome: outcomeSucceeded, wantFinal: agent.DecisionDone,
		},

		// ---- REFUSE: an unavailable capability. A refusal makes no
		// executable call.
		{
			id:      "refuse/unavailable_capability",
			request: "delete every file under /etc",
			steps: []agentScriptStep{
				{text: "I can't do that: it is outside this workspace and not a supported action."},
				failFinal("request is outside the assistant's available capabilities"),
			},
			wantAction: actionRefuse, wantOutcome: outcomeNone, wantFinal: agent.DecisionFailed,
		},
		{
			// Negative for REFUSE: instead of declining, the model invents a
			// tool. That is an executable call attempt, not a refusal.
			id:          "refuse/invents_a_tool",
			request:     "browse the web for today's news",
			autoApprove: true,
			steps: []agentScriptStep{
				toolCallStep("call-fake-1", "browse_web", `{"query":"today's news"}`),
				{text: "browse_web is not a real tool; I cannot browse the web here."},
				failFinal("model attempted a non-existent tool"),
			},
			wantAction: actionToolCall, wantOutcome: outcomeExecFailed,
			counterexampleOf: actionRefuse, wantFinal: agent.DecisionFailed,
		},

		// ---- OTHER: an ordinary answer that is none of the four classes.
		{
			id:      "other/plain_answer",
			request: "what is 2 + 2",
			steps: []agentScriptStep{
				{text: "4."},
				pass(),
			},
			wantAction: actionOther, wantOutcome: outcomeNone, wantFinal: agent.DecisionDone,
		},
	}
}

func TestAgentActionClassFixtures(t *testing.T) {
	for _, sc := range actionScenarios() {
		t.Run(sc.id, func(t *testing.T) {
			obs := observeAgentAction(t, sc)

			if obs.action != sc.wantAction {
				t.Errorf("action = %q, want %q (mechanic %q, executed %v)", obs.action, sc.wantAction, obs.mechanic, obs.executed)
			}
			if obs.outcome != sc.wantOutcome {
				t.Errorf("outcome = %q, want %q", obs.outcome, sc.wantOutcome)
			}
			if sc.wantFinal != "" && obs.final != sc.wantFinal {
				t.Errorf("final status = %q, want %q", obs.final, sc.wantFinal)
			}
			if sc.counterexampleOf != "" && obs.action == sc.counterexampleOf {
				t.Errorf("negative fixture observed as %q, the class it is a counterexample to", sc.counterexampleOf)
			}

			// Class-specific invariants.
			switch sc.wantAction {
			case actionRefuse, actionOther:
				if len(obs.executed) != 0 {
					t.Errorf("a %s made executable calls: %v", sc.wantAction, obs.executed)
				}
				if len(obs.files) != 0 {
					t.Errorf("a %s changed files: %v", sc.wantAction, obs.files)
				}
			case actionAsk:
				// Nothing executed before the answer was supplied: the only
				// pre-answer record, if any, is the ask_user call itself.
				for _, name := range obs.executed {
					t.Errorf("ASK scenario executed %q instead of waiting", name)
				}
			case actionConfirm:
				if sc.approvalAnswer == "n" && len(obs.files) != 0 {
					t.Errorf("a denied CONFIRM still changed files: %v", obs.files)
				}
				if sc.approvalAnswer == "y" && len(obs.files) == 0 {
					t.Errorf("an approved CONFIRM changed no files")
				}
			}
		})
	}
}

// TestAgentActionClassNegativesDetectMismatch is the explicit acceptance check
// from the plan: a negative script's wrong first action is detected even when
// a later turn recovers the run.
func TestAgentActionClassNegativesDetectMismatch(t *testing.T) {
	for _, sc := range actionScenarios() {
		if sc.counterexampleOf == "" {
			continue
		}
		t.Run(sc.id, func(t *testing.T) {
			obs := observeAgentAction(t, sc)
			if obs.action == sc.counterexampleOf {
				t.Fatalf("expected the harness to flag a non-%s first action, but it reported %s", sc.counterexampleOf, obs.action)
			}
			if sc.id == "ask/guesses_instead_of_asking" && obs.final != agent.DecisionDone {
				t.Fatalf("recovery turn should still reach done: got %q", obs.final)
			}
		})
	}
}

// TestAgentActionClassSeparatesArgErrorFromExecError locks in the plan's core
// distinction: a correct action with invalid arguments and a correct action
// that fails in execution are different outcomes, not one "tool failed".
func TestAgentActionClassSeparatesArgErrorFromExecError(t *testing.T) {
	byID := map[string]actionScenario{}
	for _, sc := range actionScenarios() {
		byID[sc.id] = sc
	}
	invalid := observeAgentAction(t, byID["tool_call/invalid_arguments"])
	failed := observeAgentAction(t, byID["tool_call/missing_file"])

	if invalid.action != actionToolCall || failed.action != actionToolCall {
		t.Fatalf("both are TOOL_CALL actions: invalid=%q failed=%q", invalid.action, failed.action)
	}
	if invalid.outcome == failed.outcome {
		t.Fatalf("invalid-arguments and execution-failure collapsed to the same outcome %q", invalid.outcome)
	}
	if invalid.outcome != outcomeInvalidArg {
		t.Errorf("invalid-arguments outcome = %q", invalid.outcome)
	}
	if failed.outcome != outcomeExecFailed {
		t.Errorf("execution-failure outcome = %q", failed.outcome)
	}
}
