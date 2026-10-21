package harness

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// Scenarios are trusted, versioned fixtures, never paths or user task text.
func Scenarios() []string {
	return []string{"tool-use", "premature", "recovery", "coverage", "misleading", "unknown", "equivalent"}
}

const fixtureVersion = "fixture-v1"
const fixtureInput = "one\ntwo\nthree\n"
const fixtureOutput = "verified\n"

type Criterion struct {
	ID     string `json:"id"`
	Passed bool   `json:"passed"`
}

// Evidence contains receipts and oracle facts, never raw arguments or outputs.
type Evidence struct {
	ID                   string                 `json:"id"`
	Snapshot             string                 `json:"snapshot"`
	Fixture              string                 `json:"fixture"`
	InitialDigest        string                 `json:"initial_digest"`
	Protocol             string                 `json:"protocol"`
	Criteria             []Criterion            `json:"criteria"`
	Oracle               bool                   `json:"oracle"`
	Passed               bool                   `json:"passed"`
	DeterministicFailure bool                   `json:"deterministic_failure"`
	SemanticClaim        bool                   `json:"semantic_claim"`
	ToolCalls            int                    `json:"tool_calls"`
	ToolErrors           int                    `json:"tool_errors"`
	InvalidCalls         int                    `json:"invalid_calls"`
	Recoveries           int                    `json:"recoveries"`
	RepeatedCalls        int                    `json:"repeated_calls"`
	CoveredLines         int64                  `json:"covered_lines"`
	ArtifactValid        bool                   `json:"artifact_valid"`
	FailedTests          int                    `json:"failed_tests"`
	StopReason           string                 `json:"stop_reason"`
	Elapsed              time.Duration          `json:"elapsed"`
	Receipts             []agent.ToolCallRecord `json:"receipts"`
}

