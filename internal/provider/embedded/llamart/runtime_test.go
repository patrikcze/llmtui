package llamart

import (
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/hybridgroup/yzma/pkg/llama"
	"github.com/hybridgroup/yzma/pkg/loader"

	"github.com/patrikcze/llmtui/internal/provider/embedded"
)

func TestRuntime_Probe(t *testing.T) {
	t.Setenv("YZMA_LIB", "")
	runtime := New()

	if err := runtime.Probe(embedded.Options{}); err == nil || !strings.Contains(err.Error(), "library path is unset") {
		t.Fatalf("Probe() error = %v, want unset-path guidance", err)
	}

	dir := t.TempDir()
	if err := runtime.Probe(embedded.Options{LibraryPath: dir}); err == nil || !strings.Contains(err.Error(), "fetch-llama-runtime.sh") {
		t.Fatalf("Probe() error = %v, want fetch-script guidance", err)
	}

	library := loader.GetLibraryFilename(dir, "llama")
	if err := os.WriteFile(library, []byte("test fixture"), 0o600); err != nil {
		t.Fatalf("write fake library: %v", err)
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

func TestEffectiveContextSize(t *testing.T) {
	tests := []struct {
		name       string
		configured int
		trained    int
		want       int
	}{
		{name: "caps large trained context", trained: 1_000_000, want: defaultContextSize},
		{name: "keeps smaller trained context", trained: 4096, want: 4096},
		{name: "configured lower than trained", configured: 2048, trained: 8192, want: 2048},
		{name: "configured cannot exceed trained", configured: 16384, trained: 8192, want: 8192},
		{name: "unknown trained context stays bounded", trained: 0, want: defaultContextSize},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := effectiveContextSize(tt.configured, tt.trained); got != tt.want {
				t.Errorf("effectiveContextSize(%d, %d) = %d, want %d", tt.configured, tt.trained, got, tt.want)
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
		{name: "request exceeds remaining", prompt: 190, requested: 20, context: 200, wantErr: "lower max_tokens"},
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
