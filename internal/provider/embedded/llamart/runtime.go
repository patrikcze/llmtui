// Package llamart implements embedded.Runtime with yzma's pure-Go llama.cpp
// bindings. It intentionally imports only github.com/hybridgroup/yzma/pkg/llama
// and pkg/loader; yzma's optional downloader and its network dependencies are
// not part of llmtui.
package llamart

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/hybridgroup/yzma/pkg/loader"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

const (
	defaultContextSize = 8192
	defaultBatchSize   = 512
	allGPULayers       = 999
	tokenPieceBufSize  = 256
)

var globalBackend struct {
	mu   sync.Mutex
	once sync.Once
	dir  string
	err  error
}

// Runtime owns one llama.cpp model and context. Calls are serialized by the
// embedded provider; Runtime itself deliberately provides no concurrency
// contract.
type Runtime struct {
	model llama.Model
	lctx  llama.Context
	vocab llama.Vocab
	mem   llama.Memory

	template  string
	nCtx      int
	batchSize int
	opts      embedded.Options
	kvTokens  []llama.Token

	// purego callback slots live for the process lifetime. Install one callback
	// per context and update this flag per request instead of allocating a new
	// callback for every generation.
	abort atomic.Bool
}

// New returns an unloaded llama.cpp runtime.
func New() *Runtime {
	return &Runtime{kvTokens: []llama.Token{}}
}

// Probe performs stat-only library validation. It never loads native code.
func (r *Runtime) Probe(opts embedded.Options) error {
	dir, err := resolveLibraryDir(opts)
	if err != nil {
		return err
	}

	filename := loader.GetLibraryFilename(dir, "llama")
	info, err := os.Stat(filename)
	if err != nil {
		return fmt.Errorf(
			"llama.cpp library not found at %q: %w; run scripts/fetch-llama-runtime.sh or see docs/embedded.md",
			filename,
			err,
		)
	}
	if info.IsDir() {
		return fmt.Errorf("llama.cpp library path %q is a directory; run scripts/fetch-llama-runtime.sh or see docs/embedded.md", filename)
	}
	return nil
}

