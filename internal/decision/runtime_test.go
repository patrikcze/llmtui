package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type testRuntimeLoader struct {
	called bool
	engine Engine
	err    error
}

func (l *testRuntimeLoader) Load(context.Context, Installation) (Engine, error) {
	l.called = true
	return l.engine, l.err
}

func TestLoadRuntimeRejectsSourceCheckpoint(t *testing.T) {
	loader := &testRuntimeLoader{engine: UnavailableEngine{}}
	engine, err := LoadRuntime(context.Background(), Installation{
		Valid: true,
		Manifest: ModelManifest{Runtime: RuntimeArtifact{
			Format: RuntimeFormatSafeTensors,
		}},
	}, loader)
	if !errors.Is(err, ErrRuntimeArtifactUnavailable) {
		t.Fatalf("LoadRuntime() error = %v, want ErrRuntimeArtifactUnavailable", err)
	}
	if loader.called {
		t.Fatal("runtime loader was called for a source-only checkpoint")
	}
	if engine == nil || engine.Name() != "unavailable" {
		t.Fatalf("LoadRuntime() engine = %#v, want unavailable engine", engine)
	}
}

func TestLoadRuntimeVerifiesArtifactBeforeLoader(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runtime.onnx")
	data := []byte("verified runtime")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	loader := &testRuntimeLoader{engine: UnavailableEngine{Reason: "test"}}
	engine, err := LoadRuntime(context.Background(), Installation{
		Path:  dir,
		Valid: true,
		Manifest: ModelManifest{Runtime: RuntimeArtifact{
			Format:           RuntimeFormatONNX,
			Ready:            true,
			Path:             "runtime.onnx",
			Size:             int64(len(data)),
			SHA256:           hex.EncodeToString(sum[:]),
			Exporter:         "test-exporter@1",
			UpstreamRevision: "abc123",
		}},
	}, loader)
	if err != nil {
		t.Fatalf("LoadRuntime() error = %v", err)
	}
	if !loader.called || engine == nil {
		t.Fatalf("LoadRuntime() did not call loader: called=%v engine=%#v", loader.called, engine)
	}

	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	loader.called = false
	if _, err := LoadRuntime(context.Background(), Installation{
		Path:  dir,
		Valid: true,
		Manifest: ModelManifest{Runtime: RuntimeArtifact{
			Format:           RuntimeFormatONNX,
			Ready:            true,
			Path:             "runtime.onnx",
			Size:             int64(len(data)),
			SHA256:           hex.EncodeToString(sum[:]),
			Exporter:         "test-exporter@1",
			UpstreamRevision: "abc123",
		}},
	}, loader); !errors.Is(err, ErrRuntimeArtifactUnavailable) {
		t.Fatalf("tampered LoadRuntime() error = %v, want unavailable", err)
	}
	if loader.called {
		t.Fatal("runtime loader was called for a tampered artifact")
	}
}

func TestValidateGoldenFixture(t *testing.T) {
	fixture := GoldenFixture{
		SchemaVersion:    GoldenFixtureSchema,
		UpstreamRevision: "abc123",
		Cases: []GoldenCase{{
			Name:      "choice",
			State:     []byte(`{"text":"hello"}`),
			Questions: map[string]Question{"q": {Type: QuestionChoice, Criteria: []string{"yes", "no"}}},
			Expected:  Result{Answers: map[string]Answer{"q": {Type: QuestionChoice, Choice: "yes"}}},
		}},
	}
	if err := ValidateGoldenFixture(fixture); err != nil {
		t.Fatalf("ValidateGoldenFixture() error = %v", err)
	}
	fixture.Cases[0].Expected.Answers["q"] = Answer{Type: QuestionScore, Score: 1}
	if err := ValidateGoldenFixture(fixture); !errors.Is(err, ErrInvalid) {
		t.Fatalf("mismatched fixture error = %v, want ErrInvalid", err)
	}
}
