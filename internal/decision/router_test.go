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
	"time"
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

// ============================================================================
// Phase 0c: reload/close independent of a cold model load. See the plan's
// §11.0c and router.go's acquire/runLoad/Close doc comments.
// ============================================================================

// loadGate is one alias's controllable load point for gatedLoader — a test
// blocks Load() deterministically via a channel, never a sleep, and can
// observe exactly when Load was entered.
type loadGate struct {
	entered chan struct{}
	release chan struct{}
	engine  Engine
	err     error
}

// gatedLoader is a channel-controlled RuntimeLoader for lifecycle tests:
// Load blocks on the alias's gate until the test closes release or the
// caller's ctx is cancelled, so tests can deterministically observe
// "still loading" versus "load settled" without any wall-clock race.
type gatedLoader struct {
	mu    sync.Mutex
	gates map[string]*loadGate
	calls map[string]int
}

func newGatedLoader() *gatedLoader {
	return &gatedLoader{gates: make(map[string]*loadGate), calls: make(map[string]int)}
}

// gate returns (lazily creating) alias's gate. Tests call this before
// triggering the acquire/Predict that will consume it, so the returned
// channels are safe to read/close from the test goroutine immediately.
func (l *gatedLoader) gate(alias string) *loadGate {
	l.mu.Lock()
	defer l.mu.Unlock()
	g, ok := l.gates[alias]
	if !ok {
		g = &loadGate{entered: make(chan struct{}), release: make(chan struct{}), engine: &routerEngine{}}
		l.gates[alias] = g
	}
	return g
}

func (l *gatedLoader) callCount(alias string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[alias]
}

func (l *gatedLoader) Load(ctx context.Context, installation Installation) (Engine, error) {
	g := l.gate(installation.Manifest.Model)
	l.mu.Lock()
	l.calls[installation.Manifest.Model]++
	l.mu.Unlock()
	close(g.entered)
	select {
	case <-g.release:
		return g.engine, g.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// obliviousLoader ignores ctx cancellation entirely and only ever returns
// once release is closed — used to deterministically exercise "a load that
// finishes successfully despite Close" without depending on a race between
// cancellation and release in a select (gatedLoader's ctx-aware behavior
// would make that outcome nondeterministic).
type obliviousLoader struct {
	entered chan struct{}
	release chan struct{}
	engine  *routerEngine
	once    sync.Once
}

func newObliviousLoader() *obliviousLoader {
	return &obliviousLoader{entered: make(chan struct{}), release: make(chan struct{}), engine: &routerEngine{}}
}

func (l *obliviousLoader) Load(context.Context, Installation) (Engine, error) {
	l.once.Do(func() { close(l.entered) })
	<-l.release
	return l.engine, nil
}

func singleEnglishStore(t *testing.T, root string) routerStore {
	t.Helper()
	english := readyInstallation(t, root, "english")
	return routerStore{catalog: []ModelDescriptor{LayaModels[0]}, installations: map[string][]Installation{modelID("english"): {english}}}
}

var oneChoiceQuestion = map[string]Question{"q": {Type: QuestionChoice, Criteria: []string{"yes", "no"}}}

// TestRouterCloseDoesNotBlockOnInFlightLoad covers the plan's core Phase 0c
// defect: acquire must not hold r.mu across LoadRuntime, so Close (config
// reload/shutdown) never waits behind a cold load in progress.
func TestRouterCloseDoesNotBlockOnInFlightLoad(t *testing.T) {
	loader := newGatedLoader()
	gate := loader.gate("english")
	router, err := NewRouter(RouterOptions{Store: singleEnglishStore(t, t.TempDir()), Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}

	predictDone := make(chan error, 1)
	go func() {
		_, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "english"})
		predictDone <- err
	}()
	<-gate.entered // Load is now blocked, holding no Router lock.

	closeDone := make(chan error, 1)
	go func() { closeDone <- router.Close() }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked behind an in-flight cold load")
	}

	close(gate.release) // let the now-cancelled load unwind; avoids a leak.
	if err := <-predictDone; err == nil {
		t.Fatal("Predict succeeded with an engine published after Close")
	}
}