// Load initializes the process-global backend, then loads this runtime's model
// and bounded inference context. llama.cpp's backend remains initialized for
// the process lifetime; Close releases only the per-runtime context and model.
func (r *Runtime) Load(
	ctx context.Context,
	opts embedded.Options,
	progress func(string),
) (meta embedded.ModelMeta, err error) {
	defer recoverError("load llama.cpp model", &err)

	if r.model != 0 || r.lctx != 0 {
		return meta, errors.New("llama.cpp runtime is already loaded")
	}
	if opts.ModelPath == "" {
		return meta, errors.New("GGUF model path is empty")
	}
	if opts.ContextSize < 0 {
		return meta, fmt.Errorf("context_size must not be negative: %d", opts.ContextSize)
	}
	if err := validateSampling(opts.Sampling); err != nil {
		return meta, err
	}
	if err := ctx.Err(); err != nil {
		return meta, err
	}
	if err := r.Probe(opts); err != nil {
		return meta, err
	}

	dir, err := resolveLibraryDir(opts)
	if err != nil {
		return meta, err
	}
	if err := initBackend(dir); err != nil {
		return meta, fmt.Errorf("initialize llama.cpp libraries from %q: %w", dir, err)
	}
	if err := ctx.Err(); err != nil {
		return meta, err
	}

	modelParams := llama.ModelDefaultParams()
	gpuLayers, err := gpuLayerCount(opts.GPULayers)
	if err != nil {
		return meta, err
	}
	modelParams.NGpuLayers = gpuLayers

	started := time.Now()
	emitProgress(progress, "loading model weights …")
	model, err := llama.ModelLoadFromFile(opts.ModelPath, modelParams)
	if err != nil {
		return meta, fmt.Errorf("load GGUF model: %w", err)
	}
	loaded := false
	defer func() {
		if loaded {
			return
		}
		var cleanupErrors []error
		if r.lctx != 0 {
			if freeErr := llama.Free(r.lctx); freeErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("free context after load failure: %w", freeErr))
			}
			r.lctx = 0
		}
		if model != 0 {
			if freeErr := llama.ModelFree(model); freeErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("free model after load failure: %w", freeErr))
			}
		}
		err = errors.Join(err, errors.Join(cleanupErrors...))
	}()
	if err := ctx.Err(); err != nil {
		return meta, err
	}
	emitProgress(progress, fmt.Sprintf("model weights loaded in %s", time.Since(started).Round(time.Millisecond)))

	vocab := llama.ModelGetVocab(model)
	if vocab == 0 {
		return meta, errors.New("loaded model has no vocabulary")
	}

	nCtxTrain := int(llama.ModelNCtxTrain(model))
	nCtx := effectiveContextSize(opts.ContextSize, nCtxTrain)
	contextParams, batchSize, err := buildContextParams(opts, nCtx)
	if err != nil {
		return meta, err
	}

	emitProgress(progress, fmt.Sprintf("initializing %d-token context …", nCtx))
	lctx, err := llama.InitFromModel(model, contextParams)
	if err != nil {
		return meta, fmt.Errorf("initialize model context: %w", err)
	}
	r.lctx = lctx
	mem, err := llama.GetMemory(lctx)
	if err != nil {
		return meta, fmt.Errorf("get model memory: %w", err)
	}

	template := opts.ChatTemplate
	if template == "" {
		template = llama.ModelChatTemplate(model, "")
	}
	architecture, _ := llama.ModelMetaValStr(model, "general.architecture")
	sizeBytes, err := uint64ToInt64(llama.ModelSize(model))
	if err != nil {
		return meta, fmt.Errorf("read model size: %w", err)
	}

	meta = embedded.ModelMeta{
		Name:         filepath.Base(opts.ModelPath),
		Architecture: architecture,
		Quantization: llama.FtypeName(llama.ModelFtype(model)),
		NCtxTrain:    nCtxTrain,
		ContextSize:  nCtx,
		SizeBytes:    sizeBytes,
		Parameters:   llama.ModelNParams(model),
		HasTemplate:  template != "",
	}

	llama.SetAbortCallback(lctx, func() bool { return r.abort.Load() })
	opts.Sampling.Stop = slices.Clone(opts.Sampling.Stop)
	r.model = model
	r.vocab = vocab
	r.mem = mem
	r.template = template
	r.nCtx = nCtx
	r.batchSize = batchSize
	r.opts = opts
	r.kvTokens = []llama.Token{}
	loaded = true
	emitProgress(progress, "model ready")
	return meta, nil
}

