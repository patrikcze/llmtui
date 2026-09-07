package personalapps

import (
	"errors"
	"testing"
)

func TestOperationValid(t *testing.T) {
	for _, op := range Operations() {
		if !op.Valid() {
			t.Errorf("Operations() returned %q but Valid() is false", op)
		}
	}
	for _, op := range []Operation{"", "mail", "MAIL_SEARCH", "mail_send", "run_command"} {
		if Operation(op).Valid() {
			t.Errorf("Operation(%q).Valid() = true, want false", op)
		}
	}
}

func TestOperationsReturnsCopy(t *testing.T) {
	first := Operations()
	first[0] = "tampered"
	if got := Operations()[0]; got != OpStatus {
		t.Fatalf("Operations()[0] = %q after mutating a returned slice, want %q", got, OpStatus)
	}
}

func TestOperationAdapterAndEffect(t *testing.T) {
	tests := []struct {
		op      Operation
		adapter Adapter
		effect  Effect
	}{
		{OpStatus, AdapterNone, EffectMetadata},
		{OpMailAccounts, AdapterMail, EffectMetadata},
		{OpMailMailboxes, AdapterMail, EffectMetadata},
		{OpMailSearch, AdapterMail, EffectMetadata},
		{OpMailRead, AdapterMail, EffectRead},
		{OpCalendarList, AdapterCalendar, EffectMetadata},
		{OpCalendarEvents, AdapterCalendar, EffectRead},
		{OpCalendarEvent, AdapterCalendar, EffectRead},
		{OpCalendarFreeSlots, AdapterCalendar, EffectRead},
		{OpChangePrepare, AdapterNone, EffectRead},
		{OpChangeApply, AdapterNone, EffectMutate},
		{OpOpenItem, AdapterNone, EffectNavigate},
	}
	if len(tests) != len(Operations()) {
		t.Fatalf("classification table covers %d operations, vocabulary has %d", len(tests), len(Operations()))
	}
	for _, tt := range tests {
		t.Run(string(tt.op), func(t *testing.T) {
			if got := tt.op.Adapter(); got != tt.adapter {
				t.Errorf("Adapter() = %q, want %q", got, tt.adapter)
			}
			if got := tt.op.Effect(); got != tt.effect {
				t.Errorf("Effect() = %q, want %q", got, tt.effect)
			}
		})
	}
}

// An unknown operation must never be classified as harmless.
func TestUnknownOperationEffectIsMutate(t *testing.T) {
	if got := Operation("wat").Effect(); got != EffectMutate {
		t.Fatalf("Effect() = %q for an unknown operation, want %q", got, EffectMutate)
	}
}

func TestErrorIsMatchesWrappedSentinel(t *testing.T) {
	wrapped := wrap(ErrScopeDenied, errors.New("mailbox 4 not allowed"))
	if !errors.Is(wrapped, ErrScopeDenied) {
		t.Fatalf("errors.Is(wrapped, ErrScopeDenied) = false")
	}
	if errors.Is(wrapped, ErrNotConnected) {
		t.Fatalf("errors.Is(wrapped, ErrNotConnected) = true, want false")
	}
	if got := CodeOf(wrapped); got != CodeScopeDenied {
		t.Fatalf("CodeOf() = %q, want %q", got, CodeScopeDenied)
	}
}

func TestErrorUnwrapKeepsCause(t *testing.T) {
	cause := errors.New("boom")
	err := wrap(ErrPlanNotFound, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("errors.Is(err, cause) = false, want true")
	}
}

func TestCodeOfAndStatusOf(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		code   Code
		status Status
	}{
		{"nil", nil, "", StatusOK},
		{"foreign", errors.New("x"), CodeInternal, StatusError},
		{"permission", &Error{Code: CodePermissionDenied}, CodePermissionDenied, StatusDenied},
		{"scope", ErrScopeDenied, CodeScopeDenied, StatusDenied},
		{"unsupported", ErrUnsupportedPlatform, CodeUnsupportedOperation, StatusUnsupported},
		{"stale", ErrUnknownHandle, CodeStaleReference, StatusStale},
		{"unknown outcome", &Error{Code: CodeOutcomeUnknown}, CodeOutcomeUnknown, StatusOutcomeUnknown},
		{"timeout is not success", &Error{Code: CodeBridgeProtocolError}, CodeBridgeProtocolError, StatusError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CodeOf(tt.err); got != tt.code {
				t.Errorf("CodeOf() = %q, want %q", got, tt.code)
			}
			if got := StatusOf(tt.err); got != tt.status {
				t.Errorf("StatusOf() = %q, want %q", got, tt.status)
			}
		})
	}
}
