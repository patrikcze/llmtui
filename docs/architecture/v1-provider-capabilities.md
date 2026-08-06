# v1 Provider Capability Model

## Current state (confirmed, `internal/provider/capabilities.go:13-20`)

```go
type Capabilities struct {
    SupportsStreaming    bool
    SupportsModelList    bool
    SupportsTokenUsage   bool
    SupportsJSONMode     bool
    SupportsSystemPrompt bool
    ContextWindowTokens  int
}
```

Populated via `CapabilitiesOf(p Provider)`, which calls an optional
`CapabilityReporter.Capabilities()` or falls back to a conservative
`DefaultCapabilities()` (streaming + system prompt only). This is a real,
if minimal, capability model — not absent, contrary to what a naive reading
of "scattered model-name checks" might suggest. The gap is narrower than
that: **native tool calling, parallel tool calls, reasoning/thinking
events, and structured-output support are not represented in this struct at
all.**

Native-tool-calling rejection is instead detected reactively, by
string-matching a provider's HTTP error response (`toolsRejectedError`,
`internal/tui/pipeline.go:794-805`) after a request has already failed, not
by consulting a declared capability beforehand.

## Target (master-prompt §7.4)

Extend `Capabilities` with the missing fields:

```go
type Capabilities struct {
    SupportsStreaming     bool
    SupportsModelList     bool
    SupportsTokenUsage    bool
    SupportsJSONMode      bool
    SupportsSystemPrompt  bool
    SupportsNativeTools   bool
    SupportsParallelTools bool
    SupportsReasoning     bool
    ContextWindowTokens   int
}
```

Resolution order, per master-prompt §7.4 (highest to lowest precedence):

1. Explicit provider implementation knowledge (e.g. the OpenAI-compatible
   provider knows its own request shape supports native tools when the
   backend accepts a `tools` field without error).
2. Server-advertised capability, where a backend exposes one (most
   OpenAI-compatible local servers do not; Ollama's `/api/show` reports
   some model metadata that can inform `ContextWindowTokens`).
3. Configuration override (`providers.<name>.capabilities.*` in YAML, for
   servers that don't self-report and the user knows the answer).
4. Bounded probe: on first use, a single low-cost request that would
   reveal the capability (e.g. an empty-tools-array request), cached for
   the session — not repeated per turn.
5. Model-name heuristics, **last resort only**, and confined to a single
   lookup table/function, not scattered through orchestration code (this
   preserves the one confirmed-good practice already in place:
   `toolsRejectedError`'s reactive detection becomes a fallback path, not
   the primary mechanism, once this exists).

## Migration note

Adding fields to `Capabilities` with safe zero-value defaults
(`SupportsNativeTools: false` etc. until proven otherwise) is additive and
does not require a config migration. `DefaultCapabilities()` should remain
conservative. No existing provider behavior changes until a provider
package is updated to populate the new fields — this can land as an
independent slice (master-prompt §11, slice 1) ahead of the progress-ledger
work, since it is lower-risk and unblocks nothing else on the critical path
to fixing the reported failure mode.

This is explicitly **not** on the critical path for the reported
repeated-tool-call bug (`v1-audit.md` §4.1-4.2 are the causal chain); it is
tracked here because the master prompt requires a capability-matrix
investigator workstream, and because a declared `SupportsNativeTools` flag
will let the progress ledger's fingerprinting logic treat native vs.
fenced-fallback tool calls uniformly without re-deriving that distinction
itself.
