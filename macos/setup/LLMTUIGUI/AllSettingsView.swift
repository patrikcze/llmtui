import SwiftUI

/// Every llmtui setting from llmtui's own schema (`llmtui config schema`),
/// grouped by section, with a control for each type: switches for booleans,
/// menus for fixed choices, checked fields for numbers and durations, and
/// one-per-line editors for lists.
///
/// A setting shows llmtui's default until it is changed; Save writes only
/// changed settings, and Reset removes one from config.yaml again. Secrets
/// (provider API keys, MCP server environment values) are never shown.
/// Settings with their own screen (providers, model profiles, Chat, Tools,
/// Agent, Personal Apps) are left to that screen.
struct AllSettingsSections: View {
    let model: AppModel
    let schema: LLMTUIConfigSchema
    @State private var search = ""

    /// Keys edited on another screen, besides the field-backed ones.
    private static let ownedElsewhere: Set<String> = [
        "default_provider", "default_model",
        "chat.history_dir", "chat.save_history", "chat.strip_leaked_thinking", "chat.force_vision",
        "personal_apps.enabled", "personal_apps.mail.enabled", "personal_apps.mail.allowed_accounts",
        "personal_apps.calendar.enabled", "personal_apps.calendar.allowed_calendars",
        "personal_apps.calendar.helper_path", "personal_apps.mutations.enabled"
    ]

    /// Named collections this view can add entries to.
    private static let addableCollections: [String: String] = [
        "mcp.servers": "MCP server",
        "templates": "template"
    ]

    var body: some View {
        Section {
            TextField("Search settings", text: $search, prompt: Text("Search, e.g. timeout or rag"))
            if let check = model.llmtuiCheck {
                LLMTUICheckSummary(outcome: check)
            }
        } header: {
            Text("llmtui settings")
        } footer: {
            Text("\(schema.fields.count) settings from the llmtui found on this Mac. Unchanged settings use llmtui's default and are not written to config.yaml.")
        }

        ForEach(sections, id: \.self) { section in
            let fields = visibleFields(in: section)
            let collections = collectionPatterns(in: section)
            if !fields.isEmpty || !collections.isEmpty {
                Section {
                    ForEach(fields) { field in
                        SchemaSettingRow(model: model, field: field, key: field.key, label: label(for: field.key, in: section))
                    }
                    ForEach(collections, id: \.self) { collection in
                        SchemaCollectionEditor(
                            model: model,
                            schema: schema,
                            collection: collection,
                            noun: Self.addableCollections[collection],
                            search: search
                        )
                    }
                } header: {
                    HStack(spacing: 8) {
                        IconTile(systemName: Self.icon(for: section), tint: Theme.accent, size: 22)
                        Text(Self.title(for: section))
                    }
                }
            }
        }
    }

    private var sections: [String] {
        Array(Set(schema.fields.map(\.section))).sorted { Self.title(for: $0) < Self.title(for: $1) }
    }

    private func isOwnedElsewhere(_ key: String) -> Bool {
        Self.ownedElsewhere.contains(key)
            || model.configurationStore.fieldKeys.contains(key)
            || key.hasPrefix("providers.") || key.hasPrefix("model_profiles.")
    }

    private func matchesSearch(_ key: String) -> Bool {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        guard !query.isEmpty else { return true }
        return key.lowercased().contains(query)
            || key.replacingOccurrences(of: "_", with: " ").lowercased().contains(query)
    }

    private func visibleFields(in section: String) -> [LLMTUIConfigSchema.Field] {
        schema.fields.filter { field in
            field.section == section && !field.key.contains("*") && !field.isSecret
                && field.kind != .map && !isOwnedElsewhere(field.key) && matchesSearch(field.key)
        }
    }

    /// Named collections (`mcp.servers`, `templates`, …) in a section: the
    /// key part before the first "*".
    private func collectionPatterns(in section: String) -> [String] {
        let patterns = schema.fields
            .filter { $0.section == section && $0.key.contains(".*.") && !isOwnedElsewhere($0.key) }
            .compactMap { $0.key.components(separatedBy: ".*.").first }
        return Array(Set(patterns)).sorted()
    }

    private func label(for key: String, in section: String) -> String {
        let rest = key.hasPrefix(section + ".") ? String(key.dropFirst(section.count + 1)) : key
        return rest.split(separator: ".")
            .map { $0.replacingOccurrences(of: "_", with: " ").capitalized }
            .joined(separator: " › ")
    }

