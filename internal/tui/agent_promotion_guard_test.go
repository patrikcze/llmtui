package tui

import (
	"testing"
)

// TestUnverifiedRunIsNeverPromoted pins the documented rule (docs/memory.md,
// docs/security.md) that only an outcome a semantic verifier passed becomes
// project memory. In verifier.mode off and deterministic, the controller
// synthesizes a "passed" verdict without any model verification ("verification
// disabled; executor output was not verified"); promoting that outcome
// wrote unverified executor text into approved project memory.
func TestUnverifiedRunIsNeverPromoted(t *testing.T) {
	for _, mode := range []string{"off", "deterministic"} {
		t.Run(mode, func(t *testing.T) {
			m, _ := configureAgentTestModel(t,
				agentScriptStep{text: "Implemented the bounded change."},
			)
			m.cfg.Agent.Verifier.Mode = mode
			m.memEnabled = true
			driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))

			if status := m.agentLoop.run.Status; status != "done" {
				t.Fatalf("run status = %s, want done", status)
			}
			records, err := m.projectStore.Load()
			if err != nil {
				t.Fatal(err)
			}
			if len(records) != 0 {
				t.Fatalf("mode %s promoted an unverified outcome: %+v", mode, records)
			}
		})
	}
}
