package tui

import (
	"fmt"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	zone "github.com/lrstanley/bubblezone/v2"

	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/personalapps"
	"github.com/patrikcze/llmtui/internal/skill"
	"github.com/patrikcze/llmtui/internal/terminaltext"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

// Dialog pickers for /skills list, /plugins, /history, /template list,
// /personal-apps and /entities list. Each shares the picker machinery
// (pickerItems/pickerIdx, updatePicker, renderPicker); row i is line i+2 of
// the body, after the column header and its rule (pickerHeaderLines).

// tableHeader renders a column-header line and the rule under it.
func (m *Model) tableHeader(columns []components.Column, labels []string, width int) []string {
	return []string{
		lipgloss.NewStyle().Foreground(m.theme.Faint).Render(components.TableRow(columns, labels, width)),
		lipgloss.NewStyle().Foreground(m.theme.PanelEdge).Render(strings.Repeat("─", width)),
	}
}

// pickerRow renders one selectable row: marker, then cells. The selected
// row gets a ▸ marker and its label (cells[0]) as a pill.
func (m *Model) pickerRow(i int, columns []components.Column, label string, cells []string, width int) string {
	marker := ""
	labelText := m.theme.StatusValue.Render(label)
	if i == m.picker.pickerIdx {
		marker = m.theme.BadgeOK.Render("▸")
		labelText = m.theme.TabActive.Render(" " + label + " ")
	}
	row := components.TableRow(columns, append([]string{marker, labelText}, cells...), width)
	// zone.Mark is the outermost wrap: nothing downstream may re-sanitize
	// this string or the marker escape sequence is lost.
	return zone.Mark(pickerRowZoneID(i), row)
}

func (m *Model) emptyNote(text string) string {
	return m.theme.SystemNote.Render(text)
}

// --- /skills list --------------------------------------------------------

func (m *Model) skillsPickerOverlay() string {
	width := m.overlayWidth()
	skills := m.skillMgr.Skills()
	if len(skills) == 0 {
		return m.emptyNote("no skills found — add one under a search path (/skills paths) or enable a plugin")
	}
	columns := []components.Column{{Width: 2}, {Width: 24}, {Width: 8}, {Width: 18}, {Width: 8}, {Width: 0}}
	lines := m.tableHeader(columns, []string{"", "skill", "version", "source", "active", "description"}, width)
	subtle := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	for i, s := range skills {
		scope, isActive := m.skillMgr.IsActive(s.QualifiedID())
		active := subtle.Render("–")
		if isActive {
			active = m.theme.BadgeOK.Render(string(scope))
		}
		source := string(s.Source)
		if s.Source == skill.SourcePlugin {
			source = "plugin:" + s.PluginID
		}
		lines = append(lines, m.pickerRow(i, columns, terminaltext.Sanitize(s.Meta.ID), []string{
			subtle.Render(orNone(s.Meta.Version)),
			subtle.Render(terminaltext.Sanitize(source)),
			active,
			m.theme.StatusBar.Render(terminaltext.Sanitize(s.Meta.Description)),
		}, width))
	}
	if !m.modalActive() {
		lines = append(lines, "", m.theme.SystemNote.Render("↑/↓ select · enter activate/deactivate (session) · esc cancel"))
	}
	return strings.Join(lines, "\n")
}

// --- /plugins ------------------------------------------------------------

func (m *Model) pluginsPickerOverlay() string {
	width := m.overlayWidth()
	plugins := m.skillMgr.Plugins()
	if len(plugins) == 0 {
		return m.emptyNote("no plugins found — put one at <plugin path>/<id>/plugin.yaml (/plugins paths)")
	}
	columns := []components.Column{{Width: 2}, {Width: 20}, {Width: 8}, {Width: 10}, {Width: 9}, {Width: 0}}
	lines := m.tableHeader(columns, []string{"", "plugin", "version", "source", "state", "description"}, width)
	subtle := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	for i, p := range plugins {
		state := subtle.Render("disabled")
		desc := m.theme.StatusBar.Render(terminaltext.Sanitize(p.Manifest.Description))
		switch {
		case p.Err != nil:
			state = m.theme.BadgeWarn.Render("invalid")
			desc = m.theme.ErrorText.Render(terminaltext.Sanitize(p.Err.Error()))
		case p.Enabled:
			state = m.theme.BadgeOK.Render("enabled")
		}
		lines = append(lines, m.pickerRow(i, columns, terminaltext.Sanitize(p.Manifest.ID), []string{
			subtle.Render(orNone(p.Manifest.Version)), subtle.Render(string(p.Source)), state, desc,
		}, width))
	}
	lines = append(lines, "", m.theme.StatusBar.Render(components.FitWidth(
		"enabling a plugin registers its skills and nothing else: no skill is activated, no code runs, no MCP server starts", width)))
	if !m.modalActive() {
		lines = append(lines, m.theme.SystemNote.Render("↑/↓ select · enter enable/disable · esc cancel"))
	}
	return strings.Join(lines, "\n")
}

// --- /history ------------------------------------------------------------

// openHistoryPicker lists saved sessions; Enter loads the highlighted one,
// exactly like /history load <name>.
func (m *Model) openHistoryPicker() {
	m.picker.pickerKind = pickerHistory
	m.picker.pickerItems = []string{}
	m.picker.historyMetas = nil
	if m.historyDir != "" {
		if metas, err := history.List(m.historyDir); err == nil {
			m.picker.historyMetas = metas
			for _, meta := range metas {
				m.picker.pickerItems = append(m.picker.pickerItems, meta.Name)
			}
		} else {
			m.picker.historyErr = err.Error()
		}
	}
	m.picker.pickerIdx = selectedIndex(m.picker.pickerItems, m.sessionName)
	m.overlayOpen = true
	m.renderPicker()
}

func (m *Model) historyPickerOverlay() string {
	width := m.overlayWidth()
	if m.historyDir == "" {
		return m.emptyNote("history saving is disabled (chat.save_history)")
	}
	if m.picker.historyErr != "" {
		return m.theme.ErrorText.Render(terminaltext.Sanitize(m.picker.historyErr))
	}
	if len(m.picker.historyMetas) == 0 {
		return m.emptyNote("no saved sessions yet — /save or ctrl+s")
	}
	// The provider/model column is dropped on narrow dialogs so the session
	// name keeps its room.
	wide := width >= 96
	columns := []components.Column{{Width: 2}, {Width: 0}, {Width: 16}, {Width: 6, Right: true}, {Width: 8, Right: true}}
	labels := []string{"", "session", "saved", "msgs", "tokens"}
	if wide {
		columns = []components.Column{{Width: 2}, {Width: 0}, {Width: 16}, {Width: 26}, {Width: 6, Right: true}, {Width: 8, Right: true}}
		labels = []string{"", "session", "saved", "provider/model", "msgs", "tokens"}
	}
	lines := m.tableHeader(columns, labels, width)
	subtle := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	for i, meta := range m.picker.historyMetas {
		name := terminaltext.Sanitize(meta.Name)
		if meta.Name == m.sessionName {
			name += " ●"
		}
		cells := []string{subtle.Render(meta.SavedAt.Format("2006-01-02 15:04"))}
		if wide {
			cells = append(cells, subtle.Render(terminaltext.Sanitize(meta.Provider+"/"+meta.Model)))
		}
		cells = append(cells, subtle.Render(fmt.Sprint(meta.Messages)), subtle.Render(components.FormatTokens(meta.Tokens)))
		lines = append(lines, m.pickerRow(i, columns, name, cells, width))
	}
	lines = append(lines, "", m.theme.SystemNote.Render(components.FitWidth("stored in "+m.historyDir+" · /history search <q> · /history export md|json", width)))
	if !m.modalActive() {
		lines = append(lines, m.theme.SystemNote.Render("↑/↓ select · enter load · esc close"))
	}
	return strings.Join(lines, "\n")
}

// loadHistorySession loads a saved session (the /history load path).
func (m *Model) loadHistorySession(name string) tea.Cmd {
	if m.thinking {
		return m.fail("/history load is unavailable while a reply is streaming — esc to stop it first")
	}
	s, err := history.Load(m.historyDir, name)
	if err != nil {
		return m.fail(err.Error())
	}
	m.adoptSession(name, s)
	m.refreshViewport()
	m.notice = fmt.Sprintf("loaded %s (%d messages, %s/%s)", name, len(s.Messages), s.Provider, s.Model)
	return nil
}

// --- /template list ------------------------------------------------------

// openTemplatePicker lists templates; Enter uses the highlighted one, or
// clears it when it is already active.
func (m *Model) openTemplatePicker() {
	m.picker.pickerKind = pickerTemplate
	m.picker.pickerItems = make([]string, 0, len(m.cfg.Templates))
	for name := range m.cfg.Templates {
		m.picker.pickerItems = append(m.picker.pickerItems, name)
	}
	sort.Strings(m.picker.pickerItems)
	m.picker.pickerIdx = selectedIndex(m.picker.pickerItems, m.template)
	m.overlayOpen = true
	m.renderPicker()
}

func (m *Model) templatePickerOverlay() string {
	width := m.overlayWidth()
	if len(m.picker.pickerItems) == 0 {
		return m.emptyNote("no templates configured — add a templates: section to the config")
	}
	columns := []components.Column{{Width: 2}, {Width: 16}, {Width: 14}, {Width: 5, Right: true}, {Width: 2}, {Width: 0}}
	lines := m.tableHeader(columns, []string{"", "template", "mode", "temp", "", "description"}, width)
	subtle := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	for i, name := range m.picker.pickerItems {
		t := m.cfg.Templates[name]
		active := ""
		if name == m.template {
			active = m.theme.BadgeOK.Render("✓")
		}
		lines = append(lines, m.pickerRow(i, columns, terminaltext.Sanitize(name), []string{
			subtle.Render(orNone(t.PromptMode)),
			subtle.Render(fmt.Sprintf("%.2f", t.Temperature)),
			active,
			m.theme.StatusBar.Render(terminaltext.Sanitize(t.Description)),
		}, width))
	}
	lines = append(lines, "", m.theme.SystemNote.Render("enter on the active template (✓) clears it · /template inspect <name>"))
	if !m.modalActive() {
		lines = append(lines, m.theme.SystemNote.Render("↑/↓ select · enter use · esc close"))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) toggleTemplate(name string) {
	if name == m.template {
		m.template = ""
		m.notice = "template cleared"
		return
	}
	m.template = name
	m.notice = "template set to " + name
}

// --- /personal-apps ------------------------------------------------------

// openPersonalAppsPicker shows Mail and Calendar with their configuration
// and connection state. Enter connects or disconnects the highlighted app:
// the same human decision /personal-apps connect|disconnect records, made
// by the person at the keyboard, never the model.
func (m *Model) openPersonalAppsPicker() {
	m.picker.pickerKind = pickerPersonalApps
	m.picker.pickerItems = []string{string(personalapps.AdapterMail), string(personalapps.AdapterCalendar)}
	if m.picker.pickerIdx >= len(m.picker.pickerItems) {
		m.picker.pickerIdx = 0
	}
	m.overlayOpen = true
	m.renderPicker()
}

func (m *Model) personalAppsPickerOverlay() string {
	width := m.overlayWidth()
	status := m.personalApps.Status()
	scope := m.personalApps.Scope()
	cfg := m.cfg.PersonalApps
	columns := []components.Column{{Width: 2}, {Width: 10}, {Width: 10}, {Width: 15}, {Width: 0}}
	lines := m.tableHeader(columns, []string{"", "app", "config", "connection", "scope"}, width)
	good, bad, subtle := m.theme.BadgeOK, m.theme.ErrorText, lipgloss.NewStyle().Foreground(m.theme.Subtle)
	state := func(enabled, connected bool) (string, string) {
		cfgText := subtle.Render("disabled")
		if enabled {
			cfgText = good.Render("enabled")
		}
		conn := subtle.Render("–")
		switch {
		case connected:
			conn = good.Render("connected")
		case enabled:
			conn = m.theme.BadgeWarn.Render("not connected")
		}
		return cfgText, conn
	}
	mailCfg, mailConn := state(status.MailEnabled, status.MailConnected)
	lines = append(lines, m.pickerRow(0, columns, "Mail", []string{mailCfg, mailConn,
		subtle.Render(fmt.Sprintf("%d allowed account(s)", len(scope.AllowedAccounts)))}, width))
	calCfg, calConn := state(status.CalendarEnabled, status.CalendarConnected)
	calScope := fmt.Sprintf("%d allowed calendar(s)", len(scope.AllowedCalendars))
	if status.CalendarEnabled {
		// Stat-only check of the configured helper; it never launches it or
		// requests permission (same check as /doctor).
		if helper := personalapps.CheckCalendarHelper(cfg.Calendar.HelperPath); !helper.Ready {
			calScope += " · " + bad.Render(terminaltext.Sanitize(helper.Message))
		}
	}
	lines = append(lines, m.pickerRow(1, columns, "Calendar", []string{calCfg, calConn, subtle.Render(calScope)}, width))

	lines = append(lines, "", components.SectionTitle(m.theme, "session", width))
	kv := func(k, v string) string {
		return components.TableRow([]components.Column{{Width: 16}, {Width: 0}},
			[]string{lipgloss.NewStyle().Foreground(m.theme.Faint).Render(k), v}, width)
	}
	lines = append(lines,
		kv("mutations", enabledWord(status.MutationsEnabled)),
		kv("private session", enabledWord(status.PrivateSession)),
		kv("platform", map[bool]string{true: "supported", false: "not supported on this build"}[status.PlatformSupported]),
	)
	for _, note := range status.Notes {
		lines = append(lines, m.theme.SystemNote.Render(components.FitWidth("· "+terminaltext.Sanitize(note), width)))
	}
	lines = append(lines, "", m.theme.SystemNote.Render(components.FitWidth(
		"scope comes from personal_apps in the config; connecting records your decision to use it and grants nothing more", width)))
	if !m.modalActive() {
		lines = append(lines, m.theme.SystemNote.Render("↑/↓ select · enter connect/disconnect · esc close"))
	}
	return strings.Join(lines, "\n")
}

// togglePersonalApp connects or disconnects one adapter, then reopens the
// dialog so the new state is visible.
func (m *Model) togglePersonalApp(name string) tea.Cmd {
	adapter := personalapps.Adapter(name)
	status := m.personalApps.Status()
	enabled, connected := status.MailEnabled, status.MailConnected
	if adapter == personalapps.AdapterCalendar {
		enabled, connected = status.CalendarEnabled, status.CalendarConnected
	}
	var cmd tea.Cmd
	switch {
	case !enabled:
		cmd = m.fail(fmt.Sprintf("%s is disabled — set personal_apps.%s.enabled: true in the config first", name, name))
	case connected:
		cmd = personalAppsDisconnect(m, name)
	default:
		cmd = personalAppsConnect(m, name)
	}
	m.openPersonalAppsPicker()
	return cmd
}

// --- /entities list ------------------------------------------------------

// openEntitiesPicker lists live entities; Enter opens the highlighted one's
// details (the /entities inspect view).
func (m *Model) openEntitiesPicker() {
	m.picker.pickerKind = pickerEntity
	m.picker.pickerItems = []string{}
	m.picker.entityViews = nil
	if m.entitiesEnabled() {
		m.picker.entityViews = m.entities.MinimalViews(16 * 1024)
		for _, view := range m.picker.entityViews {
			m.picker.pickerItems = append(m.picker.pickerItems, view.ID.String())
		}
	}
	m.picker.pickerIdx = 0
	m.overlayOpen = true
	m.renderPicker()
}

func (m *Model) entitiesPickerOverlay() string {
	width := m.overlayWidth()
	if !m.entitiesEnabled() {
		return m.emptyNote("entity context runtime is disabled")
	}
	if len(m.picker.entityViews) == 0 {
		return m.emptyNote("no live entities — tool results, reads and searches create them")
	}
	columns := []components.Column{{Width: 2}, {Width: 12}, {Width: 18}, {Width: 0}, {Width: 22}}
	lines := m.tableHeader(columns, []string{"", "id", "kind", "label", "source"}, width)
	subtle := lipgloss.NewStyle().Foreground(m.theme.Subtle)
	for i, view := range m.picker.entityViews {
		lines = append(lines, m.pickerRow(i, columns, view.ID.String(), []string{
			lipgloss.NewStyle().Foreground(m.theme.Accent).Render(string(view.Kind)),
			m.theme.StatusValue.Render(terminaltext.Sanitize(view.Label)),
			subtle.Render(terminaltext.Sanitize(view.Source)),
		}, width))
	}
	if !m.modalActive() {
		lines = append(lines, "", m.theme.SystemNote.Render("↑/↓ select · enter inspect · esc close"))
	}
	return strings.Join(lines, "\n")
}

func (m *Model) inspectEntity(id string) {
	m.openModalOverlay("Entity "+id, "↑/↓ scroll · esc close", func() string { return m.entityInspectOverlay(id) })
}
