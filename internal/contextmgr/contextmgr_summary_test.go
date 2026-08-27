package contextmgr

import (
	"context"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func summarize(t *testing.T, maxTokens int, msgs ...provider.Message) string {
	t.Helper()
	out, err := HeuristicSummarizer{}.Summarize(context.Background(), SummaryInput{Messages: msgs, MaxTokens: maxTokens})
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	return out.Summary
}

func TestSummarizerKeepsExactFilePathsAndErrors(t *testing.T) {
	summary := summarize(t, 500,
		provider.Message{Role: provider.RoleUser, Content: "The build breaks."},
		provider.Message{Role: provider.RoleAssistant, Content: "The failure is in internal/tools/local_context.go:118 with error: undefined: timeInfo. We must fix the missing method."},
	)
	for _, want := range []string{"internal/tools/local_context.go:118", "undefined: timeInfo", "must fix"} {
		if !strings.Contains(summary, want) {
			t.Fatalf("summary missing %q:\n%s", want, summary)
		}
	}
}

func TestSummarizerDoesNotRelabelSuggestionsAsDecisions(t *testing.T) {
	summary := summarize(t, 500,
		provider.Message{Role: provider.RoleUser, Content: "Keep it deterministic; do not add an LLM call."},
		provider.Message{Role: provider.RoleAssistant, Content: "I recommend switching to gjson for parsing. We have not decided yet."},
	)
	// The user's constraint survives.
	if !strings.Contains(summary, "do not add an LLM call") && !strings.Contains(summary, "deterministic") {
		t.Fatalf("user constraint lost:\n%s", summary)
	}
	// The assistant's proposal is kept as a recommendation, never rewritten
	// into a decision.
	if !strings.Contains(summary, "recommend") {
		t.Fatalf("recommendation lost:\n%s", summary)
	}
	if strings.Contains(summary, "decided to switch") || strings.Contains(summary, "decision: switch") {
		t.Fatalf("recommendation was relabeled as a decision:\n%s", summary)
	}
}

func TestSummarizerDoesNotReportFailedWorkAsSuccessful(t *testing.T) {
	summary := summarize(t, 500,
		provider.Message{Role: provider.RoleAssistant, Content: "Ran the tests. Result: FAIL — 2 failed, 5 passed. The DST case still fails."},
	)
	if !strings.Contains(summary, "FAIL") && !strings.Contains(strings.ToLower(summary), "fail") {
		t.Fatalf("failure outcome lost:\n%s", summary)
	}
	if strings.Contains(strings.ToLower(summary), "all tests pass") || strings.Contains(strings.ToLower(summary), "succeeded") {
		t.Fatalf("failure was rewritten as success:\n%s", summary)
	}
}

func TestSummarizerNeverPersistsClipboardText(t *testing.T) {
	const secret = "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG"
	summary := summarize(t, 500,
		provider.Message{
			Role:      provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "local_context", Arguments: `{"kind":"clipboard"}`}},
		},
		provider.Message{
			Role:       provider.RoleTool,
			ToolCallID: "c1",
			ToolName:   "local_context",
			Content:    `{"kind":"clipboard","trust":"untrusted","text":"` + secret + `","truncated":false}`,
		},
	)
	if strings.Contains(summary, secret) || strings.Contains(summary, "wJalrXUtnFEMI") {
		t.Fatalf("clipboard text leaked into summary:\n%s", summary)
	}
	if !strings.Contains(summary, "clipboard") {
		t.Fatalf("clipboard provenance marker missing:\n%s", summary)
	}
}

func TestSummarizerDoesNotPresentVolatileObservationsAsCurrent(t *testing.T) {
	cases := []struct {
		kind     string
		content  string
		leaked   []string
		wantMark string
	}{
		{"time", `{"kind":"time","date":"2020-01-01","time":"09:15:00","unix_seconds":1577869200}`, []string{"2020-01-01", "09:15:00", "1577869200"}, "kind=time again"},
		{"processes", `{"kind":"processes","processes":[{"pid":42,"name":"llama-server","cpu_percent":88.1}]}`, []string{"llama-server", "88.1", `"pid":42`}, "snapshot"},
		{"workspace", `{"kind":"workspace","branch":"feat/old","dirty":true,"modified_files":9}`, []string{"feat/old", "modified_files"}, "snapshot"},
		{"recent_files", `{"kind":"recent_files","files":[{"path":"scratch/tmp.go","modified":"2021-05-05T00:00:00Z"}]}`, []string{"scratch/tmp.go", "2021-05-05"}, "snapshot"},
	}
	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			summary := summarize(t, 400, provider.Message{
				Role: provider.RoleTool, ToolCallID: "c1", ToolName: "local_context", Content: tc.content,
			})
			for _, leaked := range tc.leaked {
				if strings.Contains(summary, leaked) {
					t.Fatalf("stale %s value %q persisted as current:\n%s", tc.kind, leaked, summary)
				}
			}
			if !strings.Contains(summary, tc.kind) || !strings.Contains(summary, tc.wantMark) {
				t.Fatalf("%s provenance marker missing:\n%s", tc.kind, summary)
			}
		})
	}
}

