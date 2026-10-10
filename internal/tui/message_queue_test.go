package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/tools"
)

// queueBusyModel returns a model with a reply streaming for "first".
func queueBusyModel(t *testing.T) *Model {
	t.Helper()
	m := newTestModel(t)
	m.input.SetValue("first")
	m.Update(kEnter())
	if !m.busy() {
		t.Fatal("model should be busy after the first send")
	}
	return m
}

// queueType replaces the composer text and presses Enter.
func queueType(m *Model, text string) {
	m.input.SetValue(text)
	m.Update(kEnter())
}

func queueTexts(m *Model) []string {
	out := make([]string, len(m.queue.items))
	for i, item := range m.queue.items {
		out[i] = item.text
	}
	return out
}

func userTexts(m *Model) []string {
	var out []string
	for _, msg := range m.session.Messages {
		if msg.Role == provider.RoleUser {
			out = append(out, msg.Content)
		}
	}
	return out
}

// finishReply ends the streaming reply the way a provider's Done event does.
func finishReply(m *Model) {
	m.Update(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{Type: provider.EventDelta, Delta: "ok"}})
	m.Update(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{Type: provider.EventDone}})
}

func TestQueueHoldsMessagesWhileBusyAndSendsThemInOrder(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	queueType(m, "third")

	if got := queueTexts(m); strings.Join(got, ",") != "second,third" {
		t.Fatalf("queue = %v, want [second third]", got)
	}
	if m.input.Value() != "" {
		t.Fatalf("composer = %q, want it cleared after queueing", m.input.Value())
	}
	if got := userTexts(m); len(got) != 1 {
		t.Fatalf("user messages = %v, want only the first one sent", got)
	}

	finishReply(m)
	if m.busy() {
		t.Fatal("reply should have finished")
	}
	if !m.queue.drainScheduled {
		t.Fatal("finishing the reply should schedule the next queued message")
	}
	m.Update(queueDrainMsg{})
	if !m.busy() {
		t.Fatal("draining should send the next queued message")
	}
	if got := userTexts(m); strings.Join(got, ",") != "first,second" {
		t.Fatalf("user messages = %v, want first then second", got)
	}
	if got := queueTexts(m); strings.Join(got, ",") != "third" {
		t.Fatalf("queue = %v, want [third]", got)
	}

	finishReply(m)
	m.Update(queueDrainMsg{})
	if got := userTexts(m); strings.Join(got, ",") != "first,second,third" {
		t.Fatalf("user messages = %v, want all three in order", got)
	}
	if len(m.queue.items) != 0 {
		t.Fatalf("queue = %v, want empty", queueTexts(m))
	}
}

func TestQueueDrainKeepsTheComposerDraft(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	m.input.SetValue("half-typed draft")
	finishReply(m)
	m.Update(queueDrainMsg{})
	if got := m.input.Value(); got != "half-typed draft" {
		t.Fatalf("composer = %q, want the draft untouched by the queued send", got)
	}
	if got := userTexts(m); strings.Join(got, ",") != "first,second" {
		t.Fatalf("user messages = %v", got)
	}
}

func TestQueueUpEditsLastQueuedMessageAndEnterPutsItBack(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	queueType(m, "third")

	m.Update(kUp())
	if got := m.input.Value(); got != "third" {
		t.Fatalf("composer = %q, want the last queued message pulled back", got)
	}
	if got := queueTexts(m); strings.Join(got, ",") != "second" {
		t.Fatalf("queue while editing = %v, want [second]", got)
	}
	m.input.SetValue("third, edited")
	m.Update(kEnter())
	if got := queueTexts(m); strings.Join(got, ",") != "second,third, edited" {
		t.Fatalf("queue = %v, want the edited message back in its slot", got)
	}
	if m.queue.editing {
		t.Fatal("editing should end once the message is re-queued")
	}
}

func TestQueueEditKeepsPositionInLine(t *testing.T) {
	m := queueBusyModel(t)
	for _, s := range []string{"a", "b", "c"} {
		queueType(m, s)
	}
	runCommand(m, "/queue edit 1")
	if got := m.input.Value(); got != "a" {
		t.Fatalf("composer = %q, want queued #1", got)
	}
	m.input.SetValue("a2")
	m.Update(kEnter())
	if got := queueTexts(m); strings.Join(got, ",") != "a2,b,c" {
		t.Fatalf("queue = %v, want [a2 b c]", got)
	}
}

func TestQueueWaitsWhileItsNextMessageIsBeingEdited(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "a")
	queueType(m, "b")
	runCommand(m, "/queue edit 1")
	finishReply(m)
	if m.queue.drainScheduled {
		t.Fatal("the queue must wait while the next message in line is open for editing")
	}
	m.Update(kEnter()) // put "a" back; it is next and the model is idle
	if !m.queue.drainScheduled {
		t.Fatal("re-queueing the edited message should let the queue continue")
	}
	m.Update(queueDrainMsg{})
	if got := userTexts(m); strings.Join(got, ",") != "first,a" {
		t.Fatalf("user messages = %v, want the edited message sent next", got)
	}
}

