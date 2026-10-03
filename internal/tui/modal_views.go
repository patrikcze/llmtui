package tui

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
	"github.com/patrikcze/llmtui/internal/tools"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

// pickerModalTitle reports whether the open picker is drawn as a dialog, and
// its title and key hints.
func (m *Model) pickerModalTitle() (title, hint string, ok bool) {
	switch m.picker.pickerKind {
	case pickerProvider:
		hint := "↑/↓ select · enter switch · esc close"
		if m.cfg.UI.ProviderProbe {
			hint = "↑/↓ select · enter switch · r recheck · esc close"
		}
		return "Providers", hint, true
	case pickerModel:
		name := "provider"
		if m.prov != nil {
			name = terminaltext.Sanitize(m.prov.Name())
		}
		return "Models · " + name, "↑/↓ select · enter switch · esc close", true
	case pickerProfile:
		return "Model profiles", "↑/↓ select · enter pin · click a row to pin · esc close", true
	case pickerSkill:
		return "Skills", "↑/↓ select · enter activate/deactivate (session) · esc close", true
	case pickerPlugin:
		return "Plugins", "↑/↓ select · enter enable/disable · esc close", true
	case pickerHistory:
		return "Saved sessions", "↑/↓ select · enter load · esc close", true
	case pickerTemplate:
		return "Templates", "↑/↓ select · enter use / clear · esc close", true
	case pickerPersonalApps:
		return "Personal apps", "↑/↓ select · enter connect/disconnect · esc close", true
	case pickerEntity:
		return "Entities", "↑/↓ select · enter inspect · esc close", true
	}
	return "", "", false
}

// --- provider checks -----------------------------------------------------

const providerProbeConcurrency = 2

// providerProbeTimeout bounds each provider check (a var so tests can
// shorten it).
var providerProbeTimeout = 3 * time.Second

type providerProbeStatus int

const (
	probeUnknown providerProbeStatus = iota
	probePending
	probeOK
	probeFailed
)

// providerProbeResult is what the /providers dialog knows about one
// configured provider. It is display-only state, rebuilt on every open.
type providerProbeResult struct {
	status providerProbeStatus
	detail string // sanitized, bounded failure reason
	models []provider.ModelInfo
}

// providerProbeState holds the dialog's per-provider checks. gen guards
// against results from an earlier open or refresh.
type providerProbeState struct {
	gen     int
	results map[string]providerProbeResult
}

type providerProbeMsg struct {
	gen    int
	name   string
	result providerProbeResult
}

// startProviderProbes checks every configured provider in the background:
// a health check, then its model list. Only configured endpoints are
// contacted; embedded providers are stat-only and never load a model.
func (m *Model) startProviderProbes() tea.Cmd {
	m.probes.gen++
	m.probes.results = map[string]providerProbeResult{}
	if !m.cfg.UI.ProviderProbe {
		return nil
	}
	gen := m.probes.gen
	sem := make(chan struct{}, providerProbeConcurrency)
	var cmds []tea.Cmd
	for name, pc := range m.cfg.Providers {
		m.probes.results[name] = providerProbeResult{status: probePending}
		var active provider.Provider
		if m.prov != nil && m.prov.Name() == name {
			active = m.prov
		}
		netCfg := m.cfg.Network
		cmds = append(cmds, func() tea.Msg {
			sem <- struct{}{}
			defer func() { <-sem }()
			prov := active
			if prov == nil {
				built, err := app.BuildProvider(name, pc, netCfg)
				if err != nil {
					return providerProbeMsg{gen: gen, name: name, result: providerProbeResult{status: probeFailed, detail: probeDetail(err)}}
				}
				defer func() { _ = provider.CloseProvider(built) }()
				prov = built
			}
			ctx, cancel := context.WithTimeout(context.Background(), providerProbeTimeout)
			defer cancel()
			if err := prov.HealthCheck(ctx); err != nil {
				return providerProbeMsg{gen: gen, name: name, result: providerProbeResult{status: probeFailed, detail: probeDetail(err)}}
			}
			models, err := prov.ListModels(ctx)
			if err != nil {
				return providerProbeMsg{gen: gen, name: name, result: providerProbeResult{status: probeOK, detail: probeDetail(err)}}
			}
			return providerProbeMsg{gen: gen, name: name, result: providerProbeResult{status: probeOK, models: models}}
		})
	}
	return tea.Batch(cmds...)
}

