package embedded

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/patrikcze/llmtui/internal/provider"
)

// state is the Provider's lifecycle stage.
type state int

const (
	stateUnloaded state = iota
	stateReady
	stateClosed
)

// Provider is an in-process, llama.cpp-backed provider. It implements
// provider.Provider, provider.CapabilityReporter, provider.Closer, and
// provider.RuntimeFingerprinter. All native work is delegated to a Runtime,
// so this type contains no native code itself.
//
// The active model is mutable state: a ChatRequest whose Model names a
// different .gguf file on disk (e.g. a sibling picked in the TUI's model
// picker) switches the provider to that file — the current runtime is
// closed, a fresh one is created via newRuntime, and the new model is
// loaded on the same request.
type Provider struct {
	name       string
	opts       Options // static config; ModelPath is only the *initial* model
	newRuntime func() Runtime

	mu         sync.Mutex // guards st, meta, activePath, rt
	st         state
	meta       ModelMeta
	haveM      bool
	activePath string  // model file the provider currently targets
	rt         Runtime // current runtime instance (replaced on model switch)

	genMu chan struct{} // 1-slot lock serializing generations and switches

	closeOnce   sync.Once
	closeCtx    context.Context
	cancelClose context.CancelFunc
}

// New creates an embedded Provider. It does nothing heavy: no file I/O, no
// native calls. name is the configured provider name (as it would appear in
// the status bar / cache attribution); opts configures the initial model
// and runtime settings; newRuntime constructs Runtime instances (a mock in
// tests, an llama.cpp-backed implementation in the real build) — one is
// created immediately for HealthCheck probing, and a fresh one replaces it
// on every model switch.
func New(name string, opts Options, newRuntime func() Runtime) *Provider {
	closeCtx, cancel := context.WithCancel(context.Background())
	return &Provider{
		name:        name,
		opts:        opts,
		newRuntime:  newRuntime,
		rt:          newRuntime(),
		activePath:  opts.ModelPath,
		genMu:       make(chan struct{}, 1),
		closeCtx:    closeCtx,
		cancelClose: cancel,
	}
}

// activeOptions returns an Options copy whose ModelPath is the currently
// active model file.
func (p *Provider) activeOptions() Options {
	p.mu.Lock()
	defer p.mu.Unlock()
	opts := p.opts
	opts.ModelPath = p.activePath
	return opts
}

// expandHome resolves a leading "~/" against the user's home directory. On
// any resolution failure the path is returned unchanged (the subsequent
// stat produces the actionable error).
func expandHome(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// Name returns the configured provider name.
func (p *Provider) Name() string { return p.name }

// HealthCheck performs cheap, stat-level validation only. It never loads
// the model: the TUI's startup health check has a tight (4s) budget that
// falls back to offline demo mode, so anything slower than a stat call is
// unsafe here.
func (p *Provider) HealthCheck(ctx context.Context) error {
	opts := p.activeOptions()
	if opts.ModelPath == "" {
		return fmt.Errorf("embedded provider %q has no model configured — set providers.%s.model_path in your config, or pass --model /path/to/model.gguf", p.name, p.name)
	}
	info, err := os.Stat(opts.ModelPath)
	if err != nil {
		return fmt.Errorf("embedded provider %q: model file not found at %q: %w", p.name, opts.ModelPath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("embedded provider %q: %q is a directory — a single .gguf model file is required", p.name, opts.ModelPath)
	}
	p.mu.Lock()
	rt := p.rt
	p.mu.Unlock()
	if err := rt.Probe(opts); err != nil {
		return fmt.Errorf("embedded provider %q: runtime unavailable: %w", p.name, err)
	}
	return nil
}

// ListModels returns the active model plus sibling *.gguf files found in
// the same directory, sorted and deduplicated. A missing or unreadable
// directory is not an error: it just yields the active entry alone.
func (p *Provider) ListModels(ctx context.Context) ([]provider.ModelInfo, error) {
	p.mu.Lock()
	active := p.activePath
	p.mu.Unlock()

	configured := provider.ModelInfo{
		ID:         active,
		Name:       filepath.Base(active),
		ContextLen: p.effectiveContextLen(),
	}

	seen := map[string]bool{active: true}
	models := []provider.ModelInfo{configured}

	if active == "" {
		return models, nil
	}
	dir := filepath.Dir(active)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return models, nil
	}

	var siblings []string
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".gguf" {
			continue
		}
		abs := filepath.Join(dir, e.Name())
		if seen[abs] {
			continue
		}
		seen[abs] = true
		siblings = append(siblings, abs)
	}
	sort.Strings(siblings)
	for _, abs := range siblings {
		models = append(models, provider.ModelInfo{
			ID:   abs,
			Name: filepath.Base(abs),
		})
	}
	return models, nil
}

