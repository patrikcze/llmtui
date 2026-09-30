package tui

import (
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"

	"github.com/patrikcze/llmtui/internal/agent"
)

// trivialContractCriterion is the one criterion pinned when the task-contract
// request is skipped. The semantic verifier still judges it.
const trivialContractCriterion = "answer the user's request as stated"

// maxTrivialContractRunes bounds what may count as a trivial request.
const maxTrivialContractRunes = 160

// trivialIntentWords mark workspace, command, web, or code intent. Any of
// them keeps the model-generated contract, whose criteria matter there.
var trivialIntentWords = map[string]bool{
	"file": true, "files": true, "read": true, "write": true, "edit": true, "create": true,
	"delete": true, "remove": true, "rename": true, "move": true, "copy": true, "save": true,
	"run": true, "execute": true, "install": true, "build": true, "compile": true, "test": true,
	"tests": true, "fix": true, "debug": true, "refactor": true, "implement": true, "commit": true,
	"push": true, "search": true, "find": true, "grep": true, "fetch": true, "download": true,
	"open": true, "directory": true, "folder": true, "repo": true, "repository": true,
	"project": true, "code": true, "script": true, "function": true, "command": true,
	"terminal": true, "shell": true, "web": true, "website": true, "url": true, "api": true,
	"summarize": true, "list": true, "check": true, "update": true, "change": true,
	"generate": true, "analyze": true, "compare": true, "email": true, "mail": true,
	"calendar": true, "then": true, "and": true,
}

var (
	trivialWordPattern     = regexp.MustCompile(`[\p{L}\p{N}_']+`)
	trivialFilenamePattern = regexp.MustCompile(`\w\.[A-Za-z0-9]{1,5}\b`)
	trivialSentenceBreak   = regexp.MustCompile(`[.?!;:]\s+\S`)
)

// trivialContractRequest reports whether request is a short, single-sentence
// question or instruction with no attachment and no workspace, command, web,
// or code intent. It is deliberately conservative: a false "trivial" skips
// the contract's decomposition, so anything ambiguous keeps the contract.
func trivialContractRequest(request string, hasImages bool) bool {
	request = strings.TrimSpace(request)
	if hasImages || request == "" || utf8.RuneCountInString(request) > maxTrivialContractRunes {
		return false
	}
	if strings.ContainsAny(request, "\n/\\`~$<>{}[]|=") || strings.Contains(request, "://") {
		return false
	}
	if trivialFilenamePattern.MatchString(request) || trivialSentenceBreak.MatchString(request) {
		return false
	}
	for _, word := range trivialWordPattern.FindAllString(strings.ToLower(request), -1) {
		if trivialIntentWords[word] {
			return false
		}
	}
	return true
}

// skipTrivialContract pins trivialContractCriterion without a contract model
// request when agent.skip_trivial_contract is on and the run's request is
// trivial. It returns nil when the normal contract request must run.
func (m *Model) skipTrivialContract(run *agent.AgentRun) tea.Cmd {
	if !m.cfg.Agent.SkipTrivialContract || run.ContractInput != "" ||
		!trivialContractRequest(run.Request, len(m.agentLoop.initialImages) > 0) {
		return nil
	}
	now := time.Now()
	if err := run.BeginContract(now); err != nil {
		return nil // the normal path reports the same transition or budget error
	}
	if err := run.CompleteContract([]string{trivialContractCriterion}, now); err != nil {
		m.failVerifiedRun(err)
		m.endAgentRun()
		return m.persistAgentRun()
	}
	run.NoteDiagnostic(now, "contract_skipped", "trivial request: one criterion pinned without a contract request")
	m.syncAgentDebug()
	return tea.Batch(m.persistAgentRun(), m.startInitialAgentCycle(run.Request, m.agentLoop.initialImages))
}
