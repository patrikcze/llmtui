package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/patrikcze/llmtui/internal/agent"
)

func agentCardTranscript(m *Model) string {
	return ansi.Strip(m.renderSettledTranscript())
}

func TestAgentRunCardShowsCriteriaAndTicksThemAcrossPasses(t *testing.T) {
	var afterFirstPass string
	var m *Model
	failedWithFirstMet := strings.Replace(
		verifierJSON("failed", "the report is missing", "produce the report", true, true),
		`"criteria":[]`, `"criteria":[{"id":"c1","status":"satisfied"}]`, 1)
	m, _ = configureAgentTestModel(t,
		agentScriptStep{text: "Gathered the data."},
		agentScriptStep{text: failedWithFirstMet},
		agentScriptStep{text: "Produced the report.", before: func() { afterFirstPass = agentCardTranscript(m) }},
		agentScriptStep{text: verifierJSONSatisfying("both done", "c1", "c2")},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("gather data and produce a report", nil))

	run := m.agentLoop.run
	if run.Status != agent.DecisionDone || run.Cycle != 2 {
		t.Fatalf("run status = %s cycle = %d (%s)", run.Status, run.Cycle, run.StopReason)
	}

	for _, want := range []string{
		"Goal: gather data and produce a report",
		"Done when:",
		"✓ gather data",
		"○ produce report",
		"pass 2 of",
		"1 of 2 criteria met",
	} {
		if !strings.Contains(afterFirstPass, want) {
			t.Errorf("card after pass 1 missing %q:\n%s", want, afterFirstPass)
		}
	}

	final := agentCardTranscript(m)
	for _, want := range []string{
		"done · verified after 2 passes · 2 of 2 criteria met",
		"✓ gather data",
		"✓ produce report",
		"Stopped because every criterion was verified.",
	} {
		if !strings.Contains(final, want) {
			t.Errorf("final card missing %q:\n%s", want, final)
		}
	}
	if strings.Count(final, "Done when:") != 1 {
		t.Fatalf("want exactly one card, got:\n%s", final)
	}
	// The card sits right after the request that started the run, before
	// the executor's first reply.
	request := strings.Index(final, "gather data and produce a report")
	card := strings.Index(final, "Done when:")
	reply := strings.Index(final, "Gathered the data.")
	if request < 0 || card < 0 || reply < 0 || request >= card || card >= reply {
		t.Fatalf("card is not anchored after the request (request=%d card=%d reply=%d):\n%s", request, card, reply, final)
	}
}

func TestAgentRunCardNeverEntersTheSession(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Done."},
		agentScriptStep{text: verifierJSONSatisfying("done", "c1")},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))
	if len(m.agentCards) != 1 {
		t.Fatalf("cards = %d, want 1", len(m.agentCards))
	}
	for _, msg := range m.session.Messages {
		if strings.Contains(msg.Content, "Done when") || strings.Contains(msg.Content, "criteria met") {
			t.Fatalf("card text leaked into the session: %+v", msg)
		}
	}
}

