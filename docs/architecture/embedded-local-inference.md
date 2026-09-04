# ADR: Embedded Local-Model Inference

Status: **Accepted** (2026-07-18)

## 2026-09-04 addendum: Linux NVIDIA CUDA and multi-GPU validation

Status: **Accepted (validation record)**. This addendum records a manually
validated deployment and updates stale version/support text. It changes no
design decision.

### Pin (supersedes all earlier version numbers in this ADR)

`internal/runtime/pin.json` is authoritative. As of this addendum:

- yzma `v1.24.0` (`go.mod`).
- llama.cpp tag `b10549`, commit `b2e5e9b28b2484fbf94b543432ece638996a8b97`.
- Compatible build range `b10545`–`b10549`.

Every earlier "`yzma v1.19.0`", "`b10066`", "commit `86a9c79…`" and
"`b9979`+" reference elsewhere in this document is historical and is
superseded by the values above.

### What this addendum supersedes

- **"Non-goals (first increment)"** listed *CUDA validation* as out of scope.
  CUDA inference has now been exercised end to end (details below). It remains
  a **manually validated, non-CI, non-vendor-certified** path — the design
  non-goal is lifted, a support guarantee is not added.
- **"Supported platforms (first release)"** marks Linux amd64/arm64 and
  Windows amd64 as *"Compiles but untested"*. Linux amd64 CPU inference is now
  CI-tested (per the 2026-08-19 addendum) and Linux amd64 CUDA inference has
  been manually validated. The other cells stand.
- **"Native build strategy" / "Upstream pinning"** still reference
  `scripts/fetch-llama-runtime.sh` as the fetch mechanism. That script now
  only prints a deprecation notice and delegates to `llmtui runtime install`;
  the 2026-08-19 addendum is the current description.

### Validated deployment

| Layer | Validated configuration |
| --- | --- |
| Hypervisor | VMware ESXi; GPUs assigned via **VMDirectPath I/O** (raw passthrough). No NVIDIA vGPU profile, no vGPU Manager. |
| Guest | Rocky Linux 10.x x86_64, EFI firmware, full guest-memory reservation, `pciPassthru.use64bitMMIO=TRUE` with a 64-bit MMIO window rounded up to cover the firmware's reported BAR requirement. |
| GPU | NVIDIA A16 — a board of **four independent 16 GB GPU processors**; four processors passed through as four distinct CUDA devices (~60 GiB aggregate, **not** one contiguous device). Compute capability 8.6. |
| NVIDIA stack | Open DKMS kernel module; driver 610.57.04; CUDA toolkit 13.3 (`nvcc` 13.3.73); host GCC 14.3.1. Reference versions, not minimums. |
| Runtime | llama.cpp built from the pinned tag with `-DBUILD_SHARED_LIBS=ON -DGGML_CUDA=ON`, installed to `/opt/llmtui/llama-<tag>-cuda/lib64`, registered via `/etc/ld.so.conf.d` + `ldconfig`, selected with `providers.<name>.library_path`. |
| Model | `gpt-oss-20b` `Q4_K_S` GGUF; layers split across the selected GPUs; ~34 tok/s observed on two A16 processors (uncontrolled). |

The selected design was unchanged by this exercise:

```text
embedded provider → llamart → yzma → dynamically loaded llama.cpp/ggml
  libraries → CUDA backend → multiple passed-through NVIDIA GPU devices
```

No sidecar, hidden HTTP server, application `import "C"`, or build-tag
bifurcation was introduced. The GPU layer split is llama.cpp's own default:
llmtui exposes only `gpu_layers` (`-1` → all layers), not `tensor_split`,
`split_mode`, `main_gpu`, or device selection; device visibility is
constrained with `CUDA_VISIBLE_DEVICES` at launch.

### Packaging unchanged

`llmtui runtime install` still ships **no CUDA runtime**; `--backend cuda`
still errors (no CUDA entry in `pin.json`). CUDA is an administrator-provided
`library_path` runtime and carries the documented trusted-override caveat: it
is probed for the expected `libllama`/`libggml*` files and a version-stamp
mismatch warns, but its files are not hashed against the embedded manifest.

The full operator procedure is `docs/embedded-cuda-linux.md`.

