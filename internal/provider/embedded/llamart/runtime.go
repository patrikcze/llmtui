// Package llamart implements embedded.Runtime with yzma's pure-Go llama.cpp
// bindings. It intentionally imports only yzma's llama, mtmd, and loader
// packages; yzma's optional downloader and network dependencies are not part
// of llmtui.
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
	"github.com/hybridgroup/yzma/pkg/mtmd"

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
	mu        sync.Mutex
	dir       string
	llamaOnce sync.Once
	llamaErr  error
	mtmdOnce  sync.Once
	mtmdErr   error
}

// Runtime owns one llama.cpp model and context. Calls are serialized by the
// embedded provider; Runtime itself deliberately provides no concurrency
// contract.
type Runtime struct {
	model llama.Model
	lctx  llama.Context
	vocab llama.Vocab
	mem   llama.Memory
	mctx  mtmd.Context

	template  string
	nCtx      int
	batchSize int
	opts      embedded.Options
	kvTokens  []llama.Token
	// kvContaminated is set as soon as mtmd evaluation may have inserted image
	// embeddings. Prefix reuse stays disabled until a later text request has
	// cleared memory and rebuilt a text-only cache.
	kvContaminated bool
	vision         visionNative

	// purego callback slots live for the process lifetime. Install one callback
	// per context and update this flag per request instead of allocating a new
	// callback for every generation.
	abort atomic.Bool
}

// New returns an unloaded llama.cpp runtime.
func New() *Runtime {
	return &Runtime{kvTokens: []llama.Token{}, vision: defaultVisionNative()}
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
	if opts.ModelPath != "" {
		modelInfo, err := os.Stat(opts.ModelPath)
		if err != nil {
			return fmt.Errorf("GGUF model not found at %q: %w", opts.ModelPath, err)
		}
		if modelInfo.IsDir() {
			return fmt.Errorf("GGUF model path %q is a directory", opts.ModelPath)
		}
	}
	if opts.MMProjPath != "" {
		projector, err := os.Stat(opts.MMProjPath)
		if err != nil {
			return fmt.Errorf("vision projector not found at %q: %w", opts.MMProjPath, err)
		}
		if projector.IsDir() {
			return fmt.Errorf("vision projector path %q is a directory; a matching mmproj GGUF file is required", opts.MMProjPath)
		}
		mtmdLibrary := loader.GetLibraryFilename(dir, "mtmd")
		mtmdInfo, err := os.Stat(mtmdLibrary)
		if err != nil {
			return fmt.Errorf("mtmd vision library not found at %q: %w; run scripts/fetch-llama-runtime.sh or see docs/embedded.md", mtmdLibrary, err)
		}
		if mtmdInfo.IsDir() {
			return fmt.Errorf("mtmd vision library path %q is a directory", mtmdLibrary)
		}
	}
	return nil
}