func probeDetail(err error) string {
	text := terminaltext.Sanitize(strings.TrimSpace(err.Error()))
	if len([]rune(text)) > 160 {
		text = string([]rune(text)[:160]) + "…"
	}
	return text
}

// handleProviderProbe records one check result and redraws the dialog if it
// is still the one the result belongs to.
func (m *Model) handleProviderProbe(msg providerProbeMsg) {
	if msg.gen != m.probes.gen || m.probes.results == nil {
		return
	}
	m.probes.results[msg.name] = msg.result
	if m.overlayOpen && m.picker.pickerKind == pickerProvider {
		m.renderPicker()
	}
}

// --- /providers dialog ---------------------------------------------------

// providersOverlay renders the provider list (left) and the highlighted
// provider's details and models (right). Row i of the list is line i+2 of
// the body (pickerHeaderLines[pickerProvider]).
func (m *Model) providersOverlay() string {
	width := m.overlayWidth()
	names := make([]string, 0, len(m.cfg.Providers))
	for name := range m.cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	leftW := min(max(width*2/5, 34), width)
	twoPane := width >= 76
	if !twoPane {
		leftW = width
	}
	columns := []components.Column{{Width: 2}, {Width: 0}, {Width: 12, Right: true}}
	faint := lipgloss.NewStyle().Foreground(m.theme.Faint)

	left := []string{
		faint.Render(components.TableRow(columns, []string{"", "provider", "status"}, leftW)),
		lipgloss.NewStyle().Foreground(m.theme.PanelEdge).Render(strings.Repeat("─", leftW)),
	}
	for i, name := range names {
		selected := m.picker.pickerKind == pickerProvider && i == m.picker.pickerIdx
		dot, meta := m.providerStatus(name)
		label := m.theme.StatusValue.Render(terminaltext.Sanitize(name))
		if m.prov != nil && m.prov.Name() == name {
			label += m.theme.BadgeOK.Render(" ●")
		}
		if selected {
			label = m.theme.TabActive.Render(" " + terminaltext.Sanitize(name) + " ")
		}
		row := components.TableRow(columns, []string{dot, label, meta}, leftW)
		left = append(left, zone.Mark(pickerRowZoneID(i), row))
	}
	if !twoPane {
		if m.picker.pickerIdx >= 0 && m.picker.pickerIdx < len(names) {
			left = append(append(left, ""), m.providerDetailLines(names[m.picker.pickerIdx], width)...)
		}
		return strings.Join(left, "\n")
	}
	right := []string{}
	if m.picker.pickerIdx >= 0 && m.picker.pickerIdx < len(names) {
		right = m.providerDetailLines(names[m.picker.pickerIdx], width-leftW-3)
	}
	sep := lipgloss.NewStyle().Foreground(m.theme.PanelEdge).Render("│")
	rows := max(len(left), len(right))
	out := make([]string, rows)
	for i := range rows {
		l, r := "", ""
		if i < len(left) {
			l = left[i]
		}
		if i < len(right) {
			r = right[i]
		}
		out[i] = components.FitWidth(l, leftW) + " " + sep + " " + r
	}
	return strings.Join(out, "\n")
}

// providerStatus returns the status dot and short status text for a row.
func (m *Model) providerStatus(name string) (dot, meta string) {
	good := lipgloss.NewStyle().Foreground(m.theme.Good)
	bad := lipgloss.NewStyle().Foreground(m.theme.Bad)
	faint := lipgloss.NewStyle().Foreground(m.theme.Faint)
	result := m.probes.results[name]
	switch result.status {
	case probePending:
		return faint.Render("◌"), faint.Render("checking…")
	case probeOK:
		if result.models != nil {
			return good.Render("●"), m.theme.StatusBar.Render(fmt.Sprintf("%d models", len(result.models)))
		}
		return good.Render("●"), m.theme.StatusBar.Render("online")
	case probeFailed:
		return bad.Render("○"), bad.Render("offline")
	}
	return faint.Render("·"), faint.Render(m.cfg.Providers[name].Type)
}

