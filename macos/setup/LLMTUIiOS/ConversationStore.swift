import Foundation

/// One chat: its own title, provider and messages.
struct MobileConversation: Codable, Identifiable, Equatable, Sendable {
    static let defaultTitle = "New Chat"

    var id: UUID
    var title: String
    /// True once the user renamed the chat. Until then the title follows the
    /// chat's first message.
    var hasCustomTitle: Bool
    /// The provider this chat talks to. Nil, or a deleted provider, falls back
    /// to the app's active provider.
    var profileID: UUID?
    var messages: [MobileChatMessage]
    var createdAt: Date
    var updatedAt: Date
    /// Providers the user allowed to receive excerpts from this chat's
    /// attachments. Absent in chats saved before attachments existed.
    var documentConsentProfileIDs: [UUID]?
    /// Set when retention (or the user) archived the chat. Archived chats are
    /// hidden from the chat list until restored.
    var archivedAt: Date?
    /// A pinned chat stays at the top and is never archived or deleted by
    /// retention.
    var isPinned: Bool?

    init(id: UUID = UUID(), profileID: UUID?, now: Date = .now) {
        self.id = id
        self.title = Self.defaultTitle
        self.hasCustomTitle = false
        self.profileID = profileID
        self.messages = []
        self.createdAt = now
        self.updatedAt = now
    }

    /// A short title from the first user message: its first line, at most 40
    /// characters, or "Image" for a message that is only images.
    static func automaticTitle(for messages: [MobileChatMessage]) -> String {
        guard let first = messages.first(where: { $0.role == .user }) else { return defaultTitle }
        let line = first.text
            .split(whereSeparator: \.isNewline)
            .first
            .map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
        if line.isEmpty { return first.attachments.isEmpty ? defaultTitle : "Image" }
        guard line.count > 40 else { return line }
        return String(line.prefix(39)).trimmingCharacters(in: .whitespaces) + "\u{2026}"
    }

    /// The last message's first line, for the chat list.
    var preview: String {
        guard let last = messages.last(where: { !$0.text.isEmpty }) else { return "" }
        let line = last.text.split(whereSeparator: \.isNewline).first.map(String.init) ?? ""
        return String(line.prefix(120))
    }
}

/// Saves each chat as its own JSON file under Application Support, so chats
/// survive the app being closed. Writes are atomic and use iOS data
/// protection; nothing leaves the device.
struct MobileConversationStore: Sendable {
    let directory: URL

    /// Nonisolated because `MobileAppModel.init` creates the store in a
    /// default argument, which is evaluated outside the main actor; it only
    /// builds a URL.
    nonisolated init(directory: URL? = nil) {
        self.directory = directory
            ?? URL.applicationSupportDirectory.appending(path: "Conversations", directoryHint: .isDirectory)
    }

    /// Every saved chat, newest first. A file that cannot be read is skipped
    /// rather than failing the whole list. Replies that were streaming when
    /// the app closed are marked finished.
    func loadAll() -> [MobileConversation] {
        guard let urls = try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil) else {
            return []
        }
        let decoder = JSONDecoder()
        return urls
            .filter { $0.pathExtension == "json" }
            .compactMap { url in try? decoder.decode(MobileConversation.self, from: Data(contentsOf: url)) }
            .map { conversation in
                var loaded = conversation
                for index in loaded.messages.indices {
                    loaded.messages[index].isStreaming = false
                }
                return loaded
            }
            .sorted { $0.updatedAt > $1.updatedAt }
    }

    func save(_ conversation: MobileConversation) throws {
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let data = try JSONEncoder().encode(conversation)
        try data.write(to: fileURL(for: conversation.id), options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    func delete(_ id: UUID) {
        try? FileManager.default.removeItem(at: fileURL(for: id))
    }

    private func fileURL(for id: UUID) -> URL {
        directory.appending(path: "\(id.uuidString).json")
    }
}
