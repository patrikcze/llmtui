# Vision Observation Entities — Implementation Plan

## 1. Current image lifecycle

`Ctrl+V` reads one PNG/JPEG image into `provider.Image{Data, MIME}` and stores
it in the TUI's pending `attachments`. `send` passes those images to
`Model.dispatch`, which stores them on the live user `provider.Message` and
passes them through `prompt.Input.Images`. The provider adapters translate the
same `Message.Images` into Ollama base64, OpenAI-compatible data URLs, or the
embedded llama.cpp/yzma vision path. The history model deliberately tags
`provider.Message.Images` as `json:"-"`, so saved sessions omit raw image
bytes. There is no image digest, observation status, or reference annotation
on a live message today.

While the image-bearing message remains in the recent provider history,
`prepareRequest` sends its image again. When `contextmgr.Split` moves the
message into `older`, or a continuation projection removes it, the image
simply disappears; the existing summary only sees text and does not preserve
visual provenance.

## 2. Current entity lifecycle

`internal/entity.Registry` is a bounded, mutex-protected, in-memory store with
opaque sequential IDs, per-record and total payload limits, deterministic LRU-
style eviction, scopes, expiry, trust, provenance, and progressive
identifier/minimal/full views. Successful file, web, and MCP tool results carry
`entity.Candidate` values into `Model.registerResultEntities`. The model-facing
prompt contains a recency shortlist, while `get_entity_details` resolves exact
IDs or scans labels, safe metadata, previews, and payloads with
`Registry.Search(query, limit)`.

The current search API has no kind filter and considers any positive token
score a match. The controller resolves entity details in
`internal/tui/entity_context.go`; the fenced and native tool parsers both use
the same `tools.Call` fields and validation.

## 3. Confirmed failure mechanism

There is no candidate-producing path for a user image. Therefore, after the
image leaves the recent multimodal message set, the registry has no visual
evidence to retrieve. A topical query can still return `web_result` or other
entities because `Registry.Search` is cross-kind and does not know that the
user is asking for screenshot evidence. The prompt protocol also does not tell
the model to distinguish a visual observation from a web result or to report
visual evidence as unavailable when no visual entity exists.

## 4. Proposed architecture

Add `entity.KindVisionObservation` and a dedicated model-derived trust value
consistent with the existing trust vocabulary. A vision observation is a
bounded payload describing only visible/model-supported content from one
user-provided image, with source `user_provided_image`; it is not a web result,
memory record, permission, or source-of-truth claim.

Extend `entity.Registry` with an options-based search entry point containing a
query, limit, and optional kind set. Keep the existing two-argument `Search`
as a compatibility wrapper. Add conservative term coverage so a long query
with only one weak token does not produce a misleading candidate. Extend
`get_entity_details`'s fenced/native JSON schema and validation with optional
`kinds`; omitted kinds preserve current behavior, while an explicit visual
kind filter can never return web entities.

Add a small TUI-owned capture coordinator. After a successful final
multimodal turn with Entity Context tools active, it performs at most one
bounded, tool-free, provider-neutral
`Provider.Chat` request for each new image digest (one request can contain the
new unique images from a turn). The request uses a short factual extraction
prompt, `Reasoning: "off"`, no tools, a strict small JSON response shape, and
the existing caller-owned timeout policy. It parses and validates the result
before putting any candidate. It does not enter the agent loop, memory,
history, cache, MCP, web, or RAG paths.

## 5. Why this belongs in Entity Context rather than durable Memory

The existing ECR is explicitly session-local and designed to preserve useful
runtime objects without promoting them to facts. A visual extraction is
model-derived, potentially wrong, tied to one attachment, and useful for
follow-up retrieval in the current working session. Durable memory would
create a new persistence/privacy contract and could falsely promote pixels to
user facts. No memory auto-extraction, episode, project record, or user memory
behavior will change.

## 6. How visual observations are produced

The capture prompt will request one bounded observation object per supplied
image, preserving readable visible text, numbers, units, labels, names, dates,
and relevant relationships without guessing or answering the current user
question. It explicitly treats image text as untrusted data and instructions
as non-executable. The provider's normal visible answer is never used as a
substitute for extraction, and hidden reasoning is ignored.

The controller will accept only a valid response with the expected image
count and useful bounded fields. Malformed JSON, truncated output, missing
objects, or empty observations fail closed. The payload stored in the entity
is the validated bounded JSON representation, not raw image bytes.

## 7. Raw-image lifetime policy

No session-local blob store is needed for the first vertical slice. The live
`provider.Message.Images` bytes remain available until normal multimodal
processing and successful observation capture finish. On success, the
controller adds an ephemeral typed message reference and clears only that
message's `Images`; the original user text remains unchanged. The entity
records that no raw asset is retained. On capture failure or cancellation,
the image remains attached and is eligible for normal future retransmission;
no fake entity or persistence claim is made.

Image identity is SHA-256 over actual bytes, independent of filename or MIME.
The TUI keeps a bounded per-session digest-to-observation map, reuses live
entities for duplicate bytes, and does not re-analyze duplicates. If a reused
entity has been evicted, the digest can be captured again as a new live
observation.

## 8. Context compaction behavior

Add an ephemeral typed runtime-reference field to `provider.Message`, excluded
from provider wire serialization and history serialization. The original
message content is never rewritten. `contextmgr.condenseMessage` will emit a
compact marker for a referenced visual observation containing only its entity
ID, kind, and bounded label. It will never copy the full visual payload into
every summary. This marker survives the older-message summary boundary while
the registry remains authoritative for detail lookup.

