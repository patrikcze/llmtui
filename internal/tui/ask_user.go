package tui

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
	"github.com/patrikcze/llmtui/internal/tools"
)

const maxAskUserAnswerRunes = 4096

func (m *Model) handleAskUserBatch(calls []tools.Call) (tea.Cmd, bool) {
	askCount := 0
	for _, call := range calls {
		if call.Tool == tools.ToolAskUser {
			askCount++
		}
	}
	if askCount == 0 {
		return nil, false
	}
	if len(calls) != 1 || askCount != 1 {
		err := errors.New("ask_user is a control-flow barrier and must be the only call in its batch; no calls in this batch were executed")
		results := make([]tools.Result, len(calls))
		for index, call := range calls {
			results[index] = tools.Result{Call: call, Err: err}
		}
		m.toolErr += len(results)
		m.recordAgentToolResultsCount(results, false, 0)
		return m.sendToolResults(results), true
	}

	call := calls[0]
	if call.InputErr == "" {
		if err := tools.ValidateAskUserCall(&call); err != nil {
			call.InputErr = err.Error()
		}
	}
	if call.InputErr != "" {
		result := tools.Result{Call: call, Err: fmt.Errorf("invalid arguments for ask_user: %s", call.InputErr)}
		m.toolErr++
		m.recordAgentToolResultsCount([]tools.Result{result}, false, 0)
		return m.sendToolResults([]tools.Result{result}), true
	}
	return m.pauseForAskUser(call), true
}

func (m *Model) pauseForAskUser(call tools.Call) tea.Cmd {
	m.overlayOpen = false
	m.keysMode = false
	m.pendingAsk = &pendingAskUser{call: call}
	m.turnRuntime.waitForUserInput()
	m.notice = "assistant is waiting for your answer"
	if m.agentRunActive() {
		if err := m.agentLoop.run.WaitForUserInput(call.Question, time.Now()); err != nil {
			m.pendingAsk = nil
			m.errText = "pause agent for user input: " + err.Error()
			m.failVerifiedRun(err)
			m.refreshViewport()
			return m.persistAgentRun()
		}
		m.agentLoop.execution.NeedsUserInput = true
	}
	if choices := call.AskUserChoices(); len(choices) > 0 {
		m.openAgentQuestionPicker(call.Question, choices)
	} else {
		m.refreshViewport()
	}
	return m.persistAgentRun()
}

func (m *Model) answerAskUser(answer string) tea.Cmd {
	if m.pendingAsk == nil {
		return nil
	}
	answer = strings.TrimSpace(answer)
	if answer == "" {
		m.errText = "answer cannot be empty"
		m.refreshViewport()
		return nil
	}
	if utf8.RuneCountInString(answer) > maxAskUserAnswerRunes {
		m.errText = fmt.Sprintf("answer exceeds %d characters", maxAskUserAnswerRunes)
		m.refreshViewport()
		return nil
	}
	call := m.pendingAsk.call
	if !call.AllowText && !containsExact(call.AskUserChoices(), answer) {
		m.errText = "choose one of the available answers"
		m.openAgentQuestionPicker(call.Question, call.AskUserChoices())
		return nil
	}
	if m.agentLoop != nil && m.agentLoop.run != nil && m.agentLoop.run.Status == agent.DecisionNeedsUserInput && m.agentLoop.run.Stage == agent.StageExecutor {
		if err := m.agentLoop.run.ContinueExecutorWithUserInput(time.Now()); err != nil {
			m.errText = "resume agent after user input: " + err.Error()
			m.refreshViewport()
			return nil
		}
		m.agentLoop.execution.NeedsUserInput = false
		m.agentLoop.execution.NewEvidence = true
	}
	m.pendingAsk = nil
	m.closeOverlay()
	m.turnRuntime.continueAfterUserInput()
	m.errText = ""
	m.notice = "answer received; continuing the original task"
	payload, _ := json.Marshal(map[string]any{
		"answer":               answer,
		"grants_authorization": false,
	})
	result := tools.Result{Call: call, Output: string(payload)}
	m.recordAgentToolResultsCount([]tools.Result{result}, false, 0)
	m.toolOK++
	return tea.Batch(m.sendToolResults([]tools.Result{result}), m.persistAgentRun())
}

func containsExact(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func (m *Model) completePendingAskForShutdown() {
	if m.pendingAsk == nil {
		return
	}
	result := tools.Result{Call: m.pendingAsk.call, Err: errors.New("application closed before the human answered; this incomplete interaction must not be replayed")}
	m.pendingAsk = nil
	m.turnRuntime.continueAfterUserInput()
	m.appendTerminalToolResults([]tools.Result{result})
}
