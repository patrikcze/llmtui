# Tool-call lifecycle diagnostics

Native tool use crosses several independently configured boundaries: model
output, chat template, provider transport and stream decoder, provider-native
tool parsing, llmtui normalization, the registered-tool set, argument checks,
approval, execution, and the correlated result sent back to the model. An HTTP
success with an empty native `tool_calls` list is therefore not proof that the
model chose not to call a tool. It can also mean a model/template/parser
contract did not line up.

llmtui records a small, in-memory, content-free trace for the current turn.
Use it with:

```text
/debug tool-calls
/debug tool-calls test
```

The first command shows the last lifecycle. The second sends one explicit,
harmless request containing only `conformance_echo(probe_token)`. It checks the
provider's observed native call, name, JSON arguments, call ID, and whether the
provider accepts a synthetic correlated `role:"tool"` result. The probe does
not add `conformance_echo` to the normal tool registry, does not call the
runner, and performs no workspace or personal-app action.

## Security boundary

Marker detection is diagnostic evidence only. In particular, a `<tool_call>`
envelope, Qwen `<function=...>` envelope, Mistral `[TOOL_CALLS]` prefix, or a
Harmony recipient/channel prefix that arrives as visible content is **never**
parsed into an executable call. The existing structured provider call remains
the only native path into call-ID handling, argument validation, registration,
approval, and execution.

The detector intentionally ignores ordinary prose, fenced JSON examples, and
bare JSON. It does not inspect or display reasoning content. When it sees a
strong provider-specific marker but the provider supplied no structured call,
the controller drops that visible envelope and may make **one** schema-bound
reissue for the current execution cycle. The reissue contains a fixed protocol
reminder, never the envelope, inferred arguments, or reasoning. A later call
is executable only if the provider returns a fresh structured call that passes
the normal registration, validation, and approval path.

The single recovery budget is shared with malformed-native-call,
hidden-MCP-schema, and empty-continuation recovery. It prevents alternating
failures from multiplying model requests. `/debug tool-calls` records a
content-free `recovery` event with its category, attempt, and scheduled or
exhausted outcome. A marker itself still never becomes a tool call.

The historic fenced ` ```tool ` compatibility protocol remains separate from
native-provider diagnostics. This feature does not widen it, reinterpret a
marker as a fenced command, or add another executor.

## What is retained

The trace is bounded to 64 events and exists only in memory for the current
TUI process. It records stages, classifications, call IDs/names that were
already structured calls, streaming state, native-call count, and a fixed
marker category. It never records raw provider bodies, headers, credentials,
tool arguments, tool results, conversation text, or hidden reasoning. Raw
response capture is intentionally not implemented: the relevant response
content is already either visible assistant text or unavailable after a
provider/runtime parser consumes it, and retaining a second copy would worsen
the privacy posture without making it executable.

## Reading outcomes

- `native_tool_call_received` at `provider_decoded` means the provider
  delivered a native structure; it is not approval to execute.
- `provider_parse_error`, `suspected_censored_tool_call`, or
  `incomplete_streamed_tool_call` identifies an earlier provider/streaming
  boundary. These observations cannot run a tool.
- `recovery_scheduled` and `recovery_budget_exhausted` identify the bounded
  controller decision around a later inference; neither is execution evidence.
- `normalized`, `tool_resolved`, `arguments_invalid`, `approval_required`,
  `approval_denied`, `execution_succeeded`/`execution_failed`, and
  `result_correlated` identify existing llmtui controller boundaries.
- `unknown_tool` and `invalid_arguments` remain normal model-correctable tool
  results. Approval policy and all destructive-operation protections stay
  authoritative.

The conformance probe reports `required selection: not_supported` because the
shared `provider.ChatRequest` contract deliberately has no `tool_choice:
required` field. It does not pretend a prompt instruction is a transport-level
required-call assertion. A provider that needs that test must expose it through
the common contract first.

## Provider limits and manual matrix

The automated suite uses synthetic OpenAI-compatible, Ollama, and embedded
fixtures; it does not claim a live model/backend is conformant. For each live
test, record backend version, model/quantization, selected template, sampling,
streaming mode, offered tools, observed native calls, marker diagnostics,
argument validity, result correlation, and outcome.

| Backend/model family | Current observation point | Manual command | Status in this change |
| --- | --- | --- | --- |
| embedded/Yzma/llama.cpp | runtime output router, after template rendering and before normalized calls | `/debug tool-calls test` | not run against hardware/model |
| LM Studio / OpenAI-compatible | chat-completions decoded response or SSE accumulator | `/debug tool-calls test` | synthetic decoder tests only |
| Ollama | `/api/chat` NDJSON native `tool_calls` | `/debug tool-calls test` | synthetic decoder tests only |
| GPT-OSS / Harmony | Harmony-safe visible-content guard; reasoning is excluded | `/debug tool-calls test` | synthetic marker tests only |
| Gemma or Qwen | depends on selected provider/template; marker evidence is diagnostic only | `/debug tool-calls test` | not run against a live model |

If a probe yields suspected censoring, preserve the displayed metadata and
compare the model's expected template/tool format with the server's configured
parser. Do not paste a detected response into a tool block, disable approval,
or treat a successful HTTP response as evidence that the action was executed.

For repeated live measurements, use the developer-only, opt-in matrix in
`internal/eval`. These tests are retained in the repository as measurement
tooling, never run in normal CI, and never route a probe through the host
executor:

```bash
LLMTUI_EVAL_BASE_URL=http://127.0.0.1:1234/v1 \
LLMTUI_EVAL_MODEL=your-model \
LLMTUI_EVAL_ENDPOINT_TYPE=openai_compatible \
LLMTUI_EVAL_OUTPUT=/tmp/llmtui-live-evaluation.jsonl \
go test -count=1 ./internal/eval -run '^TestLiveEvaluationMatrix$' -v
```

It repeats the same harmless `conformance_echo` probe, reports native-call,
name, argument, ID, streaming, correlation, bounded recovery, token, and
timing fields, and writes JSONL when `LLMTUI_EVAL_OUTPUT` is supplied. Missing
endpoint/model configuration skips the test; it is never part of normal CI.
If `LLMTUI_EVAL_OUTPUT` is omitted, the test writes to `t.TempDir()` and the
report is deleted when the test exits.
