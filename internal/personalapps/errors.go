package personalapps

import (
	"errors"
	"fmt"
)

// Code is the stable, model-facing failure classification. Codes are part of
// the tool contract: rename one only with a schema version bump.
type Code string

const (
	// CodeInvalidRequest marks a request rejected before any backend call.
	CodeInvalidRequest Code = "invalid_request"
	// CodePermissionDenied marks a refusal by the operating system.
	CodePermissionDenied Code = "permission_denied"
	// CodeAppUnavailable marks an app or helper that could not be reached.
	CodeAppUnavailable Code = "app_unavailable"
	// CodeScopeDenied marks a target outside the user-configured allowlist.
	CodeScopeDenied Code = "scope_denied"
	// CodeContentUnavailable marks content the app could not supply, for
	// example an unsynchronized body. It is never reported as empty success.
	CodeContentUnavailable Code = "content_unavailable"
	// CodeStaleReference marks a handle, version or plan that no longer
	// matches observed state.
	CodeStaleReference Code = "stale_reference"
	// CodePreconditionFailed marks observed state that contradicts the
	// expectations recorded when a plan was prepared.
	CodePreconditionFailed Code = "precondition_failed"
	// CodeInvalidTimezone marks an unknown IANA zone or an offset that
	// disagrees with the zone at that instant.
	CodeInvalidTimezone Code = "invalid_timezone"
	// CodeAmbiguousTime marks a wall-clock time that is skipped or repeated
	// by a DST transition and must be resolved by a human.
	CodeAmbiguousTime Code = "ambiguous_time"
	// CodeReadOnlyCalendar marks a write to a calendar that cannot accept it.
	CodeReadOnlyCalendar Code = "read_only_calendar"
	// CodeUnsupportedOperation marks a capability this build does not have.
	CodeUnsupportedOperation Code = "unsupported_operation"
	// CodeBridgeProtocolError marks malformed or unversioned helper output.
	CodeBridgeProtocolError Code = "bridge_protocol_error"
	// CodeRateLimited marks a refusal to exceed a host-side budget.
	CodeRateLimited Code = "rate_limited"
	// CodeJournalUnavailable marks durable intent that could not be
	// persisted. A mutation must not proceed past it.
	CodeJournalUnavailable Code = "journal_unavailable"
	// CodeOutcomeUnknown marks a mutation whose effect could not be
	// established. It never authorizes an automatic retry.
	CodeOutcomeUnknown Code = "outcome_unknown"
	// CodeInternal marks a host defect.
	CodeInternal Code = "internal"
)

// Sentinel errors for the conditions callers branch on. Wrap them with
// Errorf so a caller keeps both the code and the context.
var (
	// ErrUnsupportedPlatform is returned by the non-Darwin build and by any
	// adapter that cannot exist on the running platform.
	ErrUnsupportedPlatform = &Error{Code: CodeUnsupportedOperation, Message: "personal apps integration is only available on macOS"}
	// ErrNotConnected is returned when the user has not explicitly
	// connected the adapter this operation needs.
	ErrNotConnected = &Error{Code: CodeAppUnavailable, Message: "adapter is not connected"}
	// ErrDisabled is returned when the feature or the adapter is off.
	ErrDisabled = &Error{Code: CodeUnsupportedOperation, Message: "adapter is disabled"}
	// ErrScopeDenied is returned when a target is outside the allowlist.
	ErrScopeDenied = &Error{Code: CodeScopeDenied, Message: "target is outside the approved scope"}
	// ErrMutationsDisabled is returned when a change is requested while
	// mutations are off.
	ErrMutationsDisabled = &Error{Code: CodeUnsupportedOperation, Message: "mutations are disabled"}
	// ErrPlanNotFound is returned for an unknown, consumed or expired plan.
	ErrPlanNotFound = &Error{Code: CodeStaleReference, Message: "plan is unknown or expired"}
	// ErrPlanNotApproved is returned when apply runs without a human
	// approval bound to that exact plan digest.
	ErrPlanNotApproved = &Error{Code: CodePreconditionFailed, Message: "plan has not been approved"}
	// ErrUnknownHandle is returned for a handle this session never issued.
	ErrUnknownHandle = &Error{Code: CodeStaleReference, Message: "handle is unknown or has expired"}
)

// Error is a personal-apps failure carrying a stable Code. Message is safe
// to show a model: callers must not place message bodies, recipients,
// credentials or absolute private paths in it.
type Error struct {
	Code    Code
	Message string
	// Err is an optional wrapped cause. It is not shown to the model.
	Err error
}

func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Is treats two personal-apps errors as equal when their codes and messages
// match, so wrapped sentinels stay comparable with errors.Is.
func (e *Error) Is(target error) bool {
	var other *Error
	if !errors.As(target, &other) || e == nil || other == nil {
		return false
	}
	return e.Code == other.Code && e.Message == other.Message
}

// Errorf builds an *Error with a formatted, model-safe message.
func Errorf(code Code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// wrap attaches a cause to a sentinel without changing its identity.
func wrap(sentinel *Error, cause error) *Error {
	return &Error{Code: sentinel.Code, Message: sentinel.Message, Err: cause}
}

// CodeOf reports the stable code for err, defaulting to CodeInternal for an
// error this package did not produce. A nil error has no code.
func CodeOf(err error) Code {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInternal
}

// MessageOf reports the model-safe message for err, empty for a nil error.
// It never surfaces a wrapped cause (Error.Err): that field is kept for Go's
// own error chain (errors.Is/As, %w — see Error.Error()'s own use of it),
// not for display, and every caller that puts a message in front of a model
// or a human must go through this accessor instead of err.Error() to keep
// that boundary. A bridge-reported failure's Message is already bounded
// (bridgeErrorToDomain clips it) specifically so it can be shown, unlike a
// generic host-side error, which falls back to a fixed, content-free string.
func MessageOf(err error) string {
	if err == nil {
		return ""
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "an internal error occurred"
}

// StatusOf maps an error onto the Status a Result should report.
func StatusOf(err error) Status {
	switch CodeOf(err) {
	case "":
		return StatusOK
	case CodePermissionDenied:
		return StatusDenied
	case CodeScopeDenied:
		return StatusDenied
	case CodeUnsupportedOperation:
		return StatusUnsupported
	case CodeStaleReference:
		return StatusStale
	case CodeOutcomeUnknown:
		return StatusOutcomeUnknown
	default:
		return StatusError
	}
}