## 2026-08-19 addendum: verified runtime distribution

Status: **Accepted and implemented**. This addendum supersedes the original
native build strategy, supported-platform, resource-lifecycle, packaging,
security-download, known-limitation, and upstream-pinning text where they
conflict.

llmtui still uses yzma v1.19.0, in-process dynamic loading, no application
`import "C"`, and no sidecar. llama.cpp b10066 and commit
`86a9c79f866799eb0e7e89c03578ccfbcc5d808e` are now defined once in
`internal/runtime/pin.json` together with the yzma compatibility range,
official platform asset URLs/sizes/SHA-256 values, regular payload hashes, and
trusted aliases. Self-contained release archives ship the trimmed runtime at
`lib/llmtui/runtime`; no GGUF model is bundled.

Runtime precedence is explicit `library_path`, `YZMA_LIB`, executable-relative
bundle, managed user-data `runtime/<tag>`, then a matching stamped legacy
directory. The first two remain trusted advanced overrides. Bundled/managed
tiers require an owner-controlled directory, matching stamp and embedded file
set, and primary-library hash; `runtime verify` hashes every file. Resolution
performs no network access and no writes.

`llmtui runtime install|list|verify|uninstall` is the management surface.
Install is the only network path: it downloads an exact pinned URL, enforces
exact size and SHA-256 before archive parsing, extracts only regular allowlisted
payloads, recreates aliases from the embedded pin, fully verifies staging, and
publishes by atomic rename. Unknown existing destinations are never deleted.
Uninstall trusts the embedded pin rather than arbitrary manifest filenames.

The actual provider state machine is `unloaded -> ready -> closed`; model
loading is serialized inside the first chat but is not a distinct externally
observable state. Provider/model switch and app quit free projector, context,
and model; there is no `/runtime unload` command. The process-global native
backends remain initialized until exit.

One process-lifetime abort callback uses opaque numeric runtime IDs and a Go
registry, avoiding permanent purego callback allocation on model switches.
Failed llama library loads do not pin the attempted directory and can retry.
Windows registers the absolute verified runtime directory with
`AddDllDirectory`. Linux libffi initialization is lazy: missing
`libffi.so.8` fails only embedded inference with an actionable error, never
process startup or unrelated providers. Linux release notes and setup docs
retain `libffi8` as a system prerequisite.

CI builds and extracts every platform archive and requires doctor to report a
verified bundled tier. CPU native inference runs on macOS arm64 and Linux
amd64 against an immutable, size/SHA-256-pinned SmolLM2 GGUF fixture. macOS
continues to use `CGO_ENABLED=1`; Linux and Windows remain `CGO_ENABLED=0`.

## 2026-07-18 addendum: multimodal, tools, and reasoning

Status: **Accepted for the next increment**. This addendum supersedes only the
first increment's vision/native-tool non-goals, tool-calling section, chat
template restriction, and corresponding known limitations. The selected
in-process yzma/purego architecture, optional provider boundary, lazy health
check, serialized native access, cancellation, release packaging, and security
model remain unchanged.

The next increment extends one embedded provider entry into an explicit model
pair: a main GGUF and, when vision is enabled, its compatible `mmproj` GGUF.
`yzma/pkg/mtmd` loads the projector lazily from the same pinned llama.cpp
library directory and evaluates in-memory PNG/JPEG attachments. A provider with
no projector remains text-only and never loads `libmtmd`. Because projector
compatibility cannot be inferred safely from sibling filenames, a vision
provider exposes only its configured main model; another model/projector pair
requires another provider entry.

The existing shared request/event contract is sufficient for feature parity:
`ChatRequest.Tools`, `ChatRequest.Reasoning`, and `Message.Images` flow through
the embedded runtime, while `EventReasoning`, `EventDelta`, and terminal
`EventDone.ToolCalls` feed the existing TUI reasoning and approved tool loop.
No embedded-specific tool executor is introduced. Model-family tool grammars
come from yzma's `pkg/message`; unrecognized automatic formats keep the current
synchronous unsupported-tool error so the established prompt fallback still
works. LLMTUI adds one schema-aware Gemma compatibility seam for
`call:name{}`: yzma intentionally omits empty argument blocks, but an empty
object is valid for tools such as pathless `list_dir`. The adapter accepts that
shape only when the offered JSON schema has no missing required property, then
uses the same shared approval/execution/continuation loop. A request-local
Gemma hint requires a user-facing answer after tool results without modifying
stored conversation history.

