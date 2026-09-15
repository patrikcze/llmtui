package tui

// layoutMetrics is the single owner of main-screen row budgets. It contains
// only dimensions and visibility decisions so rendering it cannot mutate UI
// state or cause a second layout pass.
type layoutMetrics struct {
	viewportHeight int
	inputMaxLines  int
	suggestionRows int
	activityRows   int
	statusLines    int
	showUsage      bool
	compact        bool
}

func (m *Model) layoutFor(width, height, statusLines int) layoutMetrics {
	compact := m.cfg.UI.CompactMode || width < 80 || height < 20
	showUsage := m.cfg.UI.ShowUsageChart && !compact && height >= 30
	usageRows := 0
	if showUsage {
		usageRows = 4
	}
	if statusLines < 1 {
		statusLines = 1
	}

	activityRows := m.activityRowsForHeight(height)
	attachmentRows := 0
	if len(m.attachments) > 0 {
		attachmentRows = 1
	}
	targetViewportRows := 4
	if height < 12 {
		targetViewportRows = 1
	}
	// The composer has a top and bottom border. The footer remains one row,
	// including the compact and busy variants.
	fixedRows := usageRows + activityRows + attachmentRows + 2 + statusLines + 1
	requestedSuggestions := min(len(m.suggest.sugs), 6)
	switch {
	case height < 12:
		requestedSuggestions = min(requestedSuggestions, 1)
	case height < 20:
		requestedSuggestions = min(requestedSuggestions, 3)
	}
	availableSuggestions := max(0, height-fixedRows-1-targetViewportRows)
	suggestionRows := min(requestedSuggestions, availableSuggestions)
	inputMaxLines := max(1, height-fixedRows-suggestionRows-targetViewportRows)

	return layoutMetrics{
		viewportHeight: targetViewportRows,
		inputMaxLines:  inputMaxLines,
		suggestionRows: suggestionRows,
		activityRows:   activityRows,
		statusLines:    statusLines,
		showUsage:      showUsage,
		compact:        compact,
	}
}

func (m *Model) activityRowsForHeight(height int) int {
	rows := 0
	if m.verifierActivityHeight() > 0 {
		rows++
	}
	if m.activity == nil {
		return rows
	}
	limit := activityEntryLimit(height)
	entries := min(len(m.activity.entries), limit)
	rows += entries
	if len(m.activity.entries) > limit {
		rows++
	}
	return rows
}

func activityEntryLimit(height int) int {
	switch {
	case height < 12:
		return 1
	case height < 20:
		return 2
	default:
		return 6
	}
}

func (m *Model) viewportHeightFor(inputLines int) int {
	usageRows := 0
	if m.layout.showUsage {
		usageRows = 4
	}
	attachmentRows := 0
	if len(m.attachments) > 0 {
		attachmentRows = 1
	}
	height := m.height - usageRows - m.layout.activityRows - m.layout.suggestionRows - attachmentRows - 2 - inputLines - m.layout.statusLines - 1
	return max(1, height)
}
