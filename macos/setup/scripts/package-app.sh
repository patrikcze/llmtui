#!/usr/bin/env bash
# Build the LLMTUIGUI setup app (Release, ad-hoc signed), embed an llmtui
# binary at Contents/Helpers/llmtui, re-sign, verify, and zip it.
#
# Usage: package-app.sh <llmtui-binary> <version> <output-dir>
#   version: the llmtui version, e.g. v1.0.43. A leading "v" is stripped; the
#            bundle's CFBundleShortVersionString gets its numeric core (so a
#            `git describe` value like 1.0.42-3-gabc still builds), and the zip
#            name keeps the full string.
#
# Ad-hoc signing ("-") needs no Apple Developer account. A downloaded copy is
# quarantined by Gatekeeper; users open it with right-click → Open or
# `xattr -dr com.apple.quarantine LLMTUIGUI.app`.
#
# Requires Xcode. DEVELOPER_DIR defaults to /Applications/Xcode.app so this
# works even when xcode-select points at the Command Line Tools.
set -euo pipefail

if [[ $# -ne 3 ]]; then
  echo "usage: $0 <llmtui-binary> <version> <output-dir>" >&2
  exit 2
fi

LLMTUI_BIN=$1
VERSION=${2#v}
MARKETING_VERSION=$(printf '%s' "$VERSION" | grep -oE '^[0-9]+(\.[0-9]+){0,2}' || true)
MARKETING_VERSION=${MARKETING_VERSION:-0.0.0}
OUT_DIR=$3
BUILD_NUMBER=${BUILD_NUMBER:-1}
export DEVELOPER_DIR=${DEVELOPER_DIR:-/Applications/Xcode.app/Contents/Developer}

HERE=$(cd "$(dirname "$0")/.." && pwd)
DERIVED=${DERIVED_DATA:-$HERE/build/DerivedData}
APP_NAME=LLMTUIGUI
ARCH=$(uname -m)

[[ -x $LLMTUI_BIN ]] || { echo "llmtui binary not found or not executable: $LLMTUI_BIN" >&2; exit 1; }
mkdir -p "$OUT_DIR"

echo "==> building $APP_NAME $MARKETING_VERSION ($BUILD_NUMBER)"
# Release product: no coverage instrumentation (the scheme enables it for
# tests) and no testability.
# Swift packages come only from the committed Package.resolved pins.
xcodebuild \
  -quiet \
  -onlyUsePackageVersionsFromResolvedFile \
  -project "$HERE/$APP_NAME.xcodeproj" \
  -scheme "$APP_NAME" \
  -configuration Release \
  -derivedDataPath "$DERIVED" \
  MARKETING_VERSION="$MARKETING_VERSION" \
  CURRENT_PROJECT_VERSION="$BUILD_NUMBER" \
  ENABLE_TESTABILITY=NO \
  CLANG_COVERAGE_MAPPING=NO \
  CLANG_ENABLE_CODE_COVERAGE=NO \
  CODE_SIGN_IDENTITY=- \
  CODE_SIGN_STYLE=Manual \
  DEVELOPMENT_TEAM= \
  build

APP="$DERIVED/Build/Products/Release/$APP_NAME.app"
[[ -d $APP ]] || { echo "build produced no app at $APP" >&2; exit 1; }

echo "==> embedding llmtui"
HELPERS="$APP/Contents/Helpers"
mkdir -p "$HELPERS"
cp "$LLMTUI_BIN" "$HELPERS/llmtui"
chmod 0755 "$HELPERS/llmtui"

echo "==> signing (ad-hoc, hardened runtime)"
# Keep the entitlements Xcode generated from the target's capabilities
# (Apple Events for Mail, Calendars, network client) but drop the debug-only
# get-task-allow, then sign inner code first and the app last.
ENTITLEMENTS="$DERIVED/release.entitlements"
codesign -d --entitlements - --xml "$APP" > "$ENTITLEMENTS"
/usr/libexec/PlistBuddy -c "Delete :com.apple.security.get-task-allow" "$ENTITLEMENTS" 2>/dev/null || true
codesign --force --sign - --options runtime --timestamp=none "$HELPERS/llmtui"
codesign --force --sign - --options runtime --timestamp=none --entitlements "$ENTITLEMENTS" "$APP"

echo "==> verifying"
codesign --verify --deep --strict --verbose=2 "$APP"
"$HELPERS/llmtui" version

ZIP="$OUT_DIR/$APP_NAME-$VERSION-macos-$ARCH.zip"
rm -f "$ZIP" "$ZIP.sha256"
ditto -c -k --keepParent "$APP" "$ZIP"
(cd "$OUT_DIR" && shasum -a 256 "$(basename "$ZIP")" > "$(basename "$ZIP").sha256")
echo "==> $ZIP"
