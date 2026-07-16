package skill

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Scope is an activation lifetime.
type Scope string

const (
	// ScopeRun keeps a skill active for the current agent run only; it is
	// cleared when the run produces its final answer, fails, or is cancelled.
	ScopeRun Scope = "run"
	// ScopeSession keeps a skill active until deactivated or the session ends.
	ScopeSession Scope = "session"
)

// ValidScope reports whether s is a known activation scope.
func ValidScope(s string) bool { return s == string(ScopeRun) || s == string(ScopeSession) }

// Limits bounds how much skill content may enter prompts.
type Limits struct {
	// MaxSkillBytes caps one skill file (default DefaultMaxSkillBytes).
	MaxSkillBytes int
	// MaxActive caps concurrently active skills (default 8).
	MaxActive int
	// MaxTotalActiveBytes caps the combined size of active skill bodies
	// (default 256 KiB).
	MaxTotalActiveBytes int
}

func (l Limits) withDefaults() Limits {
	if l.MaxSkillBytes <= 0 {
		l.MaxSkillBytes = DefaultMaxSkillBytes
	}
	if l.MaxActive <= 0 {
		l.MaxActive = 8
	}
	if l.MaxTotalActiveBytes <= 0 {
		l.MaxTotalActiveBytes = 256 * 1024
	}
	return l
}

// Options configures a Manager.
type Options struct {
	Enabled bool
	Paths   Paths
	Limits  Limits
	// EnabledPlugins are plugin IDs enabled from configuration; /plugins
	// enable adds to this set at runtime.
	EnabledPlugins []string
	// ExposeCatalog controls whether a compact skill catalog (and the
	// skill_load tool) may be offered to the model.
	ExposeCatalog bool
}

// Active is one activated skill: a snapshot of the skill content taken at
// activation time, so a reload or plugin change never mutates what an
// in-flight or upcoming inference sees.
type Active struct {
	Skill Skill
	Scope Scope
}

// Ref identifies an activation for session persistence and restore.
type Ref struct {
	ID     string `json:"id"`
	Scope  string `json:"scope"`
	Source string `json:"source"`
	// PluginID qualifies plugin-sourced skills.
	PluginID string `json:"plugin_id,omitempty"`
	Version  string `json:"version,omitempty"`
	Hash     string `json:"hash"`
}

// Manager owns the skill registry and activation state. All methods are safe
// for concurrent use: the tool loop's skill_load executes on a background
// goroutine while the TUI reads state on the update goroutine.
type Manager struct {
	mu             sync.Mutex
	opts           Options
	enabledPlugins map[string]bool

	skills   []Skill
	plugins  []Plugin
	warnings []Warning

	session []Active
	run     []Active
}

// NewManager builds a manager and performs the initial discovery scan.
func NewManager(opts Options) *Manager {
	m := &Manager{}
	m.configureLocked(opts)
	m.rescanLocked()
	return m
}

// Configure replaces the options (config reload) and rescans. Activation
// snapshots survive; activations whose skill no longer resolves are kept but
// reported by Reload-style warnings on the next /skills reload.
func (m *Manager) Configure(opts Options) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configureLocked(opts)
	m.rescanLocked()
}

func (m *Manager) configureLocked(opts Options) {
	opts.Limits = opts.Limits.withDefaults()
	m.opts = opts
	m.enabledPlugins = make(map[string]bool, len(opts.EnabledPlugins))
	for _, id := range opts.EnabledPlugins {
		if id = strings.TrimSpace(id); id != "" {
			m.enabledPlugins[id] = true
		}
	}
}

// Enabled reports whether the skills subsystem is on.
func (m *Manager) Enabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opts.Enabled
}

// ExposeCatalog reports whether the model may see the skill catalog and the
// skill_load tool.
func (m *Manager) ExposeCatalog() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opts.Enabled && m.opts.ExposeCatalog
}

// Limits returns the effective limits.
func (m *Manager) Limits() Limits {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.opts.Limits
}

// SearchPaths returns the skill and plugin discovery locations, for
// /skills paths and /plugins paths.
func (m *Manager) SearchPaths() Paths {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.opts.Paths
	p.Extra = append([]string(nil), p.Extra...)
	p.ExtraPluginDirs = append([]string(nil), p.ExtraPluginDirs...)
	return p
}

