package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/skill"
	"github.com/patrikcze/llmtui/internal/tools"
)

// writeTestSkill drops a valid SKILL.md under dir/<id>/.
func writeTestSkill(t *testing.T, dir, id, body string) {
	t.Helper()
	doc := "---\nschema_version: 1\nid: " + id + "\nname: " + id +
		"\nversion: 1.0.0\ndescription: Test skill " + id + ".\n---\n" + body
	path := filepath.Join(dir, id, "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupSkills points the model's skill manager at a temp skill dir with the
// given skills and (re)wires the tool runner's loader seam.
func setupSkills(t *testing.T, m *Model, skills map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for id, body := range skills {
		writeTestSkill(t, dir, id, body)
	}
	m.skillMgr.Configure(skill.Options{
		Enabled:       true,
		ExposeCatalog: true,
		Paths:         skill.Paths{UserDir: dir},
	})
	m.syncSkillLoader()
	return dir
}

// agentModel returns a test model with tools on and one skill available.
func agentModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	setupSkills(t, m, map[string]string{
		"go-agent-loop-review": "Trace the loop. Verify message ordering.",
	})
	return m
}

func composedSystem(t *testing.T, m *Model) string {
	t.Helper()
	out, _ := m.compose("next message", nil, true)
	if len(out.Messages) == 0 || out.Messages[0].Role != provider.RoleSystem {
		t.Fatalf("no system message: %+v", out.Messages)
	}
	return out.Messages[0].Content
}

func TestComposeIncludesActiveSkillWithProvenance(t *testing.T) {
	m := agentModel(t)
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}
	sys := composedSystem(t, m)
	if !strings.Contains(sys, "Verify message ordering.") {
		t.Error("active skill body missing from the composed system prompt")
	}
	if !strings.Contains(sys, `<skill id="go-agent-loop-review" source="user:go-agent-loop-review" version="1.0.0">`) {
		t.Errorf("skill provenance missing:\n%s", sys)
	}
	if !strings.Contains(sys, "do not grant permissions") &&
		!strings.Contains(sys, "They do not grant permissions") {
		t.Error("subordination preamble missing")
	}
	// Core system prompt stays first, above skills.
	core := strings.Index(sys, "You are a helpful local assistant.")
	skillIdx := strings.Index(sys, "<active_skills>")
	if core < 0 || skillIdx < 0 || core > skillIdx {
		t.Errorf("core system prompt must precede skills (core=%d skills=%d)", core, skillIdx)
	}
	// The preview sections carry provenance for /prompt preview.
	out, _ := m.compose("x", nil, true)
	found := false
	for _, s := range out.Sections {
		if s.Title == "Active Skills" {
			found = true
		}
	}
	if !found {
		t.Error("Active Skills section missing from preview")
	}
}

func TestComposeExcludesInactiveSkills(t *testing.T) {
	m := agentModel(t)
	sys := composedSystem(t, m)
	if strings.Contains(sys, "Verify message ordering.") {
		t.Error("inactive skill body leaked into the prompt")
	}
	// The catalog (metadata only) is present since skill_load is available.
	if !strings.Contains(sys, "- go-agent-loop-review: Test skill go-agent-loop-review.") {
		t.Errorf("catalog missing:\n%s", sys)
	}
	if strings.Count(sys, "Verify message ordering.") != 0 {
		t.Error("catalog leaked a skill body")
	}
}

func TestCatalogAbsentWhenToolsOff(t *testing.T) {
	m := agentModel(t)
	m.toolsOn = false
	sys := composedSystem(t, m)
	if strings.Contains(sys, "Available optional skills") {
		t.Error("catalog offered while the tool loop is off")
	}
	if specs := m.activeToolSpecs(); len(specs) != 0 {
		t.Errorf("tool specs offered with tools off: %v", specs)
	}
}

func TestSkillOrderingDeterministic(t *testing.T) {
	m := newTestModel(t)
	m.toolsOn = true
	m.toolRunner = tools.NewRunner(t.TempDir(), 64)
	setupSkills(t, m, map[string]string{"alpha": "AAA", "beta": "BBB"})
	if _, err := m.skillMgr.Activate("beta", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}
	if _, err := m.skillMgr.Activate("alpha", skill.ScopeRun); err != nil { // session first, then run
		t.Fatal(err)
	}
	sys := composedSystem(t, m)
	if b, a := strings.Index(sys, "BBB"), strings.Index(sys, "AAA"); b < 0 || a < 0 || b > a {
		t.Errorf("skill order wrong (beta=%d alpha=%d)", b, a)
	}
}

