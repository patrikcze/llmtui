package embedded

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/patrikcze/llmtui/internal/provider"
)

func writeFakeModel(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("fake gguf content"), 0o600); err != nil {
		t.Fatalf("write fake model: %v", err)
	}
	return path
}

func TestChatStreamsDeltasInOrderThenDone(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{genPieces: []string{"hello", " ", "world"}}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	events, err := p.Chat(context.Background(), provider.ChatRequest{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}}})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	got := drain(events)

	var deltas []string
	var done *provider.ChatEvent
	for i := range got {
		ev := got[i]
		switch ev.Type {
		case provider.EventDelta:
			deltas = append(deltas, ev.Delta)
		case provider.EventDone:
			done = &got[i]
		case provider.EventError:
			t.Fatalf("unexpected error event: %v", ev.Err)
		}
	}
	if strings.Join(deltas, "") != "hello world" {
		t.Fatalf("deltas = %q, want %q", strings.Join(deltas, ""), "hello world")
	}
	if done == nil {
		t.Fatal("no EventDone received")
	}
	if done.Usage == nil {
		t.Fatal("EventDone has no usage")
	}
	if done.Usage.Estimated {
		t.Error("usage should not be marked estimated")
	}
	if done.Usage.PromptTokens != 7 || done.Usage.CompletionTokens != 3 || done.Usage.TotalTokens != 10 {
		t.Errorf("usage = %+v, want prompt=7 completion=3 total=10", done.Usage)
	}
}

func TestModelLoadsOnceAcrossSequentialChats(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{genPieces: []string{"a"}}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	for i := 0; i < 2; i++ {
		events, err := p.Chat(context.Background(), provider.ChatRequest{})
		if err != nil {
			t.Fatalf("Chat[%d]: %v", i, err)
		}
		drain(events)
	}
	if got := rt.loadCallCount(); got != 1 {
		t.Fatalf("Load called %d times, want 1", got)
	}
	if got := rt.genCallCount(); got != 2 {
		t.Fatalf("Generate called %d times, want 2", got)
	}
}