// rescanLocked rebuilds the registry from disk. Callers hold m.mu.
func (m *Manager) rescanLocked() {
	m.skills = nil
	m.plugins = nil
	m.warnings = nil

	maxBytes := m.opts.Limits.MaxSkillBytes
	scan := func(dir string, source Source) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		skills, warns := discoverDir(dir, source, maxBytes)
		m.skills = append(m.skills, skills...)
		m.warnings = append(m.warnings, warns...)
	}
	scan(m.opts.Paths.UserDir, SourceUser)
	scan(m.opts.Paths.WorkspaceDir, SourceWorkspace)
	for _, dir := range expandExtra(m.opts.Paths.Extra) {
		scan(dir, SourceExtra)
	}

	scanPlugins := func(dir string, source Source) {
		if strings.TrimSpace(dir) == "" {
			return
		}
		plugins, warns := discoverPlugins(dir, source)
		m.plugins = append(m.plugins, plugins...)
		m.warnings = append(m.warnings, warns...)
	}
	scanPlugins(m.opts.Paths.UserPluginDir, SourceUser)
	scanPlugins(m.opts.Paths.WorkspacePluginDir, SourceWorkspace)
	for _, dir := range expandExtra(m.opts.Paths.ExtraPluginDirs) {
		scanPlugins(dir, SourceExtra)
	}

	// Duplicate plugin IDs: the first discovered wins; later ones are marked
	// broken rather than silently replacing an already-registered plugin.
	seenPlugin := map[string]bool{}
	for i := range m.plugins {
		id := m.plugins[i].Manifest.ID
		if id == "" {
			continue
		}
		if seenPlugin[id] {
			m.plugins[i].Err = fmt.Errorf("duplicate plugin id %q (already provided by another plugin directory)", id)
			continue
		}
		seenPlugin[id] = true
	}

	// Contribute skills from enabled, valid plugins only.
	for _, p := range m.plugins {
		if p.Err != nil || !m.enabledPlugins[p.Manifest.ID] {
			continue
		}
		skills, warns := loadPluginSkills(p, maxBytes)
		m.skills = append(m.skills, skills...)
		m.warnings = append(m.warnings, warns...)
	}

	// Duplicate qualified IDs are impossible per directory scan, but two
	// extra paths (or a plugin) could still collide; drop exact qualified
	// duplicates deterministically and warn.
	seen := map[string]bool{}
	kept := m.skills[:0]
	for _, s := range m.skills {
		qid := s.QualifiedID()
		if seen[qid] {
			m.warnings = append(m.warnings, Warning{Path: s.Path,
				Message: fmt.Sprintf("duplicate skill %s ignored (already registered from another path)", qid)})
			continue
		}
		seen[qid] = true
		kept = append(kept, s)
	}
	m.skills = kept

	// Unqualified duplicates stay registered (addressable by qualified ID)
	// but are surfaced as warnings so the conflict is never silent.
	byID := map[string][]string{}
	for _, s := range m.skills {
		byID[s.Meta.ID] = append(byID[s.Meta.ID], s.QualifiedID())
	}
	ids := make([]string, 0, len(byID))
	for id, qids := range byID {
		if len(qids) > 1 {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	for _, id := range ids {
		m.warnings = append(m.warnings, Warning{
			Message: fmt.Sprintf("skill id %q is provided by multiple sources (%s) — use a qualified id",
				id, strings.Join(byID[id], ", "))})
	}
}

// Reload rescans the configured paths. Active skills keep the content
// snapshot they were activated with; if a currently active skill's on-disk
// content changed (or it disappeared), that is reported so the user can
// decide to re-activate. Returns human-readable change notes.
func (m *Manager) Reload() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rescanLocked()

	var notes []string
	for _, a := range append(append([]Active(nil), m.session...), m.run...) {
		cur, ok := m.lookupLocked(a.Skill.QualifiedID())
		switch {
		case !ok:
			notes = append(notes, fmt.Sprintf("active skill %s no longer exists on disk — the activation snapshot is kept until you deactivate it", a.Skill.QualifiedID()))
		case cur.Hash != a.Skill.Hash:
			notes = append(notes, fmt.Sprintf("active skill %s changed on disk (hash %.8s → %.8s) — the activation snapshot is kept; /skills use %s again to pick up the new content",
				a.Skill.QualifiedID(), a.Skill.Hash, cur.Hash, a.Skill.Meta.ID))
		}
	}
	return notes
}

