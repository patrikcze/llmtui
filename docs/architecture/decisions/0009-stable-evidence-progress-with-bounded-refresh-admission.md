# ADR 0009: Stable evidence progress with bounded refresh admission

Status: Accepted
Date: 2026-09-23

## Context

The existing repeat-call ledger (`internal/tui/progress.go`) fingerprints a
call's canonical identity to block genuinely repeated, non-progressing tool
calls. Once results carried resource IDs, byte/line windows, and web
refresh state (ADRs 0004, 0008), the ledger needed to keep treating a
"different window/version/epoch of the same target" as real progress while
still blocking a caller that merely varies an incidental token (a new call
ID, a re-worded freshness string) to evade the guard — without building a
second, parallel repetition detector.

## Decision

The existing ledger's canonical-identity fingerprint was extended, not
replaced: a resource read's identity includes the entity body's digest and
the normalized window/query/cursor interval, so lines 1–100 and 101–200 of
the same file fingerprint as distinct useful operations while an identical
repeated window collapses to one. A web fetch's identity includes its cache
mode and `webRefreshEpoch` (ADR 0008), so an `auto`-mode cache hit twice
produces no new fingerprint (no network, no new progress claim) while a
`refresh`-mode call — which always carries a freshly incremented epoch —
is never conflated with the `auto` call before it, and a caller cannot mint
unlimited epochs simply by varying an opaque token: epoch admission is
controller-owned state (`m.webRefreshEpoch`, incremented only by the
controller path that decided a refresh was warranted), not something a call
argument can request directly. `ResultMeta`'s stable digest (ADR 0005)
feeds this fingerprint for resource/search reads; a stale-edit rejection's
identity includes the expected/current version digests (ADR 0007) so a
repeated stale attempt is recognized as a repeat while a corrected retry
against the right version is recognized as new.

Alternatives considered and rejected: a second, independent repetition
detector layered specifically for resource/web/version-aware calls
(rejected — two ledgers making inconsistent repeat/progress calls is worse
than one ledger correctly extended, and the plan explicitly rules this out
as scope creep); using a newly allocated ID or a request timestamp as a
progress signal (rejected — a fresh random entity ID or wall-clock time
changes on every call regardless of whether anything useful actually
happened, which is precisely the false-progress bug ADR 0005 also
addresses at the `Result.Meta` layer); letting a model-supplied freshness
token alone authorize a new web refresh epoch (rejected — that is a direct
polling-bypass vector, since a model could simply vary the token string on
every call to defeat the guard).

## Consequences

- Legitimate pagination through a large file or search result set is never
  blocked as a false repeat, while an identical repeated page is.
- A model cannot escape the no-progress guard on a stale edit or a
  redundant web fetch by changing only a call ID, timestamp, or freshness
  string — the ledger's identity is built from stable, controller-verified
  content, not caller-supplied incidentals.
- Web refresh admission stays subject to the same global tool/token/time
  budgets the ledger already enforces; it is one more identity dimension
  inside the existing mechanism, not a parallel budget system.
