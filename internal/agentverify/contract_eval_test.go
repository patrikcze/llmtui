package agentverify_test

// Opt-in probe for the contract half of the ambiguous-file-request case in
// docs/architecture/local-llm-evaluation-plan.md (Slice 3). It runs the real
// task-contract stage against a configured OpenAI-compatible endpoint and
// reports, per prompt, whether the model produced a clarification (ASK), an
// executable decomposition, or an output that parks the run.
//
// It never runs in normal CI: set LLMTUI_EVAL_BASE_URL and LLMTUI_EVAL_MODEL
// (optionally LLMTUI_EVAL_API_KEY) to enable it. This is a diagnostic, not a
// pass/fail gate — it logs outcomes and only fails on a hard error contacting
// the endpoint.

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
		name string
		task string
		// wantAsk is the documented expectation for the ambiguous prompts;
		// a mismatch is logged, not failed, because the point of the probe is
		// to measure model behaviour.
		wantAsk bool
	}{
		{"ambiguous_reference", "Read the file I mentioned and give me its heading.", true},
		{"named_missing_file", "Read absent.md and summarize it.", true},
		{"clear_multipart", "Read report.md and give me its heading and the percentage change.", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			out, err := agentverify.EstablishContract(ctx, p,
				agentverify.Config{Model: model, MaxTokens: 12288, Timeout: 2 * time.Minute},
				agentverify.ContractInput{Task: tc.task})

			switch {
			case err != nil && errors.Is(err, agent.ErrMalformedControl):
				t.Logf("outcome = PARK (%v); raw = %s", err, out.Raw)
			case err != nil:
				t.Fatalf("hard error contacting the endpoint: %v", err)
			case out.Contract.NeedsUserInput:
				t.Logf("outcome = ASK; question = %q; options = %v", out.Contract.Question, out.Contract.UserOptions)
				if !tc.wantAsk {
					t.Logf("NOTE: expected an executable decomposition here")
				}
			default:
				t.Logf("outcome = DECOMPOSE; criteria = %v", out.Contract.Criteria)
				if tc.wantAsk {
					t.Logf("NOTE: expected a clarifying question here")
				}
			}
		})
	}
}
