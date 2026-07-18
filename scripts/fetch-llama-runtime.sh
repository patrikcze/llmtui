#!/usr/bin/env sh
# fetch-llama-runtime.sh — download the pinned llama.cpp runtime libraries
# for llmtui's embedded provider.
#
# This script runs ONLY when you invoke it. llmtui itself never downloads
# anything. The artifact is the official llama.cpp GitHub release archive,
# pinned to an exact tag and verified against a hardcoded SHA256 before
# extraction.
#
# Usage:
#   scripts/fetch-llama-runtime.sh [DEST_DIR]
#
# Default DEST_DIR: ~/.local/share/llmtui/llama.cpp
# Afterwards, point the embedded provider at it:
#   providers.embedded.library_path: <DEST_DIR>   (config)
#   or: export YZMA_LIB=<DEST_DIR>
#
# Pinned upstream revision (update together with the yzma dependency —
# see docs/architecture/embedded-local-inference.md "Upstream pinning"):
LLAMA_TAG="b10066"
LLAMA_COMMIT="86a9c79f866799eb0e7e89c03578ccfbcc5d808e"

set -eu

os="$(uname -s)"
arch="$(uname -m)"
case "$os/$arch" in
Darwin/arm64)
    asset="llama-${LLAMA_TAG}-bin-macos-arm64.tar.gz"
    sha256="52db287b6f39dfab93cc0dd1953c13d2108ca12c2817431c79a169ed3da57597"
    ;;
*)
    echo "error: no pinned, tested llama.cpp runtime for $os/$arch." >&2
    echo "The first llmtui release supports macOS on Apple Silicon." >&2
    echo "On other platforms, build llama.cpp ${LLAMA_TAG} from source with" >&2
    echo "  cmake -B build -DBUILD_SHARED_LIBS=ON && cmake --build build -j" >&2
    echo "and point providers.embedded.library_path (or YZMA_LIB) at the" >&2
    echo "directory containing libllama and libggml* libraries." >&2
    exit 1
    ;;
esac

dest="${1:-$HOME/.local/share/llmtui/llama.cpp}"
url="https://github.com/ggml-org/llama.cpp/releases/download/${LLAMA_TAG}/${asset}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

echo "fetching llama.cpp ${LLAMA_TAG} (${LLAMA_COMMIT})"
echo "  ${url}"
curl -fSL --proto '=https' -o "$tmp/$asset" "$url"

echo "verifying SHA256"
actual="$(shasum -a 256 "$tmp/$asset" | awk '{print $1}')"
if [ "$actual" != "$sha256" ]; then
    echo "error: checksum mismatch for $asset" >&2
    echo "  expected: $sha256" >&2
    echo "  actual:   $actual" >&2
    echo "Refusing to install. The upstream artifact may have changed;" >&2
    echo "do not bypass this check — update the pin deliberately instead." >&2
    exit 1
fi

mkdir -p "$dest"
tar xzf "$tmp/$asset" -C "$tmp"
# The archive unpacks into llama-<tag>/ with dylibs at the top level.
cp "$tmp/llama-${LLAMA_TAG}"/*.dylib "$dest"/
printf '%s %s\n' "$LLAMA_TAG" "$LLAMA_COMMIT" >"$dest/LLAMA_VERSION"

echo "installed llama.cpp ${LLAMA_TAG} runtime libraries to $dest"
echo
echo "Next steps:"
echo "  1. Add to your llmtui config:"
echo "       providers:"
echo "         embedded:"
echo "           type: embedded"
echo "           library_path: $dest"
echo "  2. Chat with a local GGUF model:"
echo "       llmtui chat --provider embedded --model /path/to/model.gguf"
