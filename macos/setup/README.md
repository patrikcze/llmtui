# LLMTUIGUI — macOS setup app

A SwiftUI app for configuring llmtui graphically on macOS. It edits the same
`~/.config/llmtui/config.yaml` the terminal app reads — mainly Personal Apps
(Mail / Calendar), plus providers, model profiles, agent, entities, tools and
other settings. A secondary chat view (with its own agent and tool loop) talks
to an LM Studio or Ollama server using the provider settings from that config;
it does not run embedded (GGUF) models.

Apart from sharing the config file and calling the `llmtui` binary for
commands such as `config path` and `doctor`, the app is independent of the Go
code. See `Documentation/Architecture.md` for its internal layout.

## Requirements

- macOS 26 or later (deployment target 26.0).
- Xcode 26 or later to build. The project is maintained with Xcode 27.

## Open in Xcode

Open `macos/setup/LLMTUIGUI.xcodeproj` from this repository. Source folders
are Xcode *synchronized folders*: any file added under `LLMTUIGUI/` or
`LLMTUIGUITests/` is part of the target automatically.

### Signing (local only)

No signing team is committed. To run with your own Apple ID (a free personal
team is enough):

```bash
cp macos/setup/Config/Signing.local.xcconfig.example macos/setup/Config/Signing.local.xcconfig
```

Then set `DEVELOPMENT_TEAM` in the copy to your Team ID (Xcode → Settings →
Accounts). That file is git-ignored. Do **not** pick a team in the target's
Signing & Capabilities tab — Xcode would write it into `project.pbxproj`, and
CI rejects a committed team ID.

Xcode can still write `DEVELOPMENT_TEAM` into `project.pbxproj` on its own.
Before committing, check with:

```bash
make macos-setup-guard
```

To run that check on every commit, install it as a pre-commit hook:

```bash
ln -s ../../macos/setup/scripts/check-no-signing-identity.sh .git/hooks/pre-commit
```

If it fails, delete every `DEVELOPMENT_TEAM = …;` line from `project.pbxproj`,
including empty ones (`DEVELOPMENT_TEAM = "";`). An empty value hides the team
from your local xcconfig, so Xcode asks for a team again and writes it into a
target. When adding a new target in Xcode, leave its Team set to **None**; it
inherits your team from the xcconfig.

## Build and test from the command line

If `xcode-select` points at the Command Line Tools, the Makefile targets set
`DEVELOPER_DIR` to `/Applications/Xcode.app` for you.

```bash
make macos-setup-test
```

```bash
make macos-setup
```

`make macos-setup` builds llmtui, then runs `scripts/package-app.sh`. That
script builds the app in Release mode and embeds the llmtui binary at
`Contents/Helpers/llmtui`. It signs the app ad hoc with the hardened runtime,
keeping the Apple Events, Calendars and network-client entitlements but
dropping `get-task-allow`. It then verifies the signature and writes
`dist/LLMTUIGUI-<version>-macos-<arch>.zip` plus a `.sha256` file. The app's
version is the llmtui version it is built with.

The Go gates (`make check`, `go test ./...`) do not build or test this app.

## How the app finds llmtui

In order:

1. `LLMTUI_EXECUTABLE`;
2. the bundled `Contents/Helpers/llmtui`;
3. `~/.local/bin`, `~/go/bin`, `/opt/homebrew/bin`, `/usr/local/bin`.

The llama.cpp runtime is not bundled. Embedded inference stays a terminal
llmtui feature (`llmtui runtime install`).

## Releases and Gatekeeper

Each `v*` tag attaches `LLMTUIGUI-<version>-macos-arm64.zip` to the GitHub
release (`.github/workflows/macos-setup.yml`). The app is ad-hoc signed, not
notarized, so macOS quarantines the downloaded copy. After unzipping, either
right-click the app and choose **Open** once, or run:

```bash
xattr -dr com.apple.quarantine /Applications/LLMTUIGUI.app
```

Ad-hoc signatures change on every build. After you update, macOS may ask
again for the Automation (Mail) and Calendars permissions.
