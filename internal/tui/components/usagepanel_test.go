package components

import (
	"strconv"
	"strings"
	"testing"

	"charm.land/lipgloss/v2"

	"github.com/patrikcze/llmtui/internal/tui/styles"
)

func TestUsagePanelHonorsAssignedWidth(t *testing.T) {
	theme := styles.ByName("mono")
	for _, width := range []int{60, 80, 100, 120, 160, 200} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			out := UsagePanel(theme, UsagePanelData{
				TokenHistory: []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10},
				PromptTotal:  1234,
				ReplyTotal:   5678,
			}, width)
			for row, line := range strings.Split(out, "\n") {
				if got := lipgloss.Width(line); got != width {
					t.Errorf("row %d width = %d, want %d", row, got, width)
				}
			}
		})
	}
}

func TestUsagePanelDenseSparklineDoesNotWrap(t *testing.T) {
	theme := styles.ByName("mono")
	values := make([]int, 200)
	for i := range values {
		values[i] = i + 1
	}
	out := UsagePanel(theme, UsagePanelData{TokenHistory: values}, 80)
	if got := lipgloss.Height(out); got != 4 {
		t.Fatalf("panel height = %d, want 4; dense sparkline wrapped:\n%s", got, out)
	}
}
