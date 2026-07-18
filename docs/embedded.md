# Embedded GGUF inference

The `embedded` provider runs a GGUF model inside the llmtui process through
llama.cpp. It needs no Ollama, LM Studio, HTTP server, or API key. It is
opt-in: existing providers and the default `llmtui chat` flow are unchanged.

The implementation uses `hybridgroup/yzma` with purego and libffi. Native
llama.cpp libraries remain a separate runtime dependency and are never
downloaded automatically. Published macOS binaries enable Go's cgo runtime so
Metal can safely use native threads; they still load llama.cpp dynamically and
do not require users to compile llmtui or link a model runtime.

## Platform status

| Platform | Status | Acceleration |
| --- | --- | --- |
| macOS arm64 (Apple Silicon) | Tested and supported | Metal or CPU |
| Linux amd64/arm64 | Compiles; source-built runtime required; not yet acceptance-tested by this project | CUDA, Vulkan, or CPU as built |
| Windows amd64 | Compiles; source-built runtime required; not yet acceptance-tested by this project | CUDA, Vulkan, or CPU as built |
| macOS amd64 | Not supported for embedded inference | Use a server provider instead |

The normal Ollama and OpenAI-compatible providers remain portable regardless
of embedded-runtime support on the host.

## Install the native runtime

On Apple Silicon, fetch the exact llama.cpp release tested with this llmtui
version:

```bash
scripts/fetch-llama-runtime.sh
```

The script downloads the official `b10066` macOS arm64 archive, verifies its
pinned SHA-256 checksum, and installs its llama, ggml, and mtmd dynamic
libraries under
`~/.local/share/llmtui/llama.cpp`. It does not download a model.

To choose another destination:

```bash
scripts/fetch-llama-runtime.sh /path/to/llama.cpp-libs
```

On other supported architectures, build the matching llama.cpp revision as
shared libraries and point llmtui at the directory containing `libllama`,
`libmtmd`, and `libggml*`:

```bash
git clone https://github.com/ggml-org/llama.cpp.git
cd llama.cpp
git checkout b10066
cmake -B build -DBUILD_SHARED_LIBS=ON
cmake --build build --config Release -j
```

Backend flags such as CUDA or Vulkan are llama.cpp build choices. Keep yzma,
the llama.cpp revision, and llmtui's pin aligned; an ABI-incompatible library
normally fails during symbol resolution and must not be used.

## Configure and run

Add an embedded provider to `~/.config/llmtui/config.yaml`:

```yaml
providers:
  embedded:
    type: embedded
    model_path: "~/models/model.gguf"
    # Optional: enables vision. This projector must match model_path exactly.
    # mmproj_path: "~/models/mmproj-model.gguf"
    library_path: "~/.local/share/llmtui/llama.cpp"
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
      seed: 0
      stop: []
```

Then start chat:

```bash
llmtui chat --provider embedded
```

You can keep the runtime directory out of the config:

```bash
export YZMA_LIB="$HOME/.local/share/llmtui/llama.cpp"
llmtui chat --provider embedded --model "$HOME/models/model.gguf"
```

`--model` wins over `model_path`. Without an explicit override, `model_path`
wins over provider and global `default_model` values. For a text-only provider,
the model picker lists sibling main-model `.gguf` files and excludes
`mmproj-*.gguf`. A provider with `mmproj_path` is a fixed model/projector pair:
only its configured main model is selectable, because guessing compatibility
from filenames could load an unsafe or nonsensical pair. Create another
embedded provider entry for another vision model.

Vision configuration example:

```yaml
providers:
  embedded_gemma4:
    type: embedded
    model_path: "~/.lmstudio/models/lmstudio-community/gemma-4-E4B-it-GGUF/gemma-4-E4B-it-Q4_K_M.gguf"
    mmproj_path: "~/.lmstudio/models/lmstudio-community/gemma-4-E4B-it-GGUF/mmproj-gemma-4-E4B-it-BF16.gguf"
    library_path: "~/.local/share/llmtui/llama.cpp"
    context_size: 8192
    gpu_layers: -1
    tool_format: auto
```

