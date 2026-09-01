# ADR 0003: Establish the task contract before agent execution

Status: Accepted
Date: 2026-09-01

## Context

The original `/agent on` controller pinned semantic acceptance criteria in the
first post-execution verifier request. Cycle 1 could therefore dispatch the
executor, including tools subject to the normal approval path, before the
harness had a stable task contract.

## Decision

Before cycle 1, the controller sends one bounded fresh-context request with no
tool schemas. It validates a small criteria-or-user-input envelope and pins the
result into the existing `AgentRun.Criteria` with controller-assigned IDs. The
original request remains immutable. Contract content cannot grant tools,
permissions, network access, credentials, or any instruction precedence.

The contract request uses the active executor model, the existing verifier
token/timeout bounds, response-constraint capability fallback, one malformed
control repair, run-scoped cancellation, and run token/time accounting. A
contract failure parks or exhausts the run without dispatching an executor.

Post-execution semantic verification now evaluates pinned criteria; it does not
establish initial scope. The shared `dispatch`/stream/tool/approval kernel is
unchanged.

Older persisted runs with no criteria take the contract stage on their next
fresh `/agent resume` cycle. They never replay an incomplete exchange or run
under an unpinned contract.

## Consequences

- A normal executable run has `HasCriteria() == true` before its first
  `BeginCycle`/executor request.
- Contract generation adds one bounded provider request and token cost.
- Ordinary chat, tool approval policy, provider protocol, MCP behavior, and
  workspace restrictions do not change.
