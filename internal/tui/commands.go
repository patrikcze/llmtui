package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/history"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tui/components"
)

// slashCommand is one command reachable by typing "/" in the input.
type slashCommand struct {
	name     string
	aliases  []string
	usage    string
	desc     string
	category string
	hidden   bool
	// blockWhileThinking rejects the command while a reply is streaming:
	// it would mutate state the in-flight request still depends on.
	blockWhileThinking bool
	run                func(m *Model, args string) tea.Cmd
}

// matches reports whether typed is a prefix of the name or any alias.
func (c slashCommand) matches(typed string) bool {
	if strings.HasPrefix(c.name, typed) {
		return true
	}
	for _, a := range c.aliases {
		if strings.HasPrefix(a, typed) {
			return true
		}
	}
	return false
}

// is reports whether name is the command's name or an alias.
func (c slashCommand) is(name string) bool {
	if c.name == name {
		return true
	}
	for _, a := range c.aliases {
		if a == name {
			return true
		}
	}
	return false
}

// Command categories, in /help display order.
var commandCategories = []string{
	"Chat", "Provider", "Model", "Prompt", "Context", "Cache", "Memory", "Diagnostics", "Session",
}

type modelsResultMsg struct {
	models []provider.ModelInfo
	err    error
}

func slashCommands() []slashCommand {
	return []slashCommand{
		// --- Chat ---
		{name: "help", usage: "/help [topic]", desc: "show keys and commands, grouped by category", category: "Chat", run: func(m *Model, args string) tea.Cmd {
			m.openOverlay(m.helpOverlay(args))
			return nil
		}},
		{name: "copy", usage: "/copy", desc: "copy the last reply to the clipboard", category: "Chat", run: func(m *Model, _ string) tea.Cmd {
			return m.copyLastReply()
		}},
		{name: "clear", usage: "/clear", desc: "clear the conversation", category: "Chat", blockWhileThinking: true, run: func(m *Model, _ string) tea.Cmd {
			m.session.Clear()
			m.summary = ""
			m.refreshViewport()
			return nil
		}},
		{name: "retry", usage: "/retry", desc: "retry the last user message", category: "Chat", run: func(m *Model, _ string) tea.Cmd {
			return m.retryLast()
		}},
		{name: "quit", aliases: []string{"exit"}, usage: "/quit", desc: "save session and exit llmtui", category: "Chat", run: func(m *Model, _ string) tea.Cmd {
			return m.quit()
		}},

		// --- Provider ---
		{name: "provider", usage: "/provider [list|switch <name>]", desc: "show or switch the active provider", category: "Provider", blockWhileThinking: true, run: cmdProvider},
		{name: "providers", usage: "/providers", desc: "list configured providers", category: "Provider", run: func(m *Model, _ string) tea.Cmd {
			m.openOverlay(m.providersOverlay())
			return nil
		}},

		// --- Model ---
		{name: "models", usage: "/models [refresh]", desc: "list models on the current provider", category: "Model", run: func(m *Model, _ string) tea.Cmd {
			prov := m.prov
			return func() tea.Msg {
				models, err := prov.ListModels(context.Background())
				return modelsResultMsg{models: models, err: err}
			}
		}},
		{name: "model", usage: "/model <id>", desc: "switch to a different model", category: "Model", blockWhileThinking: true, run: func(m *Model, args string) tea.Cmd {
			if args == "" {
				m.errText = "usage: /model <id> (see /models)"
				m.refreshViewport()
				return nil
			}
			m.model = args
			m.notice = "model set to " + args
			return nil
		}},
		{name: "profile", usage: "/profile [list|auto|set <name>|inspect]", desc: "model profiles: context window, temperature, style", category: "Model", run: cmdProfile},

		// --- Prompt ---
		{name: "prompt", usage: "/prompt [preview|raw|composed|mode <m>]", desc: "inspect and configure prompt composition", category: "Prompt", run: cmdPrompt},
		{name: "template", usage: "/template [list|use <name>|clear|inspect <name>]", desc: "reusable conversation templates", category: "Prompt", run: cmdTemplate},

		// --- Context ---
		{name: "context", usage: "/context [summary|rebuild|clear-summary|strategy <s>]", desc: "context window management and session summary", category: "Context", run: cmdContext},

		// --- Cache ---
		{name: "cache", usage: "/cache [stats|clear|on|off]", desc: "local response cache", category: "Cache", run: cmdCache},

		// --- Memory ---
		{name: "memory", usage: "/memory [on|off|add <text>|list|remove <id>|clear]", desc: "local memory snippets (opt-in)", category: "Memory", run: cmdMemory},

		// --- Diagnostics ---
		{name: "doctor", usage: "/doctor [provider [name]]", desc: "provider and model diagnostics", category: "Diagnostics", run: cmdDoctor},
		{name: "debug", usage: "/debug [on|off|last]", desc: "debug drawer for the last request", category: "Diagnostics", run: cmdDebug},
		{name: "keys", usage: "/keys [raw]", desc: "interactive key inspector (debug shift+enter)", category: "Diagnostics", run: cmdKeys},
		{name: "config", usage: "/config [path|show|reload]", desc: "show or reload configuration (secrets redacted)", category: "Diagnostics", run: cmdConfig},

		// --- Session ---
		{name: "usage", usage: "/usage [session|last|reset|export]", desc: "usage dashboard: charts, models, cache, streaks", category: "Session", run: cmdUsage},
		{name: "stats", usage: "/stats", desc: "per-exchange session statistics", category: "Session", run: func(m *Model, _ string) tea.Cmd {
			m.openOverlay(m.statsOverlay())
			return nil
		}},
		{name: "save", usage: "/save", desc: "save this session to the history directory", category: "Session", run: func(m *Model, _ string) tea.Cmd {
			m.saveWithNotice()
			return nil
		}},
		{name: "history", usage: "/history [load <name>|search <q>|export md|json|clear]", desc: "saved sessions: list, load, search, export", category: "Session", run: cmdHistory},
	}
}

