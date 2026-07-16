package tui

import (
	"fmt"
	"os"
	"sort"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/skill"
	"github.com/patrikcze/llmtui/internal/tools"
)

// --- /skills -----------------------------------------------------------------

func cmdSkills(m *Model, args string) tea.Cmd {
	if m.skillMgr == nil {
		return m.fail("skills unavailable")
	}
	sub, rest := splitArgs(args)
	switch sub {
	case "", "status":
		m.openOverlay(m.skillsStatusOverlay())
	case "list":
		m.openOverlay(m.skillsListOverlay())
	case "active":
		m.openOverlay(m.skillsActiveOverlay())
	case "inspect":
		if rest == "" {
			return m.fail("usage: /skills inspect <id> (see /skills list)")
		}
		s, err := m.skillMgr.Resolve(rest)
		if err != nil {
			return m.fail(err.Error())
		}
		m.openOverlay(m.skillsInspectOverlay(s))
	case "use", "activate":
		return m.skillsUse(rest)
	case "disable", "deactivate", "unuse":
		if rest == "" {
			return m.fail("usage: /skills disable <id> (deactivates it; the file stays on disk)")
		}
		if err := m.skillMgr.Deactivate(rest); err != nil {
			return m.fail(err.Error())
		}
		m.notice = "◈ skill " + rest + " deactivated"
	case "reload":
		notes := m.skillMgr.Reload()
		m.notice = fmt.Sprintf("◈ skills reloaded — %d available, %d plugin(s), %d warning(s)",
			len(m.skillMgr.Skills()), len(m.skillMgr.Plugins()), len(m.skillMgr.Warnings()))
		if len(notes) > 0 {
			m.errText = strings.Join(notes, "; ")
			m.refreshViewport()
		}
	case "paths":
		m.openOverlay(m.skillsPathsOverlay())
	default:
		return m.fail("usage: /skills [status|list|active|inspect <id>|use <id> [--scope run|session]|disable <id>|reload|paths]")
	}
	return nil
}

// skillsUse parses "<id> [--scope run|session]" and activates the skill.
// The default scope is session, matching llmtui's other in-session choices
// (/template use, /memory on); model-driven skill_load is always run-scoped.
func (m *Model) skillsUse(rest string) tea.Cmd {
	fields := strings.Fields(rest)
	if len(fields) == 0 {
		return m.fail("usage: /skills use <id> [--scope run|session]")
	}
	id := fields[0]
	scope := skill.ScopeSession
	for i := 1; i < len(fields); i++ {
		val := ""
		switch {
		case fields[i] == "--scope" && i+1 < len(fields):
			i++
			val = fields[i]
		case strings.HasPrefix(fields[i], "--scope="):
			val = strings.TrimPrefix(fields[i], "--scope=")
		default:
			return m.fail(fmt.Sprintf("unknown argument %q — usage: /skills use <id> [--scope run|session]", fields[i]))
		}
		if !skill.ValidScope(val) {
			return m.fail(fmt.Sprintf("unknown scope %q (run|session)", val))
		}
		scope = skill.Scope(val)
	}

	s, err := m.skillMgr.Activate(id, scope)
	if err != nil {
		return m.fail(err.Error())
	}
	label := "for this session (/skills disable " + s.Meta.ID + " to remove)"
	if scope == skill.ScopeRun {
		label = "for the next run"
	}
	m.notice = fmt.Sprintf("◈ skill %s activated %s", s.QualifiedID(), label)
	if skill.RequiresToolCalling(s) && (!m.toolsOn || m.toolRunner == nil) {
		m.errText = fmt.Sprintf("skill %q declares tool_calling: required, but workspace tools are off (/tools on)", s.Meta.ID)
		m.refreshViewport()
	}
	return nil
}

