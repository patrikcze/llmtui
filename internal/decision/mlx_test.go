package decision

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// The Go test binary is the fake worker; ordinary CI requires no Python/MLX.
func TestMLXWorkerHelper(t *testing.T) {
	mode := os.Getenv("LLMTUI_FAKE_MLX")
	if mode == "" {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 4096), mlxFrameLimit)
	if !scanner.Scan() {
		os.Exit(1)
	}
	var init mlxRequest
	if json.Unmarshal(scanner.Bytes(), &init) != nil {
		os.Exit(2)
	}
	if mode == "malformed_handshake" {
		fmt.Println("not json")
		os.Exit(0)
	}
	if mode == "protocol" {
		// A stale/mismatched worker reporting an older protocol number than
		// the current mlxProtocol must still be rejected as a mismatch.
		fmt.Println(`{"type":"ready","protocol":1}`)
		os.Exit(0)
	}
	if mode == "dependencies" {
		fmt.Printf(`{"type":"error","protocol":%d,"code":"dependencies"}`+"\n", mlxProtocol)
		os.Exit(0)
	}
	if mode == "handshake_type" {
		fmt.Printf(`{"type":"result","protocol":%d}`+"\n", mlxProtocol)
		os.Exit(0)
	}
	if mode == "stderr" {
		fmt.Fprint(os.Stderr, strings.Repeat("x", 128<<10)+"tail marker")
	}
	fmt.Printf(`{"type":"ready","protocol":%d,"version":"0.2.0"}`+"\n", mlxProtocol)
	if mode == "exit" {
		os.Exit(3)
	}
	for scanner.Scan() {
		var req mlxRequest
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(4)
		}
		if mode == "block" {
			_, _ = os.Stderr.WriteString("prediction started")
			select {}
		}
		if mode == "crash" {
			fmt.Fprintln(os.Stderr, "crash marker")
			os.Exit(5)
		}
		if mode == "malformed" {
			fmt.Println("[")
			continue
		}
		if mode == "wrong_id" {
			req.ID++
		}
		if mode == "strict_reject" {
			_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "error", "protocol": mlxProtocol, "id": req.ID, "code": "capacity"})
			continue
		}
		answers := map[string]any{
			"department": map[string]any{"type": "choice", "choice": "billing", "confidence": 0.8332, "probabilities": map[string]float64{"billing": 0.9554, "technical": 0.0446}},
			"urgency":    map[string]any{"type": "score", "score": 1.4465, "confidence": 0.1588, "probabilities": map[string]float64{"0": 0.0956, "1": 0.3624, "2": 0.542}},
			"refund":     map[string]any{"type": "noul", "noul": 0.8592, "confidence": 0.8592},
		}
		if mode == "missing" {
			delete(answers, "refund")
		}
		if mode == "missing_noul" {
			answers["refund"] = map[string]any{"type": "noul", "confidence": 0.9}
		}
		result := map[string]any{"answers": answers}
		usage := map[string]any{
			"department": map[string]any{"head_tokens": 6, "option_tokens": 8, "state_tokens": 20, "max_len": 512, "state_budget": 494, "head_truncated": false, "options_truncated": false, "state_truncated": false},
			"urgency":    map[string]any{"head_tokens": 5, "option_tokens": 20, "state_tokens": 20, "max_len": 512, "state_budget": 480, "head_truncated": false, "options_truncated": false, "state_truncated": false},
			"refund":     map[string]any{"head_tokens": 4, "option_tokens": 10, "state_tokens": 20, "max_len": 512, "state_budget": 490, "head_truncated": false, "options_truncated": false, "state_truncated": false},
		}
		switch mode {
		case "usage_unknown_question":
			usage["nonexistent"] = map[string]any{"head_tokens": 1, "option_tokens": 1, "state_tokens": 1, "max_len": 512, "state_budget": 500}
		case "usage_negative":
			usage["department"] = map[string]any{"head_tokens": -1, "option_tokens": 8, "state_tokens": 20, "max_len": 512, "state_budget": 494}
		case "usage_over_budget":
			usage["department"] = map[string]any{"head_tokens": 6, "option_tokens": 8, "state_tokens": 600, "max_len": 512, "state_budget": 494}
		case "no_usage":
			usage = nil
		}
		if usage != nil {
			result["input_usage"] = usage
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "protocol": mlxProtocol, "id": req.ID, "result": result})
	}
	os.Exit(0)
}

