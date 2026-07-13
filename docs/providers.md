# Providers

| Provider | Type | Default base URL | Notes |
| --- | --- | --- | --- |
| Ollama | `ollama` | `http://localhost:11434` | Native API, NDJSON streaming, real token counts |
| LM Studio | `openai_compatible` | `http://localhost:1234/v1` | SSE streaming |
| vLLM / llama.cpp / Unsloth | `openai_compatible` | your server | Set `base_url` |
| Anything OpenAI-compatible | `openai_compatible` | — | `api_key_env` keeps secrets out of YAML |

Each provider reports **capabilities** (streaming, model listing, token
usage, JSON mode, system prompt) used by `/doctor` and prompt composition.
Unknown backends get conservative defaults.

## Network behavior

- `network.connect_timeout` (default 10s) bounds connection attempts.
- `network.timeout` (default 120s) is an **inactivity** timeout for streams:
  the maximum wait for the *next* token. It resets on every token, so a slow
  local model is never cut off mid-answer as long as it keeps producing
  output, however long the full reply. For a non-streaming request it acts as
  a whole-response cap.
  - Set it without a config file via `LLMTUI_NETWORK_TIMEOUT=600s`, or in the
    config's `network.timeout`.
  - Only a genuine stall trips it — the message is
    *"no response from &lt;provider&gt; for &lt;timeout&gt; — the model may be
    stuck…"*, and any partial output is kept.
  - Raise it when the model pauses a long time **before its first token** —
    a cold model load, or thinking that emits nothing at all for that long.
- **Reasoning models** (that "think" before answering) stream their thinking
  separately (OpenAI `reasoning_content`, Ollama `thinking`). llmtui treats
  that as activity: it resets the inactivity timer (so a long thinking phase
  never times out) and shows a live `thinking…` indicator with a running
  token estimate. The thinking is not part of the visible answer and is not
  cached; if the model spends its whole budget thinking without answering,
  the reasoning is surfaced with a note to raise `chat.max_tokens`.
- Transient failures (connection refused/reset, timeouts) retry up to
  `network.retry.max_attempts` with `network.retry.backoff` — HTTP errors
  (wrong model, bad request) and user cancellations are never retried.
- Partial streamed output is preserved when a stream dies or is stopped.

`/doctor` checks reachability, whether the selected model exists, streaming
and token-usage support, and where the context window number comes from.

## Reasoning models (Qwen 3.5 / 3.6, DeepSeek-R1)

llmtui talks to every backend through structured chat APIs
(`/v1/chat/completions`, Ollama `/api/chat`). The **chat template — the
Jinja program that turns messages into the model's token stream — is
applied by the backend, never by llmtui.** If a Qwen 3.5/3.6 model is slow
or unstable (degenerate reasoning loops, stalled tool calls, KV-cache
thrash making every turn slower), fix the template in the backend:

The official Qwen 3.5/3.6 templates have known bugs; the community-fixed
drop-in replacement is
[froggeric/Qwen-Fixed-Chat-Templates](https://huggingface.co/froggeric/Qwen-Fixed-Chat-Templates):

- **LM Studio**: My Models → model settings → Prompt tab → replace the
  template with the contents of `chat_template.jinja` → Save.
- **llama.cpp / koboldcpp**: `--jinja --chat-template-file chat_template.jinja`
- **vLLM**: replace `chat_template` in `tokenizer_config.json`; serve with
  `--tool-call-parser qwen3_coder`.
- **Ollama**: not supported — Ollama uses Go templates, not Jinja. Rely on
  Ollama's own model templates and keep them updated (`ollama pull`).

What llmtui does client-side, for any reasoning model:

- Strips a leaked leading `<think>…</think>` block out of the answer
  (`chat.strip_leaked_thinking`, default `true`), so broken-template
  reasoning is never stored in history, re-sent each turn, or cached.
- `/think on|off|auto` (or `chat.reasoning`) requests or suppresses the
  thinking phase explicitly: OpenAI-compatible backends receive
  `chat_template_kwargs: {"enable_thinking": …}` (honored by vLLM and
  llama.cpp server, ignored elsewhere), Ollama receives `think`. `auto`
  sends nothing. Note: Ollama returns an error if `think` is set for a
  model without thinking support — use `auto` there.
