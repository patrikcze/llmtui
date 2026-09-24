package decision

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
)

type InstallationStore interface {
	Catalog() []ModelDescriptor
	Inspect(id string) ([]Installation, error)
}

type RouterOptions struct {
	Store        InstallationStore
	Loader       RuntimeLoader
	DefaultModel string
	MaxLoaded    int
}

type Router struct {
	store        InstallationStore
	loader       RuntimeLoader
	defaultModel string
	maxLoaded    int

	mu      sync.Mutex
	closed  bool
	clock   uint64
	entries map[string]*routerEntry
}

type routerEntry struct {
	engine Engine
	// revision is the exact installed manifest revision selected when this
	// entry was loaded, bound once and never re-derived — see acquire and
	// Result.Routing.Revision's doc comment.
	revision string
	refs     int
	usedAt   uint64
	closing  bool
	// load is non-nil exactly while this alias's engine is still being
	// loaded (Phase 0c) — see acquire/runLoad. engine/revision stay zero
	// until the load publishes and clears this field; evictVictimsLocked
	// and Close both treat a non-nil load as "not yet closable" instead of
	// touching a nil engine.
	load *pendingLoad
}

// pendingLoad tracks exactly one in-flight Router-owned load attempt for
// one alias. acquire creates it and launches runLoad as the sole owner
// while still holding r.mu, then releases the lock before any disk or
// process I/O; every acquire call for that alias — including the one that
// created it — waits on ready without holding r.mu, so a cold load never
// blocks a concurrent acquire (same or different alias) or Close. runLoad
// is the sole writer of err and the sole closer of ready. cancel lets
// Close abort a load still in flight without waiting for it to finish.
type pendingLoad struct {
	ready  chan struct{}
	cancel context.CancelFunc
	err    error
}

// evictedEntry pairs an alias with the engine an eviction or Close removed
// from r.entries, so the caller can run its (potentially blocking)
// Engine.Close after releasing r.mu — never while holding it (Phase 0c).
type evictedEntry struct {
	alias  string
	engine Engine
}

func closeVictims(victims []evictedEntry) {
	for _, v := range victims {
		_ = v.engine.Close()
	}
}

func NewRouter(options RouterOptions) (*Router, error) {
	if options.Store == nil {
		return nil, errors.New("decision router requires an installation store")
	}
	if options.Loader == nil {
		return nil, errors.New("decision router requires a runtime loader")
	}
	maxLoaded := options.MaxLoaded
	if maxLoaded == 0 {
		maxLoaded = 1
	}
	if maxLoaded < 1 {
		return nil, fmt.Errorf("invalid decision router max_loaded %d", maxLoaded)
	}
	defaultModel := options.DefaultModel
	if defaultModel == "" {
		defaultModel = "english"
	}
	defaultAlias, err := routerAlias(defaultModel, options.Store.Catalog())
	if err != nil {
		return nil, err
	}
	return &Router{
		store: options.Store, loader: options.Loader, defaultModel: defaultAlias,
		maxLoaded: maxLoaded, entries: make(map[string]*routerEntry),
	}, nil
}

func (r *Router) Name() string { return "laya-router" }

func (r *Router) Predict(ctx context.Context, state any, questions map[string]Question, options PredictOptions) (Result, error) {
	if err := Validate(state, questions); err != nil {
		return Result{}, err
	}
	model := options.Model
	if model == "" {
		model = r.defaultModel
	}
	alias, err := routerAlias(model, r.store.Catalog())
	if err != nil {
		return Result{}, err
	}
	engine, revision, release, err := r.acquire(ctx, alias)
	if err != nil {
		return Result{}, err
	}
	defer release()
	result, err := engine.Predict(ctx, state, questions, options)
	if err != nil {
		unusable := errors.Is(err, ErrUnavailable)
		if health, ok := engine.(interface{ usable() bool }); ok {
			unusable = unusable || !health.usable()
		}
		if unusable {
			r.mu.Lock()
			if entry := r.entries[alias]; entry != nil {
				entry.closing = true
			}
			r.mu.Unlock()
		}
		return Result{}, err
	}
	result.Routing.Model = modelID(alias)
	// Revision is bound at load time on the routerEntry (acquire), never
	// re-derived from a later catalog/installation query here — a newer
	// installation appearing while this engine is still loaded and in use
	// must not silently relabel predictions it never actually produced.
	result.Routing.Revision = revision
	for _, descriptor := range r.store.Catalog() {
		if descriptor.Alias == alias {
			result.Routing.Repository = descriptor.Repository
			break
		}
	}
	return result, nil
}

