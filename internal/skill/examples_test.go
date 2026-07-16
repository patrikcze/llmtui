package skill

import (
	"os"
	"path/filepath"
	"testing"
)

// TestShippedExamplesAreValid keeps examples/ parseable: the docs point
// users at these files as starting points, so they must never rot.
func TestShippedExamplesAreValid(t *testing.T) {
	root := filepath.Join("..", "..", "examples")
	if _, err := os.Stat(root); err != nil {
		t.Skipf("examples directory not present: %v", err)
	}

	for _, p := range []string{
		filepath.Join(root, "skills", "grilling", "SKILL.md"),
		filepath.Join(root, "skills", "tdd", "SKILL.md"),
		filepath.Join(root, "skills", "code-review", "SKILL.md"),
		filepath.Join(root, "skills", "go-agent-loop-review", "SKILL.md"),
		filepath.Join(root, "skills", "llmtui-daily-workflow", "SKILL.md"),
		filepath.Join(root, "plugins", "jira-tools", "skills", "jira-worklog", "SKILL.md"),
		filepath.Join(root, "plugins", "jira-tools", "skills", "jira-task-review", "SKILL.md"),
	} {
		raw, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		if _, err := Parse(raw, 0); err != nil {
			t.Errorf("example skill %s does not validate: %v", p, err)
		}
	}

	raw, err := os.ReadFile(filepath.Join(root, "plugins", "jira-tools", "plugin.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	m, err := ParseManifest(raw)
	if err != nil {
		t.Fatalf("example plugin manifest does not validate: %v", err)
	}
	if m.ID != "jira-tools" || len(m.Skills) != 2 {
		t.Errorf("manifest = %+v", m)
	}

	// The example plugin loads end to end through the manager.
	mgr := NewManager(Options{
		Enabled:        true,
		Paths:          Paths{UserPluginDir: filepath.Join(root, "plugins")},
		EnabledPlugins: []string{"jira-tools"},
	})
	if got := len(mgr.Skills()); got != 2 {
		t.Errorf("example plugin contributed %d skills, want 2 (warnings: %v)", got, mgr.Warnings())
	}
}
