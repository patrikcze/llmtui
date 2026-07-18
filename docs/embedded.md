# Embedded GGUF inference

The `embedded` provider runs a GGUF model inside the llmtui process through
llama.cpp. It needs no Ollama, LM Studio, HTTP server, or API key. It is
opt-in: existing providers and the default `llmtui chat` flow are unchanged.

The implementation uses `hybridgroup/yzma` with purego and libffi, so llmtui
still builds with `CGO_ENABLED=0`. Native llama.cpp libraries remain a separate
runtime dependency and are never downloaded automatically.

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
pinned SHA-256 checksum, and installs its dynamic libraries under
`~/.local/share/llmtui/llama.cpp`. It does not download a model.

To choose another destination:

```bash
scripts/fetch-llama-runtime.sh /path/to/llama.cpp-libs
```

On other supported architectures, build the matching llama.cpp revision as
shared libraries and point llmtui at the directory containing `libllama` and
`libggml*`:

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
    library_path: "~/.local/share/llmtui/llama.cpp"
    context_size: 8192
    gpu_layers: -1
    threads: 0
    batch_size: 512
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

`--model` wins over `model_path`. Without an explicit override,
`model_path` wins over provider and global `default_model` values. The model
picker lists sibling `.gguf` files and loads a selected model lazily; a bad
selection is validated before the working model is unloaded.

## Runtime and sampling options

| Key | Default | Meaning |
| --- | --- | --- |
| `model_path` | empty | Local GGUF file; required unless `--model`/`LLMTUI_MODEL` supplies it |
| `library_path` | `YZMA_LIB` | Directory containing the llama.cpp shared libraries |
| `context_size` | `0` | Bounded model default: `min(n_ctx_train, 8192)`; a positive value is capped at the trained context |
| `gpu_layers` | `-1` | `-1` offloads all possible layers, `0` is CPU-only, positive values set an exact layer count |
| `threads` | `0` | llama.cpp automatic CPU thread selection |
| `batch_size` | `512` | Prompt-decode batch size, capped by the context size |
| `chat_template` | model metadata | Inline Jinja chat template override; this is template text, not a filename |
| `sampling.top_k` | `40` | Top-k sampling; `0` disables it |
| `sampling.min_p` | `0.05` | Min-p sampling; `0` disables it |
| `sampling.repeat_penalty` | `1.1` | Repetition penalty |
| `sampling.repeat_last_n` | `64` | Token history used by the repetition penalty |
| `sampling.seed` | `0` | `0` selects a random seed; another value is deterministic |
| `sampling.stop` | `[]` | Case-sensitive stop strings, safe across token/UTF-8 boundaries |

The shared `chat.temperature`, `chat.top_p`, and `chat.max_tokens` settings
still shape each request. A temperature at or below zero uses greedy sampling.
If prompt tokens plus `max_tokens` exceed the loaded context, llmtui returns an
actionable error rather than overflowing the KV cache.

Per-run overrides:

```bash
llmtui chat --provider embedded --model /models/model.gguf \
  --context-size 4096 --gpu-layers 0
```

The environment equivalents are `LLMTUI_CONTEXT_SIZE` and
`LLMTUI_GPU_LAYERS`.

## Lifecycle and behavior

- Health checks only stat the model and libraries. The first chat loads the
  model and reports load/prompt progress in the TUI.
- Prompt KV state is reused only when the token prefix is safe; otherwise the
  context is cleared and decoded again.
- `Esc` cancels native decode and keeps the runtime reusable for the next
  prompt.
- Switching models or providers frees the old context and model. Quitting
  also unloads them. The process-global llama.cpp backend stays initialized
  until process exit by design.
- Model file size/mtime and every runtime/sampling option are part of the
  response-cache fingerprint, so replacing a GGUF or changing inference
  settings cannot return an incompatible cached answer.
- All prompts and inference stay in-process. Enabling web tools, MCP, or a
  separately configured remote provider follows their normal, explicit
  network behavior.

## Limitations

- Text-only in the first release; image attachments are not supported.
- No native function-calling protocol. When tools are enabled, llmtui
  automatically uses its prompt-based fenced tool protocol.
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
contains `libllama` and `libggml*`. On Apple Silicon, rerun
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

### Out of memory or very slow prompt processing

Lower `context_size` first. Use `gpu_layers: 0` to diagnose GPU/backend
problems, or raise GPU offload on a machine with sufficient unified/VRAM
memory. A larger `batch_size` can speed prompt processing but consumes more
memory.

### Symbol-resolution or ABI errors

Remove the incompatible runtime and fetch the pinned build. Do not mix yzma
v1.19.0 with old llama.cpp libraries; this release requires `b9979` or newer
and is acceptance-tested with `b10066`.

## Design and licensing

The accepted architecture and rejected alternatives are recorded in
[architecture/embedded-local-inference.md](architecture/embedded-local-inference.md).
Third-party attribution is in [`THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md).
