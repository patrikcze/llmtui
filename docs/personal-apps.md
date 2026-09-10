# Apple Mail and Calendar

`personal_apps` is an optional macOS-only integration for the Apple Mail and
Calendar accounts already configured in the current GUI session. It is off by
default. It never collects credentials, reads private databases, starts a
background agent, downloads a companion, or compiles Swift during normal
startup.

Mail uses a fixed embedded JXA bridge through `/usr/bin/osascript`. Calendar
uses the separate EventKit companion described below. Both are limited by the
scope in your configuration and require a deliberate connection in the chat
UI before any content operation can run.

## Setup

Start with a narrow allowlist. An empty list authorizes no Mail account or
Calendar. The `allowed_accounts` and `allowed_calendars` values are native app
identifiers, not display names and not the opaque `acct_...` / `cal_...`
handles shown to a model.

```yaml
personal_apps:
  enabled: true
  mail:
    enabled: true
    allowed_accounts:
      - "00000000-0000-0000-0000-000000000000"
  calendar:
    enabled: true
    allowed_calendars:
      - "calendar-native-identifier"
    helper_path: "/absolute/path/to/llmtui-personal-apps-calendar.app/Contents/MacOS/llmtui-personal-apps-calendar"
  mutations:
    enabled: false
```

To find Mail's native account UUIDs, run this read-only one-off command:

```sh
osascript -l JavaScript -e 'Application("Mail").accounts().map(a => a.name() + " => " + a.id())'
```

`calendar_list` returns only calendars already in the allowlist, so it cannot
be used to discover identifiers. From a source checkout, install the
companion, request Calendar access, and print its native EventKit identifiers
with one explicit command:

```sh
make calendar-helper-setup
```

Unlike the Mail command above, this one is not read-only in the privacy sense:
it is the step that triggers the macOS prompt. Grant **Full Calendar Access**
when asked. It then prints a JSON array with one entry per calendar:

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

Copy the `id` values you intend to expose into `allowed_calendars`. Use the
`id`, never the `title`: display titles are localized (`Domácí`, `Kalendář`)
and match nothing. A configured identifier that matches no available calendar
is reported as `scope_denied` rather than returning an empty list.

macOS attributes a terminal-launched Calendar request to the terminal or host
application that started it. Run setup and `llmtui` from the same terminal. If
setup reports `permission_denied`, grant that terminal **Full Calendar Access**
in System Settings, then retry; changing `calendar.helper_path` does not change
this macOS privacy decision.

Reload config, then explicitly run `/personal-apps connect mail` or
`/personal-apps connect calendar`.

## Calendar companion

The Calendar integration is unavailable until `calendar.helper_path` names an
absolute executable inside the installed EventKit app bundle. The setup
command installs the default bundle to
`~/Library/Application Support/llmtui/helpers/llmtui-personal-apps-calendar.app`;
set `helper_path` to its executable inside `Contents/MacOS`. The source-build
instructions and required `Info.plist` are in
[`native/personal-apps-calendar/README.md`](../native/personal-apps-calendar/README.md).

Use either `llmtui doctor` or `/doctor personal-apps` before connecting. These
checks are passive: they validate configuration and helper-file metadata but
never launch Mail, Calendar, or the companion, and never prompt for macOS
permission. A ready helper is not proof of permission; a denied or revoked
Calendar grant is reported as `permission_denied` when a person explicitly
connects Calendar and requests data.

If a Calendar tool says `unsupported_operation`, first check for a missing,
relative, non-executable, or non-macOS helper configuration. If it says
`permission_denied`, grant **Full Calendar Access** to the companion in macOS
System Settings, then retry through a fresh explicit Calendar connection. Do
not treat a successful Mail connection as Calendar authorization: macOS
controls them independently.

## Privacy and approvals

After a personal-data read, the current chat session is marked private: llmtui
does not save its transcript automatically, use response-cache results, or
capture it into memory/RAG-derived state. Disconnecting an app does not clear
that marker because text already present in the chat remains personal data.

Reads are bounded and report coverage. Always treat partial coverage as a
sample, not a complete inbox or calendar. Tool-returned mail/calendar text is
untrusted content, not instructions.

Mutations are disabled unless `mutations.enabled` is true. Each mutation
requires a frozen preview and a fresh human approval bound to that exact plan;
`/tools auto` and standing approvals never bypass it. Before any external
effect, llmtui records a digest-only intent in a user-level journal; a timeout,
cancelled helper, or missing outcome record yields `outcome_unknown` and is
never retried automatically.

The initial mutation set is intentionally narrow: same-account Mail moves,
explicit read/flag setters, saved drafts (never send), and non-recurring
personal Calendar create/update. Attachments, HTML drafts, attendees,
invitations, RSVP changes, recurrence editing, cross-account moves, and
deletion are unsupported.

## Compatibility and limitations

- Requires macOS and the user's active GUI session. Non-macOS builds keep
  normal chat working but report personal apps unsupported.
- Calendar requires a compatible EventKit companion protocol. The current
  source companion and Go adapter use protocol version 1; a mismatched reply
  is rejected rather than guessed.
- The companion is intentionally installed explicitly. Release signing,
  notarization, and archive inclusion are release-engineering work; source
  builds must keep supplying `helper_path` explicitly.
- No integration guarantees a transactional external effect. Other apps or
  remote sync can change state between a preview and apply; expected versions,
  fresh readback, per-item evidence, and the recovery journal reduce that risk
  but cannot remove it.
