package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/history"
)

func TestUsageTabIsActivityByDefault(t *testing.T) {
	var tab usageTab
	if tab != usageTabActivity {
		t.Fatalf("zero value of usageTab = %v, want usageTabActivity (a freshly opened overlay must default to it)", tab)
	}
}

func TestUsageTabCyclingBothDirections(t *testing.T) {
	forward := []usageTab{usageTabActivity, usageTabAllTime, usageTabModels, usageTabActivity}
	for i := 0; i < len(forward)-1; i++ {
		if got := forward[i].next(); got != forward[i+1] {
			t.Errorf("%v.next() = %v, want %v", forward[i], got, forward[i+1])
		}
	}
	backward := []usageTab{usageTabActivity, usageTabModels, usageTabAllTime, usageTabActivity}
	for i := 0; i < len(backward)-1; i++ {
		if got := backward[i].prev(); got != backward[i+1] {
			t.Errorf("%v.prev() = %v, want %v", backward[i], got, backward[i+1])
		}
	}
}

func TestUsageTabLabels(t *testing.T) {
	cases := map[usageTab]string{
		usageTabActivity: "activity",
		usageTabAllTime:  "all time",
		usageTabModels:   "models",
	}
	for tab, want := range cases {
		if got := tab.label(); got != want {
			t.Errorf("%v.label() = %q, want %q", tab, got, want)
		}
	}
}

func TestUsageRangeCycling(t *testing.T) {
	seq := []usageRange{usageRangeAll, usageRangeLast7, usageRangeLast30, usageRangeAll}
	for i := 0; i < len(seq)-1; i++ {
		if got := seq[i].next(); got != seq[i+1] {
			t.Errorf("%v.next() = %v, want %v", seq[i], got, seq[i+1])
		}
	}
}

func TestUsageRangeSince(t *testing.T) {
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if got := usageRangeAll.since(now); !got.IsZero() {
		t.Errorf("usageRangeAll.since() = %v, want zero", got)
	}
	if got := usageRangeLast7.since(now); got != now.AddDate(0, 0, -6) {
		t.Errorf("usageRangeLast7.since() = %v, want 6 days back", got)
	}
	if got := usageRangeLast30.since(now); got != now.AddDate(0, 0, -29) {
		t.Errorf("usageRangeLast30.since() = %v, want 29 days back", got)
	}
}

func TestUsageRangeLabelsAndWindows(t *testing.T) {
	cases := []struct {
		r            usageRange
		label        string
		barWindow    int
		heatmapWeeks int
	}{
		{usageRangeAll, "full history", 30, 16},
		{usageRangeLast7, "last 7 days", 7, 2},
		{usageRangeLast30, "last 30 days", 30, 5},
	}
	for _, c := range cases {
		if got := c.r.label(); got != c.label {
			t.Errorf("%v.label() = %q, want %q", c.r, got, c.label)
		}
		if got := c.r.barWindow(); got != c.barWindow {
			t.Errorf("%v.barWindow() = %d, want %d", c.r, got, c.barWindow)
		}
		if got := c.r.heatmapWeeks(); got != c.heatmapWeeks {
			t.Errorf("%v.heatmapWeeks() = %d, want %d", c.r, got, c.heatmapWeeks)
		}
	}
}

func TestFilterRecordsSince(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	records := []history.UsageRecord{
		{Time: now.AddDate(0, 0, -10), PromptTokens: 1},
		{Time: now.AddDate(0, 0, -3), PromptTokens: 2},
		{Time: now, PromptTokens: 3},
	}
	if got := filterRecordsSince(records, time.Time{}); len(got) != 3 {
		t.Fatalf("zero since should return all records, got %d", len(got))
	}
	got := filterRecordsSince(records, now.AddDate(0, 0, -5))
	if len(got) != 2 {
		t.Fatalf("filterRecordsSince = %d records, want 2", len(got))
	}
	for _, r := range got {
		if r.Time.Before(now.AddDate(0, 0, -5)) {
			t.Errorf("filterRecordsSince kept a record before the cutoff: %v", r.Time)
		}
	}
}

func TestFilterMetasSince(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	metas := []history.Meta{
		{Name: "old", SavedAt: now.AddDate(0, 0, -10)},
		{Name: "recent", SavedAt: now.AddDate(0, 0, -1)},
	}
	if got := filterMetasSince(metas, time.Time{}); len(got) != 2 {
		t.Fatalf("zero since should return all metas, got %d", len(got))
	}
	got := filterMetasSince(metas, now.AddDate(0, 0, -5))
	if len(got) != 1 || got[0].Name != "recent" {
		t.Fatalf("filterMetasSince = %+v, want only 'recent'", got)
	}
}

