package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/harness"
)

func TestExperimentalHarnessOnlyAffectsExplicitAgentSession(t *testing.T) {
	m, _ := configureAgentTestModel(t)
	m.cfg.Provider = "mock"
	compat := harness.Compatibility{Provider: m.cfg.ActiveProviderName(), Model: m.model}
	snapshot, err := harness.NewSnapshot("", []harness.Edit{harness.ExecuteTools}, "fixture", compat, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	m.harnessSnapshot = &snapshot
	if text := m.experimentalHarnessGuidance(); text != "" {
		t.Fatal("normal chat received experimental advice")
	}
	// An already active bounded run remains the only execution authority.
	m.agentLoop.run = &agent.AgentRun{Status: agent.DecisionRunning}
	text := m.experimentalHarnessGuidance()
	if !strings.Contains(text, "execute_tools") || !strings.Contains(text, "approvals still apply") {
		t.Fatal("explicit agent overlay missing", text)
	}
	m.harnessSnapshot = nil
	if m.experimentalHarnessGuidance() != "" {
		t.Fatal("disabled overlay present")
	}
	m.harnessSnapshot = &snapshot
	m.model = "another-model"
	if m.experimentalHarnessGuidance() != "" {
		t.Fatal("stale model overlay used")
	}
	m.model = compat.Model
	snapshot.Edits = []harness.Edit{"disable_approvals"}
	if m.experimentalHarnessGuidance() != "" {
		t.Fatal("corrupt/protected overlay used")
	}
}
