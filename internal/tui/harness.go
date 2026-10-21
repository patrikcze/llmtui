package tui

import "github.com/patrikcze/llmtui/internal/provider"

// The overlay is advisory and only reaches the existing agent executor's
// prompt composition. It changes no contract, verifier, policy or tool loop.
func (m *Model) experimentalHarnessGuidance() string {
	s := m.harnessSnapshot
	if s == nil || !m.agentRunActive() || s.Compatibility.Provider != m.cfg.ActiveProviderName() || s.Compatibility.Model != m.model || s.Compatibility.Runtime != provider.RuntimeFingerprintOf(m.prov) {
		return ""
	}
	guidance, err := s.GuidanceText()
	if err != nil {
		return ""
	}
	return guidance
}
