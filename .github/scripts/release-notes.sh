#!/usr/bin/env bash
# release-notes.sh <tag> — generate categorized Markdown release notes.
#
# Commits between the previous tag and <tag> are grouped by their
# conventional-commit prefix (feat/fix/docs/perf/refactor/test/chore/ci/build);
# anything else lands under "Other changes". Requires full git history
# (checkout with fetch-depth: 0).
set -euo pipefail

tag="${1:?usage: release-notes.sh <tag>}"
repo_url="https://github.com/${GITHUB_REPOSITORY:-patrikcze/llmtui}"

prev="$(git describe --tags --abbrev=0 "${tag}^" 2>/dev/null || true)"
if [ -n "$prev" ]; then
  range="${prev}..${tag}"
else
  range="$tag"
fi

# Subject + short hash per commit, oldest first, merges skipped.
commits="$(git log --no-merges --reverse --format='%s|%h' "$range")"

section() { # section <title> <prefix-regex>
  local title="$1" re="$2" body=""
  body="$(echo "$commits" | { grep -iE "^${re}(\([^)]*\))?!?:" || true; })"
  [ -z "$body" ] && return 0
  echo "### $title"
  echo
  echo "$body" | while IFS='|' read -r subject hash; do
    # Drop the "type(scope):" prefix; the section already says it.
    cleaned="$(echo "$subject" | sed -E 's/^[a-zA-Z]+(\([^)]*\))?!?:[[:space:]]*//')"
    echo "- ${cleaned} (${hash})"
  done
  echo
}

echo "## llmtui ${tag}"
echo
if [ -n "$prev" ]; then
  echo "Changes since ${prev}."
else
  echo "First tagged release."
fi
echo

section "🚨 Breaking changes" "[a-zA-Z]+(\([^)]*\))?!"
section "✨ Features"         "feat"
section "🐛 Bug fixes"        "fix"
section "⚡ Performance"      "perf"
section "📝 Documentation"    "docs"
section "🔧 Maintenance"      "(chore|refactor|test|ci|build|style)"

other="$(echo "$commits" | { grep -ivE '^[a-zA-Z]+(\([^)]*\))?!?:' || true; })"
if [ -n "$other" ]; then
  echo "### Other changes"
  echo
  echo "$other" | while IFS='|' read -r subject hash; do
    echo "- ${subject} (${hash})"
  done
  echo
fi

cat <<EOF
### 📦 Installation

Download the binary for your platform below, then:

\`\`\`bash
# macOS / Linux (example: Apple Silicon)
chmod +x llmtui_${tag}_darwin_arm64
sudo mv llmtui_${tag}_darwin_arm64 /usr/local/bin/llmtui
llmtui version
\`\`\`

Or install from source: \`go install github.com/patrikcze/llmtui/cmd/llmtui@${tag}\`

Verify downloads against \`checksums.txt\`:

\`\`\`bash
shasum -a 256 -c checksums.txt --ignore-missing
\`\`\`
EOF

if [ -n "$prev" ]; then
  echo
  echo "**Full changelog:** [${prev}...${tag}](${repo_url}/compare/${prev}...${tag})"
fi