func TestLoadFailureEmitsErrorAndAllowsRetry(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{loadErr: errBoom}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	events, err := p.Chat(context.Background(), provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	got := drain(events)
	if len(got) == 0 || got[len(got)-1].Type != provider.EventError {
		t.Fatalf("expected trailing EventError, got %+v", got)
	}
	if !errors.Is(got[len(got)-1].Err, errBoom) {
		t.Errorf("error = %v, want wrapping %v", got[len(got)-1].Err, errBoom)
	}

	// Retry: clear the load error, next Chat should attempt Load again and
	// succeed.
	rt.loadErr = nil
	rt.genPieces = []string{"ok"}
	events2, err := p.Chat(context.Background(), provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat retry: %v", err)
	}
	got2 := drain(events2)
	foundDone := false
	for _, ev := range got2 {
		if ev.Type == provider.EventDone {
			foundDone = true
		}
		if ev.Type == provider.EventError {
			t.Fatalf("unexpected error on retry: %v", ev.Err)
		}
	}
	if !foundDone {
		t.Fatal("no EventDone on retry")
	}
	if got := rt.loadCallCount(); got != 2 {
		t.Fatalf("Load called %d times across failure+retry, want 2", got)
	}
}

func TestCancellationMidGenerationIsTerminalAndEngineReusable(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{blockUntilCanceled: true}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	ctx, cancel := context.WithCancel(context.Background())
	events, err := p.Chat(ctx, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	done := make(chan []provider.ChatEvent, 1)
	go func() { done <- drain(events) }()

	time.Sleep(20 * time.Millisecond)
	cancel()

	var got []provider.ChatEvent
	select {
	case got = <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("producer did not exit after cancellation")
	}
	if len(got) == 0 || got[len(got)-1].Type != provider.EventError {
		t.Fatalf("expected trailing EventError, got %+v", got)
	}
	if !errors.Is(got[len(got)-1].Err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", got[len(got)-1].Err)
	}

	// Engine must remain usable: a following Chat call succeeds.
	rt.blockUntilCanceled = false
	rt.genPieces = []string{"back"}
	events2, err := p.Chat(context.Background(), provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat after cancel: %v", err)
	}
	got2 := drain(events2)
	sawDone := false
	for _, ev := range got2 {
		if ev.Type == provider.EventDone {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatalf("expected EventDone after recovering from cancellation, got %+v", got2)
	}
	// Load should not be called again: the model stayed loaded.
	if got := rt.loadCallCount(); got != 1 {
		t.Fatalf("Load called %d times, want 1 (model should stay loaded across cancellation)", got)
	}
}

func TestCloseDuringInFlightGenerationAbortsAndClosesRuntime(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{blockUntilCanceled: true}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	events, err := p.Chat(context.Background(), provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	drainDone := make(chan struct{})
	go func() {
		drain(events)
		close(drainDone)
	}()

	time.Sleep(20 * time.Millisecond)

	closeDone := make(chan error, 1)
	go func() { closeDone <- p.Close() }()

	select {
	case err := <-closeDone:
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return (deadlock)")
	}
	select {
	case <-drainDone:
	case <-time.After(2 * time.Second):
		t.Fatal("generation did not wind down after Close")
	}
	if got := rt.closeCallCount(); got != 1 {
		t.Fatalf("rt.Close called %d times, want 1", got)
	}

	// Subsequent Chat calls must error.
	if _, err := p.Chat(context.Background(), provider.ChatRequest{}); err == nil {
		t.Fatal("Chat after Close should error")
	}
}

func TestCloseIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	if err := p.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if got := rt.closeCallCount(); got != 1 {
		t.Fatalf("rt.Close called %d times, want 1", got)
	}
}

func TestChatWithToolsRejectsSynchronouslyAndMatchesFallbackDetector(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	_, err := p.Chat(context.Background(), provider.ChatRequest{
		Tools: []provider.ToolSpec{{Name: "read_file"}},
	})
	if err == nil {
		t.Fatal("expected error for native tool request")
	}
	if !toolsRejectedErrorLike(err) {
		t.Fatalf("error %q does not match the TUI's toolsRejectedError predicate (needs \"tool\" + \"does not support\")", err.Error())
	}
	if got := rt.loadCallCount(); got != 0 {
		t.Fatalf("Load should not be called for a rejected tool request, got %d calls", got)
	}
}

// toolsRejectedErrorLike mirrors internal/tui/pipeline.go's toolsRejectedError
// predicate (substring "tool" plus "does not support") without importing the
// tui package, which would create an import cycle risk and pull in
// unrelated dependencies for this provider-only test.
func toolsRejectedErrorLike(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	if !strings.Contains(s, "tool") {
		return false
	}
	return strings.Contains(s, "does not support") || strings.Contains(s, "not supported") ||
		strings.Contains(s, "status 400") || strings.Contains(s, "status 422") ||
		strings.Contains(s, "invalid")
}

func TestStreamProducerExitsWhenAbandoned(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{genPieces: []string{"a", "b", "c"}, genDelay: 20 * time.Millisecond}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))

	ctx, cancel := context.WithCancel(context.Background())
	events, err := p.Chat(ctx, provider.ChatRequest{})
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}

	<-events // read exactly one event (the "loading model" reasoning line)
	cancel() // then walk away: cancel and stop reading

	producerDone := make(chan struct{})
	go func() {
		// Nobody else reads events; the producer must still exit and close
		// the channel on its own via TryEmit, not block forever.
		for range events {
		}
		close(producerDone)
	}()

	select {
	case <-producerDone:
	case <-time.After(2 * time.Second):
		t.Fatal("producer goroutine still blocked after cancel+abandon")
	}
}

