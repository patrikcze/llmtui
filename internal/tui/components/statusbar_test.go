package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

func statusData() StatusBarData {
	return StatusBarData{
		Provider:     "ollama",
		Model:        "hf.co/empero-ai/Qwythos-9B-Claude-Mythos-5-1M-GGUF:Q4_K_M",
		Connected:    true,
		TotalTokens:  494140,
		LastTPS:      15.2,
		ContextUsed:  5700,
		ContextLimit: 32800,
		Profile:      "auto/qwen",
		PromptMode:   "coding",
		CacheOn:      true,
		ToolsOn:      true,
		WebOn:        true,
	}
}

func TestStatusBarShowsWebIndicator(t *testing.T) {
	out := StatusBar(styles.ByName("mono"), statusData(), 300)
	if !strings.Contains(out, "web") {
		t.Errorf("status bar missing web indicator:\n%s", out)
	}
}

func TestStatusBarSingleLineWhenItFits(t *testing.T) {
	out := StatusBar(styles.ByName("mono"), statusData(), 300)
	if strings.Contains(out, "\n") {
		t.Errorf("expected one line at width 300:\n%s", out)
	}
}

func TestStatusBarWrapsToTwoRowsWhenNarrow(t *testing.T) {
	out := StatusBar(styles.ByName("mono"), statusData(), 100)
	lines := strings.Split(out, "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 rows at width 100, got %d:\n%s", len(lines), out)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w > 100 {
			t.Errorf("row %d overflows: width %d > 100", i, w)
		}
	}
	// The session/speed telemetry must survive the wrap, not be cut off.
	if !strings.Contains(out, "tok/s") {
		t.Errorf("speed lost in wrap:\n%s", out)
	}
}
