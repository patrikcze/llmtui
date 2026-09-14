package agent

import (
	"errors"
	"fmt"
)

// ErrorKind classifies failures for stop policy and diagnostics.
type ErrorKind string

const (
	ErrorProvider          ErrorKind = "provider"
	ErrorMalformedResponse ErrorKind = "malformed_model_response"
	ErrorToolValidation    ErrorKind = "tool_validation"
	ErrorToolExecution     ErrorKind = "tool_execution"
	ErrorPermissionDenied  ErrorKind = "permission_denied"
	ErrorSafety            ErrorKind = "safety_constraint"
	ErrorCancelled         ErrorKind = "cancelled"
	ErrorTimeout           ErrorKind = "timeout"
	ErrorTruncated         ErrorKind = "truncated_response"
	ErrorVerification      ErrorKind = "verification"
	ErrorMemoryRead        ErrorKind = "memory_read"
	ErrorMemoryWrite       ErrorKind = "memory_write"
	ErrorBudget            ErrorKind = "budget_exhaustion"
	ErrorInvariant         ErrorKind = "internal_invariant"
)

var (
	ErrMalformedControl  = errors.New("malformed model control output")
	ErrPermissionDenied  = errors.New("tool permission denied")
	ErrBudgetExhausted   = errors.New("agent budget exhausted")
	ErrInvalidTransition = errors.New("invalid agent stage transition")
)

// RunError is a serializable, classifiable error. Cause is retained in memory
// for errors.Is/errors.As but excluded from persistence.
type RunError struct {
	Kind    ErrorKind `json:"kind"`
	Op      string    `json:"op,omitempty"`
	Message string    `json:"message"`
	// Resource is the specific instance a tool failure acted on (the same
	// dedup-relevant detail recorded on ToolCallRecord.Detail — a path, URL,
	// or search pattern), additive to schema v1. Empty on errors with no
	// narrow resource identity and on records persisted before this field
	// existed; recovery attribution falls back to Op alone in that case. See
	// recoveredToolError in recovered.go.
	Resource string `json:"resource,omitempty"`
	Cause    error  `json:"-"`
}

func (e RunError) Error() string {
	if e.Op == "" {
		return e.Message
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Message)
}

// Unwrap exposes the underlying error for errors.Is/errors.As.
func (e RunError) Unwrap() error { return e.Cause }

// NewError constructs a bounded error record while preserving cause.
func NewError(kind ErrorKind, op string, err error) RunError {
	if err == nil {
		err = errors.New("unknown error")
	}
	return RunError{Kind: kind, Op: op, Message: truncate(err.Error(), 512), Cause: err}
}

// NewToolError constructs a bounded tool-failure error record with resource
// attribution, so recovery (see recoveredToolError) can require the same
// resource to have later succeeded rather than merely the same tool name.
// Resource is the failed call's dedup-relevant detail; pass "" when the tool
// has none, and attribution falls back to op alone.
func NewToolError(kind ErrorKind, op, resource string, err error) RunError {
	e := NewError(kind, op, err)
	e.Resource = resource
	return e
}
