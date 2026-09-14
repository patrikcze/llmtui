package agent

import "testing"

func TestRecordControlAttemptTracksConsecutiveFailuresAndSuccesses(t *testing.T) {
	var s BehaviorStats
	s.RecordControlAttempt(true)
	s.RecordControlAttempt(true)
	if s.FormatAttempts != 2 || s.FormatFailures != 2 || s.ConsecutiveFormatFailures != 2 || s.ConsecutiveSuccesses != 0 {
		t.Fatalf("after two failures: %+v", s)
	}
	s.RecordControlAttempt(false)
	if s.ConsecutiveFormatFailures != 0 || s.ConsecutiveSuccesses != 1 || s.FormatFailures != 2 {
		t.Fatalf("after a success: %+v", s)
	}
	s.RecordControlAttempt(true)
	if s.ConsecutiveFormatFailures != 1 || s.ConsecutiveSuccesses != 0 {
		t.Fatalf("a later failure must reset the success streak: %+v", s)
	}
}

func TestRecordControlAttemptNilReceiverIsSafe(t *testing.T) {
	var s *BehaviorStats
	s.RecordControlAttempt(true) // must not panic
}

func TestChooseAssistanceRecommendsHintAfterThreshold(t *testing.T) {
	cases := []struct {
		name   string
		stats  BehaviorStats
		want   bool
		reason AssistanceReason
	}{
		{"below threshold", BehaviorStats{ConsecutiveFormatFailures: 1}, false, AssistanceNone},
		{"at threshold", BehaviorStats{ConsecutiveFormatFailures: 2}, true, AssistanceFormatHint},
		{"above threshold", BehaviorStats{ConsecutiveFormatFailures: 5}, true, AssistanceFormatHint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ChooseAssistance(tc.stats, false)
			if got.Hint != tc.want || got.Reason != tc.reason {
				t.Fatalf("ChooseAssistance(%+v, false) = %+v, want hint=%v reason=%s", tc.stats, got, tc.want, tc.reason)
			}
		})
	}
}

func TestChooseAssistanceDoesNotCountPermissionOrTimeoutFailures(t *testing.T) {
	// RecordControlAttempt is only ever called with formatFailure=true for
	// agent.ErrMalformedControl per its own contract; this test documents
	// that a stats value with zero consecutive format failures (as it would
	// be after only non-format errors, which callers must never record as
	// format failures) never recommends a hint regardless of other fields.
	stats := BehaviorStats{FormatAttempts: 5, FormatFailures: 0, ConsecutiveFormatFailures: 0}
	if got := ChooseAssistance(stats, false); got.Hint {
		t.Fatalf("ChooseAssistance = %+v, want no hint when no format failures were recorded", got)
	}
}

func TestChooseAssistanceHoldsHintThroughSuccessWindow(t *testing.T) {
	// Once active, one success must not immediately withdraw the hint — a
	// short window of clean successes is required first.
	stats := BehaviorStats{ConsecutiveFormatFailures: 0, ConsecutiveSuccesses: 1}
	got := ChooseAssistance(stats, true)
	if !got.Hint {
		t.Fatalf("ChooseAssistance = %+v, want the hint held through the success window", got)
	}
}

func TestChooseAssistanceRelaxesAfterSuccessWindow(t *testing.T) {
	stats := BehaviorStats{ConsecutiveFormatFailures: 0, ConsecutiveSuccesses: successRelaxationWindow}
	got := ChooseAssistance(stats, true)
	if got.Hint || got.Reason != AssistanceRelaxed {
		t.Fatalf("ChooseAssistance = %+v, want the hint relaxed after the success window", got)
	}
}

func TestChooseAssistanceNeverActiveStaysInactiveOnMereSuccesses(t *testing.T) {
	stats := BehaviorStats{ConsecutiveSuccesses: 10}
	got := ChooseAssistance(stats, false)
	if got.Hint || got.Reason != AssistanceNone {
		t.Fatalf("ChooseAssistance = %+v, want no recommendation when never active and no failures", got)
	}
}

func TestChooseAssistanceRenewedFailureOverridesActiveRelaxation(t *testing.T) {
	// A fresh run of failures must re-trigger the hint even if it was
	// nominally "active" — the failure check is evaluated first.
	stats := BehaviorStats{ConsecutiveFormatFailures: formatFailureThreshold, ConsecutiveSuccesses: 0}
	got := ChooseAssistance(stats, true)
	if !got.Hint || got.Reason != AssistanceFormatHint {
		t.Fatalf("ChooseAssistance = %+v, want the hint re-triggered by fresh failures", got)
	}
}
