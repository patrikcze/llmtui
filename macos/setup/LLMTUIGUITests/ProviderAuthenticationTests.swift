import Foundation
import Testing
@testable import LLMTUIGUI

@MainActor
struct ProviderAuthenticationTests {
    @Test func credentialResolution() throws {
        var provider = ProviderConfiguration()
        provider.apiKeyEnvironment = "TEST_PROVIDER_KEY"
        for value in ["secret", "Bearer secret", "bearer secret", " secret \n"] {
            var request = URLRequest(url: URL(string: "https://provider.invalid")!)
            try ProviderAuthentication.apply(to: &request, provider: provider, environment: ["TEST_PROVIDER_KEY": value])
            #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer secret")
        }
        for environment in [[:], ["TEST_PROVIDER_KEY": ""], ["TEST_PROVIDER_KEY": "  "], ["WRONG_NAME": "secret"]] {
            var request = URLRequest(url: URL(string: "https://provider.invalid")!)
            #expect(throws: ProviderAuthentication.MissingCredential.self) {
                try ProviderAuthentication.apply(to: &request, provider: provider, environment: environment)
            }
        }
        provider.apiKey = "explicit"
        var request = URLRequest(url: URL(string: "https://provider.invalid")!)
        try ProviderAuthentication.apply(to: &request, provider: provider, environment: [:])
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer explicit")
        try ProviderAuthentication.apply(to: &request, provider: provider, environment: ["TEST_PROVIDER_KEY": "environment"])
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer environment")
    }

    @Test func missingCredentialFailsBeforeDiscoveryOrChat() async {
        var config = LLMTUIConfiguration()
        config.provider.baseURL = "https://provider.invalid/v1"
        config.provider.apiKeyEnvironment = "LLMTUI_MISSING_" + UUID().uuidString
        do {
            _ = try await ProviderModelDiscovery.fetchModels(configuration: config)
            Issue.record("Expected a local missing credential error")
        } catch {
            #expect(error is ProviderAuthentication.MissingCredential)
        }
        do {
            _ = try await OpenAIRequest.send(configuration: config, messages: [], tools: [], onText: { _ in })
            Issue.record("Expected a local missing credential error")
        } catch {
            #expect(error is ProviderAuthentication.MissingCredential)
        }
    }

    @Test func yamlAndProviderSwitching() {
        let config = LLMTUIConfigurationStore().parse("""
        default_provider: remote
        providers:
          remote:
            type: openai_compatible
            api_key_env: TEST_PROVIDER_KEY
            api_key: explicit
        """)
        #expect(config.provider.apiKeyEnvironment == "TEST_PROVIDER_KEY")
        #expect(config.provider.apiKey == "explicit")
        #expect(ProviderConfiguration(profile: config.providers[0]).apiKey == "explicit")
    }

    @Test func authenticatedRequestsAndCredentialFreeProviders() async throws {
        let settings = URLSessionConfiguration.ephemeral
        settings.protocolClasses = [AuthenticationMockProtocol.self]
        let session = URLSession(configuration: settings)
        defer { session.invalidateAndCancel() }
        var config = LLMTUIConfiguration()
        config.provider.baseURL = "https://provider.invalid/authenticated/v1"
        let variable = "LLMTUI_AUTH_TEST_" + UUID().uuidString.replacingOccurrences(of: "-", with: "_")
        setenv(variable, "test-secret", 1)
        defer { unsetenv(variable) }
        config.provider.apiKeyEnvironment = variable
        #expect(try await ProviderModelDiscovery.fetchModels(configuration: config, session: session) == ["test-model"])
        let response = try await OpenAIRequest.send(configuration: config, messages: [], tools: [], session: session, onText: { _ in })
        #expect(response.content == "hello")
        let tool = ToolDefinition(function: .init(name: "test_tool", description: "Test", parameters: .object([:])), safety: .readOnly)
        _ = try await OpenAIRequest.send(configuration: config, messages: [], tools: [tool], session: session, onText: { _ in })
        for type in [ProviderType.ollama, .openAICompatible] {
            config.provider.type = type
            config.provider.apiKeyEnvironment = ""
            config.provider.baseURL = "https://provider.invalid/v1"
            #expect(try await ProviderModelDiscovery.fetchModels(configuration: config, session: session) == ["test-model"])
        }
    }
}

private final class AuthenticationMockProtocol: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "provider.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let chat = request.url!.path.hasSuffix("/chat/completions")
        let authorization = request.value(forHTTPHeaderField: "Authorization")
        #expect(authorization == nil || authorization == "Bearer test-secret")
        if request.url!.path.contains("/authenticated/") {
            #expect(authorization == "Bearer test-secret")
        } else {
            #expect(authorization == nil)
        }
        let body = chat ? "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\ndata: [DONE]\n\n" : "{\"data\":[{\"id\":\"test-model\"}],\"models\":[{\"name\":\"test-model\"}]}"
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: ["Content-Type": chat ? "text/event-stream" : "application/json"])!, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
