package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// continuationModel returns a model whose session ends with a native tool
// result, so compositionBase/prepareRequest with omitRaw build a native-tool
// continuation.
func continuationModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	m.toolsOn, m.toolsNative = true, true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	m.session.AddUser("read a.txt")
	m.session.AddMessage(provider.Message{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{{ID: "c1", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`}}})
	m.session.AddMessage(provider.Message{Role: provider.RoleTool, ToolCallID: "c1", Content: "alpha"})
	return m
}

// freezeFreshRequest prepares and commits the turn's fresh request, as
// dispatch does, and returns its system message.
func freezeFreshRequest(t *testing.T, m *Model) string {
	t.Helper()
	fresh, err := m.prepareRequest("read a.txt", nil, false)
	if err != nil {
		t.Fatal(err)
	}
	m.commitPrepared(fresh)
	return fresh.composed.Messages[0].Content
}

// TestAgentContinuationRepeatsFirstSystemMessage drives a real agent tool
// round: the continuation's system message is byte-identical to the turn's
// first executor request, and its current runtime state follows history.
func TestAgentContinuationRepeatsFirstSystemMessage(t *testing.T) {
	m, prov := configureAgentTestModel(t,
		agentScriptStep{toolCalls: []provider.ToolCall{{ID: "r1", Name: tools.ToolReadFile, Arguments: `{"path":"a.txt"}`}}},
		agentScriptStep{text: "a.txt says alpha."},
		agentScriptStep{text: verifierJSON("passed", "read and reported", "", false, false)},
	)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.toolsOn, m.toolsNative, m.toolsAutoApprove = true, true, true
	m.toolRunner = tools.NewRunner(root, 64)
	driveAgentCommands(t, m, m.startVerifiedRun("What does a.txt say?", nil))

	var executor []provider.ChatRequest
	for _, req := range prov.requests {
		if len(req.Tools) > 0 {
			executor = append(executor, req)
		}
	}
	if len(executor) != 2 {
		t.Fatalf("executor requests = %d, want 2", len(executor))
	}
	first, cont := executor[0].Messages, executor[1].Messages
	if cont[0].Role != provider.RoleSystem || cont[0].Content != first[0].Content {
		t.Fatal("the continuation's system message differs from the turn's first one")
	}
	last := cont[len(cont)-1]
	if last.Role != provider.RoleUser || !strings.HasPrefix(last.Content, "Runtime context supplied by llmtui") ||
		!strings.Contains(last.Content, "### Agent Cycle") {
		t.Fatalf("last message = %q, want the current runtime context after history", last.Content)
	}
}

// TestFrozenSystemNotReusedAfterStaticChange: a static change mid-turn (here
// the system prompt; in practice a loaded skill, disclosed tools, or a
// protocol fallback) must reach the model, so the frozen message is not
// reused and the new one is frozen instead.
func TestFrozenSystemNotReusedAfterStaticChange(t *testing.T) {
	m := continuationModel(t)
	freezeFreshRequest(t, m)
	if base := m.compositionBase("", nil, true); base.input.FrozenSystem == "" {
		t.Fatal("an unchanged continuation should reuse the frozen system message")
	}
	m.cfg.Chat.SystemPrompt = "changed static instructions"
	cont, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if cont.frozen || !strings.Contains(cont.composed.Messages[0].Content, "changed static instructions") {
		t.Fatalf("frozen=%t system=%q, want the changed system message", cont.frozen, cont.composed.Messages[0].Content)
	}
	m.commitPrepared(cont)
	if base := m.compositionBase("", nil, true); base.input.FrozenSystem != cont.composed.Messages[0].Content {
		t.Fatal("the continuation's new system message should be frozen for the next one")
	}
}

// TestFrozenSystemClearedWhenTurnEnds: a new turn never inherits the last
// turn's frozen system message.
func TestFrozenSystemClearedWhenTurnEnds(t *testing.T) {
	for _, end := range []struct {
		name string
		do   func(m *Model)
	}{
		{"final answer", func(m *Model) { m.complete(turnOutcomeFinalAnswer) }},
		{"cancelled", func(m *Model) { m.complete(turnOutcomeCancelled) }},
		{"new submission", func(m *Model) { m.resetTurn(0, "") }},
	} {
		t.Run(end.name, func(t *testing.T) {
			m := continuationModel(t)
			freezeFreshRequest(t, m)
			end.do(m)
			if base := m.compositionBase("", nil, true); base.input.FrozenSystem != "" {
				t.Fatal("frozen system message survived the end of its turn")
			}
		})
	}
}

// TestFrozenSystemNeverCostsHistory: the frozen message still carries the
// turn's first runtime sections, so it is larger. When using it would make
// the request not fit (or compact more history), the continuation falls back
// to the unfrozen layout instead of failing.
func TestFrozenSystemNeverCostsHistory(t *testing.T) {
	m := continuationModel(t)
	freezeFreshRequest(t, m)
	base := m.compositionBase("", nil, true)
	huge := strings.Repeat("stale runtime data ", 20000) // far beyond any window
	m.freezeSystem(m.frozenSystemKey(), base.staticSystem, huge)

	cont, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatalf("prepareRequest: %v (the unfrozen layout fits)", err)
	}
	if cont.frozen || strings.Contains(cont.composed.Messages[0].Content, "stale runtime data") {
		t.Fatal("an oversized frozen system message was used instead of the unfrozen layout")
	}
}

// TestCacheKeyCoversRuntimeContextMessage keeps the Workspace Tool Safety
// Invariant: two continuations with the identical frozen system message but
// different runtime context must not share a response-cache key.
func TestCacheKeyCoversRuntimeContextMessage(t *testing.T) {
	m := continuationModel(t)
	freezeFreshRequest(t, m)
	m.toolRecoveryFeedback = "runtime note A"
	a, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	m.toolRecoveryFeedback = "runtime note B"
	b, err := m.prepareRequest("", nil, true)
	if err != nil {
		t.Fatal(err)
	}
	if !a.frozen || !b.frozen || a.composed.Messages[0].Content != b.composed.Messages[0].Content {
		t.Fatal("both continuations should reuse the same frozen system message")
	}
	if m.cacheKeyFromPrepared("", a) == m.cacheKeyFromPrepared("", b) {
		t.Fatal("cache key ignores the runtime context message")
	}
}