// providerDetailLines describes one provider: connection facts and up to a
// screenful of its models. It never shows API keys or env values, and strips
// credentials from the base URL.
func (m *Model) providerDetailLines(name string, width int) []string {
	pc := m.cfg.Providers[name]
	key := lipgloss.NewStyle().Foreground(m.theme.Faint)
	kv := func(k, v string) string {
		return components.FitWidth(key.Render(fmt.Sprintf("%-9s", k))+" "+m.theme.StatusValue.Render(v), width)
	}
	lines := []string{components.SectionTitle(m.theme, terminaltext.Sanitize(name), width)}
	lines = append(lines, kv("type", terminaltext.Sanitize(pc.Type)))
	if endpoint := displayEndpoint(pc.BaseURL); endpoint != "" {
		lines = append(lines, kv("endpoint", endpoint))
	}
	if pc.DefaultModel != "" {
		lines = append(lines, kv("model", terminaltext.Sanitize(pc.DefaultModel)))
	}
	if m.prov != nil && m.prov.Name() == name {
		lines = append(lines, kv("active", m.theme.BadgeOK.Render("yes")+m.theme.StatusBar.Render(" · "+terminaltext.Sanitize(m.model))))
	}
	result := m.probes.results[name]
	switch {
	case !m.cfg.UI.ProviderProbe:
		lines = append(lines, kv("status", "not checked (ui.provider_probe: false)"))
	case result.status == probePending:
		lines = append(lines, kv("status", "checking…"))
	case result.status == probeFailed:
		lines = append(lines, kv("status", m.theme.ErrorText.Render(result.detail)))
	case result.status == probeOK && result.detail != "":
		lines = append(lines, kv("status", "online · models unavailable: "+result.detail))
	}
	if len(result.models) > 0 {
		lines = append(lines, "", components.SectionTitle(m.theme, fmt.Sprintf("models (%d)", len(result.models)), width))
		limit := min(len(result.models), max(m.modal.innerH-len(lines)-2, 3))
		for _, mi := range result.models[:limit] {
			lines = append(lines, components.FitWidth(m.modelCells(mi, name, width), width))
		}
		if rest := len(result.models) - limit; rest > 0 {
			lines = append(lines, key.Render(fmt.Sprintf("… %d more · /models after switching", rest)))
		}
	}
	return lines
}

// modelCells renders one model line: id, context, capability tags, and an
// active marker.
func (m *Model) modelCells(mi provider.ModelInfo, providerName string, width int) string {
	columns := []components.Column{{Width: 0}, {Width: 6, Right: true}, {Width: 9}, {Width: 2}}
	active := ""
	if m.prov != nil && m.prov.Name() == providerName && mi.ID == m.model {
		active = m.theme.BadgeOK.Render("✓")
	}
	return components.TableRow(columns, []string{
		m.theme.StatusValue.Render(terminaltext.Sanitize(mi.ID)),
		lipgloss.NewStyle().Foreground(m.theme.Subtle).Render(contextLabel(mi.ContextLen)),
		m.modelTags(mi),
		active,
	}, width)
}

func (m *Model) modelTags(mi provider.ModelInfo) string {
	id := strings.ToLower(mi.ID)
	switch {
	case strings.Contains(id, "embed"):
		return lipgloss.NewStyle().Foreground(m.theme.Subtle).Render("embedding")
	case mi.Vision != nil && *mi.Vision, mi.Vision == nil && provider.SupportsVision(mi.ID):
		return lipgloss.NewStyle().Foreground(m.theme.Accent).Render("vision")
	}
	return ""
}

func contextLabel(n int) string {
	if n <= 0 {
		return ""
	}
	return components.FormatTokens(n)
}

// displayEndpoint shows a base URL without any user:password component.
func displayEndpoint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return terminaltext.Sanitize(raw)
	}
	u.User = nil
	return terminaltext.Sanitize(u.String())
}

// --- /models dialog ------------------------------------------------------

