package tui

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/eval"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/ollama"
	"github.com/patrikcze/llmtui/internal/provider/openai"
	"github.com/patrikcze/llmtui/internal/tools"
)

type liveAgentCase struct {
	id, request, expectedAction, answer, approval string
	seedFiles                                     map[string]string
}

// TestLiveAgentMatrix runs the real bounded contract -> executor -> tool ->
// verifier loop. It is deliberately skipped without an explicit endpoint and
// model, and every mutation is confined to a fresh temporary directory.
func TestLiveAgentMatrix(t *testing.T) {
	baseURL := os.Getenv("LLMTUI_EVAL_BASE_URL")
	model := os.Getenv("LLMTUI_EVAL_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LLMTUI_EVAL_BASE_URL and LLMTUI_EVAL_MODEL to run the live agent matrix")
	}

	endpointType := strings.ToLower(os.Getenv("LLMTUI_EVAL_ENDPOINT_TYPE"))
	if endpointType == "" {
		endpointType = "openai_compatible"
	}
	var raw provider.Provider
	switch endpointType {
	case "openai_compatible", "lmstudio", "vllm", "llama.cpp":
		raw = openai.New("eval", baseURL, os.Getenv("LLMTUI_EVAL_API_KEY"))
	case "ollama":
		raw = ollama.New(baseURL, ollama.WithName("eval"))
	default:
		t.Fatalf("unsupported endpoint type %q", endpointType)
	}
	defer func() { _ = provider.CloseProvider(raw) }()
	counted := &countingProvider{Provider: raw}

	trials := 5
	if value := os.Getenv("LLMTUI_EVAL_TRIALS"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil || parsed < 1 {
			t.Fatalf("LLMTUI_EVAL_TRIALS must be a positive integer: %q", value)
		}
		trials = parsed
	}
	cases := []liveAgentCase{
		{
			id: "read_known_file", expectedAction: "TOOL_CALL",
			request:   "Read notes.txt and tell me its first line. The file path is exact; do not ask me for another path.",
			seedFiles: map[string]string{"notes.txt": "hello from live fixture\nsecond line\n"},
		},
		{
			id: "ask_missing_path", expectedAction: "ASK",
			request: "Read the file I mentioned and give me its heading. Ask me for the filename before reading anything.",
			answer:  "report.md", seedFiles: map[string]string{"report.md": "# Live fixture\nbody\n"},
		},
		{
			id: "confirm_write", expectedAction: "CONFIRM",
			request:  "Create result.txt containing exactly approved. Ask for confirmation before writing it.",
			approval: "y",
		},
	}

	metadata := eval.Metadata{
		Commit: os.Getenv("LLMTUI_EVAL_COMMIT"), Provider: counted.Name(),
		EndpointType: endpointType, Model: model, ToolSchema: "workspace-tools:v1",
		MaxTokens: 4096, VerifierMode: "always", AssistanceMode: "shadow",
		WarmModel: os.Getenv("LLMTUI_EVAL_WARM") == "true",
	}
	if err := eval.ValidateMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	report := eval.Report{Metadata: metadata}
	for _, fixture := range cases {
		for trial := 1; trial <= trials; trial++ {
			started := time.Now()
			m := newTestModel(t)
			m.prov = counted
			m.model = model
			m.agentOn = true
			m.cfg.Agent.Verifier.Enabled = true
			m.cfg.Agent.Verifier.Mode = "always"
			m.cfg.Agent.Verifier.Timeout = "2m"
			m.cfg.Agent.Verifier.MaxTokens = 4096
			m.cfg.Agent.Verifier.MaxAttempts = 2
			m.cfg.Agent.MaxCycles = 2
			m.cfg.Agent.MaxToolCalls = 8
			m.cfg.Agent.MaxElapsed = "5m"
			m.cfg.Agent.MaxTokens = 20000
			m.toolsOn = true
			m.toolsNative = true
			m.toolRunner = tools.NewRunner(t.TempDir(), 64)
			for name, body := range fixture.seedFiles {
				path := filepath.Join(m.toolRunner.Root(), name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
					t.Fatal(err)
				}
			}

			beforeRequests := counted.requestCount()
			driveLiveAgent(t, m, m.startVerifiedRun(fixture.request, nil), fixture.answer, fixture.approval)
			row := liveAgentTrial(fixture, trial, m, counted.requestCount()-beforeRequests, time.Since(started))
			report.Agent = append(report.Agent, row)
			t.Logf("scenario=%s trial=%d action=%s final=%s tools=%d requests=%d elapsed=%s",
				fixture.id, trial, row.ObservedAction, row.FinalResult, row.ToolCalls, row.ProviderRequests, time.Duration(row.Elapsed).Round(time.Millisecond))
		}
	}

	path := os.Getenv("LLMTUI_EVAL_OUTPUT")
	if path == "" {
		path = filepath.Join(t.TempDir(), "llmtui-live-agent.jsonl")
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create evaluation report: %v", err)
	}
	if err := eval.WriteJSONL(file, report); err != nil {
		_ = file.Close()
		t.Fatalf("write evaluation report: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	t.Logf("live agent matrix: trials=%d report=%s", len(report.Agent), path)
}

type countingProvider struct {
	provider.Provider
	mu       sync.Mutex
	requests int
}

func (p *countingProvider) Chat(ctx context.Context, request provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	p.mu.Lock()
	p.requests++
	p.mu.Unlock()
	return p.Provider.Chat(ctx, request)
}

func (p *countingProvider) requestCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.requests
}