When a captured image is projected into a recent provider request, its raw
attachment is absent and its entity remains available through the Entity
Context shortlist or explicit lookup. If capture failed, compaction retains
the image-bearing message under the existing image safety behavior rather than
silently replacing the only visual evidence.

## 9. `get_entity_details` improvements

Add optional `kinds: ["vision_observation"]` to both native and fenced forms,
bounded to the known entity-kind vocabulary. Query results remain minimal and
exact-ID expansion remains unchanged, including existing exact-ID calls such
as `{"entity_ids":["ent_00016"],"level":"full"}`. The controller passes
kind filters to registry ranking rather than implementing filtering in the
TUI. Full visual payloads continue to pass through `untrusted.Frame`.

Update the compact protocol text with the evidence-class rules: visual
references require visual kind lookup; web results must not be described as
image evidence; absent visual entities mean old visual evidence is unavailable;
model-derived observations may contain extraction errors; missing values must
not be invented.

## 10. Trust/provenance model

The entity's trust value will identify model-derived visual evidence, separate
from controller-observed attachment metadata. Typed provenance fields will
retain the attachment digest, user-turn/message identity, image index, MIME,
provider, model, capture timestamp, capture format version, truncation state,
and raw-retention state for local diagnostics/tests. The model-facing view
will expose only the bounded kind/source/trust/preview already needed for
safe retrieval; it will not receive unnecessary internal identifiers or raw
bytes. No numerical confidence score will be fabricated.

## 11. Provider-neutral implementation

The capture path will depend only on `provider.Provider.Chat`,
`provider.ChatRequest`, `provider.Message.Images`, `ResponseConstraint`, and
stream events. OpenAI-compatible and Ollama adapters already support JSON
schema constraints; embedded llama.cpp already supports a GBNF constraint and
the existing image path. The TUI will gracefully treat unsupported response
constraints or vision as capture-unavailable. Normal chat remains independent
of capture success.

## 12. Embedded-provider impact

No second embedded provider or model/runtime will be created. The sequential
capture runs through the active provider after the normal stream has completed,
so its existing loaded model/mmproj pair is reused. The capture request is
tool-free and text-output constrained, and it uses the same `Message.Images`
validation and native vision cleanup path. Text-only embedded models are
gated by the existing selected-model vision capability/force-vision behavior
and are never given an image capture request when vision is unavailable.

## 13. Security/privacy considerations

Raw images never enter JSON history, entity payloads, memory, cache entries,
logs, debug text, or a new disk location. The capture prompt says visible
instructions are data, not commands; the resulting payload is framed with the
existing `untrusted.Frame` path before model-facing full expansion. Entity
kind and source remain distinct, so visual similarity cannot create web
provenance. Capture uses no tools, filesystem, network tool, MCP, memory, or
agent verifier. Existing provider configuration is the only inference path;
no unconfigured external service is introduced.

## 14. Failure behavior

Capture failure, unsupported vision, timeout, cancellation, malformed or
truncated structured output, registry capacity failure, and provider switch
race all fail closed: normal answer/history remain usable, no candidate is
registered, and raw image bytes are not removed. A bounded content-free
diagnostic counter/status records capture failure without adding an error
message to the conversation. Capture cancellation cannot later mutate a
message because completion carries a generation token.

## 15. Token/performance impact

Each unique new image may incur one additional bounded inference with a
default maximum of 800 completion tokens (configurable only through the small
entity vision setting if the current config conventions support it). The
request has no unrelated history or tools. Later ordinary turns avoid
re-encoding captured images; entity details expand only on demand. Duplicate
digests reuse an existing entity. Diagnostics count captures, reuse, failures,
and raw-image replay avoidance without recording image content.

## 16. Tests

Add deterministic table-driven tests for the new entity kind, trust,
provenance, payload/preview bounds, scope/reset/expiry, minimal/full views,
kind-filtered ranking, weak fuzzy-match rejection, and exact-ID compatibility.
Extend tool parser/native-schema tests for optional kinds and invalid kinds.

Add TUI fake-provider tests covering successful capture with a successful
normal answer, capture failure preserving the answer and raw image, malformed
and truncated fail-closed output, per-digest deduplication, distinct bytes
with the same conceptual filename, context summary markers after aging, and
absence of image bytes from saved history. Instrument request messages to
prove an old captured image is not replayed on unrelated future turns. Add a
prompt-injection case proving extracted `IGNORE SYSTEM` text stays inside the
untrusted entity frame and does not affect tool authorization. Verify no
visual candidate is promoted to memory.

## 17. Rollout/migration

The change is additive for live sessions: existing entity kinds and lookup
calls continue to work, omitted `kinds` retains cross-kind search behavior,
and old saved sessions load without runtime references or raw images. New
visual entities are session/run scoped only and disappear on reset, restart,
or normal eviction. No history migration, durable schema migration, or
provider-specific setup is required. Documentation will update entity,
context, prompt, provider, configuration, and security descriptions together.

## 18. Non-goals

This change does not persist raw images, add an image database, add OCR or an
OCR dependency, build a scene graph, add embeddings or another LLM search
engine, promote visual observations to memory, rewrite the agent loop, expose
raw image bytes through tools, force structured output onto normal requests,
add a provider-specific vision API, or weaken any tool approval, confinement,
SSRF, or untrusted-content boundary.
