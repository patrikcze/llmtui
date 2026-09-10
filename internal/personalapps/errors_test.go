package personalapps

import (
	"errors"
	"testing"
)

// TestMessageOfNeverSurfacesTheWrappedCause guards the boundary Error.Err
// documents but Error.Error() does not itself enforce: MessageOf must return
// only Message, never format in Err, since callers use it specifically to
// put a model/human-safe string in front of a person (see mail_mutator.go
// and calendar_mutator.go's OutcomeUnknown Detail fields).
func TestMessageOfNeverSurfacesTheWrappedCause(t *testing.T) {
	cause := errors.New("/private/path/should/never/appear")
	err := wrap(&Error{Code: CodeAppUnavailable, Message: "could not reach Mail"}, cause)
	got := MessageOf(err)
	if got != "could not reach Mail" {
		t.Errorf("MessageOf = %q, want just the Message", got)
	}
	if errors.Is(err, cause) == false {
		t.Fatal("test setup: wrap did not preserve the cause for errors.Is")
	}
}

func TestMessageOfDefaultsForANonPackageError(t *testing.T) {
	if got := MessageOf(errors.New("boom")); got != "an internal error occurred" {
		t.Errorf("MessageOf = %q, want the generic fallback", got)
	}
}

func TestMessageOfNilError(t *testing.T) {
	if got := MessageOf(nil); got != "" {
		t.Errorf("MessageOf(nil) = %q, want empty", got)
	}
}
