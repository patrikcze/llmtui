package tui

import (
	"fmt"
	"sort"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// maxSuggestionRows is how many popup rows show at once; ↑/↓ scroll the
// window through the rest of the matches.
const maxSuggestionRows = 6

// pinnedCommands lead the popup, in this order, ahead of registry order:
// the commands reached for most often. An exact name match still comes first.
var pinnedCommands = []string{"providers", "models", "skills", "plugins", "mcp", "help"}

// suggestState is the slash-command autocomplete dropdown. sugs holds command
// names while the name itself is being typed; args holds argument phrases once
// a known command and a space are in the box. At most one is non-empty. Every
// field's zero value is the correct "no popup" state.
type suggestState struct {
	sugs []slashCommand
	args []argSuggestion
	// sugIdx is the highlighted row and sugTop the first visible one.
	sugIdx, sugTop int
	// navigated records that ↑/↓ moved the highlight in the argument popup
	// since the last edit: only then does Enter accept the highlighted
	// phrase instead of running exactly what was typed.
	navigated bool
	// value is the composer text the lists were computed for.
	value string
}

// argSuggestion is one argument completion for a typed command.
type argSuggestion struct {
	label    string // shown in the popup, e.g. "/mcp inspect <server>"
	insert   string // composer text after accepting, e.g. "/mcp inspect "
	takesArg bool   // a required <placeholder> still has to be typed
}

// count is the number of popup rows available to scroll through.
func (s suggestState) count() int {
	return len(s.sugs) + len(s.args)
}

// updateSuggestions recomputes the command popup from the current input.
func (m *Model) updateSuggestions() {
	prev := m.suggest.count()
	val := m.input.Value()
	if val != m.suggest.value {
		m.suggest.sugIdx, m.suggest.sugTop, m.suggest.navigated = 0, 0, false
		m.suggest.value = val
	}
	m.suggest.sugs, m.suggest.args = nil, nil

	if strings.HasPrefix(val, "/") && !strings.Contains(val, "\n") {
		if name, args, hasArgs := strings.Cut(strings.TrimPrefix(val, "/"), " "); !hasArgs {
			// Suggest command names while the name itself is being typed.
			m.suggest.sugs = commandSuggestions(name)
		} else {
			m.suggest.args = m.argSuggestions(name, args)
		}
	}
	if n := m.suggest.count(); m.suggest.sugIdx >= n {
		m.suggest.sugIdx, m.suggest.sugTop = 0, 0
	}
	if m.suggest.count() != prev {
		m.relayout()
	}
}

// commandSuggestions lists every visible command whose name or alias starts
// with typed: an exact name first, then pinned commands, then registry order.
func commandSuggestions(typed string) []slashCommand {
	var out []slashCommand
	for _, c := range slashCommands() {
		if !c.hidden && c.matches(typed) {
			out = append(out, c)
		}
	}
	rank := func(c slashCommand) int {
		if c.is(typed) {
			return -1 // "/model" must not highlight "/models"
		}
		for i, name := range pinnedCommands {
			if c.name == name {
				return i
			}
		}
		return len(pinnedCommands)
	}
	sort.SliceStable(out, func(i, j int) bool { return rank(out[i]) < rank(out[j]) })
	return out
}

// argSuggestions lists the argument phrases of command name that extend
// typed, plus live values (server names, provider names, …) once a phrase
// that takes one is complete. Unknown commands get none.
func (m *Model) argSuggestions(name, typed string) []argSuggestion {
	cmd, ok := findSlashCommand(name)
	if !ok || len(cmd.subs) == 0 {
		return nil
	}
	typed = strings.TrimLeft(typed, " ")
	prefix := "/" + name + " "
	var out []argSuggestion
	for _, phrase := range cmd.subs {
		literal, placeholder := splitPhrase(phrase)
		if strings.HasPrefix(literal, typed) {
			insert := prefix + literal + " "
			out = append(out, argSuggestion{
				label:    strings.TrimSpace(prefix + phrase),
				insert:   insert,
				takesArg: strings.HasPrefix(placeholder, "<"),
			})
			continue
		}
		// The phrase is typed in full: offer live values for its argument.
		if placeholder == "" || cmd.argValues == nil || !strings.HasPrefix(typed, literal+" ") {
			continue
		}
		partial := strings.TrimLeft(strings.TrimPrefix(typed, literal+" "), " ")
		if strings.Contains(partial, " ") {
			continue
		}
		for _, v := range cmd.argValues(m, literal) {
			if v != partial && strings.HasPrefix(v, partial) && completableValue(v) {
				out = append(out, argSuggestion{
					label:  prefix + literal + " " + v,
					insert: prefix + literal + " " + v + " ",
				})
			}
		}
	}
	return out
}

// splitPhrase separates a sub phrase's literal words from its trailing
// <required> or [optional] placeholder.
func splitPhrase(phrase string) (literal, placeholder string) {
	if i := strings.IndexAny(phrase, "<["); i >= 0 {
		return strings.TrimSpace(phrase[:i]), phrase[i:]
	}
	return phrase, ""
}

// completableValue keeps live values that can be inserted as one plain word:
// names come from local config and files, so anything with spaces or control
// characters is left for the user to type.
func completableValue(v string) bool {
	if v == "" || v != terminaltext.Sanitize(v) {
		return false
	}
	return strings.IndexFunc(v, func(r rune) bool { return unicode.IsSpace(r) || !unicode.IsPrint(r) }) < 0
}

// findSlashCommand returns the command whose name or alias is name.
func findSlashCommand(name string) (slashCommand, bool) {
	for _, c := range slashCommands() {
		if c.is(name) {
			return c, true
		}
	}
	return slashCommand{}, false
}

// handleSuggestionKey lets an open popup own ↑/↓, Tab and (for a highlighted
// argument the user picked with the arrows) Enter. It reports whether the key
// was consumed; false leaves it to the normal handlers.
func (m *Model) handleSuggestionKey(msg tea.KeyPressMsg) bool {
	n := m.suggest.count()
	if n == 0 {
		return false
	}
	switch msg.String() {
	case "up":
		m.moveSuggestion((m.suggest.sugIdx - 1 + n) % n)
		return true
	case "down":
		m.moveSuggestion((m.suggest.sugIdx + 1) % n)
		return true
	case "tab":
		if len(m.suggest.sugs) > 0 {
			m.input.SetValue("/" + m.suggest.sugs[m.suggest.sugIdx].name + " ")
		} else {
			m.input.SetValue(m.suggest.args[m.suggest.sugIdx].insert)
		}
		m.input.CursorEnd()
		m.updateSuggestions()
		return true
	case "enter":
		// Enter runs exactly what was typed unless the user picked an
		// argument with the arrows; a typed prefix never expands into a
		// subcommand ("/history c" must not become "/history clear").
		if len(m.suggest.args) == 0 || !m.suggest.navigated || m.pendingAsk != nil {
			return false
		}
		pick := m.suggest.args[m.suggest.sugIdx]
		if pick.takesArg {
			m.input.SetValue(pick.insert)
			m.input.CursorEnd()
			m.updateSuggestions()
			return true
		}
		m.input.SetValue(strings.TrimSpace(pick.insert))
		return false // the Enter handler runs the completed command
	}
	return false
}

// moveSuggestion highlights row idx and scrolls the window to keep it shown.
func (m *Model) moveSuggestion(idx int) {
	m.suggest.sugIdx = idx
	m.suggest.sugTop = suggestionWindow(m.suggest.sugTop, idx, m.suggest.count(), m.layout.suggestionRows)
	if len(m.suggest.args) > 0 {
		m.suggest.navigated = true
	}
}

// suggestionWindow returns the first visible row so that idx is inside a
// window of rows out of n, moving top as little as possible.
func suggestionWindow(top, idx, n, rows int) int {
	if rows <= 0 || n <= rows {
		return 0
	}
	if idx < top {
		top = idx
	}
	if idx >= top+rows {
		top = idx - rows + 1
	}
	return min(max(top, 0), n-rows)
}

// suggestionsView renders the visible window of the popup above the input.
// A ↑ or ↓ in the gutter marks more rows above or below the window.
func (m *Model) suggestionsView() string {
	type row struct{ label, desc string }
	rows := make([]row, 0, m.suggest.count())
	for _, c := range m.suggest.sugs {
		rows = append(rows, row{c.usage, c.desc})
	}
	for _, a := range m.suggest.args {
		rows = append(rows, row{terminaltext.Sanitize(a.label), ""})
	}
	limit := min(len(rows), m.layout.suggestionRows)
	top := suggestionWindow(m.suggest.sugTop, m.suggest.sugIdx, len(rows), limit)
	visible := rows[top : top+limit]

	labelW := 20
	for _, r := range visible {
		labelW = max(labelW, ansi.StringWidth(r.label))
	}
	labelW = min(labelW, 44)

	lines := make([]string, len(visible))
	for i, r := range visible {
		idx := top + i
		gutter := "   "
		switch {
		case idx == m.suggest.sugIdx:
			gutter = " ▸ "
		case i == 0 && top > 0:
			gutter = " ↑ "
		case i == len(visible)-1 && idx < len(rows)-1:
			gutter = " ↓ "
		}
		label := fmt.Sprintf("%-*s", labelW, ansi.Truncate(r.label, labelW, "…"))
		var line string
		if idx == m.suggest.sugIdx {
			line = m.theme.UserLabel.Render(gutter+label) + "  " + m.theme.StatusValue.Render(r.desc)
		} else {
			line = m.theme.StatusBar.Render(gutter+label) + "  " + m.theme.StatusBar.Render(r.desc)
		}
		if m.width > 0 {
			line = ansi.Truncate(line, m.width, "…")
		}
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

// --- live argument values ----------------------------------------------------

func providerArgValues(m *Model, _ string) []string {
	names := make([]string, 0, len(m.cfg.Providers))
	for name := range m.cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func profileArgValues(m *Model, _ string) []string {
	names := make([]string, 0, len(m.profiles))
	for _, p := range m.profiles {
		names = append(names, p.Name)
	}
	return names
}

func skillArgValues(m *Model, _ string) []string {
	if m.skillMgr == nil {
		return nil
	}
	skills := m.skillMgr.Skills()
	ids := make([]string, 0, len(skills))
	for _, s := range skills {
		ids = append(ids, s.QualifiedID())
	}
	return ids
}

func pluginArgValues(m *Model, _ string) []string {
	if m.skillMgr == nil {
		return nil
	}
	plugins := m.skillMgr.Plugins()
	ids := make([]string, 0, len(plugins))
	for _, p := range plugins {
		ids = append(ids, p.Manifest.ID)
	}
	return ids
}

func mcpArgValues(m *Model, _ string) []string {
	if m.mcpRegistry == nil {
		return nil
	}
	servers := m.mcpRegistry.List()
	names := make([]string, 0, len(servers))
	for _, s := range servers {
		names = append(names, s.Config.Name)
	}
	return names
}
