package llamart

// Diagnostic measurement for the 2026-09-30 agent latency audit
// (.claude/tasks/plans/llmtui-agent-improvement-plan.md). It replays request
// sequences captured by internal/tui TestAgentAuditTrace through the real
// llama.cpp runtime and reports, per request, prompt tokens, tokens reused
// from the KV cache, newly evaluated tokens, and wall time. It never runs in
// the ordinary suite:
//
//	LLMTUI_AGENT_AUDIT_TRACE=1 LLMTUI_AGENT_AUDIT_DUMP=<dir> go test ./internal/tui -run TestAgentAuditTrace -count=1
//	LLMTUI_AGENT_AUDIT_REPLAY=<dir> YZMA_LIB=<runtime dir> LLMTUI_TEST_GGUF=<model.gguf> \
//	  go test ./internal/provider/embedded/llamart -run TestAgentAuditPrefillReplay -v -count=1 -timeout 60m

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

type auditReplayRequest struct {
	Kind     string              `json:"kind"`
	Messages []provider.Message  `json:"messages"`
	Tools    []provider.ToolSpec `json:"tools,omitempty"`
}

type auditReplayRow struct {
	kind                 string
	prompt, reused, eval int
	elapsed              time.Duration
}

func auditReplaySequence(t *testing.T, rt *Runtime, opts embedded.Options, seq []auditReplayRequest) ([]auditReplayRow, time.Duration) {
	t.Helper()
	// Start every trial from an unrelated cached prompt, as after a previous task.
	if _, err := rt.Generate(context.Background(), embedded.GenRequest{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "Reply with OK."}},
		MaxTokens: 1,
	}, func(embedded.GenDelta) {}); err != nil {
		t.Fatalf("reset generation: %v", err)
	}
	rows := make([]auditReplayRow, 0, len(seq))
	var total time.Duration
	for _, req := range seq {
		gen := embedded.GenRequest{Messages: req.Messages, Tools: req.Tools, MaxTokens: 1, Temperature: 0, TopP: 1}
		if len(req.Tools) > 0 {
			gen.ToolFormat, _ = embedded.ResolveToolFormat(opts.ToolFormat, opts.ModelPath)
		}
		before := slices.Clone(rt.kvTokens)
		started := time.Now()
		result, err := rt.Generate(context.Background(), gen, func(embedded.GenDelta) {})
		elapsed := time.Since(started)
		if err != nil {
			// A one-token cap can cut off an opening tool call, which the
			// parser rejects after prefill already happened. The KV cache
			// then holds prompt+1 token; that is the measurement we want.
			if !strings.Contains(err.Error(), "tool call") {
				t.Fatalf("%s request: %v", req.Kind, err)
			}
			result.PromptTokens = max(len(rt.kvTokens)-1, 0)
		}
		reused := min(commonPrefix(before, rt.kvTokens), result.PromptTokens)
		rows = append(rows, auditReplayRow{kind: req.Kind, prompt: result.PromptTokens, reused: reused, eval: result.PromptTokens - reused, elapsed: elapsed})
		total += elapsed
	}
	return rows, total
}

func TestAgentAuditPrefillReplay(t *testing.T) {
	dir := os.Getenv("LLMTUI_AGENT_AUDIT_REPLAY")
	if dir == "" {
		t.Skip("diagnostic replay; set LLMTUI_AGENT_AUDIT_REPLAY to a TestAgentAuditTrace dump directory")
	}
	opts := integrationOptions(t)
	opts.ContextSize = 16384
	trials := 3
	if v, err := strconv.Atoi(os.Getenv("LLMTUI_AGENT_AUDIT_TRIALS")); err == nil && v > 0 {
		trials = v
	}
	rt := New()
	loadStarted := time.Now()
	meta, err := rt.Load(context.Background(), opts, func(string) {})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	t.Logf("model=%s load=%s ctx=%d", meta.Name, time.Since(loadStarted).Round(time.Millisecond), opts.ContextSize)

	// Decode speed, measured once on a short prompt.
	decodeStarted := time.Now()
	decoded, err := rt.Generate(context.Background(), embedded.GenRequest{
		Messages:  []provider.Message{{Role: provider.RoleUser, Content: "Count from 1 to 200 separated by spaces."}},
		MaxTokens: 128, Temperature: 0, TopP: 1,
	}, func(embedded.GenDelta) {})
	if err != nil {
		t.Fatalf("decode probe: %v", err)
	}
	t.Logf("decode probe: %d prompt + %d completion tokens in %s", decoded.PromptTokens, decoded.CompletionTokens, time.Since(decodeStarted).Round(time.Millisecond))

	files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
	sort.Strings(files)
	filter := os.Getenv("LLMTUI_AGENT_AUDIT_FILTER")
	for _, file := range files {
		if filter != "" && !strings.Contains(filepath.Base(file), filter) {
			continue
		}
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		var seq []auditReplayRequest
		if err := json.Unmarshal(data, &seq); err != nil {
			t.Fatal(err)
		}
		if os.Getenv("LLMTUI_AGENT_AUDIT_DECODE") == "1" && !strings.Contains(file, "_STABLE") {
			// Full reply cost of the control requests: prefill plus decode of
			// the JSON reply. Unconstrained (no grammar), so the reply length
			// is the model's own, not the schema-forced minimum.
			for _, req := range seq {
				if req.Kind == "executor" {
					continue
				}
				started := time.Now()
				result, err := rt.Generate(context.Background(), embedded.GenRequest{Messages: req.Messages, MaxTokens: 512, Temperature: 0, TopP: 1}, func(embedded.GenDelta) {})
				if err != nil {
					t.Fatalf("%s decode: %v", req.Kind, err)
				}
				t.Logf("%s control-decode %-8s prompt=%d completion=%d wall=%s", filepath.Base(file), req.Kind, result.PromptTokens, result.CompletionTokens, time.Since(started).Round(time.Millisecond))
			}
		}
		var executorOnly []auditReplayRequest
		for _, req := range seq {
			if req.Kind == "executor" {
				executorOnly = append(executorOnly, req)
			}
		}
		for _, variant := range []struct {
			name string
			seq  []auditReplayRequest
		}{{"as-issued", seq}, {"executor-only", executorOnly}} {
			if variant.name == "executor-only" && strings.Contains(file, "_STABLE") {
				continue // the counterfactual only needs the as-issued sequence
			}
			var totals []time.Duration
			var last []auditReplayRow
			for range trials {
				rows, total := auditReplaySequence(t, rt, opts, variant.seq)
				totals = append(totals, total)
				last = rows
			}
			slices.Sort(totals)
			var b strings.Builder
			prompt, eval := 0, 0
			for i, row := range last {
				prompt += row.prompt
				eval += row.eval
				fmt.Fprintf(&b, "\n  #%d %-8s prompt=%-5d reused=%-5d evaluated=%-5d %s", i+1, row.kind, row.prompt, row.reused, row.eval, row.elapsed.Round(time.Millisecond))
			}
			t.Logf("%s [%s] requests=%d prompt_tokens=%d evaluated_tokens=%d wall median=%s min=%s max=%s (n=%d)%s",
				filepath.Base(file), variant.name, len(variant.seq), prompt, eval,
				totals[len(totals)/2].Round(time.Millisecond), totals[0].Round(time.Millisecond), totals[len(totals)-1].Round(time.Millisecond), trials, b.String())
		}
	}
}
