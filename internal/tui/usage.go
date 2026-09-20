package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/tui/components"
	"github.com/patrikcze/llmtui/internal/tui/styles"
)

// modelDotColors cycles through the legend colors for the model breakdown.
// Each pair is resolved once via styles.IsDark, the local stand-in for
// lipgloss v1's removed AdaptiveColor.
var modelDotColors = func() []color.Color {
	pick := lipgloss.LightDark(styles.IsDark())
	pairs := [][2]string{
		{"#B4551F", "#E58E54"}, // accent
		{"#3D7A45", "#7CBF85"}, // green
		{"#3B5FA0", "#8AB0E8"}, // blue
		{"#7A4A9E", "#C39FE0"}, // purple
		{"#A0713B", "#E0C08A"}, // gold
		{"#8A8580", "#9C9691"}, // gray
	}
	colors := make([]color.Color, len(pairs))
	for i, p := range pairs {
		colors[i] = pick(lipgloss.Color(p[0]), lipgloss.Color(p[1]))
	}
	return colors
}()

// usageRange narrows the /usage overlay to a trailing window of days, or
// usageRangeAll for the full recorded history. It is cycled with the 'r'
// key while the overlay is open (see updateOverlay).
type usageRange int

const (
	usageRangeAll usageRange = iota
	usageRangeLast7
	usageRangeLast30
)

func (r usageRange) next() usageRange {
	switch r {
	case usageRangeAll:
		return usageRangeLast7
	case usageRangeLast7:
		return usageRangeLast30
	default:
		return usageRangeAll
	}
}

// label names r for the range row. usageRangeAll reads as "full history"
// rather than "all time" so it never sits directly under the unrelated
// "all time" tab and reads as a duplicate of it.
func (r usageRange) label() string {
	switch r {
	case usageRangeLast7:
		return "last 7 days"
	case usageRangeLast30:
		return "last 30 days"
	default:
		return "full history"
	}
}

// days returns the trailing window size in days, or 0 for usageRangeAll
// (no lower bound).
func (r usageRange) days() int {
	switch r {
	case usageRangeLast7:
		return 7
	case usageRangeLast30:
		return 30
	default:
		return 0
	}
}

// barWindow returns the "tokens per day" bar chart's day count. usageRangeAll
// still plots a bounded recent trend (30 days) rather than every day the
// history spans, which would be unreadable as a bar chart.
func (r usageRange) barWindow() int {
	if d := r.days(); d > 0 {
		return d
	}
	return 30
}

// heatmapWeeks returns the activity heatmap's default week count before the
// caller's terminal-width clamp is applied.
func (r usageRange) heatmapWeeks() int {
	switch r {
	case usageRangeLast7:
		return 2
	case usageRangeLast30:
		return 5
	default:
		return 16
	}
}

// since returns the earliest instant included in r relative to now, or the
// zero Time for usageRangeAll (meaning "no lower bound").
func (r usageRange) since(now time.Time) time.Time {
	if d := r.days(); d > 0 {
		return now.AddDate(0, 0, -(d - 1))
	}
	return time.Time{}
}

// usageTab selects which body /usage's shared header (active config, tab
// row, range row) is followed by. Cycled left/right while the overlay is
// open (see updateOverlay). usageTabActivity is the zero value, so it is
// what a freshly opened overlay shows.
type usageTab int

const (
	usageTabActivity usageTab = iota
	usageTabAllTime
	usageTabModels
)

const usageTabCount = 3

func (t usageTab) next() usageTab { return (t + 1) % usageTabCount }
func (t usageTab) prev() usageTab { return (t + usageTabCount - 1) % usageTabCount }

func (t usageTab) label() string {
	switch t {
	case usageTabAllTime:
		return "all time"
	case usageTabModels:
		return "models"
	default:
		return "activity"
	}
}

// usageOverlayState holds the /usage overlay's cached data and current tab
// and range selection. records/metas are fetched once when the overlay
// opens (see cmdUsage) — openOverlay's render closure is re-invoked on
// every resize and must not perform new I/O — and re-sliced by rangeSel on
// every render, including tab- and range-cycle keypresses.
type usageOverlayState struct {
	active   bool
	records  []history.UsageRecord
	metas    []history.Meta
	tab      usageTab
	rangeSel usageRange
}

