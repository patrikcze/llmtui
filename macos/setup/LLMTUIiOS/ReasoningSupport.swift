import Foundation

/// How the reasoning (brain) choice reaches the server, and how reasoning
/// that comes back is kept apart from the answer.
///
/// Servers and model templates disagree on the switch, so each choice is
/// sent the ways that are documented to work and are harmless elsewhere:
/// - `reasoning_effort` (top level): OpenAI, Ollama (maps it to the model's
///   own levels, "none" turns thinking off) and GPT-OSS.
/// - `chat_template_kwargs.enable_thinking`: the variable Qwen 3 and
///   Gemma 4 templates read. Gemma 4's template treats it as off when it is
///   missing, so a bare `reasoning_effort` never turns Gemma thinking on.
///   Sent to LM Studio and OpenAI-compatible servers (vLLM, llama.cpp,
///   mlx-lm), never to Ollama or api.openai.com, which reject or ignore it.
/// - Qwen 3.8's template accepts only low, medium and xhigh, and raises an
///   error for anything else, so High is sent as xhigh for that family.
/// Automatic sends nothing and leaves the model and server defaults alone.
nonisolated enum ReasoningRequest {
    static func fields(
        for reasoning: MobileReasoning,
        model: String,
        providerType: MobileProviderType,
        baseURL: String
    ) -> [String: Any] {
        let id = model.lowercased()
        let isGPTOSS = id.contains("gpt-oss") || id.contains("gpt_oss")
        let isOpenAI = URL(string: baseURL.trimmingCharacters(in: .whitespacesAndNewlines))?
            .host?.lowercased().hasSuffix("openai.com") == true
        let templateKwargs = providerType != .ollama && !isOpenAI && !isGPTOSS

        switch reasoning {
        case .automatic:
            return [:]
        case .off:
            if providerType == .ollama { return ["reasoning_effort": "none"] }
            // GPT-OSS cannot turn reasoning off; low is the least it does.
            if isGPTOSS { return ["reasoning_effort": "low"] }
            return templateKwargs ? ["chat_template_kwargs": ["enable_thinking": false]] : [:]
        case .low, .medium, .high:
            let effort = effort(for: reasoning, model: id)
            var fields: [String: Any] = ["reasoning_effort": effort]
            if templateKwargs {
                var kwargs: [String: Any] = ["enable_thinking": true]
                if isQwen38(id) { kwargs["reasoning_effort"] = effort }
                fields["chat_template_kwargs"] = kwargs
            }
            return fields
        }
    }

    /// The effort name the model's template accepts.
    static func effort(for reasoning: MobileReasoning, model: String) -> String {
        if reasoning == .high, isQwen38(model.lowercased()) { return "xhigh" }
        return reasoning.rawValue
    }

    /// Qwen 3.8 ids as servers name them: "qwen3.8-27b", "Qwen/Qwen3.8-27B",
    /// "qwen3.8:27b", "mlx-community/Qwen3_8-27B-4bit", "qwen-3.8".
    static func isQwen38(_ lowercasedModel: String) -> Bool {
        lowercasedModel.range(of: #"qwen-?3[._]8(?![0-9])"#, options: .regularExpression) != nil
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
