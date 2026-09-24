package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

type routerStore struct {
	catalog       []ModelDescriptor
	installations map[string][]Installation
}

func (s routerStore) Catalog() []ModelDescriptor { return append([]ModelDescriptor(nil), s.catalog...) }
func (s routerStore) Inspect(id string) ([]Installation, error) {
	return append([]Installation(nil), s.installations[id]...), nil
}

type routerEngine struct {
	mu     sync.Mutex
	closed int
}

func (e *routerEngine) Name() string { return "test" }
func (e *routerEngine) Predict(context.Context, any, map[string]Question, PredictOptions) (Result, error) {
	return Result{Answers: map[string]Answer{}}, nil
}
func (e *routerEngine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed++
	return nil
}

type routerLoader struct {
	mu      sync.Mutex
	engines []*routerEngine
}

func (l *routerLoader) Load(context.Context, Installation) (Engine, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	engine := &routerEngine{}
	l.engines = append(l.engines, engine)
	return engine, nil
}

func readyInstallation(t *testing.T, root, alias string) Installation {
	t.Helper()
	dir := filepath.Join(root, alias)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	data := []byte("runtime-" + alias)
	if err := os.WriteFile(filepath.Join(dir, "runtime.onnx"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return Installation{
		ID:    modelID(alias),
		Path:  dir,
		Valid: true,
		Manifest: ModelManifest{
			Model:   alias,
			Source:  ModelSource{Repository: "convaiinnovations/laya", Revision: alias + "-revision"},
			Runtime: RuntimeArtifact{Format: RuntimeFormatONNX, Ready: true, Path: "runtime.onnx", Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:]), Exporter: "test", UpstreamRevision: "source"},
		},
	}
}

func TestRouterLoadsAndEvictsIdleEngines(t *testing.T) {
	root := t.TempDir()
	english := readyInstallation(t, root, "english")
	multilingual := readyInstallation(t, root, "multilingual")
	store := routerStore{
		catalog: []ModelDescriptor{LayaModels[0], LayaModels[1]},
		installations: map[string][]Installation{
			modelID("english"):      {english},
			modelID("multilingual"): {multilingual},
		},
	}
	loader := &routerLoader{}
	router, err := NewRouter(RouterOptions{Store: store, Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}
	question := map[string]Question{"q": {Type: QuestionChoice, Criteria: []string{"yes", "no"}}}
	if _, err := router.Predict(context.Background(), nil, question, PredictOptions{Model: "english"}); err != nil {
		t.Fatal(err)
	}
	if _, err := router.Predict(context.Background(), nil, question, PredictOptions{Model: "multilingual"}); err != nil {
		t.Fatal(err)
	}
	if len(loader.engines) != 2 || loader.engines[0].closed != 1 {
		t.Fatalf("engines after eviction = %d, first closes=%d", len(loader.engines), loader.engines[0].closed)
	}
	if _, err := router.Predict(context.Background(), nil, question, PredictOptions{Model: "english"}); err != nil {
		t.Fatal(err)
	}
	if len(loader.engines) != 3 {
		t.Fatalf("loader calls = %d, want 3 after eviction", len(loader.engines))
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRouterRejectsUnknownModel(t *testing.T) {
	store := routerStore{catalog: LayaModels[:1]}
	_, err := NewRouter(RouterOptions{Store: store, Loader: &routerLoader{}, DefaultModel: "missing"})
	if err == nil || !errors.Is(err, ErrInvalid) {
		t.Fatalf("NewRouter() error = %v, want ErrInvalid", err)
	}
}

// TestRouterBindsLoadedRevisionNotNewestCatalog covers Phase 0b: the
// revision attached to a prediction is the one actually loaded, bound once
// at load time — not re-derived from a later, possibly-newer installation
// that appears in the store while the loaded engine is still cached and in
// use. This is exactly the gap the plan's §3 flagged: "newestValidInstallation
// chooses the newest revision; Predict re-queries the catalog for
// Repository but never carried a bound Revision at all."
func TestRouterBindsLoadedRevisionNotNewestCatalog(t *testing.T) {
	root := t.TempDir()
	english := readyInstallation(t, root, "english")
	store := routerStore{
		catalog:       []ModelDescriptor{LayaModels[0]},
		installations: map[string][]Installation{modelID("english"): {english}},
	}
	loader := &routerLoader{}
	router, err := NewRouter(RouterOptions{Store: store, Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}
	question := map[string]Question{"q": {Type: QuestionChoice, Criteria: []string{"yes", "no"}}}
	first, err := router.Predict(context.Background(), nil, question, PredictOptions{Model: "english"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Routing.Revision != "english-revision" {
		t.Fatalf("Routing.Revision = %q, want %q", first.Routing.Revision, "english-revision")
	}

	// A newer installation appears in the store — the router never
	// re-inspects while the cached engine (MaxLoaded=1, single alias, never
	// evicted) is still loaded, so the bound revision must not change.
	newer := english
	newer.Manifest.Source.Revision = "english-revision-newer"
	store.installations[modelID("english")] = append(store.installations[modelID("english")], newer)

	second, err := router.Predict(context.Background(), nil, question, PredictOptions{Model: "english"})
	if err != nil {
		t.Fatal(err)
	}
	if second.Routing.Revision != "english-revision" {
		t.Fatalf("Routing.Revision drifted to %q after a newer installation appeared, want the still-loaded %q", second.Routing.Revision, "english-revision")
	}
	if len(loader.engines) != 1 {
		t.Fatalf("loader calls = %d, want 1 (engine reused, never reloaded)", len(loader.engines))
	}
}