type modelTotal struct {
	Model    string
	Prompt   int
	Reply    int
	Requests int
}

func (t modelTotal) total() int { return t.Prompt + t.Reply }

// usageOverlay renders the /usage dashboard: a shared header (active config,
// tab row, range row) followed by the body for m.usageState.tab, scoped to
// m.usageState.rangeSel.
func (m *Model) usageOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("usage") + "\n\n")

	if m.historyDir == "" {
		b.WriteString(m.theme.SystemNote.Render("history saving is disabled (chat.save_history)") + "\n")
		return b.String() + "\n" + m.theme.SystemNote.Render("esc to close")
	}
	allRecords := m.usageState.records
	if len(allRecords) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no usage recorded yet — chat a bit first") + "\n")
		return b.String() + "\n" + m.theme.SystemNote.Render("esc to close")
	}

	now := time.Now()
	ascii := false
	rangeSel := m.usageState.rangeSel
	tab := m.usageState.tab

	// --- Active configuration ----------------------------------------------
	prof, _ := m.activeProfile()
	current := fmt.Sprintf("%s · %s · profile %s · prompt %s", m.prov.Name(), m.model, prof.Name, m.effectivePromptMode())
	if m.template != "" {
		current += " · template " + m.template
	}
	b.WriteString(m.theme.StatusValue.Render(current) + "\n")
	if m.responseCache != nil {
		cs := m.responseCache.Stats()
		b.WriteString(m.theme.StatusBar.Render(fmt.Sprintf("cache %s · %d hits / %d misses this session · %d entries",
			onOff(cs.Enabled), cs.Hits, cs.Misses, cs.Entries)) + "\n")
	}
	b.WriteString("\n")

	// --- Tab and range selectors --------------------------------------------
	b.WriteString(usageTabRow(m.theme, tab) + "\n")
	b.WriteString(usageRangeRow(m.theme, rangeSel) + "\n\n")

	since := rangeSel.since(now)
	records := filterRecordsSince(allRecords, since)
	rangeMetas := filterMetasSince(m.usageState.metas, since)

	switch tab {
	case usageTabAllTime:
		m.writeUsageAllTimeTab(&b, rangeSel, records, allRecords, rangeMetas, m.usageState.metas, now, ascii)
	case usageTabModels:
		m.writeUsageModelsTab(&b, records)
	default:
		m.writeUsageActivityTab(&b, rangeSel, records, now, ascii)
	}

	b.WriteString("\n" + m.theme.SystemNote.Render("esc to close · ← → switch tab · r to cycle range"))
	return b.String()
}

// writeUsageActivityTab renders the activity heatmap for the selected range.
func (m *Model) writeUsageActivityTab(b *strings.Builder, rangeSel usageRange, records []history.UsageRecord, now time.Time, ascii bool) {
	byDay := dayTotals(records)
	weeks := rangeSel.heatmapWeeks()
	if avail := m.width - 8; avail > 0 && avail < weeks {
		weeks = avail
	}
	heat := components.Heatmap(components.HeatmapData{
		Values: byDay, Weeks: weeks, Today: now, ASCII: ascii,
	})
	for _, line := range heat {
		b.WriteString(m.theme.ChartBar.Render(line) + "\n")
	}
}

