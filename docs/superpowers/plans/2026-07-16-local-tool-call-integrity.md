# Local Tool-Call Argument Integrity Plan

## Goal

Make native tool-call failures from OpenAI-compatible local backends
diagnosable and safe: distinguish a genuinely omitted required field from
malformed JSON received over the wire, and prove that streaming reconstruction
preserves a large `write_file` call exactly. Do not change tool-loop ordering,
approval, MCP behavior, or the prompt template in this work.

## Evidence and scope

The 2026-07-16 Qwen/LM Studio trace records a `write_file` call with both
`content` and `path: "scientific_calculator.py"`, but llmtui feeds the model
`error: write_file needs a path`. Today `tools.CallsFromNative` ignores a
`json.Unmarshal` error and therefore turns any malformed argument string into
an otherwise ordinary call with empty fields. The TUI reports the missing path,
not the actual protocol failure.

The trace alone does not prove whether LM Studio emitted malformed JSON or
whether its streaming response used a non-delta argument representation that
llmtui appended incorrectly. Both must be kept separate from a model choosing
to omit `path`.

The later HTTP 500 is a backend failure after llmtui submitted the tool-result
continuation. It is out of scope for a blind client-side retry: HTTP 500 must
remain visible and must not duplicate external side effects.

## Constraints

- Never execute a native call whose argument JSON cannot be decoded.
- The model receives a concise, actionable error naming the tool and the
  invalid JSON condition; no raw file content, secrets, or full arguments are
  written to normal logs or shown in the UI.
- A valid call that genuinely omits `path` retains the existing
  `write_file needs a path` response.
- Preserve native-tool IDs and the assistant-call/tool-result pairing.
- Keep the OpenAI-compatible streaming protocol delta-based; do not guess at
  undocumented cumulative chunks. A captured raw SSE fixture is required
  before supporting another representation.
- Run `go fmt ./...`, `go vet ./...`, `go test ./...`, and
  `go test -race ./internal/tui ./internal/provider/openai` before handoff.

## Task 1: Preserve native-argument parse failures

**Files:**

- Modify: `internal/tools/tools.go`
- Modify: `internal/tools/native.go`
- Modify: `internal/tools/native_test.go`
- Modify: `internal/tools/tools_test.go`

1. Extend the internal `tools.Call` representation with an unexported or
   clearly internal parse-error field. Keep the raw native arguments out of
   user-facing `Describe()` output.
2. In `CallsFromNative`, decode non-MCP arguments strictly. If JSON is
   malformed or not an object, record a deterministic parse error instead of
   leaving every mapped field empty.
3. In `Runner.Execute`, return that parse error before dispatching the call.
   Include the native tool name and a concise JSON-decode reason; do not echo
   the arguments.
4. Add table-driven tests for:
   - malformed JSON for `write_file` returns a parse error, never a missing
     path error;
   - a valid `{ "content": "x" }` still returns `write_file needs a path`;
   - valid large escaped content plus a `path` maps to both fields;
   - existing MCP raw-argument pass-through is unchanged.

## Task 2: Test streamed tool-call reconstruction at realistic sizes

**Files:**

- Modify: `internal/provider/openai/tools_test.go`
- Modify: `internal/tui/toolloop_test.go`

1. Add an OpenAI-compatible SSE fixture that splits a `write_file` call with
   multiline, escaped Python content and a trailing `path` across many
   argument fragments.
2. Assert the final `provider.ToolCall.Arguments` is byte-for-byte valid JSON
   and decodes to the expected content and path.
3. Feed that result through the TUI tool loop with a temporary workspace;
   assert the intended file is written only after approval and its full
   content matches.
4. Add a negative fixture with malformed final JSON. Assert the model receives
   the new parse error and no file is created.

## Task 3: Add privacy-safe tool-call diagnostics

**Files:**

- Modify: `internal/provider/openai/stream.go`
- Modify: `internal/tui/app.go` and the existing debug/diagnostic surface
- Tests: provider and TUI tests as appropriate

1. Expose per-call diagnostic metadata only when debug mode is enabled:
   provider name, tool-call ID/name, argument byte count, SHA-256 digest, and
   whether final argument JSON is valid. Never log raw arguments or tool
   output.
2. On a native argument parse failure, surface the same metadata with the
   model-visible error so a user can match llmtui evidence to `lms log stream`
   without disclosing the generated file.
3. Document a minimal reproduction procedure: one required-argument tool,
   stream on/off comparison, then capture raw SSE only if the outcomes differ.

## Task 4: Reproduce against the local backend before changing protocol logic

1. Run the supplied calculator prompt once with streaming disabled and once
   with streaming enabled, using the same model, template, and tool set.
2. If only streaming fails, capture a redacted raw SSE sequence and add it as
   a fixture. Implement support only for that documented sequence.
3. If both fail, verify the model's LM Studio tool-use mode/template and retry
   with a minimal `write_file` request before changing llmtui.
4. If the backend still returns HTTP 500 after a valid tool-result
   continuation, collect its server error log and treat it as an LM Studio
   template/backend incident; do not retry a request that might repeat a
   mutating tool call.

## Acceptance criteria

- A malformed native JSON argument never becomes a misleading missing-field
  error.
- A correctly streamed, large Python `write_file` call survives reconstruction
  and writes exactly once after approval.
- The model can recover from malformed arguments without llmtui leaking file
  content into diagnostics.
- The existing MCP and native-tool loop tests, full suite, vet, and targeted
  race tests pass.