GGUF chat-template metadata is authoritative but not every valid Jinja template
is supported by llama.cpp's `ChatApplyTemplate` convenience renderer. This was
reproduced with Gemma 4 E4B: model loading succeeds, the metadata template is
present, and `ChatApplyTemplate` rejects its macros/namespace/tool branches.
The runtime therefore tries that fast renderer for compatible auto/text
requests, then falls back automatically to the full `ardanlabs/jinja` renderer.
The fallback omits `enable_thinking` for `auto`, sets it only for explicit
`on`/`off`, and supplies tools/rich messages when required. The YAML
`chat_template` field remains an escape hatch for genuinely absent or broken
metadata, not a per-model setup requirement.

Multimodal prompts clear and invalidate the text prefix-KV bookkeeping because
image embeddings cannot be represented by its token slice. Every bitmap,
input-chunk list, projector context, llama context, and model has explicit,
ordered cleanup. Images stay in memory, are bounded and validated before FFI,
are never logged, and retain the existing history policy that does not persist
attachment bytes. Native mtmd sections that cannot be interrupted mid-call
observe cancellation at the next safe boundary; llama decode retains its abort
callback.

The compatibility invariant is unchanged: old YAML, text-only embedded models,
remote providers, default selection, and all five release targets must retain
their v0.9.5 behavior. A release-validation addendum below records why macOS
artifacts now use the real cgo runtime. The implementation and acceptance
checklist is maintained in `.claude/tasks/embedded-local-inference-plan.md`.

## Current architecture summary

llmtui contains no application cgo code or build tags. All LLM
backends implement `provider.Provider` (`internal/provider/provider.go`):
`Chat(ctx, req)` returns a `<-chan ChatEvent` that emits `EventDelta` /
`EventReasoning` events and exactly one terminal `EventDone`/`EventError`,
then closes; implementations must respect context cancellation. Providers are
constructed by a `switch pc.Type` in `internal/app/factory.go` from
`config.ProviderConfig` entries. The TUI owns retries, an inactivity
watchdog, a native-tool fallback (`toolsRejectedError` → prompt-based tool
protocol), a response cache keyed by request-shaping fields
(`internal/cache`), and drains abandoned streams so producer goroutines exit.
Release builds cover the desktop platforms in a native GitHub Actions matrix
(plus a runtime-less Android archive). macOS uses `CGO_ENABLED=1` so Metal's
native threads run with Go's real cgo runtime; Linux and Windows remain
`CGO_ENABLED=0`.

The embedded runtime is pinned once in `internal/runtime/pin.json` (yzma
`v1.24.0`, llama.cpp `b10549`, compatible builds `b10545`–`b10549`). Packaged
acceleration is Metal (macOS arm64) and the pinned Vulkan pack (Linux/Windows
amd64/arm64); NVIDIA CUDA on Linux is a manually validated
administrator-supplied `library_path` runtime (2026-09-04 addendum,
`docs/embedded-cuda-linux.md`).

## Problem statement

Users must run a separate inference server (Ollama, LM Studio, llama-server)
to chat with a local model. llmtui should be able to load a GGUF model from
disk into its own process and stream tokens directly, with no HTTP server.

## Goals

- In-process GGUF inference on macOS Apple Silicon (Metal, CPU fallback).
- Model loads once per chat session; streaming, cancellation, usage stats,
  chat templates, Unicode-safe output.
- Strictly opt-in; zero impact on existing providers, builds, and releases.

## Non-goals (first increment)

Model downloads/marketplace, model conversion, training, embeddings, vision,
multiple simultaneously loaded models, CUDA validation, native tool calling
for local models, an MLX backend, KV-prefix reuse beyond the conservative
strategy described below.

## Evaluated alternatives

