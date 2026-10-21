package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/patrikcze/llmtui/internal/harness"
	"github.com/patrikcze/llmtui/internal/provider/mock"
)

func TestHarnessCLIWorkflow(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	stateDir := filepath.Join(dir, "state")
	if err := os.WriteFile(configPath, []byte("default_provider: mock\ndefault_model: demo-model\nprivacy:\n  store_prompts: false\nproviders:\n  mock:\n    type: mock\n    default_model: demo-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	invoke := func(args ...string) ([]byte, error) {
		t.Helper()
		out := new(bytes.Buffer)
		cmd := newRootCmd("test", "test", "test", func(r *Root, _ string, _ bool) error {
			snapshot, err := r.activeHarness(context.Background(), mock.New())
			if r.useHarness && err != nil {
				return err
			}
			if r.useHarness && snapshot == nil {
				t.Fatal("explicit adoption not usable", err)
			}
			if !r.useHarness && (snapshot != nil || err != nil) {
				t.Fatal("disabled startup touched corrupt state", err)
			}
			return nil
		})
		cmd.SetOut(out)
		cmd.SetErr(out)
		cmd.SetArgs(append([]string{"--config", configPath, "--harness-state-dir", stateDir}, args...))
		err := cmd.Execute()
		return out.Bytes(), err
	}
	out, err := invoke("harness", "run", "tool-use")
	if err != nil {
		t.Fatal(err)
	}
	var report harness.Report
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatal(err)
	}
	if report.Recommendation.Outcome != harness.ComposeSupported {
		t.Fatal(string(out))
	}
	for _, args := range [][]string{{"harness", "inspect", report.ID}, {"harness", "reflect", report.ID}, {"harness", "adopt", report.ID}, {"--experimental-harness", "chat"}, {"harness", "revert"}, {"harness", "disable"}} {
		if _, err := invoke(args...); err != nil {
			t.Fatal(args, err)
		}
	}
	state, err := (harness.Store{Dir: stateDir}).Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.Experiences) == 0 {
		t.Fatal("missing reflection")
	}
	if _, err := invoke("harness", "delete-experience", state.Experiences[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("harness", "reject", report.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("harness", "run", dir); err == nil {
		t.Fatal("live probe accepted")
	}
	if err := os.WriteFile(filepath.Join(stateDir, "state.json"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := invoke("chat"); err != nil {
		t.Fatal("normal startup blocked by experimental state", err)
	}
	if _, err := invoke("--experimental-harness", "chat"); err == nil {
		t.Fatal("corrupt state activated")
	}
}
