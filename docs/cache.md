# Response Cache

Repeated prompts against slow local models answer instantly from a local
file cache (`cache.path`, default `~/.cache/llmtui/responses`).

The key includes: provider name, base URL (hashed), model, raw user message
(hashed), system prompt (hashed), prompt mode, template, temperature, top_p,
max tokens, the complete provider-visible conversation history (including
images and tool metadata), and the active tool specifications. **API keys are
never part of the key or the entries.**

The key schema is versioned. Correctness changes invalidate older keys rather
than risking a response produced under older request-identity rules.

- Failed and empty responses are never cached.
- Messages with image attachments are never cached.
- Responses produced after falling back from rejected native tools are not
  cached under the original native-tool request key.
- Entries expire after `cache.ttl` (default 24h) and the directory is
  pruned oldest-first past `cache.max_size_mb`.
- Entries are written through owner-only temporary files and atomically
  renamed, so interrupted writes cannot expose partial JSON entries.
- Read, write, decoding, and pruning failures remain visible in `/cache`;
  read failures fall back to a normal provider request.
- Cached replies show a `cached response` notice in the chat.

`/cache` shows stats (entries, size, session hits/misses), `/cache clear`
wipes it, `/cache on|off` toggles at runtime.