// modelsOverlay renders the active provider's models as a table. Row i is
// line i+2 of the body (pickerHeaderLines[pickerModel]).
func (m *Model) modelsOverlay(models []provider.ModelInfo) string {
	width := m.overlayWidth()
	faint := lipgloss.NewStyle().Foreground(m.theme.Faint)
	columns := []components.Column{{Width: 2}, {Width: 0}, {Width: 6, Right: true}, {Width: 9}, {Width: 2}}
	lines := []string{
		faint.Render(components.TableRow(columns, []string{"", "model", "ctx", "", ""}, width)),
		lipgloss.NewStyle().Foreground(m.theme.PanelEdge).Render(strings.Repeat("─", width)),
	}
	if len(models) == 0 {
		lines = append(lines, m.theme.SystemNote.Render("no models found"))
	}
	for i, mi := range models {
		id := terminaltext.Sanitize(mi.ID)
		label := m.theme.StatusValue.Render(id)
		marker := ""
		if m.picker.pickerKind == pickerModel && i == m.picker.pickerIdx {
			label = m.theme.TabActive.Render(" " + id + " ")
			marker = m.theme.BadgeOK.Render("▸")
		}
		active := ""
		if mi.ID == m.model {
			active = m.theme.BadgeOK.Render("✓")
		}
		row := components.TableRow(columns, []string{
			marker, label,
			lipgloss.NewStyle().Foreground(m.theme.Subtle).Render(contextLabel(mi.ContextLen)),
			m.modelTags(mi), active,
		}, width)
		lines = append(lines, zone.Mark(pickerRowZoneID(i), row))
	}
	if !m.modalActive() {
		lines = append(lines, "", m.theme.SystemNote.Render("↑/↓ select · enter switch · esc cancel"))
	}
	return strings.Join(lines, "\n")
}

// --- /profile list dialog ------------------------------------------------

// profileListOverlay renders the model profiles as a table. Row i is line
// i+2 of the body (pickerHeaderLines[pickerProfile]).
func (m *Model) profileListOverlay() string {
	width := m.overlayWidth()
	faint := lipgloss.NewStyle().Foreground(m.theme.Faint)
	subtle := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	columns := []components.Column{{Width: 2}, {Width: 14}, {Width: 7, Right: true}, {Width: 5, Right: true}, {Width: 16}, {Width: 0}}
	lines := []string{
		faint.Render(components.TableRow(columns, []string{"", "profile", "ctx", "temp", "style", "matches"}, width)),
		lipgloss.NewStyle().Foreground(m.theme.PanelEdge).Render(strings.Repeat("─", width)),
	}
	for i, p := range m.profiles {
		name := m.theme.StatusValue.Render(p.Name)
		marker := ""
		if m.picker.pickerKind == pickerProfile && i == m.picker.pickerIdx {
			name = m.theme.TabActive.Render(" " + p.Name + " ")
			marker = m.theme.BadgeOK.Render("▸")
		}
		row := components.TableRow(columns, []string{
			marker, name,
			subtle.Render(components.FormatTokens(p.ContextWindow)),
			subtle.Render(fmt.Sprintf("%.2f", p.PreferredTemperature)),
			subtle.Render(string(p.PromptStyle)),
			faint.Render(strings.Join(p.Match, ", ")),
		}, width)
		// zone.Mark is the outermost wrap: nothing downstream may re-sanitize
		// this string or the marker escape sequence is lost.
		lines = append(lines, zone.Mark(pickerRowZoneID(i), row))
	}
	lines = append(lines, "", m.theme.SystemNote.Render("custom profiles come from model_profiles in the config"))
	if !m.modalActive() {
		lines = append(lines, m.theme.SystemNote.Render("↑/↓ select · enter pin · esc cancel · click a row to pin it"))
	}
	return strings.Join(lines, "\n")
}

// --- /tools dialog -------------------------------------------------------

// toolAccess classifies a built-in tool's default approval for the /tools
// table.
type toolRow struct {
	name, access, desc string
}