// Skills returns the registry snapshot in deterministic order.
func (m *Manager) Skills() []Skill {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Skill(nil), m.skills...)
}

// Plugins returns the discovered plugins with their enabled state resolved.
func (m *Manager) Plugins() []Plugin {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := append([]Plugin(nil), m.plugins...)
	for i := range out {
		out[i].Enabled = m.enabledPlugins[out[i].Manifest.ID] && out[i].Err == nil
	}
	return out
}

// Warnings returns discovery/validation warnings from the last scan.
func (m *Manager) Warnings() []Warning {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Warning(nil), m.warnings...)
}

// lookupLocked resolves qualified IDs exactly and unqualified IDs uniquely.
func (m *Manager) lookupLocked(id string) (Skill, bool) {
	for _, s := range m.skills {
		if s.QualifiedID() == id {
			return s, true
		}
	}
	var found []Skill
	for _, s := range m.skills {
		if s.Meta.ID == id {
			found = append(found, s)
		}
	}
	if len(found) == 1 {
		return found[0], true
	}
	return Skill{}, false
}

// Resolve finds a skill by qualified or unqualified ID. An ambiguous
// unqualified ID is an error that lists the qualified alternatives.
func (m *Manager) Resolve(id string) (Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.resolveLocked(id)
}

func (m *Manager) resolveLocked(id string) (Skill, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Skill{}, fmt.Errorf("skill id is required")
	}
	for _, s := range m.skills {
		if s.QualifiedID() == id {
			return s, nil
		}
	}
	var found []Skill
	for _, s := range m.skills {
		if s.Meta.ID == id {
			found = append(found, s)
		}
	}
	switch len(found) {
	case 0:
		return Skill{}, fmt.Errorf("no skill named %q (see /skills list)", id)
	case 1:
		return found[0], nil
	default:
		qids := make([]string, len(found))
		for i, s := range found {
			qids[i] = s.QualifiedID()
		}
		return Skill{}, fmt.Errorf("skill id %q is ambiguous — use one of: %s", id, strings.Join(qids, ", "))
	}
}

// Activate resolves and activates a skill for the given scope. Activation is
// idempotent per skill: re-activating an already active skill refreshes its
// content snapshot and, if the scope differs, moves it to the new scope.
// Activation validates limits and never grants any permission.
func (m *Manager) Activate(id string, scope Scope) (Skill, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.opts.Enabled {
		return Skill{}, fmt.Errorf("skills are disabled (skills.enabled)")
	}
	if scope != ScopeRun && scope != ScopeSession {
		return Skill{}, fmt.Errorf("unknown scope %q (run|session)", scope)
	}
	s, err := m.resolveLocked(id)
	if err != nil {
		return Skill{}, err
	}
	return s, m.activateSkillLocked(s, scope)
}

func (m *Manager) activateSkillLocked(s Skill, scope Scope) error {
	qid := s.QualifiedID()
	m.removeActiveLocked(qid) // re-activation refreshes snapshot and scope

	active := append(append([]Active(nil), m.session...), m.run...)
	if len(active)+1 > m.opts.Limits.MaxActive {
		return fmt.Errorf("cannot activate %s: %d skills are already active (skills.max_active = %d) — /skills disable one first",
			qid, len(active), m.opts.Limits.MaxActive)
	}
	total := len(s.Body)
	for _, a := range active {
		total += len(a.Skill.Body)
	}
	if total > m.opts.Limits.MaxTotalActiveBytes {
		return fmt.Errorf("cannot activate %s: active skills would total %d bytes, over the %d byte budget (skills.max_total_active_kb) — deactivate a skill or raise the limit",
			qid, total, m.opts.Limits.MaxTotalActiveBytes)
	}
	if scope == ScopeSession {
		m.session = append(m.session, Active{Skill: s, Scope: ScopeSession})
	} else {
		m.run = append(m.run, Active{Skill: s, Scope: ScopeRun})
	}
	return nil
}

