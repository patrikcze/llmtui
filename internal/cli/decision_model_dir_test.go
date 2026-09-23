package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/decision"
)

func TestDecisionModelDirSelection(t *testing.T) {
	for _, mode := range []string{"default", "yaml", "environment"} {
		t.Run(mode, func(t *testing.T) {
			base := t.TempDir()
			t.Setenv("XDG_DATA_HOME", base)
			t.Setenv("LOCALAPPDATA", base)
			t.Setenv("LLMTUI_DECISION_ENGINE_LAYA_MODEL_DIR", "")
			want := filepath.Join(base, "llmtui", "models", "laya")
			configured := ""
			if mode != "default" {
				configured = filepath.Join(base, "yaml-models")
				want = configured
			}
			if mode == "environment" {
				want = filepath.Join(base, "env-models")
				t.Setenv("LLMTUI_DECISION_ENGINE_LAYA_MODEL_DIR", want)
			}
			// JSON is valid YAML and safely quotes platform-specific paths.
			path := filepath.Join(base, "config.yaml")
			data, err := json.Marshal(map[string]any{"decision_engine": map[string]any{"laya": map[string]any{"model_dir": configured}}})
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			installed := filepath.Join(want, "english-mlx", "abc1234")
			if err := os.MkdirAll(installed, 0o755); err != nil {
				t.Fatal(err)
			}
			manifest := decision.ModelManifest{SchemaVersion: 1, Engine: "laya", Model: "english-mlx", Source: decision.ModelSource{Repository: "aac6fef/laya-mlx", Revision: "abc1234"}, Runtime: decision.RuntimeArtifact{Format: decision.RuntimeFormatMLX}}
			data, err = json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(installed, "LLMTUI_DECISION_MANIFEST.json"), data, 0o600); err != nil {
				t.Fatal(err)
			}
			cmd := NewRootCmd("test", "test", "test")
			var output bytes.Buffer
			cmd.SetOut(&output)
			cmd.SetErr(&output)
			cmd.SetArgs([]string{"--config", path, "decision", "inspect", "laya:english-mlx"})
			if err := cmd.Execute(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Path: "+installed+"\n") || !strings.Contains(output.String(), "Valid: true") {
				t.Fatalf("wrong model store selected: %s", output.String())
			}
		})
	}
}
