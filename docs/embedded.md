# Embedded GGUF inference

The `embedded` provider runs a GGUF model inside the llmtui process through
llama.cpp. It needs no Ollama, LM Studio, HTTP server, or API key. It is
opt-in: existing providers and the default `llmtui chat` flow are unchanged.

The implementation uses `hybridgroup/yzma` with purego and libffi. Native
llama.cpp libraries are included in self-contained release archives and remain
dynamically loaded. Bare binaries and source builds can install the same pinned
runtime with an explicit command; startup and inference never download it.
Published macOS binaries enable Go's cgo runtime so Metal can safely use native
threads without adding application `import "C"` code.

## Platform status

| Platform | Status | Acceleration |
| --- | --- | --- |
| macOS arm64 (Apple Silicon) | Native CI and packaged runtime | Metal or CPU |
| macOS amd64 | Packaged runtime | CPU |
| Linux amd64 | Native CPU CI and packaged runtime | CPU packaged; pinned Vulkan pack via `runtime install --backend vulkan`; NVIDIA CUDA via a self-built `library_path` runtime (manually validated once — see below) |
| Linux arm64 | Packaged runtime | CPU packaged; pinned Vulkan pack via `runtime install --backend vulkan` |
| Windows amd64 | Packaged runtime | CPU packaged; pinned Vulkan pack via `runtime install --backend vulkan` |
| Android arm64 (Termux) | Not supported — no upstream llama.cpp release | — |

"Packaged" / "CI" mean project-supported and exercised in automation.
"Manually validated once" means one lab deployment was verified and recorded,
not that it is CI-tested or vendor-certified. The normal Ollama and
OpenAI-compatible providers remain portable regardless of embedded-runtime
support on the host. See [android.md](android.md) for Termux/Android specifics.

**Linux + NVIDIA CUDA:** `llmtui runtime install` never installs a CUDA
runtime. Running the embedded provider on NVIDIA GPUs (including GPUs passed
through to a VM) needs a manually compiled llama.cpp runtime selected with
`library_path`. The full procedure — VMware ESXi passthrough, the NVIDIA
driver, building the pinned revision with `-DGGML_CUDA=ON`, dynamic-linker
setup, multi-GPU behaviour and troubleshooting — is in
[embedded-cuda-linux.md](embedded-cuda-linux.md), based on a validated Rocky
Linux / NVIDIA A16 deployment.

## Install the native runtime

If you downloaded a self-contained release archive, no runtime setup is
required. Keep the `llmtui` binary and its adjacent `lib/llmtui/runtime`
directory together.

For a bare binary or source build, run:

```bash
llmtui runtime install
```

This explicit command downloads the official pinned asset for the current
platform (the tag in `internal/runtime/pin.json`, currently `b10549`),
verifies its exact byte size and pinned SHA-256 before parsing it, extracts
only the embedded allowlist, recreates trusted library aliases, fully verifies
the result, and atomically installs it under the platform user-data directory
as `llmtui/runtime/<tag>`. It never downloads a model.

Management commands:

```bash
llmtui runtime list
llmtui runtime verify
llmtui runtime uninstall
```

`--dest` on `runtime install` is an advanced override used by release staging.
The deprecated `scripts/fetch-llama-runtime.sh` wrapper delegates to this
command.

Resolution precedence is:

1. `providers.<name>.library_path` (trusted advanced override)
2. `YZMA_LIB` (trusted advanced override)
3. release-archive runtime beside the executable (manifest verified)
4. managed user runtime for the pinned tag (manifest verified)
5. matching stamped legacy `~/.local/share/llmtui/llama.cpp` directory

Managed directories must be owner-controlled and not group/world writable.
Their version, file set, and primary library hash must match the manifest
embedded in llmtui. `llmtui runtime verify` hashes every managed file.

Linux amd64/arm64 and Windows amd64 can install the official pinned Vulkan
variant explicitly:

```bash
llmtui runtime install --backend vulkan
```

The host Vulkan loader and GPU driver still come from the operating system or
GPU vendor. To use another accelerator build (NVIDIA CUDA, ROCm, SYCL, …),
compile the pinned llama.cpp revision as shared libraries and select it with
`library_path` or `YZMA_LIB`:

```bash
# PIN = the llama_tag from internal/runtime/pin.json in your llmtui source
# tree; `llmtui doctor` also prints the tag llmtui expects. Currently b10549.
PIN=b10549
git clone https://github.com/ggml-org/llama.cpp.git
cd llama.cpp && git checkout "$PIN"
cmake -B build -DBUILD_SHARED_LIBS=ON -DCMAKE_BUILD_TYPE=Release   # + your backend, e.g. -DGGML_CUDA=ON
cmake --build build -j
```

For the complete, validated NVIDIA CUDA / multi-GPU procedure on Linux
(including the exact CMake flags, `lib` vs `lib64`, `ldconfig`, and a
troubleshooting matrix) see
[embedded-cuda-linux.md](embedded-cuda-linux.md).

**No CUDA pack is published.** `runtime install --backend cuda` fails on
purpose: upstream ships no pinned Linux CUDA artifact, and the Windows CUDA
runtime is hundreds of megabytes with separate redistribution requirements.
(The `--backend` flag still lists `cuda` as an accepted value; there is simply
no CUDA entry in `pin.json` for it to resolve, so it errors rather than
downloading an unpinned or incomplete asset.) Keep yzma, the llama.cpp
revision, and llmtui's pin aligned — the pin is the single source of truth
(`internal/runtime/pin.json`: currently yzma `v1.24.0`, llama.cpp `b10549`,
compatible builds `b10545`–`b10549`).

Linux additionally needs `libffi.so.8` from the distribution's `libffi8`
package. The dependency is initialized lazily: when it is missing, only the
embedded provider fails, and remote/local-server providers remain usable.
The packaged Windows CPU runtime includes its required
`libomp140.x86_64.dll`; keep it in the runtime directory with the other DLLs.

## Configure and run

Add an embedded provider to `~/.config/llmtui/config.yaml`:

```yaml
providers:
  embedded:
    type: embedded
    model_path: "~/models/model.gguf"
    # Optional: enables vision. This projector must match model_path exactly.
    # mmproj_path: "~/models/mmproj-model.gguf"
    # Optional advanced override. Omit for bundled/managed runtime discovery.
    # library_path: "/path/to/trusted/llama.cpp-libs"
    context_size: 8192
    gpu_layers: -1
    threads: 0
    batch_size: 512
    tool_format: auto
    sampling:
      top_k: 40
      min_p: 0.05
      repeat_penalty: 1.1
      repeat_last_n: 64
      presence_penalty: 0.0
      seed: 0
      stop: []
```

Then start chat:

```bash
llmtui chat --provider embedded
```

Most setups need no `library_path` and no `YZMA_LIB` at all: a release
archive bundles a verified runtime next to the binary, and
`llmtui runtime install` writes one to a managed, verified directory that
resolution finds automatically (`llmtui doctor` reports which tier a
provider actually resolved from). Only set `library_path` (or the
equivalent `YZMA_LIB` environment variable) to pin an explicit runtime
llmtui does not manage itself — for example a custom-built llama.cpp, or a
non-default location:

```bash
export YZMA_LIB="/path/to/trusted/llama.cpp-libs"
llmtui chat --provider embedded --model "$HOME/models/model.gguf"
```

`--model` wins over `model_path`. Without an explicit override, `model_path`
wins over provider and global `default_model` values. For a text-only provider,
the model picker lists sibling main-model `.gguf` files and excludes
`mmproj-*.gguf`. A provider with `mmproj_path` is a fixed model/projector pair:
only its configured main model is selectable, because guessing compatibility
from filenames could load an unsafe or nonsensical pair. Create another
embedded provider entry for another vision model.

