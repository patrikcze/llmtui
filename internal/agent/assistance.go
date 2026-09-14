package agent

// This file is the Phase 4 measured-assistance layer: process-local,
// content-free counters of a model's control-format reliability, and a pure
// function relating them to one narrow, reversible intervention
// recommendation. Rollout is shadow-only for now — see
// .claude/tasks/plans/llmtui-agent-evolution.md §14: nothing in this
// package, or any current caller, alters a request based on Assistance yet.
// The recommendation is presently a diagnostic only (surfaced in `/debug
// last`), pending the measured evaluation the plan requires before an
// intervention is allowed to change live behavior. Counters are never
// persisted and are scoped to one run; a future cross-run store must key by
// provider/model/endpoint identity and reset on any of those changing (see
// §8) — this package does not yet attempt that broader scope.

// BehaviorStats are bounded, content-free counters: no prompt text, no tool
// output, no model identity beyond what the caller already keys the value
// by externally. The zero value is a fresh, unmeasured run.
type BehaviorStats struct {
	FormatAttempts            int
	FormatFailures            int
	ConsecutiveFormatFailures int
	ConsecutiveSuccesses      int
}

// RecordControlAttempt updates counters for one contract/verifier control
// request. formatFailure must be true only for an attributable control-
// format failure (malformed/unparseable control JSON, i.e. the request
// failed with agent.ErrMalformedControl) — never for permission denial, a
// missing resource, a timeout, cancellation, a legitimate clarifying
// question, or verifier/semantic disagreement. None of those are model
// tool-syntax failures, and counting them would misattribute an
// intervention to a problem a corrective hint cannot fix. A nil receiver is
// a no-op, matching this package's other bounded state.
func (s *BehaviorStats) RecordControlAttempt(formatFailure bool) {
	if s == nil {
		return
	}
	s.FormatAttempts++
	if formatFailure {
		s.FormatFailures++
		s.ConsecutiveFormatFailures++
		s.ConsecutiveSuccesses = 0
		return
	}
	s.ConsecutiveFormatFailures = 0
	s.ConsecutiveSuccesses++
}

// AssistanceReason names why ChooseAssistance did or did not recommend a
// hint, for diagnostics only — never for prompt text.
type AssistanceReason string

const (
	AssistanceNone       AssistanceReason = "none"
	AssistanceFormatHint AssistanceReason = "repeated_format_failures"
	AssistanceRelaxed    AssistanceReason = "success_window_met"
)

// Assistance is ChooseAssistance's bounded, reversible recommendation. It
// can never authorize execution, change permissions, or select a protocol
// the model's declared capabilities forbid (see provider.Capabilities) — it
// only says whether a short, narrow corrective hint currently looks
// warranted.
type Assistance struct {
	Hint   bool
	Reason AssistanceReason
}

// formatFailureThreshold and successRelaxationWindow are intentionally not
// configuration: §8 says "live thresholds remain experimental until
// measured" and to choose them via shadow-mode evaluation, not YAML.
const (
	formatFailureThreshold  = 2
	successRelaxationWindow = 2
)

// ChooseAssistance relates observed counters and whether a hint is
// currently active to one recommendation. It recommends a hint only after
// formatFailureThreshold consecutive attributable format failures, and,
// once active, keeps recommending it through successRelaxationWindow
// consecutive clean successes before recommending it be withdrawn — see
// §8's "Keep intervention active through a short success window before
// removing it" — so a single lucky success does not immediately flip
// intervention off and invite oscillation on the very next failure.
func ChooseAssistance(stats BehaviorStats, currentlyActive bool) Assistance {
	if stats.ConsecutiveFormatFailures >= formatFailureThreshold {
		return Assistance{Hint: true, Reason: AssistanceFormatHint}
	}
	if currentlyActive {
		if stats.ConsecutiveSuccesses < successRelaxationWindow {
			return Assistance{Hint: true, Reason: AssistanceFormatHint}
		}
		return Assistance{Hint: false, Reason: AssistanceRelaxed}
	}
	return Assistance{Hint: false, Reason: AssistanceNone}
}
