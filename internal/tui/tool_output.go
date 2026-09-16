package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
	"github.com/patrikcze/llmtui/internal/tools"
)

func toolOutputZoneID(i int) string {
	return "tool-output-" + strconv.Itoa(i)
}

func (m *Model) toolOutputIsExpanded(i int) bool {
	if expanded, ok := m.toolOutputExpanded[i]; ok {
		return expanded
	}
	return m.toolsShowOutput
}

func (m *Model) resetToolOutput() {
	clear(m.toolOutputExpanded)
	m.toolOutputRevision++
}

func (m *Model) renderToolOutput(i int, msg provider.Message) string {
	expanded := m.toolOutputIsExpanded(i)
	glyph, hint := "+", "click to expand"
	if expanded {
		glyph, hint = "-", "click to collapse"
	}
	summary := tools.SummarizeOutput(msg.Content)
	if msg.Role == provider.RoleUser {
		summary = fmt.Sprintf("Tools: %d lines of output", strings.Count(strings.TrimSpace(msg.Content), "\n")+1)
	}
	header := "  ⎿  " + glyph + " " + summary + " · " + hint
	var b strings.Builder
	b.WriteString(zone.Mark(toolOutputZoneID(i), m.transcriptCaptionStyle().Render(terminaltext.Sanitize(header))))
	b.WriteByte('\n')
	if !expanded {
		if msg.Role == provider.RoleUser {
			// Keep per-call status (especially errors) visible for fenced batches.
			collapsed := strings.ReplaceAll(tools.CollapseResults(msg.Content), "⎿ ", "⎿  ")
			b.WriteString(m.theme.ReasoningText.Render(terminaltext.Sanitize(collapsed)))
			b.WriteByte('\n')
		}
		return b.String()
	}
	if msg.Role == provider.RoleUser && i > 0 {
		if previous := m.session.Messages[i-1]; previous.Role == provider.RoleAssistant {
			b.WriteString(m.renderToolDetail("Execution:\n" + previous.Content))
		}
	}
	// Match by call ID rather than adjacency: parallel batches can contain
	// several calls and return their results in a different order.
	execution := ""
	if msg.ToolName != "" {
		execution = "Tool: " + msg.ToolName
	}
	if msg.ToolCallID != "" {
	findCall:
		for j := i - 1; j >= 0; j-- {
			for _, call := range m.session.Messages[j].ToolCalls {
				if call.ID == msg.ToolCallID {
					execution = "Tool: " + call.Name + "\nArguments: " + call.Arguments
					break findCall
				}
			}
		}
	}
	if execution != "" {
		b.WriteString(m.renderToolDetail(execution))
	}
	b.WriteString(m.renderToolDetail("Output:\n" + msg.Content))
	return b.String()
}

func (m *Model) renderToolDetail(text string) string {
	text = terminaltext.Sanitize(text)
	return m.theme.ReasoningText.Render("      "+strings.ReplaceAll(strings.TrimRight(text, "\n"), "\n", "\n      ")) + "\n"
}

func (m *Model) updateToolOutputClick(msg tea.MouseReleaseMsg) (tea.Model, tea.Cmd, bool) {
	if msg.Button != tea.MouseLeft || m.overlayOpen || len(m.pendingCalls) != 0 {
		return m, nil, false
	}
	viewportZone := zone.Get(chatViewportZoneID)
	if viewportZone == nil || !viewportZone.InBounds(msg) {
		return m, nil, false
	}
	if m.sel.selecting {
		x, y := clampToZone(viewportZone, msg.X, msg.Y)
		if x != m.sel.selStartX || y != m.sel.selStartY {
			return m, nil, false
		}
	}
	for i, message := range m.session.Messages {
		isResult := message.Role == provider.RoleTool ||
			(message.Role == provider.RoleUser && strings.HasPrefix(message.Content, tools.ResultsPrefix))
		if !isResult || !zone.Get(toolOutputZoneID(i)).InBounds(msg) {
			continue
		}
		if m.toolOutputExpanded == nil {
			m.toolOutputExpanded = make(map[int]bool)
		}
		m.toolOutputExpanded[i] = !m.toolOutputIsExpanded(i)
		m.toolOutputRevision++
		m.clearSelection()
		offset := m.viewport.YOffset()
		m.refreshViewport()
		// Expanding at the bottom should leave the caption in view instead
		// of jumping to the end of a potentially very long result.
		m.viewport.SetYOffset(offset)
		return m, nil, true
	}
	return m, nil, false
}
