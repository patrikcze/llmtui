package agentverify_test

// Opt-in probe for the contract half of the ambiguous-file-request case in
// docs/architecture/local-llm-evaluation-plan.md (Slice 3). It runs the real
// task-contract stage against a configured OpenAI-compatible endpoint and
// reports, per prompt, whether the model produced a clarification (ASK), an
// executable decomposition, or an output that parks the run.
//
// It never runs in normal CI: set LLMTUI_EVAL_BASE_URL and LLMTUI_EVAL_MODEL
// (optionally LLMTUI_EVAL_API_KEY) to enable it. This is a diagnostic, not a
// pass/fail gate — it logs only bounded outcome metadata and fails only on a
// hard error contacting the endpoint. It never exports the endpoint's raw
// control response, question text, or criteria.

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/provider/openai"
)

func TestContractStageAgainstRealEndpoint(t *testing.T) {
	baseURL := os.Getenv("LLMTUI_EVAL_BASE_URL")
	model := os.Getenv("LLMTUI_EVAL_MODEL")
	if baseURL == "" || model == "" {
		t.Skip("set LLMTUI_EVAL_BASE_URL and LLMTUI_EVAL_MODEL to run the contract-stage evaluation probe")
	}
	p := openai.New("eval", baseURL, os.Getenv("LLMTUI_EVAL_API_KEY"))

	cases := []struct {
		name       string
		task       string
		scenarioID string
		// wantAsk is the documented expectation for the ambiguous prompt;
		// a mismatch is logged, not failed, because the point of the probe is
		// to measure model behaviour.
		wantAsk      bool
		capabilities agentverify.CapabilityCapsule
	}{
		{"ambiguous_reference", "Read the file I mentioned and give me its heading.", "contract/ambiguous_reference", true, agentverify.CapabilityCapsule{
			WorkspaceAccess: true, Available: []string{"read_files", "write_files", "ask_user"},
		}},
		{"named_missing_file", "Read absent.md and summarize it.", "contract/named_missing_file", false, agentverify.CapabilityCapsule{
			WorkspaceAccess: true, Available: []string{"read_files", "write_files", "ask_user"},
		}},
		{"clear_multipart", "Read report.md and write its heading to result.txt.", "contract/omitted_deliverable", false, agentverify.CapabilityCapsule{
			WorkspaceAccess: true, Available: []string{"read_files", "write_files", "ask_user"},
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Logf("scenario=%s model=%s", tc.scenarioID, model)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			out, err := agentverify.EstablishContract(ctx, p,
				agentverify.Config{Model: model, MaxTokens: 12288, Timeout: 2 * time.Minute},
				agentverify.ContractInput{Task: tc.task, Capabilities: tc.capabilities})

			switch {
			case err != nil && errors.Is(err, agent.ErrMalformedControl):
				t.Logf("outcome = PARK (%v)", err)
			case err != nil:
				t.Fatalf("hard error contacting the endpoint: %v", err)
			case out.Contract.NeedsUserInput:
				t.Log("outcome = ASK")
				if !tc.wantAsk {
					t.Logf("NOTE: expected an executable decomposition here")
				}
			default:
				t.Logf("outcome = DECOMPOSE; criteria_count = %d", len(out.Contract.Criteria))
				if tc.wantAsk {
					t.Logf("NOTE: expected a clarifying question here")
				}
			}
		})
	}
}
