import Foundation
import Testing
@testable import LLMTUIGUI

/// The settings browser's data path: llmtui's schema (a fixture of
/// `llmtui config schema` output), reading missing keys as llmtui's defaults,
/// writing keys the file does not have yet, Reset, and never writing secrets.
/// Everything runs on config text in memory; the real config.yaml is never
/// read or written.
@MainActor
struct ConfigSchemaTests {
    private func fixtureSchema() throws -> LLMTUIConfigSchema {
        let url = try #require(Bundle(for: FixtureAnchor.self).url(forResource: "llmtui-config-schema", withExtension: "json"))
        return try LLMTUIConfigSchema.decode(Data(contentsOf: url))
    }

    private func store() throws -> LLMTUIConfigurationStore {
        var store = LLMTUIConfigurationStore()
        store.schema = try fixtureSchema()
        return store
    }

    private let source = """
    default_provider: lmstudio
    providers:
      lmstudio:
        type: openai_compatible
        base_url: "http://localhost:1234/v1"
        default_model: "local-model"
        api_key: "sk-original-secret"
    chat:
      temperature: 0.7
    mcp:
      enabled: false
      servers:
        files:
          command: npx
          env:
            TOKEN: "mcp-original-secret"
    """

    @Test func decodesTypesDefaultsAndSecrets() throws {
        let schema = try fixtureSchema()
        #expect(schema.fields.count > 200)
        #expect(schema.field(for: "chat.max_tokens")?.kind == .int)
        #expect(schema.defaultValue(for: "chat.max_tokens") == "4096")
        #expect(schema.defaultValue(for: "chat.temperature") == "0.7")
        #expect(schema.defaultValue(for: "chat.stream") == "true")
        #expect(schema.field(for: "cache.ttl")?.kind == .duration)
        #expect(schema.field(for: "rag.workspace.include")?.listDefault.contains("**/*.go") == true)
        #expect(schema.field(for: "tools.approve")?.allowedValues == ["ask", "auto"])
        #expect(schema.field(for: "providers.lmstudio.type")?.key == "providers.*.type")
        #expect(schema.isSecret("providers.lmstudio.api_key"))
        #expect(schema.isSecret("mcp.servers.files.env.TOKEN"))
        #expect(!schema.isSecret("providers.lmstudio.api_key_env"))
    }

    @Test func thisAppsFieldDefaultsMatchLLMTUI() throws {
        // A key missing from config.yaml must read the same in this app's
        // fields as llmtui uses, or Save would write a value nobody chose.
        let schema = try fixtureSchema()
        let parsed = try store().parse("")
        let mine = LLMTUIConfiguration()
        #expect(parsed.systemPrompt == schema.defaultValue(for: "chat.system_prompt"))
        #expect(mine.systemPrompt == schema.defaultValue(for: "chat.system_prompt"))
        #expect(String(mine.maxTokens) == schema.defaultValue(for: "chat.max_tokens"))
        #expect(String(mine.maxToolIterations) == schema.defaultValue(for: "tools.max_iterations"))
        #expect(mine.approvalPolicy.rawValue == schema.defaultValue(for: "tools.approve"))
        #expect(String(mine.agent.maxCycles) == schema.defaultValue(for: "agent.max_cycles"))
        #expect(mine.agent.maxElapsed == schema.defaultValue(for: "agent.max_elapsed"))
        #expect(String(mine.entities.maxSessionEntities) == schema.defaultValue(for: "entities.max_session_entities"))
    }

    @Test func missingKeysReadAsDefaultsButAreNotWrittenBack() throws {
        let store = try store()
        let configuration = store.parse(source)
        #expect(configuration.rawSettings["chat.max_tokens"] == nil)
        #expect(configuration.maxTokens == 4096)

        let saved = store.patch(source, with: configuration)
        #expect(!saved.contains("max_tokens"))
        #expect(!saved.contains("agent:"))
    }

