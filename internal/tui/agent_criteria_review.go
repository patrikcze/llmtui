package tui

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/agentverify"
	"github.com/patrikcze/llmtui/internal/provider"
	"github.com/patrikcze/llmtui/internal/terminaltext"
)

// agentCriteriaReviewReason is the run's StopReason while the person reviews
// the proposed criteria. It is also what a resumed run shows: the review
// itself is process-local, so after a restart the next message is recorded as
// ordinary contract input and the contract is proposed again.
const agentCriteriaReviewReason = "review the proposed acceptance criteria before the agent starts"

// agentCriteriaReview holds a proposed task contract while the person at the
// keyboard accepts or edits it (agent.review_criteria). Nothing is pinned
// until they accept, so the controller-owned criteria are still pinned
// exactly once, by the ordinary CompleteContractWithAssessments path.
type agentCriteriaReview struct {
	runID       string
	criteria    []string
	assessments map[int]agent.CriterionAssessmentSpec
}

var (
	criteriaReviewDrop    = regexp.MustCompile(`^-\s*(\d+)$`)
	criteriaReviewReplace = regexp.MustCompile(`^(\d+)\s*[:.)]\s*(.+)$`)
)

// agentCriteriaReviewPending reports whether the current run is paused on a
// criteria review. A review left over from a cancelled or replaced run is
// ignored rather than cleared at every exit path.
func (m *Model) agentCriteriaReviewPending() bool {
	if m.agentLoop == nil || m.agentLoop.criteriaReview == nil || m.agentLoop.run == nil {
		return false
	}
	run := m.agentLoop.run
	return m.agentLoop.criteriaReview.runID == run.ID &&
		run.Status == agent.DecisionNeedsUserInput && run.Stage == agent.StageContract && !run.HasCriteria()
}

// beginAgentCriteriaReview pauses the run before the executor starts so the
// person can accept, edit or add criteria. The pause uses the same
// needs-user-input state as a contract question, so review time does not
// count against max_elapsed.
func (m *Model) beginAgentCriteriaReview(contract agentverify.Contract) tea.Cmd {
	run := m.agentLoop.run
	if err := run.WaitForContractInput(agentCriteriaReviewReason, time.Now()); err != nil {
		m.failVerifiedRun(err)
		m.endAgentRun()
		return m.persistAgentRun()
	}
	m.agentLoop.criteriaReview = &agentCriteriaReview{
		runID:       run.ID,
		criteria:    append([]string(nil), contract.Criteria...),
		assessments: contract.Assessments,
	}
	m.errText = ""
	m.notice = fmt.Sprintf("agent %s · review the criteria, then press enter to start", shortRunID(run.ID))
	m.syncAgentDebug()
	m.endAgentRun()
	m.refreshViewport()
	return m.persistAgentRun()
}

// handleAgentCriteriaReviewInput applies one composer submission to a pending
// review. An empty line or "ok" accepts; "+ text" adds, "-N" drops and
// "N: text" replaces a criterion; any other text asks the contract stage to
// propose new criteria with that feedback.
func (m *Model) handleAgentCriteriaReviewInput(input string, images []provider.Image) tea.Cmd {
	review := m.agentLoop.criteriaReview
	if len(images) > 0 {
		m.agentLoop.initialImages = append(m.agentLoop.initialImages, images...)
	}
	text := strings.TrimSpace(input)
	switch strings.ToLower(text) {
	case "", "y", "yes", "ok", "accept", "start", "go":
		return m.acceptAgentCriteriaReview()
	}
	switch {
	case strings.HasPrefix(text, "+"):
		criterion := strings.TrimSpace(strings.TrimPrefix(text, "+"))
		if criterion == "" {
			return m.criteriaReviewError("type the criterion after +")
		}
		if len(review.criteria) >= agent.MaxCriteria {
			return m.criteriaReviewError(fmt.Sprintf("a run can have at most %d criteria", agent.MaxCriteria))
		}
		review.criteria = append(review.criteria, criterion)
		return m.criteriaReviewEdited(fmt.Sprintf("added criterion %d", len(review.criteria)))
	case criteriaReviewDrop.MatchString(text):
		n, ok := review.criterionNumber(criteriaReviewDrop.FindStringSubmatch(text)[1])
		if !ok {
			return m.criteriaReviewError(fmt.Sprintf("there is no criterion %s", strings.TrimSpace(strings.TrimPrefix(text, "-"))))
		}
		if len(review.criteria) == 1 {
			return m.criteriaReviewError("a run needs at least one criterion")
		}
		review.criteria = append(review.criteria[:n-1], review.criteria[n:]...)
		return m.criteriaReviewEdited(fmt.Sprintf("dropped criterion %d", n))
	case criteriaReviewReplace.MatchString(text):
		match := criteriaReviewReplace.FindStringSubmatch(text)
		n, ok := review.criterionNumber(match[1])
		if !ok {
			return m.criteriaReviewError("there is no criterion " + match[1])
		}
		review.criteria[n-1] = strings.TrimSpace(match[2])
		return m.criteriaReviewEdited(fmt.Sprintf("replaced criterion %d", n))
	}
	return m.reviseAgentCriteria(text)
}

