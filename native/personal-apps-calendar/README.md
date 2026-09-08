# llmtui EventKit calendar companion

This is the macOS-only EventKit companion used by the optional
`personal_apps.calendar` integration. It reads one length-framed JSON request
from standard input and writes exactly one framed JSON response to standard
output. It has no listener, no network access, and no model integration.

It supports bounded calendar reads plus the explicitly approved,
non-recurring `calendar_create_event` and `calendar_update_event` operations.
It never sends invitations, changes attendees, edits recurrence, or starts a
background agent.

From a source checkout, install it with one explicit setup command:

```sh
make calendar-helper-setup
```

This builds in a temporary directory under Application Support rather than in
the project, signs the completed bundle, and installs it to
`~/Library/Application Support/llmtui/helpers/llmtui-personal-apps-calendar.app`.
It then opens the installed bundle, requests Full Calendar Access, and prints
the native calendar IDs needed in the configuration. Use
`make calendar-helper-install` when installation is all that is wanted, or
`make calendar-helper-list` to repeat just the permission/ID step.

Sign the completed bundle, not only the executable produced by `swiftc`.
Bundle-level signing binds `Info.plist` and its
`com.patrikcze.llmtui.personalapps.calendar` identifier to the executable so
macOS Calendar privacy controls can identify the helper consistently. The
Calendar entitlement is required for macOS to offer the full-access prompt.
The setup command creates an ad-hoc signature suitable for a local source
build. Set `CODESIGN_IDENTITY` when a local signing identity is required.

Verify the entitlement actually landed before going further:

```sh
codesign -dv --entitlements - "$HOME/Library/Application Support/llmtui/helpers/llmtui-personal-apps-calendar.app"
```

The output must list `com.apple.security.personal-information.calendars`. A
bundle signed without `--entitlements` still reports a valid ad-hoc signature,
but macOS denies the access request before displaying a prompt.

If the setup command reports that the active SDK is unsupported by the
compiler, Command Line Tools and the selected developer directory are from
different releases. When the active developer directory is
`/Library/Developer/CommandLineTools`, that path is correct: update or
reinstall Command Line Tools through macOS Software Update so its Swift
compiler and SDK come from the same release, then rerun the command. llmtui
cannot repair a mixed system toolchain.

The caller must configure the resulting **absolute** executable path in
`personal_apps.calendar.helper_path` to the executable inside that bundle.
Run `llmtui doctor` or `/doctor personal-apps` first: both checks are passive
and report a missing, relative, or non-executable path without launching the
companion or requesting Calendar access.

After installing the signed bundle, explicitly run its setup command once
through LaunchServices:

```sh
APP="$HOME/Library/Application Support/llmtui/helpers/llmtui-personal-apps-calendar.app"
open -W -n "$APP" --stdout /dev/stdout --stderr /dev/stderr --args --list-calendars
```

This is the permission-triggering validation step. Grant Full Calendar Access
when macOS asks. The command prints a JSON array in which each entry carries
the calendar's localized `title` and its native EventKit `id`:

```json
[
  {
    "id" : "35284761-30D3-418F-A1D7-7C67DC3417A1",
    "shared" : false,
    "source" : "US ICLOUD",
    "title" : "Domácí",
    "writable" : true
  }
]
```

Put the intended `id` values, not localized titles such as `Domácí`, in
`personal_apps.calendar.allowed_calendars`. Then restart llmtui, run
`/personal-apps connect calendar`, and request events.

If no prompt appears and the command reports that access was not granted, the
bundle identifier may hold a cached decision from an earlier build that was
signed without the entitlement. Clear it and rerun the command above:

```sh
tccutil reset Calendar com.patrikcze.llmtui.personalapps.calendar
```

LaunchServices matters for this first request: invoking the nested executable
from a hardened terminal or editor makes that parent application responsible
for the privacy prompt, and macOS can deny the request before showing one.

This source-build helper is only ad-hoc signed. A release app bundle needs a
separately reviewed Developer ID signing/notarization and archive-inclusion
process; llmtui does not discover or trust helpers from `PATH`, download one,
or compile Swift during ordinary startup. Outside the explicit setup command,
the companion requests macOS full Calendar access only when an explicitly
connected Calendar operation first needs data.
