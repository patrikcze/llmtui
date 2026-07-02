package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

// modelDotColors cycles through the legend colors for the model breakdown.
var modelDotColors = []lipgloss.AdaptiveColor{
	{Light: "#B4551F", Dark: "#E58E54"}, // accent
	{Light: "#3D7A45", Dark: "#7CBF85"}, // green
	{Light: "#3B5FA0", Dark: "#8AB0E8"}, // blue
	{Light: "#7A4A9E", Dark: "#C39FE0"}, // purple
	{Light: "#A0713B", Dark: "#E0C08A"}, // gold
	{Light: "#8A8580", Dark: "#9C9691"}, // gray
}

type modelTotal struct {
	Model    string
	Prompt   int
	Reply    int
	Requests int
}

func (t modelTotal) total() int { return t.Prompt + t.Reply }

// usageOverlay renders the all-time usage dashboard for /usage.
func (m *Model) usageOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("usage") + "\n\n")

	if m.historyDir == "" {
		b.WriteString(m.theme.SystemNote.Render("history saving is disabled (chat.save_history)") + "\n")
		return b.String() + "\n" + m.theme.SystemNote.Render("esc to close")
	}
	records, err := history.ReadUsage(m.historyDir)
	if err != nil {
		b.WriteString(m.theme.ErrorText.Render(err.Error()) + "\n")
		return b.String() + "\n" + m.theme.SystemNote.Render("esc to close")
	}
	if len(records) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no usage recorded yet — chat a bit first") + "\n")
		return b.String() + "\n" + m.theme.SystemNote.Render("esc to close")
	}

	now := time.Now()
	ascii := false

	// --- Tokens per day (last 30 days, gaps filled) -----------------------
	b.WriteString(m.theme.UserLabel.Render("tokens per day") + "\n")
	byDay := map[string]int{}
	for _, d := range history.AggregateByDay(records) {
		byDay[d.Day] = d.TotalTokens()
	}
	const window = 30
	values := make([]int, window)
	xlabels := map[int]string{}
	for i := 0; i < window; i++ {
		day := now.AddDate(0, 0, i-window+1)
		values[i] = byDay[day.Format("2006-01-02")]
		if i == 0 || i == window/2 || i == window-1 {
			xlabels[i] = day.Format("Jan 02")
		}
	}
	chart := components.BarChart(components.BarChartData{
		Values: values, XLabels: xlabels, Height: 6, ASCII: ascii,
	})
	for _, line := range chart {
		b.WriteString(m.theme.ChartBar.Render(line) + "\n")
	}
	b.WriteString("\n")

	// --- Activity heatmap --------------------------------------------------
	b.WriteString(m.theme.UserLabel.Render("activity") + "\n")
	weeks := 16
	if avail := (m.width - 8); avail > 0 && avail < weeks {
		weeks = avail
	}
	heat := components.Heatmap(components.HeatmapData{
		Values: byDay, Weeks: weeks, Today: now, ASCII: ascii,
	})
	for _, line := range heat {
		b.WriteString(m.theme.ChartBar.Render(line) + "\n")
	}
	b.WriteString("\n")

	// --- Per-model breakdown ------------------------------------------------
	b.WriteString(m.theme.UserLabel.Render("models") + "\n")
	models, grand := aggregateByModel(records)
	for i, mt := range models {
		if i >= len(modelDotColors) {
			break
		}
		dot := lipgloss.NewStyle().Foreground(modelDotColors[i]).Render("●")
		pct := 0.0
		if grand > 0 {
			pct = 100 * float64(mt.total()) / float64(grand)
		}
		name := mt.Model
		if len(name) > 40 {
			name = name[:39] + "…"
		}
		fmt.Fprintf(&b, "  %s %s %s\n", dot,
			m.theme.StatusValue.Render(fmt.Sprintf("%-42s", name)),
			m.theme.StatusBar.Render(fmt.Sprintf("(%.1f%%)", pct)))
		fmt.Fprintf(&b, "    %s\n", m.theme.StatusBar.Render(fmt.Sprintf(
			"in: %s · out: %s · %d requests",
			components.FormatTokens(mt.Prompt), components.FormatTokens(mt.Reply), mt.Requests)))
	}
	b.WriteString("\n")

	// --- Summary ------------------------------------------------------------
	b.WriteString(m.theme.UserLabel.Render("all time") + "\n")
	sessions := 0
	if metas, err := history.List(m.historyDir); err == nil {
		sessions = len(metas)
	}
	activeDays, topDay, topDayTokens, streak := usageSummary(byDay, now)
	favorite := ""
	if len(models) > 0 {
		favorite = models[0].Model
	}
	summary := [][2]string{
		{"total tokens", components.FormatTokens(grand)},
		{"requests", fmt.Sprintf("%d", len(records))},
		{"sessions saved", fmt.Sprintf("%d", sessions)},
		{"favorite model", favorite},
		{"active days", fmt.Sprintf("%d", activeDays)},
		{"most active day", fmt.Sprintf("%s (%s tok)", topDay, components.FormatTokens(topDayTokens))},
		{"current streak", fmt.Sprintf("%d day(s)", streak)},
	}
	for _, row := range summary {
		fmt.Fprintf(&b, "  %s %s\n",
			m.theme.StatusBar.Render(fmt.Sprintf("%-16s", row[0])),
			m.theme.StatusValue.Render(row[1]))
	}

	b.WriteString("\n" + m.theme.SystemNote.Render("esc to close"))
	return b.String()
}

func aggregateByModel(records []history.UsageRecord) ([]modelTotal, int) {
	byModel := map[string]*modelTotal{}
	grand := 0
	for _, r := range records {
		key := r.Provider + "/" + r.Model
		t, ok := byModel[key]
		if !ok {
			t = &modelTotal{Model: key}
			byModel[key] = t
		}
		t.Prompt += r.PromptTokens
		t.Reply += r.CompletionTokens
		t.Requests++
		grand += r.PromptTokens + r.CompletionTokens
	}
	out := make([]modelTotal, 0, len(byModel))
	for _, t := range byModel {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].total() > out[j].total() })
	return out, grand
}

// usageSummary derives active-day stats from the per-day totals.
func usageSummary(byDay map[string]int, now time.Time) (activeDays int, topDay string, topTokens, streak int) {
	for day, tokens := range byDay {
		if tokens == 0 {
			continue
		}
		activeDays++
		if tokens > topTokens {
			topTokens = tokens
			topDay = day
		}
	}
	// Streak counts back from today; a quiet today doesn't break yesterday's run.
	day := now
	if byDay[day.Format("2006-01-02")] == 0 {
		day = day.AddDate(0, 0, -1)
	}
	for byDay[day.Format("2006-01-02")] > 0 {
		streak++
		day = day.AddDate(0, 0, -1)
	}
	return activeDays, topDay, topTokens, streak
}
