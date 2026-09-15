package tui

import (
	"strings"

	"github.com/patrikcze/llmtui/internal/contextmgr"
	"github.com/patrikcze/llmtui/internal/terminaltext"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

// statusView projects immutable model state into the status component. It is
// deliberately side-effect free so View and render can be called repeatedly.
func (m *Model) statusView() string {
	prof, _ := m.activeProfile()
	profileLabel := prof.Name
	if m.profileMode == "auto" || m.profileMode == "" {
		profileLabel = "auto/" + prof.Name
	}
	ctxWindow, _ := m.contextWindow()
	return components.StatusBar(m.theme, components.StatusBarData{
		Provider:       terminaltext.Sanitize(m.prov.Name()),
		Model:          terminaltext.Sanitize(m.model),
		Connected:      m.connected,
		DemoMode:       m.demoMode,
		TotalTokens:    m.session.TotalTokens(),
		LastTPS:        m.lastTPS,
		Estimated:      m.session.AnyEstimated,
		Profile:        terminaltext.Sanitize(profileLabel),
		PromptMode:     m.effectivePromptMode(),
		Template:       terminaltext.Sanitize(m.template),
		ContextUsed:    contextmgr.EstimateTokens(m.session.Messages),
		ContextLimit:   ctxWindow,
		CacheOn:        m.responseCache != nil && m.responseCache.Enabled(),
		SummaryOn:      m.summary != "",
		ToolsOn:        m.toolsOn,
		WebOn:          m.webOn,
		ShowTokenStats: m.cfg.UI.ShowTokenStats,
		Compact:        m.layout.compact,
	}, m.width)
}

func statusLineCount(status string) int {
	return strings.Count(status, "\n") + 1
}
