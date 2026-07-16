package skill

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// writeSkill creates <dir>/<id>/SKILL.md with a minimal valid document.
func writeSkill(t *testing.T, dir, id, body string) string {
	t.Helper()
	doc := "---\nschema_version: 1\nid: " + id + "\nname: " + strings.ToUpper(id) +
		"\nversion: 1.0.0\ndescription: Test skill " + id + ".\n---\n" + body
	path := filepath.Join(dir, id, SkillFileName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// writePlugin creates a plugin directory with a manifest and skills.
func writePlugin(t *testing.T, dir, id string, skillIDs ...string) string {
	t.Helper()
	root := filepath.Join(dir, id)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	var refs strings.Builder
	for _, sid := range skillIDs {
		writeSkill(t, filepath.Join(root, "skills"), sid, "Plugin skill body.")
		refs.WriteString("  - path: skills/" + sid + "/SKILL.md\n")
	}
	manifest := "schema_version: 1\nid: " + id + "\nname: " + id +
		"\nversion: 1.0.0\ndescription: Test plugin " + id + ".\nskills:\n" + refs.String()
	if err := os.WriteFile(filepath.Join(root, PluginManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func newTestManager(t *testing.T, opts Options) *Manager {
	t.Helper()
	opts.Enabled = true
	return NewManager(opts)
}

func TestDiscoveryAcrossSources(t *testing.T) {
	user := t.TempDir()
	ws := t.TempDir()
	extra := t.TempDir()
	writeSkill(t, user, "user-skill", "u")
	writeSkill(t, ws, "ws-skill", "w")
	writeSkill(t, extra, "extra-skill", "e")

	m := newTestManager(t, Options{Paths: Paths{
		UserDir: user, WorkspaceDir: ws, Extra: []string{extra},
	}})
	skills := m.Skills()
	if len(skills) != 3 {
		t.Fatalf("discovered %d skills, want 3: %+v", len(skills), skills)
	}
	bySource := map[Source]string{}
	for _, s := range skills {
		bySource[s.Source] = s.Meta.ID
	}
	if bySource[SourceUser] != "user-skill" || bySource[SourceWorkspace] != "ws-skill" || bySource[SourceExtra] != "extra-skill" {
		t.Errorf("sources wrong: %v", bySource)
	}
}

func TestDiscoveryMissingDirsNotFatal(t *testing.T) {
	m := newTestManager(t, Options{Paths: Paths{
		UserDir:      filepath.Join(t.TempDir(), "does-not-exist"),
		WorkspaceDir: "",
	}})
	if len(m.Skills()) != 0 || len(m.Warnings()) != 0 {
		t.Errorf("expected clean empty scan, got %v / %v", m.Skills(), m.Warnings())
	}
}

func TestDuplicateIDsQualifiedResolution(t *testing.T) {
	user := t.TempDir()
	ws := t.TempDir()
	writeSkill(t, user, "go-review", "user version")
	writeSkill(t, ws, "go-review", "workspace version")

	m := newTestManager(t, Options{Paths: Paths{UserDir: user, WorkspaceDir: ws}})
	if len(m.Skills()) != 2 {
		t.Fatalf("both duplicates must stay registered, got %d", len(m.Skills()))
	}
	// Unqualified is ambiguous.
	if _, err := m.Resolve("go-review"); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("ambiguous id resolved: %v", err)
	}
	// Qualified resolves each.
	u, err := m.Resolve("user:go-review")
	if err != nil || u.Source != SourceUser {
		t.Fatalf("user:go-review: %v %v", u, err)
	}
	w, err := m.Resolve("workspace:go-review")
	if err != nil || w.Source != SourceWorkspace {
		t.Fatalf("workspace:go-review: %v %v", w, err)
	}
	// The conflict is reported, never silent.
	found := false
	for _, warn := range m.Warnings() {
		if strings.Contains(warn.Message, "multiple sources") {
			found = true
		}
	}
	if !found {
		t.Errorf("duplicate warning missing: %v", m.Warnings())
	}
}

func TestActivationScopes(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	writeSkill(t, user, "beta", "b")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}})

	if _, err := m.Activate("alpha", ScopeSession); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate("beta", ScopeRun); err != nil {
		t.Fatal(err)
	}
	active := m.Active()
	if len(active) != 2 {
		t.Fatalf("active = %d", len(active))
	}
	// Deterministic order: session first, then run.
	if active[0].Skill.Meta.ID != "alpha" || active[0].Scope != ScopeSession {
		t.Errorf("active[0] = %+v", active[0])
	}
	if active[1].Skill.Meta.ID != "beta" || active[1].Scope != ScopeRun {
		t.Errorf("active[1] = %+v", active[1])
	}

	// Run cleanup clears run skills only; session skills survive.
	cleared := m.ClearRun()
	if len(cleared) != 1 || cleared[0] != "beta" {
		t.Errorf("cleared = %v", cleared)
	}
	active = m.Active()
	if len(active) != 1 || active[0].Skill.Meta.ID != "alpha" {
		t.Errorf("session skill lost: %+v", active)
	}

	// Deactivation.
	if err := m.Deactivate("alpha"); err != nil {
		t.Fatal(err)
	}
	if len(m.Active()) != 0 {
		t.Error("deactivate left skills active")
	}
	if err := m.Deactivate("alpha"); err == nil {
		t.Error("deactivating an inactive skill must error")
	}
}

func TestActivationIdempotentAndScopeMove(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}})

	if _, err := m.Activate("alpha", ScopeRun); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate("alpha", ScopeRun); err != nil {
		t.Fatalf("duplicate activation must be idempotent: %v", err)
	}
	if len(m.Active()) != 1 {
		t.Fatalf("active = %d, want 1", len(m.Active()))
	}
	// Re-activating with a different scope moves it.
	if _, err := m.Activate("alpha", ScopeSession); err != nil {
		t.Fatal(err)
	}
	active := m.Active()
	if len(active) != 1 || active[0].Scope != ScopeSession {
		t.Errorf("scope move failed: %+v", active)
	}
}

