import Foundation
import CryptoKit

enum PersonalAppsMutationKind: String, Codable, Sendable {
    case mailDraft = "mail_draft"
    case mailSend = "mail_send"
    case calendarCreate = "calendar_create"

    var title: String {
        switch self {
        case .mailDraft: "Save Mail draft"
        case .mailSend: "Send email"
        case .calendarCreate: "Create calendar event"
        }
    }
}

struct MailMutationPayload: Codable, Equatable, Sendable {
    let accountID: String
    let accountName: String
    let to: [String]
    let cc: [String]
    let bcc: [String]
    let subject: String
    let body: String
}

struct CalendarMutationPayload: Codable, Equatable, Sendable {
    let calendarID: String
    let calendarTitle: String
    let title: String
    let start: Date
    let end: Date
    let isAllDay: Bool
    let timeZoneID: String
    let location: String?
    let notes: String?
    let url: String?
    let availability: String
    let alarmMinutesBefore: [Int]
}

enum PersonalAppsMutationPayload: Codable, Equatable, Sendable {
    case mail(MailMutationPayload)
    case calendar(CalendarMutationPayload)
}

struct PersonalAppsMutationPlan: Identifiable, Codable, Equatable, Sendable {
    let id: UUID
    let kind: PersonalAppsMutationKind
    let digest: String
    let createdAt: Date
    let expiresAt: Date
    let payload: PersonalAppsMutationPayload

    var exactPreview: [String] {
        switch payload {
        case .mail(let mail):
            return [
                "Account: \(mail.accountName)",
                "To: \(mail.to.joined(separator: ", "))",
                "Cc: \(mail.cc.isEmpty ? "—" : mail.cc.joined(separator: ", "))",
                "Bcc: \(mail.bcc.isEmpty ? "—" : mail.bcc.joined(separator: ", "))",
                "Subject: \(mail.subject)",
                "Body:\n\(mail.body)"
            ]
        case .calendar(let event):
            let formatter = ISO8601DateFormatter()
            return [
                "Calendar: \(event.calendarTitle)",
                "Title: \(event.title)",
                "Start: \(formatter.string(from: event.start))",
                "End: \(formatter.string(from: event.end))",
                "Time zone: \(event.timeZoneID)",
                "All day: \(event.isAllDay ? "Yes" : "No")",
                "Location: \(event.location ?? "—")",
                "URL: \(event.url ?? "—")",
                "Availability: \(event.availability)",
                "Alarms (minutes before): \(event.alarmMinutesBefore.isEmpty ? "—" : event.alarmMinutesBefore.map(String.init).joined(separator: ", "))",
                "Notes:\n\(event.notes ?? "—")"
            ]
        }
    }

    static func make(kind: PersonalAppsMutationKind, payload: PersonalAppsMutationPayload, now: Date = .now) throws -> Self {
        let canonical = try JSONEncoder.canonical.encode(payload)
        let digest = SHA256.hash(data: canonical).map { String(format: "%02x", $0) }.joined()
        return .init(id: UUID(), kind: kind, digest: digest, createdAt: now, expiresAt: now.addingTimeInterval(5 * 60), payload: payload)
    }
}

@MainActor
final class PersonalAppsMutationStore {
    private struct JournalEntry: Codable {
        let planID: UUID
        let digest: String
        let kind: PersonalAppsMutationKind
        let phase: String
        let timestamp: Date
        let outcome: String?
    }

    private var plans: [UUID: PersonalAppsMutationPlan] = [:]
    private var consumed: Set<UUID> = []
    private let journalURL: URL

    init(journalURL: URL? = nil) {
        self.journalURL = journalURL ?? FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".local/share/llmtui", isDirectory: true)
            .appendingPathComponent("llmtuigui-personal-apps-journal.json")
    }

    func insert(_ plan: PersonalAppsMutationPlan) throws {
        purgeExpired()
        guard plans.count < 8 else {
            throw PersonalAppsRuntimeError.invalidRequest("at most 8 active mutation plans are allowed")
        }
        plans[plan.id] = plan
    }

    func plan(id: UUID) -> PersonalAppsMutationPlan? {
        purgeExpired()
        return plans[id]
    }

    func consume(id: UUID, expectedDigest: String) throws -> PersonalAppsMutationPlan {
        purgeExpired()
        guard !consumed.contains(id), let plan = plans[id] else {
            throw PersonalAppsRuntimeError.invalidRequest("the mutation plan is missing, expired, or already consumed")
        }
        guard plan.digest == expectedDigest else {
            throw PersonalAppsRuntimeError.invalidRequest("the approved plan digest does not match")
        }
        try append(.init(planID: id, digest: plan.digest, kind: plan.kind, phase: "intent", timestamp: .now, outcome: nil))
        consumed.insert(id)
        plans.removeValue(forKey: id)
        return plan
    }

    func recordOutcome(for plan: PersonalAppsMutationPlan, outcome: String) {
        try? append(.init(planID: plan.id, digest: plan.digest, kind: plan.kind, phase: "outcome", timestamp: .now, outcome: outcome))
    }

    func clearPending() {
        plans.removeAll()
    }

    private func purgeExpired(now: Date = .now) {
        plans = plans.filter { $0.value.expiresAt > now }
    }

    private func append(_ entry: JournalEntry) throws {
        let manager = FileManager.default
        try manager.createDirectory(at: journalURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        var entries: [JournalEntry] = []
        if let data = try? Data(contentsOf: journalURL) {
            entries = (try? JSONDecoder().decode([JournalEntry].self, from: data)) ?? []
        }
        entries.append(entry)
        let data = try JSONEncoder.canonical.encode(entries.suffix(512))
        try data.write(to: journalURL, options: [.atomic])
        try manager.setAttributes([.posixPermissions: 0o600], ofItemAtPath: journalURL.path)
    }
}

private extension JSONEncoder {
    static var canonical: JSONEncoder {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        return encoder
    }
}