func TestActiveToolSpecsIncludeSkillLoad(t *testing.T) {
	m := agentModel(t)
	found := false
	for _, s := range m.activeToolSpecs() {
		if s.Name == tools.ToolSkillLoad {
			found = true
		}
	}
	if !found {
		t.Error("skill_load spec not offered")
	}
	// Not offered when the catalog is off.
	m.skillMgr.Configure(skill.Options{Enabled: true, ExposeCatalog: false, Paths: m.skillMgr.SearchPaths()})
	m.syncSkillLoader()
	for _, s := range m.activeToolSpecs() {
		if s.Name == tools.ToolSkillLoad {
			t.Error("skill_load offered while catalog exposure is off")
		}
	}
	if m.toolRunner.Skills != nil {
		t.Error("loader seam still wired while exposure is off")
	}
}

func TestCacheKeyVariesWithSkills(t *testing.T) {
	m := agentModel(t)
	base := m.cacheKey("hello", nil).Hash()
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}
	withSkill := m.cacheKey("hello", nil).Hash()
	if base == withSkill {
		t.Error("cache key ignores active skills")
	}
	if err := m.skillMgr.Deactivate("go-agent-loop-review"); err != nil {
		t.Fatal(err)
	}
	if m.cacheKey("hello", nil).Hash() != base {
		t.Error("cache key not restored after deactivation")
	}
}

// TestSkillLoadToolLoop drives the scripted flow: the model calls skill_load,
// the result answers the same call ID, the next inference includes the skill,
// and the final answer clears the run-scoped activation.
func TestSkillLoadToolLoop(t *testing.T) {
	m := agentModel(t)
	m.session.AddUser("review my agent loop")
	m.thinking = true
	m.streamStart = m.startedAt

	_, cmd := m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDone,
		ToolCalls: []provider.ToolCall{{
			ID: "call_7", Name: tools.ToolSkillLoad,
			Arguments: `{"skill":"go-agent-loop-review"}`,
		}},
	}, ok: true, gen: m.streamGen})
	if cmd == nil {
		t.Fatal("skill_load must continue the loop without approval")
	}

	// Activated for the run.
	active := m.skillMgr.Active()
	if len(active) != 1 || active[0].Scope != skill.ScopeRun || active[0].Skill.Meta.ID != "go-agent-loop-review" {
		t.Fatalf("active = %+v", active)
	}

	// Message ordering: assistant tool call, then a role:tool result with the
	// matching call ID confirming activation.
	msgs := m.session.Messages
	if len(msgs) < 3 {
		t.Fatalf("messages = %d", len(msgs))
	}
	asst, res := msgs[len(msgs)-2], msgs[len(msgs)-1]
	if asst.Role != provider.RoleAssistant || len(asst.ToolCalls) != 1 || asst.ToolCalls[0].ID != "call_7" {
		t.Fatalf("assistant message = %+v", asst)
	}
	if res.Role != provider.RoleTool || res.ToolCallID != "call_7" {
		t.Fatalf("tool result = %+v", res)
	}
	if !strings.Contains(res.Content, "loaded for the current run") {
		t.Errorf("result content = %q", res.Content)
	}

	// The continuation (already composed by continueChat) includes the skill.
	foundSection := false
	for _, s := range m.lastDebug.Sections {
		if s.Title == "Active Skills" && strings.Contains(s.Content, "Verify message ordering.") {
			foundSection = true
		}
	}
	if !foundSection {
		t.Errorf("continuation prompt missing the loaded skill: %+v", m.lastDebug.Sections)
	}

	// The model streams its final answer; the run ends; run skills clear.
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{Type: provider.EventDelta, Delta: "Reviewed."}, ok: true, gen: m.streamGen})
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{Type: provider.EventDone}, ok: true, gen: m.streamGen})
	if len(m.skillMgr.Active()) != 0 {
		t.Error("run-scoped skill survived the final answer")
	}
}

