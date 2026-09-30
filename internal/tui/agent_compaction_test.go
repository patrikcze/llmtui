package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestVerifiedAgentRecordsContinuationCompaction is the TUI regression for
// audit P2-8: compaction during tool-round continuations — where long
// episodes actually compact — was never recorded in the run, and the
// executor was never told that earlier tool results had left its context.
func TestVerifiedAgentRecordsContinuationCompaction(t *testing.T) {
	steps := []agentScriptStep{}
	for i := 0; i < 4; i++ {
		steps = append(steps, agentScriptStep{toolCalls: []provider.ToolCall{{ID: "l" + string(rune('a'+i)), Name: tools.ToolListDir, Arguments: `{"limit":` + string(rune('1'+i)) + `}`}}})
	}
	steps = append(steps,
		agentScriptStep{text: "Listed the workspace."},
		agentScriptStep{text: verifierJSON("passed", "listed", "", false, false)},
	)
	m, prov := configureAgentTestModel(t, steps...)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.ctxStrategy = "summarize"
	m.cfg.Context.SummarizeAfterMessages = 4
	m.cfg.Context.KeepLastMessages = 2
	driveAgentCommands(t, m, m.startVerifiedRun("list the workspace a few ways", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone {
		t.Fatalf("status = %s (%q)", run.Status, run.StopReason)
	}
	var compressed []agent.Event
	for _, event := range run.Events {
		if event.Kind == "context_compressed" {
			compressed = append(compressed, event)
		}
	}
	if len(compressed) == 0 {
		t.Fatal("no context_compressed event recorded for continuation requests")
	}
	if !strings.Contains(compressed[0].Detail, "compacted=") {
		t.Fatalf("event detail = %q, want the compacted count", compressed[0].Detail)
	}
	var sawNote bool
	for _, req := range prov.requests {
		for _, message := range req.Messages {
			// A continuation carries the current agent-cycle note in the
			// runtime context after history, not in its frozen system message.
			if strings.Contains(message.Content, "tool result(s) from this cycle were compacted out") {
				sawNote = true
			}
		}
	}
	if !sawNote {
		t.Fatal("executor was never told that earlier tool results were compacted away")
	}
}
