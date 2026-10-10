package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// This file ports the iOS app's message queue (MobileAppModel.send /
// dequeueNextIfNeeded) to the TUI. A message submitted while a reply, tool
// batch or agent run is in progress is queued instead of being dropped, and
// queued messages go out one at a time, in order, once the model is idle.
//
// The queue never answers a question on the user's behalf: it waits while a
// tool approval, an ask_user question, an agent criteria review or any other
// agent input pause is open. It also pauses (keeping its messages) when the
// user stops a turn or a turn fails, so Esc never silently starts the next
// message. Queued messages are session-local and never written to disk.

// maxQueuedMessages matches the iOS app's cap.
const maxQueuedMessages = 10

// queuedMessage is one submission waiting for the model to become idle.
type queuedMessage struct {
	text   string
	images []provider.Image
}

// messageQueueState holds the queued submissions and their editing state.
type messageQueueState struct {
	items []queuedMessage
	// paused stops automatic sending after the user stopped a turn or a
	// turn failed. Enter on an empty composer or /queue resume clears it.
	paused bool
	// editing marks that the composer holds a queued message pulled back
	// for editing; editSlot is the position it returns to when re-queued,
	// so editing a message never changes its place in line.
	editing  bool
	editSlot int
	// drainScheduled guards against scheduling a second queueDrainMsg while
	// one is already on its way.
	drainScheduled bool
}

// queueDrainMsg asks Update to send the next queued message. Sending is a
// separate message, not part of the Update that ended the turn, so the
// finished turn's own commands (persistence, saves) are issued first.
type queueDrainMsg struct{}

// queueWaiting reports whether anything is waiting in line, counting a
// queued message currently pulled back into the composer.
func (q *messageQueueState) queueWaiting() bool {
	return len(q.items) > 0 || q.editing
}

// queueIdle reports whether the model could take the next queued message
// now: nothing is running and nothing is waiting for the person's answer.
// It ignores the paused flag; callers check that separately.
func (m *Model) queueIdle() bool {
	if m.quitting || m.busy() || m.pendingAsk != nil || len(m.pendingCalls) > 0 {
		return false
	}
	switch m.turnRuntime.state {
	case turnWaitingApproval, turnWaitingUserInput:
		return false
	}
	return !m.agentRunActive() && !m.agentNeedsUserInput() && !m.agentContracting()
}

// enqueueComposer moves the composer's text and images into the queue. It
// returns false (leaving the composer untouched) when there is nothing to
// queue or the queue is full.
func (m *Model) enqueueComposer() bool {
	text := strings.TrimSpace(m.input.Value())
	if text == "" && len(m.attachments) == 0 {
		return false
	}
	if len(m.queue.items) >= maxQueuedMessages {
		m.errText = fmt.Sprintf("you can queue up to %d messages — /queue to edit or drop one", maxQueuedMessages)
		m.refreshViewport()
		return false
	}
	item := queuedMessage{text: text, images: m.attachments}
	slot := len(m.queue.items)
	if m.queue.editing {
		slot = min(max(m.queue.editSlot, 0), len(m.queue.items))
	}
	m.queue.items = append(m.queue.items, queuedMessage{})
	copy(m.queue.items[slot+1:], m.queue.items[slot:])
	m.queue.items[slot] = item
	m.queue.editing = false

	m.composerHistory.record(text)
	m.input.Reset()
	m.attachments = nil
	m.errText = ""
	m.notice = fmt.Sprintf("queued as #%d — sends when the model is free · ↑ edits the last one", slot+1)
	m.updateSuggestions()
	m.syncInputHeight()
	m.relayout()
	return true
}

// editQueued pulls queued message i (0-based) back into the composer. Enter
// re-queues it at the same position; clearing the composer drops it.
func (m *Model) editQueued(i int) {
	if i < 0 || i >= len(m.queue.items) {
		return
	}
	if m.queue.editing {
		// Only one message can be in the composer at a time; put the one
		// already being edited back before pulling another.
		slot := m.queue.editSlot
		if m.enqueueComposer() && slot <= i {
			i++
		}
		m.queue.editing = false
		if i >= len(m.queue.items) {
			return
		}
	}
	item := m.queue.items[i]
	m.queue.items = append(m.queue.items[:i], m.queue.items[i+1:]...)
	m.queue.editing = true
	m.queue.editSlot = i
	m.composerHistory.stopBrowsing()
	m.input.SetValue(item.text)
	m.input.CursorEnd()
	m.attachments = append(m.attachments, item.images...)
	m.notice = fmt.Sprintf("editing queued message #%d — enter puts it back · ctrl+u then enter drops it", i+1)
	m.updateSuggestions()
	m.syncInputHeight()
	m.relayout()
}

