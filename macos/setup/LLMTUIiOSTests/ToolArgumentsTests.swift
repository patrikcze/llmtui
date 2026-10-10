import Foundation
import Testing
@testable import LLMTUIiOS

/// Tool arguments as different servers deliver them, and the errors the
/// model gets back when an argument is really missing.
struct ToolArgumentsTests {
    @Test func nearMissesAreAccepted() {
        #expect(ToolArguments.string(["query": " moto 2027 "], ["question", "query"]) == "moto 2027")
        #expect(ToolArguments.string(["question": "", "q": "x"], ["question", "q"]) == "x")
        #expect(ToolArguments.string(["max": 4], ["max"]) == "4")
        #expect(ToolArguments.string([:], ["question"]) == nil)

        #expect(ToolArguments.strings(["a", " ", 3, "b"]) == ["a", "b"])
        #expect(ToolArguments.strings("single query") == ["single query"])
        #expect(ToolArguments.strings(nil).isEmpty)

        #expect(ToolArguments.int(["max_sources": "5"], ["max_sources"]) == 5)
        #expect(ToolArguments.int(["sources": 3], ["max_sources", "sources"]) == 3)
        #expect(ToolArguments.int(["max_sources": "many"], ["max_sources"]) == nil)
    }

    @Test func aMissingArgumentErrorTeachesTheModel() {
        let error = ToolArguments.missing(
            tool: "web_research",
            argument: "question",
            example: #"{"question":"Q"}"#,
            received: ["max_sources": 4]
        )
        #expect(error.contains("needs \"question\""))
        #expect(error.contains(#"It received {"max_sources":4}"#))
        #expect(error.contains(#"{"question":"Q"}"#))
        #expect(error.contains("Do not repeat a call that failed"))
        #expect(ToolArguments.describe([:]) == "It received no arguments.")
        #expect(ToolArguments.describe(["text": String(repeating: "x", count: 500)]).count < 230)
    }

    @Test func anUnknownToolErrorListsTheRealTools() {
        let error = ToolArguments.unknownTool("search_events_tool", offered: ["web_search", "web_fetch"])
        #expect(error.contains("There is no tool named \"search_events_tool\""))
        #expect(error.contains("Use one of: web_fetch, web_search."))
        #expect(ToolArguments.unknownTool("x", offered: []).contains("answer without tools"))
    }
}

/// The tool loop against a scripted server: what the model is told when it
/// calls a tool wrongly.
@MainActor @Suite(.serialized)
struct ToolArgumentLoopTests {
    private static func stream(_ text: String) -> String {
        let chunk = ["choices": [["delta": ["content": text], "finish_reason": "stop"]]]
        return "data: \(String(decoding: try! JSONSerialization.data(withJSONObject: chunk), as: UTF8.self))\n\ndata: [DONE]\n\n"
    }

    private static func call(_ name: String, _ arguments: String) -> String {
        let chunk: [String: Any] = ["choices": [["delta": ["tool_calls": [["index": 0, "id": "c1", "function": ["name": name, "arguments": arguments]]]], "finish_reason": "tool_calls"]]]
        return "data: \(String(decoding: try! JSONSerialization.data(withJSONObject: chunk), as: UTF8.self))\n\ndata: [DONE]\n\n"
    }

    private func toolResult(after replies: [String]) async throws -> String {
        let base = FileManager.default.temporaryDirectory.appending(path: "ToolArgumentLoopTests-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: base) }
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ToolArgumentStubProtocol.self]
        let model = MobileAppModel(
            conversationStore: MobileConversationStore(directory: base.appending(path: "Conversations")),
            documentStore: DocumentStore(root: base.appending(path: "Attachments")),
            runtime: MobileChatRuntime(session: URLSession(configuration: configuration))
        )
        let provider = MobileProviderProfile(model: "test-model")
        model.profiles = [provider]
        model.activeProfileID = provider.id
        model.toolsEnabled = true
        ToolArgumentStubProtocol.set(replies)
        _ = model.newConversation()
        model.draft = "Find motorcycle events."
        model.send()
        #expect(await waitUntil { !model.isGenerating })
        let requests = ToolArgumentStubProtocol.requests()
        try #require(requests.count == 2)
        let stream = try #require(requests[1].httpBodyStream ?? requests[1].httpBody.map { InputStream(data: $0) })
        stream.open(); defer { stream.close() }
        var data = Data(); var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable { let n = stream.read(&buffer, maxLength: buffer.count); if n <= 0 { break }; data.append(contentsOf: buffer.prefix(n)) }
        let body = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        let messages = body["messages"] as? [[String: Any]] ?? []
        return try #require(messages.last { $0["role"] as? String == "tool" }?["content"] as? String)
    }

    @Test func anInventedToolIsAnsweredWithTheRealOnes() async throws {
        let result = try await toolResult(after: [Self.call("search_events_tool", "{}"), Self.stream("Done.")])
        #expect(result.contains("There is no tool named \"search_events_tool\""))
        #expect(result.contains("web_research") && result.contains("web_search"))
    }

    @Test func aCallMissingItsArgumentGetsAnExample() async throws {
        // web_fetch without a URL fails before any network access or approval.
        let result = try await toolResult(after: [Self.call("web_fetch", #"{"focus":"dates"}"#), Self.stream("Done.")])
        #expect(result.contains("web_fetch needs \"url\""))
        #expect(result.contains(#"It received {"focus":"dates"}"#))
        #expect(result.contains(#"{"url":"https://example.com/page","focus":"dates"}"#))
    }
}

/// A scripted model server private to `ToolArgumentLoopTests`.
nonisolated final class ToolArgumentStubProtocol: URLProtocol {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var replies: [Data] = []
    nonisolated(unsafe) private static var captured: [URLRequest] = []

    static func set(_ texts: [String]) {
        lock.lock(); defer { lock.unlock() }
        replies = texts.map { Data($0.utf8) }
        captured = []
    }

    static func requests() -> [URLRequest] {
        lock.lock(); defer { lock.unlock() }
        return captured
    }

    private static func next(_ request: URLRequest) -> Data {
        lock.lock(); defer { lock.unlock() }
        captured.append(request)
        return replies.isEmpty ? Data() : replies.removeFirst()
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let data = Self.next(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "text/event-stream"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