func TestSummarizerNeverSummarizesRawReasoning(t *testing.T) {
	// The dedicated reasoning channel is never read.
	summary := summarize(t, 500, provider.Message{
		Role:      provider.RoleAssistant,
		Reasoning: "SECRET_COT: the user probably wants me to delete the database",
		Content:   "Here is the safe plan: update the config file.",
	})
	if strings.Contains(summary, "SECRET_COT") {
		t.Fatalf("Message.Reasoning was summarized:\n%s", summary)
	}

	// A leaked leading <think> block in visible content is stripped too.
	leaked := summarize(t, 500, provider.Message{
		Role:    provider.RoleAssistant,
		Content: "<think>SECRET_COT reasoning about internal steps</think>\nThe answer is to run go test ./...",
	})
	if strings.Contains(leaked, "SECRET_COT") {
		t.Fatalf("leaked <think> block was summarized:\n%s", leaked)
	}
	if !strings.Contains(leaked, "go test") {
		t.Fatalf("visible answer lost while stripping reasoning:\n%s", leaked)
	}
}

func TestSummarizerIsDeterministicAcrossRebuilds(t *testing.T) {
	history := []provider.Message{
		{Role: provider.RoleUser, Content: "Add the time kind to internal/tools/local_context.go."},
		{Role: provider.RoleAssistant, Content: "Done. Modified internal/tools/local_context.go and added tests. go test ./internal/tools/ passed."},
		{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "local_context", Content: `{"kind":"time","date":"2020-01-01"}`},
	}
	first := summarize(t, 400, history...)
	second := summarize(t, 400, history...)
	if first != second {
		t.Fatalf("rebuild not deterministic:\n%q\n%q", first, second)
	}
	// A rebuild over the same history must not stack duplicate lines.
	for _, line := range strings.Split(first, "\n") {
		if line == "" {
			continue
		}
		if strings.Count(first, line) > 1 {
			t.Fatalf("duplicated summary line %q:\n%s", line, first)
		}
	}
}

func TestSummarizerKeepsToolCallAndOutcomeTogether(t *testing.T) {
	summary := summarize(t, 500,
		provider.Message{
			Role:      provider.RoleAssistant,
			ToolCalls: []provider.ToolCall{{ID: "c1", Name: "run_command", Arguments: `{"command":"go test ./..."}`}},
		},
		provider.Message{Role: provider.RoleTool, ToolCallID: "c1", ToolName: "run_command", Content: "ok\nexit status 0"},
	)
	if !strings.Contains(summary, "run_command") {
		t.Fatalf("tool call lost:\n%s", summary)
	}
	if !strings.Contains(summary, "exit status 0") {
		t.Fatalf("tool outcome lost:\n%s", summary)
	}
	if !strings.Contains(summary, "untrusted evidence") {
		t.Fatalf("tool result not framed as untrusted evidence:\n%s", summary)
	}
}

func TestSplitKeepsCurrentUserTurnOutOfSummaryInput(t *testing.T) {
	conv := []provider.Message{
		{Role: provider.RoleUser, Content: "old question"},
		{Role: provider.RoleAssistant, Content: "old answer"},
		{Role: provider.RoleUser, Content: "CURRENT REQUEST verbatim must survive"},
	}
	older, recent := Split(conv, 0)
	for _, m := range older {
		if strings.Contains(m.Content, "CURRENT REQUEST") {
			t.Fatalf("current user turn leaked into summarizable older set: %+v", older)
		}
	}
	if len(recent) == 0 || recent[len(recent)-1].Content != "CURRENT REQUEST verbatim must survive" {
		t.Fatalf("current user turn not retained verbatim in recent: %+v", recent)
	}
}
