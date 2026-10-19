import Foundation

/// What the active model accepts for reasoning control. It decides which
/// brain-menu choices are offered and what an unavailable choice becomes.
///
/// Sources, best first: LM Studio's `capabilities.reasoning.allowed_options`
/// from `/api/v1/models`; then the model family (GPT-OSS has levels but no
/// off; Qwen 3.8 has off and levels); otherwise unknown.
nonisolated enum ReasoningCapability: Equatable, Sendable {
    /// Nothing is known: every choice is offered and sent as chosen.
    case unknown
    /// The server reports no reasoning control for this model.
    case unsupported
    /// Only on and off (LM Studio's Gemma 4, most Qwen builds).
    /// `defaultOn` is the server's default when it reports one.
    case toggle(defaultOn: Bool?)
    /// Effort levels; `canDisable` is false for GPT-OSS, which always
    /// reasons at least at low.
    case levels(canDisable: Bool)

    /// The brain-menu choices for this capability, in menu order.
    var offered: [MobileReasoning] {
        switch self {
        case .unknown: MobileReasoning.allCases
        case .unsupported: [.automatic]
        case .toggle: [.automatic, .off, .on]
        case .levels(let canDisable): canDisable ? MobileReasoning.allCases : [.automatic, .on, .low, .medium, .high]
        }
    }

    /// The setting actually used for `choice`: Off becomes Low on a model
    /// that cannot stop reasoning, On becomes Medium on a level model (on,
    /// but not the deepest), and a level becomes On on an on/off model.
    func resolve(_ choice: MobileReasoning) -> MobileReasoning {
        switch (self, choice) {
        case (_, .automatic), (.unknown, _): choice
        case (.unsupported, _): .automatic
        case (.toggle, .low), (.toggle, .medium), (.toggle, .high): .on
        case (.toggle, _): choice
        case (.levels(false), .off): .low
        case (.levels, .on): .medium
        case (.levels, _): choice
        }
    }

    /// The capability from LM Studio's model entry: `allowedOptions` is nil
    /// when the entry has no reasoning configuration.
    static func lmStudio(allowedOptions: [String]?, defaultOption: String?) -> ReasoningCapability {
        guard let options = allowedOptions?.map({ $0.lowercased() }), !options.isEmpty else { return .unsupported }
        if options.contains(where: { ["low", "medium", "high"].contains($0) }) {
            return .levels(canDisable: options.contains("off"))
        }
        return .toggle(defaultOn: defaultOption.map { $0.lowercased() != "off" })
    }

    /// The capability known from the model id alone.
    static func family(model: String) -> ReasoningCapability {
        let id = model.lowercased()
        if ReasoningRequest.isGPTOSS(id) { return .levels(canDisable: false) }
        if ReasoningRequest.isQwen38(id) { return .levels(canDisable: true) }
        return .unknown
    }
}