// writeUsageAllTimeTab renders the "tokens per day" bar chart, the stats
// block scoped to rangeSel (records/rangeMetas), and the all-time-only stats
// block that ignores rangeSel (allRecords/allMetas) — a "longest streak" or
// "largest session" reads as a fixed personal record, not something a date
// filter narrows.
func (m *Model) writeUsageAllTimeTab(b *strings.Builder, rangeSel usageRange, records, allRecords []history.UsageRecord, rangeMetas, allMetas []history.Meta, now time.Time, ascii bool) {
	byDay := dayTotals(records)

	b.WriteString(m.theme.UserLabel.Render("tokens per day") + "\n")
	window := rangeSel.barWindow()
	values := make([]int, window)
	for i := 0; i < window; i++ {
		day := now.AddDate(0, 0, i-window+1)
		values[i] = byDay[day.Format("2006-01-02")]
	}
	xlabels := barChartXLabels(window, now)
	chart := components.BarChart(components.BarChartData{
		Values: values, XLabels: xlabels, Height: 6, ASCII: ascii,
	})
	for _, line := range chart {
		b.WriteString(m.theme.ChartBar.Render(line) + "\n")
	}
	b.WriteString("\n")

	models, grand := aggregateByModel(records)
	activeDays, topDay, topDayTokens, streak := usageSummary(byDay, now)
	favorite := ""
	if len(models) > 0 {
		favorite = models[0].Model
	}
	b.WriteString(m.theme.UserLabel.Render(rangeSel.label()) + "\n")
	summary := [][2]string{
		{"total tokens", components.FormatTokens(grand)},
		{"requests", fmt.Sprintf("%d", len(records))},
		{"sessions saved", fmt.Sprintf("%d", len(rangeMetas))},
		{"favorite model", favorite},
		{"active days", fmt.Sprintf("%d", activeDays)},
		{"most active day", fmt.Sprintf("%s (%s tok)", topDay, components.FormatTokens(topDayTokens))},
		{"current streak", fmt.Sprintf("%d day(s)", streak)},
	}
	for _, row := range summary {
		fmt.Fprintf(b, "  %s %s\n",
			m.theme.StatusBar.Render(fmt.Sprintf("%-16s", row[0])),
			m.theme.StatusValue.Render(row[1]))
	}

	b.WriteString("\n" + m.theme.UserLabel.Render("all-time records") + "\n")
	longest := longestStreak(dayTotals(allRecords))
	largestTokens, largestWhen := largestSession(allMetas)
	allTime := [][2]string{
		{"longest streak", fmt.Sprintf("%d day(s)", longest)},
		{"largest session", largestSessionLabel(largestTokens, largestWhen)},
	}
	for _, row := range allTime {
		fmt.Fprintf(b, "  %s %s\n",
			m.theme.StatusBar.Render(fmt.Sprintf("%-16s", row[0])),
			m.theme.StatusValue.Render(row[1]))
	}
}

// writeUsageModelsTab renders the full per-model breakdown for records
// (already scoped to the selected range), uncapped: this tab is the model
// list's own dedicated screen, so unlike a shared page there is no reason to
// fold the tail into a summary line.
func (m *Model) writeUsageModelsTab(b *strings.Builder, records []history.UsageRecord) {
	models, grand := aggregateByModel(records)
	for i, mt := range models {
		dot := lipgloss.NewStyle().Foreground(modelDotColors[i%len(modelDotColors)]).Render("●")
		pct := 0.0
		if grand > 0 {
			pct = 100 * float64(mt.total()) / float64(grand)
		}
		name := mt.Model
		if r := []rune(name); len(r) > 40 {
			name = string(r[:39]) + "…"
		}
		fmt.Fprintf(b, "  %s %s %s\n", dot,
			m.theme.StatusValue.Render(fmt.Sprintf("%-42s", name)),
			m.theme.StatusBar.Render(fmt.Sprintf("(%.1f%%)", pct)))
		fmt.Fprintf(b, "    %s\n", m.theme.StatusBar.Render(fmt.Sprintf(
			"in: %s · out: %s · %d requests",
			components.FormatTokens(mt.Prompt), components.FormatTokens(mt.Reply), mt.Requests)))
	}
}

// dayTotals aggregates records into a day -> total-tokens map.
func dayTotals(records []history.UsageRecord) map[string]int {
	byDay := map[string]int{}
	for _, d := range history.AggregateByDay(records) {
		byDay[d.Day] = d.TotalTokens()
	}
	return byDay
}

