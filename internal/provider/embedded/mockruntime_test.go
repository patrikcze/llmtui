package embedded

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

// scriptedRuntime is a fully in-Go, no-native Runtime for exercising
// Provider lifecycle, streaming, cancellation, and error paths.
type scriptedRuntime struct {
	probeErr error

	loadCalls int32
	loadErr   error
	loadDelay time.Duration
	loadMeta  ModelMeta

	genPieces []string
	genErr    error
	genDelay  time.Duration // delay between pieces
	// blockUntilCanceled, if true, makes Generate ignore genPieces and
	// block until ctx is done, then return ctx.Err().
	blockUntilCanceled bool

	genCalls  int32
	closeCall int32
	closeErr  error

	pathMu    sync.Mutex
	loadPaths []string // ModelPath of every Load call, in order
}

func (r *scriptedRuntime) Probe(Options) error { return r.probeErr }

func (r *scriptedRuntime) Load(ctx context.Context, opts Options, progress func(string)) (ModelMeta, error) {
	atomic.AddInt32(&r.loadCalls, 1)
	r.pathMu.Lock()
	r.loadPaths = append(r.loadPaths, opts.ModelPath)
	r.pathMu.Unlock()
	progress("loading model …")
	if r.loadDelay > 0 {
		select {
		case <-time.After(r.loadDelay):
		case <-ctx.Done():
			return ModelMeta{}, ctx.Err()
		}
	}
	if r.loadErr != nil {
		return ModelMeta{}, r.loadErr
	}
	meta := r.loadMeta
	if meta.NCtxTrain == 0 {
		meta.NCtxTrain = 4096
	}
	return meta, nil
}

func (r *scriptedRuntime) Generate(ctx context.Context, req GenRequest, emit func(string)) (GenResult, error) {
	atomic.AddInt32(&r.genCalls, 1)

	if r.blockUntilCanceled {
		<-ctx.Done()
		return GenResult{}, ctx.Err()
	}

	completion := 0
	for _, piece := range r.genPieces {
		if ctx.Err() != nil {
			return GenResult{}, ctx.Err()
		}
		if r.genDelay > 0 {
			select {
			case <-time.After(r.genDelay):
			case <-ctx.Done():
				return GenResult{}, ctx.Err()
			}
		}
		emit(piece)
		completion++
	}
	if r.genErr != nil {
		return GenResult{}, r.genErr
	}
	return GenResult{PromptTokens: 7, CompletionTokens: completion}, nil
}

func (r *scriptedRuntime) Close() error {
	atomic.AddInt32(&r.closeCall, 1)
	return r.closeErr
}

func (r *scriptedRuntime) loadCallCount() int  { return int(atomic.LoadInt32(&r.loadCalls)) }
func (r *scriptedRuntime) genCallCount() int   { return int(atomic.LoadInt32(&r.genCalls)) }
func (r *scriptedRuntime) closeCallCount() int { return int(atomic.LoadInt32(&r.closeCall)) }

func (r *scriptedRuntime) loadedPaths() []string {
	r.pathMu.Lock()
	defer r.pathMu.Unlock()
	return append([]string(nil), r.loadPaths...)
}

// fixedRuntime adapts a single pre-configured runtime instance to the
// factory signature New expects. Suitable for tests that never switch
// models (a switch would hand the same — possibly closed — instance back).
func fixedRuntime(rt Runtime) func() Runtime {
	return func() Runtime { return rt }
}

// runtimeFactory creates a fresh scriptedRuntime per call and records every
// instance in creation order, so switch tests can assert which instance was
// loaded, generated on, and closed.
type runtimeFactory struct {
	mu        sync.Mutex
	configure func(*scriptedRuntime) // applied to each new instance
	instances []*scriptedRuntime
}

func (f *runtimeFactory) new() Runtime {
	f.mu.Lock()
	defer f.mu.Unlock()
	rt := &scriptedRuntime{}
	if f.configure != nil {
		f.configure(rt)
	}
	f.instances = append(f.instances, rt)
	return rt
}

func (f *runtimeFactory) instance(i int) *scriptedRuntime {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.instances[i]
}

func (f *runtimeFactory) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.instances)
}

var errBoom = errors.New("boom")

func testOptions(modelPath string) Options {
	return Options{ModelPath: modelPath}
}

func drain(events <-chan provider.ChatEvent) []provider.ChatEvent {
	var out []provider.ChatEvent
	for ev := range events {
		out = append(out, ev)
	}
	return out
}
