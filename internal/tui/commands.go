package tui

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/patrikcze/llmtui/internal/app"
	"github.com/patrikcze/llmtui/internal/provider"
)

// slashCommand is one command reachable by typing "/" in the input.
type slashCommand struct {
	name  string
	usage string
	desc  string
	run   func(m *Model, args string) tea.Cmd
}

type modelsResultMsg struct {
	models []provider.ModelInfo
	err    error
}

func slashCommands() []slashCommand {
	return []slashCommand{
		{"help", "/help", "show keyboard shortcuts and commands", func(m *Model, _ string) tea.Cmd {
			m.openOverlay(m.helpOverlay())
			return nil
		}},
		{"copy", "/copy", "copy the last reply to the clipboard", func(m *Model, _ string) tea.Cmd {
			return m.copyLastReply()
		}},
		{"clear", "/clear", "clear the conversation", func(m *Model, _ string) tea.Cmd {
			m.session.Clear()
			m.refreshViewport()
			return nil
		}},
		{"models", "/models", "list models on the current provider", func(m *Model, _ string) tea.Cmd {
			prov := m.prov
			return func() tea.Msg {
				models, err := prov.ListModels(context.Background())
				return modelsResultMsg{models: models, err: err}
			}
		}},
		{"model", "/model <id>", "switch to a different model", func(m *Model, args string) tea.Cmd {
			if args == "" {
				m.errText = "usage: /model <id> (see /models)"
				m.refreshViewport()
				return nil
			}
			m.model = args
			m.notice = "model set to " + args
			return nil
		}},
		{"providers", "/providers", "list configured providers", func(m *Model, _ string) tea.Cmd {
			m.openOverlay(m.providersOverlay())
			return nil
		}},
		{"provider", "/provider <name>", "switch provider (and its default model)", func(m *Model, args string) tea.Cmd {
			return m.switchProvider(args)
		}},
		{"stats", "/stats", "show session usage statistics", func(m *Model, _ string) tea.Cmd {
			m.openOverlay(m.statsOverlay())
			return nil
		}},
		{"quit", "/quit", "exit llmtui", func(_ *Model, _ string) tea.Cmd {
			return tea.Quit
		}},
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
			if strings.HasPrefix(c.name, typed) {
				m.sugs = append(m.sugs, c)
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

	// A highlighted suggestion wins over the partially typed name.
	if len(m.sugs) > 0 {
		name = m.sugs[m.sugIdx].name
	}

	m.input.Reset()
	m.updateSuggestions()

	for _, c := range slashCommands() {
		if c.name == name {
			m.errText = ""
			return c.run(m, args)
		}
	}
	m.errText = fmt.Sprintf("unknown command /%s — try /help", name)
	m.refreshViewport()
	return nil
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
	prov, err := app.BuildProvider(name, pc)
	if err != nil {
		m.errText = err.Error()
		m.refreshViewport()
		return nil
	}
	m.prov = prov
	if pc.DefaultModel != "" {
		m.model = pc.DefaultModel
	}
	m.demoMode = false
	m.connected = false
	m.notice = fmt.Sprintf("switched to %s (%s)", name, m.model)
	return m.checkHealth()
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

func (m *Model) helpOverlay() string {
	var b strings.Builder
	title := m.theme.Badge.Render("llmtui help")
	b.WriteString(title + "\n\n")

	b.WriteString(m.theme.UserLabel.Render("keyboard") + "\n")
	keys := [][2]string{
		{"enter", "send message / run command"},
		{"ctrl+y", "copy last reply to clipboard"},
		{"ctrl+o", "toggle text-selection mode (release mouse)"},
		{"ctrl+v", "paste image from clipboard (vision models)"},
		{"ctrl+x", "remove last pasted image"},
		{"esc", "stop generation · close this overlay"},
		{"ctrl+l", "clear conversation"},
		{"↑/↓", "navigate command suggestions · scroll"},
		{"tab", "complete selected command"},
		{"ctrl+c", "quit"},
	}
	for _, k := range keys {
		fmt.Fprintf(&b, "  %s  %s\n",
			m.theme.StatusValue.Render(fmt.Sprintf("%-8s", k[0])),
			m.theme.StatusBar.Render(k[1]))
	}

	b.WriteString("\n" + m.theme.UserLabel.Render("commands") + "\n")
	for _, c := range slashCommands() {
		fmt.Fprintf(&b, "  %s  %s\n",
			m.theme.StatusValue.Render(fmt.Sprintf("%-20s", c.usage)),
			m.theme.StatusBar.Render(c.desc))
	}

	b.WriteString("\n" + m.theme.SystemNote.Render("esc to close"))
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
