package tui

import (
	"fmt"
	"slices"
	"strings"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// An agent run card is the transcript's view of one verified run's task
// contract: the goal, the pinned "done when" criteria with their current
// status, which pass the run is on, and why it stopped. It mirrors the iOS
// app's AgentRunCard. Cards are display-only: they live beside the session,
// never in it, so they never reach a prompt, the response cache, or a saved
// conversation. Each card is a snapshot taken at the run's sync points
// (syncAgentDebug and endAgentRun) rather than a pointer to the live run, so
// the settled-transcript cache can key on agentCardRevision alone.

const (
	agentCardGoalMaxBytes      = 240
	agentCardCriterionMaxBytes = 240
	agentCardReasonMaxBytes    = 320
)

type agentRunCardCriterion struct {
	ID     string
	Text   string
	Status agent.CriterionStatus
}

type agentRunCard struct {
	RunID string
	// Anchor is the session index of the run's first message (the user's
	// request); the card renders right after it.
	Anchor     int
	Goal       string
	Criteria   []agentRunCardCriterion
	Cycle      int
	MaxCycles  int
	Stage      agent.Stage
	Status     agent.Decision
	StopReason string
}

func (c agentRunCard) equal(o agentRunCard) bool {
	return c.RunID == o.RunID && c.Anchor == o.Anchor && c.Goal == o.Goal &&
		c.Cycle == o.Cycle && c.MaxCycles == o.MaxCycles && c.Stage == o.Stage &&
		c.Status == o.Status && c.StopReason == o.StopReason && slices.Equal(c.Criteria, o.Criteria)
}

// syncAgentRunCard creates or refreshes the card for the current run once
// its criteria are pinned. It is cheap and idempotent; the transcript cache
// is invalidated only when the snapshot actually changed.
func (m *Model) syncAgentRunCard() {
	if m.agentLoop == nil || m.agentLoop.run == nil || !m.agentLoop.run.HasCriteria() {
		return
	}
	run := m.agentLoop.run
	card := agentRunCard{
		RunID: run.ID, Anchor: m.agentLoop.historyStart, Goal: run.Request,
		Cycle: run.Cycle, MaxCycles: run.Limits.MaxCycles,
		Stage: run.Stage, Status: run.Status, StopReason: run.StopReason,
		Criteria: make([]agentRunCardCriterion, 0, len(run.Criteria)),
	}
	for _, criterion := range run.Criteria {
		card.Criteria = append(card.Criteria, agentRunCardCriterion{ID: criterion.ID, Text: criterion.Text, Status: criterion.Status})
	}
	for i := range m.agentCards {
		if m.agentCards[i].RunID != run.ID {
			continue
		}
		// A resumed run keeps the anchor of the turn that started it.
		card.Anchor = m.agentCards[i].Anchor
		if !m.agentCards[i].equal(card) {
			m.agentCards[i] = card
			m.agentCardRevision++
		}
		return
	}
	m.agentCards = append(m.agentCards, card)
	m.agentCardRevision++
}

// resetAgentRunCards drops every card when the conversation they were
// anchored to is cleared or replaced.
func (m *Model) resetAgentRunCards() {
	if len(m.agentCards) == 0 {
		return
	}
	m.agentCards = nil
	m.agentCardRevision++
}

// writeAgentRunCards renders the cards anchored to session message index
// anchor. tail renders every card whose anchor is at or past the end of the
// session (a run whose request has not been appended yet).
func (m *Model) writeAgentRunCards(b *strings.Builder, anchor int, tail bool) {
	for _, card := range m.agentCards {
		if card.Anchor == anchor || (tail && card.Anchor > anchor) {
			b.WriteString(m.renderAgentRunCard(card))
			b.WriteString("\n\n")
		}
	}
}

func (m *Model) renderAgentRunCard(card agentRunCard) string {
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("◆ agent"))
	b.WriteString(" ")
	b.WriteString(m.theme.StatusValue.Render(agentRunCardStatus(card)))
	b.WriteString("\n")
	if goal := agentCardLine(card.Goal, agentCardGoalMaxBytes); goal != "" {
		b.WriteString(m.theme.SystemNote.Render("  Goal: " + goal))
		b.WriteString("\n")
	}
	b.WriteString(m.theme.SystemNote.Render("  Done when:"))
	for _, criterion := range card.Criteria {
		b.WriteString("\n    ")
		b.WriteString(m.renderAgentCardCriterion(criterion))
	}
	if summary := agentRunCardStopSummary(card); summary != "" {
		b.WriteString("\n")
		style := m.theme.SystemNote
		switch card.Status {
		case agent.DecisionDone:
			style = style.Foreground(m.theme.Good)
		case agent.DecisionCancelled, agent.DecisionNeedsUserInput:
		default:
			style = style.Foreground(m.theme.Warning)
		}
		b.WriteString(style.Render("  " + summary))
	}
	return b.String()
}