func (m *Model) skillsStatusOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("skills") + "\n\n")
	skills := m.skillMgr.Skills()
	active := m.skillMgr.Active()
	sess, run := 0, 0
	for _, a := range active {
		if a.Scope == skill.ScopeSession {
			sess++
		} else {
			run++
		}
	}
	lim := m.skillMgr.Limits()
	m.kv(&b, "enabled", onOff(m.skillMgr.Enabled()))
	m.kv(&b, "available", fmt.Sprintf("%d skill(s)", len(skills)))
	m.kv(&b, "active (session)", fmt.Sprintf("%d", sess))
	m.kv(&b, "active (run)", fmt.Sprintf("%d", run))
	m.kv(&b, "catalog to model", onOff(m.skillMgr.ExposeCatalog()))
	loadState := "no"
	switch {
	case m.skillLoadAvailable():
		loadState = "yes (skill_load offered with the tool specs)"
	case !m.toolsOn:
		loadState = "no — workspace tools are off (/tools on)"
	case len(skills) == 0:
		loadState = "no — no skills discovered"
	case !m.skillMgr.ExposeCatalog():
		loadState = "no — skills.expose_catalog_to_model is off"
	}
	m.kv(&b, "model-driven load", loadState)
	m.kv(&b, "limits", fmt.Sprintf("%d active · %d KB/skill · %d KB total",
		lim.MaxActive, lim.MaxSkillBytes/1024, lim.MaxTotalActiveBytes/1024))
	m.kv(&b, "plugins", fmt.Sprintf("%d discovered, %d enabled", len(m.skillMgr.Plugins()), len(m.skillMgr.EnabledPluginIDs())))
	if warns := m.skillMgr.Warnings(); len(warns) > 0 {
		b.WriteString("\n" + m.theme.UserLabel.Render("warnings") + "\n")
		for _, w := range warns {
			b.WriteString("  " + m.theme.BadgeWarn.Render("⚠ "+w.String()) + "\n")
		}
	}
	b.WriteString("\n" + m.theme.StatusBar.Render("  skills are instructions, not code: activating one adds its text to the\n  prompt and grants no tool permissions — /tools and approvals stay in charge") + "\n")
	b.WriteString("\n" + m.theme.SystemNote.Render("/skills list · /skills use <id> · /skills active · /skills paths"))
	return m.overlayFooter(&b)
}

func (m *Model) skillsListOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("skills") + "\n\n")
	skills := m.skillMgr.Skills()
	if len(skills) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no skills found — add one under a search path (/skills paths) or enable a plugin") + "\n")
		return m.overlayFooter(&b)
	}
	b.WriteString(m.theme.UserLabel.Render(fmt.Sprintf("%-26s %-9s %-22s %-8s %s", "id", "version", "source", "active", "description")) + "\n")
	for _, s := range skills {
		scope, isActive := m.skillMgr.IsActive(s.QualifiedID())
		activeStr := "-"
		if isActive {
			activeStr = string(scope)
		}
		src := string(s.Source)
		if s.Source == skill.SourcePlugin {
			src = "plugin:" + s.PluginID
		}
		row := fmt.Sprintf("%-26s %-9s %-22s %-8s %s",
			s.Meta.ID, orNone(s.Meta.Version), src, activeStr, truncateForRow(s.Meta.Description))
		if isActive {
			b.WriteString("  " + m.theme.StatusValue.Render(row) + "\n")
		} else {
			b.WriteString("  " + m.theme.SystemNote.Render(row) + "\n")
		}
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("/skills inspect <id> · /skills use <id> [--scope run|session]"))
	return m.overlayFooter(&b)
}

func (m *Model) skillsActiveOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("active skills (prompt order)") + "\n\n")
	active := m.skillMgr.Active()
	if len(active) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no active skills — /skills use <id> activates one") + "\n")
		return m.overlayFooter(&b)
	}
	total := 0
	for i, a := range active {
		total += len(a.Skill.Body)
		fmt.Fprintf(&b, "  %s %s %s\n",
			m.theme.BadgeOK.Render(fmt.Sprintf("%d.", i+1)),
			m.theme.StatusValue.Render(a.Skill.QualifiedID()),
			m.theme.StatusBar.Render(fmt.Sprintf("scope %s · %d bytes · hash %.8s", a.Scope, len(a.Skill.Body), a.Skill.Hash)))
	}
	lim := m.skillMgr.Limits()
	fmt.Fprintf(&b, "\n  %s\n", m.theme.StatusBar.Render(fmt.Sprintf(
		"≈ %d of %d KB active-content budget · run-scoped skills clear when the run ends", total/1024, lim.MaxTotalActiveBytes/1024)))
	b.WriteString("\n" + m.theme.SystemNote.Render("/skills disable <id> · full text: /prompt composed"))
	return m.overlayFooter(&b)
}