No `chat_template` override is needed for that Gemma 4 build; llmtui falls
back to a full Jinja renderer when llama.cpp's restricted renderer does not
support valid GGUF template constructs.

## Runtime and sampling options

| Key | Default | Meaning |
| --- | --- | --- |
| `model_path` | empty | Local GGUF file; required unless `--model`/`LLMTUI_MODEL` supplies it |
| `mmproj_path` | empty | Optional compatible multimodal projector GGUF; enables authoritative vision support and fixes the provider to this model/projector pair |
| `library_path` | `YZMA_LIB` | Directory containing the llama.cpp shared libraries |
| `context_size` | `0` | Bounded model default: `min(n_ctx_train, 8192)`; a positive value is capped at the trained context |
| `gpu_layers` | `-1` | `-1` offloads all possible layers, `0` is CPU-only, positive values set an exact layer count |
| `threads` | `0` | llama.cpp automatic CPU thread selection |
| `batch_size` | `512` | Prompt-decode batch size, capped by the context size |
| `chat_template` | model metadata | Inline Jinja chat template override; this is template text, not a filename |
| `tool_format` | `auto` | Native tool grammar: `auto`, `standard`, `qwen`, `glm`, `mistral`, `gemma`, `gpt`, or `phi`; prefer `auto` unless model detection needs an override |
| `sampling.top_k` | `40` | Top-k sampling; `0` disables it |
| `sampling.min_p` | `0.05` | Min-p sampling; `0` disables it |
| `sampling.repeat_penalty` | `1.1` | Repetition penalty |
| `sampling.repeat_last_n` | `64` | Token history used by the repetition penalty |
| `sampling.seed` | `0` | `0` selects a random seed; another value is deterministic |
| `sampling.stop` | `[]` | Case-sensitive stop strings, safe across token/UTF-8 boundaries |

The shared `chat.temperature`, `chat.top_p`, and `chat.max_tokens` settings
still shape each request. A temperature at or below zero uses greedy sampling.
`max_tokens` is a ceiling: when a valid text or multimodal prompt leaves fewer
positions, llmtui automatically clamps that request to the remaining context
instead of rejecting it. A prompt that already fills the context still returns
an actionable error rather than overflowing the KV cache.

Per-run overrides:

```bash
llmtui chat --provider embedded --model /models/model.gguf \
  --context-size 4096 --gpu-layers 0
```

The environment equivalents are `LLMTUI_CONTEXT_SIZE` and
`LLMTUI_GPU_LAYERS`.

## Vision, tools, and reasoning

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
  template as `enable_thinking`: `auto` omits it, `on` sets true, and `off`
  sets false. A model may still choose to answer directly when thinking is on.
  When the model emits supported thought delimiters, llmtui routes the content
  to the reasoning stream and keeps it out of the visible answer, history,
  subsequent prompts, and response cache.

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

Set `providers.embedded.library_path` or `YZMA_LIB` to the directory that
contains `libllama` and `libggml*`; vision additionally requires `libmtmd` in
that same directory. On Apple Silicon, rerun
`scripts/fetch-llama-runtime.sh`; it is checksum-safe and idempotent.

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

Lower `context_size` first. Use `gpu_layers: 0` to diagnose GPU/backend
problems, or raise GPU offload on a machine with sufficient unified/VRAM
memory. A larger `batch_size` can speed prompt processing but consumes more
memory.

If the TUI reports that request overhead is too large, the runtime context is
not larger than the context manager's response reserve (2048 tokens by
default). Raise `context_size` or set a smaller
`context.reserve_response_tokens` value appropriate for the model and desired
answer length.

### Symbol-resolution or ABI errors

Remove the incompatible runtime and fetch the pinned build. Do not mix yzma
v1.19.0 with old llama.cpp libraries; this release requires `b9979` or newer
and is acceptance-tested with `b10066`.

## Design and licensing

The accepted architecture and rejected alternatives are recorded in
[architecture/embedded-local-inference.md](architecture/embedded-local-inference.md).
Third-party attribution is in [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md).