func TestAgentRunCardStopSummaryExplainsWhyTheRunStopped(t *testing.T) {
	for _, tc := range []struct {
		status agent.Decision
		reason string
		want   string
	}{
		{agent.DecisionDone, "", "Stopped because every criterion was verified."},
		{agent.DecisionCancelled, "cancelled by /agent cancel", "Stopped because the run was cancelled."},
		{agent.DecisionBudgetExhausted, "agent tool-call budget exhausted (maximum 4)", "Stopped because a budget ran out: agent tool-call budget exhausted (maximum 4)"},
		{agent.DecisionNoProgress, "same calls twice", "Stopped because the run made no progress: same calls twice"},
		{agent.DecisionVerificationUnavailable, "timeout", "Stopped because the verifier was unavailable: timeout"},
		{agent.DecisionNeedsUserInput, "which file?", "Paused for your input: which file?"},
		{agent.DecisionFailed, "boom", "Failed: boom"},
		{agent.DecisionParked, "", "Parked"},
		{agent.DecisionRunning, "", ""},
		{agent.DecisionRetry, "", ""},
	} {
		t.Run(string(tc.status), func(t *testing.T) {
			got := agentRunCardStopSummary(agentRunCard{Status: tc.status, StopReason: tc.reason})
			if got != tc.want {
				t.Fatalf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentRunCardCancelledRunShowsFinalSummary(t *testing.T) {
	m, _ := configureAgentTestModel(t)
	run, err := agent.NewRun("run-cancel", "tidy the notes", m.agentLimits(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := run.BeginContract(time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := run.CompleteContract([]string{"notes are tidy"}, time.Now()); err != nil {
		t.Fatal(err)
	}
	m.agentLoop.run = run
	m.syncAgentDebug()
	if got := agentCardTranscript(m); !strings.Contains(got, "○ notes are tidy") || !strings.Contains(got, "0 of 1 criteria met") {
		t.Fatalf("pinned card:\n%s", got)
	}

	m.cancelVerifiedRun("cancelled by /agent cancel")
	m.endAgentRun()
	got := agentCardTranscript(m)
	if !strings.Contains(got, "stopped · 0 of 1 criteria met") || !strings.Contains(got, "Stopped because the run was cancelled.") {
		t.Fatalf("cancelled card:\n%s", got)
	}
}

func TestAgentRunCardSanitizesAndFlattensCriterionText(t *testing.T) {
	m, _ := configureAgentTestModel(t)
	card := agentRunCard{
		RunID: "r", Goal: "line one\nline two \x1b[31mred\x1b[0m", Status: agent.DecisionRunning, Stage: agent.StageExecutor, Cycle: 1, MaxCycles: 3,
		Criteria: []agentRunCardCriterion{
			{ID: "c1", Text: "multi\nline \x1b]0;title\x07criterion", Status: agent.CriterionPending},
			{ID: "c2", Text: "skipped", Status: agent.CriterionNotApplicable},
			{ID: "c3", Text: "broken", Status: agent.CriterionFailed},
		},
	}
	out := m.renderAgentRunCard(card)
	if strings.Contains(out, "\x1b]0;") || strings.Contains(out, "\x07") {
		t.Fatalf("untrusted escape sequence reached the card: %q", out)
	}
	plain := ansi.Strip(out)
	for _, want := range []string{
		"Goal: line one line two",
		"pass 1 of 3 · working · 0 of 2 criteria met",
		"– skipped (not applicable)",
		"✗ broken",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("card missing %q:\n%s", want, plain)
		}
	}
}

func TestAgentRunCardsResetWithTheConversation(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Done."},
		agentScriptStep{text: verifierJSONSatisfying("done", "c1")},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("make the bounded change", nil))
	if len(m.agentCards) != 1 {
		t.Fatalf("cards = %d, want 1", len(m.agentCards))
	}
	before := m.agentCardRevision
	runCommand(m, "/clear")
	if len(m.agentCards) != 0 || m.agentCardRevision == before {
		t.Fatalf("cards after /clear = %d (revision %d -> %d)", len(m.agentCards), before, m.agentCardRevision)
	}
	if strings.Contains(agentCardTranscript(m), "Done when:") {
		t.Fatal("card still rendered after /clear")
	}
}

func TestAgentStatusListsCriteria(t *testing.T) {
	m, _ := configureAgentTestModel(t,
		agentScriptStep{text: "Gathered and reported."},
		agentScriptStep{text: verifierJSONSatisfying("done", "c1", "c2")},
	)
	driveAgentCommands(t, m, m.startVerifiedRun("gather data and produce a report", nil))
	_ = cmdAgent(m, "status")
	if !m.overlayOpen {
		t.Fatal("/agent status did not open the criteria dialog")
	}
	got := ansi.Strip(m.agentCriteriaStatusOverlay())
	for _, want := range []string{"Done when:", "✓ gather data", "✓ produce report", "Stopped because every criterion was verified."} {
		if !strings.Contains(got, want) {
			t.Errorf("/agent status missing %q:\n%s", want, got)
		}
	}
}

func TestAgentStatusWithoutRunKeepsNotice(t *testing.T) {
	m, _ := configureAgentTestModel(t)
	m.agentLoop.run = nil
	_ = cmdAgent(m, "status")
	if m.overlayOpen || !strings.Contains(m.notice, "no run") {
		t.Fatalf("overlay=%t notice=%q", m.overlayOpen, m.notice)
	}
}