// TestSkillLoadThenNormalTool verifies a loaded skill stays active while the
// model continues with an ordinary built-in tool in the same run.
func TestSkillLoadThenNormalTool(t *testing.T) {
	m := agentModel(t)
	m.session.AddUser("review it")
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:      provider.EventDone,
		ToolCalls: []provider.ToolCall{{ID: "c1", Name: tools.ToolSkillLoad, Arguments: `{"skill":"go-agent-loop-review"}`}},
	}, ok: true, gen: m.streamGen})

	// Next round: a read-only list_dir call runs and the skill stays active.
	m.thinking = true
	_, cmd := m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:      provider.EventDone,
		ToolCalls: []provider.ToolCall{{ID: "c2", Name: tools.ToolListDir, Arguments: `{}`}},
	}, ok: true, gen: m.streamGen})
	if cmd == nil {
		t.Fatal("list_dir continuation missing")
	}
	if len(m.skillMgr.Active()) != 1 {
		t.Error("skill deactivated mid-run")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || last.ToolCallID != "c2" {
		t.Errorf("tool result = %+v", last)
	}
}

func TestSkillLoadUnknownSkillRecoverable(t *testing.T) {
	m := agentModel(t)
	m.session.AddUser("go")
	m.thinking = true
	_, cmd := m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:      provider.EventDone,
		ToolCalls: []provider.ToolCall{{ID: "c1", Name: tools.ToolSkillLoad, Arguments: `{"skill":"does-not-exist"}`}},
	}, ok: true, gen: m.streamGen})
	if cmd == nil {
		t.Fatal("an unknown skill must produce a recoverable tool error, not a dead end")
	}
	if len(m.skillMgr.Active()) != 0 {
		t.Error("unknown skill activated something")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if last.Role != provider.RoleTool || !strings.HasPrefix(last.Content, "error:") ||
		!strings.Contains(last.Content, "no skill named") {
		t.Errorf("tool result = %+v", last)
	}
}

func TestSkillLoadMalformedArguments(t *testing.T) {
	m := agentModel(t)
	m.session.AddUser("go")
	m.thinking = true
	_, cmd := m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type:      provider.EventDone,
		ToolCalls: []provider.ToolCall{{ID: "c1", Name: tools.ToolSkillLoad, Arguments: `{"ski`}},
	}, ok: true, gen: m.streamGen})
	if cmd == nil {
		t.Fatal("malformed arguments must still answer the call")
	}
	if len(m.skillMgr.Active()) != 0 {
		t.Error("malformed arguments activated a skill")
	}
	last := m.session.Messages[len(m.session.Messages)-1]
	if !strings.Contains(last.Content, "invalid arguments for skill_load") {
		t.Errorf("tool result = %q", last.Content)
	}
}

func TestSkillLoadRepeatedIsIdempotent(t *testing.T) {
	m := agentModel(t)
	m.session.AddUser("go")
	for i := 0; i < 3; i++ {
		m.thinking = true
		m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
			Type:      provider.EventDone,
			ToolCalls: []provider.ToolCall{{Name: tools.ToolSkillLoad, Arguments: `{"skill":"go-agent-loop-review"}`}},
		}, ok: true, gen: m.streamGen})
	}
	if len(m.skillMgr.Active()) != 1 {
		t.Errorf("repeated loads produced %d activations", len(m.skillMgr.Active()))
	}
	// The per-turn budget still bounds the loop.
	if m.toolDepth != 3 {
		t.Errorf("toolDepth = %d", m.toolDepth)
	}
}

func TestFencedSkillLoadWorksAndPlainTextDoesNot(t *testing.T) {
	m := agentModel(t)
	m.toolsNative = false

	// Text that merely resembles a tool call must not execute.
	m.session.AddUser("hi")
	m.session.AddAssistant(`I would call {"tool": "skill_load", "skill": "go-agent-loop-review"} now.`)
	if cmd := m.maybeRunTools(); cmd != nil {
		t.Fatal("plain JSON text executed as a tool call")
	}
	if len(m.skillMgr.Active()) != 0 {
		t.Fatal("plain text activated a skill")
	}

	// The validated fenced protocol does work.
	m.session.AddAssistant("```tool skill_load go-agent-loop-review\n```")
	if cmd := m.maybeRunTools(); cmd == nil {
		t.Fatal("fenced skill_load did not run")
	}
	active := m.skillMgr.Active()
	if len(active) != 1 || active[0].Scope != skill.ScopeRun {
		t.Fatalf("active = %+v", active)
	}
}

