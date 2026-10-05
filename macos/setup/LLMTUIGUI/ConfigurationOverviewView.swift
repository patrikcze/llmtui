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
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 210), spacing: 14)], spacing: 14) {
            OverviewCard(title: "Configuration file", value: configExists ? "Loaded" : "Not created", detail: configExists ? "Reading the default llmtui path." : "Saving will create it with llmtui-compatible YAML.", color: configExists ? .green : .orange)
            OverviewCard(title: "Active provider", value: model.configuration.provider.name, detail: model.configuration.provider.model.isEmpty ? "No model selected" : model.configuration.provider.model, color: .blue)
            OverviewCard(title: "Providers", value: String(configuredProviderCount), detail: "Configured provider profiles", color: .purple)
            OverviewCard(title: "Draft state", value: model.statusMessage, detail: "Use Save, Reload, Reset draft, or Revert below.", color: .teal)
            capabilitiesCard
        }
    }

    private var capabilitiesCard: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Enabled capabilities").font(.headline)
            FeatureRow(title: "Tools", enabled: model.configuration.toolsEnabled)
            FeatureRow(title: "Go LLMTUI agent mode", enabled: model.configuration.agent.enabled)
            FeatureRow(title: "Session entities", enabled: model.configuration.entities.enabled)
            FeatureRow(title: "Chat history", enabled: model.configuration.rawSettings["chat.save_history"] == "true")
            FeatureRow(title: "MCP", enabled: model.configuration.rawSettings["mcp.enabled"] == "true")
            FeatureRow(title: "RAG", enabled: model.configuration.rawSettings["rag.enabled"] == "true")
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(.quaternary.opacity(0.55), in: RoundedRectangle(cornerRadius: 12))
    }

    /// Flags things this app's own hand-rolled YAML patcher (not a real
    /// YAML library) can silently get wrong rather than error on — a
    /// duplicate key collapsing to "last value wins" with no warning, or a
    /// named entry (a model profile, provider, etc.) containing "." , which
    /// collides with the "." this app uses internally to flatten nested
    /// settings into dotted paths. See `LLMTUIConfigurationStore.validate`.
    private var validationWarnings: some View {
        GroupBox {
            VStack(alignment: .leading, spacing: 10) {
                Label("Configuration file issues", systemImage: "exclamationmark.triangle.fill")
                    .font(.headline)
                    .foregroundStyle(.orange)
                ForEach(Array(model.configurationIssues.enumerated()), id: \.offset) { _, issue in
                    Text(issue)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
        }
    }

    private var actions: some View {
        GroupBox("Safe editing") {
            HStack {
                Button("Reload from disk", systemImage: "arrow.clockwise") { model.loadConfiguration() }
                Button("Reset draft", systemImage: "arrow.counterclockwise") { model.resetConfigurationDraft() }
                Button("Revert last save", systemImage: "arrow.uturn.backward") {
                    model.revertConfiguration()
                }
                .disabled(!model.hasConfigurationBackup)
                Spacer()
                Button("Save configuration", systemImage: "checkmark.circle") { model.saveConfiguration() }
                    .buttonStyle(.borderedProminent)
            }
        }
    }
}

private struct OverviewCard: View {
    let title: String
    let value: String
    let detail: String
    let color: Color

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title).font(.headline)
            Text(value).font(.title3.weight(.semibold)).foregroundStyle(color)
            Text(detail).font(.caption).foregroundStyle(.secondary).lineLimit(3)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(16)
        .background(.quaternary.opacity(0.55), in: RoundedRectangle(cornerRadius: 12))
    }
}

private struct FeatureRow: View {
    let title: String
    let enabled: Bool

    var body: some View {
        Label(title, systemImage: enabled ? "checkmark.circle.fill" : "circle")
            .foregroundStyle(enabled ? .primary : .secondary)
    }
}
