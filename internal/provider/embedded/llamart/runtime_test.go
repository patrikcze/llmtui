package llamart

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/hybridgroup/yzma/pkg/loader"

	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestRuntime_Probe(t *testing.T) {
	t.Setenv("YZMA_LIB", "")
	// Isolate every resolver tier that reads ambient machine state (bundled
	// exe-relative dir, managed user dir, legacy ~/.local/share directory) so
	// this test is hermetic on a developer machine that has a real runtime
	// installed. Without this, tier 5 (legacy) can pick up a real prior
	// `scripts/fetch-llama-runtime.sh`/`llmtui runtime install` result and
	// this test's very first assertion (total-resolution-failure) fails.
	emptyHome := t.TempDir()
	t.Setenv("HOME", emptyHome)
	t.Setenv("XDG_DATA_HOME", filepath.Join(emptyHome, "xdg-data"))
	t.Setenv("LOCALAPPDATA", filepath.Join(emptyHome, "local-appdata"))
	runtime := New()

	if err := runtime.Probe(embedded.Options{}); err == nil || !strings.Contains(err.Error(), "llmtui runtime install") {
		t.Fatalf("Probe() error = %v, want runtime-install guidance", err)
	}

	dir := t.TempDir()
	if err := runtime.Probe(embedded.Options{LibraryPath: dir}); err == nil || !strings.Contains(err.Error(), "llmtui runtime install") {
		t.Fatalf("Probe() error = %v, want runtime-install guidance", err)
	}

	library := loader.GetLibraryFilename(dir, "llama")
	if err := os.WriteFile(library, []byte("test fixture"), 0o600); err != nil {
		t.Fatalf("write fake library: %v", err)
	}
	if err := runtime.Probe(embedded.Options{LibraryPath: dir}); err == nil || !strings.Contains(err.Error(), "incomplete llama.cpp runtime") {
		t.Fatalf("Probe() missing backend error = %v", err)
	}
	for _, pattern := range requiredLibraryPatterns() {
		name := strings.ReplaceAll(pattern, "*", "")
		if err := os.WriteFile(filepath.Join(dir, name), []byte("test fixture"), 0o600); err != nil {
			t.Fatalf("write fake required library %q: %v", name, err)
		}
	}
	if err := runtime.Probe(embedded.Options{LibraryPath: dir}); err != nil {
		t.Fatalf("Probe() = %v, want stat-only success", err)
	}

	model := filepath.Join(dir, "model.gguf")
	projector := filepath.Join(dir, "mmproj-model.gguf")
	if err := os.WriteFile(model, []byte("model"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Probe(embedded.Options{LibraryPath: dir, ModelPath: model, MMProjPath: projector}); err == nil || !strings.Contains(err.Error(), "vision projector not found") {
		t.Fatalf("Probe() missing projector error = %v", err)
	}
	if err := os.WriteFile(projector, []byte("projector"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Probe(embedded.Options{LibraryPath: dir, ModelPath: model, MMProjPath: projector}); err == nil || !strings.Contains(err.Error(), "mtmd vision library not found") {
		t.Fatalf("Probe() missing mtmd error = %v", err)
	}
	mtmdLibrary := loader.GetLibraryFilename(dir, "mtmd")
	if err := os.WriteFile(mtmdLibrary, []byte("test fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Probe(embedded.Options{LibraryPath: dir, ModelPath: model, MMProjPath: projector}); err != nil {
		t.Fatalf("Probe() vision pair = %v, want stat-only success", err)
	}
}

func TestResolveLibraryDir(t *testing.T) {
	t.Setenv("YZMA_LIB", filepath.Join("env", "libs"))

	tests := []struct {
		name string
		opts embedded.Options
		want string
	}{
		{
			name: "explicit path",
			opts: embedded.Options{LibraryPath: filepath.Join("configured", "libs")},
			want: filepath.Join("configured", "libs"),
		},
		{
			name: "environment fallback",
			want: filepath.Join("env", "libs"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveLibraryDir(tt.opts)
			if err != nil {
				t.Fatalf("resolveLibraryDir() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveLibraryDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBackendInitRetriesAfterLoadFailure(t *testing.T) {
	var state backendState
	loadCalls := 0
	load := func(string) error {
		loadCalls++
		if loadCalls == 1 {
			return os.ErrNotExist
		}
		return nil
	}
	initializeCalls := 0

	if err := state.initLlama("bad", load, func() { initializeCalls++ }); err == nil {
		t.Fatal("first initLlama() error = nil, want load failure")
	}
	if state.dir != "" || state.llamaInitialized {
		t.Fatalf("failed initialization poisoned state: dir=%q initialized=%v", state.dir, state.llamaInitialized)
	}
	if err := state.initLlama("good", load, func() { initializeCalls++ }); err != nil {
		t.Fatalf("retry initLlama() error = %v", err)
	}
	if state.dir != "good" || !state.llamaInitialized {
		t.Fatalf("successful retry state: dir=%q initialized=%v", state.dir, state.llamaInitialized)
	}
	if loadCalls != 2 || initializeCalls != 1 {
		t.Fatalf("load calls = %d, initialize calls = %d; want 2, 1", loadCalls, initializeCalls)
	}
}

func TestAbortCallbackIsProcessGlobal(t *testing.T) {
	first := processAbortCallback()
	for range 100 {
		if got := processAbortCallback(); got != first {
			t.Fatalf("processAbortCallback() = %d, want stable %d", got, first)
		}
	}
}

func TestAbortSignalRegistration(t *testing.T) {
	var signal atomic.Bool
	id := registerAbortSignal(&signal)
	if _, ok := abortCallbacks.registered.Load(id); !ok {
		t.Fatal("registered abort signal is missing")
	}
	unregisterAbortSignal(id)
	if _, ok := abortCallbacks.registered.Load(id); ok {
		t.Fatal("unregistered abort signal remains reachable")
	}
}

func TestEffectiveContextSize(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		trained    int
		extend     bool
		want       int
	}{
		{name: "caps large trained context", trained: 1_000_000, want: defaultContextSize},
		{name: "keeps smaller trained context", trained: 4096, want: 4096},
		{name: "configured lower than trained", configured: 2048, trained: 8192, want: 2048},
		{name: "configured cannot exceed trained", configured: 16384, trained: 8192, want: 8192},
		{name: "explicit scaling extends trained context", configured: 16384, trained: 8192, extend: true, want: 16384},
		{name: "unknown trained context stays bounded", trained: 0, want: defaultContextSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveContextSize(tt.configured, tt.trained, tt.extend); got != tt.want {
				t.Errorf("effectiveContextSize(%d, %d, %t) = %d, want %d", tt.configured, tt.trained, tt.extend, got, tt.want)
			}
		})
	}
}

func TestBuildContextParamsAppliesRopeScalingOverrides(t *testing.T) {
	freqBase, freqScale := 10_000_000.0, 0.25
	extFactor, attnFactor := 1.0, 0.1
	betaFast, betaSlow, origCtx := 32.0, 1.0, 262_144
	scaling := embedded.RopeScaling{
		Type:       embedded.RopeScalingYARN,
		FreqBase:   &freqBase,
		FreqScale:  &freqScale,
		ExtFactor:  &extFactor,
		AttnFactor: &attnFactor,
		BetaFast:   &betaFast,
		BetaSlow:   &betaSlow,
		OrigCtx:    &origCtx,
	}
	params := llama.ContextParams{}
	err := applyRopeScaling(&params, scaling)
	if err != nil {
		t.Fatalf("applyRopeScaling() error = %v", err)
	}
	if params.RopeScalingType != llama.RopeScalingTypeYARN ||
		params.RopeFreqBase != float32(freqBase) || params.RopeFreqScale != float32(freqScale) ||
		params.YarnExtFactor != float32(extFactor) || params.YarnAttnFactor != float32(attnFactor) ||
		params.YarnBetaFast != float32(betaFast) || params.YarnBetaSlow != float32(betaSlow) ||
		params.YarnOrigCtx != uint32(origCtx) {
		t.Fatalf("RoPE/YaRN context params not applied: %+v", params)
	}
}

func TestBuildContextParamsPreservesRopeDefaults(t *testing.T) {
	want := llama.ContextParams{
		RopeScalingType: llama.RopeScalingTypeUnspecified,
		RopeFreqBase:    123, RopeFreqScale: 456, YarnExtFactor: 789,
		YarnAttnFactor: 12, YarnBetaFast: 34, YarnBetaSlow: 56, YarnOrigCtx: 78,
	}
	got := want
	err := applyRopeScaling(&got, embedded.RopeScaling{})
	if err != nil {
		t.Fatalf("applyRopeScaling() error = %v", err)
	}
	if got.RopeScalingType != want.RopeScalingType || got.RopeFreqBase != want.RopeFreqBase ||
		got.RopeFreqScale != want.RopeFreqScale || got.YarnExtFactor != want.YarnExtFactor ||
		got.YarnAttnFactor != want.YarnAttnFactor || got.YarnBetaFast != want.YarnBetaFast ||
		got.YarnBetaSlow != want.YarnBetaSlow || got.YarnOrigCtx != want.YarnOrigCtx {
		t.Fatalf("unset RoPE options changed llama.cpp defaults: got %+v want %+v", got, want)
	}
}

func TestValidateSamplingDRY(t *testing.T) {
	valid := embedded.Sampling{DRYMultiplier: 0.8, DRYBase: 1.75, DRYAllowedLength: 2, DRYPenaltyLastN: -1}
	if err := validateSampling(valid); err != nil {
		t.Fatalf("valid DRY settings rejected: %v", err)
	}
	for name, sampling := range map[string]embedded.Sampling{
		"negative multiplier": {DRYMultiplier: -1},
		"zero base":           {DRYMultiplier: 0.8},
		"negative allowed":    {DRYMultiplier: 0.8, DRYBase: 1.75, DRYAllowedLength: -1},
		"invalid last n":      {DRYMultiplier: 0.8, DRYBase: 1.75, DRYPenaltyLastN: -2},
		"NUL breaker":         {DRYMultiplier: 0.8, DRYBase: 1.75, DRYSequenceBreakers: []string{"\x00"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := validateSampling(sampling); err == nil {
				t.Fatal("validateSampling() error = nil")
			}
		})
	}
}

func TestGenerationBudget(t *testing.T) {
	tests := []struct {
		name      string
		prompt    int
		requested int
		context   int
		want      int
		wantErr   string
	}{
		{name: "explicit budget", prompt: 100, requested: 20, context: 200, want: 20},
		{name: "zero uses remaining context", prompt: 100, context: 200, want: 100},
		{name: "prompt fills context", prompt: 200, requested: 1, context: 200, wantErr: "raise context_size"},
		{name: "request is clamped to remaining context", prompt: 190, requested: 20, context: 200, want: 10},
		{name: "reported Gemma image-sized budget", prompt: 4220, requested: 4096, context: 8192, want: 3972},
		{name: "negative request", prompt: 10, requested: -1, context: 200, wantErr: "must not be negative"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := generationBudget(tt.prompt, tt.requested, tt.context)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("generationBudget() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("generationBudget() error = %v", err)
			}
			if got != tt.want {
				t.Errorf("generationBudget() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestCommonPrefix(t *testing.T) {
	tests := []struct {
		name  string
		left  []llama.Token
		right []llama.Token
		want  int
	}{
		{name: "empty", left: []llama.Token{}, right: []llama.Token{}, want: 0},
		{name: "none", left: []llama.Token{1}, right: []llama.Token{2}, want: 0},
		{name: "partial", left: []llama.Token{1, 2, 3}, right: []llama.Token{1, 2, 4}, want: 2},
		{name: "left prefix", left: []llama.Token{1, 2}, right: []llama.Token{1, 2, 3}, want: 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := commonPrefix(tt.left, tt.right); got != tt.want {
				t.Errorf("commonPrefix() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestGPULayerCount(t *testing.T) {
	if got, err := gpuLayerCount(-1); err != nil || got != allGPULayers {
		t.Fatalf("gpuLayerCount(-1) = %d, %v; want %d, nil", got, err, allGPULayers)
	}
	if _, err := gpuLayerCount(-2); err == nil {
		t.Fatal("gpuLayerCount(-2) error = nil, want validation error")
	}
	if strconv.IntSize > 32 {
		if _, err := gpuLayerCount(int64ToInt(math.MaxInt32 + 1)); err == nil {
			t.Fatal("gpuLayerCount(MaxInt32+1) error = nil, want validation error")
		}
	}
}

func int64ToInt(value int64) int {
	return int(value)
}
