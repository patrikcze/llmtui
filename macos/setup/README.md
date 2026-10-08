# llmtui for macOS and iOS

`macos/setup` holds one Xcode project, `LLMTUIGUI.xcodeproj`, with two
SwiftUI apps. Neither is part of the Go module: `make check` and
`go test ./...` do not build or test them.

| App | Platform | What it is | Shipped in releases |
|---|---|---|---|
| **LLMTUIGUI** | macOS 26+ | Setup app for llmtui's `config.yaml`, plus a chat with an agent loop | Yes, as a zip on every `v*` tag |
| **LLMTUIiOS** | iOS / iPadOS 18+ | Standalone chat with local LLMs, with tools, memory, attachments and citations | No, run it from Xcode |

Both apps talk to LM Studio, Ollama or an OpenAI-compatible server. Neither
runs embedded (GGUF) models; that stays a terminal llmtui feature
(`llmtui runtime install`).

```text
macos/setup/
  LLMTUIGUI.xcodeproj    the project (targets below)
  LLMTUIGUI/             macOS app sources      → target LLMTUIGUI
  LLMTUIGUITests/        macOS unit tests       → target LLMTUIGUITests
  LLMTUIiOS/             iOS app sources        → target LLMTUIiOS
  LLMTUIiOSTests/        iOS unit tests         → target LLMTUIiOSTests
  LLMTUIiOSUITests/      iOS UI tests           → target LLMTUIiOSUITests
  Config/                signing xcconfig (see Signing)
  Documentation/         architecture notes for both apps
  scripts/               packaging and the signing guard
```

Source folders are Xcode *synchronized folders*: a file added under one of
them joins its target automatically, with no `project.pbxproj` edit.

## LLMTUIGUI — macOS setup app

A graphical editor for the same `~/.config/llmtui/config.yaml` the terminal
app reads. It is aimed mainly at Personal Apps (Mail and Calendar), and also
covers providers, model profiles, the agent, entities, tools and other
settings. Saving refuses if the file changed on disk since it was loaded,
copies the current file to `config.yaml.bak`, and replaces the file
atomically, keeping its permissions (`0600` for a new file).

A secondary chat view uses the provider settings from that config. It has
its own agent and tool loop, with a separate verifier that checks the
final answer against the tool results. It can also render images, Markdown,
math and Mermaid diagrams (with PNG export), and copy replies.

Apart from sharing the config file and calling the `llmtui` binary for
commands such as `config path` and `doctor`, the app is independent of the
Go code. See [`Documentation/Architecture.md`](Documentation/Architecture.md).

### How the app finds llmtui

In order:

1. `LLMTUI_EXECUTABLE`;
2. the bundled `Contents/Helpers/llmtui` (release builds);
3. `~/.local/bin`, `~/go/bin`, `/opt/homebrew/bin`, `/usr/local/bin`.

The llama.cpp runtime is not bundled.

### Releases and Gatekeeper

Each `v*` tag attaches `LLMTUIGUI-<version>-macos-arm64.zip` (and a
`.sha256`) to the GitHub release, built with that tag's llmtui embedded.
The workflow is `.github/workflows/macos-setup.yml`. The app's version is
the llmtui version.

The app is ad-hoc signed, not notarized, so macOS quarantines the downloaded
copy. After unzipping, either right-click the app and choose **Open** once,
or run:

```bash
xattr -dr com.apple.quarantine /Applications/LLMTUIGUI.app
```

Ad-hoc signatures change on every build. After an update, macOS may ask
again for the Automation (Mail) and Calendars permissions.

## LLMTUIiOS — iOS chat app

A chat app for iPhone and iPad that talks to a model server on your network
(LM Studio, Ollama or any OpenAI-compatible server). It is **independent of
`config.yaml`**:
- Providers are set up in the app's **Providers** tab, and saved on the
  device.
- API keys are kept in the iOS Keychain.

