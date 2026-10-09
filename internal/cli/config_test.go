package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/contextmgr"
	"github.com/patrikcze/llmtui/internal/prompt"
)

func TestConfigInitWritesStarterConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	r := &Root{cfgFile: path}
	cmd := newConfigInitCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated config: %v", err)
	}
	if string(data) != config.DefaultYAML {
		t.Fatal("config init output differs from the tested starter config")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat generated config: %v", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
		t.Errorf("generated config mode = %o, want 600", info.Mode().Perm())
	}
	if !strings.Contains(out.String(), path) {
		t.Fatalf("config init confirmation omits path: %q", out.String())
	}
}

func TestConfigShowRedactsMCPEnvironmentValues(t *testing.T) {
	r := &Root{cfg: &config.Config{
		Providers: map[string]config.ProviderConfig{"remote": {APIKey: "provider-secret-marker"}},
		MCP: config.MCPConfig{Servers: map[string]config.MCPServerConfig{
			"jira": {Env: map[string]string{"TOKEN": "mcp-secret-marker"}},
		}},
	}}
	cmd := newConfigShowCmd(r)
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config show: %v", err)
	}
	for _, secret := range []string{"provider-secret-marker", "mcp-secret-marker"} {
		if strings.Contains(out.String(), secret) {
			t.Fatalf("config show leaked %q:\n%s", secret, out.String())
		}
	}
	if !strings.Contains(out.String(), "TOKEN: '***'") && !strings.Contains(out.String(), "TOKEN: \"***\"") {
		t.Fatalf("config show omitted the redacted MCP env key:\n%s", out.String())
	}
}

func runConfigCommand(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := newRootCmd("test", "test", "test", stubLaunch)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

func TestConfigSchemaPrintsFieldsWithoutReadingTheConfig(t *testing.T) {
	// A config that fails to load must not stop the schema from printing.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("entities:\n  output_storage: nowhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runConfigCommand(t, "--config", path, "config", "schema")
	if err != nil {
		t.Fatalf("config schema: %v", err)
	}
	var schema struct {
		Fields []config.SchemaField `json:"fields"`
	}
	if err := json.Unmarshal([]byte(out), &schema); err != nil {
		t.Fatalf("schema is not JSON: %v", err)
	}
	if len(schema.Fields) < 100 {
		t.Fatalf("schema lists only %d fields", len(schema.Fields))
	}
}

func TestConfigValidateReportsErrorsAndWarnings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "default_provider: mlxserve\nchat:\n  temprature: 0.2\nproviders:\n  lmstudio:\n    type: openai_compatible\n    api_key: validate-secret-marker\n"
	if err := os.WriteFile(path, []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runConfigCommand(t, "--config", path, "config", "validate", "--json")
	if err == nil {
		t.Fatal("validate passed a config whose default provider is missing")
	}
	var report struct {
		Problems []config.Problem `json:"problems"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &report); jsonErr != nil {
		t.Fatalf("validate --json is not JSON: %v\n%s", jsonErr, out)
	}
	keys := map[string]bool{}
	for _, p := range report.Problems {
		keys[p.Key] = p.Error
	}
	if isErr, ok := keys["default_provider"]; !ok || !isErr {
		t.Errorf("missing default_provider error: %+v", report.Problems)
	}
	if isErr, ok := keys["chat.temprature"]; !ok || isErr {
		t.Errorf("missing unknown-key warning: %+v", report.Problems)
	}
	if strings.Contains(out, "validate-secret-marker") {
		t.Fatal("validate printed an API key")
	}

	valid := filepath.Join(t.TempDir(), "valid.yaml")
	if err := os.WriteFile(valid, []byte("default_provider: lmstudio\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runConfigCommand(t, "--config", valid, "config", "validate")
	if err != nil || !strings.Contains(out, "configuration is valid") {
		t.Fatalf("valid config: err=%v out=%q", err, out)
	}
}

// The schema's enum literals live in config, below the packages that
// consume them; this keeps the lists in step with those packages.
func TestConfigSchemaEnumsMatchConsumers(t *testing.T) {
	enums := map[string][]string{}
	for _, f := range config.Schema() {
		enums[f.Key] = f.Enum
	}
	for _, key := range []string{"prompt.mode", "templates.*.prompt_mode"} {
		for _, mode := range enums[key] {
			if !prompt.ValidMode(mode) {
				t.Errorf("%s value %q is not a prompt mode", key, mode)
			}
		}
	}
	for _, s := range enums["context.strategy"] {
		if !contextmgr.ValidStrategy(s) {
			t.Errorf("context.strategy value %q is not a strategy", s)
		}
	}
	for _, s := range []string{contextmgr.StrategyAuto, contextmgr.StrategyNone, contextmgr.StrategyTruncate, contextmgr.StrategySummarize} {
		if !slices.Contains(enums["context.strategy"], s) {
			t.Errorf("context.strategy is missing %q", s)
		}
	}
	for _, m := range []string{prompt.ModeMinimal, prompt.ModeBalanced, prompt.ModeCoding, prompt.ModeStrict} {
		if !slices.Contains(enums["prompt.mode"], m) {
			t.Errorf("prompt.mode is missing %q", m)
		}
	}
}
