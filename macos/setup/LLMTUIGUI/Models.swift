 
import Foundation
import Observation

enum AppSection: String, CaseIterable, Identifiable {
    case overview
    case chat
    case providers
    case chatSettings
    case tools
    case agentRuntime
    case profiles
    case moreSettings
    case personalApps

    var id: String { rawValue }

    var title: String {
        switch self {
        case .overview: "Overview"
        case .chat: "Chat"
        case .providers: "Providers"
        case .chatSettings: "Chat Settings"
        case .tools: "Tools & Safety"
        case .agentRuntime: "Agent & Runtime"
        case .profiles: "Model Profiles"
        case .moreSettings: "More Settings"
        case .personalApps: "Personal Apps"
        }
    }

    var systemImage: String {
        switch self {
        case .overview: "rectangle.grid.2x2"
        case .chat: "bubble.left.and.bubble.right.fill"
        case .providers: "server.rack"
        case .chatSettings: "slider.horizontal.3"
        case .tools: "checkmark.shield"
        case .agentRuntime: "infinity"
        case .profiles: "square.stack.3d.up"
        case .moreSettings: "ellipsis.circle"
        case .personalApps: "calendar.badge.clock"
        }
    }
}

enum ProviderType: String, CaseIterable, Identifiable {
    case ollama
    case openAICompatible = "openai_compatible"
    case embedded
    case mock

    var id: String { rawValue }

    var title: String {
        switch self {
        case .ollama: "Ollama"
        case .openAICompatible: "OpenAI-compatible"
        case .embedded: "Embedded GGUF"
        case .mock: "Mock"
        }
    }

    /// Embedded is a local GGUF file path, not a server; mock is fake —
    /// neither has anything to ask over HTTP.
    var supportsModelDiscovery: Bool {
        self == .ollama || self == .openAICompatible
    }
}

enum ReasoningMode: String, CaseIterable, Identifiable {
    case automatic = "auto"
    case on
    case off

    var id: String { rawValue }

    var title: String {
        switch self {
        case .automatic: "Automatic"
        case .on: "On"
        case .off: "Off"
        }
    }
}

enum ApprovalPolicy: String, CaseIterable, Identifiable {
    case ask
    case automatic = "auto"

    var id: String { rawValue }

    var title: String {
        switch self {
        case .ask: "Always ask"
        case .automatic: "Automatic"
        }
    }

    var detail: String {
        switch self {
        case .ask: "Writes and non-read-only commands require approval."
        case .automatic: "Tool calls run without an approval prompt."
        }
    }
}

struct ProviderConfiguration: Equatable {
    var name = "lmstudio"
    var type: ProviderType = .openAICompatible
    var baseURL = "http://localhost:1234/v1/"
    var model = "google/gemma-4-e4b"
    var apiKeyEnvironment = ""
}

struct ProviderProfile: Identifiable, Equatable {
    var id: String { name }
    var name: String
    var type: ProviderType
    var baseURL: String
    var model: String
    var apiKeyEnvironment: String
}

struct MailAccountInfo: Codable, Identifiable, Equatable {
    let id: String
    let name: String
}

struct CalendarInfo: Codable, Identifiable, Equatable {
    let id: String
    let source: String
    let title: String
    let shared: Bool
    let writable: Bool
}


enum VerificationMode: String, CaseIterable, Identifiable {
    case off
    case deterministic
    case adaptive
    case always

    var id: String { rawValue }

    var title: String {
        switch self {
        case .off: "Off"
        case .deterministic: "Deterministic"
        case .adaptive: "Adaptive"
        case .always: "Always"
        }
    }
}

enum EntityOutputStorage: String, CaseIterable, Identifiable {
    case memory
    case disk
    case off

    var id: String { rawValue }

    var title: String {
        switch self {
        case .memory: "Memory"
        case .disk: "Disk"
        case .off: "Off"
        }
    }
}

struct VerifierConfiguration: Equatable {
    var enabled = true
    var mode: VerificationMode = .adaptive
    var model = ""
    var maxTokens = 1024
    var timeout = "120s"
    var maxAttempts = 2
}

struct YieldConfiguration: Equatable {
    var enabled = true
    var maxEpisodeRequests = 64
    var maxNudgesWithoutProgress = 2
}

struct AgentConfiguration: Equatable {
    var enabled = false
    var maxCycles = 8
    var maxToolCalls = 32
    var maxTokens = 100000
    var maxElapsed = "30m"
    var maxRepeatedFailures = 3
    var persist = true
    var path = "~/.local/share/llmtui/agent-runs"
    var maxMemoryKB = 64
    var maxRuns = 32
    var enforceBudgetsLive = true
    var verifier = VerifierConfiguration()
    var yield = YieldConfiguration()
}

struct EntitiesConfiguration: Equatable {
    var enabled = true
    var maxSessionEntities = 256
    var maxPayloadBytes = 65536
    var maxTotalPayloadBytes = 4194304
    var maxContextTokens = 1200
    var maxFullExpansions = 8
    var visionEnabled = true
    var visionMaxTokens = 800
    var outputStorage: EntityOutputStorage = .memory
    var outputStoragePath = ""
}

