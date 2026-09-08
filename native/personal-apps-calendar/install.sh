#!/bin/sh
# Build, ad-hoc sign, and install the EventKit companion outside the source
# checkout. This is an explicit setup action, never part of llmtui startup.
set -eu

if [ "$(uname -s)" != "Darwin" ]; then
	echo "error: the Calendar helper can only be installed on macOS" >&2
	exit 1
fi

: "${HOME:?HOME must be set}"

source_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
app_name=llmtui-personal-apps-calendar.app
executable_name=llmtui-personal-apps-calendar
helpers_dir=${CALENDAR_HELPERS_DIR:-"$HOME/Library/Application Support/llmtui/helpers"}
destination=${1:-"$helpers_dir/$app_name"}

case "$destination" in
/*.app) ;;
*)
	echo "usage: $0 [/absolute/path/to/$app_name]" >&2
	exit 2
	;;
esac

if [ -n "${SWIFTC:-}" ]; then
	swiftc=$SWIFTC
else
	if ! swiftc=$(xcrun --find swiftc 2>/dev/null); then
		echo "error: Swift compiler not found; install Xcode or Command Line Tools" >&2
		exit 1
	fi
fi

install_dir=$(dirname -- "$destination")
mkdir -p "$install_dir"
stage=$(mktemp -d "$install_dir/.llmtui-calendar-build.XXXXXX")
backup="$install_dir/.llmtui-calendar-backup.$$"
cleanup() {
	rm -rf "$stage"
	if [ -e "$backup" ] || [ -L "$backup" ]; then
		rm -rf "$backup"
	fi
}
trap cleanup EXIT HUP INT TERM

bundle="$stage/$app_name"
mkdir -p "$bundle/Contents/MacOS" "$stage/module-cache"

if ! "$swiftc" -parse-as-library -module-cache-path "$stage/module-cache" \
	-o "$bundle/Contents/MacOS/$executable_name" "$source_dir/main.swift"; then
	developer_dir=$(xcode-select -p 2>/dev/null || printf '%s' 'unknown')
	echo "error: Swift could not compile the Calendar helper (active developer directory: $developer_dir)." >&2
	echo "       Install matching Xcode/Command Line Tools, then select it, for example:" >&2
	echo "       sudo xcode-select --switch /Applications/Xcode.app/Contents/Developer" >&2
	exit 1
fi
cp "$source_dir/Info.plist" "$bundle/Contents/Info.plist"
codesign --force --sign "${CODESIGN_IDENTITY:--}" --entitlements "$source_dir/Calendar.entitlements" "$bundle"
codesign --verify --deep --strict "$bundle"
if ! codesign -d --entitlements - "$bundle" 2>&1 | grep -q 'com.apple.security.personal-information.calendars'; then
	echo "error: Calendar entitlement was not applied to the helper" >&2
	exit 1
fi

if [ -e "$destination" ] || [ -L "$destination" ]; then
	mv "$destination" "$backup"
fi
if ! mv "$bundle" "$destination"; then
	if [ -e "$backup" ] || [ -L "$backup" ]; then
		mv "$backup" "$destination"
	fi
	echo "error: could not install Calendar helper to $destination" >&2
	exit 1
fi
if [ -e "$backup" ] || [ -L "$backup" ]; then
	rm -rf "$backup"
fi

echo "installed Calendar helper: $destination"
echo "configure calendar.helper_path as: $destination/Contents/MacOS/$executable_name"
