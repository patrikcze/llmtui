package eval

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/ollama"
	"github.com/patrikcze/llmtui/internal/provider/openai"
)

// TestLiveEvaluationMatrix is opt-in by design. It runs the same bounded
// synthetic contract and native-tool probe against a configured local
// endpoint, never against a default server, and never routes the probe call
// to a host executor.
func TestLiveEvaluationMatrix(t *testing.T) {
	baseURL := os.Getenv("LLMTUI_EVAL_BASE_URL")
	model := os.Getenv("LLMTUI_EVAL_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LLMTUI_EVAL_BASE_URL and LLMTUI_EVAL_MODEL to run live evaluation")
	}

	endpointType := strings.ToLower(os.Getenv("LLMTUI_EVAL_ENDPOINT_TYPE"))
	if endpointType == "" {
		endpointType = "openai_compatible"
	}
	var p provider.Provider
	switch endpointType {
	case "openai_compatible", "lmstudio", "vllm", "llama.cpp":
		p = openai.New("eval", baseURL, os.Getenv("LLMTUI_EVAL_API_KEY"))
	case "ollama":
		p = ollama.New(baseURL, ollama.WithName("eval"))
	default:
		t.Fatalf("unsupported LLMTUI_EVAL_ENDPOINT_TYPE %q; use openai_compatible or ollama", endpointType)
	}
	defer func() { _ = provider.CloseProvider(p) }()

	trials := 5
	if raw := os.Getenv("LLMTUI_EVAL_TRIALS"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 {
			t.Fatalf("LLMTUI_EVAL_TRIALS must be a positive integer: %q", raw)
		}
		trials = value
	}
	streaming := os.Getenv("LLMTUI_EVAL_STREAM") != "false"

	wantAsk := true
	wantDecompose := false
	workspaceCapabilities := agentverify.CapabilityCapsule{
		WorkspaceAccess: true,
		Available:       []string{"read_files", "write_files", "ask_user"},
	}
	contract := RunContractMatrix(context.Background(), p, model, RunConfig{Trials: trials, MaxTokens: 4096, Timeout: 2 * time.Minute}, []ContractCase{
		{ID: "contract/ambiguous_reference", Task: "Read the file I mentioned and give me its heading.", WantAsk: &wantAsk, Capabilities: workspaceCapabilities},
		{ID: "contract/named_missing_file", Task: "Read absent.md and summarize it.", WantAsk: &wantDecompose, Capabilities: workspaceCapabilities},
		{ID: "contract/clear_multipart", Task: "Read report.md and write its heading to result.txt.", WantAsk: &wantDecompose,
			Capabilities: workspaceCapabilities},
	})
	conformance := RunConformanceMatrix(context.Background(), p, model, streaming, RunConfig{Trials: trials, Timeout: 2 * time.Minute})

	metadata := Metadata{
		Commit: os.Getenv("LLMTUI_EVAL_COMMIT"), Provider: p.Name(),
		EndpointType: endpointType, Model: model, ToolSchema: "conformance_echo:v1",
		MaxTokens: 4096, VerifierMode: "contract-stage", AssistanceMode: "shadow",
		WarmModel: os.Getenv("LLMTUI_EVAL_WARM") == "true",
	}
	if err := ValidateMetadata(metadata); err != nil {
		t.Fatal(err)
	}
	report := Report{Metadata: metadata, Contract: contract, Conformance: conformance}
	report.Summary = SummarizeContract(contract)
	conformanceSummary := SummarizeConformance(conformance)
	report.Summary.Trials = conformanceSummary.Trials
	report.Summary.NativeSuccess = conformanceSummary.NativeSuccess
	report.Summary.Malformed = conformanceSummary.Malformed
	report.Summary.RecoveryRequired = conformanceSummary.RecoveryRequired
	report.Summary.RecoverySucceeded = conformanceSummary.RecoverySucceeded
	report.Summary.CorrelationFailed = conformanceSummary.CorrelationFailed

	path := os.Getenv("LLMTUI_EVAL_OUTPUT")
	ephemeral := false
	if path == "" {
		path = t.TempDir() + "/llmtui-live-evaluation.jsonl"
		ephemeral = true
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatalf("create evaluation report: %v", err)
	}
	if err := WriteJSONL(file, report); err != nil {
		_ = file.Close()
		t.Fatalf("write evaluation report: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if ephemeral {
		t.Logf("live evaluation: contract_trials=%d conformance_trials=%d native_success=%d/%d recovery=%d/%d report=%s (ephemeral; set LLMTUI_EVAL_OUTPUT to retain)",
			len(contract), len(conformance), report.Summary.NativeSuccess, report.Summary.Trials,
			report.Summary.RecoverySucceeded, report.Summary.RecoveryRequired, path)
		return
	}
	t.Logf("live evaluation: contract_trials=%d conformance_trials=%d native_success=%d/%d recovery=%d/%d report=%s",
		len(contract), len(conformance), report.Summary.NativeSuccess, report.Summary.Trials,
		report.Summary.RecoverySucceeded, report.Summary.RecoveryRequired, path)
}
