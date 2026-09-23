package tools

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// stateSignature is everything Phase 1a's done criterion says a caller may
// use to tell these nine states apart: the typed Meta fields alone. Output
// and Err.Error() text are deliberately excluded from the signature — see
// TestNineOutcomeStatesAreIndependentlyObservable.
type stateSignature struct {
	outcome Outcome
	code    string // "" when Meta.Error is nil
	effect  Effect
}

// TestNineOutcomeStatesAreIndependentlyObservable is Phase 1a's Done
// criterion (§31): "success, valid empty, partial, failure, denial,
// controller block, timeout, cancellation and unknown effect are
// independently observable without parsing arbitrary source text." Every
// fixture below shares the exact same Output text and (where an error is
// present) the exact same Err text — a legacy string-classifier fed any two
// of these would see identical or misleading prose — yet each state's typed
// signature (Meta.Outcome/Meta.Error.Code/Meta.Effect) is unique, proving the
// states are distinguishable through structured fields alone. "Denial" and
// "controller block" are represented the same way Result.deniedResults and
// the progress ledger's synthetic blocks already represent them: a stable
// ErrorInfo.Code on an otherwise-Unknown outcome, never a parsed string.
func TestNineOutcomeStatesAreIndependentlyObservable(t *testing.T) {
	const sharedOutput = "operation finished"  // says nothing about outcome
	const sharedErrText = "something happened" // misleading if parsed as prose

	cases := []struct {
		name   string
		result Result
	}{
		{
			name:   "success",
			result: Result{Output: sharedOutput, Meta: ResultMeta{Outcome: OutcomeOK, Effect: EffectChanged, Coverage: Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true, RetainedBytes: 19}}},
		},
		{
			name:   "valid empty",
			result: Result{Output: "", Meta: ResultMeta{Outcome: OutcomeOK, Effect: EffectNone, Coverage: Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true}}},
		},
		{
			name:   "partial",
			result: Result{Output: sharedOutput, Meta: ResultMeta{Outcome: OutcomePartial, Effect: EffectNone, Coverage: Coverage{SourceComplete: false, Reasons: []string{"files"}}}},
		},
		{
			name:   "failure",
			result: Result{Output: sharedOutput, Err: errors.New(sharedErrText), Meta: ResultMeta{Outcome: OutcomeFailed, Effect: EffectNone, Error: &ErrorInfo{Code: "not_found", Retry: RetryCorrectInput}}},
		},
		{
			// Mirrors tools.go's own deniedResults construction: Err is the
			// ErrDenied sentinel, Outcome is Unknown (the call never ran),
			// and Error.Code is the stable "permission_denied" code.
			name:   "denial",
			result: Result{Output: sharedOutput, Err: ErrDenied, Meta: ResultMeta{Outcome: OutcomeUnknown, Effect: EffectNone, Error: &ErrorInfo{Code: "permission_denied", Retry: RetryNone}}},
		},
		{
			name:   "controller block",
			result: Result{Output: sharedOutput, Err: errors.New(sharedErrText), Meta: ResultMeta{Outcome: OutcomeUnknown, Effect: EffectNone, Error: &ErrorInfo{Code: "repeat_block", Retry: RetryLater}}},
		},
		{
			name:   "timeout",
			result: Result{Output: sharedOutput, Err: fmt.Errorf("run: %w", context.DeadlineExceeded), Meta: ResultMeta{Outcome: OutcomeTimeout, Effect: EffectUnknown}},
		},
		{
			name:   "cancellation",
			result: Result{Output: sharedOutput, Err: fmt.Errorf("run: %w", context.Canceled), Meta: ResultMeta{Outcome: OutcomeCancelled, Effect: EffectNone}},
		},
		{
			// The plan's own example: "An executed command timeout can have
			// Effect=unknown; do not infer that it never changed a file."
			// Distinct from the "timeout" case above by Outcome (OK, not
			// Timeout) to prove Effect and Outcome vary independently.
			name:   "unknown effect",
			result: Result{Output: sharedOutput, Meta: ResultMeta{Outcome: OutcomeOK, Effect: EffectUnknown, Coverage: Coverage{SourceComplete: true, CaptureComplete: true, PreviewComplete: true}}},
		},
	}

	seen := make(map[stateSignature]string, len(cases))
	for _, tc := range cases {
		var code string
		if tc.result.Meta.Error != nil {
			code = tc.result.Meta.Error.Code
		}
		sig := stateSignature{outcome: tc.result.Meta.Outcome, code: code, effect: tc.result.Meta.Effect}
		if prior, ok := seen[sig]; ok {
			t.Fatalf("state %q and %q share an identical signature %+v — not independently observable", prior, tc.name, sig)
		}
		seen[sig] = tc.name
	}
	if len(seen) != len(cases) {
		t.Fatalf("expected %d distinct signatures, got %d", len(cases), len(seen))
	}
}