struct LLMTUIConfiguration: Equatable {
    var provider = ProviderConfiguration()
    var providers: [ProviderProfile] = []
    var rawSettings: [String: String] = [:]
    var listSettings: [String: [String]] = [:]
    var removedModelProfiles: Set<String> = []
    var systemPrompt = "You are a helpful assistant."
    var temperature = 0.7
    var topP = 0.9
    var maxTokens = 4096
    var stream = true
    var reasoning: ReasoningMode = .automatic
    var toolsEnabled = false
    var approvalPolicy: ApprovalPolicy = .ask
    var maxToolIterations = 10
    /// Defaults to the folder containing the app bundle itself rather than
    /// the process's cwd: for a double-clicked `.app` (as opposed to a
    /// binary launched directly from Terminal), the cwd is whatever
    /// Finder/launchd happened to set — not the folder the user put the app
    /// in — so it doesn't actually track "wherever this copy of the app
    /// lives" the way a user moving the app around would expect.
    var toolWorkspacePath = Bundle.main.bundleURL.deletingLastPathComponent().path
    var entities = EntitiesConfiguration()
    var agent = AgentConfiguration()

    /// `rawSettings`/`listSettings` key model_profiles by joining
    /// "model_profiles", the profile name, and the field name with ".", but
    /// a profile name can itself legally contain a literal "." (llmtui's own
    /// example config ships one: "qwen3.8-27b"). Splitting on every dot and
    /// taking the second segment — the obvious approach — truncates that to
    /// "qwen3", fabricating a phantom profile whose fields all read back
    /// empty (they're stored under the real, longer key) and whose name is
    /// a literal prefix of the real one, so prefix-based lookups for it
    /// collide with the real profile's keys too. The field set is small and
    /// fixed, so the name/field boundary is found from the end instead.
    nonisolated static let modelProfileFields = [
        "match", "context_window", "preferred_temperature",
        "supports_json_mode", "prompt_style", "reasoning_hint"
    ]

    nonisolated static func modelProfileName(fromKey key: String) -> String? {
        guard key.hasPrefix("model_profiles.") else { return nil }
        let rest = key.dropFirst("model_profiles.".count)
        for field in modelProfileFields where rest.hasSuffix(".\(field)") {
            return String(rest.dropLast(field.count + 1))
        }
        return nil
    }
}

struct ChatMessage: Identifiable, Equatable {
    enum Role {
        case user
        case assistant
        case tool
    }

    let id = UUID()
    let role: Role
    let createdAt: Date
    var text: String
    var isStreaming = false
    var metrics: ChatMetrics?
    /// Tool calls and their results made while producing this assistant
    /// message, replayed ahead of it on later turns so a follow-up question
    /// has the earlier tool activity to work from. Empty for turns that used
    /// no tools.
    var toolTranscript: [OpenAIMessage] = []
    /// Tool calls shown inline in the transcript for this turn, in call
    /// order. Populated as soon as the first tool call arrives, which is
    /// often before any answer text has streamed in.
    var toolActivities: [ToolActivity] = []
    /// Images attached when this message was sent (user messages only).
    var attachments: [ChatAttachment] = []

    init(role: Role, text: String, isStreaming: Bool = false, createdAt: Date = .now, metrics: ChatMetrics? = nil, attachments: [ChatAttachment] = []) {
        self.role = role
        self.createdAt = createdAt
        self.text = text
        self.isStreaming = isStreaming
        self.metrics = metrics
        self.attachments = attachments
    }
}

/// A follow-up message typed while a prior turn is still generating.
/// Dispatched automatically, in order, once the in-flight turn finishes.
struct QueuedMessage: Identifiable, Equatable {
    let id = UUID()
    let text: String
    let attachments: [ChatAttachment]
    let usesNativeAgent: Bool
    let usesTools: Bool
    let reasoning: ChatReasoningChoice
}

struct ChatMetrics: Equatable, Sendable {
    let duration: TimeInterval
    let outputTokens: Int
    let tokensPerSecond: Double
}

struct ToolActivity: Identifiable, Equatable, Sendable {
    let id: UUID
    let name: String
    let summary: String
    /// The raw JSON arguments the model called this tool with, shown as the
    /// "Command" detail when a row is expanded.
    let arguments: String
    let safetyClass: ToolSafetyClass
    var status: ToolExecutionStatus
    var detail: String?
    var duration: TimeInterval?
}

struct ToolRequest: Identifiable, Equatable {
    let id = UUID()
    let providerToolCallID: String
    let name: String
    let arguments: String
    let summary: String
    let safetyClass: ToolSafetyClass
    var status: ToolExecutionStatus = .requested
    var result: String?

    var isMutating: Bool { safetyClass == .mutating || safetyClass == .command }

    init(providerToolCallID: String, name: String, arguments: String, safetyClass: ToolSafetyClass) {
        self.providerToolCallID = providerToolCallID
        self.name = name
        self.arguments = arguments
        self.safetyClass = safetyClass
        self.summary = ToolRequest.makeSummary(name: name, arguments: arguments)
    }

    private static func makeSummary(name: String, arguments: String) -> String {
        guard let data = arguments.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any]
        else { return name }
        let detail = ["path", "command", "query", "url"].compactMap { key in
            object[key] as? String
        }.first
        return detail.map { "\(name) · \($0)" } ?? name
    }
}

struct UserQuestion: Identifiable, Equatable, Sendable {
    let id: UUID
    let question: String
    let options: [String]
    let allowsFreeText: Bool
    let placeholder: String?

    init(request: ToolRequest) throws {
        guard let data = request.arguments.data(using: .utf8),
              let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let rawQuestion = object["question"] as? String else {
            throw ToolRuntimeError.invalidArguments("ask_user requires a question string")
        }

        let question = rawQuestion.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !question.isEmpty else {
            throw ToolRuntimeError.invalidArguments("ask_user question cannot be empty")
        }

        var seenOptions = Set<String>()
        let options = (object["options"] as? [Any] ?? [])
            .compactMap { $0 as? String }
            .map { String($0.trimmingCharacters(in: .whitespacesAndNewlines).prefix(120)) }
            .filter { !$0.isEmpty && seenOptions.insert($0).inserted }

        let rawPlaceholder = (object["placeholder"] as? String)?
            .trimmingCharacters(in: .whitespacesAndNewlines)

        id = request.id
        self.question = String(question.prefix(1_000))
        self.options = Array(options.prefix(4))
        allowsFreeText = options.isEmpty || ((object["allow_free_text"] as? Bool) ?? false)
        placeholder = rawPlaceholder.flatMap { $0.isEmpty ? nil : String($0.prefix(160)) }
    }
}

