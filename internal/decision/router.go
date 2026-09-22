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
	engine  Engine
	refs    int
	usedAt  uint64
	closing bool
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
	engine, release, err := r.acquire(ctx, alias)
	if err != nil {
		return Result{}, err
	}
	defer release()
	result, err := engine.Predict(ctx, state, questions, options)
	if err != nil {
		return Result{}, err
	}
	result.Routing.Model = modelID(alias)
	for _, descriptor := range r.store.Catalog() {
		if descriptor.Alias == alias {
			result.Routing.Repository = descriptor.Repository
			break
		}
	}
	return result, nil
}

func (r *Router) acquire(ctx context.Context, alias string) (Engine, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil, nil, errors.New("decision router is closed")
	}
	r.clock++
	if entry := r.entries[alias]; entry != nil && !entry.closing {
		entry.refs++
		entry.usedAt = r.clock
		return entry.engine, r.releaseFunc(alias, entry), nil
	}
	installations, err := r.store.Inspect(modelID(alias))
	if err != nil {
		return nil, nil, fmt.Errorf("inspect decision model %s: %w", modelID(alias), err)
	}
	installation, ok := newestValidInstallation(installations)
	if !ok {
		return nil, nil, fmt.Errorf("%w: no valid installed runtime for %s", ErrRuntimeArtifactUnavailable, modelID(alias))
	}
	engine, err := LoadRuntime(ctx, installation, r.loader)
	if err != nil {
		return nil, nil, err
	}
	entry := &routerEntry{engine: engine, refs: 1, usedAt: r.clock}
	r.entries[alias] = entry
	r.evictLocked(alias)
	return engine, r.releaseFunc(alias, entry), nil
}

func (r *Router) releaseFunc(alias string, entry *routerEntry) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			r.mu.Lock()
			defer r.mu.Unlock()
			if entry.refs > 0 {
				entry.refs--
			}
			if entry.refs == 0 && entry.closing {
				_ = entry.engine.Close()
				if r.entries[alias] == entry {
					delete(r.entries, alias)
				}
			}
		})
	}
}

func (r *Router) evictLocked(except string) {
	for len(r.entries) > r.maxLoaded {
		var victimAlias string
		var victim *routerEntry
		for alias, entry := range r.entries {
			if alias == except || entry.refs > 0 || entry.closing {
				continue
			}
			if victim == nil || entry.usedAt < victim.usedAt {
				victimAlias, victim = alias, entry
			}
		}
		if victim == nil {
			return
		}
		delete(r.entries, victimAlias)
		_ = victim.engine.Close()
	}
}

func (r *Router) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	var errs []error
	for alias, entry := range r.entries {
		if entry.refs > 0 {
			entry.closing = true
			continue
		}
		if err := entry.engine.Close(); err != nil {
			errs = append(errs, fmt.Errorf("close %s: %w", modelID(alias), err))
		}
		delete(r.entries, alias)
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