// effectiveContextLen returns the loaded model's effective context (the
// smaller of the configured ContextSize and the model's trained context) if
// a model is loaded, else the configured ContextSize alone.
func (p *Provider) effectiveContextLen() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.haveM {
		return p.opts.ContextSize
	}
	if p.meta.ContextSize > 0 {
		return p.meta.ContextSize
	}
	if p.opts.ContextSize > 0 && p.opts.ContextSize < p.meta.NCtxTrain {
		return p.opts.ContextSize
	}
	return p.meta.NCtxTrain
}

// Capabilities describes the embedded provider. It is honest about lacking
// native tool support.
func (p *Provider) Capabilities() provider.Capabilities {
	return provider.Capabilities{
		SupportsStreaming:    true,
		SupportsModelList:    true,
		SupportsTokenUsage:   true,
		SupportsSystemPrompt: true,
		ContextWindowTokens:  p.effectiveContextLen(),
	}
}

// Chat runs one completion. Native tool calls are not supported: if the
// request declares tools, Chat fails synchronously with a message the TUI's
// tool-fallback detector recognizes, so the session degrades to the
// prompt-based tool protocol automatically.
func (p *Provider) Chat(ctx context.Context, req provider.ChatRequest) (<-chan provider.ChatEvent, error) {
	if len(req.Tools) > 0 {
		return nil, fmt.Errorf("embedded provider %q does not support native tool calls — llmtui falls back to the prompt-based tool protocol", p.name)
	}

	p.mu.Lock()
	closed := p.st == stateClosed
	p.mu.Unlock()
	if closed {
		return nil, fmt.Errorf("embedded provider %q is closed", p.name)
	}

	events := make(chan provider.ChatEvent)
	go p.generate(ctx, req, events)
	return events, nil
}

