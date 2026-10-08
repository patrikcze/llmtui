# LLMTUIiOS architecture

The iOS app (`macos/setup/LLMTUIiOS`) is a SwiftUI chat client for local
model servers. It shares no code with the Go llmtui or with the macOS app,
and it does not read llmtui's `config.yaml`. Build settings: iOS 18.0
deployment target, iPhone and iPad, Swift with main-actor default isolation.

## Files

| File | Responsibility |
|---|---|
| `LLMTUIiOSApp.swift` | App entry point; shows `ContentView`. |
| `ContentView.swift` | Tabs (Chats, Providers, Settings), the chat list, chat screen, composer, tool cards, approval sheet, provider editor, Settings (appearance, tool approval, chat retention, memory) and the Archived screen. |
| `MobileAppModel.swift` | The main-actor model: chats, the open chat, providers, sending and queueing, the tool loop, tool dispatch and approval, attachment consent, citations, retention, archive and pin. |
| `ChatRuntime.swift` | HTTP to the provider: model discovery, streaming chat completions (SSE), and the tool definitions sent to the model. |
| `ProviderModels.swift` | Provider profiles and types, chat messages, tool activity records, reasoning and approval modes, turn options, queued messages, errors. |
| `ProviderStore.swift` | Saves provider profiles in the app's preferences and API keys in the Keychain. |
| `ConversationStore.swift` | `MobileConversation` and its JSON file store under `Application Support/Conversations`. |
| `ChatRetention.swift` | The retention policy (period, archive or delete) and its evaluation. |
| `SafeToolRuntime.swift` | `MobileMemoryStore` (memories in preferences), `MobileLocalContext` (date, time and device facts) and `SafeWebFetcher` (DuckDuckGo search and public-page fetch). |
| `WebResearch.swift` | `web_research`: multi-query search, result fusion, concurrent page reads, passage selection and the notes format. |
| `DocumentModels.swift` | Attachment kinds, statuses, chunks, source references and limits. |
| `DocumentStore.swift` | Attachment storage under `Application Support/Attachments` and secure, coordinated import of external files. |
| `DocumentExtractor.swift` | On-device text extraction: PDFKit per page, Vision OCR for image-only pages and images, UTF-8 text by lines. |
| `DocumentLibrary.swift` | Attachment state per chat, import and extraction tasks, the chunk cache, retry, removal and recovery after relaunch. |
| `DocumentTools.swift` | `document_list`, `document_search` and `document_read`, and citation parsing and rendering. |
| `DocumentViews.swift` | The attachment bar and the source viewer (PDF page, text lines, image with OCR text). |
| `MobileRichMessageView.swift` | Reply rendering: Markdown, tables, code, KaTeX math and Mermaid in a web view, and images. |
| `MobileMediaViews.swift` | Inline images and the image viewer, Mermaid PNG export, the share sheet, and the Copy and Share buttons. |
| `Theme.swift` | Light and dark colors, the accent gradient and shared styles. |

## Sending a message

1. `send()` queues the message if a reply is already generating. Otherwise
   `dispatch` adds the user message and an empty streaming assistant
   message, and starts a task.
2. `runToolLoop` builds the request. The system message contains formatting
   rules, and, when they apply:
   - the tool and research rules;
   - the saved memories (newest first, bounded) with the memory rules;
   - the attachment metadata (ids, names, status) with the attachment rules.

   Attachment text is never put in the prompt.