func TestHealthCheckEmptyModelPath(t *testing.T) {
	p := New("embedded", Options{}, fixedRuntime(&scriptedRuntime{}))
	err := p.HealthCheck(context.Background())
	if err == nil || !strings.Contains(err.Error(), "no model configured") {
		t.Fatalf("HealthCheck error = %v, want actionable 'no model configured' message", err)
	}
}

func TestHealthCheckMissingFile(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "does-not-exist.gguf")
	p := New("embedded", testOptions(missing), fixedRuntime(&scriptedRuntime{}))
	err := p.HealthCheck(context.Background())
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("HealthCheck error = %v, want 'not found' message", err)
	}
}

func TestHealthCheckDirectoryInsteadOfFile(t *testing.T) {
	dir := t.TempDir()
	p := New("embedded", testOptions(dir), fixedRuntime(&scriptedRuntime{}))
	err := p.HealthCheck(context.Background())
	if err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("HealthCheck error = %v, want 'directory' message", err)
	}
}

func TestHealthCheckProbeError(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{probeErr: errBoom}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))
	err := p.HealthCheck(context.Background())
	if err == nil || !errors.Is(err, errBoom) {
		t.Fatalf("HealthCheck error = %v, want wrapping %v", err, errBoom)
	}
}

func TestHealthCheckNeverLoadsModel(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	rt := &scriptedRuntime{}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))
	if err := p.HealthCheck(context.Background()); err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if got := rt.loadCallCount(); got != 0 {
		t.Fatalf("HealthCheck must never call Load, but Load was called %d times", got)
	}
}

func TestListModelsIncludesSiblingGGUFFiles(t *testing.T) {
	dir := t.TempDir()
	main := writeFakeModel(t, dir, "primary.gguf")
	writeFakeModel(t, dir, "sibling-a.gguf")
	writeFakeModel(t, dir, "sibling-b.gguf")
	// Non-gguf files must be ignored.
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	p := New("embedded", testOptions(main), fixedRuntime(&scriptedRuntime{}))
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) != 3 {
		t.Fatalf("ListModels returned %d entries, want 3: %+v", len(models), models)
	}
	if models[0].ID != main {
		t.Errorf("first entry should be the configured model, got %+v", models[0])
	}

	var ids []string
	for _, m := range models {
		ids = append(ids, m.ID)
	}
	sort.Strings(ids)
	wantSibA := filepath.Join(dir, "sibling-a.gguf")
	wantSibB := filepath.Join(dir, "sibling-b.gguf")
	found := map[string]bool{}
	for _, id := range ids {
		found[id] = true
	}
	if !found[wantSibA] || !found[wantSibB] {
		t.Errorf("expected siblings %q and %q in %v", wantSibA, wantSibB, ids)
	}
}

func TestListModelsMissingDirectoryReturnsConfiguredOnly(t *testing.T) {
	missingDir := filepath.Join(os.TempDir(), "llmtui-embedded-test-missing-dir-xyz")
	modelPath := filepath.Join(missingDir, "model.gguf")
	p := New("embedded", testOptions(modelPath), fixedRuntime(&scriptedRuntime{}))
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels should not error on missing dir: %v", err)
	}
	if len(models) != 1 || models[0].ID != modelPath {
		t.Fatalf("ListModels = %+v, want single configured entry", models)
	}
}

func TestCapabilities(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	p := New("embedded", Options{ModelPath: modelPath, ContextSize: 2048}, fixedRuntime(&scriptedRuntime{}))
	caps := p.Capabilities()
	if !caps.SupportsStreaming || !caps.SupportsModelList || !caps.SupportsTokenUsage || !caps.SupportsSystemPrompt {
		t.Fatalf("Capabilities missing expected true flags: %+v", caps)
	}
	if caps.ContextWindowTokens != 2048 {
		t.Errorf("ContextWindowTokens = %d, want 2048 (unloaded, falls back to configured)", caps.ContextWindowTokens)
	}
}

