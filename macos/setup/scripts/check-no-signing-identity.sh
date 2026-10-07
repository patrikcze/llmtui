#!/usr/bin/env bash
# Fail if a signing team or per-user Xcode state would reach git. Xcode can
# write DEVELOPMENT_TEAM into project.pbxproj when the project is opened, a
# target is added, or signing is touched. The team belongs only in the
# git-ignored Config/Signing.local.xcconfig, so any DEVELOPMENT_TEAM line in
# project.pbxproj is rejected: a real ID leaks it, and an empty value
# (DEVELOPMENT_TEAM = "";) overrides the local xcconfig, which makes Xcode
# ask for a team again and write it into a target. Run by CI and `make macos-setup-guard`;
# usable as a pre-commit hook. Checks the working tree and the index.
set -euo pipefail

ROOT=$(git rev-parse --show-toplevel)
PBXPROJ=macos/setup/LLMTUIGUI.xcodeproj/project.pbxproj
pattern='DEVELOPMENT_TEAM *='
fail=0

if grep -nE "$pattern" "$ROOT/$PBXPROJ"; then
  echo "error: $PBXPROJ sets DEVELOPMENT_TEAM. Delete every DEVELOPMENT_TEAM line from it (including empty ones); the team comes only from macos/setup/Config/Signing.local.xcconfig (git-ignored)." >&2
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
