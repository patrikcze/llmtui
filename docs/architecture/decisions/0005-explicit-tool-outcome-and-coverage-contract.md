# ADR 0005: Explicit tool outcome and coverage contract

Status: Accepted
Date: 2026-09-22

## Context

`tools.Result` previously carried only `Output string`/`Err error` (plus a
display-only `Diff`). Whether a call actually succeeded, partially covered
its source, or failed had to be inferred by the caller — the agent receipt
ledger and the progress-ledger repeat-detector both parsed *formatted output
text* and Go error strings to classify results, which meant a cosmetic
change (a timestamp, a re-worded message, a newly allocated ID) could
silently defeat repeat detection or misclassify a bounded-but-successful
observation as a failure. A storage-adjacent limitation (a capped scan, a
truncated capture) had no way to be distinguished from an outright failure.

## Decision

`tools.Result` gained an additive `Meta ResultMeta` field
(`internal/tools/result.go`): a closed `Outcome` vocabulary
(`ok`/`partial`/`failed`/`cancelled`/`timeout`/`unknown`), a stable
`ErrorInfo{Code, Retry, Message}` classification drawn from a closed,
tested vocabulary (`errorCodeVocabulary`, pinned by a golden-list test),
`Coverage{SourceComplete, CaptureComplete, PreviewComplete, ObservedBytes,
RetainedBytes, TotalBytes, TotalLines, Reasons}`, an optional `Window` for
paginated reads, and `Effect` (`none`/`changed`/`unchanged`/`unknown`) kept
strictly separate from the controller-owned `agent.ActionStatus`
(`executed`/`denied`/`blocked`/`unknown`) — producers never set the latter,
and the two are combined only by the caller that already owns both
("user denied," "controller blocked," "tool failed"). Every producer in the
tool inventory populates `Meta`; the shared result formatter renders
byte-identical text to the pre-`Meta` implementation, proven by a snapshot
test, so the change was structural and invisible to models until
`progressDigest` (`internal/tui/progress.go`) was switched to key off this
stable typed content instead of formatted text.

Alternatives considered and rejected: continuing to classify by parsing
`Output`/`Err.Error()` prose (rejected — the exact bug class this ADR fixes);
a single ambiguous `Truncated bool` flag conflating source-incomplete,
capture-incomplete, and preview-incomplete into one bit (rejected — §14's
"complete retained prefix of an incomplete source is still incomplete source
evidence" principle requires the three to stay distinguishable); letting a
retention/storage failure downgrade a successfully-executed operation's
outcome to failed (rejected — a command that ran to completion and only
failed to be *retained* for later read-back is still a successful execution;
conflating the two would make retry logic replay an already-completed
side effect).

## Consequences

- Success, valid-empty, partial, failure, denial, controller-block, timeout,
  cancellation, and unknown-effect are independently observable from typed
  fields alone — verified directly by a dedicated test constructing all nine
  states with identical `Output`/`Err` text and asserting distinct
  signatures.
- `progressDigest` now hashes stable operation content, not rendered
  strings; a repeated call with a different call ID or timestamp is still
  recognized as a repeat, and a genuinely different `Outcome`/`Coverage`
  is still recognized as new evidence.
- Every later phase (resource bodies, ranged reads, search pagination, edit
  versioning, web freshness) extends `ResultMeta` rather than inventing a
  parallel status type.