// TestRouterConcurrentAcquireLoadsOnce covers "one load owner per alias":
// many concurrent callers for the same never-before-seen alias must trigger
// exactly one RuntimeLoader.Load call, and every caller must see the same
// successfully loaded engine.
func TestRouterConcurrentAcquireLoadsOnce(t *testing.T) {
	loader := newGatedLoader()
	gate := loader.gate("english")
	router, err := NewRouter(RouterOptions{Store: singleEnglishStore(t, t.TempDir()), Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 8
	results := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "english"})
			results <- err
		}()
	}
	<-gate.entered
	close(gate.release)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatalf("Predict() error = %v", err)
		}
	}
	if got := loader.callCount("english"); got != 1 {
		t.Fatalf("loader.Load called %d times, want exactly 1", got)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRouterAcquireWaiterCancellationDoesNotAbortLoadForOthers covers "a
// cancelled waiter cannot cancel another caller's established engine": one
// caller's ctx cancelling while a shared load is in flight must return
// ctx.Err() to that caller alone, without aborting the load a second,
// uncancelled caller is still waiting on.
func TestRouterAcquireWaiterCancellationDoesNotAbortLoadForOthers(t *testing.T) {
	loader := newGatedLoader()
	gate := loader.gate("english")
	router, err := NewRouter(RouterOptions{Store: singleEnglishStore(t, t.TempDir()), Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancelledDone := make(chan error, 1)
	go func() {
		_, err := router.Predict(cancelledCtx, nil, oneChoiceQuestion, PredictOptions{Model: "english"})
		cancelledDone <- err
	}()
	<-gate.entered // one of the two callers below is now the load owner.

	patientDone := make(chan error, 1)
	go func() {
		_, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "english"})
		patientDone <- err
	}()

	cancel()
	if err := <-cancelledDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Predict error = %v, want context.Canceled", err)
	}

	// Deterministic, not timing-based: the load's only unblock signal is
	// gate.release, still unclosed, so patientDone cannot legitimately have
	// a value yet regardless of scheduling.
	select {
	case err := <-patientDone:
		t.Fatalf("uncancelled Predict returned early (err=%v) instead of waiting for the shared load", err)
	default:
	}

	close(gate.release)
	if err := <-patientDone; err != nil {
		t.Fatalf("uncancelled Predict error = %v, want nil (the shared load must still succeed)", err)
	}
	if got := loader.callCount("english"); got != 1 {
		t.Fatalf("loader.Load called %d times, want exactly 1", got)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRouterLoadFailsAfterCloseNeverPublishes covers a load whose context
// cancellation (via Close) it actually observes: the pending Predict call
// must resolve with an error, never a published engine.
func TestRouterLoadFailsAfterCloseNeverPublishes(t *testing.T) {
	loader := newGatedLoader()
	gate := loader.gate("english")
	router, err := NewRouter(RouterOptions{Store: singleEnglishStore(t, t.TempDir()), Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}

	predictDone := make(chan error, 1)
	go func() {
		_, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "english"})
		predictDone <- err
	}()
	<-gate.entered

	if err := router.Close(); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-predictDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Predict error = %v, want context.Canceled (load must observe Close's cancellation)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Predict did not resolve after Close cancelled its in-flight load")
	}
}

// TestRouterLateSuccessfulLoadClosesEngineAfterClose covers a load that
// finishes successfully despite racing Close (e.g. a loader that does not
// check ctx promptly): the just-built engine must be closed immediately
// and never published, regardless of its own success.
func TestRouterLateSuccessfulLoadClosesEngineAfterClose(t *testing.T) {
	loader := newObliviousLoader()
	router, err := NewRouter(RouterOptions{Store: singleEnglishStore(t, t.TempDir()), Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}

	predictDone := make(chan error, 1)
	go func() {
		_, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "english"})
		predictDone <- err
	}()
	<-loader.entered

	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	close(loader.release) // the loader "finishes" its successful build now.

	select {
	case err := <-predictDone:
		if err == nil {
			t.Fatal("Predict succeeded with an engine published after Close")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Predict did not resolve after its late-successful load")
	}

	loader.engine.mu.Lock()
	closed := loader.engine.closed
	loader.engine.mu.Unlock()
	if closed != 1 {
		t.Fatalf("late-loaded engine closed = %d, want exactly 1", closed)
	}
}

// TestRouterEvictionSkipsEntryStillLoading covers eviction racing a second
// alias's in-flight load: an idle loaded entry beyond MaxLoaded must be
// evicted, but an entry still loading (no engine yet) must never be chosen
// as a victim or otherwise disturbed by unrelated eviction pressure.
func TestRouterEvictionSkipsEntryStillLoading(t *testing.T) {
	root := t.TempDir()
	a := readyInstallation(t, root, "english")
	b := readyInstallation(t, root, "multilingual")
	c := readyInstallation(t, root, "typed-decisions")
	store := routerStore{
		catalog: []ModelDescriptor{LayaModels[0], LayaModels[1], LayaModels[2]},
		installations: map[string][]Installation{
			modelID("english"):         {a},
			modelID("multilingual"):    {b},
			modelID("typed-decisions"): {c},
		},
	}
	loader := newGatedLoader()
	router, err := NewRouter(RouterOptions{Store: store, Loader: loader, MaxLoaded: 2})
	if err != nil {
		t.Fatal(err)
	}

	// english loads fully and becomes idle.
	aGate := loader.gate("english")
	close(aGate.release)
	if _, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "english"}); err != nil {
		t.Fatal(err)
	}

	// multilingual's load starts and is deliberately left in flight.
	bGate := loader.gate("multilingual")
	bDone := make(chan error, 1)
	go func() {
		_, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "multilingual"})
		bDone <- err
	}()
	<-bGate.entered

	// typed-decisions loads fully, pushing entries beyond MaxLoaded=2
	// (english idle, multilingual loading, typed-decisions now loaded) —
	// eviction must remove english (the only idle, fully-loaded entry) and
	// must never touch multilingual.
	cGate := loader.gate("typed-decisions")
	close(cGate.release)
	if _, err := router.Predict(context.Background(), nil, oneChoiceQuestion, PredictOptions{Model: "typed-decisions"}); err != nil {
		t.Fatal(err)
	}

	aEngine := aGate.engine.(*routerEngine)
	aEngine.mu.Lock()
	aClosed := aEngine.closed
	aEngine.mu.Unlock()
	if aClosed != 1 {
		t.Fatalf("idle english engine closed = %d, want exactly 1 (evicted)", aClosed)
	}

	select {
	case err := <-bDone:
		t.Fatalf("multilingual load finished early (err=%v) — it must still be blocked on its own gate", err)
	default:
	}

	close(bGate.release)
	if err := <-bDone; err != nil {
		t.Fatalf("multilingual Predict error = %v, want nil (eviction must not have disturbed it)", err)
	}

	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
}

