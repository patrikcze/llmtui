# Verified Agent Loop Design

## Context

`llmtui` already has an agentic tool loop: a user message is composed with
system rules, active skills, memory, RAG context, conversation history, and
tool schemas; the provider streams a response; approved tools run
asynchronously; results are appended in provider-native form; and the model is
called again. Requests and tool batches are cancellable, provider retries and
tool rounds are bounded, side effects are journaled, and every mutating or
external action still passes through the existing approval policy.

That loop is reliable for one conversational turn, but completion is still a
model assertion. There is no explicit run/cycle record, no independent
verification request, no cross-cycle progress record, and no deterministic
policy that decides whether to continue or stop. The current tool-round budget
can also be renewed indefinitely by the user, which is appropriate for normal
chat but not for an autonomous run.

## Architecture assessment

### What is retained

- `internal/prompt` remains the single deterministic rules/context composer.
  Its precedence is system prompt, template and explicitly active skills,
  helper rules, bounded conversation state, user-authored memory, and framed
  untrusted RAG data. Agent-cycle directives are an additional high-priority
  system section, not a second instruction loader.
- The current TUI dispatch, streaming, native/fenced tool protocols, approval
  UI, guardrails, asynchronous tool execution, MCP timeouts, operation journal,
  cache, history, RAG, memory, skills, and provider adapters remain in place.
- A cycle's executor is one existing model/tool turn. It may contain several
  related tool calls, but it cannot recursively start a new agent run.
- Bubble Tea messages remain the only way asynchronous completion changes UI
  state. Core state types do not import Bubble Tea or a provider implementation.

### What is added

- `internal/agent` is a small provider- and UI-independent state machine for a
  stable run ID, cycle records, limits, lifecycle events, typed errors,
  verification results, and explicit stop decisions.
- A TUI adapter starts an opt-in run from a user message, adds one bounded
  objective to the existing prompt, accounts for tool results, and invokes the
  stop policy at every cycle boundary.
- Verification is a second provider request with a new message slice containing
  only an evaluator system prompt and bounded observable evidence. It never
  continues the executor conversation and receives no tools or hidden
  reasoning. The same provider/model may be reused for local inference while
  still getting a fresh context.
- `internal/agent` run storage uses a versioned JSON schema, owner-only files,
  atomic replacement, size/count limits, and an in-memory implementation for
  tests. Corrupt records are reported and ignored; they never prevent chat from
  starting.

## Adaptation of the six stages

1. **Trigger** — when agent mode is enabled, a normal user send creates one
   run. Tool completion remains part of the current cycle. `/agent resume`
   resumes a persisted parked or interrupted run; `/agent cancel` and Escape
   cancel the current run. Ordinary chat bypasses all of this.
2. **Rules load** — the existing composer loads system rules, explicit skills,
   bounded history, user memory, RAG, and tool definitions once per request.
   The run adapter supplies cycle objective and verification feedback as
   clearly labelled state. No filename such as `CLAUDE.md` is hard-coded.
3. **Executor** — one cycle has one concrete objective. Existing tool schemas,
   guardrails, approvals, streaming, and side-effect journal are authoritative.
4. **Verifier** — a fresh, tool-free request grades observable evidence. A
   strict, bounded JSON parser accepts a small documented envelope. Deterministic
   cancellation, denial, timeout, tool, build, or test failures override an
   optimistic model verdict.
5. **Memory write** — after verification, only bounded summaries, failed
   criteria, artifacts, and the next objective are stored. Full transcripts,
   tool bodies, secrets, and model reasoning are excluded.
6. **Stop check** — a pure policy returns `done`, `continue`, `retry`,
   `needs_user_input`, `parked`, `escalated`, `cancelled`, `failed`, or
   `budget_exhausted`. Max cycles, total tool calls, elapsed time, repeated
   failure fingerprints, cancellation, denial, and provider failures are hard
   stops. A retry requires a changed objective/strategy, new evidence, or a
   bounded transient failure.

## Compatibility decision

The feature is disabled by default (`agent.enabled: false`) and can be toggled
for a session with `/agent on`. This is intentionally conservative: verification
adds latency and another model request, and small local models may not reliably
emit the control envelope. With agent mode off, send/dispatch/cache/history/tool
behavior and all existing public Go APIs are unchanged.

Agent mode reuses the active provider and model for verification in the first
version. A separate request context provides evaluator isolation without
requiring another model to fit in local memory. A separately configured
verifier provider is deferred because provider construction, ownership, and
hot switching would add lifecycle risk; a verifier-model override on the same
provider is safe and sufficient for OpenAI-compatible servers that expose more
than one model.

## Limits and failure policy

Defaults are eight cycles, 32 total tool calls, 100,000 executor/verifier
tokens, 30 minutes elapsed time, three repeated equivalent failures, 1,024
verifier output tokens, and 64 KiB of persisted run memory. Limits are
validated at the boundary. The run never asks the model to extend them. A
permission denial produces `needs_user_input`; an
unsafe action or non-retryable invariant produces `failed`/`escalated`; user
cancellation produces `cancelled`; cycle/tool/time exhaustion produces
`budget_exhausted`.

Verifier transport or malformed-output failures are explicit. They may cause a
changed retry while budget remains, but identical failures reach the repeated
failure stop. Model text is never interpreted as permission or authority.

## Observability

The state machine emits ordered, bounded lifecycle records with time, run ID,
cycle, stage, kind, and a short redacted summary. The TUI renders only the
latest concise status and exposes the same fields in `/debug last`; persisted
records contain no prompt bodies or tool output. Event publication is a normal
Bubble Tea command/message and therefore cannot block streaming.

## Consequences

- Positive: completion becomes evidence-based and bounded; retries carry a
  specific changed objective; interrupted runs have concise recoverable state;
  and ordinary chat is untouched.
- Positive: the pure policy and storage boundary can be tested without a TUI,
  filesystem, or live model.
- Negative: agent mode costs an additional inference per completed cycle and
  small models may require a retry after malformed verifier JSON.
- Negative: deterministic command semantics are necessarily conservative.
  `llmtui` can prove a command/tool error, denial, or recorded test failure, but
  it cannot infer every acceptance criterion from arbitrary shell output.
- Deferred: parallel agents, background triggers, cron/webhooks, automatic
  approval, a second verifier provider process, and unbounded autonomous runs.
