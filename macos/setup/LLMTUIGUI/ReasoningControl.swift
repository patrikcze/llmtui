import Foundation

/// Model families that need special reasoning handling. Mirrors Go's
/// `provider.ModelFamily` for GPT-OSS (levels only, no off), plus Qwen 3.8,
/// whose template accepts low, medium and xhigh and rejects "high".
enum ModelFamily: Equatable {
    case gptOSS
    case qwen38
    case other

    static func detect(modelID: String, architecture: String?) -> ModelFamily {
        if WordBoundaryMatcher.matches("gpt-oss", in: modelID) || WordBoundaryMatcher.matches("gpt-oss", in: architecture ?? "") {
            return .gptOSS
        }
        if modelID.lowercased().range(of: #"qwen-?3[._]8(?![0-9])"#, options: .regularExpression) != nil {
            return .qwen38
        }
        return .other
    }
}

enum WordBoundaryMatcher {
    /// A case-insensitive substring match where neither character touching
    /// the match may be a letter — a digit is fine, so "gpt-oss20b" and
    /// "openai/gpt-oss-120b" both match, but "xgpt-ossy" does not. Mirrors
    /// Go's `provider.MatchesWordBoundary` (`internal/provider/model_protocol.go`)
    /// so this app's family detection agrees with the Go TUI's for the same
    /// model name.
    static func matches(_ needle: String, in haystack: String) -> Bool {
        let lowerHaystack = Array(haystack.lowercased())
        let lowerNeedle = Array(needle.lowercased())
        guard !lowerNeedle.isEmpty, lowerHaystack.count >= lowerNeedle.count else { return false }
        for start in 0...(lowerHaystack.count - lowerNeedle.count) {
            guard Array(lowerHaystack[start..<(start + lowerNeedle.count)]) == lowerNeedle else { continue }
            let beforeOK = start == 0 || !lowerHaystack[start - 1].isLetter
            let afterIndex = start + lowerNeedle.count
            let afterOK = afterIndex == lowerHaystack.count || !lowerHaystack[afterIndex].isLetter
            if beforeOK && afterOK { return true }
        }
        return false
    }
}

/// What a model accepts for reasoning control, so the composer offers only
/// choices that work and turns an unavailable one into the nearest that does.
enum ReasoningCapability: Equatable, Sendable {
    /// Effort levels. `canDisable` is false for GPT-OSS, which always
    /// reasons at least at low.
    case levels(canDisable: Bool)
    /// Only on and off, as LM Studio reports for Gemma 4 and most Qwen
    /// builds. `defaultOn` is the server's default when it reports one.
    case toggle(defaultOn: Bool?)
    /// The provider reported that this model has no reasoning control.
    case unsupported
    /// Not reported and not a known family: every choice is offered and
    /// sent as chosen.
    case unknown

    /// The capability from LM Studio's `capabilities.reasoning`:
    /// `allowedOptions` is empty when the model exposes none.
    static func lmStudio(allowedOptions: [String], defaultOption: String?) -> ReasoningCapability {
        let options = allowedOptions.map { $0.lowercased() }
        if options.isEmpty { return .unsupported }
        if options.contains(where: { ["low", "medium", "high"].contains($0) }) {
            return .levels(canDisable: options.contains("off"))
        }
        return .toggle(defaultOn: defaultOption.map { $0.lowercased() != "off" })
    }
}

enum ReasoningCapabilityDetector {
    /// GPT-OSS always gets levels without off, whatever a server reports;
    /// otherwise LM Studio's report wins, then the Qwen 3.8 family.
    static func capability(modelID: String, metadata: ProviderModelMetadata?) -> ReasoningCapability {
        let family = ModelFamily.detect(modelID: modelID, architecture: metadata?.architecture)
        if family == .gptOSS { return .levels(canDisable: false) }
        if let options = metadata?.reasoningOptions {
            return .lmStudio(allowedOptions: options, defaultOption: metadata?.reasoningDefault)
        }
        if family == .qwen38 { return .levels(canDisable: true) }
        switch metadata?.supportsReasoning {
        case true?: return .toggle(defaultOn: metadata?.defaultReasoningEnabled)
        case false?: return .unsupported
        case nil: return .unknown
        }
    }
}

/// The user's chosen reasoning setting for the next chat message. Chat-local
/// state only (see `AppModel.nativeReasoningChoice`) — never read from or
/// written to the YAML configuration shared with the Go `llmtui` app.
enum ChatReasoningChoice: String, CaseIterable, Equatable, Sendable {
    /// Send nothing; let the server/model use its own default.
    case automatic = "auto"
    case off
    case on
    case low
    case medium
    case high

    var title: String {
        switch self {
        case .automatic: "Default"
        case .on: "On"
        case .off: "Off"
        case .low: "Low"
        case .medium: "Medium"
        case .high: "High"
        }
    }

    /// Whether this setting asks the model to reason.
    var isThinking: Bool {
        switch self {
        case .automatic, .off: false
        case .on, .low, .medium, .high: true
        }
    }