/// Turns a brain-menu choice into request fields, as each server reads it.
/// Measured against LM Studio 0.4 and the official model templates:
/// - LM Studio maps `reasoning_effort` onto the model's own reasoning
///   setting: "none" is off, a value the model does not list falls back to
///   on, and "on"/"off" are rejected with HTTP 400. It ignores
///   `chat_template_kwargs.enable_thinking`.
/// - Ollama maps `reasoning_effort` to the model's levels; "none" is off.
/// - Other OpenAI-compatible servers (vLLM, llama.cpp, mlx-lm) pass
///   `chat_template_kwargs` to the template: `enable_thinking` switches
///   Qwen 3 and Gemma 4 (whose template treats a missing value as off).
/// - Qwen 3.8's template accepts low, medium and xhigh only, so High is
///   sent as xhigh; GPT-OSS gets top-level levels and never the kwargs.
/// Automatic sends nothing, leaving the model and server defaults alone.
nonisolated enum ReasoningRequest {
    static func fields(
        for choice: MobileReasoning,
        capability: ReasoningCapability,
        model: String,
        providerType: MobileProviderType,
        baseURL: String
    ) -> [String: Any] {
        let id = model.lowercased()
        let reasoning = capability.resolve(choice)
        if reasoning == .automatic { return [:] }
        let level = effort(for: reasoning, model: id)

        switch providerType {
        case .lmStudio:
            switch reasoning {
            case .off: return ["reasoning_effort": "none"]
            case .on:
                // An on/off model that is on by default needs nothing; any
                // level name turns one on (LM Studio falls back to "on").
                if case .toggle(defaultOn: true) = capability { return [:] }
                return ["reasoning_effort": "medium"]
            default: return ["reasoning_effort": reasoning.rawValue]
            }
        case .ollama:
            return ["reasoning_effort": reasoning == .off ? "none" : level]
        case .openAICompatible:
            let isOpenAI = URL(string: baseURL.trimmingCharacters(in: .whitespacesAndNewlines))?
                .host?.lowercased().hasSuffix("openai.com") == true
            if isOpenAI || isGPTOSS(id) {
                return reasoning == .off ? [:] : ["reasoning_effort": level]
            }
            switch reasoning {
            case .off: return ["chat_template_kwargs": ["enable_thinking": false]]
            case .on: return ["chat_template_kwargs": ["enable_thinking": true]]
            default:
                var kwargs: [String: Any] = ["enable_thinking": true]
                if isQwen38(id) { kwargs["reasoning_effort"] = level }
                return ["reasoning_effort": level, "chat_template_kwargs": kwargs]
            }
        }
    }

    /// The effort name for a resolved setting: On is medium, and Qwen 3.8's
    /// template calls High "xhigh".
    static func effort(for reasoning: MobileReasoning, model: String) -> String {
        switch reasoning {
        case .high: isQwen38(model.lowercased()) ? "xhigh" : "high"
        case .low, .medium: reasoning.rawValue
        default: "medium"
        }
    }

    static func isGPTOSS(_ lowercasedModel: String) -> Bool {
        lowercasedModel.contains("gpt-oss") || lowercasedModel.contains("gpt_oss")
    }

    /// Qwen 3.8 ids as servers name them: "qwen3.8-27b", "Qwen/Qwen3.8-27B",
    /// "qwen3.8:27b", "mlx-community/Qwen3_8-27B-4bit", "qwen-3.8".
    static func isQwen38(_ lowercasedModel: String) -> Bool {
        lowercasedModel.range(of: #"qwen-?3[._]8(?![0-9])"#, options: .regularExpression) != nil
    }
}

/// Reads LM Studio's reasoning options for a model from `/api/v1/models`.
/// Only the provider's own server is asked, with its own key; any failure
/// means "not reported".
struct ReasoningProbe {
    var session: URLSession = .shared

    func capability(profile: MobileProviderProfile, apiKey: String) async -> ReasoningCapability? {
        guard profile.type == .lmStudio, !profile.model.isEmpty,
              let base = ContextWindowProbe().serverRoot(profile.baseURL) else { return nil }
        var request = URLRequest(url: base.appending(path: "api/v1/models"))
        request.timeoutInterval = 10
        let key = apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if !key.isEmpty { request.setValue("Bearer \(key)", forHTTPHeaderField: "Authorization") }
        guard let (data, response) = try? await session.data(for: request),
              let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode)
        else { return nil }
        return Self.parse(data, model: profile.model)
    }

    /// The capability of `model` in an `/api/v1/models` response, matched by
    /// key, loaded instance id, or a unique last path component.
    static func parse(_ data: Data, model: String) -> ReasoningCapability? {
        guard let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let items = object["models"] as? [[String: Any]] else { return nil }
        let models = items.filter { ($0["type"] as? String ?? "llm") == "llm" }
        func ids(_ item: [String: Any]) -> [String] {
            let instances = (item["loaded_instances"] as? [[String: Any]] ?? []).compactMap { $0["id"] as? String }
            return [item["key"] as? String].compactMap { $0 } + instances
        }
        let direct = models.first { ids($0).contains(model) }
        let aliases = models.filter { ids($0).contains { $0.split(separator: "/").last.map(String.init) == model } }
        guard let item = direct ?? (aliases.count == 1 ? aliases[0] : nil) else { return nil }
        let reasoning = (item["capabilities"] as? [String: Any])?["reasoning"] as? [String: Any]
        return .lmStudio(
            allowedOptions: reasoning?["allowed_options"] as? [String],
            defaultOption: reasoning?["default"] as? String
        )
    }
}


/// Separates a reasoning block a server leaves inside the reply text from
/// the answer, the iOS counterpart of the Go `provider.ThinkFilter`. Only a
/// block that opens the reply (after optional whitespace) counts:
/// `<think>…</think>` (Qwen, DeepSeek) or `<|channel>thought…<channel|>`
/// (Gemma 4). The same text later in an answer is left alone. A partial tag
/// split across stream chunks is held back until it is decided.
nonisolated struct ReasoningSplitter: Sendable {
    private static let pairs: [(open: String, close: String)] = [
        ("<think>", "</think>"),
        ("<|channel>thought", "<channel|>")
    ]

    private enum State: Sendable {
        case deciding
        case inside(close: String)
        case passthrough
    }

    private var state = State.deciding
    private var pending = ""

    /// Consumes one content chunk. Either part may be empty while a
    /// possible tag is buffered.
    mutating func feed(_ chunk: String) -> (answer: String, reasoning: String) {
        if case .passthrough = state { return (chunk, "") }
        pending += chunk
        if case .deciding = state {
            let trimmed = pending.drop { $0.isWhitespace }
            if trimmed.isEmpty { return ("", "") }
            if let pair = Self.pairs.first(where: { trimmed.hasPrefix($0.open) }) {
                state = .inside(close: pair.close)
                pending = String(trimmed.dropFirst(pair.open.count))
            } else if Self.pairs.contains(where: { $0.open.hasPrefix(trimmed) }) {
                return ("", "")
            } else {
                state = .passthrough
                defer { pending = "" }
                return (pending, "")
            }
        }
        guard case .inside(let close) = state else { return ("", "") }
        if let range = pending.range(of: close) {
            let reasoning = String(pending[..<range.lowerBound])
            let answer = String(pending[range.upperBound...].drop { $0 == "\n" })
            pending = ""
            state = .passthrough
            return (answer, reasoning)
        }
        // Hold back the longest tail that could start the closing tag.
        var hold = 0
        for length in stride(from: min(pending.count, close.count - 1), to: 0, by: -1)
        where close.hasPrefix(pending.suffix(length)) {
            hold = length
            break
        }
        let reasoning = String(pending.dropLast(hold))
        pending = String(pending.suffix(hold))
        return ("", reasoning)
    }

    /// Ends the stream: undecided text is answer; a held-back tail inside an
    /// unclosed block is reasoning.
    mutating func finish() -> (answer: String, reasoning: String) {
        defer { pending = ""; state = .passthrough }
        switch state {
        case .deciding: return (pending, "")
        case .inside: return ("", pending)
        case .passthrough: return ("", "")
        }
    }
}