// Load initializes the process-global backend, then loads this runtime's model
// and bounded inference context. llama.cpp's backend remains initialized for
// the process lifetime; Close releases the per-runtime projector, context, and
// model.
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
	nativeLogReset()
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
		return meta, fmt.Errorf("load GGUF model: %w%s", err, nativeLogTail(3))
	}
	loaded := false
	defer func() {
		if loaded {
			return
		}
		var cleanupErrors []error
		if r.mctx != 0 {
			if freeErr := mtmd.Free(r.mctx); freeErr != nil {
				cleanupErrors = append(cleanupErrors, fmt.Errorf("free projector after load failure: %w", freeErr))
			}
			r.mctx = 0
		}
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
	if opts.MMProjPath != "" && nCtx > math.MaxInt32 {
		return meta, fmt.Errorf("vision context_size %d exceeds mtmd's supported maximum %d", nCtx, math.MaxInt32)
	}
	contextParams, batchSize, err := buildContextParams(opts, nCtx)
	if err != nil {
		return meta, err
	}

	emitProgress(progress, fmt.Sprintf("initializing %d-token context …", nCtx))
	lctx, err := llama.InitFromModel(model, contextParams)
	if err != nil {
		return meta, fmt.Errorf(
			"initialize %d-token model context: %w%s; if this is a memory failure, lower context_size, keep swa_full unset, set kv_cache_type: q8_0 (requires flash_attention auto or on), or close other model servers (e.g. LM Studio, Ollama) while the embedded model is loaded",
			nCtx, err, nativeLogTail(3),
		)
	}
	r.lctx = lctx
	mem, err := llama.GetMemory(lctx)
	if err != nil {
		return meta, fmt.Errorf("get model memory: %w", err)
	}
	if opts.MMProjPath != "" {
		if err := initMTMDBackend(dir); err != nil {
			return meta, fmt.Errorf("initialize mtmd vision library from %q: %w", dir, err)
		}
		if opts.BatchSize > math.MaxInt32 {
			return meta, fmt.Errorf("batch_size %d exceeds mtmd's supported maximum %d", opts.BatchSize, math.MaxInt32)
		}
		emitProgress(progress, "loading vision projector …")
		projectorParams := mtmd.ContextParamsDefault()
		projectorParams.UseGPU = opts.GPULayers != 0
		if opts.Threads > 0 {
			projectorParams.Threads = int32(opts.Threads)
		}
		mctx, initErr := mtmd.InitFromFile(opts.MMProjPath, model, projectorParams)
		if initErr != nil {
			return meta, fmt.Errorf("load vision projector %q: %w", opts.MMProjPath, initErr)
		}
		r.mctx = mctx
		if !mtmd.SupportVision(mctx) {
			return meta, fmt.Errorf("projector %q does not provide vision support for model %q", opts.MMProjPath, opts.ModelPath)
		}
		emitProgress(progress, "vision projector ready")
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
	r.kvContaminated = false
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
	emit func(embedded.GenDelta),
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

	if len(req.Messages) == 0 {
		return result, errors.New("chat request has no messages")
	}
	images, err := collectAndValidateImages(req.Messages)
	if err != nil {
		return result, err
	}
	messages := req.Messages
	if len(images) > 0 {
		if r.mctx == 0 {
			return result, errors.New("image messages require providers.<name>.mmproj_path with a projector matching the model")
		}
		marker := mtmd.GetMarker(r.mctx)
		if marker == "" {
			marker = mtmd.DefaultMarker()
		}
		if marker == "" {
			return result, errors.New("mtmd returned an empty image marker")
		}
		messages = injectVisionMarkers(messages, marker)
	}
	toolFormat := req.ToolFormat
	if len(req.Tools) > 0 && toolFormat == "" {
		var ok bool
		toolFormat, ok = embedded.ResolveToolFormat(r.opts.ToolFormat, r.opts.ModelPath)
		if !ok {
			return result, fmt.Errorf("native tool format is unknown for model %q", r.opts.ModelPath)
		}
	}
	messages, err = prepareToolMessages(messages, req.Tools, toolFormat)
	if err != nil {
		return result, err
	}
	rendered, err := renderChatTemplate(r.template, messages, req.Tools, req.Reasoning, applyTemplate)
	if err != nil {
		return result, err
	}
	prompt := rendered.text

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

	multimodal := len(images) > 0
	maxNew := 0
	var nextPosition llama.Pos
	if multimodal {
		visionResult, err := r.evaluateVisionPrompt(ctx, prompt, images, req.MaxTokens, req.Progress)
		if err != nil {
			return result, err
		}
		result.PromptTokens = visionResult.promptTokens
		maxNew = visionResult.maxNew
		nextPosition = visionResult.nextPosition
	} else {
		promptTokens := llama.Tokenize(r.vocab, prompt, true, true)
		if len(promptTokens) == 0 {
			return result, errors.New("chat template produced no prompt tokens")
		}
		maxNew, err = generationBudget(len(promptTokens), req.MaxTokens, r.nCtx)
		if err != nil {
			return result, err
		}
		result.PromptTokens = len(promptTokens)
		pending, err := r.preparePrompt(promptTokens)
		if err != nil {
			return result, err
		}
		if err := r.decodePrompt(ctx, pending, len(promptTokens), req.Progress); err != nil {
			return result, err
		}
	}

	sampler, err := r.newSampler(req)
	if err != nil {
		return result, err
	}
	defer llama.SamplerFree(sampler)

	assembler := &embedded.Assembler{}
	stopScanner := embedded.NewStopScanner(r.opts.Sampling.Stop)
	reasoningRouter := newReasoningRouter(rendered)
	toolRouter := newToolOutputRouter(toolFormat, req.Tools)
	emitRouted := func(text string) {
		for _, delta := range reasoningRouter.Push(text) {
			if delta.Kind == embedded.DeltaReasoning || len(req.Tools) == 0 {
				emit(delta)
				continue
			}
			for _, visible := range toolRouter.Push(delta.Text) {
				emit(embedded.GenDelta{Kind: embedded.DeltaText, Text: visible})
			}
		}
	}
	var batch []llama.Token
	stopped := false

	for range maxNew {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		if len(batch) > 0 {
			if multimodal {
				if err := r.decodeAt(ctx, batch[0], nextPosition); err != nil {
					return result, err
				}
				nextPosition++
			} else {
				if err := r.decode(ctx, batch); err != nil {
					return result, err
				}
				r.kvTokens = append(r.kvTokens, batch...)
			}
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
				emitRouted(out)
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
				emitRouted(out)
			}
			stopped = stop
		}
	}
	if !stopped {
		if tail := stopScanner.Flush(); tail != "" {
			emitRouted(tail)
		}
	}
	for _, delta := range reasoningRouter.Flush() {
		if delta.Kind == embedded.DeltaReasoning || len(req.Tools) == 0 {
			emit(delta)
			continue
		}
		for _, visible := range toolRouter.Push(delta.Text) {
			emit(embedded.GenDelta{Kind: embedded.DeltaText, Text: visible})
		}
	}
	if len(req.Tools) > 0 {
		visible, calls, err := toolRouter.Finish()
		if err != nil {
			return result, err
		}
		for _, text := range visible {
			emit(embedded.GenDelta{Kind: embedded.DeltaText, Text: text})
		}
		result.ToolCalls = calls
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
	mctx := r.mctx
	r.lctx = 0
	r.model = 0
	r.vocab = 0
	r.mem = 0
	r.mctx = 0
	r.template = ""
	r.nCtx = 0
	r.batchSize = 0
	r.kvTokens = []llama.Token{}
	r.kvContaminated = false

	var errs []error
	if mctx != 0 {
		if freeErr := mtmd.Free(mctx); freeErr != nil {
			errs = append(errs, fmt.Errorf("free mtmd projector context: %w", freeErr))
		}
	}
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
	globalBackend.llamaOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				globalBackend.llamaErr = fmt.Errorf("native llama initialization panic: %v", recovered)
			}
		}()
		if err := llama.Load(dir); err != nil {
			globalBackend.llamaErr = err
			return
		}
		// Capture native logs into a bounded in-memory ring instead of
		// silencing them: nothing reaches stderr (which would corrupt the
		// TUI), and load/allocation failures can quote the decisive lines.
		llama.LogSet(nativeLogCallback())
		llama.Init()
	})
	return globalBackend.llamaErr
}