func TestActivationErrors(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}})

	if _, err := m.Activate("missing", ScopeRun); err == nil || !strings.Contains(err.Error(), "no skill named") {
		t.Errorf("missing skill error = %v", err)
	}
	if _, err := m.Activate("alpha", Scope("forever")); err == nil {
		t.Error("unknown scope accepted")
	}

	off := NewManager(Options{Enabled: false, Paths: Paths{UserDir: user}})
	if _, err := off.Activate("alpha", ScopeRun); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Errorf("disabled subsystem error = %v", err)
	}
}

func TestActivationLimits(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	writeSkill(t, user, "beta", "b")
	writeSkill(t, user, "gamma", strings.Repeat("g", 3000))

	m := newTestManager(t, Options{
		Paths:  Paths{UserDir: user},
		Limits: Limits{MaxActive: 2, MaxTotalActiveBytes: 2048},
	})
	if _, err := m.Activate("alpha", ScopeSession); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate("gamma", ScopeSession); err == nil || !strings.Contains(err.Error(), "byte budget") {
		t.Errorf("total-bytes limit not enforced: %v", err)
	}
	if _, err := m.Activate("beta", ScopeSession); err != nil {
		t.Fatal(err)
	}
	// MaxActive reached.
	writeSkill(t, user, "delta", "d")
	m.Reload()
	if _, err := m.Activate("delta", ScopeSession); err == nil || !strings.Contains(err.Error(), "max_active") {
		t.Errorf("max-active limit not enforced: %v", err)
	}
}

func TestOversizedSkillRejectedAtDiscovery(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "big", strings.Repeat("x", 5000))
	m := newTestManager(t, Options{
		Paths:  Paths{UserDir: user},
		Limits: Limits{MaxSkillBytes: 1024},
	})
	if len(m.Skills()) != 0 {
		t.Fatal("oversized skill registered")
	}
	warns := m.Warnings()
	if len(warns) != 1 || !strings.Contains(warns[0].Message, "byte limit") {
		t.Errorf("warnings = %v", warns)
	}
}

func TestReloadKeepsActivationSnapshot(t *testing.T) {
	user := t.TempDir()
	path := writeSkill(t, user, "alpha", "original body")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}})
	s, err := m.Activate("alpha", ScopeSession)
	if err != nil {
		t.Fatal(err)
	}
	origHash := s.Hash

	// Change the file on disk, reload.
	doc := "---\nschema_version: 1\nid: alpha\nname: ALPHA\ndescription: Test skill alpha.\n---\nchanged body"
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	notes := m.Reload()
	if len(notes) != 1 || !strings.Contains(notes[0], "changed on disk") {
		t.Errorf("reload notes = %v", notes)
	}
	// The active snapshot keeps the original content.
	active := m.Active()
	if active[0].Skill.Hash != origHash || active[0].Skill.Body != "original body" {
		t.Errorf("activation snapshot mutated: %+v", active[0].Skill)
	}
	// Re-activating picks up the new content.
	if _, err := m.Activate("alpha", ScopeSession); err != nil {
		t.Fatal(err)
	}
	if m.Active()[0].Skill.Body != "changed body" {
		t.Error("re-activation did not refresh the snapshot")
	}
}