On a phone, `localhost` is the phone itself, so for a server on your Mac use
the Mac's Wi-Fi address and let the server listen on the local network.

### Features

| Area | What it does |
|---|---|
| **Chats** | Several chats, newest first, each with its own provider. Rename by tapping the title. Pin (swipe right) to keep a chat at the top. Archive (swipe left). Saved on the device. |
| **Tools** | One **Tools** switch runs a bounded tool loop: up to 8 rounds, then one last request without tools so the reply ends with an answer. It is a tool loop, not the macOS app's verified agent loop. Tools: `web_research`, `web_search`, `web_fetch`, the memory tools, `local_context` (date, time, device facts), `ask_user`, and the `document_*` tools when the chat has attachments. |
| **Web research** | `web_research` runs up to 3 DuckDuckGo searches, reads up to 6 pages (4 by default, at most 2 per site), keeps only the relevant passages, and returns numbered sources to cite. |
| **Memory** | Short facts and preferences shared by every chat. The newest saved memories are added to each chat's instructions, and `memory_search` finds older ones. The model is instructed to check memory before asking you or searching the web, and to save lasting facts itself; saving asks first unless the approval mode is *Never ask*. Text that looks like a secret (API keys, tokens, private keys) is refused. Turn memory off or manage it in **Settings › Memory**. |
| **Attachments** | Attach PDFs, plain text and Markdown files, or read the text of screenshots and images. Text is extracted on the device with PDFKit and Vision OCR. The model lists, searches and reads passages through `document_*` tools. |
| **Citations** | Answers cite attachment passages; tap a citation to open the PDF at the page (with the passage highlighted when the page has a text layer), a text file at the lines, or an image with its OCR text. Only passages a tool actually returned in that reply become links. |
| **Rich replies** | Markdown, tables, code, KaTeX math, Mermaid diagrams (**Export PNG**), and images from `![alt](url)` with a full-screen viewer. **Copy** and **Share** on every reply. |
| **Composer** | Send while a reply is generating to queue the message. Reasoning effort menu. Hide the keyboard from the composer row or by tapping the chat. |
| **Appearance** | Light (white cards, violet-to-pink accent) and Dark (charcoal cards, magenta accent), following **Settings › Appearance**. |

### Guardrails

The iOS app keeps these rules; keep them when changing the app.

- **Tool approval.** In **Settings › Tools › Approval**:
  - *Ask every time* (the default): web and memory-changing tools ask first.
  - *Ask for memory changes*: web tools run without asking; saving or
    forgetting a memory still asks.
  - *Never ask*: nothing asks.
- **Attachment text cannot trigger outbound actions.** Once a reply has
  read attachment text, web and memory tools always ask, even in
  *Never ask*.
- **Attachment consent.** Before the first search or read of a chat's
  attachments, the app asks once per chat and provider, naming the provider.
  Only metadata goes into the prompt; text reaches the model only through
  the document tools.
- **Documents stay out of memory.** Attachment content is never saved to
  memory unless you ask.
- **Images.** Only http(s) images are fetched. In a reply that read web or
  attachment content, images wait for a tap that shows the host, so injected
  text cannot send chat data out in an image URL.
- **Citations are validated.** A citation becomes a link only if a document
  tool returned that passage in the same reply. Anything else is shown as
  "[unverified citation]", and link targets the model writes are disabled.
- **Chat retention.** In **Settings › Chat Retention**:
  - Choose a period: 1, 3, **7 (default)**, 14, 30 or 90 days, 1 year, or
    Forever.
  - Then choose **Archive** (default, reversible) or **Delete** (removes the
    chat with its attachments and extracted text, including archived chats).
  - Pinned chats, the open chat, a chat that is replying, and a chat with
    queued messages are never touched.
  - It runs when the app opens or returns to the foreground. A settings
    change that would act on existing chats right away asks first.