// acquire returns a usable Engine for alias, loading it if necessary. The
// Router mutex is held only for bounded, in-memory bookkeeping — never
// across disk verification, LoadRuntime, or the worker subprocess
// handshake (Phase 0c): a caller that finds no cached entry becomes that
// alias's load owner, reserving the entry and launching runLoad while
// still holding r.mu, then releases the lock before any I/O. Every caller
// for that alias — including the owner — then waits on the same
// pendingLoad without holding r.mu, so a cold load for one alias never
// blocks acquire for another alias, a concurrent Predict release, or
// Close.
func (r *Router) acquire(ctx context.Context, alias string) (Engine, string, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, "", nil, err
	}
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil, "", nil, errors.New("decision router is closed")
	}
	r.clock++
	entry := r.entries[alias]
	if entry != nil && entry.closing {
		r.mu.Unlock()
		return nil, "", nil, fmt.Errorf("%w: decision worker is retiring", ErrUnavailable)
	}
	if entry == nil {
		loadCtx, cancel := context.WithCancel(context.Background())
		load := &pendingLoad{ready: make(chan struct{}), cancel: cancel}
		entry = &routerEntry{usedAt: r.clock, load: load}
		r.entries[alias] = entry
		go r.runLoad(alias, entry, load, loadCtx)
	}
	entry.refs++
	entry.usedAt = r.clock
	load := entry.load
	r.mu.Unlock()

	if load != nil {
		// A cancelled waiter must never cancel another caller's shared
		// load — loadCtx above is independent of every individual acquire
		// ctx, including the owner's. Leaving early here only gives back
		// this call's own reservation.
		select {
		case <-load.ready:
		case <-ctx.Done():
			r.releaseFunc(alias, entry)()
			return nil, "", nil, ctx.Err()
		}
		if load.err != nil {
			r.releaseFunc(alias, entry)()
			return nil, "", nil, load.err
		}
	}
	return entry.engine, entry.revision, r.releaseFunc(alias, entry), nil
}

// runLoad performs the installation lookup and LoadRuntime call for one
// alias's load attempt entirely outside r.mu, then publishes the result
// under the lock. It is launched exactly once per load attempt, only from
// acquire's "entry == nil" branch, so it is the sole writer of
// entry.engine/revision/load and the sole closer of load.ready.
func (r *Router) runLoad(alias string, entry *routerEntry, load *pendingLoad, loadCtx context.Context) {
	defer load.cancel()

	installations, err := r.store.Inspect(modelID(alias))
	var installation Installation
	var engine Engine
	var revision string
	if err != nil {
		err = fmt.Errorf("inspect decision model %s: %w", modelID(alias), err)
	} else {
		var ok bool
		installation, ok = newestValidInstallation(installations)
		if !ok {
			err = fmt.Errorf("%w: no valid installed runtime for %s", ErrRuntimeArtifactUnavailable, modelID(alias))
		}
	}
	if err == nil {
		engine, err = LoadRuntime(loadCtx, installation, r.loader)
	}
	if err == nil {
		revision = installation.Manifest.Source.Revision
	}

	r.mu.Lock()
	if err == nil && r.closed {
		// Close ran concurrently and could not wait for this load — never
		// hand out an engine after Close, even one that finished loading
		// successfully right as (or after) it was cancelled.
		err = errors.New("decision router is closed")
	}
	if err != nil {
		load.err = err
		if r.entries[alias] == entry {
			// Never cache a failed load: the next acquire for alias starts
			// a fresh attempt instead of returning a permanently broken
			// entry.
			delete(r.entries, alias)
		}
		close(load.ready)
		r.mu.Unlock()
		if engine != nil {
			_ = engine.Close()
		}
		return
	}
	entry.engine = engine
	entry.revision = revision
	entry.load = nil
	victims := r.evictVictimsLocked(alias)
	r.mu.Unlock()
	closeVictims(victims)
	// Only now let waiters proceed: eviction of any surplus idle engine
	// this publish triggered has already finished, matching the prior
	// synchronous-acquire behavior every existing caller/test relies on.
	close(load.ready)
}

