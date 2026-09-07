# llmtui EventKit calendar companion

This is the macOS-only EventKit companion used by the optional
`personal_apps.calendar` integration. It reads one length-framed JSON request
from standard input and writes exactly one framed JSON response to standard
output. It has no listener, no network access, and no model integration.

It supports bounded calendar reads plus the explicitly approved,
non-recurring `calendar_create_event` and `calendar_update_event` operations.
It never sends invitations, changes attendees, edits recurrence, or starts a
background agent.

Build it explicitly; llmtui never compiles or downloads it at runtime:

```sh
mkdir -p llmtui-personal-apps-calendar.app/Contents/MacOS
swiftc -parse-as-library -o llmtui-personal-apps-calendar.app/Contents/MacOS/llmtui-personal-apps-calendar main.swift
cp Info.plist llmtui-personal-apps-calendar.app/Contents/Info.plist
```

If `swiftc` reports that the active SDK is unsupported by the compiler,
Command Line Tools and the selected developer directory are from different
Xcode releases. Install matching tools or select a matching full Xcode
developer directory before building; llmtui cannot repair a system toolchain.

The caller must configure the resulting **absolute** executable path in
`personal_apps.calendar.helper_path` to the executable inside that bundle.
Run `llmtui doctor` or `/doctor personal-apps` first: both checks are passive
and report a missing, relative, or non-executable path without launching the
companion or requesting Calendar access.

This source-build helper is unsigned. A release app bundle needs a separately
reviewed signing/notarization and archive-inclusion process; llmtui does not
discover or trust helpers from `PATH`, download one, or compile Swift during
ordinary startup. The companion requests macOS full Calendar access only when
an explicitly connected Calendar operation first needs data.
