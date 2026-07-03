package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

func TestFormatWallDuration(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0s"},
		{-3 * time.Second, "0s"},
		{49 * time.Second, "49s"},
		{12*time.Minute + 49*time.Second, "12m 49s"},
		{2*time.Hour + 5*time.Second, "2h 0m 5s"},
	}
	for _, c := range cases {
		if got := formatWallDuration(c.in); got != c.want {
			t.Errorf("formatWallDuration(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGroupThousands(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{136, "136"},
		{1000, "1 000"},
		{19687, "19 687"},
		{1234567, "1 234 567"},
		{-19687, "-19 687"},
	}
	for _, c := range cases {
		if got := groupThousands(c.in); got != c.want {
			t.Errorf("groupThousands(%d) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestWrapName(t *testing.T) {
	got := wrapName("abcdefghij", 4)
	want := []string{"abcd", "efgh", "ij"}
	if len(got) != len(want) {
		t.Fatalf("wrapName chunks = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("chunk %d = %q, want %q", i, got[i], want[i])
		}
	}
	if got := wrapName("short", 40); len(got) != 1 || got[0] != "short" {
		t.Errorf("wrapName(short) = %v", got)
	}
}

func TestRecordModelUsageAggregates(t *testing.T) {
	m := newTestModel(t)
	m.recordModelUsage("ollama", "qwen3", 100, 20, false, 2*time.Second)
	m.recordModelUsage("ollama", "qwen3", 50, 10, true, time.Second)
	m.recordModelUsage("lmstudio", "phi-4", 30, 5, false, time.Second)

	if len(m.modelStats) != 2 {
		t.Fatalf("modelStats = %d entries, want 2", len(m.modelStats))
	}
	first := m.modelStats[0]
	if first.Requests != 2 || first.Prompt != 150 || first.Reply != 30 || !first.Estimated {
		t.Errorf("aggregated stat = %+v", first)
	}
	if m.apiTime != 4*time.Second {
		t.Errorf("apiTime = %v, want 4s", m.apiTime)
	}
}

func TestRenderExitSummaryContents(t *testing.T) {
	out := renderExitSummary(styles.ClaudeInspired(), exitSummaryData{
		SessionID:   "session-20260703-101500",
		Saved:       true,
		UserMsgs:    3,
		ReplyMsgs:   2,
		CacheOn:     true,
		CacheHits:   1,
		CacheMisses: 2,
		WallTime:    12*time.Minute + 49*time.Second,
		APITime:     3*time.Minute + 50*time.Second,
		Models: []modelUsageStat{
			{Provider: "ollama", Model: "qwen3:latest", Requests: 2, Prompt: 19687, Reply: 136},
		},
		Width: 100,
	})

	for _, want := range []string{
		"Goodbye!",
		"Interaction Summary",
		"session-20260703-101500",
		"3 sent · 2 replies",
		"1 hits · 2 misses",
		"Performance",
		"12m 49s",
		"3m 50s",
		"(29.9% of session)",
		"Model Usage",
		"ollama/qwen3:latest",
		"19 687",
		"136",
		"llmtui history",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q\n%s", want, out)
		}
	}
}

func TestRenderExitSummaryZeroState(t *testing.T) {
	out := renderExitSummary(styles.ClaudeInspired(), exitSummaryData{
		SessionID: "session-x",
		WallTime:  5 * time.Second,
	})
	if !strings.Contains(out, "no completed requests this session") {
		t.Errorf("zero state missing placeholder:\n%s", out)
	}
	if strings.Contains(out, "llmtui history") {
		t.Errorf("unsaved session should not print the history hint:\n%s", out)
	}
	if strings.Contains(out, "Cache:") {
		t.Errorf("cache line should be hidden when cache is off:\n%s", out)
	}
}

func TestRenderExitSummaryEstimatedMarker(t *testing.T) {
	out := renderExitSummary(styles.ClaudeInspired(), exitSummaryData{
		SessionID: "session-x",
		Models: []modelUsageStat{
			{Provider: "ollama", Model: "qwen3", Requests: 1, Prompt: 10, Reply: 5, Estimated: true},
		},
		Width: 100,
	})
	if !strings.Contains(out, "~10") || !strings.Contains(out, "token counts are estimated") {
		t.Errorf("estimated marker missing:\n%s", out)
	}
}

func TestRenderExitSummaryWrapsLongModelNames(t *testing.T) {
	long := "hf.co/example/A-Very-Long-Model-Name-That-Never-Ends-9B-GGUF:Q4_K_M"
	out := renderExitSummary(styles.ClaudeInspired(), exitSummaryData{
		SessionID: "session-x",
		Models: []modelUsageStat{
			{Provider: "lmstudio", Model: long, Requests: 1, Prompt: 10, Reply: 5},
		},
		Width: 80,
	})
	// The full name must survive wrapping across table rows.
	joined := strings.Join(strings.Fields(strings.ReplaceAll(out, "\n", "")), "")
	if !strings.Contains(joined, "lmstudio/hf.co/example") {
		t.Errorf("wrapped model name mangled:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := len([]rune(line)); w > 80 {
			t.Errorf("line exceeds width 80 (%d): %q", w, line)
		}
	}
}

func TestExitSummarySnapshot(t *testing.T) {
	m := newTestModel(t)
	m.sentCount = 4
	m.replyCount = 3
	m.savedPath = "/tmp/x.json"
	m.recordModelUsage("mock", "demo-model", 100, 50, false, time.Second)

	d := m.exitSummary()
	if d.SessionID != m.sessionName {
		t.Errorf("SessionID = %q, want %q", d.SessionID, m.sessionName)
	}
	if !d.Saved || d.UserMsgs != 4 || d.ReplyMsgs != 3 {
		t.Errorf("snapshot = %+v", d)
	}
	if len(d.Models) != 1 || d.APITime != time.Second {
		t.Errorf("model stats snapshot = %+v api=%v", d.Models, d.APITime)
	}
	if d.WallTime <= 0 {
		t.Errorf("WallTime = %v, want > 0", d.WallTime)
	}
}
