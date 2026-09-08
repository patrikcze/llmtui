import AppKit
import EventKit
import Foundation
import Darwin

private let protocolVersion = 1
private let maxFrameBytes = 1024 * 1024

private struct Request: Decodable {
    let version: Int
    let request_id: String
    let operation: String
    let calendar_ids: [String]?
    let event: EventReference?
    let start: String?
    let end: String?
    let create_event: CreateEventRequest?
    let update_event: UpdateEventRequest?
}

private struct EventReference: Decodable {
    let calendar_id: String
    let event_id: String
    let occurrence: String?
}

// CreateEventRequest mirrors CalendarCreateEventChange: exactly one of
// (start, end) or (all_day_start, all_day_end) is expected — the Go side
// enforces that shape before this ever reaches the companion, but
// createEvent below still checks it directly rather than trusting the
// pairing.
private struct CreateEventRequest: Decodable {
    let calendar_id: String
    let title: String
    let notes: String?
    let location: String?
    let timezone: String?
    let start: String?
    let end: String?
    let all_day_start: String?
    let all_day_end: String?
}

// UpdateEventRequest mirrors CalendarUpdateEventChange: title/notes/
// location are Optional so an absent field leaves the stored value
// untouched, never cleared.
private struct UpdateEventRequest: Decodable {
    let event_id: String
    let calendar_id: String
    let expected_version: String
    let timezone: String?
    let title: String?
    let notes: String?
    let location: String?
    let start: String?
    let end: String?
    let all_day_start: String?
    let all_day_end: String?
}

private struct Response: Encodable {
    let version: Int
    let request_id: String
    let operation: String
    var calendars: [Calendar]?
    var events: [Event]?
    var event: Event?
    var error: HelperError?
}

private struct HelperError: Encodable {
    let code: String
    let message: String
}

private struct Calendar: Encodable {
    let id: String
    let title: String
    let source: String
    let writable: Bool
    // EventKit does not expose a reliable generic "shared" bit for all
    // source types. Keep this conservative until write support is added.
    let shared: Bool
}

private struct Event: Encodable {
    let id: String
    let calendar_id: String
    let item_id: String
    let title: String
    let location: String
    let notes: String
    let start: String
    let end: String
    let all_day: Bool
    let timezone: String
    let recurring: Bool
    let detached: Bool
    let canceled: Bool
    let tentative: Bool
    let busy: Bool
    let has_attendees: Bool
}

private enum BridgeFailure: Error {
    case invalidRequest(String)
    case permissionDenied(String)
    case notFound(String)
    case unsupported(String)
    case unavailable(String)
}

@main
private struct PersonalAppsCalendar {
    static func main() async {
        // LaunchServices appends a process-serial-number argument when an
        // app bundle is opened through `open`. It is launch metadata, not a
        // helper argument, and must not prevent the explicit setup command
        // from reaching EventKit.
        let arguments = CommandLine.arguments.dropFirst().filter { !$0.hasPrefix("-psn_") }
        if arguments == ["--list-calendars"] {
            do {
                prepareForPermissionPrompt()
                try await listCalendarsForSetup()
            } catch {
                let diagnostic = "calendar helper setup failed: \(String(describing: error))\n"
                FileHandle.standardError.write(Data(diagnostic.utf8))
                Darwin.exit(1)
            }
            return
        }
        guard arguments.isEmpty else {
            FileHandle.standardError.write(Data("calendar helper: unsupported argument\n".utf8))
            Darwin.exit(2)
        }
        do {
            let request = try readRequest()
            let response: Response
            do {
                response = try await handle(request)
            } catch {
                response = Response(
                    version: protocolVersion,
                    request_id: request.request_id,
                    operation: request.operation,
                    error: helperError(error)
                )
            }
            try write(response)
        } catch {
            // A malformed frame has no trustworthy request ID to correlate,
            // so stdout must stay empty. The Go side treats that as a framed
            // protocol failure; diagnostics are never mixed into stdout.
            let diagnostic = "calendar helper failed: \(String(describing: error))\n"
            FileHandle.standardError.write(Data(diagnostic.utf8))
        }
    }

