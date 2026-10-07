import Foundation
import Testing
@testable import LLMTUIiOS

@MainActor
struct LLMTUIiOSTests {
    @Test func providerProfilesRoundTripWithoutCredentials() throws {
        let suiteName = "LLMTUIiOSTests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        let store = MobileProviderStore(defaults: defaults)
        let profile = MobileProviderProfile(
            name: "Studio",
            type: .lmStudio,
            baseURL: "http://192.168.1.20:1234/v1",
            model: "local-model"
        )

        try store.saveProfiles([profile])
        store.saveActiveID(profile.id)

        #expect(store.loadProfiles() == [profile])
        #expect(store.loadActiveID() == profile.id)
    }

    @Test func webToolRejectsPrivateAndLocalAddresses() {
        #expect(!SafeWebFetcher.isPublicHost("localhost"))
        #expect(!SafeWebFetcher.isPublicHost("127.0.0.1"))
        #expect(!SafeWebFetcher.isPublicHost("10.0.0.4"))
        #expect(!SafeWebFetcher.isPublicHost("172.16.0.2"))
        #expect(!SafeWebFetcher.isPublicHost("192.168.1.2"))
        #expect(!SafeWebFetcher.isPublicHost("fd00::1"))
        #expect(SafeWebFetcher.isPublicHost("example.com"))
        #expect(SafeWebFetcher.isPublicHost("8.8.8.8"))
    }

    @Test func providerDefaultsUseLANExamplesInsteadOfLocalhost() {
        #expect(!MobileProviderType.ollama.defaultBaseURL.contains("localhost"))
        #expect(!MobileProviderType.lmStudio.defaultBaseURL.contains("localhost"))
    }

    @Test func minimumWebToolsAreAdvertised() {
        let names = MobileChatRuntime.toolDefinitions.compactMap { definition in
            (definition["function"] as? [String: Any])?["name"] as? String
        }
        #expect(names.contains("web_search"))
        #expect(names.contains("web_fetch"))
        #expect(names.contains("local_context"))
        #expect(!names.contains("run_command"))
    }

    @Test func attachmentRoundTripsWithChatMessage() throws {
        let attachment = MobileAttachment(data: Data([0xFF, 0xD8, 0xFF]))
        let message = MobileChatMessage(role: .user, text: "Describe this", attachments: [attachment])
        let encoded = try JSONEncoder().encode(message)
        let decoded = try JSONDecoder().decode(MobileChatMessage.self, from: encoded)
        #expect(decoded == message)
    }

    @Test func localContextIncludesTimeAndBoundedDeviceInformation() {
        let context = MobileLocalContext.value(now: Date(timeIntervalSince1970: 0))
        #expect(context.contains("local_datetime:"))
        #expect(context.contains("utc_datetime: 1970-01-01T00:00:00Z"))
        #expect(context.contains("timezone:"))
        #expect(context.contains("os:"))
        #expect(context.contains("memory_total:"))
        #expect(context.contains("storage_available:"))
        #expect(!context.contains("username:"))
        #expect(!context.contains("hostname:"))
    }