// toolsOverlay renders the workspace tool settings and the built-in tools.
func (m *Model) toolsOverlay() string {
	width := m.overlayWidth()
	approval := "ask (y/n before writes & commands)"
	if m.toolsAutoApprove {
		approval = "auto (no confirmation)"
	} else if n := m.approvalPolicy.Active(time.Now()); n > 0 {
		approval = fmt.Sprintf("ask + %d scoped grant(s), each expiring within 15 min", n)
	}
	protocol := "prompt-based (fenced blocks)"
	if m.toolsNative {
		protocol = "native function calling (auto-falls back if unsupported)"
	}
	output := "compact one-line summaries (/tools output for full text)"
	if m.toolsShowOutput {
		output = "full (/tools output to collapse)"
	}
	key := lipgloss.NewStyle().Foreground(m.theme.Faint)
	onOffBadge := func(on bool) string {
		if on {
			return m.theme.BadgeOK.Render("on")
		}
		return m.theme.BadgeErr.Render("off")
	}
	settings := []struct{ k, v string }{
		{"enabled", onOffBadge(m.toolsOn)},
		{"web", onOffBadge(m.webOn)},
		{"approval", m.theme.StatusValue.Render(approval)},
		{"protocol", m.theme.StatusValue.Render(protocol)},
		{"output", m.theme.StatusValue.Render(output)},
		{"workspace", m.theme.StatusValue.Render(terminaltext.Sanitize(m.toolRunner.Root()))},
		{"max rounds/turn", m.theme.StatusValue.Render(fmt.Sprintf("%d", m.cfg.Tools.MaxIterations))},
		{"file/output cap", m.theme.StatusValue.Render(fmt.Sprintf("%d KB", m.cfg.Tools.MaxFileKB))},
		{"command timeout", m.theme.StatusValue.Render(m.toolRunner.CommandTimeout.String())},
	}
	kvColumns := []components.Column{{Width: 17}, {Width: 0}}
	lines := []string{components.SectionTitle(m.theme, "settings", width)}
	for _, s := range settings {
		lines = append(lines, components.TableRow(kvColumns, []string{key.Render(s.k), s.v}, width))
	}

	rows := []toolRow{
		{tools.ToolListDir, "auto", "list a directory in the workspace"},
		{tools.ToolReadFile, "auto", "read a file's contents"},
		{tools.ToolGlob, "auto", "find workspace files by glob pattern"},
		{tools.ToolGrep, "auto", "search workspace contents with a regular expression (secret files skipped)"},
		{tools.ToolWriteFile, "approval", "create or overwrite a file"},
		{tools.ToolEditFile, "approval", "replace one exact unique text fragment in an existing file"},
		{tools.ToolRunCommand, "mixed", "run one shell command; read-only ones (ls, grep, git status, …) run automatically"},
		{tools.ToolAskUser, "user", "ask a clarification with choices or text input (chat and agent)"},
		{tools.ToolWebSearch, "auto", "search the web via DuckDuckGo (/web on)"},
		{tools.ToolWebFetch, "approval", "fetch one page as Markdown (approval per URL)"},
	}
	accessStyle := map[string]lipgloss.Style{
		"auto":     lipgloss.NewStyle().Foreground(m.theme.Good),
		"approval": lipgloss.NewStyle().Foreground(m.theme.Warning),
		"mixed":    lipgloss.NewStyle().Foreground(m.theme.Accent),
		"user":     lipgloss.NewStyle().Foreground(m.theme.Subtle),
	}
	toolColumns := []components.Column{{Width: 13}, {Width: 9}, {Width: 0}}
	lines = append(lines, "", components.SectionTitle(m.theme, "available tools", width),
		key.Render(components.TableRow(toolColumns, []string{"tool", "access", "description"}, width)))
	for _, r := range rows {
		lines = append(lines, components.TableRow(toolColumns, []string{
			m.theme.StatusValue.Render(r.name), accessStyle[r.access].Render(r.access), m.theme.StatusBar.Render(r.desc),
		}, width))
	}
	lines = append(lines, "", components.SectionTitle(m.theme, "safety", width))
	for _, note := range []string{
		"everything is confined to the workspace: absolute paths, \"..\" and symlink escapes are rejected",
		"writes into .git, key-material dirs, and shell startup files are blocked",
		"reads of likely secret files (.env, *.pem, id_rsa) ask first",
		"command environments are stripped of secrets",
		"every action is shown in the chat before and after (see /tools check <cmd>)",
	} {
		lines = append(lines, m.theme.SystemNote.Render(components.FitWidth("· "+note, width)))
	}
	if !m.toolsOn {
		lines = append(lines, "", m.theme.BadgeWarn.Render("enable with /tools on (or tools.enabled in config)"))
	}
	body := strings.Join(lines, "\n") + "\n"
	if m.modalActive() {
		return body
	}
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("workspace tools") + "\n\n" + body)
	return m.overlayFooter(&b)
}
