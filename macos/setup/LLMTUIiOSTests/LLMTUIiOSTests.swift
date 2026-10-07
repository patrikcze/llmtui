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
}