func (m *Model) renderAgentCardCriterion(c agentRunCardCriterion) string {
	glyph, style := "○", m.theme.SystemNote
	switch c.Status {
	case agent.CriterionSatisfied:
		glyph, style = "✓", style.Foreground(m.theme.Good)
	case agent.CriterionFailed:
		glyph, style = "✗", style.Foreground(m.theme.Bad)
	case agent.CriterionNotApplicable:
		glyph = "–"
	}
	text := agentCardLine(c.Text, agentCardCriterionMaxBytes)
	if c.Status == agent.CriterionNotApplicable {
		text += " (not applicable)"
	}
	return style.Render(glyph+" ") + m.theme.StatusValue.Render(text)
}

// agentRunCardStatus is the card's header line: pass N of M plus what the
// run is doing, or how it ended.
func agentRunCardStatus(card agentRunCard) string {
	met, total := agentRunCardMet(card)
	progress := fmt.Sprintf("%d of %d criteria met", met, total)
	pass := ""
	if card.Cycle > 0 {
		pass = fmt.Sprintf("pass %d of %d · ", card.Cycle, card.MaxCycles)
	}
	switch card.Status {
	case agent.DecisionRunning, agent.DecisionContinue, agent.DecisionRetry:
	case agent.DecisionDone:
		if card.Cycle <= 1 {
			return "done · verified · " + progress
		}
		return fmt.Sprintf("done · verified after %d passes · %s", card.Cycle, progress)
	case agent.DecisionNeedsUserInput:
		return pass + "waiting for you · " + progress
	default:
		return pass + "stopped · " + progress
	}
	var doing string
	switch card.Stage {
	case agent.StageContract:
		doing = "agreeing on criteria"
	case agent.StageVerifier:
		doing = "checking the result"
	case agent.StageMemoryWrite, agent.StageStopCheck:
		doing = "deciding the next step"
	default:
		doing = "working"
	}
	return pass + doing + " · " + progress
}

func agentRunCardMet(card agentRunCard) (met, total int) {
	for _, c := range card.Criteria {
		switch c.Status {
		case agent.CriterionSatisfied:
			met++
			total++
		case agent.CriterionNotApplicable:
		default:
			total++
		}
	}
	return met, total
}

// agentRunCardStopSummary explains, in one line, why a finished run stopped.
// It is empty while the run is still going.
func agentRunCardStopSummary(card agentRunCard) string {
	reason := agentCardLine(card.StopReason, agentCardReasonMaxBytes)
	with := func(lead string) string {
		if reason == "" {
			return lead
		}
		return lead + ": " + reason
	}
	switch card.Status {
	case agent.DecisionRunning, agent.DecisionContinue, agent.DecisionRetry:
		return ""
	case agent.DecisionDone:
		return "Stopped because every criterion was verified."
	case agent.DecisionNeedsUserInput:
		return with("Paused for your input")
	case agent.DecisionCancelled:
		return "Stopped because the run was cancelled."
	case agent.DecisionBudgetExhausted:
		return with("Stopped because a budget ran out")
	case agent.DecisionNoProgress:
		return with("Stopped because the run made no progress")
	case agent.DecisionVerificationUnavailable:
		return with("Stopped because the verifier was unavailable")
	case agent.DecisionParked:
		return with("Parked")
	case agent.DecisionEscalated:
		return with("Escalated")
	case agent.DecisionFailed:
		return with("Failed")
	default:
		return with("Stopped (" + string(card.Status) + ")")
	}
}

// agentCardLine flattens model- or user-supplied text to one sanitized,
// bounded line for the card.
func agentCardLine(value string, maxBytes int) string {
	value = strings.Join(strings.Fields(terminaltext.Sanitize(value)), " ")
	return truncateAgentText(value, maxBytes)
}

// agentCriteriaStatusOverlay is the /agent status dialog: the run line plus
// the same card the transcript shows.
func (m *Model) agentCriteriaStatusOverlay() string {
	var b strings.Builder
	if m.agentLoop == nil || m.agentLoop.run == nil {
		b.WriteString(m.theme.SystemNote.Render("agent mode " + onOff(m.agentOn) + " · no run"))
		return m.overlayFooter(&b)
	}
	run := m.agentLoop.run
	m.kv(&b, "agent mode", onOff(m.agentOn))
	m.kv(&b, "run", shortRunID(run.ID))
	m.kv(&b, "stage", fmt.Sprintf("%s / %s", run.Stage, run.Status))
	b.WriteString("\n")
	if !run.HasCriteria() {
		b.WriteString(m.theme.SystemNote.Render("criteria are not pinned yet"))
		return m.overlayFooter(&b)
	}
	m.syncAgentRunCard()
	for _, card := range m.agentCards {
		if card.RunID == run.ID {
			b.WriteString(m.renderAgentRunCard(card))
			b.WriteString("\n")
		}
	}
	return m.overlayFooter(&b)
}
