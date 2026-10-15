import Foundation
import Testing
@testable import LLMTUIGUI

/// Inline YAML collections (`[]`, `[a, b]`, `{}`) are lists and maps, not
/// scalars. Saving once read `stop: []` as the scalar "[]" and wrote it back
/// as the quoted string `"[]"`, which llmtui then loaded as a one-item list
/// holding the stop string "[]" — every embedded reply containing an empty
/// JSON array was cut short there. Config text is parsed and patched in
/// memory; nothing here reads or writes the real config.yaml.
@MainActor
struct InlineYAMLCollectionTests {
    private let store = LLMTUIConfigurationStore()

    private let source = """
    providers:
      embedded:
        type: "embedded"
        model_path: "/models/model.gguf"
        sampling:
          top_k: 40
          stop: []
    skills:
      enabled: true
      paths: []
    plugins:
      enabled: []
    mcp:
      enabled: false
      servers: {}
    chat:
      temperature: 0.7
    """

    @Test func savingKeepsInlineEmptyCollectionsUnquoted() {
        let configuration = store.parse(source)
        let saved = store.patch(source, with: configuration)

        #expect(saved.contains("      stop: []\n"))
        #expect(saved.contains("  paths: []\n"))
        #expect(saved.contains("  enabled: []\n"))
        #expect(saved.contains("  servers: {}\n"))
        #expect(!saved.contains("\"[]\""))
        #expect(!saved.contains("\"{}\""))
    }

    @Test func inlineListsAreListsNotScalars() {
        let text = """
        providers:
          embedded:
            type: "embedded"
            sampling:
              stop: ["</s>", "<eos>"]
        skills:
          paths: []
        """
        let configuration = store.parse(text)

        #expect(configuration.rawSettings["providers.embedded.sampling.stop"] == nil)
        #expect(configuration.listSettings["providers.embedded.sampling.stop"] == ["</s>", "<eos>"])
        #expect(configuration.rawSettings["skills.paths"] == nil)
        #expect(configuration.listSettings["skills.paths"] == [])
    }

    @Test func unchangedInlineListKeepsItsForm() {
        let text = """
        providers:
          embedded:
            type: "embedded"
            sampling:
              stop: ["</s>", "a, b"] # model-specific
        """
        let configuration = store.parse(text)
        #expect(configuration.listSettings["providers.embedded.sampling.stop"] == ["</s>", "a, b"])

        let saved = store.patch(text, with: configuration)
        #expect(saved.contains(#"stop: ["</s>", "a, b"] # model-specific"#))
    }

    @Test func editingAnInlineListWritesARealList() {
        var configuration = store.parse(source)
        configuration.listSettings["providers.embedded.sampling.stop"] = ["</s>"]
        let saved = store.patch(source, with: configuration)

        #expect(saved.contains("      stop:\n        - \"</s>\"\n"))
        #expect(store.parse(saved).listSettings["providers.embedded.sampling.stop"] == ["</s>"])
    }
}
