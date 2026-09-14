package tui

import (
	"fmt"

	"github.com/patrikcze/llmtui/internal/provider"
)

const visiblePseudoCallRecoveryFeedback = "Tool-call recovery: the previous response used a visible tool-control envelope that was not decoded. If a tool is still needed, issue one call using only the currently offered tool schema. Do not repeat a textual tool envelope. Normal validation and approval still apply."

// visiblePseudoCallRecoveryDecision accepts only a provider's strict,
// content-free pseudo-call diagnosis. It does not inspect reply text, parse
// arguments, infer a tool name, or create a call; a subsequent response must
// still reach the ordinary native/fenced validation and approval pipeline.
func (m *Model) visiblePseudoCallRecoveryDecision(events []provider.ToolCallDiagnostic, calls []provider.ToolCall, truncated, malformed bool) provider.ToolRecoveryDecision {
	if !m.useNativeTools() || len(calls) != 0 || truncated || malformed {
		return provider.ToolRecoveryDecision{}
	}
	for _, event := range events {
		if event.Classification == provider.ToolCallSuspectedCensored {
			return m.turnRuntime.claimToolRecovery(provider.ToolRecoveryVisiblePseudoCall)
		}
	}
	return provider.ToolRecoveryDecision{}
}

func (m *Model) recordToolRecovery(decision provider.ToolRecoveryDecision) {
	if decision.Reason == "" {
		return
	}
	classification := provider.ToolCallRecoveryScheduled
	if !decision.Allowed() {
		classification = provider.ToolCallRecoveryExhausted
	}
	detail := string(decision.Reason)
	if decision.Attempt > 0 {
		detail = fmt.Sprintf("%s_attempt_%d", detail, decision.Attempt)
	}
	m.recordToolCallDiagnostics(provider.ToolCallDiagnostic{
		Stage: provider.ToolCallStageRecovery, Classification: classification, Detail: detail,
	})
}