    // EventKit presents its full-access prompt through the current macOS GUI
    // session. The companion is a Dock-less agent, so it initializes an
    // AppKit application context explicitly before the human-run setup path
    // requests that prompt.
    private static func prepareForPermissionPrompt() {
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory)
    }

    // listCalendarsForSetup is an explicit, human-run setup path. It is not
    // used by llmtui's framed protocol and does not alter the configured
    // allowlist; it exposes the native identifiers needed to create one.
    private static func listCalendarsForSetup() async throws {
        let store = EKEventStore()
        try await ensureFullAccess(store)
        let calendars = store.calendars(for: .event).map(calendar).sorted {
            if $0.source != $1.source { return $0.source < $1.source }
            if $0.title != $1.title { return $0.title < $1.title }
            return $0.id < $1.id
        }
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys]
        let body = try encoder.encode(calendars)
        FileHandle.standardOutput.write(body)
        FileHandle.standardOutput.write(Data([0x0a]))
    }

    private static func helperError(_ error: Error) -> HelperError {
        switch error {
        case BridgeFailure.permissionDenied:
            return HelperError(code: "permission_denied", message: "full calendar access was not granted")
        case BridgeFailure.notFound:
            return HelperError(code: "not_found", message: "the requested calendar item no longer exists")
        case BridgeFailure.unsupported:
            return HelperError(code: "unsupported", message: "the requested calendar operation is unsupported")
        case BridgeFailure.unavailable:
            return HelperError(code: "app_unavailable", message: "the EventKit store is unavailable")
        case BridgeFailure.invalidRequest:
            return HelperError(code: "invalid_request", message: "the calendar helper request was invalid")
        default:
            return HelperError(code: "app_unavailable", message: "the EventKit store could not complete the request")
        }
    }

    private static func readRequest() throws -> Request {
        let input = FileHandle.standardInput.readDataToEndOfFile()
        guard input.count >= 4 else { throw BridgeFailure.invalidRequest("partial frame") }
        let length = input.prefix(4).reduce(UInt32(0)) { ($0 << 8) | UInt32($1) }
        guard length > 0, length <= maxFrameBytes, input.count == Int(length) + 4 else {
            throw BridgeFailure.invalidRequest("invalid frame")
        }
        let request = try JSONDecoder().decode(Request.self, from: input.dropFirst(4))
        guard request.version == protocolVersion else {
            throw BridgeFailure.invalidRequest("unsupported protocol version")
        }
        return request
    }

    private static func handle(_ request: Request) async throws -> Response {
        let store = EKEventStore()
        try await ensureFullAccess(store)
        var response = Response(version: protocolVersion, request_id: request.request_id, operation: request.operation)
        switch request.operation {
        case "calendars":
            response.calendars = store.calendars(for: .event).map(calendar)
        case "events":
            guard let start = request.start.flatMap(parseDate), let end = request.end.flatMap(parseDate), start < end else {
                throw BridgeFailure.invalidRequest("events requires a non-empty RFC3339 interval")
            }
            let calendars = try resolveCalendars(request.calendar_ids, store)
            let predicate = store.predicateForEvents(withStart: start, end: end, calendars: calendars)
            response.events = store.events(matching: predicate).compactMap(event)
        case "event":
            guard let reference = request.event, let found = store.event(withIdentifier: reference.event_id) else {
                throw BridgeFailure.notFound("event no longer exists")
            }
            guard found.calendar.calendarIdentifier == reference.calendar_id, let value = event(found) else {
                throw BridgeFailure.notFound("event no longer matches its calendar")
            }
            response.event = value
        case "create_event":
            guard let create = request.create_event else {
                throw BridgeFailure.invalidRequest("create_event requires create_event")
            }
            response.event = try createEvent(create, store: store)
        case "update_event":
            guard let update = request.update_event else {
                throw BridgeFailure.invalidRequest("update_event requires update_event")
            }
            response.event = try updateEvent(update, store: store)
        default:
            throw BridgeFailure.unsupported("unknown operation")
        }
        return response
    }

    // createEvent makes one ordinary, non-recurring, attendee-free personal
    // event. It never invites anyone — this bridge has no attendee field at
    // all — and refuses a calendar that does not accept writes rather than
    // letting EventKit's save silently no-op or throw a less specific error.
    private static func createEvent(_ req: CreateEventRequest, store: EKEventStore) throws -> Event {
        guard let calendar = store.calendar(withIdentifier: req.calendar_id) else {
            throw BridgeFailure.notFound("calendar no longer exists")
        }
        guard calendar.allowsContentModifications else {
            throw BridgeFailure.unsupported("calendar does not accept writes")
        }
        let ev = EKEvent(eventStore: store)
        ev.calendar = calendar
        ev.title = req.title
        ev.notes = req.notes
        ev.location = req.location
        if let tz = req.timezone, !tz.isEmpty {
            guard let zone = TimeZone(identifier: tz) else {
                throw BridgeFailure.invalidRequest("unknown timezone")
            }
            ev.timeZone = zone
        }
        try applyInterval(to: ev, start: req.start, end: req.end, allDayStart: req.all_day_start, allDayEnd: req.all_day_end)
        try save(ev, store: store)
        guard let value = event(ev) else {
            throw BridgeFailure.unavailable("event saved but could not be read back")
        }
        return value
    }

    // updateEvent patches only the fields the request explicitly named,
    // preserving everything else — never a whole-event replacement. It
    // refuses a recurring or attendee-bearing event unconditionally: the Go
    // side already checked this against its own fresh read before sending
    // the request, but the check is repeated here directly against
    // EventKit's own current state, not trusted from the caller.
    private static func updateEvent(_ req: UpdateEventRequest, store: EKEventStore) throws -> Event {
        guard let ev = store.event(withIdentifier: req.event_id), ev.calendar.calendarIdentifier == req.calendar_id else {
            throw BridgeFailure.notFound("event no longer exists")
        }
        guard ev.calendar.allowsContentModifications else {
            throw BridgeFailure.unsupported("calendar does not accept writes")
        }
        guard !ev.hasRecurrenceRules else {
            throw BridgeFailure.unsupported("recurring events cannot be updated")
        }
        guard ev.attendees?.isEmpty ?? true else {
            throw BridgeFailure.unsupported("events with attendees cannot be updated")
        }
        if let title = req.title { ev.title = title }
        if let notes = req.notes { ev.notes = notes }
        if let location = req.location { ev.location = location }
        if let tz = req.timezone, !tz.isEmpty {
            guard let zone = TimeZone(identifier: tz) else {
                throw BridgeFailure.invalidRequest("unknown timezone")
            }
            ev.timeZone = zone
        }
        if req.start != nil || req.end != nil || req.all_day_start != nil || req.all_day_end != nil {
            try applyInterval(to: ev, start: req.start, end: req.end, allDayStart: req.all_day_start, allDayEnd: req.all_day_end)
        }
        try save(ev, store: store)
        guard let value = event(ev) else {
            throw BridgeFailure.unavailable("event updated but could not be read back")
        }
        return value
    }

    private static func applyInterval(to ev: EKEvent, start: String?, end: String?, allDayStart: String?, allDayEnd: String?) throws {
        if let allDayStart, let allDayEnd {
            guard let s = parseDateOnly(allDayStart), let e = parseDateOnly(allDayEnd), s < e else {
                throw BridgeFailure.invalidRequest("invalid all-day interval")
            }
            ev.isAllDay = true
            ev.startDate = s
            ev.endDate = e
            return
        }
        if let start, let end {
            guard let s = parseDate(start), let e = parseDate(end), s < e else {
                throw BridgeFailure.invalidRequest("invalid interval")
            }
            ev.startDate = s
            ev.endDate = e
            return
        }
        throw BridgeFailure.invalidRequest("an event requires either a timed or an all-day interval")
    }

    private static func save(_ ev: EKEvent, store: EKEventStore) throws {
        do {
            try store.save(ev, span: .thisEvent, commit: true)
        } catch {
            throw BridgeFailure.unavailable("could not save the event")
        }
    }

    private static func ensureFullAccess(_ store: EKEventStore) async throws {
        switch EKEventStore.authorizationStatus(for: .event) {
        case .fullAccess:
            return
        case .notDetermined:
            do {
                if try await store.requestFullAccessToEvents() {
                    return
                }
            } catch {
                throw BridgeFailure.permissionDenied(error.localizedDescription)
            }
            throw BridgeFailure.permissionDenied("full calendar access was not granted")
        case .writeOnly, .denied, .restricted:
            throw BridgeFailure.permissionDenied("full calendar access is required")
        @unknown default:
            throw BridgeFailure.permissionDenied("calendar authorization is unavailable")
        }
    }

    private static func resolveCalendars(_ identifiers: [String]?, _ store: EKEventStore) throws -> [EKCalendar] {
        guard let identifiers, !identifiers.isEmpty else {
            throw BridgeFailure.invalidRequest("at least one calendar is required")
        }
        let calendars = identifiers.compactMap(store.calendar(withIdentifier:))
        guard calendars.count == identifiers.count else {
            throw BridgeFailure.notFound("one or more calendars no longer exist")
        }
        return calendars
    }

    private static func calendar(_ value: EKCalendar) -> Calendar {
        Calendar(id: value.calendarIdentifier, title: value.title, source: value.source.title,
                 writable: value.allowsContentModifications, shared: false)
    }

    private static func event(_ value: EKEvent) -> Event? {
        guard let id = value.eventIdentifier else { return nil }
        let availability = value.availability
        return Event(
            id: id,
            calendar_id: value.calendar.calendarIdentifier,
            item_id: value.calendarItemIdentifier,
            title: value.title ?? "",
            location: value.location ?? "",
            notes: value.notes ?? "",
            start: formatDate(value.startDate),
            end: formatDate(value.endDate),
            all_day: value.isAllDay,
            timezone: value.timeZone?.identifier ?? "",
            recurring: value.hasRecurrenceRules,
            detached: value.isDetached,
            canceled: value.status == .canceled,
            tentative: availability == .tentative,
            busy: availability != .free,
            has_attendees: !(value.attendees?.isEmpty ?? true)
        )
    }

    private static func parseDate(_ value: String) -> Date? {
        ISO8601DateFormatter().date(from: value)
    }

    private static func formatDate(_ value: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.string(from: value)
    }

    // parseDateOnly interprets a "YYYY-MM-DD" date as UTC midnight — an
    // all-day EventKit event's startDate/endDate carry no timezone of their
    // own (isAllDay dates are calendar-day boundaries, not instants), so
    // there is no "correct" zone to pick here beyond a stable, documented
    // one. This is unverified against a real all-day event; see the
    // implementation plan's Slice 6 note.
    private static func parseDateOnly(_ value: String) -> Date? {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyy-MM-dd"
        formatter.timeZone = TimeZone(identifier: "UTC")
        formatter.calendar = Foundation.Calendar(identifier: .gregorian)
        return formatter.date(from: value)
    }

    private static func write(_ response: Response) throws {
        let body = try JSONEncoder().encode(response)
        guard body.count > 0, body.count <= maxFrameBytes else { return }
        var length = UInt32(body.count).bigEndian
        var frame = Data(bytes: &length, count: 4)
        frame.append(body)
        FileHandle.standardOutput.write(frame)
    }
}
