import Foundation
import Testing
@testable import LLMTUIGUI

/// Provider profiles: deleting them, valid names, and keeping the Chat's
/// provider separate from llmtui's `default_provider`. Config text is parsed
/// and patched in memory; nothing here reads or writes the real config.yaml.
@MainActor
struct ProviderProfileTests {
    private let store = LLMTUIConfigurationStore()

    private let source = """
    default_provider: lmstudio
    default_model: "local-model"
    providers:
      lmstudio:
        type: openai_compatible
        base_url: "http://localhost:1234/v1"
        default_model: "local-model"
      mlxserve:
        type: openai_compatible
        base_url: "http://localhost:8090/v1"
        default_model: "mlx-model"
        timeout: 30s
    chat:
      temperature: 0.7
    """

    private func model(_ text: String) -> (AppModel, UserDefaults, String) {
        let suite = "ProviderProfileTests.\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suite)!
        let model = AppModel(nativeAgentEnabled: false)
        model.preferences = defaults
        model.configuration = store.parse(text)
        return (model, defaults, suite)
    }

    @Test func deletingAProfileRemovesItsWholeBlock() {
        let (model, defaults, suite) = model(source)
        defer { defaults.removePersistentDomain(forName: suite) }
        model.selectProvider("mlxserve")
        #expect(model.deleteProviderProfile("mlxserve"))
        #expect(model.configuration.provider.name == "lmstudio")

        let saved = store.patch(source, with: model.configuration)
        #expect(!saved.contains("mlxserve"))
        #expect(!saved.contains("timeout"))
        #expect(saved.contains("lmstudio:"))
        #expect(saved.contains("temperature: 0.7"))
        #expect(store.parse(saved).providers.map(\.name) == ["lmstudio"])
    }

    @Test func deleteAndAddInOneSaveIsNotARename() {
        let (model, defaults, suite) = model(source)
        defer { defaults.removePersistentDomain(forName: suite) }
        model.deleteProviderProfile("mlxserve")
        model.createProviderProfile()

        let saved = store.patch(source, with: model.configuration)
        // The deleted block's extra fields must not move to the new profile.
        #expect(!saved.contains("mlxserve"))
        #expect(!saved.contains("timeout"))
        #expect(saved.contains("new_provider:"))
    }

    @Test func deletingLLMTUIDefaultMovesTheDefault() {
        let (model, defaults, suite) = model(source)
        defer { defaults.removePersistentDomain(forName: suite) }
        model.selectProvider("mlxserve")
        model.deleteProviderProfile("lmstudio")
        #expect(model.configuration.defaultProviderName == "mlxserve")

        let reparsed = store.parse(store.patch(source, with: model.configuration))
        #expect(reparsed.defaultProviderName == "mlxserve")
        #expect(reparsed.defaultProviderIsDefined)
    }

    @Test func theLastProfileCannotBeDeleted() {
        let (model, defaults, suite) = model("default_provider: solo\nproviders:\n  solo:\n    type: ollama\n")
        defer { defaults.removePersistentDomain(forName: suite) }
        #expect(!model.canDeleteProviderProfile("solo"))
        #expect(!model.deleteProviderProfile("solo"))
        #expect(model.configuration.providers.count == 1)
    }

    @Test func choosingTheChatProviderDoesNotChangeLLMTUIDefault() {
        let (model, defaults, suite) = model(source)
        defer { defaults.removePersistentDomain(forName: suite) }
        model.selectProvider("mlxserve")
        #expect(model.configuration.provider.name == "mlxserve")

        let saved = store.patch(source, with: model.configuration)
        #expect(saved.contains("default_provider: lmstudio") || saved.contains("default_provider: \"lmstudio\""))
        #expect(store.parse(saved).defaultProviderName == "lmstudio")

        model.makeSelectedProviderLLMTUIDefault()
        let reparsed = store.parse(store.patch(source, with: model.configuration))
        #expect(reparsed.defaultProviderName == "mlxserve")
        #expect(reparsed.provider.model == "mlx-model")
    }

    @Test func switchingProfilesKeepsUnsavedEdits() {
        let (model, defaults, suite) = model(source)
        defer { defaults.removePersistentDomain(forName: suite) }
        model.configuration.provider.baseURL = "http://localhost:9999/v1"
        model.selectProvider("mlxserve")
        model.selectProvider("lmstudio")
        #expect(model.configuration.provider.baseURL == "http://localhost:9999/v1")
    }

    @Test func providerNamesAreLowercasedAndRestricted() {
        #expect(LLMTUIConfiguration.normalizedProviderName(" MLXServe ") == "mlxserve")
        #expect(LLMTUIConfiguration.normalizedProviderName("my-server_2") == "my-server_2")
        #expect(LLMTUIConfiguration.normalizedProviderName("a.b") == nil)
        #expect(LLMTUIConfiguration.normalizedProviderName("has space") == nil)
        #expect(LLMTUIConfiguration.normalizedProviderName("") == nil)

        let (model, defaults, suite) = model(source)
        defer { defaults.removePersistentDomain(forName: suite) }
        #expect(model.renameActiveProvider(to: "LMStudio2"))
        #expect(model.configuration.provider.name == "lmstudio2")
        #expect(model.configuration.defaultProviderName == "lmstudio2")
        #expect(!model.renameActiveProvider(to: "MLXSERVE"))
    }

    @Test func defaultProviderResolvesLikeLLMTUI() {
        // Different case: llmtui lowercases provider keys.
        let upper = store.parse("default_provider: \"MLXSERVE\"\nproviders:\n  mlxserve:\n    type: openai_compatible\n    base_url: \"http://localhost:8090/v1\"\n")
        #expect(upper.defaultProviderName == "mlxserve")
        #expect(upper.defaultProviderIsDefined)

        // Unset: llmtui starts with its built-in ollama.
        let unset = store.parse("chat:\n  temperature: 0.5\n")
        #expect(unset.defaultProviderName == "ollama")
        #expect(unset.provider.type == .ollama)
        #expect(unset.provider.baseURL == "http://localhost:11434")

        // Missing: flagged, and the Chat falls back to a defined profile.
        let missing = store.parse("default_provider: gone\nproviders:\n  lmstudio:\n    type: openai_compatible\n")
        #expect(!missing.defaultProviderIsDefined)
        #expect(missing.provider.name == "lmstudio")
    }

    @Test func theChatProviderChoiceIsRemembered() {
        let (model, defaults, suite) = model(source)
        defer { defaults.removePersistentDomain(forName: suite) }
        model.selectProvider("mlxserve")
        #expect(defaults.string(forKey: AppModel.chatProviderKey) == "mlxserve")
    }
}