func (r *agentCriteriaReview) criterionNumber(value string) (int, bool) {
	n, err := strconv.Atoi(value)
	return n, err == nil && n >= 1 && n <= len(r.criteria)
}

// criteriaReviewEdited drops the optional assessment metadata, which was
// validated against the proposed list and is keyed by its indexes.
func (m *Model) criteriaReviewEdited(notice string) tea.Cmd {
	m.agentLoop.criteriaReview.assessments = nil
	m.errText = ""
	m.notice = fmt.Sprintf("agent %s · %s · press enter to start", shortRunID(m.agentLoop.run.ID), notice)
	m.refreshViewport()
	return nil
}

func (m *Model) criteriaReviewError(text string) tea.Cmd {
	m.errText = "agent criteria: " + text
	m.refreshViewport()
	return nil
}

// acceptAgentCriteriaReview pins the reviewed criteria and starts the first
// executor cycle, exactly as an unreviewed contract would.
func (m *Model) acceptAgentCriteriaReview() tea.Cmd {
	review := m.agentLoop.criteriaReview
	run := m.agentLoop.run
	now := time.Now()
	if err := run.Resume("", now); err != nil {
		m.errText = "resume agent run: " + err.Error()
		m.refreshViewport()
		return nil
	}
	m.agentLoop.criteriaReview = nil
	m.resetAgentContext()
	if err := run.BeginContract(now); err != nil {
		m.failVerifiedRun(err)
		m.errText = "agent task contract: " + err.Error()
		m.endAgentRun()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	if err := run.CompleteContractWithAssessments(review.criteria, review.assessments, now); err != nil {
		m.failVerifiedRun(err)
		m.errText = "agent task contract: " + err.Error()
		m.endAgentRun()
		m.refreshViewport()
		return m.persistAgentRun()
	}
	run.NoteDiagnostic(now, "criteria_reviewed", fmt.Sprintf("user accepted %d acceptance criteria", len(run.Criteria)))
	m.errText = ""
	m.syncAgentDebug()
	return tea.Batch(m.persistAgentRun(), m.startInitialAgentCycle(run.Request, m.agentLoop.initialImages))
}

// reviseAgentCriteria sends free-text feedback back to the contract stage.
// The contract request sees no conversation, so the feedback carries the
// list it is about; any earlier contract answer is kept in front of it.
func (m *Model) reviseAgentCriteria(feedback string) tea.Cmd {
	review := m.agentLoop.criteriaReview
	run := m.agentLoop.run
	if err := run.Resume("", time.Now()); err != nil {
		m.errText = "resume agent run: " + err.Error()
		m.refreshViewport()
		return nil
	}
	m.agentLoop.criteriaReview = nil
	var b strings.Builder
	if prior := strings.TrimSpace(run.ContractInput); prior != "" {
		b.WriteString(prior)
		b.WriteString("\n\n")
	}
	b.WriteString("The user reviewed these proposed acceptance criteria:\n")
	for i, criterion := range review.criteria {
		fmt.Fprintf(&b, "%d. %s\n", i+1, criterion)
	}
	b.WriteString("and asked for this change: ")
	b.WriteString(feedback)
	run.SetContractInput(b.String(), time.Now())
	m.resetAgentContext()
	m.bypassCache = true
	return tea.Batch(m.persistAgentRun(), m.startAgentContract())
}

// renderAgentCriteriaReview draws the pending review above the composer:
// the goal (the request as typed), the numbered criteria, and the edit keys.
func (m *Model) renderAgentCriteriaReview() string {
	review := m.agentLoop.criteriaReview
	var b strings.Builder
	b.WriteString(m.theme.Badge.Render("? review agent criteria"))
	b.WriteString("\n")
	goal := truncateAgentText(strings.Join(strings.Fields(m.agentLoop.run.Request), " "), 240)
	b.WriteString(m.theme.UserLabel.Render(terminaltext.Sanitize("Goal: " + goal)))
	b.WriteString("\n")
	for i, criterion := range review.criteria {
		b.WriteString(terminaltext.Sanitize(fmt.Sprintf("  %d. %s", i+1, criterion)))
		b.WriteString("\n")
	}
	b.WriteString(m.theme.HelpFooter.Render("enter accepts · + text adds · -N drops · N: text replaces · other text asks for new criteria · /agent cancel stops"))
	b.WriteString("\n\n")
	return b.String()
}