- **Storage and cleanup.** Chats (`Application Support/Conversations`) and
  attachments with their extracted text (`Application Support/Attachments`)
  are written with iOS data protection. Imported files are read-only copies
  owned by their chat, and deleting a chat deletes its files. Providers,
  memories and settings are kept in the app's preferences, and API keys in
  the Keychain.

### Limits

PDF 50 MB / 300 pages (OCR on at most 40 image-only pages), text 5 MB,
image 25 MB, 10 attachments per chat. A PDF that hits a limit is marked
**partial**, and searches say which pages were not covered.

### Run it

- **Simulator:** open the project, choose the `LLMTUIiOS` scheme and an
  iPhone simulator, and run.
- **Your iPhone:** set up signing (below), plug the phone in, pick it as the
  run destination, and run. With a free Apple ID, the app expires after 7
  days and you run it again from Xcode.

Unit and UI tests run on a simulator. There is no Makefile target for the
iOS app yet:

```bash
DEVELOPER_DIR=/Applications/Xcode.app/Contents/Developer \
  xcodebuild -project macos/setup/LLMTUIGUI.xcodeproj -scheme LLMTUIiOS \
  -destination 'platform=iOS Simulator,name=iPhone 18 Pro' \
  CODE_SIGNING_ALLOWED=NO test
```

On pull requests that touch `macos/**`, CI (`macos-setup.yml`) runs these
unit tests (`-only-testing:LLMTUIiOSTests`, unsigned) on the newest iPhone
simulator of the runner. The job runs in parallel with the macOS tests and
reports the test counts in the job summary. The UI-test target and device
builds are not run in CI.

See [`Documentation/iOS-Architecture.md`](Documentation/iOS-Architecture.md)
for how the app is built.

## Requirements

- macOS app: macOS 26 or later (`MACOSX_DEPLOYMENT_TARGET = 26.0`).
- iOS app: iOS or iPadOS 18 or later (`IPHONEOS_DEPLOYMENT_TARGET = 18.0`).
- The project is maintained with Xcode 27. CI builds and tests both apps
  (the iOS app on a simulator) on a `macos-26` runner with the newest Xcode
  installed there.

## Signing (local only)

No signing team is committed. To run on your own Mac or iPhone (a free
personal team is enough):

```bash
cp macos/setup/Config/Signing.local.xcconfig.example macos/setup/Config/Signing.local.xcconfig
```

Then set `DEVELOPMENT_TEAM` in the copy to your Team ID (Xcode → Settings →
Accounts). That file is git-ignored, and every target, macOS and iOS,
inherits the team from it.

Do **not** pick a team in a target's Signing & Capabilities tab. Xcode would
write it into `project.pbxproj`, and CI rejects a committed team ID. When
adding a new target, leave its Team set to **None**.

Xcode can still write `DEVELOPMENT_TEAM` into `project.pbxproj` on its own.
Before committing, check with:

```bash
make macos-setup-guard
```

To run that check on every commit, install it as a pre-commit hook:

```bash
ln -s ../../macos/setup/scripts/check-no-signing-identity.sh .git/hooks/pre-commit
```

If it fails, delete every `DEVELOPMENT_TEAM = …;` line from
`project.pbxproj`, including empty ones (`DEVELOPMENT_TEAM = "";`). An empty
value hides the team from your local xcconfig, so Xcode asks for a team
again and writes it into a target.

## Build and test the macOS app

If `xcode-select` points at the Command Line Tools, the Makefile targets set
`DEVELOPER_DIR` to `/Applications/Xcode.app` for you.

```bash
make macos-setup-test
```

```bash
make macos-setup
```

`make macos-setup` builds llmtui, then runs `scripts/package-app.sh`. That
script:
- builds the app in Release mode and embeds the llmtui binary at
  `Contents/Helpers/llmtui`;
- signs it ad hoc with the hardened runtime, keeping the Apple Events,
  Calendars and network-client entitlements but dropping `get-task-allow`;
- verifies the signature and writes
  `dist/LLMTUIGUI-<version>-macos-<arch>.zip` plus a `.sha256` file.