// dropQueued removes queued message i (0-based).
func (m *Model) dropQueued(i int) {
	if i < 0 || i >= len(m.queue.items) {
		return
	}
	m.queue.items = append(m.queue.items[:i], m.queue.items[i+1:]...)
	if m.queue.editing && i < m.queue.editSlot {
		m.queue.editSlot--
	}
	if len(m.queue.items) == 0 {
		m.queue.paused = false
	}
	m.relayout()
}

// resetQueue drops every queued message. Used when the conversation they
// were written for is cleared or replaced.
func (m *Model) resetQueue() {
	if !m.queue.queueWaiting() && !m.queue.paused {
		return
	}
	m.queue.items = nil
	m.queue.paused = false
	m.queue.editing = false
	m.relayout()
}

// afterQueueUpdate runs after every Update. It pauses the queue when the
// update ended a busy turn because the person stopped it (a key press or the
// stop button) or because the turn failed, and forgets an edit the person
// abandoned by clearing the composer. It then returns the command that sends
// the next queued message, if the model is ready for one.
func (m *Model) afterQueueUpdate(msg tea.Msg, wasBusy bool) tea.Cmd {
	if m.queue.editing && strings.TrimSpace(m.input.Value()) == "" && len(m.attachments) == 0 {
		m.queue.editing = false
		m.relayout()
	}
	if wasBusy && !m.busy() && len(m.queue.items) > 0 {
		stopped := false
		switch msg.(type) {
		case tea.KeyPressMsg, tea.MouseReleaseMsg:
			stopped = true
		}
		if stopped || m.turnRuntime.state == turnFailed {
			m.queue.paused = true
			m.notice = fmt.Sprintf("%d queued message(s) paused — enter on an empty composer resumes · /queue to edit", len(m.queue.items))
		}
	}
	if len(m.queue.items) == 0 || m.queue.paused || m.queue.drainScheduled || !m.queueIdle() {
		return nil
	}
	if m.queue.editing && m.queue.editSlot == 0 {
		// The message next in line is open in the composer; wait for it.
		return nil
	}
	m.queue.drainScheduled = true
	return func() tea.Msg { return queueDrainMsg{} }
}

// drainQueue sends the next queued message if the model is still ready.
func (m *Model) drainQueue() tea.Cmd {
	m.queue.drainScheduled = false
	if len(m.queue.items) == 0 || m.queue.paused || !m.queueIdle() {
		return nil
	}
	if m.queue.editing {
		if m.queue.editSlot == 0 {
			return nil
		}
		m.queue.editSlot--
	}
	next := m.queue.items[0]
	m.queue.items = m.queue.items[1:]
	if len(m.queue.items) == 0 {
		m.queue.items = nil
	}
	m.relayout()
	return m.submit(next.text, next.images)
}

// queueEnter handles Enter while the model is idle but messages are still
// waiting in line. A new submission joins the back of the line (or returns
// to its old slot when it was being edited) so it can never overtake older
// queued messages; Enter on an empty composer resumes a paused queue.
func (m *Model) queueEnter() (handled bool) {
	if len(m.queue.items) == 0 || !m.queueIdle() {
		return false
	}
	if strings.TrimSpace(m.input.Value()) == "" && len(m.attachments) == 0 {
		if m.queue.paused {
			m.queue.paused = false
			m.notice = "queue resumed"
		}
		return true
	}
	if m.enqueueComposer() {
		m.queue.paused = false
	}
	return true
}

// cmdQueue implements /queue.
func cmdQueue(m *Model, args string) tea.Cmd {
	sub, rest := splitArgs(args)
	index := func() (int, bool) {
		n, err := strconv.Atoi(strings.TrimPrefix(rest, "#"))
		if err != nil || n < 1 || n > len(m.queue.items) {
			if len(m.queue.items) == 0 {
				m.errText = "the queue is empty"
			} else {
				m.errText = fmt.Sprintf("usage: /queue %s <1-%d>", sub, len(m.queue.items))
			}
			m.refreshViewport()
			return 0, false
		}
		return n - 1, true
	}
	switch strings.ToLower(sub) {
	case "", "list", "show":
		m.openModalOverlay("Queued messages", "↑/↓ scroll · esc close", m.queueOverlay)
	case "edit":
		if i, ok := index(); ok {
			m.editQueued(i)
		}
	case "drop", "rm", "remove", "cancel":
		if i, ok := index(); ok {
			m.dropQueued(i)
			m.notice = fmt.Sprintf("dropped queued message #%d", i+1)
		}
	case "clear":
		n := len(m.queue.items)
		m.resetQueue()
		m.notice = fmt.Sprintf("dropped %d queued message(s)", n)
	case "resume":
		m.queue.paused = false
		if len(m.queue.items) > 0 {
			m.notice = "queue resumed"
		}
	case "pause":
		if len(m.queue.items) > 0 {
			m.queue.paused = true
			m.notice = "queue paused — /queue resume or enter on an empty composer to continue"
		}
	default:
		m.errText = "usage: /queue [list|edit <n>|drop <n>|clear|pause|resume]"
		m.refreshViewport()
	}
	return nil
}