func TestRunSkillsClearedOnCancelAndFailure(t *testing.T) {
	m := agentModel(t)
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeRun); err != nil {
		t.Fatal(err)
	}

	m.thinking = true
	m.streamFailed(os.ErrDeadlineExceeded)
	if len(m.skillMgr.Active()) != 0 {
		t.Error("run skill survived a failed run")
	}

	// Session-scoped skills survive both failure and run completion.
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}
	m.thinking = true
	m.streamFailed(os.ErrDeadlineExceeded)
	m.endAgentRun()
	if len(m.skillMgr.Active()) != 1 {
		t.Error("session skill did not survive")
	}
}

func TestSessionSkillsPersistAndRestore(t *testing.T) {
	m := agentModel(t)
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}
	rec := m.sessionRecord()
	if len(rec.Skills) != 1 || rec.Skills[0].ID != "go-agent-loop-review" || rec.Skills[0].Scope != "session" {
		t.Fatalf("record skills = %+v", rec.Skills)
	}
	if rec.Skills[0].Hash == "" {
		t.Error("persisted ref missing content hash")
	}

	// Run-scoped skills are never persisted.
	m.skillMgr.ClearAll()
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeRun); err != nil {
		t.Fatal(err)
	}
	if got := m.sessionRecord().Skills; len(got) != 0 {
		t.Errorf("run-scoped skill persisted: %+v", got)
	}

	// Restore into a fresh model sharing the same skill dir.
	m.skillMgr.ClearAll()
	m.adoptSession("restored", rec)
	active := m.skillMgr.Active()
	if len(active) != 1 || active[0].Scope != skill.ScopeSession {
		t.Errorf("restored = %+v", active)
	}

	// A ref to a missing skill warns and restores nothing.
	rec.Skills[0].ID, rec.Skills[0].Source = "gone", "user"
	m.adoptSession("restored2", rec)
	if len(m.skillMgr.Active()) != 0 {
		t.Error("missing skill silently restored")
	}
	if !strings.Contains(m.errText, "no longer available") {
		t.Errorf("errText = %q", m.errText)
	}
}

func TestSkillsUseCommandScopes(t *testing.T) {
	m := agentModel(t)
	if cmd := cmdSkills(m, "use go-agent-loop-review --scope run"); cmd != nil {
		t.Fatal("unexpected command")
	}
	if scope, ok := m.skillMgr.IsActive("user:go-agent-loop-review"); !ok || scope != skill.ScopeRun {
		t.Fatalf("scope = %v ok=%v", scope, ok)
	}
	// Default scope is session.
	cmdSkills(m, "disable go-agent-loop-review")
	cmdSkills(m, "use go-agent-loop-review")
	if scope, _ := m.skillMgr.IsActive("user:go-agent-loop-review"); scope != skill.ScopeSession {
		t.Errorf("default scope = %v, want session", scope)
	}
	// Bad input gets a usage error.
	cmdSkills(m, "use go-agent-loop-review --scope forever")
	if !strings.Contains(m.errText, "unknown scope") {
		t.Errorf("errText = %q", m.errText)
	}
	cmdSkills(m, "use nope")
	if !strings.Contains(m.errText, "no skill named") {
		t.Errorf("errText = %q", m.errText)
	}
}