func TestRuntimeFingerprintChangesWithOptionsAndModelFile(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")

	base := Options{ModelPath: modelPath, ContextSize: 4096, GPULayers: -1, Sampling: Sampling{TopK: 40}}
	p1 := New("embedded", base, fixedRuntime(&scriptedRuntime{}))
	f1 := p1.RuntimeFingerprint()
	f1again := p1.RuntimeFingerprint()
	if f1 != f1again {
		t.Errorf("fingerprint not stable across repeated calls: %q vs %q", f1, f1again)
	}

	changedCtx := base
	changedCtx.ContextSize = 8192
	p2 := New("embedded", changedCtx, fixedRuntime(&scriptedRuntime{}))
	if p2.RuntimeFingerprint() == f1 {
		t.Error("fingerprint did not change when ContextSize changed")
	}

	changedGPU := base
	changedGPU.GPULayers = 0
	p3 := New("embedded", changedGPU, fixedRuntime(&scriptedRuntime{}))
	if p3.RuntimeFingerprint() == f1 {
		t.Error("fingerprint did not change when GPULayers changed")
	}

	changedSampling := base
	changedSampling.Sampling.Stop = []string{"STOP"}
	p4 := New("embedded", changedSampling, fixedRuntime(&scriptedRuntime{}))
	if p4.RuntimeFingerprint() == f1 {
		t.Error("fingerprint did not change when Sampling.Stop changed")
	}

	// Changing the model file's content (size/mtime) must change the
	// fingerprint even though Options is identical.
	if runtime.GOOS != "windows" {
		time.Sleep(10 * time.Millisecond)
	}
	if err := os.WriteFile(modelPath, []byte("different content, different size!!"), 0o600); err != nil {
		t.Fatal(err)
	}
	p5 := New("embedded", base, fixedRuntime(&scriptedRuntime{}))
	if p5.RuntimeFingerprint() == f1 {
		t.Error("fingerprint did not change when the model file's size/mtime changed")
	}
}

// TestGenerateSpawnsNoLeakedGoroutines proves the closeCtx watcher goroutine
// started per-generation (see Provider.generate) always exits: it must not
// accumulate across many successful, failed, and canceled generations.
func TestGenerateSpawnsNoLeakedGoroutines(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")

	settle := func() {
		for i := 0; i < 5; i++ {
			runtime.Gosched()
		}
		time.Sleep(10 * time.Millisecond)
	}

	baseline := func() int {
		runtime.GC()
		settle()
		return runtime.NumGoroutine()
	}

	before := baseline()

	// Successful generations.
	rt := &scriptedRuntime{genPieces: []string{"a", "b"}}
	p := New("embedded", testOptions(modelPath), fixedRuntime(rt))
	for i := 0; i < 20; i++ {
		events, err := p.Chat(context.Background(), provider.ChatRequest{})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		drain(events)
	}
	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Canceled generations (fresh provider so Load runs each time is not
	// required; blockUntilCanceled exercises the watcher's cancel path).
	rt2 := &scriptedRuntime{blockUntilCanceled: true}
	p2 := New("embedded", testOptions(modelPath), fixedRuntime(rt2))
	for i := 0; i < 20; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		events, err := p2.Chat(ctx, provider.ChatRequest{})
		if err != nil {
			t.Fatalf("Chat: %v", err)
		}
		go cancel()
		drain(events)
	}
	if err := p2.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	after := baseline()
	if after > before+5 { // small slack for unrelated runtime/test goroutines
		t.Errorf("goroutine count grew from %d to %d after 40 generations; suspected leak", before, after)
	}
}

func TestRuntimeFingerprintStableWhenNothingChanges(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	opts := Options{ModelPath: modelPath, ContextSize: 4096}
	p1 := New("embedded", opts, fixedRuntime(&scriptedRuntime{}))
	p2 := New("embedded", opts, fixedRuntime(&scriptedRuntime{}))
	if p1.RuntimeFingerprint() != p2.RuntimeFingerprint() {
		t.Error("two providers with identical options and the same model file should fingerprint identically")
	}
}
