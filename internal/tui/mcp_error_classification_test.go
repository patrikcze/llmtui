package tui

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/mcp"
	"github.com/patrikcze/llmtui/internal/tools"
)

func TestMCPContextErrorsReachAgentRecords(t *testing.T) {
	executionErr := errors.New("transport unavailable")
	tests := []struct {
		name     string
		cancel   bool
		deadline bool
		delay    time.Duration
		wantErr  error
		wantKind agent.ErrorKind
	}{
		{
			name: "parent cancelled", cancel: true, delay: time.Hour,
			wantErr: context.Canceled, wantKind: agent.ErrorCancelled,
		},
		{
			name: "parent deadline", deadline: true, delay: time.Hour,
			wantErr: context.DeadlineExceeded, wantKind: agent.ErrorTimeout,
		},
		{
			name: "server timeout", delay: time.Hour,
			wantErr: context.DeadlineExceeded, wantKind: agent.ErrorTimeout,
		},
		{
			name:    "execution failure",
			wantErr: executionErr, wantKind: agent.ErrorToolExecution,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result tools.Result
			synctest.Test(t, func(t *testing.T) {
				factory := func(c mcp.ServerConfig) (mcp.Client, error) {
					return &mcp.MockClient{
						ServerName: c.Name, Delay: tt.delay,
						CallFunc: func(string, json.RawMessage) (mcp.Result, error) {
							return mcp.Result{}, executionErr
						},
					}, nil
				}
				reg := mcp.NewRegistry([]mcp.ServerConfig{{
					Name: "test", Transport: mcp.TransportStdio,
					Command: "mock", Enabled: true, Timeout: time.Second,
				}}, factory)
				defer reg.Close()
				if err := reg.Connect(t.Context(), "test"); err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if tt.cancel {
					cancel()
				}
				if tt.deadline {
					var cancelDeadline context.CancelFunc
					ctx, cancelDeadline = context.WithTimeout(ctx, 10*time.Millisecond)
					defer cancelDeadline()
				}
				result = executeMCPCall(ctx, reg, tools.Call{
					ID: "call-1", Tool: "mcp__test__read", MCPServer: "test", MCPTool: "read", MCPArgs: `{}`,
				}, 0)
			})
			if !errors.Is(result.Err, tt.wantErr) {
				t.Errorf("error = %v, want wrapped %v", result.Err, tt.wantErr)
			}

			m := newTestModel(t)
			run := newAgentVerificationTestRun(t, m, "read a remote file", nil, agent.ExecutionResult{})
			m.recordAgentToolResultsCount([]tools.Result{result}, false, 1)
			execution := m.agentLoop.execution
			if len(execution.ToolCalls) != 1 || len(execution.Errors) != 1 {
				t.Fatalf("execution = %+v, want one failed call and error", execution)
			}
			call := execution.ToolCalls[0]
			if call.ID != "call-1" || call.Succeeded || call.ErrorKind != tt.wantKind {
				t.Errorf("call record = %+v, want correlated failure of kind %s", call, tt.wantKind)
			}
			if execution.Errors[0].Kind != tt.wantKind {
				t.Errorf("run error kind = %s, want %s", execution.Errors[0].Kind, tt.wantKind)
			}
			if tt.cancel {
				// Even an optimistic semantic verdict must not turn cancellation
				// into completion or another executor attempt.
				run.LatestCycle().Execution = &execution
				run.LatestCycle().Verification = &agent.VerificationResult{Verdict: agent.VerificationPassed}
				if got := agent.Decide(run, time.Now()).Decision; got != agent.DecisionCancelled {
					t.Errorf("stop decision = %s, want cancelled", got)
				}
			}
		})
	}
}
