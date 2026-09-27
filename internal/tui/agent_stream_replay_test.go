package tui

import (
	"context"
	"fmt"
	"strings"
	"syscall"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestVerifiedAgentReplaysInterruptedStreamOnce is the regression for audit
// P2-7: one mid-stream transport interruption used to fail the whole agent
// run. The interrupted round must be replayed once, its partial text must
// never enter history, and a tool call completed in an earlier round must
// not run again.
func TestVerifiedAgentReplaysInterruptedStreamOnce(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "call-1", Name: tools.ToolListDir, Arguments: `{}`}}},
		agentScriptStep{text: "partial answer that must be discarded", streamErr: provider.ErrStreamInterrupted},
		agentScriptStep{text: "Listed the workspace and completed the objective."},
		agentScriptStep{text: verifierJSON("passed", "tool evidence supports completion", "", false, false)},
	)
	m.toolsOn = true
	m.toolsNative = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	driveAgentCommands(t, m, m.startVerifiedRun("inspect the workspace", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone {
		t.Fatalf("run status = %s (%q), want done after one replay", run.Status, run.StopReason)
	}
	if len(prov.requests) != 5 {
		t.Fatalf("provider requests = %d, want contract + tool round + interrupted + replay + verifier", len(prov.requests))
	}
	if run.ToolCalls != 1 {
		t.Errorf("tool calls = %d, want the earlier round's call executed exactly once", run.ToolCalls)
	}
	for _, message := range m.session.Messages {
		if strings.Contains(message.Content, "partial answer that must be discarded") {
			t.Fatal("interrupted partial text entered conversation history")
		}
	}
	// The replay resends the same history the interrupted request had.
	interrupted, replay := prov.requests[2], prov.requests[3]
	if len(interrupted.Messages) != len(replay.Messages) {
		t.Errorf("replay has %d messages, interrupted request had %d", len(replay.Messages), len(interrupted.Messages))
	}
}

// TestVerifiedAgentSecondInterruptionInSameRoundFails bounds the replay: a
// round interrupted again after its one replay fails the run as before.
func TestVerifiedAgentSecondInterruptionInSameRoundFails(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "first", streamErr: provider.ErrStreamInterrupted},
		agentScriptStep{text: "second", streamErr: fmt.Errorf("read stream: %w", syscall.ECONNRESET)},
		agentScriptStep{text: "must never be requested"},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("answer the question", nil))

	if status := m.agentLoop.run.Status; status != agent.DecisionFailed {
		t.Fatalf("run status = %s, want failed after the replay was also interrupted", status)
	}
	if len(prov.requests) != 3 {
		t.Fatalf("provider requests = %d, want contract + interrupted + one replay", len(prov.requests))
	}
}

// TestVerifiedAgentDoesNotReplayNonTransportStreamError keeps protocol and
// size failures terminal: replaying them would only repeat the same failure.
func TestVerifiedAgentDoesNotReplayNonTransportStreamError(t *testing.T) {
	for name, err := range map[string]error{
		"harmony protocol":   provider.ErrHarmonyProtocol,
		"response too large": provider.ErrResponseTooLarge,
		"decode chunk":       fmt.Errorf("decode stream chunk: %w", fmt.Errorf("unexpected end of JSON input")),
	} {
		t.Run(name, func(t *testing.T) {
			m, prov := configureAgentTestModel(t,
				agentScriptStep{text: "x", streamErr: err},
				agentScriptStep{text: "must never be requested"},
			)
			driveAgentCommands(t, m, m.startVerifiedRun("answer the question", nil))
			if status := m.agentLoop.run.Status; status != agent.DecisionFailed {
				t.Fatalf("run status = %s, want failed", status)
			}
			if len(prov.requests) != 2 {
				t.Fatalf("provider requests = %d, want contract + the failed request only", len(prov.requests))
			}
		})
	}
}

// TestOrdinaryChatDoesNotReplayInterruptedStream keeps the replay scoped to
// agent runs: in ordinary chat the partial reply is kept and the user
// decides whether to retry.
func TestOrdinaryChatDoesNotReplayInterruptedStream(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{text: "partial", streamErr: provider.ErrStreamInterrupted},
		agentScriptStep{text: "must never be requested"},
	)
	m.agentOn = false
	driveAgentCommands(t, m, m.dispatch("hello", nil))
	if len(prov.requests) != 1 {
		t.Fatalf("provider requests = %d, want the single interrupted request", len(prov.requests))
	}
	if !strings.Contains(m.errText, "partial reply kept") {
		t.Errorf("errText = %q, want the partial reply preserved", m.errText)
	}
}

func TestRetryableStreamError(t *testing.T) {
	cases := map[string]struct {
		err  error
		want bool
	}{
		"interrupted":         {provider.ErrStreamInterrupted, true},
		"wrapped interrupted": {fmt.Errorf("x: %w", provider.ErrStreamInterrupted), true},
		"connection reset":    {fmt.Errorf("read stream: %w", syscall.ECONNRESET), true},
		"unexpected EOF":      {fmt.Errorf("read stream: unexpected EOF"), true},
		"canceled":            {context.Canceled, false},
		"deadline":            {context.DeadlineExceeded, false},
		"too large":           {provider.ErrResponseTooLarge, false},
		"harmony":             {provider.ErrHarmonyProtocol, false},
		"nil":                 {nil, false},
	}
	for name, tc := range cases {
		if got := provider.RetryableStreamError(tc.err); got != tc.want {
			t.Errorf("%s: RetryableStreamError = %v, want %v", name, got, tc.want)
		}
	}
}
