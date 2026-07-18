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

## llama.cpp / ggml (github.com/ggml-org/llama.cpp)

The native inference runtime. llmtui does not vendor or compile llama.cpp;
users obtain its libraries separately (see `scripts/fetch-llama-runtime.sh`
and `docs/embedded.md`). This notice is included because llmtui is designed
to load and is distributed alongside instructions for obtaining these
libraries.

MIT License. Copyright (c) 2023-2026 The ggml authors.

Full text: <https://github.com/ggml-org/llama.cpp/blob/master/LICENSE>

## purego (github.com/ebitengine/purego) and ffi (github.com/jupiterrider/ffi)

Foreign-function-interface layers used by yzma.

purego: Apache License 2.0. Copyright the Ebitengine authors.
ffi: MIT License. Copyright (c) 2024 JupiterRider.

The ffi module embeds libffi binaries on macOS and Windows.
libffi: MIT/Expat License. Copyright (c) 1996-2025 Anthony Green,
Red Hat, Inc. and others.

Full texts:
<https://github.com/ebitengine/purego/blob/main/LICENSE>
<https://github.com/jupiterrider/ffi/blob/main/LICENSE>
<https://github.com/jupiterrider/ffi/blob/main/assets/libffi/LICENSE>
