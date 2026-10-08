import Foundation
import CryptoKit
import Testing
@testable import LLMTUIiOS

nonisolated final class MCPWireFixture: @unchecked Sendable {
    static let shared = MCPWireFixture()
    private let lock = NSLock()
    private var payloads: [Data] = []
    private var captured: [URLRequest] = []
    func set(_ text: String) { lock.lock(); defer { lock.unlock() }; payloads = [Data(text.utf8)]; captured = [] }
    func set(_ texts: [String]) { lock.lock(); defer { lock.unlock() }; payloads = texts.map { Data($0.utf8) }; captured = [] }
    func requests() -> [URLRequest] { lock.lock(); defer { lock.unlock() }; return captured }
    func response(_ request: URLRequest) -> Data { lock.lock(); defer { lock.unlock() }; captured.append(request); return payloads.count > 1 ? payloads.removeFirst() : payloads.first ?? Data() }
}
nonisolated final class MCPStreamURLProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let data = MCPWireFixture.shared.response(request)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "text/event-stream"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

@Suite(.serialized) struct MCPProviderCompletionTests {
    private func turn(_ text: String) async throws -> ChatTurnResult {
        MCPWireFixture.shared.set(text)
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [MCPStreamURLProtocol.self]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        return try await MobileChatRuntime(session: session).streamTurn(profile: MobileProviderProfile(baseURL: "https://stream.fixture", model: "test"), apiKey: "", messages: [], tools: [], reasoning: .automatic) { _ in }
    }
    @Test func completeGenericStopAndToolCallsAreAccepted() async throws {
        for reason in ["stop", "tool_calls"] {
            let text = #"data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"mcp_tool","arguments":"{}"}}]},"finish_reason":"REASON"}]}"#.replacingOccurrences(of: "REASON", with: reason) + "\n\ndata: [DONE]\n\n"
            #expect(try await turn(text).toolCalls.count == 1)
        }
    }
    @Test func truncatedInterruptedAndMalformedCallsNeverReturnTools() async throws {
        let prefix = #"data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"mcp_tool","arguments":"{}"}}]}}]}"# + "\n\n"
        let length = prefix + #"data: {"choices":[{"delta":{},"finish_reason":"length"}]}"# + "\n\ndata: [DONE]\n\n"
        let malformed = #"data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"mcp_tool","arguments":"{"}}]},"finish_reason":"tool_calls"}]}"# + "\n\ndata: [DONE]\n\n"
        for text in [prefix, prefix + "data: [DONE]\n\n", length, malformed] {
            await #expect(throws: MobileChatError.self) { try await turn(text) }
        }
    }
}

