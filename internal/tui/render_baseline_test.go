package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// TestRenderingBaselines keeps a small set of representative presentation
// states stable while the layout work is introduced in later phases. These
// fixtures intentionally contain normalized plain text: terminal colour
// capability, animation frames, and elapsed wall-clock time are covered by
// their focused tests rather than by golden files.
func TestRenderingBaselines(t *testing.T) {
	tests := []struct {
		name  string
		build func(*testing.T, *Model)
	}{
		{
			name: "conversation",
			build: func(_ *testing.T, m *Model) {
				m.session.AddUser("Explain the renderer contract.")
				m.session.AddMessage(provider.Message{
					Role:      provider.RoleAssistant,
					Content:   "The transcript is sanitized before it is styled.\n\nLong Markdown remains readable.",
					Reasoning: "Check the rendering boundary first.",
				})
			},
		},
		{
			name: "streaming_reasoning",
			build: func(_ *testing.T, m *Model) {
				m.thinking = true
				m.reasoningBuf.WriteString("Inspecting the current frame.")
				m.streamBuf.WriteString("Partial answer from the provider.")
			},
		},
		{
			name: "native_and_fenced_tools",
			build: func(_ *testing.T, m *Model) {
				m.session.AddUser("Inspect both tool protocols.")
				m.session.AddMessage(provider.Message{
					Role:    provider.RoleAssistant,
					Content: "```tool list_dir\ninternal/tui\n```",
					ToolCalls: []provider.ToolCall{
						{ID: "read-ok", Name: "read_file", Arguments: `{"path":"internal/tui/app.go"}`},
						{ID: "write-failed", Name: "write_file", Arguments: `{"path":"theme.go","content":"new"}`},
					},
				})
				m.session.AddMessage(provider.Message{
					Role:       provider.RoleTool,
					ToolCallID: "read-ok",
					ToolName:   "read_file",
					Content:    "package tui",
				})
				m.session.AddMessage(provider.Message{
					Role:       provider.RoleTool,
					ToolCallID: "write-failed",
					ToolName:   "write_file",
					Content:    "error: approval denied",
					Display:    "Update(theme.go) — added 1 line(s), removed 1 line(s)\n- old\n+ new",
				})
			},
		},
		{
			name: "pending_question_and_approval",
			build: func(_ *testing.T, m *Model) {
				m.pendingAsk = &pendingAskUser{call: tools.Call{
					Tool:     tools.ToolAskUser,
					Question: "Which environment should receive the release?",
				}}
				m.pendingCalls = []tools.Call{{
					Tool: tools.ToolWriteFile,
					Path: "deployments/production.yaml",
					Body: "enabled: true\n",
				}}
			},
		},
		{
			name: "long_question_picker",
			build: func(_ *testing.T, m *Model) {
				m.openAgentQuestionPicker(
					"Choose the source with the most useful operational context.",
					[]string{
						"internal/tui/render_baseline_test.go",
						"docs/architecture/package-map.md",
						"the very long provider model identifier used for acceptance testing",
					},
				)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(t)
			m.resize(100, 40)
			tt.build(t, m)
			m.refreshViewport()
			assertRenderingBaseline(t, tt.name, m.viewport.View())
		})
	}
}

func assertRenderingBaseline(t *testing.T, name, got string) {
	t.Helper()

	got = normalizeRenderingBaseline(got)
	path := filepath.Join("testdata", "render_baselines", name+".txt")
	want, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing rendering baseline %q:\n%s", path, got)
		}
		t.Fatalf("read rendering baseline %q: %v", path, err)
	}
	wantText := strings.TrimSuffix(string(want), "\n")
	if got != wantText {
		t.Errorf("rendering baseline %q changed (-want +got):\n- %s\n+ %s", name, wantText, got)
	}
}

func normalizeRenderingBaseline(rendered string) string {
	lines := strings.Split(ansi.Strip(rendered), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}
