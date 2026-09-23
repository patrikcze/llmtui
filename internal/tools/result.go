package tools

import (
	"context"
	"errors"

	"github.com/patrikcze/llmtui/internal/entity"
)

// This file is Phase 1a of the next-generation tool runtime plan
// (.claude/tasks/plans/next-generation-tool-runtime.md §13/§23): it gives
// every tools.Result a typed, structured outcome/coverage/window, separate
// from agent.ActionStatus (execution admission: executed/denied/blocked/
// unknown, owned by the controller/batch code and never set here). Producers
// populate ResultMeta at the point they build a Result; a later phase adds a
// reusable retained-body substrate (Phase 2b) and renders Meta into
// model-visible text (also Phase 2b+) — this phase only adds and populates
// the metadata itself.
//
// ResourceID and Observation are deliberately omitted from ResultMeta this
// phase: both depend on entity.ObservationMetadata, which does not exist
// until Phase 2b defines it. Do not add zero-value placeholders for them now.

// Outcome is the producer's own typed classification of what happened,
// independent of agent.ActionStatus. A denied or ledger-blocked call never
// reaches a producer, so it never gets an Outcome from this package; the
// controller layer that synthesizes those results uses OutcomeUnknown (see
// internal/tui/progress.go and internal/tui/tool_search.go).
type Outcome string

const (
	// OutcomeUnknown means whether the operation succeeded could not be
	// established — never treat this as either success or failure.
	OutcomeUnknown Outcome = "unknown"
	// OutcomeOK means the operation completed within its requested bounds.
	// Empty valid output (zero entries, zero bytes) is still OutcomeOK — no
	// fabricated failure for a genuinely empty successful result.
	OutcomeOK Outcome = "ok"
	// OutcomePartial means a successful bounded observation, not proof of an
	// exhaustive result — see Coverage for exactly what was left out.
	OutcomePartial Outcome = "partial"
	OutcomeFailed  Outcome = "failed"
	// OutcomeCancelled means the caller (user or controller) cancelled the
	// operation before it completed.
	OutcomeCancelled Outcome = "cancelled"
	// OutcomeTimeout means the operation's own deadline expired. An executed
	// command timeout can still have Effect=EffectUnknown — a timeout never
	// implies the command made no change.
	OutcomeTimeout Outcome = "timeout"
)

// Effect classifies what a call did to the outside world, independent of
// Outcome (a command can time out and still have changed something).
type Effect string

const (
	EffectNone      Effect = "none"
	EffectChanged   Effect = "changed"
	EffectUnchanged Effect = "unchanged"
	EffectUnknown   Effect = "unknown"
)

// Retry vocabulary for ErrorInfo.Retry — closed for this phase.
const (
	RetryNone         = "none"
	RetryCorrectInput = "correct_input"
	RetryReread       = "reread"
	RetryLater        = "later"
	RetryReconnect    = "reconnect"
)

// ErrorInfo is the closed, stable, model-safe classification of a failure.
// Code is never err.Error() — it is one of a fixed vocabulary a caller can
// switch on without parsing prose. See errorCodeVocabulary in this file for
// the exact set this phase may use.
type ErrorInfo struct {
	// Code is closed and stable; see errorCodeVocabulary.
	Code string
	// Retry is one of the RetryXxx constants.
	Retry string
	// Message is a bounded, model-safe, actionable summary — not necessarily
	// identical to the wrapped Go error's full text.
	Message string
}

// errorCodeVocabulary is the closed set of ErrorInfo.Code values this runtime
// may produce. Search cursors and retained-body lookup add their own stable
// classifications without changing the older read/write vocabulary.
var errorCodeVocabulary = map[string]bool{
	"invalid_arguments":        true,
	"invalid_pattern":          true,
	"not_found":                true,
	"range_after_eof":          true,
	"unsupported_content":      true,
	"encoding_loss":            true,
	"ambiguous_match":          true,
	"match_not_found":          true,
	"no_change":                true,
	"network":                  true,
	"dns":                      true,
	"http_status":              true,
	"unsupported_content_type": true,
	"cancelled":                true,
	"timeout":                  true,
	"permission_denied":        true,
	"safety_block":             true,
	"repeat_block":             true,
	"budget_block":             true,
	"outcome_unknown":          true,
	// symlink_write_unsupported (Phase 1b): write_file/edit_file's target, or
	// an existing parent directory component, is a symbolic link. Write
	// admission rejects this outright rather than writing through or
	// replacing the link — see rejectSymlinkWriteTarget in file_write.go.
	"symlink_write_unsupported": true,
	// resource_unavailable/resource_expired (resource reads): the requested
	// retained body is absent, the wrong entity kind, or its lease lifetime has
	// ended.
	"resource_unavailable": true,
	// capture_limit (Phase 3): a requested late line window lies beyond the
	// bounded scan budget, so the source was not exhaustively inspected.
	"capture_limit":         true,
	"source_changed":        true,
	"resource_expired":      true,
	"stale_source":          true,
	"cursor_expired":        true,
	"retention_unavailable": true,
	"wrong_resource_kind":   true,
	"snapshot_incomplete":   true,
	"cache_miss":            true,
}