enum UserQuestionResponse: Equatable, Sendable {
    case answered(String)
    case skipped

    var toolContent: String {
        let object: [String: Any]
        switch self {
        case .answered(let answer):
            object = ["answered": true, "answer": answer]
        case .skipped:
            object = ["answered": false, "answer": NSNull()]
        }
        guard let data = try? JSONSerialization.data(withJSONObject: object, options: [.sortedKeys]),
              let value = String(data: data, encoding: .utf8) else {
            return #"{"answered":false,"answer":null}"#
        }
        return value
    }
}

@MainActor
@Observable
final class AppModel {
    var selectedSection: AppSection = .overview
    var configuration = LLMTUIConfiguration()
    var messages: [ChatMessage] = [
        ChatMessage(
            role: .assistant,
            text: "Welcome to the llmtui companion. Configure a provider, then start a local-first conversation.",
            isStreaming: false
        )
    ]
    var pendingToolRequests: [ToolRequest] = []
    var pendingUserQuestion: UserQuestion?
    var isGenerating = false
    var statusMessage = "Ready"
    var configFilePath = LLMTUIConfigurationStore.defaultURL.path
    var configurationSourceText = ""
    var configurationIssues: [String] = []
    var hasConfigurationBackup = false
    var discoveredMailAccounts: [MailAccountInfo] = []
    var discoveredCalendars: [CalendarInfo] = []
    var isDiscoveringPersonalApps = false
    var discoveredModels: [String] = []
    var isDiscoveringModels = false
    /// Name of the model profile currently fetching metadata from the
    /// provider, if any — lets each profile row show its own spinner.
    var profileSettingsBeingRead: String?
    var discoveredProfileMetadata: [String: ProviderModelMetadata] = [:]
    var usageSnapshot: UsageSnapshot?
    var isLoadingUsage = false
    var lastError: String?
    var draftMessage = ""
    var draftAttachments: [ChatAttachment] = []
    /// Controls only the native Swift chat loop. The similarly named
    /// `configuration.agent.enabled` remains the persisted Go LLMTUI setting.
    var nativeAgentEnabled: Bool {
        didSet {
            UserDefaults.standard.set(nativeAgentEnabled, forKey: Self.nativeAgentEnabledDefaultsKey)
        }
    }
    /// Offers the workspace/web/memory tools in ordinary (non-agent) chat.
    /// Independent of both `nativeAgentEnabled` and the Go `tools.enabled`
    /// YAML setting — see `NativeChatRuntimeOptions`.
    var nativeToolsEnabled: Bool {
        didSet {
            UserDefaults.standard.set(nativeToolsEnabled, forKey: Self.nativeToolsEnabledDefaultsKey)
        }
    }
    /// The reasoning setting for the next message. Chat-local only — see
    /// `ReasoningControl.swift`.
    var nativeReasoningChoice: ChatReasoningChoice {
        didSet {
            UserDefaults.standard.set(nativeReasoningChoice.rawValue, forKey: Self.nativeReasoningChoiceDefaultsKey)
        }
    }
    var queuedMessages: [QueuedMessage] = []
    private static let maxQueuedMessages = 10
    private static let nativeAgentEnabledDefaultsKey = "nativeChatAgentEnabled"
    private static let nativeToolsEnabledDefaultsKey = "nativeChatToolsEnabled"
    private static let nativeReasoningChoiceDefaultsKey = "nativeChatReasoningChoice"
    private var generationID: UUID?

    /// A rough, live estimate of how much of the model's context window the
    /// next request would use — there's no real token count to read until a
    /// provider actually replies (no provider here returns `usage`).
    var contextUsage: (used: Int, total: Int) {
        let total = OpenAICompatibleChatService.contextWindow(for: configuration)
        let historyTokens = OpenAICompatibleChatService.estimatedTokens(for: messages)
        let draftTokens = (draftMessage.count + configuration.systemPrompt.count) / 4
        return (used: historyTokens + draftTokens, total: total)
    }

    /// What the currently selected model actually accepts for reasoning
    /// control, derived from its name (GPT-OSS family) and — when it's been
    /// fetched from the Model Profiles screen — the provider's own reported
    /// capability. Purely derived from other observed properties, so it
    /// updates automatically when the provider/model selection changes.
    var chatReasoningCapability: ReasoningCapability {
        ReasoningCapabilityDetector.capability(
            modelID: configuration.provider.model,
            metadata: discoveredProfileMetadata[configuration.provider.model]
        )
    }

    let configurationStore = LLMTUIConfigurationStore()
    let personalAppsRuntime = PersonalAppsRuntime.shared
    let chatService: any ChatService
    let diagnostics: any DiagnosticsLogging

