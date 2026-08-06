# v1 Provider Capability Model

## Implemented state

`internal/provider/capabilities.go` now distinguishes transport-wide booleans
from model-dependent tri-state support:

```go
type Capabilities struct {
    SupportsStreaming    bool
    SupportsModelList    bool
    SupportsTokenUsage   bool
    SupportsJSONMode     bool
    SupportsSystemPrompt bool
    NativeTools          CapabilitySupport // unknown | unsupported | supported
    ParallelToolCalls    CapabilitySupport
    ReasoningEvents      CapabilitySupport
    StructuredOutput     CapabilitySupport
    ContextWindowTokens  int
}
```

The tri-state is load-bearing. A generic OpenAI-compatible endpoint often
does not advertise whether its selected model supports native tools. Treating
that unknown as `unsupported` would silently remove a feature that worked
before; treating an explicit negative as unknown would cause a known-bad
request. Unknown therefore keeps one optimistic real request, while an
explicit negative selects the fenced compatibility protocol immediately.

`CapabilitiesFor(provider, model)` consults model-scoped reporters before
provider-wide reports. The embedded provider uses its configured or
centrally-detected tool grammar to report native/parallel tool-call support
for the selected GGUF. Unknown remote backends stay honest rather than being
declared universally capable.

## Resolution and runtime use

Current resolution order is:

1. `providers.<name>.capabilities.*` explicit configuration override;
2. selected-model implementation knowledge (`ModelCapabilityReporter`);
3. provider/transport implementation knowledge (`CapabilityReporter`);
4. bounded learning from a real native-tool request rejection, cached for
   the current provider/model session key;
5. `unknown`, which preserves the existing optimistic first attempt.

The learned negative is keyed by provider and model. Switching away from an
incapable model no longer leaves native tools disabled for an unrelated model
or provider; switching back reuses the learned result. `/doctor` reports the
new capability values, request assembly omits native schemas when support is
known false, and context-window resolution uses the selected-model report.

Configuration overrides use pointer booleans so omitted remains `unknown`
while `false` remains an authoritative negative. The wrapper that applies
them preserves provider shutdown and runtime-fingerprint behavior.

## Honest limits

- “Parallel tool calls” means the backend/model may emit multiple calls in
  one turn. llmtui intentionally executes the resulting batch sequentially
  and in order, including mixed MCP/native batches.
- `StructuredOutput` remains unknown unless explicitly configured because
  `ChatRequest` does not yet expose a strict response-schema contract.
- Ollama and generic OpenAI-compatible model advertisements are not
  consistently available across server versions. llmtui therefore does not
  add a speculative network probe or model-name heuristic; config and the
  bounded real-request fallback cover those servers safely.
- Live compatibility still requires the manual matrix in
  `v1-test-matrix.md`; deterministic tests cannot prove a particular local
  server/model installation.