// Generate formats and tokenizes a chat request, reuses an exact token prefix
// when safe, and streams UTF-8 text until EOG, a configured stop, cancellation,
// or the request token limit.
func (r *Runtime) Generate(
	ctx context.Context,
	req embedded.GenRequest,
	emit func(string),
) (result embedded.GenResult, err error) {
	defer recoverError("generate with llama.cpp", &err)

	if r.model == 0 || r.lctx == 0 || r.vocab == 0 || r.mem == 0 {
		return result, errors.New("llama.cpp runtime is not loaded")
	}
	if err := validateRequestSampling(req); err != nil {
		return result, err
	}
	if r.template == "" {
		return result, errors.New("model has no chat template; set providers.<name>.chat_template")
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}

	messages, err := chatMessages(req.Messages)
	if err != nil {
		return result, err
	}
	prompt, err := applyTemplate(r.template, messages)
	if err != nil {
		return result, err
	}
	promptTokens := llama.Tokenize(r.vocab, prompt, true, true)
	if len(promptTokens) == 0 {
		return result, errors.New("chat template produced no prompt tokens")
	}

	maxNew, err := generationBudget(len(promptTokens), req.MaxTokens, r.nCtx)
	if err != nil {
		return result, err
	}
	result.PromptTokens = len(promptTokens)

	r.abort.Store(false)
	abortDone := make(chan struct{})
	stopAbort := context.AfterFunc(ctx, func() {
		r.abort.Store(true)
		close(abortDone)
	})
	defer func() {
		if !stopAbort() {
			<-abortDone
		}
		r.abort.Store(false)
	}()

	pending, err := r.preparePrompt(promptTokens)
	if err != nil {
		return result, err
	}
	if err := r.decodePrompt(ctx, pending, len(promptTokens), req.Progress); err != nil {
		return result, err
	}

	sampler, err := r.newSampler(req)
	if err != nil {
		return result, err
	}
	defer llama.SamplerFree(sampler)

	assembler := &embedded.Assembler{}
	stopScanner := embedded.NewStopScanner(r.opts.Sampling.Stop)
	var batch []llama.Token
	stopped := false

	for range maxNew {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(batch) > 0 {
			if err := r.decode(ctx, batch); err != nil {
				return result, err
			}
			r.kvTokens = append(r.kvTokens, batch...)
		}

		token := llama.SamplerSample(sampler, r.lctx, -1)
		if token == llama.TokenNull {
			return result, errors.New("llama.cpp sampler returned an invalid token")
		}
		if llama.VocabIsEOG(r.vocab, token) {
			break
		}

		result.CompletionTokens++
		piece, err := tokenPiece(r.vocab, token)
		if err != nil {
			return result, err
		}
		text := assembler.Push(piece)
		if text != "" {
			out, stop := stopScanner.Push(text)
			if out != "" {
				emit(out)
			}
			if stop {
				stopped = true
				break
			}
		}
		batch = []llama.Token{token}
	}

	if !stopped {
		if tail := assembler.Flush(); tail != "" {
			out, stop := stopScanner.Push(tail)
			if out != "" {
				emit(out)
			}
			stopped = stop
		}
	}
	if !stopped {
		if tail := stopScanner.Flush(); tail != "" {
			emit(tail)
		}
	}
	return result, nil
}

// Close releases the per-runtime context and model. The process-global
// backend is intentionally retained because yzma/llama.cpp exposes it as
// process-global state and other embedded Runtime instances may still use it.
func (r *Runtime) Close() (err error) {
	defer recoverError("close llama.cpp runtime", &err)

	r.abort.Store(true)
	lctx := r.lctx
	model := r.model
	r.lctx = 0
	r.model = 0
	r.vocab = 0
	r.mem = 0
	r.template = ""
	r.nCtx = 0
	r.batchSize = 0
	r.kvTokens = []llama.Token{}

	var errs []error
	if lctx != 0 {
		if freeErr := llama.Free(lctx); freeErr != nil {
			errs = append(errs, fmt.Errorf("free llama.cpp context: %w", freeErr))
		}
	}
	if model != 0 {
		if freeErr := llama.ModelFree(model); freeErr != nil {
			errs = append(errs, fmt.Errorf("free llama.cpp model: %w", freeErr))
		}
	}
	return errors.Join(errs...)
}

func resolveLibraryDir(opts embedded.Options) (string, error) {
	dir := opts.LibraryPath
	if dir == "" {
		dir = os.Getenv("YZMA_LIB")
	}
	if dir == "" {
		return "", errors.New("llama.cpp library path is unset; set providers.<name>.library_path or YZMA_LIB, run scripts/fetch-llama-runtime.sh, or see docs/embedded.md")
	}
	return filepath.Clean(dir), nil
}

func initBackend(dir string) error {
	globalBackend.mu.Lock()
	defer globalBackend.mu.Unlock()

	if globalBackend.dir != "" && globalBackend.dir != dir {
		return fmt.Errorf("llama.cpp is already initialized from %q and cannot be reloaded from %q in the same process", globalBackend.dir, dir)
	}
	globalBackend.dir = dir
	globalBackend.once.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				globalBackend.err = fmt.Errorf("native initialization panic: %v", recovered)
			}
		}()
		if err := llama.Load(dir); err != nil {
			globalBackend.err = err
			return
		}
		llama.LogSet(llama.LogSilent())
		llama.Init()
	})
	return globalBackend.err
}

func gpuLayerCount(configured int) (int32, error) {
	if configured == -1 {
		return allGPULayers, nil
	}
	if configured < -1 || configured > math.MaxInt32 {
		return 0, fmt.Errorf("gpu_layers %d is outside the supported range -1..%d", configured, math.MaxInt32)
	}
	return int32(configured), nil
}

