import SwiftUI

struct ConfigurationOverviewView: View {
    let model: AppModel

    private var configExists: Bool {
        FileManager.default.fileExists(atPath: model.configFilePath)
    }

    private var configuredProviderCount: Int {
        model.configuration.providers.count
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 22) {
                header
                UsageDashboardView(model: model)
                statusCards
                if !model.configurationIssues.isEmpty {
                    validationWarnings
                }
                actions
            }
            .frame(maxWidth: 900, alignment: .leading)
            .padding(28)
        }
        .navigationTitle("Configuration Overview")
        .task {
            model.loadUsageSnapshot()
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Configuration Overview")
                .font(.largeTitle.weight(.semibold))
            Text("A safe graphical editor for the real llmtui configuration. Changes stay in a draft until you save them.")
                .foregroundStyle(.secondary)
            Label(model.configFilePath, systemImage: "doc.text")
                .font(.callout)
                .textSelection(.enabled)
        }
    }

    private var statusCards: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 210), spacing: 14, alignment: .top)], spacing: 14) {
            OverviewCard(title: "Configuration file", systemImage: "doc.text.fill", value: configExists ? "Loaded" : "Not created", detail: configExists ? "Reading the default llmtui path." : "Saving will create it with llmtui-compatible YAML.", color: configExists ? Theme.success : Theme.warning)
            OverviewCard(title: "Chat uses", systemImage: Theme.systemImage(for: model.configuration.provider.type), value: model.configuration.provider.name, detail: model.configuration.provider.model.isEmpty ? "No model selected" : model.configuration.provider.model, color: Theme.tint(for: model.configuration.provider.type))
            OverviewCard(title: "llmtui starts with", systemImage: "terminal.fill", value: model.configuration.defaultProviderName, detail: model.configuration.defaultProviderIsDefined ? "default_provider in config.yaml" : "Not defined — llmtui will not start", color: model.configuration.defaultProviderIsDefined ? Theme.info : Theme.danger)
            OverviewCard(title: "Providers", systemImage: "server.rack", value: String(configuredProviderCount), detail: "Configured provider profiles", color: Theme.accent)
            OverviewCard(title: "Draft state", systemImage: "pencil.and.list.clipboard", value: model.statusMessage, detail: "Use Save, Reload, Reset draft, or Revert below.", color: Theme.teal)
            capabilitiesCard
        }
    }

    private var capabilitiesCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                IconTile(systemName: "switch.2", tint: Theme.success, size: 26)
                Text("Enabled capabilities").font(.headline)
            }
            FeatureRow(title: "Tools", enabled: model.configuration.toolsEnabled)
            FeatureRow(title: "Go LLMTUI agent mode", enabled: model.configuration.agent.enabled)
            FeatureRow(title: "Session entities", enabled: model.configuration.entities.enabled)
            FeatureRow(title: "Chat history", enabled: model.configuration.rawSettings["chat.save_history"] == "true")
            FeatureRow(title: "MCP", enabled: model.configuration.rawSettings["mcp.enabled"] == "true")
            FeatureRow(title: "RAG", enabled: model.configuration.rawSettings["rag.enabled"] == "true")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .themedCard()
    }

    /// Flags things this app's own hand-rolled YAML patcher (not a real
    /// YAML library) can silently get wrong rather than error on — a
    /// duplicate key collapsing to "last value wins" with no warning, or a
    /// named entry (a model profile, provider, etc.) containing "." , which
    /// collides with the "." this app uses internally to flatten nested
    /// settings into dotted paths. See `LLMTUIConfigurationStore.validate`.
    private var validationWarnings: some View {
        VStack {
            VStack(alignment: .leading, spacing: 10) {
                Label("Configuration file issues", systemImage: "exclamationmark.triangle.fill")
                    .font(.headline)
                    .foregroundStyle(Theme.warning)
                ForEach(Array(model.configurationIssues.enumerated()), id: \.offset) { _, issue in
                    Text(issue)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(16)
        .themedCard()
    }

    private var actions: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Safe editing").font(.headline)
            HStack {
                Button("Reload from disk", systemImage: "arrow.clockwise") { model.loadConfiguration() }
                Button("Reset draft", systemImage: "arrow.counterclockwise") { model.resetConfigurationDraft() }
                Button("Revert last save", systemImage: "arrow.uturn.backward") {
                    model.revertConfiguration()
                }
                .disabled(!model.hasConfigurationBackup)
                Spacer()
                Button("Save configuration", systemImage: "checkmark.circle") { model.saveConfiguration() }
                    .buttonStyle(AccentButtonStyle())
            }
        }
        .padding(16)
        .themedCard()
    }
}

private struct OverviewCard: View {
    let title: String
    let systemImage: String
    let value: String
    let detail: String
    let color: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 8) {
                IconTile(systemName: systemImage, tint: color, size: 26)
                Text(title).font(.headline)
            }
            Text(value).font(.title3.weight(.semibold)).foregroundStyle(color).lineLimit(2)
            Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(3)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .themedCard()
    }
}

private struct FeatureRow: View {
    let title: String
    let enabled: Bool

    var body: some View {
        Label(title, systemImage: enabled ? "checkmark.circle.fill" : "circle")
            .foregroundStyle(enabled ? Theme.success : .secondary)
    }
}
