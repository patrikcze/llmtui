# ADR 0001: Retain the single orchestration kernel; do not introduce a second execution path

Status: Accepted
Date: baseline commit `0458b5f`, branch `feat/v1-agent-runtime`

## Context

Master-prompt §6.2 requires determining whether the project currently has
multiple independent orchestration loops for model calls and tool execution,
since a divergent second loop was the leading hypothesis for the reported
repeated-`web_search`/`web_fetch` failure (§2).

## Decision

We will **not** build a new orchestration kernel or merge two loops into
one, because the audit (`v1-audit.md` §2-3) found only one already exists.
`/agent on` (`internal/agent` + `internal/tui/agent_loop.go`) is a state
machine that wraps the same `dispatch`/`continueChat`/`startRequest`/
`handleStreamEvent`/`startToolBatch`/`sendToolResults` primitives that
ordinary chat and tool-enabled chat use. This was verified directly against
source (`app.go:1676-1694`, `agent_loop.go:132-206`), not inferred from
documentation.

The v1 work therefore targets the kernel's **behavior gaps** — missing
tool-call-level progress detection and non-live budget enforcement (see ADR
0002) — rather than a structural rewrite of how requests are dispatched.

## Consequences

- No "unify the two loops" implementation slice is needed. Removing this
  from scope keeps the v1 release focused on the confirmed defects instead
  of a speculative rewrite, consistent with master-prompt §3.1/§3.2
  ("do not begin with a large rewrite," "preserve what is already good").
- Any future contribution that adds a second path to reach the model/tool
  primitives (e.g. a new autonomous mode that streams or executes tools
  outside `dispatch`/`startToolBatch`) must be treated as a regression
  against this decision and requires a new ADR superseding this one.
- The state-machine and progress-ledger work in `v1-agent-runtime.md`
  applies to the shared kernel, so it benefits both ordinary tool chat and
  `/agent on` simultaneously — it is not agent-mode-only work.
