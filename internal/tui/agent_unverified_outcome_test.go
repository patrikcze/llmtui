package tui

import (
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/agent"
)

// TestUnverifiedModesDoNotReportVerification: in deterministic and off
// verifier modes a clean cycle ends the run, as documented, but nothing
// judged the answer. A confident answer that never used a tool used to be
// reported as "verification passed" with the criterion satisfied by
// "controller verification passed the pinned contract". The outcome now
// says it was not verified.
func TestUnverifiedModesDoNotReportVerification(t *testing.T) {
	for _, mode := range []string{"deterministic", "off"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := callersRunModel(t, agentScriptStep{text: "parseConfig is only called from main.go. Done."})
			m.cfg.Agent.Verifier.Mode = mode
			driveAgentCommands(t, m, m.startVerifiedRun("Find every caller of parseConfig in this project.", nil))

			run := m.agentLoop.run
			if run.Status != agent.DecisionDone || run.Cycle != 1 {
				t.Fatalf("status = %s after %d cycle(s), want the documented stop after one clean cycle", run.Status, run.Cycle)
			}
			if strings.Contains(m.notice, "verification passed") || !strings.Contains(m.notice, "not verified (verifier mode "+mode+")") {
				t.Errorf("notice = %q, want it to say the run was not verified", m.notice)
			}
			if !strings.Contains(run.StopReason, "without semantic verification") {
				t.Errorf("stop reason = %q, want it to say the criteria were not verified", run.StopReason)
			}
		})
	}
}
