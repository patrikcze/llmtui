package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// modelUsageStat accumulates completed-request usage for one provider/model
// pair, in first-use order, for the exit summary table.
type modelUsageStat struct {
	Provider  string
	Model     string
	Requests  int
	Prompt    int
	Reply     int
	Estimated bool
}

// exitSummaryData is everything the exit summary needs, decoupled from the
// Bubble Tea model so rendering stays a pure, testable function.
type exitSummaryData struct {
	SessionID   string
	Saved       bool
	UserMsgs    int
	ReplyMsgs   int
	ToolOK      int
	ToolErr     int
	CacheOn     bool
	CacheHits   int
	CacheMisses int
	WallTime    time.Duration
	APITime     time.Duration
	Models      []modelUsageStat
	Width       int
}

// exitSummary snapshots the session for rendering after the TUI closes.
func (m *Model) exitSummary() exitSummaryData {
	d := exitSummaryData{
		SessionID: m.sessionName,
		Saved:     m.savedPath != "",
		UserMsgs:  m.sentCount,
		ReplyMsgs: m.replyCount,
		ToolOK:    m.toolOK,
		ToolErr:   m.toolErr,
		WallTime:  time.Since(m.startedAt),
		APITime:   m.apiTime,
		Models:    m.modelStats,
		Width:     m.width,
	}
	if m.responseCache != nil {
		cs := m.responseCache.Stats()
		d.CacheOn = cs.Enabled
		d.CacheHits = cs.Hits
		d.CacheMisses = cs.Misses
	}
	return d
}

// recordModelUsage folds one completed provider request into the per-model
// totals. Cache hits never reach here: they make no API request.
func (m *Model) recordModelUsage(providerName, model string, prompt, reply int, estimated bool, d time.Duration) {
	m.apiTime += d
	for i := range m.modelStats {
		s := &m.modelStats[i]
		if s.Provider == providerName && s.Model == model {
			s.Requests++
			s.Prompt += prompt
			s.Reply += reply
			s.Estimated = s.Estimated || estimated
			return
		}
	}
	m.modelStats = append(m.modelStats, modelUsageStat{
		Provider: providerName, Model: model,
		Requests: 1, Prompt: prompt, Reply: reply, Estimated: estimated,
	})
}

// Exit summary table column widths (right-aligned numeric columns).
const (
	exitColReqs = 6
	exitColIn   = 15
	exitColOut  = 16
)

// renderExitSummary draws the closing session report: a bordered panel with
// interaction counts, performance timings, and a per-model usage table,
// followed by a history hint when the session was saved.
func renderExitSummary(t styles.Theme, d exitSummaryData) string {
	inner := d.Width - 4 // rounded border + horizontal padding
	if inner > 74 {
		inner = 74
	}
	if inner < 44 {
		inner = 44
	}

	accent := lipgloss.NewStyle().Foreground(t.Accent)
	label := t.StatusKey
	value := t.StatusValue
	heading := t.Badge
	faint := t.HelpFooter

	row := func(b *strings.Builder, key, val string) {
		fmt.Fprintf(b, "%s %s\n", label.Render(fmt.Sprintf("%-15s", key+":")), value.Render(val))
	}

	var b strings.Builder
	b.WriteString(accent.Render("Chat session closed.") + " " + t.BadgeOK.Render("Goodbye!") + "\n\n")

	// --- Interaction summary -------------------------------------------------
	b.WriteString(heading.Render("Interaction Summary") + "\n")
	row(&b, "Session ID", d.SessionID)
	row(&b, "Messages", fmt.Sprintf("%d sent · %d replies", d.UserMsgs, d.ReplyMsgs))
	if d.ToolOK+d.ToolErr > 0 {
		row(&b, "Tool Calls", fmt.Sprintf("%d ( ✓ %d · ✗ %d )", d.ToolOK+d.ToolErr, d.ToolOK, d.ToolErr))
	}
	if d.CacheOn {
		row(&b, "Cache", fmt.Sprintf("%d hits · %d misses", d.CacheHits, d.CacheMisses))
	}
	b.WriteString("\n")

	// --- Performance -----------------------------------------------------------
	b.WriteString(heading.Render("Performance") + "\n")
	row(&b, "Wall Time", formatWallDuration(d.WallTime))
	apiLine := formatWallDuration(d.APITime)
	if d.WallTime > 0 {
		apiLine += " " + faint.Render(fmt.Sprintf("(%.1f%% of session)", 100*d.APITime.Seconds()/d.WallTime.Seconds()))
	}
	row(&b, "API Time", apiLine)
	if reply, secs := totalReplyTokens(d.Models), d.APITime.Seconds(); reply > 0 && secs > 0 {
		row(&b, "Avg Speed", fmt.Sprintf("%.1f tok/s", float64(reply)/secs))
	}
	b.WriteString("\n")

	// --- Model usage table ------------------------------------------------------
	nameW := inner - exitColReqs - exitColIn - exitColOut
	b.WriteString(heading.Render(fmt.Sprintf("%-*s", nameW, "Model Usage")))
	b.WriteString(heading.Render(fmt.Sprintf("%*s%*s%*s", exitColReqs, "Reqs", exitColIn, "Input Tokens", exitColOut, "Output Tokens")))
	b.WriteString("\n" + faint.Render(strings.Repeat("─", inner)) + "\n")
	if len(d.Models) == 0 {
		b.WriteString(faint.Render("no completed requests this session") + "\n")
	}
	estimated := false
	for _, ms := range d.Models {
		estimated = estimated || ms.Estimated
		mark := ""
		if ms.Estimated {
			mark = "~"
		}
		lines := wrapName(ms.Provider+"/"+ms.Model, nameW-1)
		b.WriteString(value.Render(fmt.Sprintf("%-*s", nameW, lines[0])))
		b.WriteString(value.Render(fmt.Sprintf("%*d", exitColReqs, ms.Requests)))
		b.WriteString(t.ChartBar.Render(fmt.Sprintf("%*s", exitColIn, mark+groupThousands(ms.Prompt))))
		b.WriteString(t.ChartBar.Render(fmt.Sprintf("%*s", exitColOut, mark+groupThousands(ms.Reply))))
		b.WriteString("\n")
		for _, cont := range lines[1:] {
			b.WriteString(value.Render(cont) + "\n")
		}
	}
	if estimated {
		b.WriteString(faint.Render("~ provider returned no usage; token counts are estimated") + "\n")
	}

	panel := t.Panel.Width(inner + 2).Render(strings.TrimRight(b.String(), "\n"))

	out := panel
	if d.Saved {
		hint := faint.Render("To revisit this session, run ") +
			accent.Render("llmtui history") +
			faint.Render(" (saved as "+d.SessionID+")")
		out += "\n" + hint
	}
	return out
}

func totalReplyTokens(models []modelUsageStat) int {
	total := 0
	for _, m := range models {
		total += m.Reply
	}
	return total
}

// formatWallDuration renders a duration as "1h 2m 3s" / "12m 49s" / "49s".
func formatWallDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}

// groupThousands formats 19687 as "19 687" for readable token counts.
func groupThousands(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	if neg {
		s = s[1:]
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + " " + s[i:]
	}
	if neg {
		s = "-" + s
	}
	return s
}

// wrapName splits an over-long model name into table-width chunks.
func wrapName(name string, w int) []string {
	if w < 1 {
		w = 1
	}
	r := []rune(name)
	var out []string
	for len(r) > w {
		out = append(out, string(r[:w]))
		r = r[w:]
	}
	return append(out, string(r))
}
