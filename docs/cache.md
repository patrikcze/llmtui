# Response Cache

Repeated prompts against slow local models answer instantly from a local
file cache (`cache.path`, default `~/.cache/llmtui/responses`).

The key includes: provider name, base URL (hashed), model, raw user message
(hashed), system prompt (hashed), prompt mode, template, temperature, top_p,
max tokens. **API keys are never part of the key or the entries.**

- Failed and empty responses are never cached.
- Messages with image attachments are never cached.
- Entries expire after `cache.ttl` (default 24h) and the directory is
  pruned oldest-first past `cache.max_size_mb`.
- Cached replies show a `cached response` notice in the chat.

`/cache` shows stats (entries, size, session hits/misses), `/cache clear`
wipes it, `/cache on|off` toggles at runtime.
