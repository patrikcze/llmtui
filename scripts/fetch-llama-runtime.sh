#!/usr/bin/env sh

set -eu

echo "warning: scripts/fetch-llama-runtime.sh is deprecated; use 'llmtui runtime install'" >&2

if command -v llmtui >/dev/null 2>&1; then
    if [ "$#" -gt 0 ]; then
        exec llmtui runtime install --dest "$1"
    fi
    exec llmtui runtime install
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
if [ "$#" -gt 0 ]; then
    exec go run "$root/cmd/llmtui" runtime install --dest "$1"
fi
exec go run "$root/cmd/llmtui" runtime install