    init(
        chatService: (any ChatService)? = nil,
        diagnostics: (any DiagnosticsLogging)? = nil,
        nativeAgentEnabled: Bool? = nil
    ) {
        self.chatService = chatService ?? OpenAICompatibleChatService()
        self.diagnostics = diagnostics ?? DiagnosticsLogger.shared
        self.nativeAgentEnabled = nativeAgentEnabled
            ?? UserDefaults.standard.bool(forKey: Self.nativeAgentEnabledDefaultsKey)
        self.nativeToolsEnabled = UserDefaults.standard.bool(forKey: Self.nativeToolsEnabledDefaultsKey)
        self.nativeReasoningChoice = UserDefaults.standard.string(forKey: Self.nativeReasoningChoiceDefaultsKey)
            .flatMap(ChatReasoningChoice.init(rawValue:)) ?? .automatic
        // A workspace path saved under the old hardcoded "~/Documents"
        // default (from before `toolWorkspacePath`'s default changed to the
        // app bundle's folder) was never actually chosen by the user — it's
        // just the previous default having been persisted. Treat it as
        // unset so the new default takes effect instead of being stuck
        // behind a stale saved value forever.
        let staleDocumentsDefault = FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent("Documents")
            .path
        let savedWorkspacePath = UserDefaults.standard.string(forKey: "chatToolWorkspacePath")
        if let savedWorkspacePath, savedWorkspacePath != staleDocumentsDefault {
            configuration.toolWorkspacePath = savedWorkspacePath
        }
        diagnose(level: .info, category: .application, event: "launch")
    }

    private func diagnose(
        level: DiagnosticLevel,
        category: DiagnosticCategory,
        event: String,
        requestID: UUID? = nil,
        metadata: [String: DiagnosticValue] = [:],
        error: Error? = nil,
        operation: String = "application"
    ) {
        let safeError = error.map { SafeDiagnosticError.describe($0, operation: operation) }
        Task {
            await diagnostics.log(
                level: level,
                category: category,
                event: event,
                requestID: requestID,
                metadata: metadata,
                error: safeError
            )
        }
    }

    func loadConfiguration() {
        do {
            let loaded = try configurationStore.load()
            configuration = loaded.configuration
            personalAppsRuntime.applySavedConfiguration(loaded.configuration)
            configurationSourceText = loaded.sourceText
            configFilePath = loaded.fileURL.path
            configurationIssues = LLMTUIConfigurationStore.validate(loaded.sourceText)
            hasConfigurationBackup = FileManager.default.fileExists(
                atPath: loaded.fileURL.appendingPathExtension("bak").path
            )
            statusMessage = "Configuration loaded"
            lastError = nil
            diagnose(
                level: .info,
                category: .configuration,
                event: "load_succeeded",
                metadata: [
                    "provider_count": .integer(configuration.providers.count),
                    "issue_count": .integer(configurationIssues.count)
                ]
            )
        } catch {
            statusMessage = "Using default configuration"
            lastError = error.localizedDescription
            diagnose(level: .error, category: .configuration, event: "load_failed", error: error, operation: "configuration load")
        }
    }

    func saveConfiguration() {
        do {
            syncActiveProvider()
            UserDefaults.standard.set(configuration.toolWorkspacePath, forKey: "chatToolWorkspacePath")
            configurationSourceText = try configurationStore.save(
                configuration,
                basedOn: configurationSourceText
            )
            personalAppsRuntime.applySavedConfiguration(configuration)
            configurationIssues = LLMTUIConfigurationStore.validate(configurationSourceText)
            hasConfigurationBackup = true
            statusMessage = "Configuration saved"
            lastError = nil
            diagnose(
                level: .info,
                category: .configuration,
                event: "save_succeeded",
                metadata: ["issue_count": .integer(configurationIssues.count)]
            )
        } catch {
            lastError = error.localizedDescription
            diagnose(level: .error, category: .configuration, event: "save_failed", error: error, operation: "configuration save")
        }
    }

    func selectProvider(_ name: String) {
        personalAppsRuntime.revokeRemoteDisclosure()
        guard let selected = configuration.providers.first(where: { $0.name == name }) else { return }
        configuration.provider = ProviderConfiguration(
            name: selected.name,
            type: selected.type,
            baseURL: selected.baseURL,
            model: selected.model,
            apiKeyEnvironment: selected.apiKeyEnvironment
        )
        statusMessage = "Editing \(name)"
    }

    func renameActiveProvider(to newName: String) {
        let trimmed = newName.trimmingCharacters(in: .whitespacesAndNewlines)
        let oldName = configuration.provider.name
        guard !trimmed.isEmpty, trimmed != oldName else { return }
        // Provider names are used as raw YAML map keys and as dotted path
        // segments ("providers.<name>.type") throughout the config store, so
        // "." would corrupt that scheme, and a duplicate name would collide
        // with ProviderProfile.id (== name) and the picker's row identity.
        guard !trimmed.contains("."), !configuration.providers.contains(where: { $0.name == trimmed }) else { return }
        if let index = configuration.providers.firstIndex(where: { $0.name == oldName }) {
            configuration.providers[index].name = trimmed
        }
        // Provider-specific fields this app doesn't model as struct fields
        // (an embedded provider's model_path/sampling/etc.) live keyed by
        // name in rawSettings/listSettings, same as model_profiles fields —
        // move them to the new name so edits survive the rename and so
        // saving finds them under the name actually on disk post-rename.
        let oldPrefix = "providers.\(oldName)."
        let newPrefix = "providers.\(trimmed)."
        for key in Array(configuration.rawSettings.keys) where key.hasPrefix(oldPrefix) {
            let value = configuration.rawSettings.removeValue(forKey: key)
            configuration.rawSettings[newPrefix + key.dropFirst(oldPrefix.count)] = value
        }
        for key in Array(configuration.listSettings.keys) where key.hasPrefix(oldPrefix) {
            let value = configuration.listSettings.removeValue(forKey: key)
            configuration.listSettings[newPrefix + key.dropFirst(oldPrefix.count)] = value
        }
        configuration.provider.name = trimmed
    }