// TestRouterReleaseAfterCloseClosesBusyEngine covers a busy (refs > 0)
// already-loaded entry: Close marks it closing without touching the
// engine, and only the later releasing caller's release() actually closes
// it — never before, never twice.
func TestRouterReleaseAfterCloseClosesBusyEngine(t *testing.T) {
	loader := &routerLoader{}
	router, err := NewRouter(RouterOptions{Store: singleEnglishStore(t, t.TempDir()), Loader: loader, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}

	engine, _, release, err := router.acquire(context.Background(), "english")
	if err != nil {
		t.Fatal(err)
	}

	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	re := engine.(*routerEngine)
	re.mu.Lock()
	closedBeforeRelease := re.closed
	re.mu.Unlock()
	if closedBeforeRelease != 0 {
		t.Fatalf("busy engine closed = %d before release, want 0 (Close must defer to the releasing caller)", closedBeforeRelease)
	}

	release()

	re.mu.Lock()
	closedAfterRelease := re.closed
	re.mu.Unlock()
	if closedAfterRelease != 1 {
		t.Fatalf("busy engine closed = %d after release, want exactly 1", closedAfterRelease)
	}

	// A second release() call (e.g. a defer alongside an explicit call)
	// must never double-close — releaseFunc's sync.Once guards this.
	release()
	re.mu.Lock()
	closedTwice := re.closed
	re.mu.Unlock()
	if closedTwice != 1 {
		t.Fatalf("busy engine closed = %d after a second release(), want still exactly 1", closedTwice)
	}
}

// TestRouterCloseIsIdempotent covers repeated Close calls (e.g. a
// belt-and-braces defer alongside an explicit shutdown close).
func TestRouterCloseIsIdempotent(t *testing.T) {
	store := routerStore{catalog: LayaModels[:1]}
	router, err := NewRouter(RouterOptions{Store: store, Loader: &routerLoader{}, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err != nil {
		t.Fatal(err)
	}
	if err := router.Close(); err != nil {
		t.Fatalf("second Close() error = %v, want nil", err)
	}
}