    static func title(for section: String) -> String {
        switch section {
        case "agent": "Agent"
        case "cache": "Response cache"
        case "chat": "Chat"
        case "context": "Context"
        case "decision_engine": "Decision engine"
        case "entities": "Entities"
        case "mcp": "MCP"
        case "memory": "Memory"
        case "network": "Network"
        case "personal_apps": "Personal Apps limits"
        case "plugins": "Plugins"
        case "privacy": "Privacy"
        case "prompt": "Prompt"
        case "rag": "RAG"
        case "skills": "Skills"
        case "templates": "Templates"
        case "tool_registry": "Tool registry"
        case "tools": "Tools"
        case "ui": "Terminal interface"
        default: section.replacingOccurrences(of: "_", with: " ").capitalized
        }
    }

    static func icon(for section: String) -> String {
        switch section {
        case "agent": "infinity"
        case "cache": "internaldrive"
        case "chat": "bubble.left.and.bubble.right"
        case "context": "text.line.first.and.arrowtriangle.forward"
        case "decision_engine": "arrow.triangle.branch"
        case "entities": "shippingbox"
        case "mcp": "puzzlepiece.extension"
        case "memory": "brain.head.profile"
        case "network": "network"
        case "personal_apps": "calendar"
        case "plugins": "powerplug"
        case "privacy": "hand.raised"
        case "prompt": "text.quote"
        case "rag": "doc.text.magnifyingglass"
        case "skills": "graduationcap"
        case "templates": "doc.on.doc"
        case "tool_registry": "server.rack"
        case "tools": "wrench.and.screwdriver"
        case "ui": "terminal"
        default: "gearshape"
        }
    }
}

/// The findings of `llmtui config validate` for the saved file.
struct LLMTUICheckSummary: View {
    let outcome: LLMTUIConfigCommands.Outcome

    var body: some View {
        switch outcome {
        case .checked(let problems) where problems.isEmpty:
            Label("llmtui checked config.yaml and found no problems.", systemImage: "checkmark.seal.fill")
                .foregroundStyle(Theme.success)
        case .checked(let problems):
            VStack(alignment: .leading, spacing: 6) {
                Label("llmtui found \(problems.count) \(problems.count == 1 ? "problem" : "problems") in config.yaml", systemImage: "exclamationmark.triangle.fill")
                    .foregroundStyle(problems.contains(where: \.isError) ? Theme.danger : Theme.warning)
                ForEach(problems) { problem in
                    Text("\(problem.isError ? "Error" : "Warning") · \(problem.key): \(problem.message)")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                }
            }
        case .failed(let message):
            Label("llmtui cannot load config.yaml: \(message)", systemImage: "xmark.octagon.fill")
                .foregroundStyle(Theme.danger)
        case .unavailable(let reason):
            Label(reason, systemImage: "questionmark.circle")
                .foregroundStyle(.secondary)
        }
    }
}

/// One setting: its control, its key, llmtui's default, and Reset when the
/// file sets it.
struct SchemaSettingRow: View {
    let model: AppModel
    let field: LLMTUIConfigSchema.Field
    /// The concrete key (differs from field.key inside a named collection).
    let key: String
    let label: String

    private var fileValue: String? { model.configuration.rawSettings[key] }
    private var fileList: [String]? { model.configuration.listSettings[key] }
    private var isSet: Bool { fileValue != nil || fileList != nil }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                control
                if isSet {
                    Button("Reset", systemImage: "arrow.uturn.backward") {
                        model.resetSetting(key)
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.borderless)
                    .help("Remove from config.yaml on Save, so llmtui's default applies")
                }
            }
            HStack(spacing: 6) {
                Text(key)
                    .font(.caption.monospaced())
                if let defaultText {
                    Text("· default \(defaultText)")
                        .font(.caption)
                }
                if let problem = inputProblem {
                    Text("· \(problem)")
                        .font(.caption)
                        .foregroundStyle(Theme.danger)
                }
            }
            .foregroundStyle(.secondary)
        }
    }

    @ViewBuilder
    private var control: some View {
        switch field.kind {
        case .bool:
            Toggle(label, isOn: Binding(
                get: { (fileValue ?? field.defaultValue) == "true" },
                set: { model.setSetting(key, to: $0 ? "true" : "false") }
            ))
        case .list:
            VStack(alignment: .leading, spacing: 4) {
                Text(label)
                TextField("One per line", text: Binding(
                    get: { (fileList ?? field.listDefault).joined(separator: "\n") },
                    set: { text in
                        let items = text.split(separator: "\n", omittingEmptySubsequences: true)
                            .map { $0.trimmingCharacters(in: .whitespaces) }
                            .filter { !$0.isEmpty }
                        model.setListSetting(key, to: items)
                    }
                ), axis: .vertical)
                .lineLimit(2...8)
                .font(.body.monospaced())
            }
        default:
            if !field.allowedValues.isEmpty {
                Picker(label, selection: Binding(
                    get: { fileValue ?? field.defaultValue ?? "" },
                    set: { model.setSetting(key, to: $0) }
                )) {
                    ForEach(pickerOptions, id: \.self) { option in
                        Text(option.isEmpty ? "(not set)" : option).tag(option)
                    }
                }
            } else {
                TextField(label, text: Binding(
                    get: { fileValue ?? "" },
                    set: { model.setSetting(key, to: $0) }
                ), prompt: Text(field.defaultValue ?? ""))
            }
        }
    }

    private var pickerOptions: [String] {
        let current = fileValue ?? field.defaultValue ?? ""
        var options = field.allowedValues
        if !options.contains(current) { options.insert(current, at: 0) }
        return options
    }

    private var defaultText: String? {
        if field.kind == .list {
            return field.listDefault.isEmpty ? nil : field.listDefault.joined(separator: ", ")
        }
        guard let value = field.defaultValue else { return nil }
        return value.count > 60 ? String(value.prefix(57)) + "…" : value
    }

    /// Why the typed value would not work, checked as you type. Save also
    /// has llmtui check the whole file.
    private var inputProblem: String? {
        guard let value = fileValue, !value.isEmpty else { return nil }
        switch field.kind {
        case .int: return Int(value) == nil ? "not a whole number" : nil
        case .float: return Double(value) == nil ? "not a number" : nil
        case .duration:
            return value.range(of: #"^(\d+(\.\d+)?(ns|us|µs|ms|s|m|h))+$"#, options: .regularExpression) == nil
                ? "not a duration such as 30s or 5m" : nil
        default:
            if !field.allowedValues.isEmpty, !field.allowedValues.contains(value) {
                return "not one of \(field.allowedValues.joined(separator: ", "))"
            }
            return nil
        }
    }
}