func initMTMDBackend(dir string) error {
	globalBackend.mu.Lock()
	defer globalBackend.mu.Unlock()

	if globalBackend.dir != "" && globalBackend.dir != dir {
		return fmt.Errorf("native libraries are already initialized from %q and cannot load mtmd from %q in the same process", globalBackend.dir, dir)
	}
	globalBackend.dir = dir
	globalBackend.mtmdOnce.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				globalBackend.mtmdErr = fmt.Errorf("native mtmd initialization panic: %v", recovered)
			}
		}()
		if err := mtmd.Load(dir); err != nil {
			globalBackend.mtmdErr = err
			return
		}
		mtmd.LogSet(nativeLogCallback())
	})
	return globalBackend.mtmdErr
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

	kvCacheType, err := embedded.ParseKVCacheType(opts.KVCacheType)
	if err != nil {
		return llama.ContextParams{}, 0, err
	}
	flashAttention, err := embedded.ParseFlashAttention(opts.FlashAttention)
	if err != nil {
		return llama.ContextParams{}, 0, err
	}
	if err := embedded.ValidateKVFlashCombination(kvCacheType, flashAttention); err != nil {
		return llama.ContextParams{}, 0, err
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

	// llama.cpp's C default is swa_full=true (full-size KV for every
	// sliding-window layer, kept for API compatibility upstream). llmtui
	// defaults to the window-sized SWA cache: on Gemma 4 E4B it shrinks
	// 131072-token KV from ~7.2 GiB to ~2.0 GiB, and generate.go already
	// falls back to a full re-decode whenever the SWA cache refuses a
	// partial prefix removal.
	if opts.SWAFull {
		params.SwaFull = 1
	} else {
		params.SwaFull = 0
	}
	if kvCacheType == embedded.KVCacheTypeQ8_0 {
		params.TypeK = llama.GGMLTypeQ8_0
		params.TypeV = llama.GGMLTypeQ8_0
	}
	switch flashAttention {
	case embedded.FlashAttentionOn:
		params.FlashAttentionType = llama.FlashAttentionTypeEnabled
	case embedded.FlashAttentionOff:
		params.FlashAttentionType = llama.FlashAttentionTypeDisabled
	default:
		params.FlashAttentionType = llama.FlashAttentionTypeAuto
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
