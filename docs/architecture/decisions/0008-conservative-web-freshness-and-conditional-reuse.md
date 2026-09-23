# ADR 0008: Conservative web freshness and conditional reuse

Status: Accepted
Date: 2026-09-23

## Context

`web_fetch` had no reuse concept: every call was a fresh network request,
and a fetched page's extracted body was clipped to the preview cap with no
way to recover more of it or to know how stale a later reference to it was.
The motivating scenario is a two-turn conversation ("what's the forecast" /
"what about at 4pm") that should reuse the first fetch's retained body
rather than silently issuing a second network request, while an explicit
"what's the weather *now*" must never be answered from a stale snapshot
relabeled as current.

## Decision

`web.Page`/fetch options gained bounded response validators (ETag,
Last-Modified, Cache-Control-derived max-age, Date/Age), requested-vs-final
URL identity, and acquisition/extraction metadata; the extracted body is
retained (as an entity resource, per ADR 0004) before the preview cap
applies, not after. `web_fetch` gained an explicit `mode` argument —
`auto` (`web.FetchAuto`), `cached` (`web.FetchCached`), `refresh`
(`web.FetchRefresh`) — with `auto` as the conservative default: reuse a
retained body only while it is within origin-derived freshness (never a
manufactured TTL invented for an origin that specified none), issue a
conditional GET when origin validators make one possible, and fall through
to an ordinary approved GET otherwise. `cached` never touches the network,
even for a stale entry. `refresh` always issues an unconditional approved
GET. Reuse admission is controller-owned, not a raw cache lookup: a
`webRefreshEpoch` counter (`internal/tui/app.go`) is threaded through the
canonical call identity the progress ledger already fingerprints
(`internal/tui/progress.go`), so a `refresh`-mode call always gets a fresh
epoch and is never mistaken for a repeat of the `auto`-mode call that
preceded it, while two identical `auto` calls with no epoch change still
correctly collapse to one non-repeated network request. Redirects keep the
existing SSRF checks and approval on every hop; validators are dropped
if the target identity changes across a redirect, so a conditional
request's credentials-adjacent headers never leak to a different origin.

Alternatives considered and rejected: a universal fixed TTL applied
regardless of what the origin actually said about its own content
(rejected — would either serve stale data past the origin's own expiry or
discard perfectly valid cached data early, and either way misrepresents
what "fresh" means); treating a failed revalidation as a successful fresh
response by falling back to the old snapshot (rejected — an explicit
"current data" request must fail visibly, never silently receive stale
data wearing a fresh label); a generic HTTP response cache independent of
the entity registry (rejected — would duplicate ADR 0004's single-owner
body storage and create two competing eviction policies).

## Consequences

- The motivating two-turn forecast scenario issues exactly one network
  fetch; the follow-up question is answered from the retained body via the
  same resource-read path ADR 0004 established.
- A user who explicitly asks for current/refreshed data and gets `mode:
  refresh` always reaches the network, regardless of how fresh the cached
  entry looks.
- `read_file`/`grep` against a previously fetched page's resource ID never
  triggers a new network call, by construction (ADR 0004's "no hidden I/O
  on lookup" principle).