func TestFingerprintActive(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	writeSkill(t, user, "beta", "b")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}})

	if m.FingerprintActive() != "" {
		t.Error("empty active set must fingerprint empty")
	}
	if _, err := m.Activate("alpha", ScopeSession); err != nil {
		t.Fatal(err)
	}
	fp1 := m.FingerprintActive()
	if _, err := m.Activate("beta", ScopeSession); err != nil {
		t.Fatal(err)
	}
	fp2 := m.FingerprintActive()
	if fp1 == "" || fp2 == "" || fp1 == fp2 {
		t.Errorf("fingerprints must differ: %q vs %q", fp1, fp2)
	}
	if err := m.Deactivate("beta"); err != nil {
		t.Fatal(err)
	}
	if m.FingerprintActive() != fp1 {
		t.Error("fingerprint not deterministic for the same active set")
	}
}

func TestSessionRefsAndRestore(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	writeSkill(t, user, "beta", "b")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}})
	if _, err := m.Activate("alpha", ScopeSession); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Activate("beta", ScopeRun); err != nil { // run-scoped: never persisted
		t.Fatal(err)
	}

	refs := m.SessionRefs()
	if len(refs) != 1 || refs[0].ID != "alpha" || refs[0].Scope != "session" {
		t.Fatalf("refs = %+v", refs)
	}

	m2 := newTestManager(t, Options{Paths: Paths{UserDir: user}})
	if warns := m2.RestoreSession(refs); len(warns) != 0 {
		t.Fatalf("restore warnings = %v", warns)
	}
	active := m2.Active()
	if len(active) != 1 || active[0].Skill.Meta.ID != "alpha" {
		t.Errorf("restored = %+v", active)
	}

	// Missing skill: warned, not substituted.
	missing := []Ref{{ID: "gone", Scope: "session", Source: "user", Hash: "deadbeef"}}
	warns := m2.RestoreSession(missing)
	if len(warns) != 1 || !strings.Contains(warns[0], "no longer available") {
		t.Errorf("warns = %v", warns)
	}
	if len(m2.Active()) != 0 {
		t.Error("missing skill silently substituted")
	}

	// Changed content: restored with a warning.
	changed := refs
	changed[0].Hash = "0000000000000000"
	warns = m2.RestoreSession(changed)
	if len(warns) != 1 || !strings.Contains(warns[0], "changed since") {
		t.Errorf("warns = %v", warns)
	}
}

func TestPluginLifecycle(t *testing.T) {
	pluginDir := t.TempDir()
	writePlugin(t, pluginDir, "jira-tools", "worklog", "task-review")

	m := newTestManager(t, Options{Paths: Paths{UserPluginDir: pluginDir}})

	// Discovered but disabled: visible, contributes nothing.
	plugins := m.Plugins()
	if len(plugins) != 1 || plugins[0].Enabled {
		t.Fatalf("plugins = %+v", plugins)
	}
	if len(m.Skills()) != 0 {
		t.Fatal("disabled plugin contributed skills")
	}

	// Enable: skills registered, none activated.
	p, err := m.EnablePlugin("jira-tools")
	if err != nil {
		t.Fatal(err)
	}
	if !p.Enabled {
		t.Error("plugin not enabled")
	}
	skills := m.Skills()
	if len(skills) != 2 {
		t.Fatalf("enabled plugin skills = %d, want 2", len(skills))
	}
	for _, s := range skills {
		if s.Source != SourcePlugin || s.PluginID != "jira-tools" {
			t.Errorf("provenance wrong: %+v", s)
		}
	}
	if len(m.Active()) != 0 {
		t.Error("enabling a plugin must not activate its skills")
	}

	// Activate one, then disable the plugin: skill deactivated + unregistered.
	if _, err := m.Activate("worklog", ScopeSession); err != nil {
		t.Fatal(err)
	}
	deactivated, err := m.DisablePlugin("jira-tools")
	if err != nil {
		t.Fatal(err)
	}
	if len(deactivated) != 1 || deactivated[0] != "plugin:jira-tools/worklog" {
		t.Errorf("deactivated = %v", deactivated)
	}
	if len(m.Skills()) != 0 || len(m.Active()) != 0 {
		t.Error("disabled plugin still contributes")
	}

	if _, err := m.EnablePlugin("nope"); err == nil {
		t.Error("enabling an unknown plugin must error")
	}
}

func TestPluginConfigEnabledList(t *testing.T) {
	pluginDir := t.TempDir()
	writePlugin(t, pluginDir, "jira-tools", "worklog")
	m := newTestManager(t, Options{
		Paths:          Paths{UserPluginDir: pluginDir},
		EnabledPlugins: []string{"jira-tools"},
	})
	if len(m.Skills()) != 1 {
		t.Fatalf("config-enabled plugin skills = %d, want 1", len(m.Skills()))
	}
}

func TestPluginPathEscapeRejected(t *testing.T) {
	pluginDir := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, outside, "outside", "outside body")

	root := filepath.Join(pluginDir, "evil")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: 1
id: evil
name: Evil
version: 1.0.0
description: Tries to escape.
skills:
  - path: ../../outside/SKILL.md
  - path: /etc/passwd