3. `MobileChatRuntime.streamTurn` POSTs to `<base>/v1/chat/completions` with
   `stream: true` (Ollama's model list comes from `/api/tags`). Text deltas
   are applied to the message in batches about every 80 ms.
4. If the model returns tool calls, each one is executed and its result
   added as a `tool` message. A tool error is returned to the model as the
   result instead of ending the reply. The loop allows 8 rounds, then sends
   one request without tools so the reply ends with an answer.
5. When the task ends, the chat is saved and the next queued message, if
   any, is sent.

## Approvals

`requiresApproval` decides which tools ask first:
- web tools and memory changes, according to the Settings approval mode;
- always, once a document tool has returned attachment text in the current
  reply.

Searching or reading attachments first needs a per-chat, per-provider consent
(`documentConsentProfileIDs`). The approval sheet resolves a continuation the
tool call is waiting on.

## Attachments

- **Import.** Files are copied into the chat's folder as read-only originals,
  using security-scoped access and `NSFileCoordinator`. Images are written
  directly.
- **Extraction.** It runs in a detached task. A PDF is processed page by
  page, and progress is saved every 5 pages. After a relaunch, unfinished
  work shows as interrupted, and Retry resumes it.
- **Cache.** Chunks are cached in `chunks.json` and reused by every search
  and read.
- **Tools.** The document tools resolve ids only within the current chat.
  Every passage they return is recorded on the reply (`sourceRefs`). A
  citation token becomes a `llmtui-cite://` link only if its passage is in
  that list.

## Retention

`ChatRetentionPolicy.evaluate` returns the chats to archive or delete for
the saved policy. It skips:
- pinned chats;
- the open chat, a chat generating a reply, and chats with queued messages;
- for archiving, chats that are already archived.

`ContentView` calls `applyRetention()` whenever the scene becomes active.
Settings previews a new policy and asks for confirmation before applying it
if it would act on existing chats.

## Tests

- `LLMTUIiOSTests` covers the core behaviour.
- `DocumentToolsTests` exercises extraction (real PDFKit and Vision), the
  document tools, citations and the attachment lifecycle.
- `ChatRetentionTests` covers the retention policy and the model behaviour.
- `LLMTUIiOSUITests` has the UI tests.

## MCP servers (Swift only)

Settings → MCP Servers manages explicit Streamable HTTP connections. A chat's
MCP Servers menu selects its servers; older chats select none. Tools must be
on, and servers must already be connected. Creating a profile or opening a
chat starts no connection. Every external tool call asks for approval showing
its server, original tool name and complete arguments. The app keeps its
existing eight-round loop and permits at most 16 MCP calls per reply.

`MobileMCPService` owns connections behind the injectable `MobileMCPClient`
actor interface. `MCPModernClient` implements `2026-07-28` using Foundation;
`MCPLegacyClient` uses official Swift SDK 0.12.1 for the initialized
`2025-03-26`, `2025-06-18`, and `2025-11-25` era. The SDK revision and all
resolved packages are recorded in the Xcode workspace's `Package.resolved`.
Only the iOS target links MCP. The legacy SDK uses an app-owned HTTP transport
so both eras share bounded HTTP/SSE decoding, response-ID checking, ephemeral
sessions, disabled logging and rejected redirects.

Discovery starts with an observational modern request. Unrecognized legacy
initialization failures select the SDK adapter; a modern unsupported-version
error can explicitly advertise a compatible older version. Authentication,
header/capability errors and unsupported modern versions are surfaced rather
than blindly retried. Legacy session IDs stay in the legacy transport. The
modern adapter sends per-request metadata, required method/name headers and
validated `x-mcp-header` primitive arguments, using the specified Base64
sentinel when needed. Resources, prompts, sampling, elicitation, roots, MCP
Apps and asynchronous Tasks are not advertised. Unsupported input requests
and media produce explicit errors/notices, never external fetches or replay.

`MobileMCPController` provides observable metadata to the UI. Profiles use
`iosMCPProfiles`, independent of provider settings; bearer and OAuth access/
refresh credentials use a distinct Keychain service and never conversation
or preference files. Known access-token echoes in tool definitions are
excluded and in results redacted. OAuth uses `ASWebAuthenticationSession`,
PKCE S256, validated state/issuer, protected-resource and OAuth/OIDC discovery,
resource-bound refresh, scope accumulation, configured client IDs/metadata
URLs, or supported dynamic registration. The callback is
`llmtui-ios-mcp://oauth/callback`; an OAuth server must accept that native
client registration. No confidential-client secret is embedded in the app.
Additional scopes require explicit Sign in / update permissions; failed tool
calls are never automatically resubmitted after authentication.

HTTPS is the default. User-opted-in HTTP is restricted to explicitly
configured local addresses/`.local` endpoints without credentials. The
iOS-only plist permits local networking and registers the OAuth callback;
it does not add a global arbitrary-loads exception or change macOS policy.
The phone's `localhost` is the phone, not a desktop server. Desktop stdio
servers need a separately hosted HTTP endpoint or bridge.

The catalog keeps arbitrary nested input schemas and assigns deterministic
provider names, mapping them to server identity and original names. Discovery
and each offered catalog are limited to 64 tools and 256 KiB; each HTTP/SSE
message and request is limited to 1 MiB; model-visible results including
markers are limited to 32 KiB. Invalid header annotations are excluded with
visible warnings. Oversized catalogs fail explicitly, and oversized results
report truncation. MCP definitions contribute to the existing context ring.

Each sent or queued turn captures selected server IDs and connection generations.
Only advertised tools execute. Approvals are revalidated against the original
chat's selection, connection generation and a refreshed tool schema immediately
before transmission. Changes require a new reply/approval. MCP results remain
untrusted tool messages; outbound web and memory actions subsequently always
ask. Remote images in replies involving MCP also require a tap, matching the
existing attachment/web rule. Existing document consent and citation ownership
remain unchanged.

Backgrounding cancels an MCP-enabled reply, connection/sign-in tasks and
approval waiters, then closes transports. Foregrounding does not reconnect or
resume tools. Interrupted calls can have unknown effects; their cards say so,
and the app requires explicit reconnection. Editing/removing a server or
changing credentials revokes its connections and pending bindings.

### MCP validation and physical-device checklist

`MCPTests`, `MCPChatTests`, `MCPOAuthCallbackTests`, `MCPWireTests`, and
`MCPNetworkTests` cover both protocol eras,
real Swift loopback HTTP/SSE sockets, independently counted approved calls,
rejections, cancellation, changed schemas/profiles, scope discovery/PKCE/
registration/refresh, provider completion, malformed framing, catalog limits,
Unicode truncation, credential isolation and remote-image consent. The full
provider → approval → MCP → correlated tool-result → provider path is tested.
`MCPServerUITests` exercises the Settings entry, endpoint validation and Cancel.
The real Keychain round trip is explicitly enabled with
`LLMTUI_IOS_KEYCHAIN_TEST=1` on a signed host; unsigned simulator tests inject
credential storage because Security.framework returns missing-entitlement
error `-34018`.

Before distributing the feature, use a signed physical-device build to verify:

1. LAN permission, explicitly allowed unauthenticated HTTP, HTTPS and revoked
   access; verify tool cards and actual server-side call counts after approval.
2. Bearer Keychain persistence/deletion across relaunch with the signed-host
   test enabled. Confirm no credentials enter preferences or conversations.
3. Browser OAuth against a registered native client, user cancellation, refresh
   and incremental scopes. Check wrong-state/issuer rejection without code exchange.
4. Background during discovery, approval and a running tool; confirm no
   foreground replay, honest interruption and explicit reconnection.

The simulator tests use local fixtures, not production accounts. Physical LAN,
interactive OAuth against a deployed authorization server, and signed-device
Keychain behavior are not established by an unsigned simulator pass.
