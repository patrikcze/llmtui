package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRootCommandDefaultsToChat(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configData := []byte(`default_provider: mock
default_model: demo-model
providers:
  mock:
    type: mock
    default_model: demo-model
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	called := 0
	cmd := newRootCmd("test", "test", "test", func(r *Root, resumeName string, continueLatest bool) error {
		called++
		if r.cfg == nil {
			t.Fatal("chat launched before configuration was loaded")
		}
		if resumeName != "" || continueLatest {
			t.Fatalf("unexpected resume options: name=%q continue=%v", resumeName, continueLatest)
		}
		return nil
	})
	cmd.SetArgs([]string{"--config", configPath})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute bare llmtui: %v", err)
	}
	if called != 1 {
		t.Fatalf("chat launch calls = %d, want 1", called)
	}
}

func TestRootCommandSupportsChatResumeFlags(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configData := []byte(`default_provider: mock
default_model: demo-model
providers:
  mock:
    type: mock
    default_model: demo-model
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cmd := newRootCmd("test", "test", "test", func(_ *Root, resumeName string, continueLatest bool) error {
		if resumeName != "saved-session" || continueLatest {
			t.Fatalf("resume options: name=%q continue=%v", resumeName, continueLatest)
		}
		return nil
	})
	cmd.SetArgs([]string{"--config", configPath, "--resume", "saved-session"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute bare llmtui with --resume: %v", err)
	}
}

func TestRootCommandOnlyChangedFlagsOverrideConfig(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	configData := []byte(`default_provider: mock
default_model: demo-model
chat:
  temperature: 0.7
providers:
  mock:
    type: mock
    default_model: demo-model
`)
	if err := os.WriteFile(configPath, configData, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	tests := []struct {
		name string
		args []string
		want float64
	}{
		{name: "unset flag preserves config", want: 0.7},
		{name: "explicit zero overrides config", args: []string{"--temperature", "0"}, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := newRootCmd("test", "test", "test", func(r *Root, _ string, _ bool) error {
				if r.cfg.Chat.Temperature != tt.want {
					t.Errorf("temperature = %v, want %v", r.cfg.Chat.Temperature, tt.want)
				}
				return nil
			})
			cmd.SetArgs(append([]string{"--config", configPath}, tt.args...))
			if err := cmd.Execute(); err != nil {
				t.Fatalf("execute root command: %v", err)
			}
		})
	}
}