actor MCPOAuthFakeHTTP: MCPHTTPFetching {
    var requests: [URLRequest] = []
    var mismatchIssuer = false
    var registrationFails = false
    nonisolated func invalidate() {}
    func changeIssuer() { mismatchIssuer = true }
    func failRegistration() { registrationFails = true }
    func fetch(_ request: URLRequest) async throws -> MCPHTTPResponse {
        requests.append(request)
        let path = request.url?.path ?? ""
        if path == "/mcp" { return MCPHTTPResponse(status: 401, headers: ["www-authenticate": "Bearer resource_metadata=\"https://server.example/resource\", scope=\"read\""], data: Data()) }
        let object: MCPJSON
        if path == "/resource" {
            object = .object(["resource": .string("https://server.example/mcp"), "authorization_servers": .array([.string("https://auth.example")])])
        } else if path.contains("well-known") {
            object = .object(["issuer": .string(mismatchIssuer ? "https://other.example" : "https://auth.example"),
                              "authorization_endpoint": .string("https://auth.example/authorize"), "token_endpoint": .string("https://auth.example/token"),
                              "registration_endpoint": .string("https://auth.example/register"), "code_challenge_methods_supported": .array([.string("S256")]),
                              "authorization_response_iss_parameter_supported": .bool(true)])
        } else if path == "/register" {
            if registrationFails { return MCPHTTPResponse(status: 400, headers: [:], data: Data()) }
            object = .object(["client_id": .string("registered-client")])
        } else if path == "/token" {
            object = .object(["access_token": .string("test-secret-access"), "token_type": .string("Bearer"), "refresh_token": .string("test-secret-refresh"), "expires_in": .number(3600)])
        } else { throw MobileMCPError.message("Unexpected OAuth fixture endpoint.") }
        return MCPHTTPResponse(status: 200, headers: ["content-type": "application/json"], data: try object.data())
    }
}
struct MCPOAuthFlowTests {
    @Test func discoveryRegistrationPKCERefreshAndIssuerBinding() async throws {
        let http = MCPOAuthFakeHTTP()
        var profile = MobileMCPProfile(); profile.endpoint = "https://server.example/mcp"; profile.authentication = .oauth
        let client = MCPOAuth(http: http)
        let secret = try await client.authorize(profile, previousScopes: "write") { url in
            let items = URLComponents(url: url, resolvingAgainstBaseURL: false)!.queryItems!
            let values = Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value ?? "") })
            #expect(values["scope"] == "read write")
            #expect(values["code_challenge_method"] == "S256")
            #expect(values["resource"] == "https://server.example/mcp")
            #expect(values["client_id"] == "registered-client")
            return URL(string: MCPOAuth.redirect + "?code=accepted&state=" + values["state"]! + "&iss=https%3A%2F%2Fauth.example")!
        }
        #expect(secret.accessToken == "test-secret-access")
        #expect(secret.issuer == "https://auth.example")
        let requests = await http.requests
        let tokenRequest = try #require(requests.first(where: { $0.url?.path == "/token" }))
        let fields = URLComponents(string: "https://test/?" + String(decoding: tokenRequest.httpBody!, as: UTF8.self))!.queryItems!
        let verifier = try #require(fields.first(where: { $0.name == "code_verifier" })?.value)
        #expect(verifier.count >= 43)
        let leakedCredential = requests.filter { $0.url?.path != "/token" }.contains(where: { $0.value(forHTTPHeaderField: "Authorization") != nil })
        #expect(!leakedCredential)
        #expect(try await client.refresh(secret, profile: profile).refreshToken == "test-secret-refresh")
        await http.changeIssuer()
        await #expect(throws: MobileMCPError.self) { try await client.refresh(secret, profile: profile) }
        #expect((await http.requests).filter { $0.url?.path == "/token" }.count == 2)
    }
    @Test func registrationFailureNeverOpensBrowserOrExchangesCode() async throws {
        let http = MCPOAuthFakeHTTP(); await http.failRegistration()
        var profile = MobileMCPProfile(); profile.endpoint = "https://server.example/mcp"; profile.authentication = .oauth
        await #expect(throws: MobileMCPError.self) {
            try await MCPOAuth(http: http).authorize(profile) { _ in Issue.record("Browser opened after registration failed"); throw CancellationError() }
        }
        #expect(!(await http.requests).contains(where: { $0.url?.path == "/token" }))
    }
    @MainActor @Test func credentialsAreSeparateFromProfileAndConversationPersistence() throws {
        let id = UUID(), store = MCPMemoryCredentials()
        defer { try? store.remove(id) }
        let credential = MCPCredentials(accessToken: "fixture-secret")
        try store.save(credential, id: id)
        #expect(try store.load(id).accessToken == "fixture-secret")
        var profile = MobileMCPProfile(); profile.id = id
        #expect(!String(decoding: try JSONEncoder().encode(profile), as: UTF8.self).contains("fixture-secret"))
        var conversation = MobileConversation(profileID: nil); conversation.mcpServerIDs = [id]
        #expect(try JSONDecoder().decode(MobileConversation.self, from: JSONEncoder().encode(conversation)).mcpServerIDs == [id])
        #expect(!String(decoding: try JSONEncoder().encode(conversation), as: UTF8.self).contains("fixture-secret"))
    }
}

nonisolated final class MCPMemoryCredentials: MCPCredentialStoring, @unchecked Sendable {
    private let lock = NSLock()
    private var values: [UUID: MCPCredentials] = [:]
    func load(_ id: UUID) throws -> MCPCredentials { lock.lock(); defer { lock.unlock() }; return values[id] ?? MCPCredentials() }
    func save(_ value: MCPCredentials, id: UUID) throws { lock.lock(); defer { lock.unlock() }; values[id] = value }
    func remove(_ id: UUID) throws { lock.lock(); defer { lock.unlock() }; values[id] = nil }
}

struct MCPDeviceKeychainTests {
    @Test(.enabled(if: ProcessInfo.processInfo.environment["LLMTUI_IOS_KEYCHAIN_TEST"] == "1", "Requires a signed test host with Keychain access"))
    func signedHostKeychainRoundTrip() throws {
        let id = UUID(), store = MCPCredentialStore()
        defer { try? store.remove(id) }
        try store.save(MCPCredentials(accessToken: "test-only-keychain-value"), id: id)
        #expect(try store.load(id).accessToken == "test-only-keychain-value")
        try store.remove(id)
        #expect(try store.load(id).accessToken.isEmpty)
    }
}

@MainActor struct MCPResultPrivacyTests {
    @Test func credentialsAndRemoteImagesRespectTrustBoundary() throws {
        let secret = "secret-bearer-value"
        let raw = MCPJSON.object(["content": .array([.object(["type": .string("text"), "text": .string("echo " + secret)])])])
        #expect(!(try MobileMCPResult(content: raw.redacting(secret)).modelText()).contains(secret))
        let result = MobileMCPResult(content: .object(["content": .array([.object(["type": .string("text"), "text": .string(String(repeating: "世", count: 40_000))])])]))
        #expect(try result.modelText().utf8.count <= MobileMCPLimits.result)
        let activity = MobileToolActivity(name: "Server / lookup", detail: "MCP", status: .completed, externalMCP: true)
        #expect(RemoteImagePolicy.readsUntrustedContent([activity]))
        let encoded = try JSONEncoder().encode(activity)
        #expect(try JSONDecoder().decode(MobileToolActivity.self, from: encoded).externalMCP == true)
    }
}
