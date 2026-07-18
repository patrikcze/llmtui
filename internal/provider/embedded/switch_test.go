package embedded

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/provider"
)

func TestExpandHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory available: %v", err)
	}
	tests := []struct{ in, want string }{
		{"~/models/x.gguf", filepath.Join(home, "models", "x.gguf")},
		{"~", home},
		{"/abs/path.gguf", "/abs/path.gguf"},
		{"relative.gguf", "relative.gguf"},
		{"~user/x.gguf", "~user/x.gguf"}, // ~user form is not expanded
		{"", ""},
	}
	for _, tt := range tests {
		if got := expandHome(tt.in); got != tt.want {
			t.Errorf("expandHome(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// chatAndDrain runs one Chat and returns all events, failing the test on a
// synchronous Chat error.
func chatAndDrain(t *testing.T, p *Provider, req provider.ChatRequest) []provider.ChatEvent {
	t.Helper()
	events, err := p.Chat(context.Background(), req)
	if err != nil {
		t.Fatalf("Chat: %v", err)
	}
	return drain(events)
}

func lastEvent(t *testing.T, events []provider.ChatEvent) provider.ChatEvent {
	t.Helper()
	if len(events) == 0 {
		t.Fatal("no events received")
	}
	return events[len(events)-1]
}

func TestChatWithSameModelPathDoesNotReload(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	f := &runtimeFactory{configure: func(rt *scriptedRuntime) { rt.genPieces = []string{"ok"} }}
	p := New("embedded", testOptions(modelPath), f.new)

	for i := 0; i < 2; i++ {
		got := chatAndDrain(t, p, provider.ChatRequest{Model: modelPath})
		if ev := lastEvent(t, got); ev.Type != provider.EventDone {
			t.Fatalf("chat %d: last event = %+v, want EventDone", i, ev)
		}
	}
	if f.count() != 1 {
		t.Fatalf("factory created %d runtimes, want 1 (no switch happened)", f.count())
	}
	if got := f.instance(0).loadCallCount(); got != 1 {
		t.Fatalf("Load called %d times, want 1 (req.Model == active path must not reload)", got)
	}
}

func TestChatSwitchesToSiblingModel(t *testing.T) {
	dir := t.TempDir()
	first := writeFakeModel(t, dir, "first.gguf")
	second := writeFakeModel(t, dir, "second.gguf")

	f := &runtimeFactory{configure: func(rt *scriptedRuntime) { rt.genPieces = []string{"hello"} }}
	p := New("embedded", testOptions(first), f.new)

	// Load the first model.
	chatAndDrain(t, p, provider.ChatRequest{Model: first})
	fingerprintBefore := p.RuntimeFingerprint()

	// Switch to the sibling.
	got := chatAndDrain(t, p, provider.ChatRequest{Model: second})

	var sawSwitchNote, sawDelta, sawDone bool
	for _, ev := range got {
		switch ev.Type {
		case provider.EventReasoning:
			if strings.Contains(ev.Delta, "switching model to second.gguf") {
				sawSwitchNote = true
			}
		case provider.EventDelta:
			sawDelta = true
		case provider.EventDone:
			sawDone = true
		case provider.EventError:
			t.Fatalf("unexpected error event during switch: %v", ev.Err)
		}
	}
	if !sawSwitchNote {
		t.Error("no 'switching model to …' reasoning event observed")
	}
	if !sawDelta || !sawDone {
		t.Errorf("deltas/done after switch: delta=%v done=%v, want both", sawDelta, sawDone)
	}

	// Old runtime closed exactly once, new instance created and loaded with
	// the new path.
	if f.count() != 2 {
		t.Fatalf("factory created %d runtimes, want 2 after one switch", f.count())
	}
	oldRt, newRt := f.instance(0), f.instance(1)
	if got := oldRt.closeCallCount(); got != 1 {
		t.Errorf("old runtime Close called %d times, want 1", got)
	}
	if paths := oldRt.loadedPaths(); len(paths) != 1 || paths[0] != first {
		t.Errorf("old runtime load paths = %v, want [%s]", paths, first)
	}
	if paths := newRt.loadedPaths(); len(paths) != 1 || paths[0] != second {
		t.Errorf("new runtime load paths = %v, want [%s]", paths, second)
	}

	// Active path updated: ListModels' first entry is now the new model.
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(models) == 0 || models[0].ID != second {
		t.Errorf("ListModels()[0].ID = %q, want %q after switch", models[0].ID, second)
	}

	// RuntimeFingerprint now stats the new file: it changed, and matches a
	// fresh provider configured directly with the new path.
	fingerprintAfter := p.RuntimeFingerprint()
	if fingerprintAfter == fingerprintBefore {
		t.Error("RuntimeFingerprint unchanged after model switch")
	}
	fresh := New("embedded", testOptions(second), f.new)
	if fingerprintAfter != fresh.RuntimeFingerprint() {
		t.Error("fingerprint after switch should equal that of a provider configured with the new path")
	}

	// Follow-up chat on the new model reuses the loaded engine.
	chatAndDrain(t, p, provider.ChatRequest{Model: second})
	if got := newRt.loadCallCount(); got != 1 {
		t.Errorf("new runtime Load called %d times, want 1 (stays loaded)", got)
	}
}

func TestSwitchToMissingModelKeepsEngineUsable(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	missing := dir + "/does-not-exist.gguf"

	f := &runtimeFactory{configure: func(rt *scriptedRuntime) { rt.genPieces = []string{"ok"} }}
	p := New("embedded", testOptions(modelPath), f.new)

	// Load the working model first.
	chatAndDrain(t, p, provider.ChatRequest{Model: modelPath})

	// Ask for a file that does not exist.
	got := chatAndDrain(t, p, provider.ChatRequest{Model: missing})
	ev := lastEvent(t, got)
	if ev.Type != provider.EventError {
		t.Fatalf("last event = %+v, want EventError for missing switch target", ev)
	}
	if !strings.Contains(ev.Err.Error(), "cannot switch to model") ||
		!strings.Contains(ev.Err.Error(), missing) ||
		!strings.Contains(ev.Err.Error(), modelPath) {
		t.Errorf("error %q should name the requested file and the currently loaded one", ev.Err)
	}

	// Validation must happen BEFORE the old runtime is touched: the working
	// engine was never closed, no new runtime was created.
	if got := f.instance(0).closeCallCount(); got != 0 {
		t.Fatalf("old runtime Close called %d times, want 0 (bad selection must never destroy a working engine)", got)
	}
	if f.count() != 1 {
		t.Fatalf("factory created %d runtimes, want 1 (no replacement on failed switch)", f.count())
	}

	// The original model still works without reloading.
	got = chatAndDrain(t, p, provider.ChatRequest{Model: modelPath})
	if ev := lastEvent(t, got); ev.Type != provider.EventDone {
		t.Fatalf("follow-up chat after failed switch: last event = %+v, want EventDone", ev)
	}
	if got := f.instance(0).loadCallCount(); got != 1 {
		t.Errorf("Load called %d times, want 1 (engine stayed loaded through the failed switch)", got)
	}
}

func TestVisionPairRejectsModelSwitchBeforeClosingRuntime(t *testing.T) {
	dir := t.TempDir()
	first := writeFakeModel(t, dir, "first.gguf")
	second := writeFakeModel(t, dir, "second.gguf")
	projector := writeFakeModel(t, dir, "mmproj-first.gguf")
	f := &runtimeFactory{configure: func(rt *scriptedRuntime) { rt.genPieces = []string{"ok"} }}
	p := New("embedded-vision", Options{ModelPath: first, MMProjPath: projector}, f.new)

	chatAndDrain(t, p, provider.ChatRequest{Model: first})
	got := chatAndDrain(t, p, provider.ChatRequest{Model: second})
	ev := lastEvent(t, got)
	if ev.Type != provider.EventError || !strings.Contains(ev.Err.Error(), "fixed pair") || !strings.Contains(ev.Err.Error(), "another embedded provider") {
		t.Fatalf("switch event = %+v, want actionable fixed-pair error", ev)
	}
	if f.count() != 1 || f.instance(0).closeCallCount() != 0 {
		t.Fatalf("failed pair switch changed runtime: instances=%d closes=%d", f.count(), f.instance(0).closeCallCount())
	}
}

func TestSwitchToDirectoryRejected(t *testing.T) {
	dir := t.TempDir()
	modelPath := writeFakeModel(t, dir, "model.gguf")
	f := &runtimeFactory{configure: func(rt *scriptedRuntime) { rt.genPieces = []string{"ok"} }}
	p := New("embedded", testOptions(modelPath), f.new)

	chatAndDrain(t, p, provider.ChatRequest{Model: modelPath})

	got := chatAndDrain(t, p, provider.ChatRequest{Model: dir})
	if ev := lastEvent(t, got); ev.Type != provider.EventError ||
		!strings.Contains(ev.Err.Error(), "cannot switch to model") {
		t.Fatalf("last event = %+v, want cannot-switch EventError for a directory", ev)
	}
	if got := f.instance(0).closeCallCount(); got != 0 {
		t.Errorf("old runtime Close called %d times, want 0", got)
	}
}

func TestSwitchBeforeFirstLoadKeepsSingleRuntime(t *testing.T) {
	// Switching before anything was loaded must not close/replace the
	// never-loaded runtime instance — just retarget it.
	dir := t.TempDir()
	first := writeFakeModel(t, dir, "first.gguf")
	second := writeFakeModel(t, dir, "second.gguf")

	f := &runtimeFactory{configure: func(rt *scriptedRuntime) { rt.genPieces = []string{"ok"} }}
	p := New("embedded", testOptions(first), f.new)

	got := chatAndDrain(t, p, provider.ChatRequest{Model: second})
	if ev := lastEvent(t, got); ev.Type != provider.EventDone {
		t.Fatalf("last event = %+v, want EventDone", ev)
	}
	if f.count() != 1 {
		t.Fatalf("factory created %d runtimes, want 1 (nothing was loaded, nothing to replace)", f.count())
	}
	if paths := f.instance(0).loadedPaths(); len(paths) != 1 || paths[0] != second {
		t.Errorf("load paths = %v, want [%s]", paths, second)
	}
	if got := f.instance(0).closeCallCount(); got != 0 {
		t.Errorf("Close called %d times on the retargeted runtime, want 0", got)
	}
}

func TestSwitchLoadFailureLeavesUnloadedAndRetries(t *testing.T) {
	dir := t.TempDir()
	first := writeFakeModel(t, dir, "first.gguf")
	second := writeFakeModel(t, dir, "second.gguf")

	failNextLoad := true
	f := &runtimeFactory{}
	f.configure = func(rt *scriptedRuntime) {
		rt.genPieces = []string{"ok"}
		if failNextLoad {
			rt.loadErr = errBoom
		}
	}

	// First instance loads fine.
	failNextLoad = false
	p := New("embedded", testOptions(first), f.new)
	chatAndDrain(t, p, provider.ChatRequest{Model: first})

	// The replacement instance created by the switch fails its load.
	failNextLoad = true
	got := chatAndDrain(t, p, provider.ChatRequest{Model: second})
	if ev := lastEvent(t, got); ev.Type != provider.EventError {
		t.Fatalf("last event = %+v, want EventError from failed post-switch load", ev)
	}
	if f.count() != 2 {
		t.Fatalf("factory created %d runtimes, want 2", f.count())
	}

	// State stayed unloaded with the new active path: the next Chat retries
	// loading the *new* model on the same (second) instance.
	secondRt := f.instance(1)
	secondRt.loadErr = nil
	got = chatAndDrain(t, p, provider.ChatRequest{Model: second})
	if ev := lastEvent(t, got); ev.Type != provider.EventDone {
		t.Fatalf("retry after failed switch-load: last event = %+v, want EventDone", ev)
	}
	if f.count() != 2 {
		t.Fatalf("factory created %d runtimes, want still 2 (retry must reuse the current instance)", f.count())
	}
	paths := secondRt.loadedPaths()
	if len(paths) != 2 || paths[0] != second || paths[1] != second {
		t.Errorf("second runtime load paths = %v, want [%s %s] (failed attempt + retry, both the new path)", paths, second, second)
	}
	models, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if models[0].ID != second {
		t.Errorf("active model = %q, want %q (switch commits the path even when the load fails)", models[0].ID, second)
	}
}

func TestCloseClosesCurrentRuntimeAfterSwitch(t *testing.T) {
	dir := t.TempDir()
	first := writeFakeModel(t, dir, "first.gguf")
	second := writeFakeModel(t, dir, "second.gguf")

	f := &runtimeFactory{configure: func(rt *scriptedRuntime) { rt.genPieces = []string{"ok"} }}
	p := New("embedded", testOptions(first), f.new)

	chatAndDrain(t, p, provider.ChatRequest{Model: first})
	chatAndDrain(t, p, provider.ChatRequest{Model: second})

	if err := p.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if got := f.instance(0).closeCallCount(); got != 1 {
		t.Errorf("first runtime Close called %d times, want 1 (closed by the switch)", got)
	}
	if got := f.instance(1).closeCallCount(); got != 1 {
		t.Errorf("second (current) runtime Close called %d times, want 1 (closed by Close)", got)
	}
}