| Option | Verdict | Reason |
| --- | --- | --- |
| A: cgo thin shim over pinned llama.cpp static libs | Rejected | Strongest compile-time safety, but requires compiling/linking llama.cpp into every artifact and forces build-tag bifurcation. Merely enabling Go's cgo runtime in a native macOS build does not adopt this design. |
| B: raw purego dlopen | Rejected | Struct-by-value ABI mirrors of `llama_context_params` (30+ volatile fields) would be hand-maintained per llama.cpp bump — exactly the class of silent-corruption risk we must not own. |
| C: maintained Go binding — **hybridgroup/yzma v1.19.0** | **Selected** | Apache-2.0; actively tracks llama.cpp (supports builds b9979+; CI runs against each upstream release); purego + jupiterrider/ffi handles ABI marshaling; covers the full needed API (verified against source: model/context lifecycle, tokenizer, batch/decode, sampler chain, `ChatApplyTemplate`, `ModelChatTemplate`, metadata, `MemoryClear`, `SetAbortCallback`, log silencing, `*DefaultParams()` backed by C calls); binding package `pkg/llama` adds only purego + jupiterrider/ffi + x/sys; no network access in the binding; cross-compiles for all five release targets with no build tags. |
| D: reuse Ollama internals | Rejected | `ollama/llama` is an internal implementation detail coupled to Ollama's fork and build layout; would embed a second application. Ollama stays an HTTP provider. |
| E: sidecar runner process | Rejected as primary | In-process loading is practical (Option C); a sidecar would be a hidden HTTP/IPC server contradicting the feature's purpose. Remains the documented mitigation path if native crashes ever prove unmanageable. |
| F: MLX | Rejected for now | mlx-c is tensor-op-level only; the LLM runtime (mlx-lm) is Python. No bindable native runtime exists; would mean writing an inference engine. Extension path preserved (see below). |

## Selected implementation

New package `internal/provider/embedded`:

- `Provider` implements `provider.Provider`, `provider.CapabilityReporter`,
  and a new optional `provider.Closer` interface.
- A small internal `Runtime` interface (load model, describe, generate with
  a per-token callback, close) isolates yzma behind one seam. Two
  implementations: `llamart` (yzma-backed, real inference) and a mock runtime
  for provider-contract tests, which run everywhere with no native library.
- The TUI gains no llama.cpp-specific logic; it sees an ordinary provider.

### Dependency & license analysis

- `github.com/hybridgroup/yzma/pkg/llama` (Apache-2.0, incl. MIT-licensed
  portions from dianlight/gollama.cpp) → transitively only
  `ebitengine/purego`, `jupiterrider/ffi`, `golang.org/x/sys` (all
  permissive). `pkg/download` (go-getter, cloud SDKs) is **never imported**.
- llama.cpp itself (MIT) is **not vendored or compiled**; users supply its
  dynamic libraries. Attribution for yzma/llama.cpp ships in
  `THIRD_PARTY_NOTICES.md`.

### Native build strategy

There is no llama.cpp native build or link step in this repository. The
embedded runtime dynamically loads `libllama`/`libggml*` at runtime. The
macOS Go artifact enables cgo only to initialize the real cgo thread runtime;
it links exclusively to Apple system libraries and frameworks. Users obtain
the llama.cpp libraries by:

1. Downloading the official llama.cpp release archive for their platform
   (pinned tag, SHA256-verified) — convenience script
   `scripts/fetch-llama-runtime.sh` automates this with a hardcoded pinned
   tag and checksum; it runs only when the user invokes it.
2. Building llama.cpp from source (`cmake -B build -DBUILD_SHARED_LIBS=ON`;
   Metal is on by default on macOS) — documented in `docs/embedded.md`.

The library directory is configured via `providers.embedded.library_path`
(or the `YZMA_LIB` environment variable that yzma honors natively).

### Supported platforms (first release)

- **Supported**: macOS Apple Silicon (arm64), Metal by default, CPU
  fallback via `gpu_layers: 0`.
- **Compiles but untested**: Linux amd64/arm64, Windows amd64 (yzma claims
  support; we do not, until exercised).
- **Not supported**: darwin/amd64 inference (yzma matrix excludes it; the
  Go code still compiles there and fails gracefully at load).

### Backward-compatibility strategy

- New provider `type: "embedded"` in the existing factory switch; no
  existing type, key, flag, env var, command, or default changes.
