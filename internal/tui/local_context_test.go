package tui

import (
	"testing"

	"github.com/patrikcze/llmtui/internal/tools"
)

func TestLocalContextReadsAreNotPermanentlyBlockedAsNoProgress(t *testing.T) {
	ledger := newProgressLedger(1)
	call := tools.Call{Tool: tools.ToolLocalContext, ContextKind: tools.LocalContextProcesses}
	fingerprint := progressFingerprint(call)
	ledger.observe(fingerprint, "unchanged")
	plan, terminal := ledger.planBatch([]tools.Call{call})
	if terminal || plan.blockedCount() != 0 {
		t.Fatalf("volatile local context was blocked: plan=%+v terminal=%v", plan, terminal)
	}
}
