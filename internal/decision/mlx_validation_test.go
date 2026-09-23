package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func mlxInstallation(t *testing.T) Installation {
	t.Helper()
	i := Installation{ID: "laya:english-mlx", Path: t.TempDir(), Valid: true, Manifest: ModelManifest{Model: "english-mlx", Source: ModelSource{Revision: "abc123"}, Runtime: RuntimeArtifact{Format: RuntimeFormatMLX}}}
	for _, name := range []string{"model.safetensors", "mlx_config.json", "rl_agent_config.json", "encoder/config.json", "tokenizer/tokenizer.json", "tokenizer/tokenizer_config.json"} {
		path := filepath.Join(i.Path, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		data := []byte("test-" + name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		i.Manifest.Files = append(i.Manifest.Files, ArtifactManifest{Path: name, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	return i
}

type mlxFakeLoader struct {
	t       *testing.T
	mode    string
	engines []*MLXEngine
}

func (*mlxFakeLoader) SupportsSource(format string) bool { return format == RuntimeFormatMLX }
func (l *mlxFakeLoader) Load(ctx context.Context, _ Installation) (Engine, error) {
	e, err := startMLX(ctx, fakeMLXCommand(l.t, l.mode), mlxRequest{Type: "init", Protocol: mlxProtocol})
	if err == nil {
		l.engines = append(l.engines, e)
	}
	return e, err
}
func TestMLXVerifiedSourceBoundary(t *testing.T) {
	for _, mode := range []string{"valid", "tampered", "symlink", "empty", "extra_tokenizer", "missing_hash", "missing_config"} {
		t.Run(mode, func(t *testing.T) {
			i := mlxInstallation(t)
			switch mode {
			case "tampered":
				if err := os.WriteFile(filepath.Join(i.Path, "model.safetensors"), []byte("changed"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				path := filepath.Join(i.Path, "model.safetensors")
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(t.TempDir(), "outside"), path); err != nil {
					t.Skip(err)
				}
			case "empty":
				i.Manifest.Files = nil
			case "extra_tokenizer":
				if err := os.WriteFile(filepath.Join(i.Path, "tokenizer", "extra.json"), []byte("{}"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "missing_hash":
				i.Manifest.Files[0].SHA256 = ""
			case "missing_config":
				i.Manifest.Files = i.Manifest.Files[:len(i.Manifest.Files)-1]
			}
			loader := &mlxFakeLoader{t: t, mode: "ok"}
			e, err := LoadRuntime(context.Background(), i, loader)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				_ = e.Close()
				return
			}
			if !errors.Is(err, ErrRuntimeArtifactUnavailable) || len(loader.engines) != 0 {
				t.Fatalf("unverified model reached loader: %v", err)
			}
		})
	}
}
func TestMLXRouterReusesEvictsAndRecovers(t *testing.T) {
	i := mlxInstallation(t)
	other := mlxInstallation(t)
	other.ID = "laya:multilingual-mlx"
	other.Manifest.Model = "multilingual-mlx"
	store := routerStore{catalog: LayaMLXModels, installations: map[string][]Installation{i.ID: {i}, other.ID: {other}}}
	loader := &mlxFakeLoader{t: t, mode: "ok"}
	router, err := NewRouter(RouterOptions{Store: store, Loader: loader, DefaultModel: i.ID, MaxLoaded: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = router.Close() }()
	for range 2 {
		if _, err := router.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	if len(loader.engines) != 1 {
		t.Fatal("model loaded per prediction")
	}
	if _, err := router.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{Model: other.ID}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-loader.engines[0].done:
	default:
		t.Fatal("eviction did not reap worker")
	}
	_ = loader.engines[1].Close()
	if _, err := router.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{Model: other.ID}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("crashed engine: %v", err)
	}
	if _, err := router.Predict(context.Background(), nil, mlxQuestions(), PredictOptions{Model: other.ID}); err != nil {
		t.Fatal(err)
	}
	if len(loader.engines) != 3 {
		t.Fatal("router did not reload failed engine")
	}
}
func TestHubLFSUsesOID(t *testing.T) {
	var entries []hubTreeEntry
	if err := json.Unmarshal([]byte(`[{"type":"file","path":"model.safetensors","size":10,"lfs":{"oid":"b9c07bf14be2fa5c78a9193a3e6d840ac80e89e62fc40f425834c3d8a6eaa3de"}}]`), &entries); err != nil {
		t.Fatal(err)
	}
	if entries[0].LFS.SHA256 != "b9c07bf14be2fa5c78a9193a3e6d840ac80e89e62fc40f425834c3d8a6eaa3de" {
		t.Fatal("upstream SHA-256 lost")
	}
}

func TestArtifactManifestKeepsSHA256Schema(t *testing.T) {
	var file ArtifactManifest
	if err := json.Unmarshal([]byte(`{"path":"model.safetensors","size":10,"sha256":"hash"}`), &file); err != nil {
		t.Fatal(err)
	}
	if file.SHA256 != "hash" {
		t.Fatal("manifest schema changed")
	}
}

func TestMLXLoaderRejectsONNX(t *testing.T) {
	installation := readyInstallation(t, t.TempDir(), "english")
	if _, err := LoadRuntime(context.Background(), installation, MLXRuntimeLoader{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("MLX accepted ONNX installation: %v", err)
	}
}