const maxSuggestions = 6

// updateSuggestions recomputes the command popup from the current input.
func (m *Model) updateSuggestions() {
	prev := len(m.sugs)
	m.sugs = nil

	val := m.input.Value()
	// Suggest only while the command name itself is being typed.
	if strings.HasPrefix(val, "/") && !strings.ContainsAny(val, " \n") {
		typed := strings.TrimPrefix(val, "/")
		for _, c := range slashCommands() {
			if !c.hidden && c.matches(typed) {
				// An exact name match leads the list ("/model" must not
				// highlight "/models" just because it registers earlier).
				if c.is(typed) {
					m.sugs = append([]slashCommand{c}, m.sugs...)
				} else {
					m.sugs = append(m.sugs, c)
				}
				if len(m.sugs) == maxSuggestions {
					break
				}
			}
		}
	}
	if m.sugIdx >= len(m.sugs) {
		m.sugIdx = 0
	}
	if len(m.sugs) != prev {
		m.relayout()
	}
}

// runSlashCommand executes the typed (or popup-selected) command.
func (m *Model) runSlashCommand() tea.Cmd {
	val := strings.TrimSpace(m.input.Value())
	name, args, _ := strings.Cut(strings.TrimPrefix(val, "/"), " ")
	args = strings.TrimSpace(args)

	// A highlighted suggestion completes a partially typed name, but an
	// exactly typed command always runs itself ("/model" is not "/models").
	if len(m.sugs) > 0 && !m.sugs[m.sugIdx].is(name) {
		exact := false
		for _, c := range slashCommands() {
			if c.is(name) {
				exact = true
				break
			}
		}
		if !exact {
			name = m.sugs[m.sugIdx].name
		}
	}

	m.input.Reset()
	m.updateSuggestions()
	m.syncInputHeight()

	for _, c := range slashCommands() {
		if c.is(name) {
			if m.thinking && c.blockWhileThinking {
				m.errText = fmt.Sprintf("/%s is unavailable while a reply is streaming — esc to stop it first", c.name)
				m.refreshViewport()
				return nil
			}
			m.errText = ""
			return c.run(m, args)
		}
	}
	m.errText = fmt.Sprintf("unknown command /%s — try /help", name)
	m.refreshViewport()
	return nil
}

// splitArgs returns the first argument word and the remaining text.
func splitArgs(args string) (first, rest string) {
	first, rest, _ = strings.Cut(strings.TrimSpace(args), " ")
	return first, strings.TrimSpace(rest)
}

func (m *Model) switchProvider(name string) tea.Cmd {
	if name == "" {
		m.errText = "usage: /provider <name> (see /providers)"
		m.refreshViewport()
		return nil
	}
	pc, ok := m.cfg.Providers[name]
	if !ok {
		m.errText = fmt.Sprintf("provider %q is not configured (see /providers)", name)
		m.refreshViewport()
		return nil
	}
	prov, err := app.BuildProvider(name, pc, m.cfg.Network)
	if err != nil {
		m.errText = err.Error()
		m.refreshViewport()
		return nil
	}
	m.prov = prov
	// Keep the config's notion of the active provider in sync, so cache keys,
	// error messages, and /doctor resolve this provider's settings. The
	// base-url/api-key overrides belonged to the launch-time provider.
	m.cfg.Provider = name
	m.cfg.BaseURL = ""
	m.cfg.APIKey = ""
	if pc.DefaultModel != "" {
		m.model = pc.DefaultModel
	}
	m.demoMode = false
	m.connected = false
	m.notice = fmt.Sprintf("switched to %s (%s)", name, m.model)
	return m.checkHealth(false)
}

