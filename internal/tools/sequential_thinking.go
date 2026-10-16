package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/patrikcze/llmtui/internal/provider"
)

// ToolSequentialThinking lets a model plan a problem as numbered thoughts it
// can revise and branch. It has no file, command, or network effect: the
// Runner only keeps a bounded, in-memory log of the steps for the current
// conversation or /agent run and answers with a status summary. Nothing is
// persisted. It is opt-in (tools.sequential_thinking.enabled) because small
// local models tend to spend their tool budget on it.
const ToolSequentialThinking = "sequential_thinking"

// Bounds on one conversation's (or agent run's) thinking log.
const (
	MaxThoughts             = 64
	MaxThoughtBytes         = 4096
	MaxThinkingBranches     = 16
	maxThinkingPayloadBytes = MaxThoughtBytes + 1024
	maxThoughtNumber        = 1000
)

var thinkingBranchID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// ThinkingStep is one sequential_thinking call's arguments. It is a value
// type so Call stays comparable.
type ThinkingStep struct {
	ThoughtNumber     int
	TotalThoughts     int
	NextThoughtNeeded bool
	IsRevision        bool
	RevisesThought    int
	BranchFromThought int
	BranchID          string
	NeedsMoreThoughts bool
}

// thinkingArgKeys maps every accepted argument spelling to its canonical
// snake_case name. The camelCase forms are the reference MCP server's, which
// models trained on that server emit.
var thinkingArgKeys = map[string]string{
	"thought":             "thought",
	"next_thought_needed": "next_thought_needed", "nextThoughtNeeded": "next_thought_needed",
	"thought_number": "thought_number", "thoughtNumber": "thought_number",
	"total_thoughts": "total_thoughts", "totalThoughts": "total_thoughts",
	"is_revision": "is_revision", "isRevision": "is_revision",
	"revises_thought": "revises_thought", "revisesThought": "revises_thought",
	"branch_from_thought": "branch_from_thought", "branchFromThought": "branch_from_thought",
	"branch_id": "branch_id", "branchId": "branch_id",
	"needs_more_thoughts": "needs_more_thoughts", "needsMoreThoughts": "needs_more_thoughts",
}

type thinkingArgs struct {
	Thought           string `json:"thought"`
	NextThoughtNeeded *bool  `json:"next_thought_needed"`
	ThoughtNumber     int    `json:"thought_number"`
	TotalThoughts     int    `json:"total_thoughts"`
	IsRevision        bool   `json:"is_revision"`
	RevisesThought    int    `json:"revises_thought"`
	BranchFromThought int    `json:"branch_from_thought"`
	BranchID          string `json:"branch_id"`
	NeedsMoreThoughts bool   `json:"needs_more_thoughts"`
}

// ThinkingSpecs declares sequential_thinking for native tool calling. The
// TUI offers it only when tools.sequential_thinking.enabled is set.
func ThinkingSpecs() []provider.ToolSpec {
	return []provider.ToolSpec{{
		Name:        ToolSequentialThinking,
		Description: "Think through a complex problem step by step before acting. Record one numbered thought per call; you may revise an earlier thought, branch from one to explore an alternative, and raise total_thoughts if you need more. Set next_thought_needed=false when the reasoning is done, then act or answer. Thoughts are kept only for this conversation and are not shown back; the result is a short status summary. Use it for planning, not for trivial steps.",
		Parameters: json.RawMessage(`{
			"type": "object",
			"properties": {
				"thought": {"type": "string", "maxLength": 4096, "description": "Your current thinking step."},
				"next_thought_needed": {"type": "boolean", "description": "Whether another thought step is needed."},
				"thought_number": {"type": "integer", "minimum": 1, "description": "Current thought number."},
				"total_thoughts": {"type": "integer", "minimum": 1, "description": "Current estimate of thoughts needed; may be adjusted up or down."},
				"is_revision": {"type": "boolean", "description": "Whether this thought revises an earlier one."},
				"revises_thought": {"type": "integer", "minimum": 1, "description": "Which thought is being reconsidered (with is_revision)."},
				"branch_from_thought": {"type": "integer", "minimum": 1, "description": "Thought number this branch starts from."},
				"branch_id": {"type": "string", "pattern": "^[A-Za-z0-9_-]{1,64}$", "description": "Identifier of the branch (with branch_from_thought)."},
				"needs_more_thoughts": {"type": "boolean", "description": "Set when reaching the end but realizing more thoughts are needed."}
			},
			"required": ["thought", "next_thought_needed", "thought_number", "total_thoughts"],
			"additionalProperties": false
		}`),
	}}
}

