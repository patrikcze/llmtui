package agentverify

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
)

type recordingClient struct {
	mu       sync.Mutex
	requests []provider.ChatRequest
	reply    string
	err      error
	block    bool
}

func (c *recordingClient) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	c.mu.Lock()
	c.requests = append(c.requests, req)
	c.mu.Unlock()
	if c.err != nil {
		return nil, c.err
	}
	events := make(chan provider.ChatEvent, 2)
	go func() {
		defer close(events)
		if c.block {
			<-ctx.Done()
			provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: ctx.Err()})
			return
		}
		events <- provider.ChatEvent{Type: provider.EventDelta, Delta: c.reply}
		events <- provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{TotalTokens: 10}}
	}()
	return events, nil
}

func validReply(verdict string) string {
	return `{"verdict":"` + verdict + `","summary":"checked evidence","retryable":false,"confidence":0.8}`
}

func TestVerifierUsesFreshIsolatedContext(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	input := Input{RunID: "r", Cycle: 2, Task: "original task", Objective: "bounded work", Execution: agent.ExecutionResult{Summary: "done"}}
	out, err := Verify(context.Background(), client, Config{Model: "local", Timeout: time.Second}, input)
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Verdict != agent.VerificationPassed {
		t.Fatalf("result = %+v", out.Result)
	}
	if len(client.requests) != 1 {
		t.Fatalf("requests = %d", len(client.requests))
	}
	req := client.requests[0]
	if len(req.Messages) != 2 || req.Messages[0].Role != provider.RoleSystem || req.Messages[1].Role != provider.RoleUser {
		t.Fatalf("messages = %+v", req.Messages)
	}
	if len(req.Tools) != 0 || req.Stream || req.Reasoning != "off" || req.Temperature != 0 {
		t.Fatalf("request = %+v", req)
	}
	if strings.Contains(req.Messages[1].Content, "unrelated conversation history") {
		t.Fatal("verifier received executor conversation history")
	}
}

func TestMalformedControlOutput(t *testing.T) {
	for _, raw := range []string{"not json", `{"verdict":"maybe","summary":"x"}`, `{"verdict":"passed"}`, validReply("passed") + validReply("failed")} {
		if _, err := Parse(raw); !errors.Is(err, agent.ErrMalformedControl) {
			t.Errorf("Parse(%q) error = %v", raw, err)
		}
	}
	result, err := Parse("```json\n" + validReply("passed") + "\n```")
	if err != nil || result.Verdict != agent.VerificationPassed {
		t.Fatalf("fenced parse = %+v, %v", result, err)
	}
}

func TestDeterministicFailureOverridesOptimisticModel(t *testing.T) {
	client := &recordingClient{reply: validReply("passed")}
	exec := agent.ExecutionResult{
		Summary:  "looks good",
		TestsRun: []agent.TestResult{{Name: "go test ./...", Passed: false, Summary: "failed"}},
	}
	out, err := Verify(context.Background(), client, Config{Timeout: time.Second}, Input{Task: "fix", Objective: "test", Execution: exec})
	if err != nil {
		t.Fatal(err)
	}
	if out.Result.Verdict != agent.VerificationFailed || !out.Result.Retryable {
		t.Fatalf("result = %+v", out.Result)
	}
}

func TestVerifierTimeoutAndCancellation(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		client := &recordingClient{block: true}
		_, err := Verify(context.Background(), client, Config{Timeout: 10 * time.Millisecond}, Input{Task: "x"})
		var runErr agent.RunError
		if !errors.As(err, &runErr) || runErr.Kind != agent.ErrorTimeout {
			t.Fatalf("error = %#v", err)
		}
	})

	t.Run("cancellation", func(t *testing.T) {
		client := &recordingClient{block: true}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := Verify(ctx, client, Config{Timeout: time.Second}, Input{Task: "x"})
		var runErr agent.RunError
		if !errors.As(err, &runErr) || runErr.Kind != agent.ErrorCancelled {
			t.Fatalf("error = %#v", err)
		}
	})
}