// removeActiveLocked drops any activation of the qualified ID. Returns true
// if something was removed.
func (m *Manager) removeActiveLocked(qid string) bool {
	removed := false
	filter := func(list []Active) []Active {
		out := list[:0]
		for _, a := range list {
			if a.Skill.QualifiedID() == qid {
				removed = true
				continue
			}
			out = append(out, a)
		}
		return out
	}
	m.session = filter(m.session)
	m.run = filter(m.run)
	return removed
}

// Deactivate removes an activation by qualified or unqualified ID.
func (m *Manager) Deactivate(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	id = strings.TrimSpace(id)
	if id == "" {
		return fmt.Errorf("skill id is required")
	}
	// Try qualified first, then unqualified across active entries.
	for _, a := range m.activeLocked() {
		if a.Skill.QualifiedID() == id || a.Skill.Meta.ID == id {
			m.removeActiveLocked(a.Skill.QualifiedID())
			return nil
		}
	}
	return fmt.Errorf("skill %q is not active (see /skills active)", id)
}

// ClearRun deactivates all run-scoped skills (the run finished, failed, or
// was cancelled) and returns their IDs.
func (m *Manager) ClearRun() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := make([]string, 0, len(m.run))
	for _, a := range m.run {
		ids = append(ids, a.Skill.Meta.ID)
	}
	m.run = nil
	return ids
}

// ClearAll deactivates everything (session reset).
func (m *Manager) ClearAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session, m.run = nil, nil
}

// Active returns the active skills in deterministic prompt order:
// session-scoped in activation order, then run-scoped in activation order.
func (m *Manager) Active() []Active {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.activeLocked()
}

func (m *Manager) activeLocked() []Active {
	out := make([]Active, 0, len(m.session)+len(m.run))
	out = append(out, m.session...)
	out = append(out, m.run...)
	return out
}

// IsActive reports whether the skill (by qualified ID) is active and in
// which scope.
func (m *Manager) IsActive(qid string) (Scope, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.activeLocked() {
		if a.Skill.QualifiedID() == qid {
			return a.Scope, true
		}
	}
	return "", false
}

// FingerprintActive hashes the active skill set (qualified IDs + content
// hashes, in prompt order) for the response-cache key. Empty when nothing is
// active.
func (m *Manager) FingerprintActive() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := m.activeLocked()
	if len(active) == 0 {
		return ""
	}
	h := sha256.New()
	for _, a := range active {
		fmt.Fprintf(h, "%s|%s|%s\n", a.Skill.QualifiedID(), a.Skill.Hash, a.Scope)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// SessionRefs returns persistable references for the session-scoped
// activations only — run-scoped skills are never persisted.
func (m *Manager) SessionRefs() []Ref {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Ref, 0, len(m.session))
	for _, a := range m.session {
		out = append(out, Ref{
			ID:       a.Skill.Meta.ID,
			Scope:    string(a.Scope),
			Source:   string(a.Skill.Source),
			PluginID: a.Skill.PluginID,
			Version:  a.Skill.Meta.Version,
			Hash:     a.Skill.Hash,
		})
	}
	return out
}

// RestoreSession re-resolves saved refs against the current registry and
// activates the ones that still match. A missing skill or one whose content
// hash changed is reported and NOT silently substituted.
func (m *Manager) RestoreSession(refs []Ref) (warnings []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.session, m.run = nil, nil
	if !m.opts.Enabled {
		if len(refs) > 0 {
			warnings = append(warnings, "the saved session used skills, but skills are disabled (skills.enabled) — none restored")
		}
		return warnings
	}
	for _, r := range refs {
		qid := r.Source + ":" + r.ID
		if r.Source == string(SourcePlugin) && r.PluginID != "" {
			qid = string(SourcePlugin) + ":" + r.PluginID + "/" + r.ID
		}
		s, ok := m.lookupLocked(qid)
		if !ok {
			s2, err := m.resolveLocked(r.ID)
			if err != nil {
				warnings = append(warnings, fmt.Sprintf("saved skill %s is no longer available — not restored", qid))
				continue
			}
			s = s2
		}
		if r.Hash != "" && s.Hash != r.Hash {
			warnings = append(warnings, fmt.Sprintf("skill %s changed since this session was saved (hash %.8s → %.8s) — restored with the current content",
				s.QualifiedID(), r.Hash, s.Hash))
		}
		if err := m.activateSkillLocked(s, ScopeSession); err != nil {
			warnings = append(warnings, err.Error())
		}
	}
	return warnings
}

