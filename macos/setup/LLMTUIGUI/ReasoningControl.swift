import Foundation

/// Mirrors Go's `provider.ModelFamily` detection for the one family this
/// chat needs to special-case: GPT-OSS models don't have a plain on/off for
/// reasoning — only low/medium/high effort levels, with no way to fully
/// disable it. Everything else is treated generically.
enum ModelFamily: Equatable {
    case gptOSS
    case other

    static func detect(modelID: String, architecture: String?) -> ModelFamily {
        if WordBoundaryMatcher.matches("gpt-oss", in: modelID) || WordBoundaryMatcher.matches("gpt-oss", in: architecture ?? "") {
            return .gptOSS
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

/// What a model actually accepts for reasoning control, so the composer only
/// offers choices that make sense for the active model instead of a generic
/// on/off that a level-only (or non-reasoning) model would reject.
enum ReasoningCapability: Equatable {
    /// GPT-OSS family: low/medium/high effort, no full off.
    case levels
    /// A plain on/off, reported by the provider (e.g. LM Studio's
    /// `capabilities.reasoning`).
    case toggle
    /// The provider explicitly reported this model does not support
    /// reasoning control at all.
    case unsupported
    /// Not detected — no model metadata has been fetched for this model, or
    /// the provider doesn't expose the capability. Every choice is offered,
    /// marked as unconfirmed, rather than silently assuming none exist.
    case unknown
}

enum ReasoningCapabilityDetector {
    static func capability(modelID: String, metadata: ProviderModelMetadata?) -> ReasoningCapability {
        if ModelFamily.detect(modelID: modelID, architecture: metadata?.architecture) == .gptOSS {
            return .levels
        }
        switch metadata?.supportsReasoning {
        case true?: return .toggle
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
    case on
    case off
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

    /// The choices worth showing in the composer's reasoning menu for a
    /// given capability, in display order. A level-only model never offers
    /// "Off" — there isn't one.
    static func offered(for capability: ReasoningCapability) -> [ChatReasoningChoice] {
        switch capability {
        case .levels: [.automatic, .low, .medium, .high]
        case .toggle: [.automatic, .on, .off]
        case .unsupported: [.automatic]
        case .unknown: [.automatic, .on, .off, .low, .medium, .high]
        }
    }
}

/// Builds the extra top-level JSON body keys for a chosen reasoning setting,
/// mirroring the Go OpenAI-compatible provider's own encoding
/// (`internal/provider/openai/openai.go`): a GPT-OSS-family model gets
/// `reasoning_effort`, since that's the field llama.cpp/vLLM/Ollama's
/// OpenAI-compatible surface actually reads for it; any other model that
/// supports toggling reasoning gets `chat_template_kwargs.enable_thinking`,
/// which is how non-reasoning-native chat templates (Qwen3, …) expose it.
/// `.automatic` always sends nothing, leaving the request exactly as it
/// would be with no reasoning control at all.
enum ReasoningRequestEncoder {
    static func requestBody(for choice: ChatReasoningChoice, capability: ReasoningCapability) -> [String: Any] {
        switch capability {
        case .unsupported:
            return [:]
        case .levels:
            switch choice {
            case .low, .medium, .high: return ["reasoning_effort": choice.rawValue]
            case .automatic, .on, .off: return [:]
            }
        case .toggle, .unknown:
            switch choice {
            case .on: return ["chat_template_kwargs": ["enable_thinking": true]]
            case .off: return ["chat_template_kwargs": ["enable_thinking": false]]
            case .low, .medium, .high: return ["reasoning_effort": choice.rawValue]
            case .automatic: return [:]
            }
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