func validateSampling(sampling embedded.Sampling) error {
	if sampling.TopK < 0 || sampling.TopK > math.MaxInt32 {
		return fmt.Errorf("sampling.top_k %d is outside the supported range 0..%d", sampling.TopK, math.MaxInt32)
	}
	if sampling.RepeatLastN < 0 || sampling.RepeatLastN > math.MaxInt32 {
		return fmt.Errorf("sampling.repeat_last_n %d is outside the supported range 0..%d", sampling.RepeatLastN, math.MaxInt32)
	}
	for name, value := range map[string]float64{
		"min_p":          sampling.MinP,
		"repeat_penalty": sampling.RepeatPenalty,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("sampling.%s must be finite", name)
		}
	}
	if sampling.MinP < 0 || sampling.MinP > 1 {
		return fmt.Errorf("sampling.min_p must be between 0 and 1: %g", sampling.MinP)
	}
	if sampling.RepeatPenalty < 0 {
		return fmt.Errorf("sampling.repeat_penalty must not be negative: %g", sampling.RepeatPenalty)
	}
	return nil
}

func validateRequestSampling(req embedded.GenRequest) error {
	if math.IsNaN(req.Temperature) || math.IsInf(req.Temperature, 0) {
		return errors.New("temperature must be finite")
	}
	if math.IsNaN(req.TopP) || math.IsInf(req.TopP, 0) {
		return errors.New("top_p must be finite")
	}
	if req.TopP < 0 || req.TopP > 1 {
		return fmt.Errorf("top_p must be between 0 and 1: %g", req.TopP)
	}
	return nil
}

func effectiveContextSize(configured, trained int) int {
	if trained <= 0 {
		trained = defaultContextSize
	}
	if configured > 0 && configured < trained {
		return configured
	}
	if configured > 0 {
		return trained
	}
	if trained < defaultContextSize {
		return trained
	}
	return defaultContextSize
}

func buildContextParams(opts embedded.Options, nCtx int) (llama.ContextParams, int, error) {
	if nCtx <= 0 || uint64(nCtx) > math.MaxUint32 {
		return llama.ContextParams{}, 0, fmt.Errorf("context_size %d is outside the supported range 1..%d", nCtx, uint64(math.MaxUint32))
	}
	if opts.Threads < 0 || opts.Threads > math.MaxInt32 {
		return llama.ContextParams{}, 0, fmt.Errorf("threads %d is outside the supported range 0..%d", opts.Threads, math.MaxInt32)
	}
	if opts.BatchSize < 0 || uint64(opts.BatchSize) > math.MaxUint32 {
		return llama.ContextParams{}, 0, fmt.Errorf("batch_size %d is outside the supported range 0..%d", opts.BatchSize, uint64(math.MaxUint32))
	}

	batchSize := opts.BatchSize
	if batchSize == 0 {
		batchSize = defaultBatchSize
	}
	if batchSize > nCtx {
		batchSize = nCtx
	}

	params := llama.ContextDefaultParams()
	params.NCtx = uint32(nCtx)
	params.NBatch = uint32(batchSize)
	if params.NUbatch > params.NBatch {
		params.NUbatch = params.NBatch
	}
	if opts.Threads > 0 {
		params.NThreads = int32(opts.Threads)
		params.NThreadsBatch = int32(opts.Threads)
	}
	return params, batchSize, nil
}

func uint64ToInt64(value uint64) (int64, error) {
	if value > math.MaxInt64 {
		return 0, fmt.Errorf("value %d exceeds int64", value)
	}
	return int64(value), nil
}

func emitProgress(progress func(string), message string) {
	if progress != nil {
		progress(message)
	}
}

func chatMessages(messages []provider.Message) ([]llama.ChatMessage, error) {
	result := make([]llama.ChatMessage, 0, len(messages))
	for _, message := range messages {
		if len(message.Images) > 0 {
			return nil, errors.New("embedded text inference does not support image messages")
		}
		result = append(result, llama.NewChatMessage(string(message.Role), message.Content))
	}
	if len(result) == 0 {
		return nil, errors.New("chat request has no messages")
	}
	return result, nil
}
