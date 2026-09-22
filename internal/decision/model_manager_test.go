package decision

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelManagerPullsSelectiveArtifactsAtomically(t *testing.T) {
	files := map[string][]byte{
		"model.safetensors":              []byte("model-bytes"),
		"rl_agent_config.json":           []byte(`{"encoder":"offline","head_layers":0}`),
		"tokenizer/tokenizer.json":       []byte("tokenizer"),
		"encoder/config.json":            []byte("encoder"),
		"README.md":                      []byte("must not download"),
		"multilingual/model.safetensors": []byte("sibling must not download"),
	}
	revision := "abcdef1234567"
	server := newHubTestServer(t, revision, files)
	defer server.Close()

	root := t.TempDir()
	manager, err := NewModelManager(ModelManagerOptions{
		RootDir:   root,
		Endpoint:  server.URL,
		AllowHTTP: true,
		Token:     "test-token",
		Catalog:   []ModelDescriptor{{Alias: "english", Repository: "owner/repo"}},
	})
	if err != nil {
		t.Fatalf("NewModelManager() error = %v", err)
	}
	var progress []Progress
	installed, err := manager.Pull(context.Background(), "laya:english", PullOptions{Progress: func(p Progress) {
		progress = append(progress, p)
	}})
	if err != nil {
		t.Fatalf("Pull() error = %v", err)
	}
	if !installed.Valid || installed.Manifest.Source.Revision != revision {
		t.Fatalf("installation = %#v, want valid revision %s", installed, revision)
	}
	if installed.Manifest.Runtime.Ready || installed.Manifest.Runtime.Format != "safetensors" {
		t.Fatalf("source runtime metadata = %#v", installed.Manifest.Runtime)
	}
	for _, rel := range []string{"model.safetensors", "rl_agent_config.json", "tokenizer/tokenizer.json", "encoder/config.json", modelManifestName} {
		if _, err := os.Stat(filepath.Join(installed.Path, rel)); err != nil {
			t.Errorf("installed file %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(installed.Path, "README.md")); !os.IsNotExist(err) {
		t.Errorf("README.md exists or returned unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(installed.Path, "multilingual", "model.safetensors")); !os.IsNotExist(err) {
		t.Errorf("sibling artifact exists or returned unexpected error: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "english", ".staging-"+revision)); !os.IsNotExist(err) {
		t.Errorf("staging directory remains after successful install: %v", err)
	}
	if len(progress) == 0 || !progress[len(progress)-1].Done {
		t.Fatalf("progress = %#v, want terminal event", progress)
	}

	listed, err := manager.ListInstalled()
	if err != nil || len(listed) != 1 || !listed[0].Valid {
		t.Fatalf("ListInstalled() = %#v, error %v", listed, err)
	}
	verified, err := manager.Verify("laya:english")
	if err != nil || len(verified) != 1 || !verified[0].Valid {
		t.Fatalf("Verify() = %#v, error %v", verified, err)
	}
}

func TestModelManagerResumesPartialArtifact(t *testing.T) {
	files := map[string][]byte{
		"model.safetensors":        []byte("model-bytes"),
		"rl_agent_config.json":     []byte(`{"encoder":"offline"}`),
		"tokenizer/tokenizer.json": []byte("tokenizer"),
		"encoder/config.json":      []byte("encoder"),
	}
	revision := "abcdef1234567"
	server := newHubTestServer(t, revision, files)
	defer server.Close()
	root := t.TempDir()
	stage := filepath.Join(root, "english", ".staging-"+revision)
	if err := os.MkdirAll(stage, 0o700); err != nil {
		t.Fatal(err)
	}
	partialPath := filepath.Join(stage, "model.safetensors.partial")
	if err := os.WriteFile(partialPath, files["model.safetensors"][:5], 0o600); err != nil {
		t.Fatal(err)
	}

	manager, err := NewModelManager(ModelManagerOptions{
		RootDir: root, Endpoint: server.URL, AllowHTTP: true,
		Catalog: []ModelDescriptor{{Alias: "english", Repository: "owner/repo"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Pull(context.Background(), "laya:english", PullOptions{}); err != nil {
		t.Fatalf("Pull() resume error = %v", err)
	}
	got, err := os.ReadFile(filepath.Join(root, "english", revision, "model.safetensors"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(files["model.safetensors"]) {
		t.Fatalf("resumed model = %q, want %q", got, files["model.safetensors"])
	}
}

func TestModelManagerPullsMLXArtifactsAsExternalRuntime(t *testing.T) {
	files := map[string][]byte{
		"model.safetensors":        []byte("mlx-model-bytes"),
		"rl_agent_config.json":     []byte(`{"encoder":"offline","head_layers":2}`),
		"mlx_config.json":          []byte(`{"format":"laya-mlx","format_version":1,"dtype":"float16"}`),
		"tokenizer/tokenizer.json": []byte("tokenizer"),
		"encoder/config.json":      []byte("encoder"),
	}
	revision := "1234567890abc"
	server := newHubTestServer(t, revision, files)
	defer server.Close()
	root := t.TempDir()
	manager, err := NewModelManager(ModelManagerOptions{
		RootDir: root, Endpoint: server.URL, AllowHTTP: true,
		Catalog: []ModelDescriptor{{Alias: "english-mlx", Repository: "owner/repo", RuntimeFormat: RuntimeFormatMLX, RuntimeNote: "external MLX"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	installed, err := manager.Pull(context.Background(), "laya:english-mlx", PullOptions{})
	if err != nil {
		t.Fatalf("MLX Pull() error = %v", err)
	}
	if installed.Manifest.Runtime.Ready || installed.Manifest.Runtime.Format != RuntimeFormatMLX {
		t.Fatalf("MLX runtime metadata = %#v", installed.Manifest.Runtime)
	}
	if _, err := os.Stat(filepath.Join(installed.Path, "mlx_config.json")); err != nil {
		t.Fatalf("MLX metadata was not installed: %v", err)
	}
}

func TestNewModelManagerRejectsNonHTTPSEndpoint(t *testing.T) {
	if _, err := NewModelManager(ModelManagerOptions{Endpoint: "http://example.test"}); err == nil {
		t.Fatal("NewModelManager() accepted non-HTTPS endpoint")
	}
}

func TestRejectSymlinkChain(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	link := filepath.Join(root, "english")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := rejectSymlinkChain(filepath.Join(link, "revision", "model.safetensors"), root); err == nil {
		t.Fatal("rejectSymlinkChain accepted a symlinked model directory")
	}
}

func newHubTestServer(t *testing.T, revision string, files map[string][]byte) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "" && got != "Bearer test-token" {
			t.Errorf("authorization header = %q", got)
		}
		switch {
		case r.URL.Path == "/api/models/owner/repo":
			writeJSON(t, w, map[string]string{"sha": revision})
		case r.URL.Path == "/api/models/owner/repo/tree/"+revision:
			entries := make([]hubTreeEntry, 0, len(files))
			for path, body := range files {
				sum := sha256.Sum256(body)
				entries = append(entries, hubTreeEntry{Path: path, Type: "file", Size: int64(len(body)), LFS: &hubLFS{SHA256: hex.EncodeToString(sum[:])}})
			}
			writeJSON(t, w, entries)
		case strings.HasPrefix(r.URL.Path, "/owner/repo/resolve/"+revision+"/"):
			path := strings.TrimPrefix(r.URL.Path, "/owner/repo/resolve/"+revision+"/")
			body, ok := files[path]
			if !ok {
				http.NotFound(w, r)
				return
			}
			start := 0
			if rangeHeader := r.Header.Get("Range"); strings.HasPrefix(rangeHeader, "bytes=") {
				_, _ = fmt.Sscanf(strings.TrimSuffix(strings.TrimPrefix(rangeHeader, "bytes="), "-"), "%d", &start)
				if start > len(body) {
					http.Error(w, "range", http.StatusRequestedRangeNotSatisfiable)
					return
				}
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(body[start:])
				return
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
}

func writeJSON(t *testing.T, w http.ResponseWriter, value any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(value); err != nil {
		t.Errorf("encode JSON: %v", err)
	}
}
