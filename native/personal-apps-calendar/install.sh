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
	if ! swiftc=$(command -v swiftc 2>/dev/null); then
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

swift_target=""
sdk_path=$(xcrun --show-sdk-path 2>/dev/null || true)
swift_module_dir="$sdk_path/usr/lib/swift/Swift.swiftmodule"
if [ "$(uname -m)" = "arm64" ] && \
	[ ! -f "$swift_module_dir/arm64-apple-macos.swiftinterface" ] && \
	[ -f "$swift_module_dir/arm64e-apple-macos.swiftinterface" ]; then
	# Some macOS 26 Command Line Tools SDKs ship only an arm64e Swift
	# interface. Target that available interface rather than letting swiftc
	# fail while it looks for a missing arm64 one.
	macos_major=$(sw_vers -productVersion | cut -d. -f1)
	swift_target="arm64e-apple-macosx$macos_major.0"
	echo "using Swift target $swift_target"
fi

if [ -n "$swift_target" ]; then
	build_calendar_helper() {
		"$swiftc" -parse-as-library -target "$swift_target" -module-cache-path "$stage/module-cache" \
			-o "$bundle/Contents/MacOS/$executable_name" "$source_dir/main.swift"
	}
else
	build_calendar_helper() {
		"$swiftc" -parse-as-library -module-cache-path "$stage/module-cache" \
			-o "$bundle/Contents/MacOS/$executable_name" "$source_dir/main.swift"
	}
fi

if ! build_calendar_helper; then
	developer_dir=$(xcode-select -p 2>/dev/null || printf '%s' 'unknown')
	echo "error: Swift could not compile the Calendar helper (active developer directory: $developer_dir)." >&2
	echo "       /Library/Developer/CommandLineTools is the correct directory for Command Line Tools." >&2
	echo "       Update or reinstall Command Line Tools so its Swift compiler and macOS SDK are from" >&2
	echo "       the same release, then rerun this command. No xcode-select switch is needed." >&2
	exit 1
fi
cp "$source_dir/Info.plist" "$bundle/Contents/Info.plist"
for key in CFBundleIdentifier CFBundleExecutable CFBundlePackageType CFBundleShortVersionString CFBundleVersion NSPrincipalClass; do
	if ! /usr/libexec/PlistBuddy -c "Print :$key" "$bundle/Contents/Info.plist" >/dev/null 2>&1; then
		echo "error: Calendar helper Info.plist is missing required $key" >&2
		exit 1
	fi
done
codesign --force --sign "${CODESIGN_IDENTITY:--}" --options runtime --entitlements "$source_dir/Calendar.entitlements" "$bundle"
codesign --verify --deep --strict "$bundle"
for entitlement in com.apple.security.app-sandbox com.apple.security.personal-information.calendars; do
	if ! codesign -d --entitlements - "$bundle" 2>&1 | grep -q "$entitlement"; then
		echo "error: $entitlement entitlement was not applied to the helper" >&2
		exit 1
	fi
done

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