// Coverage states how much of the intended source a producer actually
// inspected, retained, and showed in this result. All three booleans default
// false (zero value); a producer sets them true explicitly once it knows.
type Coverage struct {
	// SourceComplete is true only when the producer actually inspected all
	// intended source (the whole file, the whole directory, the whole
	// matched set) — not merely "returned everything it decided to look at."
	SourceComplete bool
	// CaptureComplete is true when all produced body bytes were retained (no
	// reusable retention substrate exists yet this phase, so this is
	// currently synonymous with "not truncated before capture").
	CaptureComplete bool
	// PreviewComplete is true when all retained bytes are shown in this view.
	PreviewComplete bool
	// ObservedBytes is measured from what the producer actually read, never
	// asserted from a peer-supplied Content-Length or similar.
	ObservedBytes int64
	// RetainedBytes is how many of those observed bytes made it into Output.
	RetainedBytes int64
	// TotalBytes/TotalLines are nil when genuinely unknown; a producer with a
	// hard cap can leave these nil rather than guess.
	TotalBytes *int64
	TotalLines *int64
	// Reasons is a small, bounded vocabulary of why coverage is incomplete
	// (e.g. "bytes", "files", "matches", "large", "unreadable", "binary").
	// It is not the §23 ErrorInfo.Code vocabulary — Coverage never carries an
	// error, only a completeness explanation for an otherwise-successful
	// observation.
	Reasons []string
}

// Window is the position of a bounded view inside a larger retained
// representation. A zero Window (nil in ResultMeta) means the concept does
// not apply to this tool (list_dir, glob — pagination is a later phase).
type Window struct {
	// StartLine/EndLine are 1-based inclusive; zero for an empty window.
	StartLine, EndLine int64
	// StartByte/EndByte are 0-based half-open source coordinates when the
	// producer can establish them; zero is valid for a range at file start.
	StartByte, EndByte int64
	// NextOffset is the next complete line to request, only when valid.
	NextOffset *int64
	// NextByteOffset is set only when a byte continuation is safe and useful
	// for the observed source (for example, a partial long line).
	NextByteOffset *int64
	// NextCursor is an opaque, bounded continuation token — never a path.
	NextCursor string
	// PartialLine reports whether EndLine's content was itself truncated.
	PartialLine bool
}

// EncodingInfo describes the raw bytes inspected for a file observation.
// Complete distinguishes a property established over the whole source from a
// property observed only in a bounded prefix/window.
type EncodingInfo struct {
	Name      string
	UTF8Valid bool
	CRLF      bool
	NUL       bool
	Lossy     bool
	Complete  bool
}

// SearchCoverage separates the immutable captured result set from the page
// shown in this response and from the source that was actually scanned.
type SearchCoverage struct {
	MatchesReturned int64
	MatchesCaptured int64
	MatchesTotal    *int64
	FilesEligible   int64
	FilesScanned    int64
	SourceBytes     int64
	Skipped         map[string]int64
	TextComplete    bool
}

// ResultMeta is the additive, typed outcome/coverage/window envelope every
// tool result now carries. Outcome lives entirely here; agent.ActionStatus
// (executed/denied/blocked/unknown) stays controller/batch-owned and is
// never set from this package — combining the two ("user denied," "tool
// failed," "controller blocked") is the caller's job, not this struct's.
type ResultMeta struct {
	Outcome  Outcome
	Error    *ErrorInfo
	Coverage Coverage
	Window   *Window
	// ContentDigest is a stable hash of the retained representation; empty
	// when no rendered body was retained.
	ContentDigest string
	// SourceDigest is a SHA-256 of the complete raw source, populated only for
	// a complete, stable file observation. It is never a prefix hash.
	SourceDigest string
	// FileVersion is present only when SourceDigest covers the complete file.
	FileVersion *entity.FileVersion
	Encoding    EncodingInfo
	Search      SearchCoverage
	// Snapshot is an internal handoff for a complete eligible file body. It is
	// consumed by the controller for one bounded resource publication and is
	// never formatted, serialized, or sent directly to a provider.
	Snapshot []byte
	// Precondition reports version when an observed file version guarded an
	// edit, or exact_text_only for the compatibility path.
	Precondition string
	// Effect classifies what the call did to the outside world: none |
	// changed | unchanged | unknown.
	Effect Effect
	// Reused is always false this phase — the field exists for Phase 2b+,
	// which adds a reuse substrate this package does not have yet.
	Reused bool
}

