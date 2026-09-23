package decision

import (
	"bufio"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/patrikcze/llmtui/internal/procutil"
)

//go:embed mlx_worker.py
var mlxWorker string

const mlxProtocol = 1
const mlxFrameLimit = 8 << 20

// MLXRuntimeLoader executes verified local checkpoints with laya-mlx 0.2.0.
// Python is an executable path (never a shell command); empty uses python3
// from PATH. Discovery and imports happen only on an explicit Probe or Load.
type MLXRuntimeLoader struct{ Python string }

func (MLXRuntimeLoader) SupportsSource(format string) bool { return format == RuntimeFormatMLX }

// MLXRuntimeInfo separates import/model loading from per-prediction timing.
type MLXRuntimeInfo struct {
	Version   string  `json:"version"`
	Python    string  `json:"python"`
	ImportMS  float64 `json:"import_ms"`
	LoadMS    float64 `json:"load_ms"`
	StartupMS float64 `json:"startup_ms"`
}

func (l MLXRuntimeLoader) command() (*exec.Cmd, error) {
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		return nil, fmt.Errorf("%w: MLX requires macOS arm64", ErrUnavailable)
	}
	python := l.Python
	if python == "" {
		python = "python3"
	}
	path, err := exec.LookPath(python)
	if err != nil {
		return nil, fmt.Errorf("%w: Python executable not found; configure decision_engine.laya.mlx_python (Python 3.11+ with laya-mlx==0.2.0)", ErrUnavailable)
	}
	// The process outlives the load request. exchange owns cancellation and
	// Close owns termination; CommandContext tied to Load would kill reuse.
	cmd := exec.Command(path, "-I", "-u", "-c", mlxWorker)
	// Do not pass API keys, HF_TOKEN, or Python injection variables to the worker.
	for _, key := range []string{"HOME", "PATH", "TMPDIR", "SYSTEMROOT"} {
		if value, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+value)
		}
	}
	cmd.Env = append(cmd.Env, "HF_HUB_OFFLINE=1", "HF_HUB_DISABLE_TELEMETRY=1")
	return cmd, nil
}

func (l MLXRuntimeLoader) Probe(ctx context.Context) (MLXRuntimeInfo, error) {
	cmd, err := l.command()
	if err != nil {
		return MLXRuntimeInfo{}, err
	}
	e, err := startMLX(ctx, cmd, mlxRequest{Type: "probe", Protocol: mlxProtocol})
	if err != nil {
		return MLXRuntimeInfo{}, err
	}
	defer func() { _ = e.Close() }()
	return e.info, nil
}

func (l MLXRuntimeLoader) Load(ctx context.Context, installation Installation) (Engine, error) {
	if err := verifyMLXInstallation(ctx, installation); err != nil {
		return nil, err
	}
	return l.loadVerified(ctx, installation)
}

func (l MLXRuntimeLoader) loadVerified(ctx context.Context, installation Installation) (Engine, error) {
	if installation.Manifest.Runtime.Format != RuntimeFormatMLX {
		return nil, fmt.Errorf("%w: MLX loader requires an MLX checkpoint", ErrUnavailable)
	}
	cmd, err := l.command()
	if err != nil {
		return nil, err
	}
	path, err := filepath.Abs(installation.Path)
	if err != nil {
		return nil, err
	}
	return startMLX(ctx, cmd, mlxRequest{Type: "init", Protocol: mlxProtocol, ModelPath: path})
}

type mlxRequest struct {
	Type      string              `json:"type"`
	Protocol  int                 `json:"protocol"`
	ID        uint64              `json:"id"`
	ModelPath string              `json:"model_path,omitempty"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions,omitempty"`
}

type mlxResponse struct {
	Type     string `json:"type"`
	Protocol int    `json:"protocol"`
	ID       uint64 `json:"id"`
	Code     string `json:"code"`
	MLXRuntimeInfo
	Result struct {
		Answers map[string]mlxAnswer `json:"answers"`
	} `json:"result"`
}

type mlxAnswer struct {
	Type          QuestionType       `json:"type"`
	Choice        *string            `json:"choice"`
	Score         *float64           `json:"score"`
	Noul          *float64           `json:"noul"`
	Confidence    *float64           `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities"`
}

