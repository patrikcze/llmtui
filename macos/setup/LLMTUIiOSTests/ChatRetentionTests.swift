import Foundation
import Testing
@testable import LLMTUIiOS

/// The chat retention guardrail: which chats it archives or deletes, what it
/// never touches, and what archiving, restoring and deleting do on disk.
@MainActor
struct ChatRetentionTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func chat(daysAgo: Double, pinned: Bool = false, archived: Bool = false) -> MobileConversation {
        var conversation = MobileConversation(profileID: nil, now: now.addingTimeInterval(-daysAgo * 86_400))
        conversation.messages = [MobileChatMessage(role: .user, text: "Hello")]
        conversation.isPinned = pinned ? true : nil
        conversation.archivedAt = archived ? now.addingTimeInterval(-86_400) : nil
        return conversation
    }

    // MARK: - Policy

    @Test func defaultIsSevenDaysThenArchive() {
        let suite = "ChatRetentionTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        #expect(ChatRetentionPolicy.load(from: defaults) == ChatRetentionPolicy(days: 7, action: .archive))

        ChatRetentionPolicy(days: nil, action: .delete).save(to: defaults)
        #expect(ChatRetentionPolicy.load(from: defaults) == ChatRetentionPolicy(days: nil, action: .delete))
        ChatRetentionPolicy(days: 30, action: .delete).save(to: defaults)
        #expect(ChatRetentionPolicy.load(from: defaults) == ChatRetentionPolicy(days: 30, action: .delete))
    }

    @Test func archivesOnlyChatsInactiveLongerThanThePeriod() {
        let old = chat(daysAgo: 8)
        let edge = chat(daysAgo: 6.9)
        let recent = chat(daysAgo: 1)
        let decision = ChatRetentionPolicy(days: 7, action: .archive).evaluate([old, edge, recent], now: now, protected: [])
        #expect(decision.archive == [old.id])
        #expect(decision.delete.isEmpty)
    }

    @Test func neverTouchesPinnedProtectedOrAlreadyArchivedChats() {
        let pinned = chat(daysAgo: 30, pinned: true)
        let open = chat(daysAgo: 30)
        let archived = chat(daysAgo: 30, archived: true)
        let decision = ChatRetentionPolicy(days: 7, action: .archive).evaluate([pinned, open, archived], now: now, protected: [open.id])
        #expect(decision.isEmpty)
    }

    @Test func deleteModeAlsoRemovesOldArchivedChatsButNotPinnedOnes() {
        let archived = chat(daysAgo: 30, archived: true)
        let old = chat(daysAgo: 10)
        let pinned = chat(daysAgo: 400, pinned: true)
        let decision = ChatRetentionPolicy(days: 7, action: .delete).evaluate([archived, old, pinned], now: now, protected: [])
        #expect(Set(decision.delete) == [archived.id, old.id])
        #expect(decision.archive.isEmpty)
    }

    @Test func foreverAndFutureDatesNeverTrigger() {
        let old = chat(daysAgo: 1_000)
        #expect(ChatRetentionPolicy(days: nil, action: .delete).evaluate([old], now: now, protected: []).isEmpty)
        // A chat dated in the future (the clock moved) counts as active now.
        let future = chat(daysAgo: -30)
        #expect(ChatRetentionPolicy(days: 1, action: .delete).evaluate([future], now: now, protected: []).isEmpty)
    }

    @Test func summaryNamesCountsAndPeriod() {
        let decision = ChatRetentionDecision(archive: [UUID(), UUID()], delete: [UUID()])
        #expect(decision.summary(days: 7) == "2 chats archived and 1 chat deleted after 7 days without activity.")
        #expect(ChatRetentionDecision().summary(days: 7) == nil)
    }

    // MARK: - Model

    private func model(with conversations: [MobileConversation]) throws -> (MobileAppModel, URL) {
        let base = FileManager.default.temporaryDirectory.appending(path: "ChatRetentionTests-\(UUID().uuidString)")
        let store = MobileConversationStore(directory: base.appending(path: "Conversations"))
        for conversation in conversations { try store.save(conversation) }
        let model = MobileAppModel(conversationStore: store, documentStore: DocumentStore(root: base.appending(path: "Attachments")))
        return (model, base)
    }

    @Test func archivingHidesAChatAndRestoringBringsItBackAsActive() throws {
        let old = chat(daysAgo: 10)
        let recent = chat(daysAgo: 1)
        let (model, base) = try model(with: [old, recent])
        defer { try? FileManager.default.removeItem(at: base) }

        let decision = model.applyRetention(ChatRetentionPolicy(days: 7, action: .archive), now: now)
        #expect(decision.archive == [old.id])
        #expect(model.listedConversations.map(\.id) == [recent.id])
        #expect(model.archivedConversations.map(\.id) == [old.id])
        #expect(model.retentionNotice == "1 chat archived after 7 days without activity.")

        // Archiving is saved and is not activity.
        let reloaded = MobileConversationStore(directory: base.appending(path: "Conversations")).loadAll()
        let saved = try #require(reloaded.first { $0.id == old.id })
        #expect(saved.archivedAt == now)
        #expect(saved.updatedAt == old.updatedAt)

        // Restoring counts as activity, so the next run leaves it alone.
        model.restoreConversation(old.id)
        #expect(model.archivedConversations.isEmpty)
        #expect(model.applyRetention(ChatRetentionPolicy(days: 7, action: .archive)).isEmpty)
    }

    @Test func deleteModeRemovesChatsWithTheirAttachments() async throws {
        let old = chat(daysAgo: 10)
        let (model, base) = try model(with: [old])
        defer { try? FileManager.default.removeItem(at: base) }
        let note = FileManager.default.temporaryDirectory.appending(path: "retention-\(UUID().uuidString).txt")
        try "Old notes".write(to: note, atomically: true, encoding: .utf8)
        model.documents.importFiles([note], into: old.id)
        await model.documents.waitUntilIdle()
        let folder = base.appending(path: "Attachments").appending(path: old.id.uuidString)
        #expect(FileManager.default.fileExists(atPath: folder.path))

        model.applyRetention(ChatRetentionPolicy(days: 7, action: .delete), now: now)
        #expect(model.conversations.isEmpty)
        #expect(!FileManager.default.fileExists(atPath: folder.path))
        #expect(MobileConversationStore(directory: base.appending(path: "Conversations")).loadAll().isEmpty)
    }

    @Test func pinningIsKeptAndIsNotActivity() throws {
        let old = chat(daysAgo: 10)
        let (model, base) = try model(with: [old])
        defer { try? FileManager.default.removeItem(at: base) }
        model.setPinned(old.id, true)
        #expect(model.conversations.first?.updatedAt == old.updatedAt)
        #expect(model.applyRetention(ChatRetentionPolicy(days: 7, action: .delete), now: now).isEmpty)
        #expect(model.listedConversations.first?.isPinned == true)
    }

    @Test func theOpenChatIsProtectedAndPreviewChangesNothing() throws {
        let old = chat(daysAgo: 10)
        let other = chat(daysAgo: 10)
        let (model, base) = try model(with: [old, other])
        defer { try? FileManager.default.removeItem(at: base) }
        model.openConversation(old.id)

        let preview = model.retentionPreview(ChatRetentionPolicy(days: 7, action: .delete), now: now)
        #expect(preview.delete == [other.id])
        #expect(model.conversations.count == 2)

        model.applyRetention(ChatRetentionPolicy(days: 7, action: .delete), now: now)
        #expect(model.conversations.map(\.id) == [old.id])
    }

    @Test func pinnedChatsAreListedFirst() throws {
        let newer = chat(daysAgo: 1)
        let pinnedOlder = chat(daysAgo: 5, pinned: true)
        let (model, base) = try model(with: [newer, pinnedOlder])
        defer { try? FileManager.default.removeItem(at: base) }
        #expect(model.listedConversations.map(\.id) == [pinnedOlder.id, newer.id])
    }
}
