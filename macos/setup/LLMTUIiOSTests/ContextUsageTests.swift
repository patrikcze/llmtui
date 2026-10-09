import Foundation
import Testing
@testable import LLMTUIiOS

/// The composer's context ring: the usage estimate, where the window size
/// comes from, and how each server type is asked for it. Network calls go to
/// a stub, never to a real server; profiles are changed in memory only.
@MainActor
@Suite(.serialized)
struct ContextUsageTests {
    @Test func estimateGrowsWithHistoryDraftAndTools() {
        let messages = [
            MobileChatMessage(role: .user, text: String(repeating: "a", count: 400)),
            MobileChatMessage(role: .assistant, text: String(repeating: "b", count: 800))
        ]
        let plain = ContextUsageEstimate.tokens(messages: [], draft: "", toolsEnabled: false, memoryEnabled: false)
        let withHistory = ContextUsageEstimate.tokens(messages: messages, draft: "", toolsEnabled: false, memoryEnabled: false)
        let withDraft = ContextUsageEstimate.tokens(messages: messages, draft: String(repeating: "c", count: 400), toolsEnabled: false, memoryEnabled: false)
        let withTools = ContextUsageEstimate.tokens(messages: messages, draft: "", toolsEnabled: true, memoryEnabled: false)
        #expect(withHistory - plain == 100 + 8 + 200 + 8)
        #expect(withDraft - withHistory == 100)
        #expect(withTools > withHistory + 500)
    }

    @Test func profilesSavedBeforeContextSizeStillLoad() throws {
        let old = #"{"id":"6F9619FF-8B86-D011-B42D-00C04FC964FF","name":"Mac","type":"lmStudio","baseURL":"http://192.168.1.2:1234/v1","model":"gemma"}"#
        let profile = try JSONDecoder().decode(MobileProviderProfile.self, from: Data(old.utf8))
        #expect(profile.contextWindow == nil)
    }

    @Test func windowComesFromSettingThenServerThenFallback() async throws {
        let base = FileManager.default.temporaryDirectory.appending(path: "ContextUsageTests-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: base) }
        let model = MobileAppModel(
            conversationStore: MobileConversationStore(directory: base.appending(path: "Conversations")),
            documentStore: DocumentStore(root: base.appending(path: "Attachments"))
        )
        var profile = MobileProviderProfile(name: "Test", type: .lmStudio, baseURL: "http://studio.test:1234/v1", model: "google/gemma-4")
        model.profiles = [profile]
        model.activeProfileID = profile.id
        #expect(model.contextWindow == 8192)
        #expect(!model.contextWindowIsKnown)

        StubProtocol.responses = ["/api/v0/models": #"{"data":[{"id":"google/gemma-4","max_context_length":131072,"loaded_context_length":32768}]}"#]
        model.contextWindowProbe = ContextWindowProbe(session: StubProtocol.session)
        model.refreshContextWindow()
        for _ in 0..<100 where !model.contextWindowIsKnown { try await Task.sleep(for: .milliseconds(20)) }
        #expect(model.contextWindow == 32768)

        profile.contextWindow = 4096
        model.profiles = [profile]
        #expect(model.contextWindow == 4096)
    }

    @Test func ollamaPrefersTheLoadedModelThenNumCtxThenItsMaximum() async {
        let probe = ContextWindowProbe(session: StubProtocol.session)
        let profile = MobileProviderProfile(name: "O", type: .ollama, baseURL: "http://ollama.test:11434", model: "qwen3:8b")

        StubProtocol.responses = ["/api/ps": #"{"models":[{"name":"qwen3:8b","context_length":16384}]}"#]
        #expect(await probe.contextWindow(profile: profile, apiKey: "") == 16384)

        StubProtocol.responses = [
            "/api/ps": #"{"models":[]}"#,
            "/api/show": #"{"parameters":"temperature 0.6\nnum_ctx 8192","model_info":{"qwen3.context_length":40960}}"#
        ]
        #expect(await probe.contextWindow(profile: profile, apiKey: "") == 8192)

        StubProtocol.responses = [
            "/api/ps": #"{"models":[]}"#,
            "/api/show": #"{"parameters":"temperature 0.6","model_info":{"qwen3.context_length":40960}}"#
        ]
        #expect(await probe.contextWindow(profile: profile, apiKey: "") == 40960)
    }

    @Test func openAICompatibleReadsModelListThenLlamaCppProps() async {
        let probe = ContextWindowProbe(session: StubProtocol.session)
        let profile = MobileProviderProfile(name: "V", type: .openAICompatible, baseURL: "http://vllm.test:8000/v1/", model: "llama")

        StubProtocol.responses = ["/v1/models": #"{"data":[{"id":"llama","max_model_len":65536}]}"#]
        #expect(await probe.contextWindow(profile: profile, apiKey: "") == 65536)

        StubProtocol.responses = [
            "/v1/models": #"{"data":[{"id":"llama"}]}"#,
            "/props": #"{"default_generation_settings":{"n_ctx":12288}}"#
        ]
        #expect(await probe.contextWindow(profile: profile, apiKey: "") == 12288)

        StubProtocol.responses = [:]
        #expect(await probe.contextWindow(profile: profile, apiKey: "") == nil)
    }

    @Test func onlyTheConfiguredServerIsAskedWithItsOwnKey() async {
        let probe = ContextWindowProbe(session: StubProtocol.session)
        let profile = MobileProviderProfile(name: "V", type: .openAICompatible, baseURL: "http://vllm.test:8000/v1", model: "llama")
        StubProtocol.responses = ["/v1/models": #"{"data":[{"id":"llama","context_length":2048}]}"#]
        StubProtocol.requests = []
        _ = await probe.contextWindow(profile: profile, apiKey: "test-key")
        #expect(!StubProtocol.requests.isEmpty)
        #expect(StubProtocol.requests.allSatisfy { $0.url?.host == "vllm.test" })
        #expect(StubProtocol.requests.allSatisfy { $0.value(forHTTPHeaderField: "Authorization") == "Bearer test-key" })
    }
}

/// Serves canned JSON by URL path; anything else is a 404.
private final class StubProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var responses: [String: String] = [:]
    nonisolated(unsafe) static var requests: [URLRequest] = []

    static let session: URLSession = {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [StubProtocol.self]
        return URLSession(configuration: configuration)
    }()

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.requests.append(request)
        let path = request.url?.path ?? ""
        let body = Self.responses[path]
        let response = HTTPURLResponse(url: request.url!, statusCode: body == nil ? 404 : 200, httpVersion: nil, headerFields: nil)!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data((body ?? "{}").utf8))
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}
}