// classifiedError attaches a stable ErrorInfo.Code/Retry to an existing Go
// error without changing its Error() text, so FormatResults/NativeResults
// output is byte-identical whether or not a producer has been adapted to set
// a code. Unwrap preserves errors.Is/errors.As through the wrap (e.g.
// errors.Is(result.Err, tools.ErrDenied) or errors.Is(err, os.ErrNotExist)
// keep working exactly as before).
type classifiedError struct {
	err   error
	code  string
	retry string
}

func (e *classifiedError) Error() string { return e.err.Error() }
func (e *classifiedError) Unwrap() error { return e.err }

// withCode wraps err with a stable ErrorInfo.Code/Retry pair drawn from
// errorCodeVocabulary. Passing a code outside that vocabulary is a
// programmer error (panics in tests via the vocabulary assertion below is
// deliberately avoided in production; callers in this package only ever pass
// literal constants that are covered by the shared_test.go vocabulary test).
func withCode(err error, code, retry string) error {
	if err == nil {
		return nil
	}
	return &classifiedError{err: err, code: code, retry: retry}
}

// errorInfoFor derives the closed ErrorInfo for a producer's returned error.
// It prefers a code a producer explicitly attached with withCode; otherwise
// it falls back to the two generic signals every Go context carries
// (cancellation, deadline) and, failing those, an uncoded failure — Outcome
// alone (OutcomeFailed) still tells a caller the call did not succeed even
// when no closed code applies.
func errorInfoFor(err error) *ErrorInfo {
	if err == nil {
		return nil
	}
	var ce *classifiedError
	if errors.As(err, &ce) {
		return &ErrorInfo{Code: ce.code, Retry: ce.retry, Message: boundErrorMessage(err)}
	}
	switch {
	case errors.Is(err, context.Canceled):
		return &ErrorInfo{Code: "cancelled", Retry: RetryNone, Message: boundErrorMessage(err)}
	case errors.Is(err, context.DeadlineExceeded):
		return &ErrorInfo{Code: "timeout", Retry: RetryLater, Message: boundErrorMessage(err)}
	default:
		return &ErrorInfo{Message: boundErrorMessage(err)}
	}
}

// outcomeFor derives Outcome from a producer's returned error using the same
// two generic Go-context signals errorInfoFor does, so the two never
// disagree about cancellation/timeout.
func outcomeFor(err error) Outcome {
	switch {
	case err == nil:
		return OutcomeOK
	case errors.Is(err, context.Canceled):
		return OutcomeCancelled
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimeout
	default:
		return OutcomeFailed
	}
}

// boundErrorMessage caps a Go error's text for ResultMeta.Error.Message. It
// never changes what FormatResults/NativeResults show the model — those
// still render the full res.Err.Error(); Message is a separate, bounded
// summary for typed consumers.
func boundErrorMessage(err error) string {
	return truncateLine(err.Error(), 300)
}

// finalizeMeta fills Outcome/Error from err when a producer has not already
// set a more specific Outcome (OutcomeUnknown/"" is the "not yet decided"
// sentinel a producer leaves when it wants the generic classification).
// Producers that need a custom Outcome (get_entity_details' all-invalid/
// mixed/empty-query mapping, personal_apps' domain Status mapping) set
// meta.Outcome themselves before calling this, or skip it entirely.
func finalizeMeta(meta ResultMeta, err error) ResultMeta {
	if meta.Outcome == "" {
		meta.Outcome = outcomeFor(err)
	}
	if meta.Error == nil && err != nil {
		meta.Error = errorInfoFor(err)
	}
	return meta
}

// int64Ptr is a small helper so producers can populate Coverage.TotalBytes/
// TotalLines and Window.NextOffset without a local variable at each call
// site.
func int64Ptr(v int64) *int64 { return &v }
