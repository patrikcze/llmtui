package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/entity"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// executorModel returns a model with an active agent run in its executor
// stage and an empty observation cache.
func executorModel(t *testing.T) *Model {
	t.Helper()
	m, _ := configureAgentTestModel(t)
	run, err := agent.NewRun("dedupe-run", "read a.txt", agent.DefaultLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	run.Stage, run.Objective = agent.StageExecutor, "read a.txt"
	m.agentLoop.run = run
	m.agentLoop.observations = agent.NewObservationCache()
	return m
}

func toolResultMessage(id, content string) provider.Message {
	return provider.Message{Role: provider.RoleTool, ToolCallID: id, Content: content}
}

// TestRetainedObservationCitedOnlyWhileVerbatimInHistory is plan step 3,
// change 1: an observation whose excerpt is in the request history is cited
// in one line; without that result (compacted or projected away) the
// excerpt comes back. Another read of the same file does not count.
func TestRetainedObservationCitedOnlyWhileVerbatimInHistory(t *testing.T) {
	m := executorModel(t)
	m.agentLoop.observations.Put(tools.ToolReadFile, "a.txt", 1, "line 1500: the quick brown fox", true)
	const citation = "read_file(a.txt) [cycle 1]: in the conversation above"
	const excerpt = "line 1500: the quick brown fox"

	for _, tc := range []struct {
		name    string
		history []provider.Message
		cited   bool
	}{
		{"result in history", []provider.Message{toolResultMessage("r1", "…\n"+excerpt+"\n")}, true},
		{"no history (compacted away)", nil, false},
		{"different read of the same file", []provider.Message{toolResultMessage("r2", "line 0001: another page")}, false},
		{"excerpt only in an assistant message", []provider.Message{{Role: provider.RoleAssistant, Content: excerpt}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directive := m.agentDirective(tc.history)
			if got := strings.Contains(directive, citation); got != tc.cited {
				t.Fatalf("cited=%t, want %t:\n%s", got, tc.cited, directive)
			}
			if got := strings.Contains(directive, excerpt); got == tc.cited {
				t.Fatalf("excerpt present=%t, want %t", got, !tc.cited)
			}
		})
	}
}

// TestEntityPreviewOmittedOnlyWhileProducerInHistory is change 2: an entity
// whose producing tool result is in history keeps only its header; others
// keep their preview; get_entity_details still returns the full record.
func TestEntityPreviewOmittedOnlyWhileProducerInHistory(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	results := m.registerResultEntities([]tools.Result{
		{Call: tools.Call{ID: "r1", Tool: tools.ToolReadFile, Path: "a.txt"}, Entities: []entity.Candidate{{
			Kind: entity.KindFile, Label: "a.txt", Trust: entity.TrustWorkspaceUntrusted, Scope: entity.ScopeSession,
			Preview: "alpha preview", Payload: "alpha full payload",
		}}},
		{Call: tools.Call{ID: "r2", Tool: tools.ToolReadFile, Path: "b.txt"}, Entities: []entity.Candidate{{
			Kind: entity.KindFile, Label: "b.txt", Trust: entity.TrustWorkspaceUntrusted, Scope: entity.ScopeSession,
			Preview: "beta preview", Payload: "beta full payload",
		}}},
	})
	history := tools.NativeResults(results[:1]) // only a.txt's result is in history
	records := m.entityPromptRecords(history)
	if len(records) != 2 {
		t.Fatalf("records = %+v", records)
	}
	for _, record := range records {
		want := record.Label == "a.txt"
		if record.PreviewInHistory != want {
			t.Fatalf("%s PreviewInHistory=%t, want %t", record.Label, record.PreviewInHistory, want)
		}
	}
	for _, record := range m.entityPromptRecords(nil) {
		if record.PreviewInHistory {
			t.Fatal("with no history every entity keeps its preview")
		}
	}

	calls := tools.CallsFromNative([]provider.ToolCall{{ID: "d1", Name: tools.ToolGetEntityDetails,
		Arguments: `{"entity_ids":["ent_00001"],"level":"full"}`}})
	if len(calls) != 1 || calls[0].InputErr != "" {
		t.Fatalf("decode get_entity_details: %+v", calls)
	}
	if output, _ := m.resolveEntityDetails(calls[0]); !strings.Contains(output, "alpha full payload") {
		t.Fatalf("get_entity_details = %q, want the full record", output)
	}
}

// TestCompactedReadBringsItsExcerptBack drives a real agent run whose early
// read results are compacted out of the continuation: the compacted read's
// excerpt is in the request (evidence never lost) while the read still in
// history is only cited.
func TestCompactedReadBringsItsExcerptBack(t *testing.T) {
	var steps []agentScriptStep
	for i := 1; i <= 4; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{{
			ID: fmt.Sprintf("r%d", i), Name: tools.ToolReadFile, Arguments: fmt.Sprintf(`{"path":"f%d.txt"}`, i),
		}}})
	}
	steps = append(steps,
		agentScriptStep{text: "Read all four files."},
		agentScriptStep{text: verifierJSON("passed", "read", "", false, false)},
	)
	m, prov := configureAgentTestModel(t, steps...)
	root := t.TempDir()
	for i := 1; i <= 4; i++ {
		body := fmt.Sprintf("unique content of file %d\n", i)
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("f%d.txt", i)), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	m.ctxStrategy = "summarize"
	m.cfg.Context.SummarizeAfterMessages = 4
	m.cfg.Context.KeepLastMessages = 2
	driveAgentCommands(t, m, m.startVerifiedRun("read f1.txt to f4.txt", nil))
	if m.agentLoop.run.Status != agent.DecisionDone {
		t.Fatalf("status = %s (%q)", m.agentLoop.run.Status, m.agentLoop.run.StopReason)
	}

	restored, cited := 0, 0
	for _, req := range prov.requests {
		inHistory := map[string]bool{}
		for _, msg := range req.Messages {
			if msg.Role == provider.RoleTool {
				for i := 1; i <= 4; i++ {
					if strings.Contains(msg.Content, fmt.Sprintf("unique content of file %d", i)) {
						inHistory[fmt.Sprintf("f%d.txt", i)] = true
					}
				}
			}
		}
		all := ""
		for _, msg := range req.Messages {
			all += msg.Content + "\n"
		}
		for i := 1; i <= 4; i++ {
			name := fmt.Sprintf("f%d.txt", i)
			if !strings.Contains(all, "read_file("+name+")") {
				continue // not retained as an observation in this request
			}
			excerpt := fmt.Sprintf("unique content of file %d", i)
			if inHistory[name] {
				if strings.Contains(all, "read_file("+name+") [cycle 1]: in the conversation above") {
					cited++
				}
				continue
			}
			// The directive's own observation line, not merely a summary that
			// happens to quote the text.
			if !strings.Contains(all, "read_file("+name+") [cycle 1]: "+excerpt) {
				t.Fatalf("%s was compacted away and its excerpt is missing: evidence lost", name)
			}
			restored++
		}
	}
	if restored == 0 || cited == 0 {
		t.Fatalf("restored=%d cited=%d: want both a compacted read shown in full and an in-history read cited", restored, cited)
	}
}
