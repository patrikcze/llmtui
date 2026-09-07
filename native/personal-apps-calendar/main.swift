import EventKit
import Foundation

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
}

private struct EventReference: Decodable {
    let calendar_id: String
    let event_id: String
    let occurrence: String?
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
    static func main() {
        do {
            let request = try readRequest()
            let response: Response
            do {
                response = try handle(request)
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

    private static func handle(_ request: Request) throws -> Response {
        let store = EKEventStore()
        try ensureFullAccess(store)
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
        default:
            throw BridgeFailure.unsupported("unknown operation")
        }
        return response
    }

    private static func ensureFullAccess(_ store: EKEventStore) throws {
        switch EKEventStore.authorizationStatus(for: .event) {
        case .fullAccess:
            return
        case .notDetermined:
            let semaphore = DispatchSemaphore(value: 0)
            var granted = false
            var requestError: Error?
            Task {
                do {
                    granted = try await store.requestFullAccessToEvents()
                } catch {
                    requestError = error
                }
                semaphore.signal()
            }
            semaphore.wait()
            if let requestError { throw BridgeFailure.permissionDenied(requestError.localizedDescription) }
            if granted { return }
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

    private static func write(_ response: Response) throws {
        let body = try JSONEncoder().encode(response)
        guard body.count > 0, body.count <= maxFrameBytes else { return }
        var length = UInt32(body.count).bigEndian
        var frame = Data(bytes: &length, count: 4)
        frame.append(body)
        FileHandle.standardOutput.write(frame)
    }
}
