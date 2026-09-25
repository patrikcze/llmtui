package agent

import (
	"reflect"
	"testing"
)

// TestEvaluateYieldPrecedenceOrder proves the ordering from the harness
// plan's §7, not just each rule in isolation: every case below sets up two
// or more simultaneously-true conditions from different precedence tiers and
// asserts the higher-precedence one wins, exactly as EvaluateYield's own
// numbered comments describe. A test that only ever sets one field at a time
// could pass with the rules in any order; these cannot.
func TestEvaluateYieldPrecedenceOrder(t *testing.T) {
	obligation := []MechanicalObligation{{CriterionID: "c1", Actionable: true}}

	cases := []struct {
		name string
		in   YieldInput
		want YieldAction
	}{
		{
			name: "cancelled outranks safety block",
			in:   YieldInput{Cancelled: true, SafetyBlocked: true},
			want: YieldCancelled,
		},
		{
			name: "safety block outranks budget exhaustion",
			in:   YieldInput{SafetyBlocked: true, BudgetExhausted: true},
			want: YieldBlocked,
		},
		{
			name: "budget exhaustion outranks a pending approval",
			in:   YieldInput{BudgetExhausted: true, PendingApproval: true},
			want: YieldBudgetExhausted,
		},
		{
			name: "pending approval outranks incomplete protocol",
			in:   YieldInput{PendingApproval: true, IncompleteProtocol: true, ProtocolRecoveryEligible: true},
			want: YieldNeedsUser,
		},
		{
			name: "pending ask outranks a pending tool batch",
			in:   YieldInput{PendingAsk: true, PendingToolBatch: true},
			want: YieldNeedsUser,
		},
		{
			name: "incomplete protocol outranks a pending tool batch",
			in:   YieldInput{IncompleteProtocol: true, ProtocolRecoveryEligible: false, PendingToolBatch: true},
			want: YieldFailed,
		},
		{
			name: "pending tool batch outranks an exhausted nudge budget",
			in:   YieldInput{PendingToolBatch: true, NoProgressNudges: 5, NudgeLimit: 2},
			want: YieldWait,
		},
		{
			name: "pending vision capture outranks an actionable obligation",
			in:   YieldInput{PendingVisionCapture: true, MechanicalObligations: obligation, ContextFeasible: true},
			want: YieldWait,
		},
		{
			name: "exhausted nudge budget outranks an actionable obligation",
			in:   YieldInput{NoProgressNudges: 2, NudgeLimit: 2, MechanicalObligations: obligation, ContextFeasible: true},
			want: YieldBlocked,
		},
		{
			name: "actionable obligation outranks plain quiescence",
			in:   YieldInput{MechanicalObligations: obligation, ContextFeasible: true},
			want: YieldContinue,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := EvaluateYield(tc.in).Action; got != tc.want {
				t.Fatalf("action = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestEvaluateYieldQuiescence exercises §8's quiescence definition directly:
// every individual blocking condition must prevent YieldVerify, and their
// absence (including any number of unresolved *semantic* criteria, which
// this input type cannot even represent) must reach it.
func TestEvaluateYieldQuiescence(t *testing.T) {
	base := YieldInput{ContextFeasible: true}

	notQuiescent := []struct {
		name string
		in   YieldInput
	}{
		{"cancelled", YieldInput{Cancelled: true}},
		{"safety blocked", YieldInput{SafetyBlocked: true}},
		{"budget exhausted", YieldInput{BudgetExhausted: true}},
		{"pending approval", YieldInput{PendingApproval: true}},
		{"pending ask", YieldInput{PendingAsk: true}},
		{"incomplete protocol", YieldInput{IncompleteProtocol: true}},
		{"pending tool batch", YieldInput{PendingToolBatch: true}},
		{"pending vision capture", YieldInput{PendingVisionCapture: true}},
		{"nudge budget exhausted", YieldInput{NoProgressNudges: 2, NudgeLimit: 2}},
		{"actionable obligation remains", YieldInput{MechanicalObligations: []MechanicalObligation{{CriterionID: "c1", Actionable: true}}, ContextFeasible: true}},
	}
	for _, tc := range notQuiescent {
		t.Run("blocks: "+tc.name, func(t *testing.T) {
			got := EvaluateYield(tc.in)
			if got.Action == YieldVerify {
				t.Fatalf("action = %q, want anything but YieldVerify while %s", got.Action, tc.name)
			}
			if got.Quiescent() {
				t.Fatalf("Quiescent() = true while %s", tc.name)
			}
		})
	}

	t.Run("plain empty input is quiescent", func(t *testing.T) {
		got := EvaluateYield(base)
		if got.Action != YieldVerify || !got.Quiescent() {
			t.Fatalf("decision = %+v, want quiescent YieldVerify", got)
		}
	})

	t.Run("a non-actionable obligation does not block quiescence", func(t *testing.T) {
		in := base
		in.MechanicalObligations = []MechanicalObligation{{CriterionID: "c1", Actionable: false}}
		got := EvaluateYield(in)
		if got.Action != YieldVerify {
			t.Fatalf("action = %q, want YieldVerify: a non-actionable obligation must never justify YieldContinue, and must not otherwise block quiescence", got.Action)
		}
	})
}

// TestEvaluateYieldNoProgressBudget covers the exact boundary and the
// "uncapped" sentinel, since off-by-one here either lets a stalled episode
// nudge forever or stops a healthy one one nudge too early.
func TestEvaluateYieldNoProgressBudget(t *testing.T) {
	obligation := []MechanicalObligation{{CriterionID: "c1", Actionable: true}}
	cases := []struct {
		name   string
		nudges int
		limit  int
		want   YieldAction
	}{
		{"below limit continues", 1, 2, YieldContinue},
		{"exactly at limit blocks", 2, 2, YieldBlocked},
		{"above limit blocks", 3, 2, YieldBlocked},
		{"zero limit means uncapped", 1000, 0, YieldContinue},
		{"negative limit means uncapped", 5, -1, YieldContinue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := YieldInput{
				NoProgressNudges:      tc.nudges,
				NudgeLimit:            tc.limit,
				MechanicalObligations: obligation,
				ContextFeasible:       true,
			}
			got := EvaluateYield(in)
			if got.Action != tc.want {
				t.Fatalf("action = %q, want %q for nudges=%d limit=%d", got.Action, tc.want, tc.nudges, tc.limit)
			}
			if tc.want == YieldBlocked && got.Reason != ReasonNoProgressStalled {
				t.Fatalf("reason = %q, want %q", got.Reason, ReasonNoProgressStalled)
			}
		})
	}
}

// TestEvaluateYieldMechanicalObligationsFilterNonActionable proves a
// non-actionable obligation is silently excluded from CriterionIDs rather
// than either crashing or being treated as actionable.
func TestEvaluateYieldMechanicalObligationsFilterNonActionable(t *testing.T) {
	in := YieldInput{
		MechanicalObligations: []MechanicalObligation{
			{CriterionID: "c1", Actionable: false},
			{CriterionID: "c2", Actionable: true},
			{CriterionID: "c3", Actionable: false},
		},
		ContextFeasible: true,
	}
	got := EvaluateYield(in)
	if got.Action != YieldContinue {
		t.Fatalf("action = %q, want YieldContinue", got.Action)
	}
	if len(got.CriterionIDs) != 1 || got.CriterionIDs[0] != "c2" {
		t.Fatalf("criterion IDs = %v, want exactly [c2]", got.CriterionIDs)
	}
}

// TestEvaluateYieldObligationsCappedAtMaxCriteria proves the returned
// criterion list stays bounded even if a caller somehow hands in more
// obligations than the pinned-criteria cap allows.
func TestEvaluateYieldObligationsCappedAtMaxCriteria(t *testing.T) {
	var obligations []MechanicalObligation
	for i := 0; i < MaxCriteria+5; i++ {
		obligations = append(obligations, MechanicalObligation{CriterionID: "c", Actionable: true})
	}
	got := EvaluateYield(YieldInput{MechanicalObligations: obligations, ContextFeasible: true})
	if len(got.CriterionIDs) != MaxCriteria {
		t.Fatalf("criterion IDs = %d, want capped at MaxCriteria (%d)", len(got.CriterionIDs), MaxCriteria)
	}
}

// TestEvaluateYieldContextInfeasibleBlocksContinuation proves an actionable
// obligation does not force a continuation when context preparation for it
// is not currently feasible — it must block explicitly, not silently drop
// the obligation or retry forever.
func TestEvaluateYieldContextInfeasibleBlocksContinuation(t *testing.T) {
	in := YieldInput{
		MechanicalObligations: []MechanicalObligation{{CriterionID: "c1", Actionable: true}},
		ContextFeasible:       false,
	}
	got := EvaluateYield(in)
	if got.Action != YieldBlocked || got.Reason != ReasonContextInfeasible {
		t.Fatalf("decision = %+v, want Blocked/context_infeasible", got)
	}
	if len(got.CriterionIDs) != 1 || got.CriterionIDs[0] != "c1" {
		t.Fatalf("criterion IDs = %v, want the blocked obligation named", got.CriterionIDs)
	}
}

// TestYieldDecisionTerminalMapping proves Terminal() and TerminalDecision()
// agree with each other and reuse existing Decision vocabulary rather than
// inventing a parallel one, for every action EvaluateYield can produce.
func TestYieldDecisionTerminalMapping(t *testing.T) {
	cases := []struct {
		name         string
		decision     YieldDecision
		wantTerminal bool
		wantDecision Decision
	}{
		{"continue", YieldDecision{Action: YieldContinue}, false, ""},
		{"verify", YieldDecision{Action: YieldVerify}, false, ""},
		{"wait", YieldDecision{Action: YieldWait}, false, ""},
		{"needs user", YieldDecision{Action: YieldNeedsUser}, true, DecisionNeedsUserInput},
		{"cancelled", YieldDecision{Action: YieldCancelled}, true, DecisionCancelled},
		{"budget exhausted", YieldDecision{Action: YieldBudgetExhausted}, true, DecisionBudgetExhausted},
		{"failed", YieldDecision{Action: YieldFailed}, true, DecisionFailed},
		{"blocked: no progress", YieldDecision{Action: YieldBlocked, Reason: ReasonNoProgressStalled}, true, DecisionNoProgress},
		{"blocked: safety", YieldDecision{Action: YieldBlocked, Reason: ReasonSafetyBlocked}, true, DecisionEscalated},
		{"blocked: context infeasible", YieldDecision{Action: YieldBlocked, Reason: ReasonContextInfeasible}, true, DecisionEscalated},
		{"unknown zero value", YieldDecision{}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.decision.Terminal(); got != tc.wantTerminal {
				t.Fatalf("Terminal() = %v, want %v", got, tc.wantTerminal)
			}
			gotDecision, ok := tc.decision.TerminalDecision()
			if ok != tc.wantTerminal {
				t.Fatalf("TerminalDecision() ok = %v, want %v", ok, tc.wantTerminal)
			}
			if ok && gotDecision != tc.wantDecision {
				t.Fatalf("TerminalDecision() = %q, want %q", gotDecision, tc.wantDecision)
			}
		})
	}
}

// TestEvaluateYieldZeroValueInputIsQuiescentNotDone is the Phase 1 gate's
// "unknown/malformed state never reports done" requirement made concrete: a
// totally zero-value YieldInput (a caller that filled in nothing) has no
// executable action defined for it here beyond "nothing pending" — it must
// resolve to YieldVerify (hand off to the existing verifier, which is
// authoritative for completion) and never to a terminal "done"-shaped
// outcome invented by this policy itself. YieldAction has no "done" value at
// all; TerminalDecision returning ok=false here is the proof this policy
// never manufactures one.
func TestEvaluateYieldZeroValueInputIsQuiescentNotDone(t *testing.T) {
	got := EvaluateYield(YieldInput{})
	if got.Action != YieldVerify {
		t.Fatalf("action = %q, want YieldVerify for a zero-value input", got.Action)
	}
	if _, ok := got.TerminalDecision(); ok {
		t.Fatal("a zero-value input must never resolve to a terminal decision by itself")
	}
	if zero := (YieldDecision{}); zero.Action != YieldUnknown {
		t.Fatalf("YieldDecision zero value action = %q, want YieldUnknown", zero.Action)
	}
}

// TestEvaluateYieldContradictoryInputStaysDeterministic proves that even a
// self-contradictory input (multiple tiers simultaneously true, some of them
// nonsensical together) always resolves through the same strict precedence
// order rather than panicking or silently picking an arbitrary branch.
func TestEvaluateYieldContradictoryInputStaysDeterministic(t *testing.T) {
	in := YieldInput{
		Cancelled:                true,
		SafetyBlocked:            true,
		BudgetExhausted:          true,
		PendingApproval:          true,
		PendingAsk:               true,
		IncompleteProtocol:       true,
		ProtocolRecoveryEligible: true,
		PendingToolBatch:         true,
		PendingVisionCapture:     true,
		NoProgressNudges:         99,
		NudgeLimit:               1,
		MechanicalObligations:    []MechanicalObligation{{CriterionID: "c1", Actionable: true}},
		ContextFeasible:          true,
	}
	first := EvaluateYield(in)
	if first.Action != YieldCancelled {
		t.Fatalf("action = %q, want YieldCancelled to win over every other simultaneously-true condition", first.Action)
	}
	// Pure and total: the same input evaluated again must agree exactly.
	if second := EvaluateYield(in); !reflect.DeepEqual(second, first) {
		t.Fatalf("EvaluateYield is not deterministic: %+v != %+v", second, first)
	}
}
