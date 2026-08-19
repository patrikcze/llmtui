# Third-Party Notices

llmtui's embedded local-inference provider builds on the following projects.
These notices apply in addition to the licenses of llmtui's Go module
dependencies (see `go.mod`), each of which retains its own license.

## yzma (github.com/hybridgroup/yzma)

Go bindings used to load and drive llama.cpp at runtime.

License: Apache License 2.0. Copyright The Hybrid Group.
yzma incorporates definitions originating from `dianlight/gollama.cpp`
(MIT License, Copyright (c) 2025 Lucio Tarantino).

Full text: <https://github.com/hybridgroup/yzma/blob/main/LICENSE>

## jinja (github.com/ardanlabs/jinja)

Full Jinja chat-template rendering used when a valid GGUF template is outside
llama.cpp's restricted native renderer.

License: Apache License 2.0. Copyright Ardan Labs.

Full text: <https://github.com/ardanlabs/jinja/blob/main/LICENSE>

## llama.cpp / ggml (github.com/ggml-org/llama.cpp)

The native inference runtime. Self-contained llmtui release archives
redistribute a checksum-pinned, trimmed copy of the official llama.cpp binary
release for that platform under `lib/llmtui/runtime/`. The upstream `LICENSE`
file is included verbatim in every runtime directory. Bare-binary users may
obtain the same verified files with `llmtui runtime install`.

MIT License. Copyright (c) 2023-2026 The ggml authors.

Full text: <https://github.com/ggml-org/llama.cpp/blob/master/LICENSE>

## purego (github.com/ebitengine/purego) and ffi (github.com/jupiterrider/ffi)

Foreign-function-interface layers used by yzma.

purego: Apache License 2.0. Copyright the Ebitengine authors.
ffi: MIT License. Copyright (c) 2024 JupiterRider.

llmtui carries the ffi v0.6.0 source under `third_party/ffi` with one local
behavioral change: libffi symbol initialization is lazy and reports an error
at embedded-provider use instead of panicking during process startup. The
upstream copyright and MIT license files are retained unmodified in that
directory. The ffi module embeds libffi binaries on macOS and Windows.
libffi: MIT/Expat License. Copyright (c) 1996-2025 Anthony Green,
Red Hat, Inc. and others.

Full texts:
<https://github.com/ebitengine/purego/blob/main/LICENSE>
<https://github.com/jupiterrider/ffi/blob/main/LICENSE>
<https://github.com/jupiterrider/ffi/blob/main/assets/libffi/LICENSE>

## LLVM OpenMP runtime (Windows runtime archive only)

The official llama.cpp Windows CPU archive includes
`libomp140.x86_64.dll`, the LLVM/Intel OpenMP runtime required by its CPU
backend. llmtui preserves that upstream binary byte-for-byte and records its
SHA-256 in `internal/runtime/pin.json`.

License: Apache License 2.0 with LLVM exceptions, including the legacy Intel
OpenMP notices reproduced by the canonical license file.

Full text:
<https://github.com/llvm/llvm-project/blob/main/openmp/LICENSE.TXT>
