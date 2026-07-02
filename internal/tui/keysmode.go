package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

const maxKeyLog = 12

// enterKeysMode starts the interactive key inspector (/keys, /keys raw).
func (m *Model) enterKeysMode(raw bool) {
	m.keysMode = true
	m.keysRaw = raw
	m.keyLog = nil
	m.openOverlay(m.keysOverlay())
}

// logKey records one received key event and refreshes the inspector.
func (m *Model) logKey(entry string) {
	m.keyLog = append(m.keyLog, entry)
	if len(m.keyLog) > maxKeyLog {
		m.keyLog = m.keyLog[len(m.keyLog)-maxKeyLog:]
	}
	m.viewport.SetContent(m.keysOverlay())
	m.viewport.GotoBottom()
}

// updateKeysMode handles input while the inspector is active.
func (m *Model) updateKeysMode(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Extended sequences (e.g. shift+enter via modifyOtherKeys) are the
	// main thing users come here to verify.
	if seq, ok := extendedKeySeq(msg); ok {
		name := "extended CSI sequence"
		if isModifiedEnter(seq) {
			name = "shift+enter (modified enter)"
		}
		entry := name
		if m.keysRaw {
			entry += fmt.Sprintf("  —  ESC[%s", seq)
		}
		m.logKey(entry)
		return m, nil
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if key.Type == tea.KeyEsc {
		m.keysMode = false
		m.closeOverlay()
		return m, nil
	}
	if key.Type == tea.KeyCtrlC {
		return m.handleCtrlC()
	}

	entry := key.String()
	if m.keysRaw && len(key.Runes) > 0 {
		entry += fmt.Sprintf("  —  runes %v", key.Runes)
	}
	m.logKey(entry)
	return m, nil
}

func (m *Model) keysOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("key inspector") + "\n\n")
	b.WriteString(m.theme.SystemNote.Render("Press keys to inspect what the terminal sends. Esc exits.") + "\n\n")

	b.WriteString(m.theme.UserLabel.Render("received") + "\n")
	if len(m.keyLog) == 0 {
		b.WriteString(m.theme.StatusBar.Render("  (nothing yet — try enter, shift+enter, alt+enter, ctrl+j)") + "\n")
	}
	for _, k := range m.keyLog {
		b.WriteString("  " + m.theme.StatusValue.Render("· "+k) + "\n")
	}

	b.WriteString("\n" + m.theme.UserLabel.Render("what to look for") + "\n")
	b.WriteString(m.theme.StatusBar.Render(
		"  If shift+enter shows as \"enter\", this terminal does not expose it as a\n"+
			"  distinct key event. Use alt+enter or ctrl+j for newlines, or enable an\n"+
			"  enhanced keyboard protocol in your terminal if supported.") + "\n")
	b.WriteString("\n" + m.theme.SystemNote.Render("esc to close"))
	return b.String()
}