Vision configuration example (like the main example above, no
`library_path` is needed unless you're pinning an unmanaged runtime):

```yaml
providers:
  embedded_gemma4:
    type: embedded
    model_path: "~/.lmstudio/models/lmstudio-community/gemma-4-E4B-it-GGUF/gemma-4-E4B-it-Q4_K_M.gguf"
    mmproj_path: "~/.lmstudio/models/lmstudio-community/gemma-4-E4B-it-GGUF/mmproj-gemma-4-E4B-it-BF16.gguf"
    context_size: 8192
    gpu_layers: -1
    tool_format: auto
```

No `chat_template` override is needed for that Gemma 4 build; llmtui falls
back to a full Jinja renderer when llama.cpp's restricted renderer does not
support valid GGUF template constructs.

For a model served by LM Studio or another OpenAI-compatible backend, use a
model profile rather than copying machine-specific model paths. Match the
context to the size loaded by the server. For a non-reasoning model, disable
the prompt hint and leave the wire protocol on `chat.reasoning: auto`:

```yaml
chat:
  reasoning: auto

model_profiles:
  local-gemma-12b:
    match: ["vendor/gemma-12b", "gemma-12b"]
    context_window: 65536
    preferred_temperature: 0.2
    supports_json_mode: true
    prompt_style: direct
    reasoning_hint: false
```

## Runtime and sampling options

| Key | Default | Meaning |
| --- | --- | --- |
| `model_path` | empty | Local GGUF file; required unless `--model`/`LLMTUI_MODEL` supplies it |
| `mmproj_path` | empty | Optional compatible multimodal projector GGUF; enables authoritative vision support and fixes the provider to this model/projector pair |
| `library_path` | automatic | Advanced trusted override; otherwise use the resolution tiers above |
| `context_size` | `0` | Bounded model default: `min(n_ctx_train, 8192)`; a positive value is capped at the trained context unless `linear`, `yarn`, or `longrope` scaling is explicitly selected |
| `gpu_layers` | `-1` | `-1` offloads all possible layers, `0` is CPU-only, positive values set an exact layer count |
| `threads` | `0` | llama.cpp automatic CPU thread selection |
| `batch_size` | `512` | Prompt-decode batch size, capped by the context size |
| `chat_template` | model metadata | Inline Jinja chat template override; this is template text, not a filename |
| `swa_full` | `false` | `true` restores llama.cpp's full-size KV cache for sliding-window-attention layers. The default window-sized SWA cache cuts KV memory several-fold on Gemma-style models (Gemma 4 E4B at 131072 tokens: ~2.0 GiB vs ~7.2 GiB) at the cost of occasional full prompt re-decodes when an old prefix cannot be trimmed in place |
| `kv_cache_type` | `f16` | K/V cache element type; `q8_0` roughly halves KV memory with a small quality cost |
| `flash_attention` | `auto` | Flash-attention mode: `auto` (llama.cpp decides per model/backend), `on`, or `off` |
| `tool_format` | `auto` | Native tool grammar: `auto`, `standard`, `qwen`, `glm`, `mistral`, `gemma`, `gpt`, or `phi`; prefer `auto` unless model detection needs an override |
| `rope_scaling_type` | model metadata | Optional override: `none`, `linear`, `yarn`, or `longrope` |
| `rope_freq_base` | model metadata | Positive RoPE base-frequency override |
| `rope_freq_scale` | model metadata | Positive RoPE frequency-scale override |
| `yarn_ext_factor` | llama.cpp/model default | YaRN extrapolation mix factor |
| `yarn_attn_factor` | llama.cpp/model default | YaRN attention magnitude factor |
| `yarn_beta_fast` | llama.cpp/model default | YaRN low correction dimension |
| `yarn_beta_slow` | llama.cpp/model default | YaRN high correction dimension |
| `yarn_orig_ctx` | llama.cpp/model default | Positive original context size used by YaRN |
| `sampling.top_k` | `40` | Top-k sampling; omit the field to use the default, or set `0` to explicitly disable top-k filtering |
| `sampling.min_p` | `0.05` | Min-p sampling; omit the field to use the default, or set `0.0` to explicitly disable min-p filtering |
| `sampling.repeat_penalty` | `1.1` | Repetition penalty |
| `sampling.repeat_last_n` | `64` | Token history used by the repetition penalty |
| `sampling.presence_penalty` | `0.0` | Flat per-token penalty applied once a token has appeared at all (independent of `repeat_penalty`); some model cards (e.g. Qwen3) recommend a nonzero value for non-thinking/instruct mode and `0.0` for thinking mode |
| `sampling.dry_multiplier` | `0.0` | DRY anti-repetition strength; a positive value enables the sampler |
| `sampling.dry_base` | `1.75` | DRY exponential base |
| `sampling.dry_allowed_length` | `2` | Repeated sequence length allowed before a penalty |
| `sampling.dry_penalty_last_n` | `-1` | History window; `-1` uses the active context size |
| `sampling.dry_sequence_breakers` | newline, `:`, `"`, `*` | Boundaries that stop DRY sequence matching; these llama.cpp defaults apply when DRY is enabled and the key is omitted |
| `sampling.seed` | `0` | `0` selects a random seed; another value is deterministic |
| `sampling.stop` | `[]` | Case-sensitive stop strings, safe across token/UTF-8 boundaries |

The shared `chat.temperature`, `chat.top_p`, and `chat.max_tokens` settings
still shape each request. A temperature at or below zero uses greedy sampling.
`max_tokens` is a ceiling: when a valid text or multimodal prompt leaves fewer
positions, llmtui automatically clamps that request to the remaining context
instead of rejecting it. A prompt that already fills the context still returns
an actionable error rather than overflowing the KV cache.

Leave all RoPE/YaRN keys unset when the requested context is within the GGUF's
trained window. Overrides are intended for model-card-directed extrapolation;
incorrect values can sharply reduce quality even when the model loads. For
example, a model that explicitly documents YaRN extension may use:

```yaml
providers:
  long-context:
    type: embedded
    model_path: /models/model.gguf
    context_size: 524288
    rope_scaling_type: yarn
    rope_freq_scale: 0.25
    yarn_orig_ctx: 262144
    sampling:
      dry_multiplier: 0.8
```

Per-run overrides:

```bash
llmtui chat --provider embedded --model /models/model.gguf \
  --context-size 4096 --gpu-layers 0
```

The environment equivalents are `LLMTUI_CONTEXT_SIZE` and
`LLMTUI_GPU_LAYERS`.

## Multiple GPUs and acceleration backends

The embedded provider exposes exactly one GPU control: `gpu_layers`
(`-1` offloads every layer — mapped internally to "all layers" — `0` is
CPU-only, a positive integer is an explicit layer count). It does **not**
expose `tensor_split`, `split_mode`, `main_gpu`, or device selection.

With an accelerated runtime (CUDA/Vulkan/Metal/ROCm) the linked llama.cpp
enumerates the visible devices and applies **its own default** multi-device
split; llmtui does not override it. To constrain which devices are used, set
the backend's standard environment variable before launch, e.g. for NVIDIA:

```bash
CUDA_VISIBLE_DEVICES=0,1 llmtui chat --provider embedded
```

VRAM is per device. Several passed-through GPUs (for example the four
processors of an NVIDIA A16) are **distinct CUDA devices** — their capacities
add up in aggregate, but a single indivisible buffer or layer must still fit
on one device. A model small enough to fit on one GPU may load entirely there.
Verify real placement with `nvidia-smi`; inside the TUI, `/debug` reports
`native backends: N registered, M devices` once a model is loaded.

Metal (macOS Apple Silicon) and the pinned Vulkan pack are the
project-packaged accelerators. NVIDIA CUDA on Linux needs a self-built
runtime — see [embedded-cuda-linux.md](embedded-cuda-linux.md).

## Vision, tools, and reasoning

GPT-OSS is a special native protocol path. The embedded runtime identifies it
from GGUF architecture metadata (with a centralized filename fallback),
renders the GGUF's trusted Jinja chat template with Harmony's
`reasoning_effort`, and decodes Harmony control tokens before emitting typed
reasoning, final-content, or tool-call events. It never uses the generic tool
grammar or leaked-thinking filter for GPT-OSS. A missing or incompatible GGUF
template is an explicit error; there is no generic-template fallback.

The current `yzma/pkg/llama` binding exposes GGUF metadata, tokenization, and
special-token conversion but not llama.cpp `common/chat`'s C++ parser API.
LLMTUI consequently performs the small strict streaming state-machine decode
at the binding boundary. Embedded GPT-OSS supports sequential tool rounds;
the upstream template terminates at the first `<|call|>`, so it does not
advertise parallel tool calls in one assistant response.

- Vision requires both the main GGUF and its matching `mmproj` GGUF. Paste a
  PNG or JPEG with `Ctrl+V`; attachments are passed as encoded bytes directly
  to mtmd in memory. Up to 8 images are accepted per request, with limits of
  20 MiB per image, 64 MiB total, 8192 pixels per dimension, and 40 million
  decoded pixels per image. Declared MIME and detected format must agree.
- Images keep message and attachment order. Prompt usage reports exact mtmd
  chunk tokens; context capacity is budgeted with mtmd positions, which are a
  different native quantity.
- Tools use the same `/tools` approval, execution, result, and continuation
  loop as remote providers. `tool_format: auto` recognizes supported model
  families; a recognized native grammar returns structured calls. Unknown
  formats use llmtui's existing fenced prompt-protocol fallback. Model training
  determines reliability—configuration can enable a protocol, but cannot make
  a model good at tool use. Gemma's `call:name{}` form is accepted for tools
  whose JSON schema permits an empty object (for example pathless `list_dir`);
  missing required arguments remain errors. A Gemma-only prompt hint asks the
  model to answer after tool results and is applied to a cloned request—it is
  not written into the conversation history.
- `/think on|off|auto` and `chat.reasoning` are passed to the GGUF Jinja
  template as `enable_thinking`: `on` sets true and `off` sets false. `auto`
  omits it unless the active model profile sets `reasoning_hint: true`, in
  which case `auto` resolves to `on` for this provider — some chat templates
  (Gemma 4's confirmed among them) treat an omitted `enable_thinking` as off,
  so leaving it unset would make "auto" silently behave like "always off" for
  those models. Remote providers (Ollama, LM Studio, any OpenAI-compatible
  server) still omit it under `auto`, since those hosts apply their own
  template and may have their own independent default — this promotion is
  embedded-provider-only. A model may still choose to answer directly when
  thinking is on.
  When the model emits supported thought delimiters, llmtui routes the content
  to the reasoning stream and keeps it out of the visible answer, history,
  subsequent prompts, and response cache. `/thoughts show|hide` expands or
  collapses that UI-only reasoning without changing the template setting.
- The provider request API accepts a generic GBNF response grammar. Verified
  agent checks use it to require syntactically valid JSON; native tool calls
  and a separate response grammar are rejected together because both control
  the model's output grammar.

## Lifecycle and behavior

- Health checks only stat the model and libraries. The first chat loads the
  model and reports load/prompt progress in the TUI.
- Prompt KV state is reused only when the token prefix is safe; otherwise the
  context is cleared and decoded again.
- `Esc` cancels native decode and keeps the runtime reusable for the next
  prompt.
- Switching models or providers frees the old projector, context, and model.
  Quitting also unloads them. The process-global llama.cpp/mtmd backends stay
  initialized until process exit by design.
- Model/projector size and mtime plus every runtime, sampling, and tool-format
  option are part of the response-cache fingerprint, so replacing a GGUF or
  changing inference settings cannot return an incompatible cached answer.
- Image requests bypass response-cache writes and image embeddings never use
  the text-prefix KV cache. Native memory is cleared before image evaluation
  and again before the next text request, including after cancellation/error.
- All prompts and inference stay in-process. Enabling web tools, MCP, or a
  separately configured remote provider follows their normal, explicit
  network behavior.

## Limitations

- Vision supports encoded PNG and JPEG images only; audio/video projectors are
  not exposed.
- Main-model/projector compatibility is not auto-discovered. Use a pair
  published together by the model distributor.
- Tool and reasoning quality is model/template dependent. `tool_format` and
  `/think` select protocols; they do not add capabilities absent from training.
- One generation runs at a time per embedded provider.
- No `/runtime unload` command. Switch providers or quit to unload.
- Model loading itself cannot be interrupted inside llama.cpp. If cancellation
  arrives during load, llmtui frees the model immediately after the native
  load call returns.
- Native crashes such as a segmentation fault cannot be recovered by Go. Use
  the pinned libraries and trusted GGUF sources.

## Troubleshooting

### Runtime library missing

Run `llmtui runtime install` (checksum-verified, idempotent, safe to rerun);
`llmtui doctor` shows which resolution tier a provider found (or didn't) and
what to do next. Only fall back to an explicit
`providers.embedded.library_path` or `YZMA_LIB` pointing at a directory
containing `libllama` and `libggml*` (plus `libmtmd` for vision) if you need
an unmanaged, hand-built runtime instead of the one llmtui manages.

### `libffi.8.dylib` cannot be opened on macOS

The FFI dependency bundles libffi and normally extracts it to the user cache.
In a locked-down environment where that cache is read-only, install libffi
and point the dynamic loader at it before starting llmtui:

```bash
brew install libffi
FFI_NO_EMBED=1 DYLD_LIBRARY_PATH="$(brew --prefix libffi)/lib" \
  llmtui chat --provider embedded
```

### Model has no chat template

Use an instruct/chat GGUF with `tokenizer.chat_template` metadata, or put the
actual Jinja template text in `providers.<name>.chat_template`. Base models
often have no appropriate chat format.

### Vision projector errors

`mmproj_path` must name a file, not its directory, and must match the exact
main-model family/revision. A missing `libmtmd`, absent projector, incompatible
pair, or projector without vision support fails before the provider accepts an
image. Do not rename an unrelated projector to bypass this validation.

### Tool calls are not produced

Keep `tool_format: auto` for recognized Gemma/Qwen/GLM/Mistral/GPT/Phi model
paths. If the filename is unconventional, set the matching format explicitly.
An unknown format falls back to fenced calls; a known format with no calls is
usually model behavior, so try a clearer instruction or a model trained for
tool use. A “recognizable but malformed” error means the emitted call is still
incomplete or violates the offered schema; a valid Gemma zero-argument call is
accepted when the tool has no required parameters.

### Out of memory or very slow prompt processing

Context-init and model-load failures now quote the decisive lines of
llama.cpp's own log (for example the exact buffer that failed to allocate),
so read the error text first. The memory levers, in order of preference:
keep `swa_full` unset (window-sized SWA caches are the default and are
dramatically smaller for Gemma-style models), set `kv_cache_type: q8_0`
(halves KV memory), lower `context_size`, and close other model hosts
(LM Studio, Ollama) that share the same GPU memory while the embedded model
is loaded. Use `gpu_layers: 0` to diagnose GPU/backend problems. A larger
`batch_size` can speed prompt processing but consumes more memory.

If the TUI reports that request overhead is too large, the runtime context is
not larger than the context manager's response reserve (4096 tokens by
default). Raise `context_size` or set a smaller
`context.reserve_response_tokens` value appropriate for the model and desired
answer length.

### NVIDIA CUDA on Linux

Building the pinned llama.cpp revision with CUDA, pointing llmtui at it,
`lib` vs `lib64`, `ldconfig`, Secure Boot / DKMS, VMware passthrough MMIO, and
a full symptom→cause→fix matrix are covered in
[embedded-cuda-linux.md](embedded-cuda-linux.md).

### Pasting an image over SSH does not work

`Ctrl+V` reads the clipboard of the machine **running** llmtui. Over SSH on a
headless host there is no graphical clipboard, so it fails even with
`wl-paste` / `xclip` installed — the real cause is the absence of `DISPLAY` /
`WAYLAND_DISPLAY`, not the missing tool the error names. There is no
file-path image-attachment command today. For vision on a remote GPU host,
run `llama-server` bound to `127.0.0.1`, forward it over SSH, and run llmtui
locally as an `openai_compatible` client (see
[embedded-cuda-linux.md](embedded-cuda-linux.md#10-headless--ssh-image-limitation)).

### Symbol-resolution or ABI errors

Remove the incompatible runtime and install the pinned build. yzma and
llama.cpp share a narrow compatible window: use only the tag/range in
`internal/runtime/pin.json` (currently yzma `v1.24.0`, llama.cpp `b10549`,
compatible builds `b10545`–`b10549`). Never pair a new binary with an old
hand-built runtime, or vice versa.

## Design and licensing

The accepted architecture and rejected alternatives are recorded in
[architecture/embedded-local-inference.md](architecture/embedded-local-inference.md).
Third-party attribution is in [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md).