// ThinkingInstructions is the fenced-protocol tool line for
// sequential_thinking, appended to Instructions when it is enabled.
const ThinkingInstructions = `- sequential_thinking — plan a complex problem as numbered thoughts before acting; the block body is one JSON object {"thought":"…","thought_number":1,"total_thoughts":3,"next_thought_needed":true}, optionally with is_revision/revises_thought or branch_from_thought/branch_id. Use it for planning, not for trivial steps.`

// decodeThinkingArgs parses sequential_thinking's JSON object, accepting both
// snake_case and the reference camelCase spellings and rejecting anything
// else, then validates it into call.
func decodeThinkingArgs(call *Call, raw string) {
	call.Body = ""
	raw = strings.TrimSpace(raw)
	if len(raw) > maxThinkingPayloadBytes {
		call.InputErr = fmt.Sprintf("sequential_thinking arguments exceed the %d byte limit", maxThinkingPayloadBytes)
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil || fields == nil {
		call.InputErr = "sequential_thinking needs one JSON object with thought, thought_number, total_thoughts and next_thought_needed"
		return
	}
	canonical := make(map[string]json.RawMessage, len(fields))
	for key, value := range fields {
		name, ok := thinkingArgKeys[key]
		if !ok {
			call.InputErr = fmt.Sprintf("sequential_thinking does not accept argument %q", key)
			return
		}
		if _, dup := canonical[name]; dup {
			call.InputErr = fmt.Sprintf("sequential_thinking argument %q is given twice", name)
			return
		}
		canonical[name] = value
	}
	normalized, _ := json.Marshal(canonical)
	var args thinkingArgs
	if err := json.Unmarshal(normalized, &args); err != nil {
		call.InputErr = "sequential_thinking arguments have the wrong type: " + err.Error()
		return
	}
	if args.NextThoughtNeeded == nil {
		call.InputErr = "sequential_thinking needs next_thought_needed"
		return
	}
	call.Body = args.Thought
	call.Thinking = ThinkingStep{
		ThoughtNumber: args.ThoughtNumber, TotalThoughts: args.TotalThoughts,
		NextThoughtNeeded: *args.NextThoughtNeeded, IsRevision: args.IsRevision,
		RevisesThought: args.RevisesThought, BranchFromThought: args.BranchFromThought,
		BranchID: strings.TrimSpace(args.BranchID), NeedsMoreThoughts: args.NeedsMoreThoughts,
	}
	if err := ValidateThinkingCall(call); err != nil {
		call.InputErr = err.Error()
	}
}

// ValidateThinkingCall checks one step's shape without touching state.
func ValidateThinkingCall(call *Call) error {
	s := call.Thinking
	switch {
	case strings.TrimSpace(call.Body) == "":
		return errors.New("sequential_thinking needs a non-empty thought")
	case len(call.Body) > MaxThoughtBytes:
		return fmt.Errorf("a thought may be at most %d bytes; split it into several thoughts", MaxThoughtBytes)
	case s.ThoughtNumber < 1 || s.ThoughtNumber > maxThoughtNumber:
		return fmt.Errorf("thought_number must be between 1 and %d", maxThoughtNumber)
	case s.TotalThoughts < 1 || s.TotalThoughts > maxThoughtNumber:
		return fmt.Errorf("total_thoughts must be between 1 and %d", maxThoughtNumber)
	case s.RevisesThought != 0 && !s.IsRevision:
		return errors.New("revises_thought needs is_revision=true")
	case s.IsRevision && (s.RevisesThought < 1 || s.RevisesThought >= s.ThoughtNumber):
		return errors.New("a revision needs revises_thought set to an earlier thought number")
	case (s.BranchFromThought != 0) != (s.BranchID != ""):
		return errors.New("branch_from_thought and branch_id must be given together")
	case s.BranchFromThought != 0 && (s.BranchFromThought < 1 || s.BranchFromThought >= s.ThoughtNumber):
		return errors.New("branch_from_thought must name an earlier thought number")
	case s.BranchID != "" && !thinkingBranchID.MatchString(s.BranchID):
		return errors.New("branch_id may use only letters, digits, '_' and '-' (at most 64)")
	}
	return nil
}

// thinkingLog is the Runner's bounded, in-memory sequential_thinking state.
type thinkingLog struct {
	mu       sync.Mutex
	thoughts int
	branches map[string]int
}

// ResetThinking forgets the current sequential_thinking log. The TUI calls
// it at every session boundary and at the start of each /agent run.
func (r *Runner) ResetThinking() {
	if r == nil {
		return
	}
	r.thinking.mu.Lock()
	r.thinking.thoughts, r.thinking.branches = 0, nil
	r.thinking.mu.Unlock()
}

type thinkingStatus struct {
	ThoughtNumber        int      `json:"thought_number"`
	TotalThoughts        int      `json:"total_thoughts"`
	NextThoughtNeeded    bool     `json:"next_thought_needed"`
	Branches             []string `json:"branches"`
	ThoughtHistoryLength int      `json:"thought_history_length"`
	RemainingThoughts    int      `json:"remaining_thoughts"`
}

// sequentialThinking records one step and returns the status summary. The
// thought text itself is never echoed back: the model already has it, and
// keeping it out of the result keeps the context small.
func (r *Runner) sequentialThinking(c Call) (string, ResultMeta, error) {
	meta := ResultMeta{Effect: EffectNone}
	if !r.SequentialThinking {
		return "", meta, withCode(errors.New("sequential_thinking is not enabled (tools.sequential_thinking.enabled)"), "not_found", RetryNone)
	}
	if err := ValidateThinkingCall(&c); err != nil {
		return "", meta, withCode(err, "invalid_arguments", RetryCorrectInput)
	}
	s := c.Thinking
	log := &r.thinking
	log.mu.Lock()
	defer log.mu.Unlock()
	if log.thoughts >= MaxThoughts {
		return "", meta, withCode(fmt.Errorf("the %d-thought limit for this conversation is reached; stop thinking and act on your conclusion", MaxThoughts), "limit_reached", RetryNone)
	}
	if s.BranchID != "" {
		if _, known := log.branches[s.BranchID]; !known && len(log.branches) >= MaxThinkingBranches {
			return "", meta, withCode(fmt.Errorf("at most %d branches per conversation; continue an existing branch", MaxThinkingBranches), "limit_reached", RetryCorrectInput)
		}
		if log.branches == nil {
			log.branches = make(map[string]int)
		}
		log.branches[s.BranchID]++
	}
	log.thoughts++
	status := thinkingStatus{
		ThoughtNumber:        s.ThoughtNumber,
		TotalThoughts:        max(s.TotalThoughts, s.ThoughtNumber),
		NextThoughtNeeded:    s.NextThoughtNeeded,
		Branches:             make([]string, 0, len(log.branches)),
		ThoughtHistoryLength: log.thoughts,
		RemainingThoughts:    MaxThoughts - log.thoughts,
	}
	for id := range log.branches {
		status.Branches = append(status.Branches, id)
	}
	sort.Strings(status.Branches)
	body, err := json.Marshal(status)
	if err != nil {
		return "", meta, fmt.Errorf("sequential_thinking: %w", err)
	}
	meta.Outcome = OutcomeOK
	meta.Coverage = Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, ObservedBytes: int64(len(body)), RetainedBytes: int64(len(body))}
	return string(body), meta, nil
}

func describeThinking(c Call) string {
	s := c.Thinking
	label := fmt.Sprintf("sequential_thinking: thought %d/%d", s.ThoughtNumber, max(s.TotalThoughts, s.ThoughtNumber))
	switch {
	case s.IsRevision:
		label += fmt.Sprintf(" (revises %d)", s.RevisesThought)
	case s.BranchID != "":
		label += fmt.Sprintf(" (branch %s from %d)", s.BranchID, s.BranchFromThought)
	}
	return label
}