    func createProviderProfile() {
        let baseName = "new_provider"
        var name = baseName
        var suffix = 2
        while configuration.providers.contains(where: { $0.name == name }) {
            name = "\(baseName)_\(suffix)"
            suffix += 1
        }
        let profile = ProviderProfile(
            name: name,
            type: .openAICompatible,
            baseURL: "http://localhost:1234/v1",
            model: "local-model",
            apiKeyEnvironment: ""
        )
        configuration.providers.append(profile)
        configuration.provider = ProviderConfiguration(
            name: profile.name,
            type: profile.type,
            baseURL: profile.baseURL,
            model: profile.model,
            apiKeyEnvironment: profile.apiKeyEnvironment
        )
        statusMessage = "New provider draft created — save to add it"
    }

    /// Creates a model profile with every setting already filled in from the
    /// "New Model Profile" sheet, rather than a bare default the user then
    /// has to scroll an alphabetically-sorted list to find and edit. Returns
    /// false (creating nothing) if the name is empty, contains ".", or
    /// collides with an existing profile.
    @discardableResult
    func createModelProfile(
        name: String,
        matches: [String],
        contextWindow: String,
        preferredTemperature: String,
        supportsJSONMode: Bool,
        promptStyle: String,
        reasoningHint: Bool
    ) -> Bool {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !trimmed.contains(".") else { return false }
        let existingNames = Set(configuration.rawSettings.keys.compactMap(LLMTUIConfiguration.modelProfileName(fromKey:)))
        guard !existingNames.contains(trimmed) else { return false }
        let prefix = "model_profiles.\(trimmed)."
        configuration.rawSettings[prefix + "context_window"] = contextWindow
        configuration.rawSettings[prefix + "preferred_temperature"] = preferredTemperature
        configuration.rawSettings[prefix + "supports_json_mode"] = supportsJSONMode ? "true" : "false"
        configuration.rawSettings[prefix + "prompt_style"] = promptStyle
        configuration.rawSettings[prefix + "reasoning_hint"] = reasoningHint ? "true" : "false"
        configuration.listSettings[prefix + "match"] = matches
        configuration.removedModelProfiles.remove(trimmed)
        statusMessage = "Created model profile \(trimmed) — save to apply"
        return true
    }

    /// Renames a model profile, moving every `model_profiles.<old>.*` entry
    /// in rawSettings/listSettings under the new name. Returns false (and
    /// leaves the draft untouched) when the new name is empty, unchanged,
    /// contains "." (a dotted-path separator), or collides with another profile.
    @discardableResult
    func renameModelProfile(_ oldName: String, to newName: String) -> Bool {
        let trimmed = newName.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, trimmed != oldName, !trimmed.contains(".") else { return false }
        let existingNames = Set(configuration.rawSettings.keys.compactMap(LLMTUIConfiguration.modelProfileName(fromKey:)))
        guard !existingNames.contains(trimmed) else { return false }
        let oldPrefix = "model_profiles.\(oldName)."
        let newPrefix = "model_profiles.\(trimmed)."
        for key in Array(configuration.rawSettings.keys) where LLMTUIConfiguration.modelProfileName(fromKey: key) == oldName {
            let value = configuration.rawSettings.removeValue(forKey: key)
            configuration.rawSettings[newPrefix + key.dropFirst(oldPrefix.count)] = value
        }
        for key in Array(configuration.listSettings.keys) where LLMTUIConfiguration.modelProfileName(fromKey: key) == oldName {
            let value = configuration.listSettings.removeValue(forKey: key)
            configuration.listSettings[newPrefix + key.dropFirst(oldPrefix.count)] = value
        }
        // The save path deletes every map in removedModelProfiles and then
        // synthesizes a block for any profile name not already on disk, so a
        // rename is "remove old, insert new". For a draft that was never
        // saved the removal is a harmless no-op.
        configuration.removedModelProfiles.insert(oldName)
        configuration.removedModelProfiles.remove(trimmed)
        statusMessage = "Model profile renamed to \(trimmed) — save to apply"
        return true
    }

    func removeModelProfile(_ name: String) {
        Array(configuration.rawSettings.keys)
            .filter { LLMTUIConfiguration.modelProfileName(fromKey: $0) == name }
            .forEach { configuration.rawSettings.removeValue(forKey: $0) }
        configuration.listSettings.removeValue(forKey: "model_profiles.\(name).match")
        configuration.removedModelProfiles.insert(name)
        statusMessage = "Model profile \(name) marked for removal — save to apply"
    }

    private func syncActiveProvider() {
        let active = ProviderProfile(
            name: configuration.provider.name,
            type: configuration.provider.type,
            baseURL: configuration.provider.baseURL,
            model: configuration.provider.model,
            apiKeyEnvironment: configuration.provider.apiKeyEnvironment
        )
        if let index = configuration.providers.firstIndex(where: { $0.name == active.name }) {
            configuration.providers[index] = active
        } else {
            configuration.providers.append(active)
        }
    }

    func resetConfigurationDraft() {
        configuration = LLMTUIConfiguration()
        statusMessage = "Defaults loaded into the editor — press Save to apply"
        lastError = nil
    }

    func revertConfiguration() {
        do {
            let loaded = try configurationStore.revertLastSave()
            configuration = loaded.configuration
            personalAppsRuntime.applySavedConfiguration(loaded.configuration)
            configurationSourceText = loaded.sourceText
            configFilePath = loaded.fileURL.path
            configurationIssues = LLMTUIConfigurationStore.validate(loaded.sourceText)
            hasConfigurationBackup = true
            statusMessage = "Reverted the last saved configuration"
            lastError = nil
        } catch {
            lastError = error.localizedDescription
        }
    }

