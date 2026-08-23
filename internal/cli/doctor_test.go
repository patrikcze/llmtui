package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoctorConfig(t *testing.T, extra string) string {
	t.Helper()
	isolateDoctorEnvironment(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	data := "default_provider: mock\ndefault_model: demo-model\nproviders:\n  mock:\n    type: mock\n    default_model: demo-model\n" + extra
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func isolateDoctorEnvironment(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "xdg"))
	t.Setenv("APPDATA", filepath.Join(root, "appdata"))
	t.Setenv("LOCALAPPDATA", filepath.Join(root, "localappdata"))
	t.Chdir(root)
}

func runDoctor(t *testing.T, configPath string) string {
	t.Helper()
	cmd := newRootCmd("test", "test", "test", func(*Root, string, bool) error {
		t.Fatal("doctor must not launch chat")
		return nil
	})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--config", configPath, "doctor"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("doctor: %v", err)
	}
	return out.String()
}

func TestDoctorReportsSkillsDisabled(t *testing.T) {
	path := writeDoctorConfig(t, "skills:\n  enabled: false\n")
	out := runDoctor(t, path)
	if !strings.Contains(out, "skills") || !strings.Contains(out, "skills are disabled") {
		t.Fatalf("doctor output missing disabled-skills line:\n%s", out)
	}
}

func TestDoctorReportsDiscoveredSkillsAndPlugins(t *testing.T) {
	pluginDir := t.TempDir()
	root := filepath.Join(pluginDir, "jira-tools")
	skillDir := filepath.Join(root, "skills", "worklog")
	if err := os.MkdirAll(skillDir, 0o755); err != nil {
		t.Fatal(err)
	}
	skillDoc := "---\nschema_version: 1\nid: worklog\nname: worklog\nversion: 1.0.0\ndescription: Prepare a worklog.\n---\nDo it.\n"
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte(skillDoc), 0o644); err != nil {
		t.Fatal(err)
	}
	manifest := "schema_version: 1\nid: jira-tools\nname: Jira Tools\nversion: 1.0.0\ndescription: Jira skills.\nskills:\n  - path: skills/worklog/SKILL.md\n"
	if err := os.WriteFile(filepath.Join(root, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	extra := "skills:\n  enabled: true\n  expose_catalog_to_model: true\nplugins:\n  paths:\n    - " + pluginDir + "\n  enabled:\n    - jira-tools\n"
	path := writeDoctorConfig(t, extra)
	out := runDoctor(t, path)

	for _, want := range []string{
		"skills",
		"1 skill(s) discovered",
		"1 plugin(s) discovered, 1 enabled",
		"catalog exposed to tool-capable models",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor output missing %q:\n%s", want, out)
		}
	}
}

func TestDoctorReportsInvalidPluginManifest(t *testing.T) {
	pluginDir := t.TempDir()
	root := filepath.Join(pluginDir, "broken")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	// Missing required "description" field.
	manifest := "schema_version: 1\nid: broken\nname: Broken\nversion: 1.0.0\n"
	if err := os.WriteFile(filepath.Join(root, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	extra := "skills:\n  enabled: true\nplugins:\n  paths:\n    - " + pluginDir + "\n"
	path := writeDoctorConfig(t, extra)
	out := runDoctor(t, path)

	if !strings.Contains(out, "1 plugin(s) have an invalid manifest") {
		t.Errorf("doctor output missing invalid-manifest warning:\n%s", out)
	}
}