func fakeMLXCommand(t *testing.T, mode string) *exec.Cmd {
	t.Helper()
	path, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(path, "-test.run=^TestMLXWorkerHelper$")
	cmd.Env = append(os.Environ(), "LLMTUI_FAKE_MLX="+mode)
	return cmd
}
func fakeMLX(t *testing.T, mode string) *MLXEngine {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	e, err := startMLX(ctx, fakeMLXCommand(t, mode), mlxRequest{Type: "init", Protocol: mlxProtocol, ModelPath: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	return e
}
func mlxQuestions() map[string]Question {
	return map[string]Question{
		"department": {Type: QuestionChoice, Criteria: map[string]string{"billing": "payments", "technical": "bugs"}},
		"urgency":    {Type: QuestionScore, Criteria: []string{"not urgent", "soon", "critical"}},
		"refund":     {Type: QuestionNoul},
	}
}

func TestMLXProtocolReuseAndMapping(t *testing.T) {
	e := fakeMLX(t, "ok")
	for range 3 {
		result, err := e.Predict(context.Background(), map[string]any{"body": "refund"}, mlxQuestions(), PredictOptions{})
		if err != nil {
			t.Fatal(err)
		}
		if result.Answers["department"].Choice != "billing" || result.Answers["department"].Probabilities["billing"] != 0.9554 || result.Answers["urgency"].Score != 1.4465 || result.Answers["refund"].Probability != 0.8592 {
			t.Fatalf("mapping recalibrated or lost values: %+v", result)
		}
	}
	if e.nextID != 3 {
		t.Fatalf("requests=%d", e.nextID)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-e.done:
	default:
		t.Fatal("Close did not reap worker")
	}
	if _, err := e.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("closed predict: %v", err)
	}
}

// TestMLXInputUsagePopulatedNonStrict covers Phase 0b: a non-strict predict
// call still receives per-question capacity diagnostics from the worker,
// and existing answer semantics are unaffected.
func TestMLXInputUsagePopulatedNonStrict(t *testing.T) {
	e := fakeMLX(t, "ok")
	result, err := e.Predict(context.Background(), map[string]any{"body": "refund"}, mlxQuestions(), PredictOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.InputUsage) != 3 {
		t.Fatalf("InputUsage entries = %d, want 3", len(result.InputUsage))
	}
	usage, ok := result.InputUsage["department"]
	if !ok || usage.HeadTokens != 6 || usage.MaxLen != 512 || usage.Truncated() {
		t.Fatalf("department usage = %+v, want the fake worker's fixed values untruncated", usage)
	}
}

// TestMLXNoUsageInResponseLeavesInputUsageEmpty covers a backend/older
// worker that never reports usage: Result.InputUsage must stay nil/empty,
// never fabricated, and the answers themselves are unaffected.
func TestMLXNoUsageInResponseLeavesInputUsageEmpty(t *testing.T) {
	e := fakeMLX(t, "no_usage")
	result, err := e.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.InputUsage) != 0 {
		t.Fatalf("InputUsage = %+v, want empty when the worker reports none", result.InputUsage)
	}
	if result.Answers["department"].Choice != "billing" {
		t.Fatalf("answers affected by absent usage: %+v", result.Answers)
	}
}

