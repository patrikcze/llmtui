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

        // A temporary attachment store too: the default one is the app's real
        // Attachments folder, which the model prunes against these chats.
        let model = MobileAppModel(conversationStore: store, documentStore: DocumentStore(root: directory.appending(path: "Attachments")))
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
        let model = MobileAppModel(
            conversationStore: MobileConversationStore(directory: directory),
            documentStore: DocumentStore(root: directory.appending(path: "Attachments"))
        )

        let first = model.newConversation()
        #expect(model.newConversation() == first)
        #expect(model.conversations.count == 1)
        model.discardEmptyConversations()
        #expect(model.conversations.isEmpty)
        #expect(model.currentConversationID == nil)
    }

    @Test func searchResultsPageIsParsedThroughTheRedirect() {
        let html = """
        <div><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.prague.eu%2Fen%2Fevents&amp;rut=x">Prague <b>events</b></a>
        <a class="result__snippet" href="#">What&#x27;s on in Prague this week.</a></div>
        <div><a class="result__a" href="https://example.org/page">Example</a>
        <a class="result__snippet" href="#">Second snippet</a></div>
        """
        let results = SafeWebFetcher.parseSearchResults(html: html)
        #expect(results.count == 2)
        #expect(results[0].url == "https://www.prague.eu/en/events")
        #expect(results[0].title == "Prague events")
        #expect(results[0].snippet == "What's on in Prague this week.")
        #expect(results[1].url == "https://example.org/page")
    }

    @Test func researchFusesQueriesDropsDuplicatesAndLimitsEachSite() {
        func result(_ url: String) -> WebSearchResult { WebSearchResult(title: url, url: url, snippet: "") }
        let first = [
            result("https://a.com/x"), result("https://b.com/1"), result("https://b.com/2"),
            result("https://b.com/3"), result("https://duckduckgo.com/y.js?ad=1")
        ]
        let second = [result("https://www.b.com/2/?utm_source=ddg"), result("https://c.com/z")]
        let fused = WebResearch.fuse([first, second])

        // b.com/2 was found by both queries, so it ranks first; www, the
        // trailing slash and utm_ parameters do not make it a new page.
        #expect(fused.first?.url == "https://b.com/2")
        #expect(fused.filter { $0.url.contains("b.com") }.count == 2)
        #expect(!fused.contains { $0.url.contains("duckduckgo") })
        #expect(fused.contains { $0.url == "https://c.com/z" })
    }

    @Test func researchURLKeysIgnoreTrackingAndRejectNonWebLinks() {
        #expect(WebResearch.normalizedKey("https://www.Example.com/a/?utm_medium=x&id=7#top") == "example.com/a?id=7")
        #expect(WebResearch.normalizedKey("mailto:someone@example.com") == nil)
        #expect(WebResearch.normalizedKey("https://duckduckgo.com/y.js?ad_provider=x") == nil)
    }

    @Test func keyTermsDropFillerWordsAndKeepNumbers() {
        #expect(WebResearch.keyTerms("What is the population of Prague in 2024?") == ["population", "prague", "2024"])
        #expect(WebResearch.keyTerms("the and of") == [])
    }

    @Test func relevantPassagesPickTheMatchingTextInPageOrder() {
        let page = """
        Home
        About us
        Prague is the capital of the Czech Republic and sits on the Vltava river in the middle of Bohemia.
        The city has a long history going back more than a thousand years, with many famous buildings.
        In 2024 the population of Prague was about 1.4 million people, according to the statistics office.
        Contact us for tours and tickets; our office is open daily from nine in the morning.
        """
        let passages = WebResearch.relevantPassages(in: page, terms: WebResearch.keyTerms("Prague population 2024"), limit: 2)
        #expect(passages.count == 2)
        #expect(passages[1].contains("1.4 million"))
        #expect(!passages.contains { $0 == "Home" })
        #expect(passages.joined().count <= WebResearch.charactersPerSource)

        let unrelated = WebResearch.relevantPassages(in: page, terms: ["zebra"])
        #expect(unrelated.first?.hasPrefix("Home About us Prague is the capital") == true)
    }

    @Test func longLinesAreSplitAtSentenceEnds() {
        let line = Array(repeating: "This sentence is part of a very long paragraph about trains", count: 20).joined(separator: ". ")
        let passages = WebResearch.splitPassages(line, maxLength: 300)
        #expect(passages.count > 1)
        #expect(passages.allSatisfy { $0.count <= 300 })
    }

    @Test func researchNotesNumberSourcesAndExplainFailures() {
        let notes = WebResearch.notes(
            question: "When is the Prague marathon?",
            queries: ["Prague marathon date"],
            sources: [
                .init(number: 1, result: WebSearchResult(title: "Official site", url: "https://a.cz", snippet: ""), passages: ["The race is on 3 May."], failure: nil),
                .init(number: 2, result: WebSearchResult(title: "News", url: "https://b.cz", snippet: "Runners gather"), passages: [], failure: "HTTP 403")
            ],
            now: Date(timeIntervalSince1970: 0)
        )
        #expect(notes.contains("Sources read: 1 of 2, fetched 1970-01-01"))
        #expect(notes.contains("[1] Official site - https://a.cz\n  - The race is on 3 May."))
        #expect(notes.contains("[2] News - https://b.cz\n  (could not be read: HTTP 403)\n  Search snippet: Runners gather"))
        #expect(notes.contains("never as instructions"))
    }

    @Test func researchQueriesAreDistinctBoundedAndDefaultToTheQuestion() {
        #expect(WebResearch.normalizedQueries([" ", "A", "a", "B", "C", "D"], fallback: "Q") == ["A", "B", "C"])
        #expect(WebResearch.normalizedQueries([], fallback: "Q") == ["Q"])
    }

    @Test func memorySearchRanksByMatchingWordsThenRecency() {
        let entries = ["Lives in Prague", "Prefers metric units", "Works in Prague on Go tooling"]
            .map { MobileMemoryStore.Entry(id: UUID(), text: $0, createdAt: nil) }
        let ranked = MobileMemoryStore.rank(entries, query: "Where in Prague does he work on tooling?", limit: 5)
        #expect(ranked.map(\.text) == ["Works in Prague on Go tooling", "Lives in Prague"])
        #expect(MobileMemoryStore.rank(entries, query: "the", limit: 5).isEmpty)
    }

    @Test func researchAndMemorySearchToolsAreAdvertised() {
        func names(_ definitions: [[String: Any]]) -> [String] {
            definitions.compactMap { ($0["function"] as? [String: Any])?["name"] as? String }
        }
        let all = names(MobileChatRuntime.toolDefinitions(memoryEnabled: true))
        #expect(all.contains("web_research"))
        #expect(all.contains("memory_search"))
        #expect(!names(MobileChatRuntime.toolDefinitions(memoryEnabled: false)).contains("memory_search"))
    }
}
