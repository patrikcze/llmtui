package tui

import (
	"fmt"
	"strconv"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

// plainAssistantContent and markdownAssistantContent give the two shapes the
// plan's Phase 6 gate asks to distinguish: a short plain reply versus a
// heavier Markdown answer (headings, a list, a fenced code block) that
// exercises the Glamour render path in renderMarkdown.
const plainAssistantContent = "The transcript is sanitized before it is styled, and long Markdown remains readable across resizes."

const markdownAssistantContent = `## Summary

The renderer keeps three responsibilities separate:

- Sanitization happens before styling.
- Settled Markdown goes through Glamour once per refresh.
- Streaming deltas render as plain text until they settle.

` + "```go\nfunc refreshViewport() {\n\t// settled history plus a live tail\n}\n```" + `

See [render.go](internal/tui/render.go) for the composition entry point.
`

// buildBenchSession populates a model's session with n alternating
// user/assistant messages, so BenchmarkRefreshViewport can measure
// whole-history reassembly cost at realistic transcript sizes.
func buildBenchSession(m *Model, n int, markdownHeavy bool) {
	content := plainAssistantContent
	if markdownHeavy {
		content = markdownAssistantContent
	}
	for i := 0; i < n; i++ {
		m.session.AddUser("Message " + strconv.Itoa(i) + ": what does this component do?")
		m.session.AddMessage(provider.Message{
			Role:      provider.RoleAssistant,
			Content:   content,
			Reasoning: "Checked the relevant file before answering.",
		})
	}
}

// BenchmarkRefreshViewport measures refreshViewport's cost — the function
// called on every streaming delta — at the transcript sizes and content
// shapes the Phase 6 plan calls out (10/100/500 messages; plain vs
// Markdown-heavy), establishing the baseline the plan requires before any
// caching is added. Run with:
//
//	go test ./internal/tui/... -run '^$' -bench RefreshViewport -benchmem
func BenchmarkRefreshViewport(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		for _, md := range []bool{false, true} {
			shape := "plain"
			if md {
				shape = "markdown"
			}
			b.Run(fmt.Sprintf("messages=%d/%s", n, shape), func(b *testing.B) {
				m := newTestModel(b)
				m.cfg.UI.Markdown = md
				buildBenchSession(m, n, md)
				m.refreshViewport()
				b.ResetTimer()
				b.ReportAllocs()
				for i := 0; i < b.N; i++ {
					m.refreshViewport()
				}
			})
		}
	}
}

// BenchmarkRefreshViewportStreamingDelta measures the case that actually
// happens once per token/chunk while a reasoning or answer stream is
// active: a whole-history reassembly triggered by one small delta to the
// live tail, with a large settled history that has not changed at all.
func BenchmarkRefreshViewportStreamingDelta(b *testing.B) {
	for _, n := range []int{10, 100, 500} {
		b.Run(fmt.Sprintf("settled=%d", n), func(b *testing.B) {
			m := newTestModel(b)
			m.cfg.UI.Markdown = true
			buildBenchSession(m, n, true)
			m.thinking = true
			m.refreshViewport()
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				m.streamBuf.WriteString("x")
				m.refreshViewport()
			}
		})
	}
}