// runFixture has no caller-supplied executor or arbitrary tool dispatcher.
// A fresh generated fixture is the ONLY source of files. Trusted scripts can
// read input.txt/missing.txt and write result.txt; no shell/MCP/env/network is
// reachable. Separate directories alone would not contain a real model loop.
func runFixture(ctx context.Context, scenario, protocol string, snapshot Snapshot) (e Evidence, retErr error) {
	start := time.Now()
	e = Evidence{Snapshot: snapshot.ID, Fixture: fixtureVersion + ":" + scenario, Protocol: protocol, Oracle: scenario != "unknown", SemanticClaim: true}
	if err := ctx.Err(); err != nil {
		return e, err
	}
	if !slices.Contains(Scenarios(), scenario) || (protocol != "native" && protocol != "fenced") {
		return e, errors.New("unsupported fixture or protocol; live probes are unavailable")
	}
	if err := snapshot.Validate(); err != nil {
		return e, err
	}
	dir, err := os.MkdirTemp("", "llmtui-harness-")
	if err != nil {
		return e, err
	}
	defer func() {
		if err := os.RemoveAll(dir); err != nil {
			retErr = errors.Join(retErr, fmt.Errorf("clean fixture: %w", err))
		}
	}()
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), []byte(fixtureInput), 0o600); err != nil {
		return e, err
	}
	e.InitialDigest = hashText(fixtureVersion + fixtureInput)
	runner := tools.NewRunner(dir, 16)
	execution := agent.ExecutionResult{}
	has := func(edit Edit) bool { return slices.Contains(snapshot.Edits, edit) }
	// Scripted model trajectories test policy plumbing, NOT model quality.
	emit := func(tool, path, body string, offset, limit int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.ToolCalls >= 6 {
			return errors.New("fixture tool budget exceeded")
		}
		if (tool != tools.ToolReadFile || (path != "input.txt" && path != "missing.txt")) && (tool != tools.ToolWriteFile || path != "result.txt" || body != fixtureOutput) {
			return errors.New("fixture capability denied")
		}
		var calls []tools.Call
		if protocol == "native" {
			args := fmt.Sprintf(`{"path":%q,"offset":%d,"limit":%d}`, path, offset, limit)
			if tool == tools.ToolWriteFile {
				args = fmt.Sprintf(`{"path":%q,"content":%q}`, path, body)
			}
			calls = tools.CallsFromNative([]provider.ToolCall{{ID: fmt.Sprintf("call_%d", e.ToolCalls), Name: tool, Arguments: args}})
		} else {
			marker := "tool " + tool + " " + path
			if offset > 0 {
				body = fmt.Sprintf(`{"offset":%d,"limit":%d}`, offset, limit)
			}
			calls = tools.Parse("```" + marker + "\n" + strings.TrimSuffix(body, "\n") + "\n```")
		}
		if len(calls) != 1 || calls[0].InputErr != "" {
			e.InvalidCalls++
			return errors.New("fixture normalization failed")
		}
		call := calls[0]
		// Recheck the normalized call at the capability boundary.
		if call.Tool != tool || call.Path != path || (tool == tools.ToolWriteFile && call.Body != fixtureOutput) || (tool == tools.ToolReadFile && (call.Offset != offset || call.Limit != limit)) {
			return errors.New("fixture normalization changed target")
		}
		result := runner.ExecuteContext(ctx, call)
		record := agent.ToolCallRecord{ID: fmt.Sprintf("receipt_%d", e.ToolCalls), Name: tool, Detail: path, Succeeded: result.Err == nil, Status: agent.ActionExecuted}
		e.ToolCalls++
		if result.Err != nil {
			e.ToolErrors++
			record.ErrorKind = agent.ErrorToolExecution
			if result.Meta.Error != nil {
				record.ErrorCode = string(result.Meta.Error.Code)
			}
		}
		execution.ToolCalls = append(execution.ToolCalls, record)
		if result.Meta.Window != nil && path == "input.txt" {
			w := result.Meta.Window
			agent.AppendReadObservation(&execution, agent.ReadObservation{Target: path, SourceDigest: result.Meta.SourceDigest, StartLine: w.StartLine, EndLine: w.EndLine, TotalLines: result.Meta.Coverage.TotalLines})
		}
		return nil
	}
	switch scenario {
	case "tool-use", "premature", "misleading", "unknown", "equivalent":
		corrected := has(ExecuteTools) || has(VerifyArtifact) || has(ArtifactProcedure) || scenario == "equivalent"
		if corrected {
			if err := emit(tools.ToolWriteFile, "result.txt", fixtureOutput, 0, 0); err != nil {
				return e, err
			}
		}
		data, readErr := os.ReadFile(filepath.Join(dir, "result.txt"))
		e.ArtifactValid = readErr == nil && string(data) == fixtureOutput
		e.Criteria = []Criterion{{"artifact_contents", e.ArtifactValid}, {"executed_action", e.ToolCalls > 0}}
		if scenario == "misleading" {
			execution.TestsRun = []agent.TestResult{{Name: "fixture oracle", Passed: false}}
			e.FailedTests = 1
		}
	case "recovery":
		if err := emit(tools.ToolReadFile, "missing.txt", "", 0, 0); err != nil {
			return e, err
		}
		corrected := has(RecoverTyped) || has(FixtureRecovery)
		target := "missing.txt"
		if corrected {
			target = "input.txt"
			e.Recoveries = 1
		} else {
			e.RepeatedCalls = 1
		}
		if err := emit(tools.ToolReadFile, target, "", 0, 0); err != nil {
			return e, err
		}
		// Correction is credited only if a typed failure and a success exist.
		recovered := corrected && execution.ToolCalls[0].ErrorCode != "" && execution.ToolCalls[1].Succeeded
		e.Criteria = []Criterion{{"typed_recovery", recovered}, {"no_blind_repetition", e.RepeatedCalls == 0}}
	case "coverage":
		if err := emit(tools.ToolReadFile, "input.txt", "", 1, 1); err != nil {
			return e, err
		}
		if has(CompleteRead) || has(ReadProcedure) {
			if err := emit(tools.ToolReadFile, "input.txt", "", 2, 2); err != nil {
				return e, err
			}
		}
		covered, known := agent.ReadCoverage("input.txt", execution.ReadObservations)
		e.CoveredLines = covered
		e.Criteria = []Criterion{{"full_versioned_read", known && covered == 3}}
	}
	if _, failed := agent.EvaluateDeterministic(execution); failed {
		e.DeterministicFailure = true
	}
	e.Receipts = execution.ToolCalls
	e.Passed = e.Oracle && !e.DeterministicFailure && e.InvalidCalls == 0
	for _, c := range e.Criteria {
		e.Passed = e.Passed && c.Passed
	}
	e.StopReason = "oracle_failed"
	if !e.Oracle {
		e.StopReason = "no_independent_oracle"
	} else if e.Passed {
		e.StopReason = "oracle_passed"
	}
	e.ID = hashText(e.Snapshot + e.Fixture + protocol)[:24]
	e.Elapsed = time.Since(start)
	return e, ctx.Err()
}
