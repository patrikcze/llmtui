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
		fmt.Println(`{"type":"ready","protocol":2}`)
		os.Exit(0)
	}
	if mode == "dependencies" {
		fmt.Println(`{"type":"error","protocol":1,"code":"dependencies"}`)
		os.Exit(0)
	}
	if mode == "handshake_type" {
		fmt.Println(`{"type":"result","protocol":1}`)
		os.Exit(0)
	}
	if mode == "stderr" {
		fmt.Fprint(os.Stderr, strings.Repeat("x", 128<<10)+"tail marker")
	}
	fmt.Println(`{"type":"ready","protocol":1,"version":"0.2.0"}`)
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
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"type": "result", "protocol": mlxProtocol, "id": req.ID, "result": map[string]any{"answers": answers}})
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
