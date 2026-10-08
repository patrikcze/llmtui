import Foundation

/// The chat retention guardrail: chats with no activity for a set number of
/// days are archived or deleted, so old conversations (and the attachment
/// text in them) do not pile up on the phone indefinitely.
///
/// Guarantees:
/// - It never touches a pinned chat, the chat that is open, a chat that is
///   generating a reply, or a chat with queued messages.
/// - A last-activity date in the future (clock changes) counts as "now", so
///   a wrong clock can only delay retention, never trigger it early.
/// - "Archive" is reversible. "Delete" removes the chat with its
///   attachments, caches and indexes, and applies to archived chats too.
/// - It runs when the app starts and returns to the foreground, never in the
///   background. A settings change that would affect existing chats right
///   away is confirmed first, with the number of chats.
nonisolated struct ChatRetentionPolicy: Equatable, Sendable {
    enum Action: String, CaseIterable, Identifiable, Sendable {
        case archive
        case delete

        var id: String { rawValue }

        var title: String {
            switch self {
            case .archive: "Archive"
            case .delete: "Delete"
            }
        }
    }

    static let daysKey = "iosChatRetentionDays"
    static let actionKey = "iosChatRetentionAction"
    static let defaultDays = 7
    /// The period choices; 0 means never.
    static let dayOptions = [1, 3, 7, 14, 30, 90, 365, 0]

    /// Days of inactivity before the action; nil means never.
    var days: Int?
    var action: Action

    static let standard = ChatRetentionPolicy(days: defaultDays, action: .archive)

    /// The policy saved in Settings, or the default (7 days, archive).
    static func load(from defaults: UserDefaults = .standard) -> ChatRetentionPolicy {
        let days = defaults.object(forKey: daysKey) == nil ? defaultDays : defaults.integer(forKey: daysKey)
        let action = defaults.string(forKey: actionKey).flatMap(Action.init(rawValue:)) ?? .archive
        return ChatRetentionPolicy(days: days > 0 ? days : nil, action: action)
    }

    func save(to defaults: UserDefaults = .standard) {
        defaults.set(days ?? 0, forKey: Self.daysKey)
        defaults.set(action.rawValue, forKey: Self.actionKey)
    }

    static func periodTitle(_ days: Int) -> String {
        switch days {
        case 0: "Forever"
        case 1: "1 day"
        case 365: "1 year"
        default: "\(days) days"
        }
    }

    /// The chats this policy archives or deletes at `now`.
    func evaluate(
        _ conversations: [MobileConversation],
        now: Date,
        protected: Set<UUID>
    ) -> ChatRetentionDecision {
        guard let days, days > 0 else { return ChatRetentionDecision() }
        let cutoff = now.addingTimeInterval(-Double(days) * 86_400)
        var decision = ChatRetentionDecision()
        for conversation in conversations {
            guard conversation.isPinned != true, !protected.contains(conversation.id) else { continue }
            let lastActivity = min(conversation.updatedAt, now)
            guard lastActivity < cutoff else { continue }
            switch action {
            case .archive:
                if conversation.archivedAt == nil { decision.archive.append(conversation.id) }
            case .delete:
                decision.delete.append(conversation.id)
            }
        }
        return decision
    }
}

nonisolated struct ChatRetentionDecision: Equatable, Sendable {
    var archive: [UUID] = []
    var delete: [UUID] = []

    var isEmpty: Bool { archive.isEmpty && delete.isEmpty }

    /// "3 chats archived after 7 days without activity."
    func summary(days: Int?) -> String? {
        guard !isEmpty else { return nil }
        let period = days.map { ChatRetentionPolicy.periodTitle($0) } ?? ""
        var parts: [String] = []
        if !archive.isEmpty { parts.append("\(archive.count) \(archive.count == 1 ? "chat" : "chats") archived") }
        if !delete.isEmpty { parts.append("\(delete.count) \(delete.count == 1 ? "chat" : "chats") deleted") }
        return parts.joined(separator: " and ") + " after \(period) without activity."
    }
}