// MLXEngine serializes whole request/response exchanges. The cancellable gate
// lets queued callers leave without disrupting an active prediction.
type MLXEngine struct {
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    *os.File
	reader    *bufio.Scanner
	stderr    boundedTail
	gate      chan struct{}
	done      chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
	nextID    uint64         // protected by gate
	info      MLXRuntimeInfo // immutable after handshake
}

func startMLX(ctx context.Context, cmd *exec.Cmd, init mlxRequest) (*MLXEngine, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	started := time.Now()
	e := &MLXEngine{cmd: cmd, gate: make(chan struct{}, 1), done: make(chan struct{}), closed: make(chan struct{})}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("%w: worker stdin: %v", ErrUnavailable, err)
	}
	e.stdin = stdin
	// Own the pipe so Wait cannot close stdout before the response is consumed.
	reader, writer, err := os.Pipe()
	if err != nil {
		_ = stdin.Close()
		return nil, err
	}
	e.stdout = reader
	cmd.Stdout, cmd.Stderr = writer, &e.stderr
	cmd.WaitDelay = 2 * time.Second
	procutil.SetupProcAttr(cmd)
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = reader.Close()
		_ = writer.Close()
		return nil, fmt.Errorf("%w: start MLX worker: %v", ErrUnavailable, err)
	}
	_ = writer.Close()
	if err := procutil.TrackProcess(cmd); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		_ = stdin.Close()
		_ = reader.Close()
		return nil, fmt.Errorf("%w: contain MLX worker: %v", ErrUnavailable, err)
	}
	go func() { _ = cmd.Wait(); close(e.done) }()
	e.reader = bufio.NewScanner(reader)
	e.reader.Buffer(make([]byte, 4096), mlxFrameLimit)
	// Bound imports and model initialization even if the caller has no deadline.
	loadCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	response, err := e.exchange(loadCtx, init)
	if err == nil && (response.Type != "ready" || response.Version != "0.2.0") {
		err = fmt.Errorf("%w: expected MLX ready handshake", ErrUnavailable)
	}
	if err != nil {
		_ = e.Close()
		return nil, &MLXRuntimeError{Err: err, diagnostics: e.Diagnostics()}
	}
	e.info = response.MLXRuntimeInfo
	e.info.StartupMS = float64(time.Since(started).Microseconds())/1000 - e.info.ImportMS - e.info.LoadMS
	return e, nil
}

// MLXRuntimeError retains bounded diagnostics without disclosing them in
// normal error text. Explicit debugging callers may inspect Diagnostics.
type MLXRuntimeError struct {
	Err         error
	diagnostics string
}

func (e *MLXRuntimeError) Error() string       { return e.Err.Error() }
func (e *MLXRuntimeError) Unwrap() error       { return e.Err }
func (e *MLXRuntimeError) Diagnostics() string { return e.diagnostics }

func (e *MLXEngine) usable() bool {
	select {
	case <-e.closed:
		return false
	default:
		return true
	}
}

func (e *MLXEngine) Name() string                { return "laya-mlx" }
func (e *MLXEngine) RuntimeInfo() MLXRuntimeInfo { return e.info }

// Diagnostics returns a bounded stderr tail for explicit debugging only. It
// must never be inserted into a prompt or displayed as trusted terminal text.
func (e *MLXEngine) Diagnostics() string { return e.stderr.String() }

func (e *MLXEngine) Predict(ctx context.Context, state any, questions map[string]Question, _ PredictOptions) (Result, error) {
	if err := Validate(state, questions); err != nil {
		return Result{}, err
	}
	response, err := e.exchange(ctx, mlxRequest{Type: "predict", Protocol: mlxProtocol, State: state, Questions: questions})
	if err != nil {
		return Result{}, &MLXRuntimeError{Err: err, diagnostics: e.Diagnostics()}
	}
	if response.Type != "result" {
		_ = e.Close()
		return Result{}, fmt.Errorf("%w: expected MLX result", ErrUnavailable)
	}
	result, err := convertMLXAnswers(questions, response.Result.Answers)
	if err != nil {
		_ = e.Close()
		return Result{}, fmt.Errorf("%w: invalid MLX result: %v", ErrUnavailable, err)
	}
	return result, nil
}