func TestQueueClearingAnEditedMessageDropsIt(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "a")
	queueType(m, "b")
	m.Update(kUp()) // pull back "b"
	m.Update(kCtrl('u'))
	if m.queue.editing {
		t.Fatal("clearing the composer should abandon the edit")
	}
	if got := queueTexts(m); strings.Join(got, ",") != "a" {
		t.Fatalf("queue = %v, want [a]", got)
	}
}

func TestQueueDropAndClearCommands(t *testing.T) {
	m := queueBusyModel(t)
	for _, s := range []string{"a", "b", "c"} {
		queueType(m, s)
	}
	runCommand(m, "/queue drop 2")
	if got := queueTexts(m); strings.Join(got, ",") != "a,c" {
		t.Fatalf("queue after drop = %v, want [a c]", got)
	}
	runCommand(m, "/queue drop 9")
	if !strings.Contains(m.errText, "usage: /queue drop <1-2>") {
		t.Fatalf("errText = %q, want a range hint", m.errText)
	}
	runCommand(m, "/queue clear")
	if len(m.queue.items) != 0 {
		t.Fatalf("queue after clear = %v", queueTexts(m))
	}
	finishReply(m)
	if m.queue.drainScheduled {
		t.Fatal("an empty queue must not schedule a send")
	}
}

func TestQueueEscPausesQueueAndEnterResumes(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	if m.busy() {
		t.Fatal("esc should stop the reply")
	}
	if !m.queue.paused || m.queue.drainScheduled {
		t.Fatalf("paused=%v drainScheduled=%v; esc must pause the queue, not send the next message",
			m.queue.paused, m.queue.drainScheduled)
	}
	if !strings.Contains(m.render(), "paused") {
		t.Fatal("the queue panel should say the queue is paused")
	}
	m.Update(kEnter())
	if m.queue.paused || !m.queue.drainScheduled {
		t.Fatalf("paused=%v drainScheduled=%v; enter on an empty composer should resume",
			m.queue.paused, m.queue.drainScheduled)
	}
}

func TestQueueNewMessageNeverOvertakesPausedQueue(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	queueType(m, "third")
	if got := userTexts(m); strings.Join(got, ",") != "first" {
		t.Fatalf("user messages = %v, want the new message queued behind the paused one", got)
	}
	if got := queueTexts(m); strings.Join(got, ",") != "second,third" {
		t.Fatalf("queue = %v, want [second third]", got)
	}
	if m.queue.paused {
		t.Fatal("submitting a new message should resume the queue")
	}
}

func TestQueueFailedTurnPausesQueue(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	m.Update(streamEventMsg{gen: m.streamGen, ok: true, event: provider.ChatEvent{Type: provider.EventError, Err: fmt.Errorf("backend down")}})
	if m.busy() {
		t.Fatal("the error should end the turn")
	}
	if !m.queue.paused || m.queue.drainScheduled {
		t.Fatalf("paused=%v drainScheduled=%v; a failed turn must pause the queue",
			m.queue.paused, m.queue.drainScheduled)
	}
}

func TestQueueNeverAnswersAPendingApproval(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	finishReply(m)
	m.queue.drainScheduled = false
	m.pendingCalls = []tools.Call{{Tool: tools.ToolRunCommand, Body: "ls"}}
	if m.queueIdle() {
		t.Fatal("a pending tool approval must block the queue")
	}
	m.Update(queueDrainMsg{})
	if got := userTexts(m); strings.Join(got, ",") != "first" {
		t.Fatalf("user messages = %v, want nothing sent while an approval is pending", got)
	}
	// The approval owns the keyboard: Up must not pull a queued message.
	m.Update(kUp())
	if m.queue.editing || len(m.queue.items) != 1 {
		t.Fatal("Up behind a pending approval must not touch the queue")
	}
}

func TestQueueIsCappedAndShowsBadge(t *testing.T) {
	m := queueBusyModel(t)
	m.resize(100, 40)
	for i := range maxQueuedMessages {
		queueType(m, fmt.Sprintf("msg %d", i+1))
	}
	queueType(m, "one too many")
	if len(m.queue.items) != maxQueuedMessages {
		t.Fatalf("queue len = %d, want cap %d", len(m.queue.items), maxQueuedMessages)
	}
	if m.input.Value() != "one too many" {
		t.Fatal("a rejected message must stay in the composer")
	}
	view := m.render()
	for _, want := range []string{"10 queued", "1. msg 1", "+7 more"} {
		if !strings.Contains(view, want) {
			t.Fatalf("view missing %q", want)
		}
	}
}

func TestQueueClearedWithConversation(t *testing.T) {
	m := queueBusyModel(t)
	queueType(m, "second")
	m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	runCommand(m, "/clear")
	if len(m.queue.items) != 0 || m.queue.paused {
		t.Fatal("/clear should drop messages queued for the old conversation")
	}
}