func (m *Model) skillsInspectOverlay(s skill.Skill) string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("skill: "+s.Meta.ID) + "\n\n")
	m.kv(&b, "name", s.Meta.Name)
	m.kv(&b, "version", orNone(s.Meta.Version))
	m.kv(&b, "source", s.QualifiedID())
	m.kv(&b, "path", s.Path)
	m.kv(&b, "hash", s.Hash)
	m.kv(&b, "size", fmt.Sprintf("%d bytes", s.Size))
	state := "inactive"
	if scope, ok := m.skillMgr.IsActive(s.QualifiedID()); ok {
		state = "active (" + string(scope) + ")"
	}
	m.kv(&b, "activation", state)
	if len(s.Meta.Tags) > 0 {
		m.kv(&b, "tags", strings.Join(s.Meta.Tags, ", "))
	}
	if len(s.Meta.Triggers) > 0 {
		m.kv(&b, "triggers", strings.Join(s.Meta.Triggers, " · "))
	}
	if len(s.Meta.RecommendedTools) > 0 {
		reg := tools.DefaultRegistry()
		registerMCPCapabilities(reg, m.mcpRegistry)
		parts := make([]string, 0, len(s.Meta.RecommendedTools))
		for _, name := range s.Meta.RecommendedTools {
			if _, ok := reg.Get(name); ok {
				parts = append(parts, name)
			} else {
				parts = append(parts, name+" (unavailable)")
			}
		}
		m.kv(&b, "recommended tools", strings.Join(parts, ", ")+"  (informational — grants nothing)")
	}
	if len(s.Meta.Capabilities) > 0 {
		keys := make([]string, 0, len(s.Meta.Capabilities))
		for k := range s.Meta.Capabilities {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+": "+s.Meta.Capabilities[k])
		}
		m.kv(&b, "capabilities", strings.Join(parts, ", "))
	}
	m.kv(&b, "description", "")
	b.WriteString("    " + s.Meta.Description + "\n")

	b.WriteString("\n" + m.theme.UserLabel.Render("content preview") + "\n")
	lines := strings.Split(s.Body, "\n")
	const maxPreview = 16
	for i, l := range lines {
		if i >= maxPreview {
			b.WriteString(m.theme.SystemNote.Render(fmt.Sprintf("  … +%d more lines (full text: the file at the path above)", len(lines)-maxPreview)) + "\n")
			break
		}
		b.WriteString("  " + m.theme.StatusValue.Render(l) + "\n")
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("/skills use "+s.Meta.ID+" [--scope run|session]"))
	return m.overlayFooter(&b)
}

func (m *Model) skillsPathsOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("skill search paths") + "\n\n")
	p := m.skillMgr.SearchPaths()
	writePath := func(label, dir string) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		state := "missing"
		if info, err := os.Stat(dir); err == nil && info.IsDir() {
			state = "exists"
		}
		m.kv(&b, label, dir+"  ("+state+")")
	}
	writePath("user", p.UserDir)
	writePath("workspace", p.WorkspaceDir)
	for i, dir := range p.Extra {
		writePath(fmt.Sprintf("extra %d", i+1), dir)
	}
	b.WriteString("\n" + m.theme.UserLabel.Render("plugin search paths") + "\n")
	writePath("user", p.UserPluginDir)
	writePath("workspace", p.WorkspacePluginDir)
	for i, dir := range p.ExtraPluginDirs {
		writePath(fmt.Sprintf("extra %d", i+1), dir)
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("skills live at <path>/<skill-id>/SKILL.md · plugins at <path>/<plugin-id>/plugin.yaml\nextra paths: skills.paths / plugins.paths in the config · /skills reload rescans"))
	return m.overlayFooter(&b)
}

// --- /plugins ----------------------------------------------------------------

func cmdPlugins(m *Model, args string) tea.Cmd {
	if m.skillMgr == nil {
		return m.fail("plugins unavailable")
	}
	sub, rest := splitArgs(args)
	switch sub {
	case "", "status", "list":
		m.openOverlay(m.pluginsListOverlay())
	case "inspect":
		if rest == "" {
			return m.fail("usage: /plugins inspect <id> (see /plugins list)")
		}
		m.openOverlay(m.pluginsInspectOverlay(rest))
	case "enable":
		if rest == "" {
			return m.fail("usage: /plugins enable <id>")
		}
		p, err := m.skillMgr.EnablePlugin(rest)
		if err != nil {
			return m.fail(err.Error())
		}
		n := len(p.Manifest.Skills)
		m.notice = fmt.Sprintf("◈ plugin %q enabled — %d skill(s) registered (none activated; /skills list)", rest, n)
		if p.Source == skill.SourceWorkspace {
			m.errText = fmt.Sprintf("plugin %q comes from the workspace (.llmtui/plugins) — treat it as untrusted local content and /plugins inspect it before activating its skills", rest)
			m.refreshViewport()
		}
	case "disable":
		if rest == "" {
			return m.fail("usage: /plugins disable <id>")
		}
		deactivated, err := m.skillMgr.DisablePlugin(rest)
		if err != nil {
			return m.fail(err.Error())
		}
		m.notice = fmt.Sprintf("◈ plugin %q disabled", rest)
		if len(deactivated) > 0 {
			m.notice += " — deactivated: " + strings.Join(deactivated, ", ")
		}
	case "reload":
		notes := m.skillMgr.Reload()
		m.notice = fmt.Sprintf("◈ plugins reloaded — %d discovered, %d enabled",
			len(m.skillMgr.Plugins()), len(m.skillMgr.EnabledPluginIDs()))
		if len(notes) > 0 {
			m.errText = strings.Join(notes, "; ")
			m.refreshViewport()
		}
	case "paths":
		m.openOverlay(m.skillsPathsOverlay())
	default:
		return m.fail("usage: /plugins [status|list|inspect <id>|enable <id>|disable <id>|reload|paths]")
	}
	return nil
}

