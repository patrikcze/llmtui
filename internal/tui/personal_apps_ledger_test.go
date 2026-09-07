package tui

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/personalapps"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestPersonalAppsJournalUsesConfiguredAbsolutePathWithoutIO(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "personal-apps")
	journal, err := personalAppsJournalFromConfig(config.PersonalAppsMutationConfig{Enabled: true, LedgerPath: dir})
	if err != nil {
		t.Fatalf("personalAppsJournalFromConfig: %v", err)
	}
	ledger, ok := journal.(*personalapps.MutationLedger)
	if !ok {
		t.Fatalf("journal is %T, want *MutationLedger", journal)
	}
	if ledger.LedgerPath() != dir {
		t.Fatalf("path = %q, want %q", ledger.LedgerPath(), dir)
	}
}

func TestPersonalAppsJournalRejectsRelativeConfiguredPath(t *testing.T) {
	_, err := personalAppsJournalFromConfig(config.PersonalAppsMutationConfig{Enabled: true, LedgerPath: "relative"})
	if err == nil {
		t.Fatal("relative ledger path was accepted")
	}
}

func TestDependentPersonalAppsApplyIsBlockedButReadAndPrepareRemainRunnable(t *testing.T) {
	plan := newToolBatchPlan([]tools.Call{
		{Tool: tools.ToolPersonalApps, Body: `{"operation":"status"}`},
		{Tool: tools.ToolPersonalApps, Body: `{"operation":"change_prepare","arguments":{"changes":[]}}`},
		{Tool: tools.ToolPersonalApps, Body: `{"operation":"change_apply","arguments":{"plan_id":"plan_1"}}`},
	})
	if !blockDependentPersonalAppsApply(&plan) {
		t.Fatal("dependent apply was not blocked")
	}
	if plan.blocked[0] != "" || plan.blocked[1] != "" {
		t.Fatalf("read/prepare were blocked: %v", plan.blocked)
	}
	if !strings.Contains(plan.blocked[2], "separate human approval") {
		t.Fatalf("apply blocker = %q", plan.blocked[2])
	}
}
