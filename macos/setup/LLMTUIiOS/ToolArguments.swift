import Foundation

/// Reads tool-call arguments the way models actually send them.
///
/// Some servers (LM Studio, for example) constrain a tool call to its JSON
/// schema, so required arguments are always there. Others only show the
/// schema to the model as text, and a small model then sends near misses:
/// `query` instead of `question`, a single string where a list belongs, a
/// number as text, or nothing at all. These helpers accept the common
/// alternatives, and when an argument is really missing they produce an
/// error that tells the model what arrived and shows a correct call, so it
/// can fix the call instead of repeating it.
nonisolated enum ToolArguments {
    /// The first non-empty text among `keys` (numbers are accepted as text).
    static func string(_ arguments: [String: Any], _ keys: [String]) -> String? {
        for key in keys {
            switch arguments[key] {
            case let text as String:
                let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
                if !trimmed.isEmpty { return trimmed }
            case let number as NSNumber:
                return number.stringValue
            default:
                continue
            }
        }
        return nil
    }

    /// A list of texts from an array, or from a single text.
    static func strings(_ value: Any?) -> [String] {
        switch value {
        case let list as [Any]:
            return list.compactMap { ($0 as? String)?.trimmingCharacters(in: .whitespacesAndNewlines) }.filter { !$0.isEmpty }
        case let text as String:
            let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
            return trimmed.isEmpty ? [] : [trimmed]
        default:
            return []
        }
    }

    /// A whole number from a number or from numeric text.
    static func int(_ arguments: [String: Any], _ keys: [String]) -> Int? {
        for key in keys {
            if let number = arguments[key] as? NSNumber { return number.intValue }
            if let text = arguments[key] as? String, let value = Int(text.trimmingCharacters(in: .whitespaces)) { return value }
        }
        return nil
    }

    /// The error for a missing required argument, written for the model.
    static func missing(tool: String, argument: String, example: String, received arguments: [String: Any]) -> String {
        "\(tool) needs \"\(argument)\" (text), but it was not in the call. \(describe(arguments)) "
            + "Call \(tool) again with arguments like \(example). Do not repeat a call that failed."
    }

    /// The error for a call to a tool that was not offered, listing the real ones.
    static func unknownTool(_ name: String, offered: Set<String>) -> String {
        let names = offered.sorted()
        let list = names.prefix(30).joined(separator: ", ")
        return "Error: There is no tool named \"\(String(name.prefix(80)))\" in this reply. "
            + (names.isEmpty ? "No tools are available; answer without tools." : "Use one of: \(list).")
    }

    /// What arrived, compact and bounded.
    static func describe(_ arguments: [String: Any]) -> String {
        guard !arguments.isEmpty,
              let data = try? JSONSerialization.data(withJSONObject: arguments, options: [.sortedKeys]),
              let text = String(data: data, encoding: .utf8)
        else { return "It received no arguments." }
        return "It received \(text.count > 200 ? String(text.prefix(200)) + "…" : text)."
    }
}