- The `embedded` provider is **not** in `builtinProviders()` defaults with
  an active role; it is configured explicitly (a commented example is added
  to `DefaultYAML`). Normal startup never touches it.
- `provider.Closer` is optional (mirrors `CapabilityReporter`); existing
  providers are untouched. The TUI calls `Close` on provider swap and quit.
- Cache key gains a `RuntimeID` field — a fingerprint of the model-file
  identity (path, size, mtime) plus native sampling/context settings,
  supplied by providers implementing `provider.RuntimeFingerprinter` and
  empty for remote providers; version bumps v6→v7 (one-time cache
  invalidation, no correctness impact).

### Resource lifecycle

Engine state machine (mutex-guarded): `unloaded → loading → ready → closed`.

- `New` (factory): validates nothing heavy; instant.
- `HealthCheck`: cheap stat-level checks only (model file exists and is a
  regular file; library directory contains the expected library). Never
  loads the model — the TUI's 4s startup health timeout with silent
  demo-mode fallback (`internal/tui/app.go`) makes anything slower unsafe.
- First `Chat` triggers the load inside the producer goroutine (never on
  the TUI event loop): `llama.Load` → `Init` → `ModelLoadFromFile` →
  `InitFromModel`. Load progress is surfaced as `EventProgress` activity
  ("loading model …"), which the TUI already treats as watchdog-resetting
  progress, so a slow load cannot trip the inactivity timeout.