func (r *Router) releaseFunc(alias string, entry *routerEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			if entry.refs > 0 {
				entry.refs--
			}
			var closeSelf bool
			if entry.refs == 0 && entry.closing {
				closeSelf = true
				if r.entries[alias] == entry {
					delete(r.entries, alias)
				}
			}
			victims := r.evictVictimsLocked("")
			r.mu.Unlock()
			// Engine.Close (subprocess kill + Wait) never runs while r.mu
			// is held (Phase 0c) — see evictVictimsLocked/Close.
			if closeSelf {
				_ = entry.engine.Close()
			}
			closeVictims(victims)
		})
	}
}

// evictVictimsLocked selects and removes idle loaded entries beyond
// maxLoaded, skipping alias except, any busy entry (refs > 0), any entry
// already marked closing, and any entry still loading (load != nil — it
// has no engine yet to close, and is never a legitimate idle-eviction
// target). Must be called with r.mu held; never calls Engine.Close itself
// — every returned entry's cleanup is the caller's responsibility, run
// after unlocking.
func (r *Router) evictVictimsLocked(except string) []evictedEntry {
	var victims []evictedEntry
	for len(r.entries) > r.maxLoaded {
		var victimAlias string
		var victim *routerEntry
		for alias, entry := range r.entries {
			if alias == except || entry.refs > 0 || entry.closing || entry.load != nil {
				continue
			}
			if victim == nil || entry.usedAt < victim.usedAt {
				victimAlias, victim = alias, entry
			}
		}
		if victim == nil {
			return victims
		}
		delete(r.entries, victimAlias)
		victims = append(victims, evictedEntry{alias: victimAlias, engine: victim.engine})
	}
	return victims
}

// Close marks the Router closed, cancels every pending load, detaches idle
// loaded engines, and marks busy ones for their releasing acquire caller to
// close later instead — all under one short lock hold. It never runs a
// loader's or an engine's blocking I/O (subprocess kill/wait, or waiting
// out a cold LoadRuntime call) while holding r.mu, so Close cannot be stuck
// behind a concurrent cold model load or a slow worker teardown (Phase
// 0c). A load that races Close and still completes successfully is caught
// by runLoad's own post-load closed check and closed immediately, never
// published.
func (r *Router) Close() error {
	r.mu.Lock()
	if r.closed {
		r.mu.Unlock()
		return nil
	}
	r.closed = true
	var victims []evictedEntry
	for alias, entry := range r.entries {
		if entry.load != nil {
			entry.load.cancel()
			continue
		}
		if entry.refs > 0 {
			entry.closing = true
			continue
		}
		delete(r.entries, alias)
		victims = append(victims, evictedEntry{alias: alias, engine: entry.engine})
	}
	r.mu.Unlock()
	var errs []error
	for _, v := range victims {
		if err := v.engine.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", modelID(v.alias), err))
		}
	}
	return errors.Join(errs...)
}

func routerAlias(model string, catalog []ModelDescriptor) (string, error) {
	if model == "" {
		model = "english"
	}
	if !strings.Contains(model, ":") {
		model = modelID(model)
	}
	descriptor, err := descriptorForID(model, catalog)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	return descriptor.Alias, nil
}

func newestValidInstallation(installations []Installation) (Installation, bool) {
	valid := make([]Installation, 0, len(installations))
	for _, installation := range installations {
		if installation.Valid {
			valid = append(valid, installation)
		}
	}
	if len(valid) == 0 {
		return Installation{}, false
	}
	sort.Slice(valid, func(i, j int) bool {
		return valid[i].Manifest.Source.Revision > valid[j].Manifest.Source.Revision
	})
	return valid[0], true
}
