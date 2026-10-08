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