    @Test func memoryStoreRemembersForgetsByPrefixAndRejectsSecrets() async throws {
        let suiteName = "LLMTUIiOSTests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        let store = MobileMemoryStore(defaults: defaults)

        _ = try await store.remember("Prefers metric units")
        let saved = try #require(await store.entries().first)
        #expect(saved.text == "Prefers metric units")
        #expect(saved.createdAt != nil)

        await #expect(throws: MobileMemoryStore.MemoryError.self) {
            try await store.remember("my key is sk-abc123def456ghi789")
        }
        #expect(await store.forget("ABC") == "Memory not found. Call memory_list for the exact id.")
        let prefix = String(saved.id.uuidString.prefix(8)).lowercased()
        #expect(await store.forget(prefix) == "Memory removed.")
        #expect(await store.entries().isEmpty)
    }

    @Test func memoryStoreReadsEntriesSavedWithoutDates() async throws {
        let suiteName = "LLMTUIiOSTests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defer { defaults.removePersistentDomain(forName: suiteName) }
        let id = UUID()
        defaults.set(Data(#"[{"id":"\#(id.uuidString)","text":"Lives in Prague"}]"#.utf8), forKey: "iosChatMemories")

        let entries = await MobileMemoryStore(defaults: defaults).entries()
        #expect(entries == [MobileMemoryStore.Entry(id: id, text: "Lives in Prague", createdAt: nil)])
    }

    @Test func memoryPromptSectionIsNewestFirstAndBounded() throws {
        let entries = (1...5).map { MobileMemoryStore.Entry(id: UUID(), text: "fact \($0)", createdAt: nil) }
        let section = try #require(MobileMemoryStore.promptSection(for: entries, maxEntries: 2))
        #expect(section.contains("- fact 5\n- fact 4"))
        #expect(!section.contains("fact 3"))
        #expect(MobileMemoryStore.promptSection(for: []) == nil)
    }

    @Test func memoryToolsAreOfferedOnlyWhileMemoryIsOn() {
        func names(_ definitions: [[String: Any]]) -> [String] {
            definitions.compactMap { ($0["function"] as? [String: Any])?["name"] as? String }
        }
        #expect(names(MobileChatRuntime.toolDefinitions(memoryEnabled: true)).contains("memory_remember"))
        let withoutMemory = names(MobileChatRuntime.toolDefinitions(memoryEnabled: false))
        #expect(!withoutMemory.contains { $0.hasPrefix("memory_") })
        #expect(withoutMemory.contains("web_search"))
    }

    @Test func approvalModesDecideWhichToolsAsk() {
        #expect(MobileToolApprovalMode.always.requiresApproval("web_fetch"))
        #expect(MobileToolApprovalMode.always.requiresApproval("memory_remember"))
        #expect(!MobileToolApprovalMode.memoryChanges.requiresApproval("web_fetch"))
        #expect(MobileToolApprovalMode.memoryChanges.requiresApproval("memory_forget"))
        #expect(!MobileToolApprovalMode.never.requiresApproval("memory_remember"))
    }

    @Test func conversationsPersistNewestFirstAndFinishInterruptedReplies() throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: "LLMTUIiOSTests-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = MobileConversationStore(directory: directory)

        var older = MobileConversation(profileID: nil, now: Date(timeIntervalSince1970: 100))
        older.messages = [MobileChatMessage(role: .user, text: "First")]
        var newer = MobileConversation(profileID: UUID(), now: Date(timeIntervalSince1970: 200))
        newer.messages = [
            MobileChatMessage(role: .user, text: "Second"),
            MobileChatMessage(role: .assistant, text: "Half a repl", isStreaming: true)
        ]
        try store.save(older)
        try store.save(newer)

        let loaded = store.loadAll()
        #expect(loaded.map(\.id) == [newer.id, older.id])
        #expect(loaded[0].profileID == newer.profileID)
        #expect(loaded[0].messages.allSatisfy { !$0.isStreaming })

        store.delete(older.id)
        #expect(store.loadAll().map(\.id) == [newer.id])
    }

    @Test func automaticTitleUsesTheFirstLineOfTheFirstUserMessage() {
        #expect(MobileConversation.automaticTitle(for: []) == MobileConversation.defaultTitle)
        let short = [MobileChatMessage(role: .user, text: "Plan a trip\nto Prague in May")]
        #expect(MobileConversation.automaticTitle(for: short) == "Plan a trip")
        let long = [MobileChatMessage(role: .user, text: String(repeating: "a", count: 60))]
        #expect(MobileConversation.automaticTitle(for: long).count == 40)
        #expect(MobileConversation.automaticTitle(for: long).hasSuffix("\u{2026}"))
        let image = [MobileChatMessage(role: .user, text: "", attachments: [MobileAttachment(data: Data([1]))])]
        #expect(MobileConversation.automaticTitle(for: image) == "Image")
    }

    @Test func renamingAndDeletingChatsUpdatesTheStore() throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: "LLMTUIiOSTests-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = MobileConversationStore(directory: directory)
        var conversation = MobileConversation(profileID: nil)
        conversation.messages = [MobileChatMessage(role: .user, text: "Hello there")]
        conversation.title = MobileConversation.automaticTitle(for: conversation.messages)
        try store.save(conversation)

        let model = MobileAppModel(conversationStore: store)
        model.renameConversation(conversation.id, to: "  Trip ideas  ")
        #expect(store.loadAll().first?.title == "Trip ideas")
        #expect(store.loadAll().first?.hasCustomTitle == true)

        model.renameConversation(conversation.id, to: "")
        #expect(store.loadAll().first?.title == "Hello there")

        model.deleteConversation(conversation.id)
        #expect(store.loadAll().isEmpty)
        #expect(model.conversations.isEmpty)
    }

    @Test func newChatIsReusedWhileEmptyAndDiscardedWhenLeft() throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: "LLMTUIiOSTests-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: directory) }
        let model = MobileAppModel(conversationStore: MobileConversationStore(directory: directory))

        let first = model.newConversation()
        #expect(model.newConversation() == first)
        #expect(model.conversations.count == 1)
        model.discardEmptyConversations()
        #expect(model.conversations.isEmpty)
        #expect(model.currentConversationID == nil)
    }
}
