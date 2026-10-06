import Foundation

/// A model whose server rejects the OpenAI-compatible `tools` request field
/// — some do, outright, with an HTTP 400 — doesn't have to lose tool access
/// entirely. It can still use tools through a plain-text convention several
/// open fine-tunes (Hermes, Qwen's text mode, …) are themselves trained on:
/// writing `<tool_call>{"name": "...", "arguments": {...}}</tool_call>` in
/// its reply instead of a structured `tool_calls` field. This isn't a new
/// format invented here — it's the same one those models already expect.
enum TextToolCallParser {
    struct ParsedCall {
        let name: String
        let argumentsJSON: String
    }

    /// Extracts every `<tool_call>…</tool_call>` block (case-insensitive,
    /// tolerant of a ```json fence inside it, and of a model that forgot
    /// the closing tag on the very last block in a response) from `text`,
    /// and the text with those blocks removed — so any surrounding prose
    /// the model wrote is preserved as its actual answer.
    static func extract(from text: String) -> (calls: [ParsedCall], remainingText: String) {
        guard let closedPattern = try? NSRegularExpression(
            pattern: "<tool_call>(.*?)</tool_call>",
            options: [.caseInsensitive, .dotMatchesLineSeparators]
        ) else { return ([], text) }

        let fullRange = NSRange(text.startIndex..., in: text)
        let closedMatches = closedPattern.matches(in: text, range: fullRange)

        var calls: [ParsedCall] = []
        for match in closedMatches {
            guard let innerRange = Range(match.range(at: 1), in: text) else { continue }
            if let call = parseOne(String(text[innerRange])) { calls.append(call) }
        }
        var remaining = closedPattern.stringByReplacingMatches(in: text, range: fullRange, withTemplate: "")

        // A response cut short at the token limit can end mid-block, with
        // no closing tag at all — if there were no *closed* blocks to act
        // on, still try the dangling opening tag through to the end of the
        // text, rather than silently treating it as plain prose.
        if calls.isEmpty, let openRange = remaining.range(of: "<tool_call>", options: [.caseInsensitive]) {
            let inner = String(remaining[openRange.upperBound...])
            if let call = parseOne(inner) {
                calls.append(call)
                remaining.removeSubrange(openRange.lowerBound..<remaining.endIndex)
            }
        }

        return (calls, remaining.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    private static func parseOne(_ raw: String) -> ParsedCall? {
        var body = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        if body.hasPrefix("```") {
            body = body
                .replacingOccurrences(of: "```json", with: "", options: [.caseInsensitive])
                .replacingOccurrences(of: "```", with: "")
                .trimmingCharacters(in: .whitespacesAndNewlines)
        }
        guard let data = body.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let name = (object["name"] as? String)?.trimmingCharacters(in: .whitespacesAndNewlines),
              !name.isEmpty else {
            return nil
        }

        let argumentsJSON: String
        switch object["arguments"] {
        case let string as String:
            argumentsJSON = string
        case let value? where JSONSerialization.isValidJSONObject(value):
            argumentsJSON = (try? JSONSerialization.data(withJSONObject: value)).flatMap { String(data: $0, encoding: .utf8) } ?? "{}"
        default:
            argumentsJSON = "{}"
        }
        return ParsedCall(name: name, argumentsJSON: argumentsJSON)
    }

    /// The system-prompt section describing this convention. Used *instead
    /// of* (never alongside) the native toolInstructions section, which
    /// explicitly tells the model to call tools "never by writing JSON ...
    /// in your reply text" — exactly backwards for a model using this path.
    static func instructions(for tools: [ToolDefinition]) -> String {
        let toolLines = tools.map { tool -> String in
            let paramNames: String
            if case .object(let schema) = tool.function.parameters,
               case .object(let properties)? = schema["properties"], !properties.isEmpty {
                paramNames = properties.keys.sorted().joined(separator: ", ")
            } else {
                paramNames = "no parameters"
            }
            return "- \(tool.function.name) (\(paramNames)): \(tool.function.description)"
        }.joined(separator: "\n")

        var lines = [
            "",
            "## Tool calling (text mode)",
            "This server doesn't accept structured function-calling, so tools are called through plain text instead of a native tool_calls field. To call a tool, write exactly this in your reply, with nothing else around it:",
            #"<tool_call>{"name": "tool_name", "arguments": {"param": "value"}}</tool_call>"#,
            "",
            "Rules:",
            "- Exactly one <tool_call> block per reply. If you need several tools, call one, wait for its result, then call the next.",
            "- The result comes back to you as a following message starting with <tool_result>. Wait for it before calling another tool or giving your final answer — never assume what a tool will return.",
            "- Do not put a <tool_call> block and your final answer in the same reply; the tool hasn't run yet when you write that reply.",
            "- If no tool is needed, just answer normally with no <tool_call> block at all.",
            "",
            "Available tools:",
            toolLines
        ]
        // Per-category guidance (web research, file edits, memory, …) is
        // identical to the native path — only how a call is written
        // differs, not how or when to use each tool.
        lines.append(contentsOf: LLMTUIDocumentationContext.categoryGuidance(for: Set(tools.map(\.function.name))))
        return lines.joined(separator: "\n")
    }
}