    @Test func changedSettingsMissingFromTheFileAreWritten() throws {
        let store = try store()
        var configuration = store.parse(source)
        configuration.agent.enabled = true                       // field-backed, no agent: section yet
        configuration.rawSettings["chat.save_history"] = "false"  // existing chat: section
        configuration.rawSettings["tools.web.timeout"] = "45s"    // new nested section
        configuration.listSettings["rag.workspace.exclude"] = ["vendor/**"]

        let saved = store.patch(source, with: configuration)
        let reparsed = store.parse(saved)
        #expect(reparsed.agent.enabled)
        #expect(reparsed.rawSettings["chat.save_history"] == "false")
        #expect(reparsed.rawSettings["tools.web.timeout"] == "45s")
        #expect(reparsed.listSettings["rag.workspace.exclude"] == ["vendor/**"])
        #expect(LLMTUIConfigurationStore.validate(saved).isEmpty)
    }

    @Test func resetRemovesASettingFromTheFile() throws {
        let store = try store()
        var configuration = store.parse(source + "\nui:\n  theme: forest\n")
        configuration.rawSettings.removeValue(forKey: "ui.theme")
        configuration.resetSettings.insert("ui.theme")
        let saved = store.patch(source + "\nui:\n  theme: forest\n", with: configuration)
        #expect(!saved.contains("theme"))
    }

    @Test func secretsAreNeverWritten() throws {
        let store = try store()
        var configuration = store.parse(source)
        configuration.rawSettings["providers.lmstudio.api_key"] = "sk-changed"
        configuration.rawSettings["mcp.servers.files.env.TOKEN"] = "changed"
        configuration.rawSettings["providers.lmstudio.extra.api_key"] = "sk-new"

        let saved = store.patch(source, with: configuration)
        #expect(saved.contains("sk-original-secret"))
        #expect(saved.contains("mcp-original-secret"))
        #expect(!saved.contains("sk-changed"))
        #expect(!saved.contains("sk-new"))
        #expect(!saved.contains("TOKEN: \"changed\""))
    }

    @Test func settingASecretFromTheAppIsRefused() throws {
        let model = AppModel(nativeAgentEnabled: false)
        model.configSchema = try fixtureSchema()
        model.setSetting("providers.lmstudio.api_key", to: "sk-typed")
        model.setSetting("mcp.servers.files.env.TOKEN", to: "typed")
        #expect(model.configuration.rawSettings["providers.lmstudio.api_key"] == nil)
        #expect(model.configuration.rawSettings["mcp.servers.files.env.TOKEN"] == nil)
    }

    @Test func aNewMCPServerStartsDisabledAndAsking() throws {
        let store = try store()
        let model = AppModel(nativeAgentEnabled: false)
        model.configSchema = try fixtureSchema()
        model.configuration = store.parse(source)
        #expect(model.addCollectionEntry("mcp.servers", name: "Search"))
        #expect(!model.addCollectionEntry("mcp.servers", name: "search"))
        #expect(!model.addCollectionEntry("mcp.servers", name: "bad name"))

        let saved = store.patch(source, with: model.configuration)
        let reparsed = store.parse(saved)
        #expect(reparsed.rawSettings["mcp.servers.search.enabled"] == "false")
        #expect(reparsed.rawSettings["mcp.servers.search.approve"] == "ask")
        #expect(reparsed.rawSettings["mcp.servers.files.command"] == "npx")
        #expect(LLMTUIConfigurationStore.validate(saved).isEmpty)
    }

    @Test func aWholeNewSectionIsAppended() throws {
        let store = try store()
        var configuration = store.parse("default_provider: lmstudio\n")
        configuration.rawSettings["network.retry.max_attempts"] = "5"
        let saved = store.patch("default_provider: lmstudio\n", with: configuration)
        #expect(store.parse(saved).rawSettings["network.retry.max_attempts"] == "5")
    }
}

private final class FixtureAnchor {}
