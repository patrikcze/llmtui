#!/usr/bin/env bash
# Fail if a signing team or per-user Xcode state would reach git. Xcode can
# write DEVELOPMENT_TEAM into project.pbxproj when the project is opened or
# signing is touched; the team belongs only in the git-ignored
# Config/Signing.local.xcconfig. Run by CI and `make macos-setup-guard`;
# usable as a pre-commit hook. Checks the working tree and the index.
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
PBXPROJ=macos/setup/LLMTUIGUI.xcodeproj/project.pbxproj
pattern='DEVELOPMENT_TEAM *= *[A-Z0-9]+'
fail=0

if grep -nE "$pattern" "$ROOT/$PBXPROJ"; then
  echo "error: $PBXPROJ sets a DEVELOPMENT_TEAM. Remove it; keep the team in macos/setup/Config/Signing.local.xcconfig (git-ignored)." >&2
  fail=1
elif git -C "$ROOT" show ":$PBXPROJ" 2>/dev/null | grep -nE "$pattern"; then
  echo "error: the staged $PBXPROJ sets a DEVELOPMENT_TEAM." >&2
  fail=1
fi

tracked=$(git -C "$ROOT" ls-files macos | grep -E '(^|/)(xcuserdata/|[^/]*\.xcuserstate$|Signing\.local\.xcconfig$|DerivedData/|build/)' || true)
if [[ -n $tracked ]]; then
  echo "error: per-user or build files are tracked:" >&2
  echo "$tracked" >&2
  fail=1
fi

exit "$fail"