`
	if err := os.WriteFile(filepath.Join(root, PluginManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestManager(t, Options{Paths: Paths{UserPluginDir: pluginDir}})
	if _, err := m.EnablePlugin("evil"); err != nil {
		t.Fatal(err)
	}
	if len(m.Skills()) != 0 {
		t.Fatalf("escaping paths registered skills: %+v", m.Skills())
	}
	escapes := 0
	for _, w := range m.Warnings() {
		if strings.Contains(w.Message, "plugin directory") {
			escapes++
		}
	}
	if escapes != 2 {
		t.Errorf("expected 2 escape warnings, got %d: %v", escapes, m.Warnings())
	}
}

func TestPluginSymlinkEscapeRejected(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	pluginDir := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, filepath.Join(outside, "s"), "linked", "outside body")

	root := writePlugin(t, pluginDir, "sneaky") // no skills yet
	// A symlinked directory inside the plugin pointing outside it.
	if err := os.Symlink(filepath.Join(outside, "s"), filepath.Join(root, "skills")); err != nil {
		t.Fatal(err)
	}
	manifest := `schema_version: 1
id: sneaky
name: Sneaky
version: 1.0.0
description: Symlink escape attempt.
skills:
  - path: skills/linked/SKILL.md
`
	if err := os.WriteFile(filepath.Join(root, PluginManifestName), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}

	m := newTestManager(t, Options{Paths: Paths{UserPluginDir: pluginDir}})
	if _, err := m.EnablePlugin("sneaky"); err != nil {
		t.Fatal(err)
	}
	if len(m.Skills()) != 0 {
		t.Fatalf("symlink escape registered skills: %+v", m.Skills())
	}
}

func TestInvalidPluginManifest(t *testing.T) {
	pluginDir := t.TempDir()
	root := filepath.Join(pluginDir, "broken")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, PluginManifestName), []byte("schema_version: 1\nid: broken\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	m := newTestManager(t, Options{Paths: Paths{UserPluginDir: pluginDir}})
	plugins := m.Plugins()
	if len(plugins) != 1 || plugins[0].Err == nil {
		t.Fatalf("broken plugin not surfaced: %+v", plugins)
	}
	if _, err := m.EnablePlugin("broken"); err == nil {
		t.Error("enabling a broken plugin must fail")
	}
}

func TestManifestUnknownFieldRejected(t *testing.T) {
	_, err := ParseManifest([]byte(`schema_version: 1
id: x
name: X
version: 1.0.0
description: d
install_script: ./setup.sh
`))
	if err == nil || !strings.Contains(err.Error(), "install_script") {
		t.Errorf("unknown manifest field accepted: %v", err)
	}
}

func TestCatalogText(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	writeSkill(t, user, "beta", "b")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}, ExposeCatalog: true})

	cat := m.CatalogText(0)
	if !strings.Contains(cat, "- alpha: Test skill alpha.") || !strings.Contains(cat, "- beta:") {
		t.Errorf("catalog = %q", cat)
	}
	if strings.Contains(cat, "body") {
		t.Error("catalog leaked skill bodies")
	}

	// Budget: entries over budget are counted, not silently dropped.
	small := m.CatalogText(len("Available optional skills (activate one with the skill_load tool when it clearly matches the task):\n") + 40)
	if !strings.Contains(small, "more (catalog size budget reached)") {
		t.Errorf("budget note missing: %q", small)
	}

	// Catalog off.
	off := newTestManager(t, Options{Paths: Paths{UserDir: user}, ExposeCatalog: false})
	if off.CatalogText(0) != "" {
		t.Error("catalog rendered while exposure is off")
	}
}

func TestConcurrentActivationAndReads(t *testing.T) {
	user := t.TempDir()
	writeSkill(t, user, "alpha", "a")
	writeSkill(t, user, "beta", "b")
	m := newTestManager(t, Options{Paths: Paths{UserDir: user}})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				switch i % 4 {
				case 0:
					_, _ = m.Activate("alpha", ScopeRun)
				case 1:
					m.Active()
					m.FingerprintActive()
				case 2:
					m.Reload()
				case 3:
					m.ClearRun()
					m.CatalogText(0)
				}
			}
		}(i)
	}
	wg.Wait()
}

func TestRequiresToolCalling(t *testing.T) {
	s := Skill{Meta: Meta{Capabilities: map[string]string{"tool_calling": "required"}}}
	if !RequiresToolCalling(s) {
		t.Error("required not detected")
	}
	s.Meta.Capabilities["tool_calling"] = "optional"
	if RequiresToolCalling(s) {
		t.Error("optional treated as required")
	}
	if RequiresToolCalling(Skill{}) {
		t.Error("empty capabilities treated as required")
	}
}