// EnablePlugin marks a plugin enabled and registers its skills. It never
// activates any skill, never runs code, and never touches MCP state.
func (m *Manager) EnablePlugin(id string) (Plugin, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.findPluginLocked(id)
	if !ok {
		return Plugin{}, fmt.Errorf("no plugin named %q (see /plugins list)", id)
	}
	if p.Err != nil {
		return p, fmt.Errorf("plugin %q is invalid: %v", id, p.Err)
	}
	m.enabledPlugins[id] = true
	m.rescanLocked()
	p, _ = m.findPluginLocked(id)
	p.Enabled = true
	return p, nil
}

// DisablePlugin disables a plugin and unregisters its skills. Active
// skills contributed by the plugin are deactivated (reported by ID); an
// in-flight inference is unaffected because it composed from snapshots.
func (m *Manager) DisablePlugin(id string) (deactivated []string, err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.findPluginLocked(id); !ok {
		return nil, fmt.Errorf("no plugin named %q (see /plugins list)", id)
	}
	delete(m.enabledPlugins, id)
	for _, a := range m.activeLocked() {
		if a.Skill.Source == SourcePlugin && a.Skill.PluginID == id {
			m.removeActiveLocked(a.Skill.QualifiedID())
			deactivated = append(deactivated, a.Skill.QualifiedID())
		}
	}
	m.rescanLocked()
	return deactivated, nil
}

// EnabledPluginIDs returns the currently enabled plugin IDs, sorted.
func (m *Manager) EnabledPluginIDs() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.enabledPlugins))
	for id := range m.enabledPlugins {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func (m *Manager) findPluginLocked(id string) (Plugin, bool) {
	for _, p := range m.plugins {
		if p.Manifest.ID == id {
			p.Enabled = m.enabledPlugins[id] && p.Err == nil
			return p, true
		}
	}
	return Plugin{}, false
}

// CatalogText renders the compact skill catalog offered to the model: ID and
// one-line description only, never full bodies. maxBytes bounds the text;
// skills beyond the budget are summarized by count so the list is never
// silently misrepresented as complete.
func (m *Manager) CatalogText(maxBytes int) string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.opts.Enabled || !m.opts.ExposeCatalog || len(m.skills) == 0 {
		return ""
	}
	if maxBytes <= 0 {
		maxBytes = 4096
	}
	var b strings.Builder
	b.WriteString("Available optional skills (activate one with the skill_load tool when it clearly matches the task):\n")
	omitted := 0
	for _, s := range m.skills {
		id := s.Meta.ID
		if m.unqualifiedAmbiguousLocked(id) {
			id = s.QualifiedID()
		}
		line := fmt.Sprintf("- %s: %s\n", id, firstSentence(s.Meta.Description))
		if b.Len()+len(line) > maxBytes {
			omitted++
			continue
		}
		b.WriteString(line)
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "… and %d more (catalog size budget reached)\n", omitted)
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m *Manager) unqualifiedAmbiguousLocked(id string) bool {
	n := 0
	for _, s := range m.skills {
		if s.Meta.ID == id {
			n++
		}
	}
	return n > 1
}

// firstSentence keeps catalog entries to one sentence.
func firstSentence(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if idx := strings.Index(s, ". "); idx >= 0 {
		return s[:idx+1]
	}
	return s
}

// RequiresToolCalling reports whether the skill declares tool calling as a
// hard requirement (capabilities: tool_calling: required).
func RequiresToolCalling(s Skill) bool {
	return strings.EqualFold(strings.TrimSpace(s.Meta.Capabilities["tool_calling"]), "required")
}