    func connectGUIMail() {
        Task { await personalAppsRuntime.connectMail() }
    }

    func connectGUICalendar() {
        Task { await personalAppsRuntime.connectCalendar() }
    }

    func disconnectGUIAdapter(_ adapter: PersonalAppsAdapterKind) {
        personalAppsRuntime.disconnect(adapter)
    }

    func allowPrivateDisclosureForCurrentProvider() {
        personalAppsRuntime.allowRemoteDisclosure(for: configuration)
    }

    func revokePrivateDisclosure() {
        personalAppsRuntime.revokeRemoteDisclosure()
    }

    func discoverMailAccounts() {
        isDiscoveringPersonalApps = true
        diagnose(level: .info, category: .personalApps, event: "mail_discovery_started")
        Task {
            do {
                discoveredMailAccounts = try await PersonalAppsDiscovery.mailAccounts()
                statusMessage = "Mail accounts discovered — review and select IDs"
                lastError = nil
                diagnose(
                    level: .info,
                    category: .personalApps,
                    event: "mail_discovery_succeeded",
                    metadata: ["result_count": .integer(discoveredMailAccounts.count)]
                )
            } catch {
                lastError = error.localizedDescription
                diagnose(level: .error, category: .personalApps, event: "mail_discovery_failed", error: error, operation: "Mail account discovery")
            }
            isDiscoveringPersonalApps = false
        }
    }

    func discoverModels() {
        isDiscoveringModels = true
        let startedAt = Date()
        diagnose(
            level: .info,
            category: .provider,
            event: "model_discovery_started",
            metadata: ["provider_type": .string(configuration.provider.type.rawValue)]
        )
        Task {
            do {
                discoveredModels = try await ProviderModelDiscovery.fetchModels(configuration: configuration)
                statusMessage = "Found \(discoveredModels.count) model(s) on \(configuration.provider.name)"
                lastError = nil
                diagnose(
                    level: .info,
                    category: .provider,
                    event: "model_discovery_succeeded",
                    metadata: [
                        "result_count": .integer(discoveredModels.count),
                        "duration_seconds": .double(Date().timeIntervalSince(startedAt))
                    ]
                )
            } catch {
                lastError = error.localizedDescription
                diagnose(level: .error, category: .provider, event: "model_discovery_failed", error: error, operation: "model discovery")
            }
            isDiscoveringModels = false
        }
    }

    /// Imports only provider metadata that has a direct llmtui model-profile
    /// equivalent. Other useful metadata is retained for display in the row.
    func readModelProfileSettings(profileName: String, model: String) {
        profileSettingsBeingRead = profileName
        Task {
            do {
                let metadata = try await ProviderModelDiscovery.fetchProfileSettings(
                    configuration: configuration,
                    model: model
                )
                let prefix = "model_profiles.\(profileName)."
                for (field, value) in metadata.profileValues {
                    configuration.rawSettings[prefix + field] = value
                }
                discoveredProfileMetadata[profileName] = metadata
                let importedFields = metadata.profileValues.keys.sorted().joined(separator: ", ")
                statusMessage = importedFields.isEmpty
                    ? "Read metadata for \(profileName); no profile settings were exposed"
                    : "Filled \(importedFields) for \(profileName) from \(configuration.provider.name)"
                lastError = nil
            } catch {
                lastError = error.localizedDescription
            }
            if profileSettingsBeingRead == profileName {
                profileSettingsBeingRead = nil
            }
        }
    }

    func discoverCalendars() {
        guard let helperPath = configuration.rawSettings["personal_apps.calendar.helper_path"], !helperPath.isEmpty else {
            lastError = "Configure the absolute Calendar helper path first. Install it with llmtui's calendar-helper-setup command."
            return
        }
        isDiscoveringPersonalApps = true
        diagnose(level: .info, category: .personalApps, event: "calendar_discovery_started")
        Task {
            do {
                discoveredCalendars = try await PersonalAppsDiscovery.calendars(helperPath: helperPath)
                statusMessage = "Calendars discovered — review and select native IDs"
                lastError = nil
                diagnose(
                    level: .info,
                    category: .personalApps,
                    event: "calendar_discovery_succeeded",
                    metadata: ["result_count": .integer(discoveredCalendars.count)]
                )
            } catch {
                if case .invalidHelperPath = error as? PersonalAppsDiscoveryError {
                    lastError = error.localizedDescription
                } else {
                    lastError = "Calendar access was denied. Open System Settings → Privacy & Security → Calendars and enable the signed llmtui Calendar helper, then try discovery again.\n\nTechnical detail: \(error.localizedDescription)"
                }
                diagnose(level: .error, category: .personalApps, event: "calendar_discovery_failed", error: error, operation: "Calendar discovery")
            }
            isDiscoveringPersonalApps = false
        }
    }

    func openCalendarPrivacySettings() {
        PersonalAppsDiscovery.openCalendarPrivacySettings()
    }

    func resetCalendarPermissionAndDiscover() {
        guard let helperPath = configuration.rawSettings["personal_apps.calendar.helper_path"], !helperPath.isEmpty else {
            lastError = "Configure the absolute Calendar helper path first."
            return
        }
        isDiscoveringPersonalApps = true
        Task {
            do {
                try await PersonalAppsDiscovery.resetCalendarPermission(helperPath: helperPath)
                discoveredCalendars = try await PersonalAppsDiscovery.calendars(helperPath: helperPath)
                statusMessage = "Calendar permission reset and calendars discovered"
                lastError = nil
            } catch {
                lastError = "macOS did not reset or grant Calendar access. Open System Settings → Privacy & Security → Calendars and enable the signed helper, then try again.\n\nTechnical detail: \(error.localizedDescription)"
            }
            isDiscoveringPersonalApps = false
        }
    }

