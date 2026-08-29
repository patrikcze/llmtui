package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const maxKeyLog = 12

// keyInspectorState holds the interactive key-inspector overlay's state
// (/keys, /keys raw): whether it is active, whether it shows raw byte
// sequences, and the bounded ring of recently received key events. Every
// field's zero value is the correct "inspector closed" state.
type keyInspectorState struct {
	keysMode bool
	keysRaw  bool
	keyLog   []string
}

// enterKeysMode starts the interactive key inspector (/keys, /keys raw).
func (m *Model) enterKeysMode(raw bool) {
	m.keys.keysMode = true
	m.keys.keysRaw = raw
	m.keys.keyLog = nil
	m.openOverlay(m.keysOverlay())
}

// logKey records one received key event and refreshes the inspector.
func (m *Model) logKey(entry string) {
	m.keys.keyLog = append(m.keys.keyLog, entry)
	if len(m.keys.keyLog) > maxKeyLog {
		m.keys.keyLog = m.keys.keyLog[len(m.keys.keyLog)-maxKeyLog:]
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
		if m.keys.keysRaw {
			entry += fmt.Sprintf("  —  ESC[%s", seq)
		}
		m.logKey(entry)
		return m, nil
	}

	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	if key.String() == "esc" {
		m.keys.keysMode = false
		m.closeOverlay()
		return m, nil
	}
	if key.String() == "ctrl+c" {
		return m.handleCtrlC()
	}

	entry := key.String()
	if m.keys.keysRaw && key.Text != "" {
		entry += fmt.Sprintf("  —  text %q", key.Text)
	}
	m.logKey(entry)
	return m, nil
}

func (m *Model) keysOverlay() string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("key inspector") + "\n\n")
	b.WriteString(m.theme.SystemNote.Render("Press keys to inspect what the terminal sends. Esc exits.") + "\n\n")

	b.WriteString(m.theme.UserLabel.Render("received") + "\n")
	if len(m.keys.keyLog) == 0 {
		b.WriteString(m.theme.StatusBar.Render("  (nothing yet — try enter, shift+enter, alt+enter, ctrl+j)") + "\n")
	}
	for _, k := range m.keys.keyLog {
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