// openOverlay shows scrollable content in the viewport area until Esc.
func (m *Model) openOverlay(content string) {
	m.overlayOpen = true
	m.viewport.SetContent(content)
	m.viewport.GotoTop()
}

func (m *Model) closeOverlay() {
	m.overlayOpen = false
	m.refreshViewport()
}

func (m *Model) helpOverlay(topic string) string {
	var b strings.Builder
	title := "llmtui help"
	if topic != "" {
		title += " — " + topic
	}
	b.WriteString(m.theme.Badge.Render(title) + "\n\n")

	topic = strings.ToLower(strings.TrimSpace(topic))
	if topic == "" {
		b.WriteString(m.theme.UserLabel.Render("keyboard") + "\n")
		keys := [][2]string{
			{"enter", "send message / run command"},
			{"shift+↵", "newline (iTerm2, VS Code, WezTerm, Ghostty, …; check /keys)"},
			{"\\ + ↵", "newline — trailing backslash continues the line"},
			{"ctrl+j", "insert newline (works everywhere)"},
			{"ctrl+s", "save session to history"},
			{"ctrl+y", "copy last reply to clipboard"},
			{"ctrl+o", "toggle text-selection mode (release mouse)"},
			{"ctrl+v", "paste image from clipboard (vision models)"},
			{"ctrl+x", "remove last pasted image"},
			{"esc", "stop generation · close this overlay"},
			{"ctrl+l", "clear conversation"},
			{"↑/↓", "navigate command suggestions · scroll"},
			{"tab", "complete selected command"},
			{"ctrl+c", "press twice to quit (first stops/clears)"},
		}
		for _, k := range keys {
			fmt.Fprintf(&b, "  %s  %s\n",
				m.theme.StatusValue.Render(fmt.Sprintf("%-8s", k[0])),
				m.theme.StatusBar.Render(k[1]))
		}
		b.WriteString("\n")
	}

	// Commands grouped by category; a topic filters to matching
	// category or command names.
	byCategory := map[string][]slashCommand{}
	for _, c := range slashCommands() {
		if c.hidden {
			continue
		}
		if topic != "" && !strings.EqualFold(c.category, topic) && !c.matches(topic) {
			continue
		}
		byCategory[c.category] = append(byCategory[c.category], c)
	}
	for _, cat := range commandCategories {
		cmds := byCategory[cat]
		if len(cmds) == 0 {
			continue
		}
		b.WriteString(m.theme.UserLabel.Render(strings.ToLower(cat)) + "\n")
		for _, c := range cmds {
			usage := c.usage
			if len(c.aliases) > 0 {
				usage += "  (alias: /" + strings.Join(c.aliases, ", /") + ")"
			}
			fmt.Fprintf(&b, "  %s  %s\n",
				m.theme.StatusValue.Render(fmt.Sprintf("%-44s", usage)),
				m.theme.StatusBar.Render(c.desc))
		}
		b.WriteString("\n")
	}

	b.WriteString(m.theme.SystemNote.Render("esc to close · /help <category> to filter"))
	return b.String()
}

func (m *Model) modelsOverlay(models []provider.ModelInfo) string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("models on "+m.prov.Name()) + "\n\n")

	if len(models) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no models found") + "\n")
	}
	for _, mi := range models {
		marker := "  "
		label := m.theme.StatusValue.Render(mi.ID)
		if mi.ID == m.model {
			marker = m.theme.BadgeOK.Render("▸ ")
			label = m.theme.BadgeOK.Render(mi.ID)
		}
		line := marker + label
		if mi.Description != "" {
			line += "  " + m.theme.StatusBar.Render(mi.Description)
		}
		b.WriteString(line + "\n")
	}

	b.WriteString("\n" + m.theme.SystemNote.Render("switch with /model <id> · esc to close"))
	return b.String()
}

func (m *Model) providersOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("configured providers") + "\n\n")

	names := make([]string, 0, len(m.cfg.Providers))
	for name := range m.cfg.Providers {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		pc := m.cfg.Providers[name]
		marker := "  "
		label := m.theme.StatusValue.Render(fmt.Sprintf("%-20s", name))
		if name == m.prov.Name() {
			marker = m.theme.BadgeOK.Render("▸ ")
			label = m.theme.BadgeOK.Render(fmt.Sprintf("%-20s", name))
		}
		fmt.Fprintf(&b, "%s%s %s\n", marker, label,
			m.theme.StatusBar.Render(pc.Type+"  "+pc.BaseURL))
	}

	b.WriteString("\n" + m.theme.SystemNote.Render("switch with /provider <name> · esc to close"))
	return b.String()
}

