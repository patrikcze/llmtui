import Foundation
import Testing
@testable import LLMTUIGUI

/// The reported bug: the app saved `api_key_env` but never sent the key, so
/// an authenticated OpenAI-compatible server answered 401 to chat and model
/// discovery.
struct ProviderAPIKeyTests {
    @Test func resolvesFromTheAppEnvironmentFirst() {
        var asked: [String] = []
        let key = ProviderAPIKey.resolve(
            environmentName: "BRNOLAB_LLAMA_API_KEY",
            environment: ["BRNOLAB_LLAMA_API_KEY": "secret-from-env\n"],
            launchd: { asked.append($0); return "from-launchd" }
        )
        #expect(key == "secret-from-env")
        #expect(asked.isEmpty)
    }

    /// An app opened from Finder or the Dock does not see shell exports;
    /// `launchctl setenv` values are read from launchd instead.
    @Test func fallsBackToLaunchd() {
        let key = ProviderAPIKey.resolve(
            environmentName: " BRNOLAB_LLAMA_API_KEY ",
            environment: ["BRNOLAB_LLAMA_API_KEY": ""],
            launchd: { $0 == "BRNOLAB_LLAMA_API_KEY" ? "from-launchd" : nil }
        )
        #expect(key == "from-launchd")
    }

    @Test func unsetEmptyOrInvalidNamesResolveToNothing() {
        var asked: [String] = []
        let launchd: (String) -> String? = { asked.append($0); return nil }
        #expect(ProviderAPIKey.resolve(environmentName: "", environment: [:], launchd: launchd) == nil)
        #expect(ProviderAPIKey.resolve(environmentName: "MISSING_KEY", environment: [:], launchd: launchd) == nil)
        // A name that is not a plain variable name is never passed to launchctl.
        for name in ["1KEY", "KEY;rm -rf", "$KEY", "KEY NAME", "-h"] {
            #expect(ProviderAPIKey.resolve(environmentName: name, environment: [name: "x"], launchd: launchd) == nil)
        }
        #expect(asked == ["MISSING_KEY"])
    }

    @Test func authorizeAddsABearerHeaderOnlyWithAKey() throws {
        var request = URLRequest(url: try #require(URL(string: "http://10.70.38.18:8000/v1/models")))
        ProviderAPIKey.authorize(&request, apiKey: nil)
        #expect(request.value(forHTTPHeaderField: "Authorization") == nil)
        ProviderAPIKey.authorize(&request, apiKey: "secret")
        #expect(request.value(forHTTPHeaderField: "Authorization") == "Bearer secret")
    }
}