/// The entries of a named collection (MCP servers, templates, Laya models):
/// each existing entry with every field it can have, plus, for MCP servers
/// and templates, adding a new one.
struct SchemaCollectionEditor: View {
    let model: AppModel
    let schema: LLMTUIConfigSchema
    /// The key part before "*", e.g. "mcp.servers".
    let collection: String
    /// What one entry is called, when entries can be added here.
    let noun: String?
    let search: String
    @State private var newName = ""

    private var fields: [LLMTUIConfigSchema.Field] {
        schema.fields.filter { $0.key.hasPrefix(collection + ".*.") && !$0.isSecret && $0.kind != .map }
    }

    private var entries: [String] {
        let prefix = collection + "."
        let keys = Array(model.configuration.rawSettings.keys) + Array(model.configuration.listSettings.keys)
        let names = keys.compactMap { key -> String? in
            guard key.hasPrefix(prefix) else { return nil }
            return key.dropFirst(prefix.count).split(separator: ".").first.map(String.init)
        }
        return Array(Set(names)).sorted()
    }

    var body: some View {
        let query = search.trimmingCharacters(in: .whitespaces).lowercased()
        let shown = entries.filter { query.isEmpty || "\(collection).\($0)".lowercased().contains(query) || collection.contains(query) }
        ForEach(shown, id: \.self) { name in
            DisclosureGroup {
                ForEach(fields) { field in
                    let leaf = String(field.key.dropFirst(collection.count + 3))
                    SchemaSettingRow(
                        model: model,
                        field: field,
                        key: "\(collection).\(name).\(leaf)",
                        label: leaf.replacingOccurrences(of: "_", with: " ").capitalized
                    )
                }
                if hasSecretFields(name) {
                    Text("Environment values are secrets and are not shown here; edit them in config.yaml.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            } label: {
                Label("\(collectionTitle) \(name)", systemImage: "square.stack")
            }
        }
        if let noun, query.isEmpty {
            HStack {
                TextField("New \(noun) name", text: $newName)
                Button("Add \(noun)", systemImage: "plus") {
                    if model.addCollectionEntry(collection, name: newName) {
                        newName = ""
                    }
                }
                .disabled(LLMTUIConfiguration.normalizedProviderName(newName) == nil
                    || entries.contains(LLMTUIConfiguration.normalizedProviderName(newName) ?? ""))
            }
            if collection == "mcp.servers" {
                Text("A new MCP server is added disabled and asks before each tool call; enable it once its command is set. Declaring a server starts nothing until you connect it in llmtui.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var collectionTitle: String {
        switch collection {
        case "mcp.servers": "MCP server"
        case "templates": "Template"
        default: collection.split(separator: ".").last.map { $0.replacingOccurrences(of: "_", with: " ").capitalized } ?? collection
        }
    }

    private func hasSecretFields(_ name: String) -> Bool {
        schema.fields.contains { $0.key.hasPrefix(collection + ".*.") && $0.isSecret }
    }
}