func driveLiveAgent(t *testing.T, m *Model, first tea.Cmd, answer, approval string) {
	t.Helper()
	queue := []tea.Cmd{first}
	for steps := 0; steps < 300; steps++ {
		if len(queue) > 0 {
			cmd := queue[0]
			queue = queue[1:]
			if cmd == nil {
				continue
			}
			msg := cmd()
			if batch, ok := msg.(tea.BatchMsg); ok {
				queue = append(queue, batch...)
				continue
			}
			_, next := m.Update(msg)
			if next != nil {
				queue = append(queue, next)
			}
			continue
		}
		if m.pendingAsk != nil {
			if answer == "" {
				return
			}
			m.input.SetValue(answer)
			_, next := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
			if next != nil {
				queue = append(queue, next)
			}
			answer = ""
			continue
		}
		if len(m.pendingCalls) > 0 {
			if approval == "" {
				return
			}
			code := rune(approval[0])
			_, next := m.Update(tea.KeyPressMsg{Code: code, Text: approval})
			if next != nil {
				queue = append(queue, next)
			}
			approval = ""
			continue
		}
		if !m.agentRunActive() && !m.thinking {
			return
		}
		return
	}
	t.Fatal("live agent driver exceeded 300 bounded events")
}

func liveAgentTrial(fixture liveAgentCase, trial int, m *Model, requests int, elapsed time.Duration) eval.AgentTrial {
	row := eval.AgentTrial{
		Scenario: fixture.id, Trial: trial, ExpectedAction: fixture.expectedAction,
		FinalResult: "unknown", ProviderRequests: requests, Elapsed: elapsed,
	}
	if m.agentLoop == nil || m.agentLoop.run == nil {
		row.ObservedAction = "OTHER"
		return row
	}
	run := m.agentLoop.run
	row.FinalResult = string(run.Status)
	row.PromptTokens = run.PromptTokens
	row.CompletionTokens = run.CompletionTokens
	row.Cycles = len(run.Cycles)
	row.NeedsUserInput = m.agentNeedsUserInput()
	for _, event := range m.toolCallDiagnostics {
		if event.Stage == provider.ToolCallStageRecovery {
			row.RecoveryRequests++
		}
	}
	for _, cycle := range run.Cycles {
		if cycle.Verification != nil {
			row.VerifierVerdict = string(cycle.Verification.Verdict)
		}
		if cycle.Execution == nil {
			continue
		}
		row.ToolCalls += len(cycle.Execution.ToolCalls)
		for _, call := range cycle.Execution.ToolCalls {
			switch call.Status {
			case agent.ActionExecuted:
				row.ToolExecuted++
				if call.Succeeded {
					row.ToolSucceeded++
				} else {
					row.ToolFailed++
				}
			case agent.ActionDenied:
				row.ToolDenied++
			case agent.ActionBlocked:
				row.ToolBlocked++
			default:
				row.ToolUnknown++
			}
			if row.ObservedAction == "" {
				switch {
				case call.Name == tools.ToolAskUser:
					row.ObservedAction = "ASK"
				case call.Name == tools.ToolWriteFile || call.Name == tools.ToolEditFile:
					row.ObservedAction = "CONFIRM"
				default:
					row.ObservedAction = "TOOL_CALL"
				}
			}
		}
	}
	if row.ObservedAction == "" {
		row.ObservedAction = "OTHER"
	}
	row.FalseSuccess = run.Status == agent.DecisionDone && row.ObservedAction != fixture.expectedAction
	return row
}
