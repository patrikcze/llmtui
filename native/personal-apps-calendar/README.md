# llmtui EventKit calendar companion

This is the macOS-only, read-only EventKit companion used by the optional
`personal_apps.calendar` integration. It reads one length-framed JSON request
from standard input and writes exactly one framed JSON response to standard
output. It has no listener, no network access, no model integration, and no
write operation.

Build it explicitly; llmtui never compiles or downloads it at runtime:

```sh
mkdir -p llmtui-personal-apps-calendar.app/Contents/MacOS
swiftc -parse-as-library -o llmtui-personal-apps-calendar.app/Contents/MacOS/llmtui-personal-apps-calendar main.swift
cp Info.plist llmtui-personal-apps-calendar.app/Contents/Info.plist
```

The caller must configure the resulting **absolute** executable path in
`personal_apps.calendar.helper_path` to the executable inside that bundle. A signed app-bundle distribution and its
permission attribution are Slice 7 release work, not implied by this
source-build helper. The companion requests macOS full calendar access only
when an explicitly connected calendar operation first needs data.