- Subsequent `Chat` calls reuse the loaded model/context (per Definition of
  Done #5).
- `Close` (provider swap, `/runtime unload`, app quit): frees sampler,
  context, model, backend deterministically under the engine mutex; further
  `Chat` calls on a closed provider return an error. No finalizers.
- yzma documents no thread-safety contract, so all native calls for a given
  engine are serialized by the engine mutex; one generation at a time.

### Streaming model

Per request (all inside the producer goroutine, engine lock held):

1. Apply chat template (below) → prompt string → `Tokenize`.
2. Validate context budget: prompt tokens + max new tokens ≤ effective
   `n_ctx` (min of configured `context_size`, model `n_ctx_train`).
3. KV strategy (conservative): if the new prompt's token sequence starts
   with the previous request's full token sequence, keep the KV cache and
   decode only the suffix; otherwise `MemoryClear` and decode the full
   prompt in `n_batch` chunks. Exact-prefix match only; anything unclear
   falls back to full re-decode. Correctness over speed.
4. Generation loop: `Decode` → `SamplerSample` → EOG check →
   `TokenToPiece` into a UTF-8 assembler that emits only complete runes
   (partial multibyte sequences are buffered; the remainder is flushed at
   end); stop-string scanning holds back a window of pending text.
5. Emit `EventDelta` per assembled piece via `provider.Emit`; finish with
   one `EventDone` carrying real (non-estimated) `Usage`.

### Cancellation model

`SetAbortCallback(ctx, func() bool { return goCtx.Err() != nil })` aborts
C-side compute between graph steps, and the Go loop checks `ctx.Err()`
between decode iterations. Cancellation emits the standard error path the
TUI already maps to "canceled" (partial reply kept); the engine stays
`ready` and the next prompt works. Abandoned-stream draining is inherited
from the existing TUI pattern.

### Error model

All native failures return wrapped Go errors mapped to actionable messages:
library missing (with install instructions), model file missing/directory/
invalid GGUF, unsupported architecture, context allocation failure, missing
chat template, canceled vs failed. No panics; the yzma `SetProgressCallback`
panic sites are simply never used.

### Configuration and CLI design

`ProviderConfig` gains optional embedded-only fields (ignored by other
types, `omitempty`): `model_path`, `library_path`, `context_size`,
`gpu_layers` (-1 = all/auto, 0 = CPU), `threads`, `batch_size`, and a
`sampling` block (`top_k`, `min_p`, `repeat_penalty`, `repeat_last_n`,
`seed`, `stop`). Temperature/top-p/max-tokens flow through the existing
`ChatRequest` fields and flags. `--model` may be a `.gguf` path for the
embedded provider (`llmtui chat --provider embedded --model ~/M/x.gguf`).
Two new optional persistent flags, `--context-size` and `--gpu-layers`,
bind only when set (existing precedence rules). `ListModels` returns the
configured model plus sibling `*.gguf` files for the model picker.

### Tool calling

Capabilities are honest: no native tool support is advertised. If the TUI
sends native `Tools`, the provider returns an error phrased to match the
existing `toolsRejectedError` detector, so the session falls back to the
established prompt-based tool protocol automatically — the same path every
non-tool-capable remote model already takes. Remote-provider tool calling is
untouched.

### Chat templates

`ModelChatTemplate(model, "")` from GGUF metadata, applied with
`ChatApplyTemplate`. A config `chat_template` override exists for models
with broken metadata. If no usable template exists and no override is set,
the request fails with an actionable error — no silent guessed formats.

### Testing strategy

- Provider-contract, lifecycle, cancellation, UTF-8 assembly, stop-string,
  context-validation, config, factory, cache-key tests run against the mock
  runtime everywhere (CI included), no native library needed.
- Real-inference integration tests are opt-in:
  `LLMTUI_TEST_GGUF=/path/to/model.gguf` (+ `YZMA_LIB` or configured
  library path); they skip with a clear message otherwise.
- `go test -race` covers the Go side of engine serialization.
- Existing provider suites are the regression gate.

### Packaging strategy

Release target coverage is unchanged. GitHub Actions builds each OS/architecture
on a matching native runner and publishes the five artifacts together. macOS
arm64/amd64 use `CGO_ENABLED=1`; Linux amd64/arm64 and Windows amd64 remain
`CGO_ENABLED=0`. `make dist` builds the current native target, while
`make dist-platform` is the CI primitive. Every binary contains the embedded
provider, which activates only when the user supplies native libraries.
Missing libraries produce an actionable error, not a linker failure or panic.

### 2026-07-18 release-validation addendum: macOS cgo runtime

The first packaged Apple Silicon smoke test exposed a deterministic SIGSEGV in
Metal during `llama.Decode`. It reproduced with 14-token and 1354-token prompts,
with both `BatchGetOne` and llama-owned batches, and with current stable
purego/libffi patch releases. `CGO_ENABLED=0` plus `gpu_layers: 0` passed;
Metal plus the ordinary cgo-enabled Go build passed the full Gemma suite and
the real TUI. The fault boundary is therefore purego's fake-cgo/native-thread
environment on macOS, not model templates, prompt batch size, or Go memory.

The narrow packaging fix is native macOS release jobs with `CGO_ENABLED=1`.
This adds no `import "C"` to llmtui, does not compile or link llama.cpp, and
does not change runtime configuration. The verified arm64 artifact depends
only on `/usr/lib/libSystem`, `/usr/lib/libresolv`, CoreFoundation, and
Security; llama.cpp remains an explicitly configured dynamic dependency.

### Security considerations

Model paths are validated (exists, regular file, `~` expansion, no shell
interpolation); GGUF content is untrusted input handled by llama.cpp —
load failures surface as errors. The native library path is user-controlled
by design (documented risk: loading a library is executing code; only point
`library_path` at libraries you trust). No downloads except the explicit,
pinned, checksum-verified fetch script. Tool approval guardrails are
unchanged; model output cannot bypass them. Prompts/outputs are not logged.

### Known limitations

Single loaded model; no native tool calling; no vision; prompt processing
re-decodes on prefix mismatch; ABI mismatch between yzma and an arbitrary
user-supplied llama.cpp build is detected only via symbol-resolution failure
at `Load` (mitigated by documenting the pinned, tested llama.cpp tag);
thread-safety of the native context is enforced by our own serialization.

### Future MLX extension path

MLX (or any other runtime) plugs in as a second `Runtime` implementation
behind `internal/provider/embedded`, or as a sibling provider type — the
TUI, agent loop, config plumbing, and cache-key changes are runtime-neutral.
No TUI rewrite is required.

### Upstream pinning and upgrade procedure

- `hybridgroup/yzma` pinned in `go.mod` (v1.19.0).
- llama.cpp runtime pinned in `scripts/fetch-llama-runtime.sh`
  (tag + SHA256) and documented in `docs/embedded.md`. Upgrade = bump yzma
  per its compatibility table, bump the pinned llama.cpp tag/checksum, run
  the opt-in integration suite against a real model, update docs.