// queueOverlay renders the full text of every queued message.
func (m *Model) queueOverlay() string {
	var b strings.Builder
	if len(m.queue.items) == 0 {
		b.WriteString(m.theme.SystemNote.Render("Nothing is queued. Messages you send while a reply is running wait here and go out in order."))
		b.WriteString("\n")
		return b.String()
	}
	state := "sends automatically when the model is free"
	if m.queue.paused {
		state = "paused — enter on an empty composer or /queue resume continues"
	}
	b.WriteString(m.theme.SystemNote.Render(fmt.Sprintf("%d queued · %s", len(m.queue.items), state)))
	b.WriteString("\n\n")
	for i, item := range m.queue.items {
		label := fmt.Sprintf("#%d", i+1)
		if len(item.images) > 0 {
			label += fmt.Sprintf(" · %d image(s)", len(item.images))
		}
		b.WriteString(m.theme.StatusValue.Render(label))
		b.WriteString("\n")
		b.WriteString(terminaltext.Sanitize(item.text))
		b.WriteString("\n\n")
	}
	b.WriteString(m.theme.HelpFooter.Render("/queue edit <n> · /queue drop <n> · /queue clear · ↑ on an empty composer edits the last one"))
	b.WriteString("\n")
	return b.String()
}

// queuePanelItemLimit is how many queued messages the panel above the
// composer lists before collapsing the rest into "+N more".
func queuePanelItemLimit(height int, compact bool) int {
	switch {
	case compact || height < 12:
		return 0
	case height < 20:
		return 1
	default:
		return 3
	}
}

// queuePanelRows is the number of rows the queue panel adds to the composer.
func (m *Model) queuePanelRows(height int, compact bool) int {
	if !m.queue.queueWaiting() {
		return 0
	}
	n := len(m.queue.items)
	limit := queuePanelItemLimit(height, compact)
	rows := 1 + min(n, limit)
	if n > limit && limit > 0 {
		rows++
	}
	return rows
}

// queuePanelView renders the queue badge and the first queued messages,
// one line each, for the top of the composer panel.
func (m *Model) queuePanelView(width int) string {
	if !m.queue.queueWaiting() {
		return ""
	}
	n := len(m.queue.items)
	var header string
	switch {
	case m.queue.paused:
		header = m.theme.BadgeWarn.Render(fmt.Sprintf("⏸ %d queued · paused", n)) +
			m.theme.HelpFooter.Render(" · enter resumes · /queue edit|drop <n>")
	case m.queue.editing:
		header = m.theme.Badge.Render(fmt.Sprintf("✎ editing queued #%d · %d more", m.queue.editSlot+1, n)) +
			m.theme.HelpFooter.Render(" · enter puts it back · ctrl+u then enter drops it")
	default:
		header = m.theme.Badge.Render(fmt.Sprintf("⧗ %d queued", n)) +
			m.theme.HelpFooter.Render(" · ↑ edit last · /queue edit|drop <n> · /queue clear")
	}
	lines := []string{ansi.Truncate(header, width, "…")}
	limit := queuePanelItemLimit(m.height, m.layout.compact)
	for i := 0; i < min(n, limit); i++ {
		item := m.queue.items[i]
		text := strings.Join(strings.Fields(terminaltext.Sanitize(item.text)), " ")
		if text == "" {
			text = "(image only)"
		} else if len(item.images) > 0 {
			text = fmt.Sprintf("[%d image(s)] %s", len(item.images), text)
		}
		line := fmt.Sprintf("  %d. %s", i+1, text)
		lines = append(lines, m.theme.SystemNote.Render(ansi.Truncate(line, width, "…")))
	}
	if n > limit && limit > 0 {
		lines = append(lines, m.theme.HelpFooter.Render(fmt.Sprintf("  +%d more · /queue to see all", n-limit)))
	}
	return strings.Join(lines, "\n")
}