    /// The short mark on the brain icon; Default has none.
    var badge: String? {
        switch self {
        case .automatic: nil
        case .off: "Off"
        case .on: "On"
        case .low: "L"
        case .medium: "M"
        case .high: "H"
        }
    }

    /// The choices worth showing in the composer's reasoning menu for a
    /// given capability, in display order.
    static func offered(for capability: ReasoningCapability) -> [ChatReasoningChoice] {
        switch capability {
        case .levels(let canDisable): canDisable ? allCases : [.automatic, .on, .low, .medium, .high]
        case .toggle: [.automatic, .off, .on]
        case .unsupported: [.automatic]
        case .unknown: allCases
        }
    }

    /// The setting actually used: Off becomes Low on a model that cannot
    /// stop reasoning, On becomes Medium on a level model (on, but not the
    /// deepest), and a level becomes On on an on/off model.
    func resolved(for capability: ReasoningCapability) -> ChatReasoningChoice {
        switch (capability, self) {
        case (_, .automatic), (.unknown, _): self
        case (.unsupported, _): .automatic
        case (.toggle, .low), (.toggle, .medium), (.toggle, .high): .on
        case (.toggle, _): self
        case (.levels(false), .off): .low
        case (.levels, .on): .medium
        case (.levels, _): self
        }
    }
}

/// The kind of server a chat request goes to, for reasoning fields.
enum ReasoningServer: Equatable, Sendable {
    /// LM Studio (detected from its `/api/v1/models` report).
    case lmStudio
    case ollama
    /// vLLM, llama.cpp, mlx-lm, … or, with `isOpenAI`, api.openai.com.
    case openAICompatible(isOpenAI: Bool)
}

/// Builds the extra top-level JSON body keys for a chosen reasoning setting,
/// as each server reads them (measured against LM Studio 0.4 and the
/// official model templates):
/// - LM Studio maps `reasoning_effort` onto the model's own reasoning
///   setting: "none" is off, a value the model does not list falls back to
///   on, "on"/"off" are rejected with HTTP 400, and
///   `chat_template_kwargs.enable_thinking` is ignored.
/// - Ollama maps `reasoning_effort` to the model's levels; "none" is off.
/// - Other OpenAI-compatible servers pass `chat_template_kwargs` to the
///   template: `enable_thinking` switches Qwen 3 and Gemma 4 (whose template
///   treats a missing value as off). Qwen 3.8 also reads the effort there,
///   with High sent as "xhigh". GPT-OSS gets top-level levels only.
/// `.automatic` always sends nothing.
enum ReasoningRequestEncoder {
    static func requestBody(
        for choice: ChatReasoningChoice,
        capability: ReasoningCapability,
        modelID: String = "",
        server: ReasoningServer = .openAICompatible(isOpenAI: false)
    ) -> [String: Any] {
        let reasoning = choice.resolved(for: capability)
        if reasoning == .automatic { return [:] }
        let family = ModelFamily.detect(modelID: modelID, architecture: nil)
        let level = effort(for: reasoning, family: family)

        switch server {
        case .lmStudio:
            switch reasoning {
            case .off: return ["reasoning_effort": "none"]
            case .on:
                // On by default: nothing to send. Otherwise any level name
                // turns an on/off model on ("on" itself is rejected).
                if case .toggle(defaultOn: true) = capability { return [:] }
                return ["reasoning_effort": "medium"]
            default: return ["reasoning_effort": reasoning.rawValue]
            }
        case .ollama:
            return ["reasoning_effort": reasoning == .off ? "none" : level]
        case .openAICompatible(let isOpenAI):
            if isOpenAI || family == .gptOSS {
                return reasoning == .off ? [:] : ["reasoning_effort": level]
            }
            switch reasoning {
            case .off: return ["chat_template_kwargs": ["enable_thinking": false]]
            case .on: return ["chat_template_kwargs": ["enable_thinking": true]]
            default:
                var kwargs: [String: Any] = ["enable_thinking": true]
                if family == .qwen38 { kwargs["reasoning_effort"] = level }
                return ["reasoning_effort": level, "chat_template_kwargs": kwargs]
            }
        }
    }

    /// The effort name for a resolved setting: On is medium, and Qwen 3.8
    /// calls High "xhigh".
    static func effort(for reasoning: ChatReasoningChoice, family: ModelFamily) -> String {
        switch reasoning {
        case .high: family == .qwen38 ? "xhigh" : "high"
        case .low, .medium: reasoning.rawValue
        default: "medium"
        }
    }
}

/// Remembers, per provider+model, that the server rejected a request that
/// included reasoning-control keys (HTTP 400), so later turns in this
/// session skip sending them instead of failing every message — the same
/// pattern `ToolSupportTracker` uses for tool definitions.
actor ReasoningSupportTracker {
    static let shared = ReasoningSupportTracker()
    private var unsupported: Set<String> = []

    func isUnsupported(_ key: String) -> Bool { unsupported.contains(key) }
    func markUnsupported(_ key: String) { unsupported.insert(key) }
}