func (e *MLXEngine) exchange(ctx context.Context, request mlxRequest) (mlxResponse, error) {
	select {
	case e.gate <- struct{}{}:
		defer func() { <-e.gate }()
	case <-ctx.Done():
		return mlxResponse{}, ctx.Err()
	case <-e.closed:
		return mlxResponse{}, ErrUnavailable
	}
	if err := ctx.Err(); err != nil {
		return mlxResponse{}, err
	}
	select {
	case <-e.closed:
		return mlxResponse{}, ErrUnavailable
	default:
	}
	if request.Type == "predict" {
		e.nextID++
		request.ID = e.nextID
	}
	data, err := json.Marshal(request)
	if err != nil {
		return mlxResponse{}, fmt.Errorf("%w: encode MLX request: %v", ErrInvalid, err)
	}
	if len(data)+1 > mlxFrameLimit {
		return mlxResponse{}, fmt.Errorf("%w: MLX request exceeds 8 MiB", ErrInvalid)
	}
	type outcome struct {
		response mlxResponse
		err      error
	}
	finished := make(chan outcome, 1)
	go func() {
		var out outcome
		if _, out.err = e.stdin.Write(append(data, '\n')); out.err == nil {
			if e.reader.Scan() {
				out.err = json.Unmarshal(e.reader.Bytes(), &out.response)
			} else {
				out.err = e.reader.Err()
				if out.err == nil {
					out.err = io.EOF
				}
			}
		}
		finished <- out
	}()
	select {
	case <-ctx.Done():
		_ = e.Close()
		<-finished
		return mlxResponse{}, ctx.Err()
	case out := <-finished:
		if err := ctx.Err(); err != nil {
			_ = e.Close()
			return mlxResponse{}, err
		}
		if out.err != nil {
			_ = e.Close()
			return mlxResponse{}, fmt.Errorf("%w: MLX worker transport failed: %v", ErrUnavailable, out.err)
		}
		if out.response.Protocol != mlxProtocol || out.response.ID != request.ID {
			_ = e.Close()
			return mlxResponse{}, fmt.Errorf("%w: MLX protocol version or response ID mismatch", ErrUnavailable)
		}
		if out.response.Type == "error" {
			if out.response.Code == "request" && request.Type == "predict" {
				return mlxResponse{}, fmt.Errorf("%w: Laya rejected the question format or token budget", ErrInvalid)
			}
			_ = e.Close()
			return mlxResponse{}, mlxWorkerError(out.response.Code)
		}
		return out.response, nil
	}
}

func mlxWorkerError(code string) error {
	reason := "worker failed"
	switch code {
	case "platform":
		reason = "requires native arm64 Python 3.11+ on macOS"
	case "dependencies", "version":
		reason = "requires laya-mlx==0.2.0 in the configured Python environment; see llmtui decision runtime status mlx --help"
	case "metal":
		reason = "Metal is unavailable to Python"
	case "model_path":
		reason = "verified local model directory is missing"
	case "load":
		reason = "could not load local checkpoint (check configuration, weights, and available memory)"
	case "inference":
		reason = "inference failed"
	}
	return fmt.Errorf("%w: MLX %s", ErrUnavailable, reason)
}

func (e *MLXEngine) Close() error {
	e.closeOnce.Do(func() {
		close(e.closed)
		_ = e.stdin.Close()
		// Kill the contained worker, then join its sole Wait owner. Closing our
		// read end also wakes an exchange if a descendant held stdout.
		procutil.KillGroup(e.cmd)
		_ = e.stdout.Close()
		<-e.done
	})
	return nil
}

type boundedTail struct {
	mu   sync.Mutex
	data []byte
}

func (b *boundedTail) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	const limit = 16 << 10
	if len(p) >= limit {
		b.data = append(b.data[:0], p[len(p)-limit:]...)
	} else {
		if excess := len(b.data) + len(p) - limit; excess > 0 {
			b.data = b.data[excess:]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *boundedTail) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