func (m *Model) statsOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("session statistics") + "\n\n")

	if len(m.session.Stats) == 0 {
		b.WriteString(m.theme.SystemNote.Render("no completed exchanges yet") + "\n")
	} else {
		b.WriteString(m.theme.StatusBar.Render("  #   prompt   reply   total   time     tok/s") + "\n")
		for i, st := range m.session.Stats {
			est := " "
			if st.Usage.Estimated {
				est = "~"
			}
			b.WriteString(m.theme.StatusValue.Render(fmt.Sprintf("  %-3d %-8d %-7d %-6d%s %-8s %.1f",
				i+1, st.Usage.PromptTokens, st.Usage.CompletionTokens, st.Usage.TotalTokens, est,
				st.Duration.Round(10*time.Millisecond), st.TokensPerSec)) + "\n")
		}
		b.WriteString("\n")
		total := fmt.Sprintf("total  prompt %d · reply %d · %d tokens",
			m.session.TotalPromptTokens, m.session.TotalCompletionTokens, m.session.TotalTokens())
		if m.session.AnyEstimated {
			total += "  (~ = estimated)"
		}
		b.WriteString(m.theme.UserLabel.Render(total) + "\n")
	}

	// All-time totals from the persistent usage log.
	if m.historyDir != "" {
		if records, err := history.ReadUsage(m.historyDir); err == nil && len(records) > 0 {
			prompt, reply := 0, 0
			for _, r := range records {
				prompt += r.PromptTokens
				reply += r.CompletionTokens
			}
			b.WriteString("\n" + m.theme.UserLabel.Render("all time") + "\n")
			fmt.Fprintf(&b, "  %s\n", m.theme.StatusValue.Render(fmt.Sprintf(
				"%d requests · prompt %d · reply %d · %d tokens",
				len(records), prompt, reply, prompt+reply)))
			days := history.AggregateByDay(records)
			totals := make([]int, len(days))
			for i, d := range days {
				totals[i] = d.TotalTokens()
			}
			b.WriteString("  " + m.theme.ChartBar.Render(components.Sparkline(totals, 40, false)) +
				m.theme.StatusBar.Render("  tokens/day") + "\n")
		}
	}

	b.WriteString("\n" + m.theme.SystemNote.Render("esc to close"))
	return b.String()
}

func (m *Model) historyOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("saved sessions") + "\n\n")

	if m.historyDir == "" {
		b.WriteString(m.theme.SystemNote.Render("history saving is disabled (chat.save_history)") + "\n")
	} else {
		metas, err := history.List(m.historyDir)
		switch {
		case err != nil:
			b.WriteString(m.theme.ErrorText.Render(err.Error()) + "\n")
		case len(metas) == 0:
			b.WriteString(m.theme.SystemNote.Render("no saved sessions yet — /save or ctrl+s") + "\n")
		default:
			for _, meta := range metas {
				marker := "  "
				name := m.theme.StatusValue.Render(meta.Name)
				if meta.Name == m.sessionName {
					marker = m.theme.BadgeOK.Render("▸ ")
					name = m.theme.BadgeOK.Render(meta.Name)
				}
				fmt.Fprintf(&b, "%s%s  %s\n", marker, name,
					m.theme.StatusBar.Render(fmt.Sprintf("%s · %s/%s · %d msgs · %d tok",
						meta.SavedAt.Format("2006-01-02 15:04"),
						meta.Provider, meta.Model, meta.Messages, meta.Tokens)))
			}
		}
		b.WriteString("\n" + m.theme.SystemNote.Render("stored in "+m.historyDir))
	}

	b.WriteString("\n" + m.theme.SystemNote.Render("esc to close"))
	return b.String()
}

// suggestionsView renders the command popup shown above the input.
func (m *Model) suggestionsView() string {
	lines := make([]string, len(m.sugs))
	for i, c := range m.sugs {
		usage := fmt.Sprintf("%-20s", c.usage)
		if i == m.sugIdx {
			lines[i] = m.theme.UserLabel.Render(" ▸ "+usage) + m.theme.StatusValue.Render(c.desc)
		} else {
			lines[i] = m.theme.StatusBar.Render("   "+usage) + m.theme.StatusBar.Render(c.desc)
		}
	}
	return strings.Join(lines, "\n")
}
