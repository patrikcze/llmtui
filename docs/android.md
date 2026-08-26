# Android (Termux)

llmtui runs on Android under [Termux](https://termux.dev/), for any provider
that talks over HTTP: Ollama (remote or running on your LAN), LM Studio, or
any OpenAI-compatible endpoint. The [embedded](embedded.md) GGUF provider is
**not supported on Android** — llama.cpp publishes no official Android
release, so there is no runtime to bundle or install. Everything else in
llmtui (chat, streaming, config, history, tools, skills) works the same as
on desktop.

## Why the plain Linux release doesn't run here

If you download the `linux-arm64` release and run it inside Termux, it
fails immediately:

```text
error: "…/llmtui" has unexpected e_type: 2
```

That is Android's Bionic dynamic linker refusing the binary — not an
architecture mismatch. Since Android 5.0, Bionic only executes
position-independent executables (`ET_DYN`, e_type 3); a normal desktop
Linux build is `ET_EXEC` (e_type 2). Use an actual Android build (below),
not the `linux-arm64` archive.

## Recommended: build on-device in Termux

This is the simplest path and needs no NDK setup — Termux's own Go and
clang packages already target Bionic directly:

```bash
pkg update
pkg install golang clang git
git clone https://github.com/patrikcze/llmtui.git
cd llmtui
go build -o llmtui ./cmd/llmtui
./llmtui doctor
```

`go env GOOS` reports `android` automatically inside Termux, and `CGO_ENABLED`
defaults to on because `clang` is on `PATH` — both are required (see
[Why CGO is required](#why-cgo-is-required-not-just-pie) below). No
`-buildmode=pie` flag is needed either; Termux's toolchain already links PIE
by default. If you hit build errors, confirm `clang --version` and
`go version` both work first.

## Alternative: cross-compile with the Android NDK

For CI or building an Android archive from another machine, `make
dist-platform` / `make dist-archive` support `TARGET_GOOS=android` directly,
provided the NDK is available:

```bash
export ANDROID_NDK_HOME=/path/to/android-ndk   # or ANDROID_NDK_ROOT / ANDROID_NDK_LATEST_HOME
make dist-archive TARGET_GOOS=android TARGET_GOARCH=arm64
```

This resolves the NDK's standalone clang (`aarch64-linux-android<API>-clang`,
API 24 by default — override with `ANDROID_API=<level>`), sets
`CGO_ENABLED=1`, and links with `-buildmode=pie`. Because there's no
upstream llama.cpp Android release, this path packages the binary with
`LICENSE`, `THIRD_PARTY_NOTICES.md`, and this document — no `lib/llmtui/runtime`
runtime bundle, unlike the desktop archives. It still includes the starter
skills and plugins under `examples/`. If `ANDROID_NDK_HOME` isn't set or the
expected clang binary isn't found, the build fails immediately with the
missing path rather than producing a broken binary.

The project's release CI builds `android/arm64` this way using
[`nttld/setup-ndk`](https://github.com/nttld/setup-ndk); see
`.github/workflows/release.yml` for the exact NDK version pinned there.

## Why CGO is required, not just PIE

Two independent things need `runtime/cgo` on Android, not just a linker flag:

- **PIE.** Bionic requires it, and Go's linker only supports
  `-buildmode=pie` for `android/arm64` through external (cgo) linking.
- **DNS.** Go's pure-Go resolver depends on `/etc/resolv.conf`, which
  Android doesn't provide in the expected form. Without cgo, hostname
  lookups fail even when the network itself is fine. With `CGO_ENABLED=1`,
  Go automatically prefers the cgo resolver on Android, and connecting to
  Ollama/LM Studio/an OpenAI-compatible host by name works normally.

## Quick start once built

```bash
./llmtui config init
./llmtui chat --provider ollama --base-url http://<host-running-ollama>:11434 --model qwen3
```

Point `--base-url` at a machine on your network actually running Ollama, LM
Studio, or an OpenAI-compatible server — the phone itself is the client.
Config, history, and cache paths resolve the same way as on Linux
(`$HOME/.config/llmtui`, XDG data/cache dirs), since Termux sets `$HOME` to
its own sandboxed home directory.

## What isn't supported

- The `embedded` provider (local in-process GGUF inference) — no upstream
  llama.cpp Android release exists to bundle or verify.
- `runtime install` on Android reports the platform as unsupported for the
  same reason.

Everything else — providers, config, TUI, tools, skills, history — is the
same llmtui as on desktop.