func (m *Model) pluginsListOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("plugins") + "\n\n")
	plugins := m.skillMgr.Plugins()
	if len(plugins) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no plugins found — put one at <plugin path>/<id>/plugin.yaml (/plugins paths)") + "\n")
		return m.overlayFooter(&b)
	}
	b.WriteString(m.theme.UserLabel.Render(fmt.Sprintf("%-20s %-9s %-11s %-9s %s", "id", "version", "source", "state", "description")) + "\n")
	for _, p := range plugins {
		state := "disabled"
		switch {
		case p.Err != nil:
			state = "invalid"
		case p.Enabled:
			state = "enabled"
		}
		desc := p.Manifest.Description
		if p.Err != nil {
			desc = p.Err.Error()
		}
		row := fmt.Sprintf("%-20s %-9s %-11s %-9s %s",
			p.Manifest.ID, orNone(p.Manifest.Version), string(p.Source), state, truncateForRow(desc))
		switch {
		case p.Err != nil:
			b.WriteString("  " + m.theme.BadgeWarn.Render(row) + "\n")
		case p.Enabled:
			b.WriteString("  " + m.theme.StatusValue.Render(row) + "\n")
		default:
			b.WriteString("  " + m.theme.SystemNote.Render(row) + "\n")
		}
	}
	b.WriteString("\n" + m.theme.StatusBar.Render("  enabling a plugin registers its skills and nothing else: no skill is\n  activated, no code runs, no MCP server starts") + "\n")
	b.WriteString("\n" + m.theme.SystemNote.Render("/plugins enable <id> · /plugins inspect <id> · persist via plugins.enabled in config"))
	return m.overlayFooter(&b)
}

func (m *Model) pluginsInspectOverlay(id string) string {
	var b strings.Builder
	plugins := m.skillMgr.Plugins()
	var found *skill.Plugin
	for i := range plugins {
		if plugins[i].Manifest.ID == id {
			found = &plugins[i]
			break
		}
	}
	if found == nil {
		b.WriteString(m.theme.Badge.Render("not found") + "\n\n  no plugin named " + id + "\n")
		return m.overlayFooter(&b)
	}
	p := *found
	b.WriteString(m.theme.Badge.Render("plugin: "+p.Manifest.ID) + "\n\n")
	m.kv(&b, "name", p.Manifest.Name)
	m.kv(&b, "version", orNone(p.Manifest.Version))
	m.kv(&b, "source", string(p.Source))
	m.kv(&b, "root", p.Root)
	state := "disabled"
	if p.Enabled {
		state = "enabled"
	}
	m.kv(&b, "state", state)
	m.kv(&b, "description", "")
	b.WriteString("    " + p.Manifest.Description + "\n")
	if p.Err != nil {
		b.WriteString("\n  " + m.theme.ErrorText.Render("✗ "+p.Err.Error()) + "\n")
	}
	if len(p.Manifest.Skills) > 0 {
		b.WriteString("\n" + m.theme.UserLabel.Render("declared skills") + "\n")
		for _, ref := range p.Manifest.Skills {
			b.WriteString("  " + m.theme.StatusValue.Render(ref.Path) + "\n")
		}
	}
	if p.Source == skill.SourceWorkspace {
		b.WriteString("\n" + m.theme.BadgeWarn.Render("⚠ workspace plugin — potentially untrusted local content; review before enabling") + "\n")
	}
	b.WriteString("\n" + m.theme.SystemNote.Render("/plugins enable "+p.Manifest.ID+" · /plugins disable "+p.Manifest.ID))
	return m.overlayFooter(&b)
}
