package decision

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Real checkpoints and Python are only required for this explicit opt-in.
func TestMLXIntegration(t *testing.T) {
	if os.Getenv("LLMTUI_TEST_LAYA_MLX") != "1" {
		t.Skip("set LLMTUI_TEST_LAYA_MLX=1 for real Metal inference")
	}
	if runtime.GOOS != "darwin" || runtime.GOARCH != "arm64" {
		t.Fatal("real MLX test requires macOS arm64")
	}
	manager, err := NewModelManager(ModelManagerOptions{RootDir: os.Getenv("LLMTUI_TEST_LAYA_MODEL_ROOT")})
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"english-mlx", "multilingual-mlx", "typed-decisions-mlx"} {
		t.Run(alias, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", "mlx", alias+".json"))
			if err != nil {
				t.Fatal(err)
			}
			var fixture GoldenFixture
			if err := json.Unmarshal(data, &fixture); err != nil {
				t.Fatal(err)
			}
			if err := ValidateGoldenFixture(fixture); err != nil {
				t.Fatal(err)
			}
			items, err := manager.Inspect(modelID(alias))
			if err != nil {
				t.Fatal(err)
			}
			var installation Installation
			for _, i := range items {
				if i.Manifest.Source.Revision == fixture.UpstreamRevision {
					installation = i
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			loaded, err := LoadRuntime(ctx, installation, MLXRuntimeLoader{Python: os.Getenv("LLMTUI_TEST_LAYA_PYTHON")})
			if err != nil {
				t.Fatal(err)
			}
			engine := loaded.(*MLXEngine)
			defer func() { _ = engine.Close() }()
			info := engine.RuntimeInfo()
			t.Logf("process/bootstrap %.2f ms; import %.2f ms; model load %.2f ms", info.StartupMS, info.ImportMS, info.LoadMS)
			for _, c := range fixture.Cases {
				var first time.Duration
				var warm time.Duration
				for n := range 6 {
					start := time.Now()
					got, err := engine.Predict(ctx, c.State, c.Questions, PredictOptions{})
					if err != nil {
						t.Fatal(err)
					}
					elapsed := time.Since(start)
					if n == 0 {
						first = elapsed
					} else {
						warm += elapsed
					}
					compareMLXGolden(t, c.Expected, got, 0.002)
					if got.Answers["department"].Choice != "billing" {
						t.Fatalf("department=%s", got.Answers["department"].Choice)
					}
					// Routing.Revision is bound by Router.Predict (see
					// TestRouterBindsLoadedRevisionNotNewestCatalog), not by
					// the engine directly — this test calls engine.Predict
					// without a Router, so Routing.Revision is intentionally
					// not asserted here.
					// Phase 0b: real tokenizer-backed capacity diagnostics
					// for this small, non-truncated golden fixture state —
					// every question gets a usage entry, and none of this
					// fixture's short questions should be flagged truncated.
					if len(got.InputUsage) != len(c.Questions) {
						t.Fatalf("InputUsage has %d entries, want one per question (%d)", len(got.InputUsage), len(c.Questions))
					}
					for id, usage := range got.InputUsage {
						if usage.Truncated() {
							t.Fatalf("question %q unexpectedly truncated on a small golden fixture: %+v", id, usage)
						}
						if usage.MaxLen <= 0 || usage.HeadTokens <= 0 {
							t.Fatalf("question %q has implausible usage: %+v", id, usage)
						}
					}
				}
				t.Logf("%s: first %s; warm mean (5) %s", c.Name, first, warm/5)

				// Phase 0b: strict admission must succeed unchanged on input
				// that comfortably fits the budget.
				strictGot, err := engine.Predict(ctx, c.State, c.Questions, PredictOptions{RequireCompleteInput: true})
				if err != nil {
					t.Fatalf("strict predict on small input: %v", err)
				}
				compareMLXGolden(t, c.Expected, strictGot, 0.002)

				// Phase 0b: strict admission must reject — before any
				// answer is produced — input that would lose content to
				// the tokenizer's budget, and must leave the worker usable
				// for the next (non-strict or fitting) request.
				overflowState := map[string]any{"body": strings.Repeat("duplicate charge refund billing dispute ", 400)}
				if _, err := engine.Predict(ctx, overflowState, c.Questions, PredictOptions{RequireCompleteInput: true}); err == nil || !errors.Is(err, ErrInvalid) {
					t.Fatalf("strict predict on oversized input: err = %v, want ErrInvalid", err)
				}
				if _, err := engine.Predict(ctx, c.State, c.Questions, PredictOptions{}); err != nil {
					t.Fatalf("engine unusable after a strict capacity rejection: %v", err)
				}
			}
		})
	}
}
func compareMLXGolden(t *testing.T, want, got Result, tolerance float64) {
	t.Helper()
	if len(want.Answers) != len(got.Answers) {
		t.Fatal("answer count mismatch")
	}
	for id, w := range want.Answers {
		g := got.Answers[id]
		if w.Type != g.Type || w.Choice != g.Choice || math.Abs(w.Score-g.Score) > tolerance || math.Abs(w.Probability-g.Probability) > tolerance || math.Abs(w.Confidence-g.Confidence) > tolerance {
			t.Fatalf("%s: got %+v want %+v", id, g, w)
		}
		if len(w.Probabilities) != len(g.Probabilities) {
			t.Fatalf("%s: probability count mismatch", id)
		}
		for key, p := range w.Probabilities {
			if gp, ok := g.Probabilities[key]; !ok || math.Abs(p-gp) > tolerance {
				t.Fatalf("%s/%s probability mismatch", id, key)
			}
		}
	}
}