    func isMailAccountAllowed(_ id: String) -> Bool {
        configuration.listSettings["personal_apps.mail.allowed_accounts", default: []].contains(id)
    }

    func setMailAccountAllowed(_ id: String, allowed: Bool) {
        var values = configuration.listSettings["personal_apps.mail.allowed_accounts", default: []]
        values.removeAll { $0 == id }
        if allowed { values.append(id) }
        configuration.listSettings["personal_apps.mail.allowed_accounts"] = values
    }

    func isCalendarAllowed(_ id: String) -> Bool {
        configuration.listSettings["personal_apps.calendar.allowed_calendars", default: []].contains(id)
    }

    func setCalendarAllowed(_ id: String, allowed: Bool) {
        var values = configuration.listSettings["personal_apps.calendar.allowed_calendars", default: []]
        values.removeAll { $0 == id }
        if allowed { values.append(id) }
        configuration.listSettings["personal_apps.calendar.allowed_calendars"] = values
    }

    func addAttachment(from url: URL) {
        do {
            addAttachment(try ChatAttachment.load(from: url))
        } catch {
            lastError = error.localizedDescription
        }
    }

    func addAttachment(_ attachment: ChatAttachment) {
        guard draftAttachments.count < 4 else {
            lastError = "You can attach up to 4 images per message."
            return
        }
        draftAttachments.append(attachment)
        lastError = nil
    }

    func removeAttachment(_ attachment: ChatAttachment) {
        draftAttachments.removeAll { $0.id == attachment.id }
    }

    func sendDraft() {
        let text = draftMessage.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { return }

        let attachments = draftAttachments
        let usesNativeAgent = nativeAgentEnabled
        let usesTools = nativeToolsEnabled
        let reasoning = nativeReasoningChoice
        draftMessage = ""
        draftAttachments = []

        guard !isGenerating else {
            guard queuedMessages.count < Self.maxQueuedMessages else {
                lastError = "You can queue up to \(Self.maxQueuedMessages) messages."
                draftMessage = text
                draftAttachments = attachments
                return
            }
            queuedMessages.append(QueuedMessage(
                text: text,
                attachments: attachments,
                usesNativeAgent: usesNativeAgent,
                usesTools: usesTools,
                reasoning: reasoning
            ))
            return
        }

        dispatch(text: text, attachments: attachments, usesNativeAgent: usesNativeAgent, usesTools: usesTools, reasoning: reasoning)
    }

    func removeQueuedMessage(_ message: QueuedMessage) {
        queuedMessages.removeAll { $0.id == message.id }
    }

    /// Pulls a queued message back out for editing: removes it from the
    /// queue and restores it into the composer so the user can revise it.
    func editQueuedMessage(_ message: QueuedMessage) {
        queuedMessages.removeAll { $0.id == message.id }
        draftMessage = message.text
        draftAttachments = message.attachments
        nativeAgentEnabled = message.usesNativeAgent
        nativeToolsEnabled = message.usesTools
        nativeReasoningChoice = message.reasoning
    }

    /// Produces request-scoped runtime options for the native Swift chat.
    /// These never borrow from or mutate the YAML configuration edited for
    /// the separate Go LLMTUI application.
    func chatRuntimeOptions(
        usesNativeAgent: Bool,
        usesTools: Bool = false,
        reasoning: ChatReasoningChoice = .automatic
    ) -> NativeChatRuntimeOptions {
        NativeChatRuntimeOptions(
            agentEnabled: usesNativeAgent,
            toolsEnabled: usesTools,
            reasoningChoice: reasoning,
            reasoningCapability: chatReasoningCapability
        )
    }

    private func dispatch(text: String, attachments: [ChatAttachment], usesNativeAgent: Bool, usesTools: Bool, reasoning: ChatReasoningChoice) {
        messages.append(ChatMessage(role: .user, text: text, attachments: attachments))
        isGenerating = true
        let requestID = UUID()
        let startedAt = Date()
        generationID = requestID
        statusMessage = "Generating…"
        diagnose(
            level: .info,
            category: .chat,
            event: "request_started",
            requestID: requestID,
            metadata: [
                "provider_type": .string(configuration.provider.type.rawValue),
                "provider_profile": .string(configuration.provider.name),
                "model": .string(configuration.provider.model),
                "attachment_count": .integer(attachments.count),
                "history_message_count": .integer(max(messages.count - 1, 0))
            ]
        )

        let runtimeOptions = chatRuntimeOptions(usesNativeAgent: usesNativeAgent, usesTools: usesTools, reasoning: reasoning)
        Task {
            do {
                var response = ""
                for try await event in chatService.send(
                    message: text,
                    attachments: attachments,
                    configuration: configuration,
                    runtimeOptions: runtimeOptions,
                    history: Array(messages.dropLast())
                ) {
                    guard generationID == requestID else { return }
                    switch event {
                    case .text(let chunk):
                        response += chunk
                        updateStreamingResponse(response)
                    case .toolRequest(let request):
                        pendingToolRequests.append(request)
                        upsertToolActivity(for: request, status: .awaitingApproval, detail: "Waiting for your approval.")
                    case .userQuestion(let question):
                        pendingUserQuestion = question
                        statusMessage = "Waiting for your answer…"
                    case .status(let status):
                        statusMessage = status
                    case .toolActivity(let activity):
                        upsertToolActivity(activity)
                    case .transcript(let transcript):
                        if let index = messages.lastIndex(where: { $0.role == .assistant && $0.isStreaming }) {
                            messages[index].toolTranscript = transcript
                        }
                    case .privateSessionActivated:
                        statusMessage = "Private Personal Apps session"
                    case .completed(let metrics):
                        finishStreamingResponse(with: metrics)
                    case .finished:
                        finishStreamingResponse()
                    }
                }

                guard generationID == requestID else { return }
                finishStreamingResponse()

                if response.isEmpty {
                    messages.append(ChatMessage(role: .assistant, text: "The provider returned an empty response."))
                }
                isGenerating = false
                pendingUserQuestion = nil
                statusMessage = "Ready"
                diagnose(
                    level: .info,
                    category: .chat,
                    event: "request_completed",
                    requestID: requestID,
                    metadata: [
                        "duration_seconds": .double(Date().timeIntervalSince(startedAt)),
                        "response_character_count": .integer(response.count)
                    ]
                )
                dequeueNextIfNeeded()
            } catch {
                guard generationID == requestID else { return }
                finishStreamingResponse()
                isGenerating = false
                pendingUserQuestion = nil
                statusMessage = "Generation failed"
                lastError = error.localizedDescription
                diagnose(
                    level: .error,
                    category: .chat,
                    event: "request_failed",
                    requestID: requestID,
                    metadata: ["duration_seconds": .double(Date().timeIntervalSince(startedAt))],
                    error: error,
                    operation: "chat request"
                )
                dequeueNextIfNeeded()
            }
        }
    }

