package tui

import (
	"fmt"
	"os"
	"strings"

	"github.com/patrikcze/llmtui/internal/config"
	"github.com/patrikcze/llmtui/internal/prompt"
	"github.com/patrikcze/llmtui/internal/skill"
	"github.com/patrikcze/llmtui/internal/tools"
)

// skillCatalogMaxBytes bounds the compact catalog text added to prompts.
const skillCatalogMaxBytes = 4096

// skillOptionsFromConfig translates the config section into manager options.
// The default discovery paths derive from the platform config dir and the
// workspace llmtui was launched from (same root the tool runner confines to).
func skillOptionsFromConfig(cfg *config.Config) skill.Options {
	wd, _ := os.Getwd()
	paths := skill.DefaultPaths(wd)
	paths.Extra = cfg.Skills.Paths
	paths.ExtraPluginDirs = cfg.Plugins.Paths
	return skill.Options{
		Enabled: cfg.Skills.Enabled,
		Paths:   paths,
		Limits: skill.Limits{
			MaxSkillBytes:       cfg.Skills.MaxSkillKB * 1024,
			MaxActive:           cfg.Skills.MaxActive,
			MaxTotalActiveBytes: cfg.Skills.MaxTotalActiveKB * 1024,
		},
		EnabledPlugins: cfg.Plugins.Enabled,
		ExposeCatalog:  cfg.Skills.ExposeCatalogToModel,
	}
}

// skillLoadAvailable reports whether the model may drive skill loading on
// the next request: the subsystem is on, the catalog is exposed, at least
// one skill is available, and the agent tool loop is enabled (skill_load
// rides the same loop as every other tool).
func (m *Model) skillLoadAvailable() bool {
	return m.skillMgr != nil && m.skillMgr.ExposeCatalog() &&
		m.toolsOn && m.toolRunner != nil &&
		len(m.skillMgr.Skills()) > 0
}

// promptSkills renders the active skill snapshots for the prompt composer.
func (m *Model) promptSkills() []prompt.SkillPrompt {
	if m.skillMgr == nil || !m.skillMgr.Enabled() {
		return nil
	}
	active := m.skillMgr.Active()
	out := make([]prompt.SkillPrompt, 0, len(active))
	for _, a := range active {
		out = append(out, prompt.SkillPrompt{
			ID:      a.Skill.Meta.ID,
			Source:  a.Skill.QualifiedID(),
			Path:    a.Skill.Path,
			Version: a.Skill.Meta.Version,
			Body:    a.Skill.Body,
		})
	}
	return out
}

// promptSkillCatalog returns the compact catalog text, or "" when
// model-driven loading is not available on this request.
func (m *Model) promptSkillCatalog() string {
	if !m.skillLoadAvailable() {
		return ""
	}
	return m.skillMgr.CatalogText(skillCatalogMaxBytes)
}

// activeSkillIDs returns the qualified IDs of the active skills, for
// /debug last.
func (m *Model) activeSkillIDs() []string {
	if m.skillMgr == nil {
		return nil
	}
	active := m.skillMgr.Active()
	out := make([]string, 0, len(active))
	for _, a := range active {
		out = append(out, a.Skill.QualifiedID()+" ("+string(a.Scope)+")")
	}
	return out
}

// endAgentRun clears run-scoped skill activations. Called when a run reaches
// its final answer, fails, is cancelled, or is answered from the cache.
func (m *Model) endAgentRun() {
	if m.agentRunActive() {
		return
	}
	m.releaseAgentContext()
	if m.skillMgr == nil {
		return
	}
	if cleared := m.skillMgr.ClearRun(); len(cleared) > 0 {
		m.notice = fmt.Sprintf("◈ run finished — run-scoped skill(s) deactivated: %s", strings.Join(cleared, ", "))
	}
}

func workspaceSkillApprovalKey(s skill.Skill) string {
	return s.QualifiedID() + "@" + s.Hash
}

// workspaceSkillForCall resolves only model-driven workspace skill loads.
// User and explicitly configured extra-path skills keep their existing
// approval-free behavior.
func (m *Model) workspaceSkillForCall(c tools.Call) (skill.Skill, bool) {
	if c.Tool != tools.ToolSkillLoad || m.skillMgr == nil || c.InputErr != "" {
		return skill.Skill{}, false
	}
	id := strings.TrimSpace(c.Path)
	if id == "" {
		id = strings.TrimSpace(c.Body)
	}
	s, err := m.skillMgr.Resolve(id)
	return s, err == nil && s.Source == skill.SourceWorkspace
}

func (m *Model) workspaceSkillNeedsApproval(c tools.Call) bool {
	s, ok := m.workspaceSkillForCall(c)
	return ok && !m.workspaceSkillApprovals[workspaceSkillApprovalKey(s)]
}

func (m *Model) approveWorkspaceSkill(s skill.Skill) {
	if s.Source != skill.SourceWorkspace {
		return
	}
	if m.workspaceSkillApprovals == nil {
		m.workspaceSkillApprovals = make(map[string]bool)
	}
	m.workspaceSkillApprovals[workspaceSkillApprovalKey(s)] = true
}

func (m *Model) approveWorkspaceSkills(calls []tools.Call) {
	for _, c := range calls {
		if s, ok := m.workspaceSkillForCall(c); ok {
			m.approveWorkspaceSkill(s)
		}
	}
}

// runSkillLoader adapts the skill manager to the tools.SkillLoader seam. It
// is called from tool-execution goroutines; the manager is mutex-protected,
// so activation is race-safe with prompt composition on the update goroutine.
type runSkillLoader struct {
	mgr *skill.Manager
}

func (l runSkillLoader) LoadSkillForRun(id string) (string, error) {
	s, err := l.mgr.Activate(id, skill.ScopeRun)
	if err != nil {
		return "", err
	}
	name := s.Meta.Name
	if v := strings.TrimSpace(s.Meta.Version); v != "" {
		name += " v" + v
	}
	return fmt.Sprintf("Skill %q (%s) was loaded for the current run. Its instructions will be included from your next turn on — continue with the task and follow them.", s.Meta.ID, name), nil
}

// syncSkillLoader points the tool runner's skill seam at the manager, or
// removes it when model-driven loading is unavailable, so skill_load can
// never activate anything while the feature is off.
func (m *Model) syncSkillLoader() {
	if m.toolRunner == nil {
		return
	}
	if m.skillMgr != nil && m.skillMgr.ExposeCatalog() {
		m.toolRunner.Skills = runSkillLoader{mgr: m.skillMgr}
	} else {
		m.toolRunner.Skills = nil
	}
}