// TestMLXStrictCapacityRejectionDoesNotKillWorker covers Phase 0b's
// invariant "capacity rejection need not imply a permanently dead worker":
// a strict request the worker rejects for capacity must surface as
// ErrInvalid (a verdict about this request, not the worker's health), and
// the same engine must remain usable for the very next request.
func TestMLXStrictCapacityRejectionDoesNotKillWorker(t *testing.T) {
	e := fakeMLX(t, "strict_reject")
	_, err := e.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{RequireCompleteInput: true})
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("strict rejection error = %v, want ErrInvalid", err)
	}
	select {
	case <-e.done:
		t.Fatal("worker was killed/reaped after a capacity rejection")
	default:
	}
}

// TestMLXMalformedInputUsageRejected covers robustness against a corrupted
// or hand-crafted usage map: it must never be silently trusted for capacity
// accounting, and (matching existing malformed-answer handling such as
// "missing"/"missing_noul") the engine is closed rather than reused with an
// unverified result.
func TestMLXMalformedInputUsageRejected(t *testing.T) {
	for _, mode := range []string{"usage_unknown_question", "usage_negative", "usage_over_budget"} {
		t.Run(mode, func(t *testing.T) {
			e := fakeMLX(t, mode)
			if _, err := e.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error = %v, want ErrUnavailable", err)
			}
			select {
			case <-e.done:
			default:
				t.Fatal("worker not reaped after a malformed usage response")
			}
		})
	}
}

func TestMLXHandshakeFailures(t *testing.T) {
	for _, mode := range []string{"malformed_handshake", "protocol", "dependencies", "handshake_type"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			e, err := startMLX(ctx, fakeMLXCommand(t, mode), mlxRequest{Type: "init", Protocol: mlxProtocol})
			if !errors.Is(err, ErrUnavailable) || e != nil {
				t.Fatalf("engine=%v error=%v", e, err)
			}
		})
	}
}
func TestMLXPredictFailures(t *testing.T) {
	for _, mode := range []string{"malformed", "wrong_id", "crash", "exit", "missing", "missing_noul"} {
		t.Run(mode, func(t *testing.T) {
			e := fakeMLX(t, mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := e.Predict(ctx, nil, mlxQuestions(), PredictOptions{}); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("error=%v", err)
			}
			select {
			case <-e.done:
			default:
				t.Fatal("failed worker not reaped")
			}
		})
	}
}
func TestMLXCancellation(t *testing.T) {
	e := fakeMLX(t, "block")
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() { _, err := e.Predict(ctx, nil, mlxQuestions(), PredictOptions{}); finished <- err }()
	// Observe the fake worker accepting the request; no timing sleeps.
	deadline := time.After(5 * time.Second)
	for !strings.Contains(e.Diagnostics(), "prediction started") {
		select {
		case err := <-finished:
			t.Fatalf("early prediction return: %v", err)
		case <-deadline:
			t.Fatal("worker did not receive request")
		default:
			runtime.Gosched()
		}
	}
	cancel()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v", err)
	}
	if _, err := e.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("reused poisoned worker: %v", err)
	}
}
func TestMLXConcurrentPredictionsAndQueuedCancellation(t *testing.T) {
	e := fakeMLX(t, "ok")
	e.gate <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Predict(ctx, nil, mlxQuestions(), PredictOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	<-e.gate
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := e.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
}
func TestMLXStderrAndBrokenPipe(t *testing.T) {
	e := fakeMLX(t, "stderr")
	_ = e.stdin.Close()
	if _, err := e.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{}); !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	// Failed exchange has closed/reaped the worker and joined stderr draining.
	if tail := e.Diagnostics(); len(tail) > 16<<10 || !strings.HasSuffix(tail, "tail marker") {
		t.Fatalf("stderr tail size=%d", len(tail))
	}
}
func TestMLXStartupAndDiscoveryFailures(t *testing.T) {
	_, err := startMLX(context.Background(), exec.Command(filepath.Join(t.TempDir(), "missing")), mlxRequest{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	_, err = (MLXRuntimeLoader{Python: filepath.Join(t.TempDir(), "missing")}).Probe(context.Background())
	if !errors.Is(err, ErrUnavailable) {
		t.Fatal(err)
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		if !strings.Contains(err.Error(), "macOS arm64") {
			t.Fatal(err)
		}
	}
}