func (p *Provider) generate(ctx context.Context, req provider.ChatRequest, events chan<- provider.ChatEvent) {
	defer close(events)

	// genCtx is canceled either by the caller's ctx or by Close(). A watcher
	// goroutine links closeCtx into genCtx; cancelGen() (deferred, runs
	// first) guarantees genCtx.Done fires before we wait for the watcher to
	// exit, so this can never deadlock on a normal, uncanceled completion.
	genCtx, cancelGen := context.WithCancel(ctx)
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		select {
		case <-p.closeCtx.Done():
			cancelGen()
		case <-genCtx.Done():
		}
	}()
	defer func() {
		cancelGen()
		<-watcherDone
	}()

	// Acquire the generation lock, honoring cancellation while waiting.
	select {
	case p.genMu <- struct{}{}:
	case <-genCtx.Done():
		provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: genCtx.Err()})
		return
	}
	defer func() { <-p.genMu }()

	p.mu.Lock()
	closed := p.st == stateClosed
	p.mu.Unlock()
	if closed {
		provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("embedded provider %q is closed", p.name)})
		return
	}

	// Honor a model switch requested via req.Model (the TUI's model picker
	// sets exactly this field). This must never be silently ignored: a user
	// who picked a sibling .gguf must not keep generating with the
	// previously loaded file.
	if err := p.switchModelIfRequested(genCtx, req.Model, events); err != nil {
		emitError(genCtx, events, err)
		return
	}

	p.mu.Lock()
	loaded := p.st == stateReady
	rt := p.rt
	opts := p.opts
	opts.ModelPath = p.activePath
	p.mu.Unlock()

	if !loaded {
		base := filepath.Base(opts.ModelPath)
		if !provider.Emit(genCtx, events, provider.ChatEvent{Type: provider.EventReasoning, Delta: fmt.Sprintf("loading model %s …", base)}) {
			return
		}
		meta, err := rt.Load(genCtx, opts, func(msg string) {
			provider.Emit(genCtx, events, provider.ChatEvent{Type: provider.EventReasoning, Delta: msg})
		})
		if err != nil {
			emitError(genCtx, events, fmt.Errorf("embedded provider %q: failed to load model %q: %w", p.name, opts.ModelPath, err))
			return
		}
		p.mu.Lock()
		p.meta = meta
		p.haveM = true
		p.st = stateReady
		p.mu.Unlock()
	}

	genReq := GenRequest{
		Messages:    req.Messages,
		Temperature: req.Temperature,
		TopP:        req.TopP,
		MaxTokens:   req.MaxTokens,
		Progress: func(message string) {
			provider.Emit(genCtx, events, provider.ChatEvent{Type: provider.EventReasoning, Delta: message})
		},
	}

	aborted := false
	result, err := rt.Generate(genCtx, genReq, func(piece string) {
		if aborted {
			return
		}
		if !provider.Emit(genCtx, events, provider.ChatEvent{Type: provider.EventDelta, Delta: piece}) {
			aborted = true
		}
	})
	if aborted {
		provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: genCtx.Err()})
		return
	}
	if err != nil {
		if genCtx.Err() != nil {
			// Cancellation (ours or the runtime's own) must surface as-is
			// so the TUI treats this as a cancel, not a failure.
			provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: genCtx.Err()})
			return
		}
		emitError(genCtx, events, fmt.Errorf("embedded provider %q: generation failed: %w", p.name, err))
		return
	}

	provider.Emit(genCtx, events, provider.ChatEvent{Type: provider.EventDone, Usage: &provider.Usage{
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		TotalTokens:      result.PromptTokens + result.CompletionTokens,
		Estimated:        false,
	}})
}

// switchModelIfRequested handles a ChatRequest.Model that names a different
// model file than the one currently active. Callers must hold genMu (all
// switches, loads, and generations are serialized by it, as is Close).
//
// The requested path is validated on disk BEFORE the current runtime is
// touched, so a bad selection can never destroy a working engine. On a valid
// switch the loaded runtime (if any) is closed, a fresh instance is created
// via newRuntime, the active path is updated, and state returns to unloaded
// so the caller's normal load path brings the new model up.
func (p *Provider) switchModelIfRequested(genCtx context.Context, requested string, events chan<- provider.ChatEvent) error {
	if requested == "" {
		return nil
	}
	requested = expandHome(requested)

	p.mu.Lock()
	active := p.activePath
	loaded := p.st == stateReady
	p.mu.Unlock()
	if requested == active {
		return nil
	}

	// Validate the new selection before closing anything.
	info, err := os.Stat(requested)
	if err != nil || info.IsDir() {
		current := active
		if current == "" {
			current = "none"
		}
		return fmt.Errorf("embedded provider %q cannot switch to model %q: not a .gguf model file on disk (currently loaded: %s)", p.name, requested, current)
	}

	provider.Emit(genCtx, events, provider.ChatEvent{Type: provider.EventReasoning, Delta: fmt.Sprintf("switching model to %s …", filepath.Base(requested))})

	var oldRt Runtime
	p.mu.Lock()
	if loaded {
		oldRt = p.rt
	}
	p.activePath = requested
	p.st = stateUnloaded
	p.haveM = false
	p.mu.Unlock()

	if oldRt != nil {
		// Close the retired engine outside the mutex (a native close can be
		// slow); genMu prevents any concurrent use of p.rt.
		if err := oldRt.Close(); err != nil {
			// The old engine is gone either way; a close error must not
			// block the switch. Surface it as activity, not failure.
			provider.Emit(genCtx, events, provider.ChatEvent{Type: provider.EventReasoning, Delta: fmt.Sprintf("note: closing previous model reported: %v", err)})
		}
		p.mu.Lock()
		p.rt = p.newRuntime()
		p.mu.Unlock()
	}
	return nil
}

