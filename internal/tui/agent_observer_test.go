package tui

import (
	"reflect"
	"strings"
	"testing"
)

// TestAgentLoopStateKeepsAdvisoryStateSeparate guards the P2-10 seam:
// Laya's shadow generations and assist waits live only in their dedicated
// sub-structs (agent_observer.go), never as loose fields beside the
// authoritative cycle state, so a new advisory hook cannot quietly be wired
// into the middle of the authoritative flow.
func TestAgentLoopStateKeepsAdvisoryStateSeparate(t *testing.T) {
	typ := reflect.TypeOf(agentLoopState{})
	for i := 0; i < typ.NumField(); i++ {
		name := typ.Field(i).Name
		lower := strings.ToLower(name)
		if name == "shadow" || name == "assist" {
			continue
		}
		// contractAssistance* is the unrelated, content-free format-assistance
		// tracker for the contract stage, not Laya state.
		if strings.Contains(lower, "shadow") || strings.Contains(lower, "guarded") ||
			strings.Contains(lower, "criterionassist") || strings.Contains(lower, "criterionassessment") ||
			name == "pendingVerificationPlan" {
			t.Errorf("agentLoopState field %q is advisory state; move it into agentShadowState or agentAssistState", name)
		}
	}
	if typ.NumField() == 0 {
		t.Fatal("agentLoopState has no fields")
	}
}