func TestSkillsPickerNavigatesAndTogglesSessionActivation(t *testing.T) {
	m := newTestModel(t)
	setupSkills(t, m, map[string]string{
		"alpha": "Alpha instructions.",
		"beta":  "Beta instructions.",
	})

	cmdSkills(m, "list")
	if !m.overlayOpen || m.pickerKind != pickerSkill {
		t.Fatal("/skills list should open the skills picker")
	}
	if len(m.pickerItems) != 2 || !strings.Contains(m.viewport.View(), "enter activate/deactivate") {
		t.Fatalf("skills picker = items %v, view:\n%s", m.pickerItems, m.viewport.View())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	selected := m.pickerItems[m.pickerIdx]
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.overlayOpen {
		t.Error("activating a skill should close the picker")
	}
	if scope, active := m.skillMgr.IsActive(selected); !active || scope != skill.ScopeSession {
		t.Fatalf("selected skill %q scope = %q, active = %v; want active session scope", selected, scope, active)
	}

	cmdSkills(m, "list")
	if m.pickerItems[m.pickerIdx] != selected {
		t.Errorf("picker index = %d, want active skill %q", m.pickerIdx, selected)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if _, active := m.skillMgr.IsActive(selected); active {
		t.Errorf("selected skill %q remained active after toggling", selected)
	}
}

func TestSkillsPickerActiveSkillDoesNotTruncateNavigation(t *testing.T) {
	m := newTestModel(t)
	setupSkills(t, m, map[string]string{
		"alpha":   "Alpha instructions.",
		"beta":    "Beta instructions.",
		"delta":   "Delta instructions.",
		"epsilon": "Epsilon instructions.",
		"gamma":   "Gamma instructions.",
	})
	if _, err := m.skillMgr.Activate("beta", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}

	cmdSkills(m, "list")
	if got, want := len(m.pickerItems), 5; got != want {
		t.Fatalf("picker items = %d, want %d: %v", got, want, m.pickerItems)
	}
	if got := m.pickerItems[m.pickerIdx]; got != "user:beta" {
		t.Fatalf("initial selection = %q, want active skill user:beta", got)
	}

	for range 3 {
		m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if got := m.pickerItems[m.pickerIdx]; got != "user:gamma" {
		t.Errorf("selection after moving to the last row = %q, want user:gamma", got)
	}
}

func TestSkillsWorkWithoutToolSupport(t *testing.T) {
	m := newTestModel(t) // tools stay off: no native calling, no fenced protocol
	setupSkills(t, m, map[string]string{"secure-powershell": "Always set StrictMode."})
	if cmd := cmdSkills(m, "use secure-powershell"); cmd != nil {
		t.Fatal("unexpected command")
	}
	sys := composedSystem(t, m)
	if !strings.Contains(sys, "Always set StrictMode.") {
		t.Error("explicitly activated skill missing without tool support")
	}
	if strings.Contains(sys, "Available optional skills") {
		t.Error("catalog offered to a model that cannot call skill_load")
	}
}

func TestSkillsOverlaysRender(t *testing.T) {
	m := agentModel(t)
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}

	list := m.skillsListOverlay()
	if !strings.Contains(list, "go-agent-loop-review") || !strings.Contains(list, "session") {
		t.Errorf("list overlay = %q", list)
	}
	s, err := m.skillMgr.Resolve("go-agent-loop-review")
	if err != nil {
		t.Fatal(err)
	}
	inspect := m.skillsInspectOverlay(s)
	for _, want := range []string{"user:go-agent-loop-review", "1.0.0", s.Hash, "active (session)", "Trace the loop."} {
		if !strings.Contains(inspect, want) {
			t.Errorf("inspect overlay missing %q", want)
		}
	}
	activeOv := m.skillsActiveOverlay()
	if !strings.Contains(activeOv, "user:go-agent-loop-review") {
		t.Errorf("active overlay = %q", activeOv)
	}
	status := m.skillsStatusOverlay()
	if !strings.Contains(status, "model-driven load") {
		t.Errorf("status overlay = %q", status)
	}
	paths := m.skillsPathsOverlay()
	if !strings.Contains(paths, "exists") {
		t.Errorf("paths overlay = %q", paths)
	}
}

func TestPluginsCommands(t *testing.T) {
	m := newTestModel(t)
	pluginDir := t.TempDir()
	root := filepath.Join(pluginDir, "jira-tools")
	writeTestSkill(t, filepath.Join(root, "skills"), "worklog", "Prepare the worklog.")
	manifest := "schema_version: 1\nid: jira-tools\nname: Jira Tools\nversion: 1.0.0\ndescription: Jira skills.\nskills:\n  - path: skills/worklog/SKILL.md\n"
	if err := os.WriteFile(filepath.Join(root, "plugin.yaml"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	m.skillMgr.Configure(skill.Options{Enabled: true, Paths: skill.Paths{UserPluginDir: pluginDir}})

	list := m.pluginsListOverlay()
	if !strings.Contains(list, "jira-tools") || !strings.Contains(list, "disabled") {
		t.Errorf("list = %q", list)
	}

	cmdPlugins(m, "enable jira-tools")
	if !strings.Contains(m.notice, "enabled") {
		t.Errorf("notice = %q", m.notice)
	}
	if len(m.skillMgr.Skills()) != 1 {
		t.Fatal("plugin skill not registered")
	}
	if len(m.skillMgr.Active()) != 0 {
		t.Error("enable activated a skill")
	}
	inspect := m.pluginsInspectOverlay("jira-tools")
	if !strings.Contains(inspect, "skills/worklog/SKILL.md") || !strings.Contains(inspect, "enabled") {
		t.Errorf("inspect = %q", inspect)
	}

	cmdPlugins(m, "disable jira-tools")
	if len(m.skillMgr.Skills()) != 0 {
		t.Error("disable left plugin skills registered")
	}

	cmdPlugins(m, "enable nope")
	if !strings.Contains(m.errText, "no plugin named") {
		t.Errorf("errText = %q", m.errText)
	}
}

func TestHelpAndCompletionIncludeSkills(t *testing.T) {
	m := newTestModel(t)
	help := m.helpOverlay("")
	if !strings.Contains(help, "/skills") || !strings.Contains(help, "/plugins") {
		t.Error("help missing skills/plugins commands")
	}
	m.input.SetValue("/skil")
	m.updateSuggestions()
	found := false
	for _, c := range m.sugs {
		if c.name == "skills" {
			found = true
		}
	}
	if !found {
		t.Errorf("completion missing /skills: %+v", m.sugs)
	}
}

func TestCompactionKeepsActiveSkills(t *testing.T) {
	m := agentModel(t)
	if _, err := m.skillMgr.Activate("go-agent-loop-review", skill.ScopeSession); err != nil {
		t.Fatal(err)
	}
	// Force compression: tiny window, long history.
	m.cfg.Context.MaxContextTokens = 600
	m.cfg.Context.ReserveResponseTokens = 100
	for i := 0; i < 30; i++ {
		m.session.AddUser(strings.Repeat("question ", 30))
		m.session.AddAssistant(strings.Repeat("answer ", 30))
	}
	out, decision := m.compose("and now?", nil, false)
	if !decision.Compress {
		t.Fatal("test setup: compaction did not trigger")
	}
	if !strings.Contains(out.Messages[0].Content, "Verify message ordering.") {
		t.Error("compaction removed the active skill from the system prompt")
	}
}

func TestSkillLoadApprovalFree(t *testing.T) {
	m := agentModel(t)
	calls := tools.CallsFromNative([]provider.ToolCall{{ID: "c", Name: tools.ToolSkillLoad, Arguments: `{"skill":"go-agent-loop-review"}`}})
	if m.callNeedsApproval(calls[0]) {
		t.Error("skill_load must not require approval — it grants nothing")
	}
}

// TestSkillLoadDuringPendingApprovalOrdering guards message ordering when a
// skill_load arrives in the same batch as a call that needs approval: the
// whole batch waits, and approving runs both.
func TestSkillLoadBatchWithWriteWaitsForApproval(t *testing.T) {
	m := agentModel(t)
	m.session.AddUser("go")
	m.thinking = true
	m.handleStreamEvent(streamEventMsg{event: provider.ChatEvent{
		Type: provider.EventDone,
		ToolCalls: []provider.ToolCall{
			{ID: "c1", Name: tools.ToolSkillLoad, Arguments: `{"skill":"go-agent-loop-review"}`},
			{ID: "c2", Name: tools.ToolWriteFile, Arguments: `{"path":"a.txt","content":"x"}`},
		},
	}, ok: true, gen: m.streamGen})
	if len(m.pendingCalls) != 2 {
		t.Fatalf("pendingCalls = %d, want 2", len(m.pendingCalls))
	}
	if len(m.skillMgr.Active()) != 0 {
		t.Fatal("skill activated before the batch was approved")
	}
	_, cmd := m.updateToolApproval(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'y'}})
	if cmd == nil {
		t.Fatal("approval did not run the batch")
	}
	if len(m.skillMgr.Active()) != 1 {
		t.Error("skill not activated after approval")
	}
}