func TestLongestStreak(t *testing.T) {
	cases := []struct {
		name  string
		byDay map[string]int
		want  int
	}{
		{"empty", map[string]int{}, 0},
		{"zeros only", map[string]int{"2026-09-01": 0, "2026-09-02": 0}, 0},
		{"single day", map[string]int{"2026-09-15": 500}, 1},
		{
			"one run", map[string]int{
				"2026-09-01": 10, "2026-09-02": 20, "2026-09-03": 30,
			}, 3,
		},
		{
			"two runs, keeps the longer", map[string]int{
				"2026-09-01": 10, "2026-09-02": 20, // run of 2
				"2026-09-10": 5, "2026-09-11": 5, "2026-09-12": 5, "2026-09-13": 5, // run of 4
			}, 4,
		},
		{
			"a zero day breaks the run", map[string]int{
				"2026-09-01": 10, "2026-09-02": 0, "2026-09-03": 10,
			}, 1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := longestStreak(c.byDay); got != c.want {
				t.Errorf("longestStreak(%v) = %d, want %d", c.byDay, got, c.want)
			}
		})
	}
}

func TestLargestSession(t *testing.T) {
	when := time.Date(2026, 9, 10, 8, 0, 0, 0, time.UTC)
	metas := []history.Meta{
		{Name: "small", Tokens: 100, SavedAt: when.AddDate(0, 0, -1)},
		{Name: "big", Tokens: 5000, SavedAt: when},
		{Name: "medium", Tokens: 2000, SavedAt: when.AddDate(0, 0, 1)},
	}
	tokens, gotWhen := largestSession(metas)
	if tokens != 5000 || !gotWhen.Equal(when) {
		t.Errorf("largestSession() = (%d, %v), want (5000, %v)", tokens, gotWhen, when)
	}

	if tokens, _ := largestSession(nil); tokens != 0 {
		t.Errorf("largestSession(nil) tokens = %d, want 0", tokens)
	}
}

// TestBarChartXLabelsDoNotOverlap guards a bug caught by manual visual
// review: for a 7-day window (usageRangeLast7.barWindow()), the original
// start/mid/end label placement put three 6-column-wide "Jan 02" labels 3
// columns apart, so BarChart rendered them running together illegibly
// (observed as "SepSepSep 20"). barChartXLabels must keep every pair of
// labels at least 6 columns apart — the label width — for every window size
// usageRange.barWindow() can actually produce (7 and 30 today).
func TestBarChartXLabelsDoNotOverlap(t *testing.T) {
	now := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	for _, window := range []int{usageRangeLast7.barWindow(), usageRangeLast30.barWindow(), usageRangeAll.barWindow()} {
		t.Run(strconv.Itoa(window), func(t *testing.T) {
			labels := barChartXLabels(window, now)
			cols := make([]int, 0, len(labels))
			for col := range labels {
				cols = append(cols, col)
			}
			for i := 0; i < len(cols); i++ {
				for j := i + 1; j < len(cols); j++ {
					gap := cols[j] - cols[i]
					if gap < 0 {
						gap = -gap
					}
					if gap < 6 {
						t.Errorf("window %d: labels at columns %d and %d are only %d apart, want >= 6 (label width)",
							window, cols[i], cols[j], gap)
					}
				}
			}
		})
	}
}

func TestLargestSessionLabelHandlesNoSessions(t *testing.T) {
	if got := largestSessionLabel(0, time.Time{}); got != "—" {
		t.Errorf("largestSessionLabel(0, zero) = %q, want placeholder", got)
	}
}

// TestUsageModelsTabListsEveryModelUncapped covers the Models tab getting
// its own dedicated screen: with more distinct models than the old shared
// page's cap of 10, every one of them must still appear (no folding into a
// "+N more" summary line — that behavior only made sense when the model
// breakdown shared a page with the bar chart and heatmap).
func TestUsageModelsTabListsEveryModelUncapped(t *testing.T) {
	m := newTestModel(t)
	m.historyDir = t.TempDir()
	now := time.Now()
	const modelCount = 13
	for i := range modelCount {
		if err := history.AppendUsage(m.historyDir, history.UsageRecord{
			Time: now, Provider: "mock", Model: modelName(i),
			PromptTokens: 100 - i, CompletionTokens: 10, DurationMS: 10,
		}); err != nil {
			t.Fatal(err)
		}
	}
	typeText(m, "/usage")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Activity is the default tab; "left" from it wraps straight to Models
	// (the last of the three).
	m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if m.usageState.tab != usageTabModels {
		t.Fatalf("tab = %v, want usageTabModels after one 'left' from the default Activity tab", m.usageState.tab)
	}

	content := m.usageOverlay()
	if strings.Contains(content, "more (") {
		t.Errorf("Models tab should never fold models into a '+N more' line:\n%s", content)
	}
	for i := range modelCount {
		if !strings.Contains(content, modelName(i)) {
			t.Errorf("Models tab is missing %q, want every model listed uncapped", modelName(i))
		}
	}
}

func modelName(i int) string {
	return "model-" + string(rune('a'+i))
}