// emitError delivers a genuine (non-cancellation) error event, blocking
// until the consumer takes it or the context dies — a TryEmit here could
// silently drop the only terminal event of the stream.
func emitError(ctx context.Context, events chan<- provider.ChatEvent, err error) {
	if !provider.Emit(ctx, events, provider.ChatEvent{Type: provider.EventError, Err: err}) {
		provider.TryEmit(events, provider.ChatEvent{Type: provider.EventError, Err: err})
	}
}

// Close releases the current runtime. It is idempotent and safe to call
// while a generation is in flight: it cancels the in-flight generation
// first, waits for it to wind down, then closes the runtime exactly once.
// Further Chat calls on a closed provider return an error.
func (p *Provider) Close() error {
	var closeErr error
	p.closeOnce.Do(func() {
		p.cancelClose()
		// Wait for any in-flight generation to release the lock. Model
		// switches also happen under genMu, so once we hold it the current
		// p.rt instance can no longer change.
		p.genMu <- struct{}{}
		defer func() { <-p.genMu }()

		p.mu.Lock()
		p.st = stateClosed
		rt := p.rt
		p.mu.Unlock()

		closeErr = rt.Close()
	})
	return closeErr
}

// RuntimeFingerprint hashes every option that shapes generated output but is
// not captured by the shared ChatRequest fields, keyed on the currently
// active model file (including its size and mtime) so both switching models
// and replacing the file on disk invalidate cached responses.
func (p *Provider) RuntimeFingerprint() string {
	opts := p.activeOptions()

	h := sha256.New()
	writeField(h, []byte(opts.ModelPath))

	var size int64
	var mtime int64
	if info, err := os.Stat(opts.ModelPath); err == nil {
		size = info.Size()
		mtime = info.ModTime().UnixNano()
	}
	writeInt64(h, size)
	writeInt64(h, mtime)

	writeInt64(h, int64(opts.ContextSize))
	writeInt64(h, int64(opts.GPULayers))
	writeInt64(h, int64(opts.Threads))
	writeInt64(h, int64(opts.BatchSize))
	writeField(h, []byte(opts.ChatTemplate))

	writeInt64(h, int64(opts.Sampling.TopK))
	writeFloat64(h, opts.Sampling.MinP)
	writeFloat64(h, opts.Sampling.RepeatPenalty)
	writeInt64(h, int64(opts.Sampling.RepeatLastN))
	writeInt64(h, int64(opts.Sampling.Seed))
	for _, s := range opts.Sampling.Stop {
		writeField(h, []byte(s))
	}
	writeField(h, []byte("stop-end"))

	return hex.EncodeToString(h.Sum(nil))
}

// writeField writes a length-prefixed field so concatenated field
// boundaries can never be confused with each other (mirrors
// internal/tui/pipeline.go's writeFingerprintField).
func writeField(h hash.Hash, field []byte) {
	_, _ = h.Write([]byte(strconv.Itoa(len(field)) + ":"))
	_, _ = h.Write(field)
}

func writeInt64(h hash.Hash, v int64) {
	var b [8]byte
	binary.BigEndian.PutUint64(b[:], uint64(v))
	writeField(h, b[:])
}

func writeFloat64(h hash.Hash, v float64) {
	writeInt64(h, int64(v*1e9))
}
