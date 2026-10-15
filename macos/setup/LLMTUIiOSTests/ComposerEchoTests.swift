import Foundation
import Testing
@testable import LLMTUIiOS

/// A sent or queued message the message field writes back must not stay in
/// the composer (ComposerEchoGuard), while typing after a send still works.
@MainActor
struct ComposerEchoTests {
    private final class FakeClock {
        var now = ContinuousClock.now
    }

    private func makeModel() -> (MobileAppModel, FakeClock, URL) {
        let base = FileManager.default.temporaryDirectory.appending(path: "ComposerEchoTests-\(UUID().uuidString)")
        let model = MobileAppModel(
            conversationStore: MobileConversationStore(directory: base.appending(path: "Conversations")),
            documentStore: DocumentStore(root: base.appending(path: "Attachments"))
        )
        // No provider, so a send never reaches the network.
        model.profiles = []
        model.activeProfileID = nil
        let clock = FakeClock()
        model.composerClock = { clock.now }
        _ = model.newConversation()
        return (model, clock, base)
    }

    @Test func queuedMessageEchoIsDroppedAndTheFieldIsRebuilt() {
        let (model, _, base) = makeModel()
        defer { try? FileManager.default.removeItem(at: base) }
        model.isGenerating = true
        model.updateDraftFromComposer("Test the connected Laya MCP server")
        model.send()
        #expect(model.currentQueuedMessages.map(\.text) == ["Test the connected Laya MCP server"])
        #expect(model.draft.isEmpty)

        // The field commits its pending prediction and writes the old text back.
        model.updateDraftFromComposer("Test the connected Laya MCP server by")
        #expect(model.draft.isEmpty)
        #expect(model.composerResetCount == 1)
        // A second write-back from the field being replaced is dropped too.
        model.updateDraftFromComposer("Test the connected Laya MCP server")
        #expect(model.draft.isEmpty)

        // The next thing typed is kept, and ends the guard.
        model.updateDraftFromComposer("A")
        #expect(model.draft == "A")
        model.updateDraftFromComposer("Test the connected Laya MCP server")
        #expect(model.draft == "Test the connected Laya MCP server")
    }

    @Test func sentMessageEchoIsDropped() {
        let (model, _, base) = makeModel()
        defer { try? FileManager.default.removeItem(at: base) }
        model.updateDraftFromComposer("Hello there")
        model.send()
        #expect(model.messages.isEmpty || model.messages.first?.text == "Hello there")
        #expect(model.draft.isEmpty)
        model.updateDraftFromComposer("Hello there")
        #expect(model.draft.isEmpty)
    }

    @Test func theSameTextLaterIsNotAnEcho() {
        let (model, clock, base) = makeModel()
        defer { try? FileManager.default.removeItem(at: base) }
        model.isGenerating = true
        model.updateDraftFromComposer("Again")
        model.send()
        clock.now = clock.now.advanced(by: .seconds(2))
        model.updateDraftFromComposer("Again")
        #expect(model.draft == "Again")
        #expect(model.composerResetCount == 0)
    }

    @Test func editingAQueuedMessageIsNotAnEcho() throws {
        let (model, _, base) = makeModel()
        defer { try? FileManager.default.removeItem(at: base) }
        model.isGenerating = true
        model.updateDraftFromComposer("Fix this")
        model.send()
        let queued = try #require(model.currentQueuedMessages.first)
        model.editQueuedMessage(queued)
        #expect(model.draft == "Fix this")
        model.updateDraftFromComposer("Fix this please")
        #expect(model.draft == "Fix this please")
    }

    @Test(arguments: [
        ("Hello world", "Hello world", true),
        ("Hello wor", "Hello world", true),
        ("Hello wrld", "Hello world", true),
        ("Hello", "H", false),
        ("Hello world", "Hello", false),
        ("Hello world", "", false),
        ("Hi", "Hi there", true),
        ("Hi", "Ho", false),
    ])
    func echoDetection(sent: String, written: String, isEcho: Bool) {
        #expect(ComposerEchoGuard.isEcho(written, of: sent) == isEcho)
    }
}