// barChartXLabels picks which columns of a window-wide, right-anchored-on-now
// bar chart get a date label. "Jan 02" is 6 columns wide, and BarChart places
// each label at its raw column index with no collision handling of its own,
// so labels here must be spaced at least that far apart or they touch or
// overlap. A narrow window (usageRangeLast7's 7 columns) only has room for a
// single anchor label; a wide one (30 columns) fits the usual start/mid/end
// three.
func barChartXLabels(window int, now time.Time) map[int]string {
	xlabels := map[int]string{window - 1: now.Format("Jan 02")}
	if window >= 20 {
		xlabels[0] = now.AddDate(0, 0, -(window - 1)).Format("Jan 02")
		xlabels[window/2] = now.AddDate(0, 0, window/2-window+1).Format("Jan 02")
	}
	return xlabels
}

// usageTabRow renders the Activity / All time / Models selector, highlighting
// the current tab.
func usageTabRow(theme styles.Theme, current usageTab) string {
	tabs := []usageTab{usageTabActivity, usageTabAllTime, usageTabModels}
	parts := make([]string, len(tabs))
	for i, t := range tabs {
		if t == current {
			parts[i] = theme.UserLabel.Render(t.label())
		} else {
			parts[i] = theme.StatusBar.Render(t.label())
		}
	}
	return strings.Join(parts, theme.StatusBar.Render(" · "))
}

// usageRangeRow renders the Full history / Last 7 days / Last 30 days
// selector, highlighting the current selection.
func usageRangeRow(theme styles.Theme, current usageRange) string {
	ranges := []usageRange{usageRangeAll, usageRangeLast7, usageRangeLast30}
	parts := make([]string, len(ranges))
	for i, r := range ranges {
		if r == current {
			parts[i] = theme.UserLabel.Render(r.label())
		} else {
			parts[i] = theme.StatusBar.Render(r.label())
		}
	}
	return strings.Join(parts, theme.StatusBar.Render(" · "))
}

// largestSessionLabel formats largestSession's result, or a placeholder when
// there is no saved session to report.
func largestSessionLabel(tokens int, when time.Time) string {
	if tokens == 0 {
		return "—"
	}
	return fmt.Sprintf("%s tok (%s)", components.FormatTokens(tokens), when.Format("Jan 02"))
}

// filterRecordsSince returns the records at or after since, or all of
// records when since is the zero Time (usageRangeAll).
func filterRecordsSince(records []history.UsageRecord, since time.Time) []history.UsageRecord {
	if since.IsZero() {
		return records
	}
	out := make([]history.UsageRecord, 0, len(records))
	for _, r := range records {
		if !r.Time.Before(since) {
			out = append(out, r)
		}
	}
	return out
}

// filterMetasSince returns the session metadata saved at or after since, or
// all of metas when since is the zero Time (usageRangeAll).
func filterMetasSince(metas []history.Meta, since time.Time) []history.Meta {
	if since.IsZero() {
		return metas
	}
	out := make([]history.Meta, 0, len(metas))
	for _, meta := range metas {
		if !meta.SavedAt.Before(since) {
			out = append(out, meta)
		}
	}
	return out
}

// longestStreak returns the longest run of consecutive days with recorded
// usage in byDay. It is computed over all-time data regardless of the
// overlay's selected range, matching how a "longest streak" reads as a
// fixed personal record rather than something a date filter narrows.
func longestStreak(byDay map[string]int) int {
	var days []time.Time
	for k, tokens := range byDay {
		if tokens == 0 {
			continue
		}
		t, err := time.Parse("2006-01-02", k)
		if err != nil {
			continue
		}
		days = append(days, t)
	}
	if len(days) == 0 {
		return 0
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	best, cur := 1, 1
	for i := 1; i < len(days); i++ {
		if days[i].Sub(days[i-1]) == 24*time.Hour {
			cur++
		} else {
			cur = 1
		}
		if cur > best {
			best = cur
		}
	}
	return best
}

// largestSession returns the token total and save time of the single
// largest saved session in metas, by Prompt+Reply tokens. llmtui does not
// record a session's start time, so unlike Claude Code's wall-clock
// "longest session" this reports the biggest conversation by token count.
func largestSession(metas []history.Meta) (tokens int, when time.Time) {
	for _, meta := range metas {
		if meta.Tokens > tokens {
			tokens = meta.Tokens
			when = meta.SavedAt
		}
	}
	return tokens, when
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