    /// Starts the next queued message, if any, once a turn has finished —
    /// called from the success/error paths above and from `stopGeneration()`
    /// so a follow-up typed while a reply was generating gets sent in turn
    /// rather than silently sitting there forever.
    private func dequeueNextIfNeeded() {
        guard !queuedMessages.isEmpty else { return }
        let next = queuedMessages.removeFirst()
        dispatch(
            text: next.text,
            attachments: next.attachments,
            usesNativeAgent: next.usesNativeAgent,
            usesTools: next.usesTools,
            reasoning: next.reasoning
        )
    }

    func stopGeneration() {
        generationID = nil
        chatService.cancel()
        finishStreamingResponse()
        pendingToolRequests.removeAll()
        pendingUserQuestion = nil
        isGenerating = false
        statusMessage = "Generation stopped"
        diagnose(level: .info, category: .chat, event: "request_cancelled")
        dequeueNextIfNeeded()
    }

    func clearChat() {
        // Clear the queue before stopping — stopGeneration() auto-dispatches
        // the next queued item, which would otherwise immediately re-fill
        // the message list this is about to wipe.
        queuedMessages.removeAll()
        if isGenerating {
            stopGeneration()
        }
        messages.removeAll()
        pendingToolRequests.removeAll()
        pendingUserQuestion = nil
        personalAppsRuntime.clearPrivateSession()
        statusMessage = "Conversation cleared"
    }

    func resolveTool(_ request: ToolRequest, approved: Bool) {
        guard let index = pendingToolRequests.firstIndex(where: { $0.id == request.id }) else { return }
        pendingToolRequests.remove(at: index)
        chatService.resolveTool(request, approved: approved)
        upsertToolActivity(
            for: request,
            status: approved ? .running : .rejected,
            detail: approved ? "Approved — preparing execution." : "Rejected by the user."
        )
        statusMessage = approved ? "Tool approved" : "Tool rejected"
        diagnose(
            level: .info,
            category: .tools,
            event: approved ? "approval_granted" : "approval_rejected",
            metadata: [
                "tool": .string(request.name),
                "safety_class": .string(request.safetyClass.rawValue)
            ]
        )
    }

    func answerUserQuestion(_ answer: String) {
        guard let question = pendingUserQuestion else { return }
        let value = answer.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return }

        pendingUserQuestion = nil
        chatService.resolveUserQuestion(
            question,
            response: .answered(String(value.prefix(4_000)))
        )
        statusMessage = "Continuing…"
    }

    func skipUserQuestion() {
        guard let question = pendingUserQuestion else { return }
        pendingUserQuestion = nil
        chatService.resolveUserQuestion(question, response: .skipped)
        statusMessage = "Continuing without an answer…"
    }

    /// Finds or creates the assistant message that the current turn's
    /// streamed text and tool activity both belong to. Tool calls commonly
    /// arrive before any answer text, so this can't simply wait for the
    /// first `.text` chunk the way streaming alone used to.
    private func currentTurnAssistantIndex() -> Int {
        if let index = messages.lastIndex(where: { $0.role == .assistant && $0.isStreaming }) {
            return index
        }
        messages.append(ChatMessage(role: .assistant, text: "", isStreaming: true))
        return messages.count - 1
    }

    private func updateStreamingResponse(_ text: String) {
        messages[currentTurnAssistantIndex()].text = text
    }

    private func finishStreamingResponse(with metrics: ChatMetrics? = nil) {
        guard let index = messages.lastIndex(where: { $0.role == .assistant && $0.isStreaming }) else { return }
        messages[index].isStreaming = false
        if let metrics {
            messages[index].metrics = metrics
        }
    }

    private func upsertToolActivity(for request: ToolRequest, status: ToolExecutionStatus, detail: String?) {
        upsertToolActivity(ToolActivity(
            id: request.id,
            name: request.name,
            summary: request.summary,
            arguments: request.arguments,
            safetyClass: request.safetyClass,
            status: status,
            detail: detail,
            duration: nil
        ))
    }

    private func upsertToolActivity(_ activity: ToolActivity) {
        let index = currentTurnAssistantIndex()
        if let activityIndex = messages[index].toolActivities.firstIndex(where: { $0.id == activity.id }) {
            messages[index].toolActivities[activityIndex] = activity
        } else {
            messages[index].toolActivities.append(activity)
        }
    }
}
