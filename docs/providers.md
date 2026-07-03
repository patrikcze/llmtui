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
  it's the maximum wait for the *next* token and resets on every token, so a
  slow local model is never cut off mid-answer as long as it keeps producing
  output. Only a stalled server (no tokens for that long) trips it. For a
  non-streaming request it acts as a whole-response cap. Raise it if your
  model pauses a long time before the first token (e.g. heavy reasoning or a
  cold model load).
- Transient failures (connection refused/reset, timeouts) retry up to
  `network.retry.max_attempts` with `network.retry.backoff` — HTTP errors
  (wrong model, bad request) and user cancellations are never retried.
- Partial streamed output is preserved when a stream dies or is stopped.

`/doctor` checks reachability, whether the selected model exists, streaming
and token-usage support, and where the context window number comes from.
