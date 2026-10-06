import Foundation

/// One remembered preference. Field names, and `tags` being omitted when
/// empty, mirror the Go `internal/memory.Snippet` struct exactly: this store
/// reads and writes the very same `memory.yaml` file the `llmtui` terminal
/// app uses, not a separate one, so a snippet saved from this chat and one
/// saved from `llmtui` end up in the same list either app can see.
struct MemorySnippet: Equatable, Sendable {
    let id: String
    var text: String
    let createdAt: Date
    var updatedAt: Date
    var tags: [String]
}

enum MemoryStoreError: LocalizedError {
    case emptyText
    case notFound(String)
    case looksLikeSecret

    var errorDescription: String? {
        switch self {
        case .emptyText: "Memory text is empty."
        case .notFound(let id): "No memory snippet with id \"\(id)\"."
        case .looksLikeSecret: "This looks like a secret (an API key, password, or token) and was not saved. Memory is for preferences, not credentials."
        }
    }
}

/// Reads and writes `~/.local/share/llmtui/memory.yaml`: the same path,
/// format, and behavior (100-snippet cap evicting the oldest, 4+ character
/// ID-prefix removal, keyword-overlap `relevant` scoring with the same
/// stopword list) as the Go `internal/memory` package, so this is a second
/// reader/writer of that one file rather than a competing store. See
/// `internal/memory/memory.go` for the authoritative behavior this mirrors.
actor MemoryStore {
    static let defaultPath = "~/.local/share/llmtui/memory.yaml"
    static let shared = MemoryStore()

    private let path: String
    private let maxSnippets: Int

    init(path: String = MemoryStore.defaultPath, maxSnippets: Int = 100) {
        self.path = (path as NSString).expandingTildeInPath
        self.maxSnippets = max(maxSnippets, 1)
    }

    func load() throws -> [MemorySnippet] {
        guard FileManager.default.fileExists(atPath: path) else { return [] }
        let text = try String(contentsOfFile: path, encoding: .utf8)
        return MemoryYAML.parse(text)
    }

    @discardableResult
    func add(text: String, tags: [String] = []) throws -> MemorySnippet {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { throw MemoryStoreError.emptyText }
        guard !SecretHeuristics.looksLikeSecret(trimmed) else { throw MemoryStoreError.looksLikeSecret }

        var snippets = try load()
        let now = Date()
        let snippet = MemorySnippet(id: Self.newID(), text: trimmed, createdAt: now, updatedAt: now, tags: tags)
        snippets.append(snippet)
        if snippets.count > maxSnippets {
            snippets.sort { $0.createdAt < $1.createdAt }
            snippets.removeFirst(snippets.count - maxSnippets)
        }
        try save(snippets)
        return snippet
    }

    /// Removes a snippet by exact ID, or by an unambiguous prefix of 4 or
    /// more characters — matching Go's `Store.Remove`.
    func remove(id: String) throws {
        var snippets = try load()
        let before = snippets.count
        snippets.removeAll { $0.id == id || (id.count >= 4 && $0.id.hasPrefix(id)) }
        guard snippets.count < before else { throw MemoryStoreError.notFound(id) }
        try save(snippets)
    }

    /// Up to `limit` snippets whose words overlap `query`, best matches
    /// first — the same keyword scoring as Go's `memory.Relevant`.
    func relevant(to query: String, limit: Int) throws -> [MemorySnippet] {
        guard limit > 0 else { return [] }
        let snippets = try load()
        guard !snippets.isEmpty else { return [] }
        let queryWords = Self.keywordSet(query)
        let scored: [(MemorySnippet, Int)] = snippets.compactMap { snippet in
            let score = Self.keywordSet(snippet.text).intersection(queryWords).count
            return score > 0 ? (snippet, score) : nil
        }
        return scored
            .sorted { $0.1 > $1.1 }
            .prefix(limit)
            .map(\.0)
    }

    private func save(_ snippets: [MemorySnippet]) throws {
        let directory = (path as NSString).deletingLastPathComponent
        try FileManager.default.createDirectory(
            atPath: directory,
            withIntermediateDirectories: true,
            attributes: [.posixPermissions: 0o700]
        )
        let text = MemoryYAML.render(snippets)
        let temporary = directory + "/." + UUID().uuidString + ".memory.yaml.tmp"
        try text.write(toFile: temporary, atomically: true, encoding: .utf8)
        try FileManager.default.setAttributes([.posixPermissions: 0o600], ofItemAtPath: temporary)
        if FileManager.default.fileExists(atPath: path) {
            _ = try FileManager.default.replaceItem(
                at: URL(fileURLWithPath: path),
                withItemAt: URL(fileURLWithPath: temporary),
                backupItemName: nil,
                resultingItemURL: nil
            )
        } else {
            try FileManager.default.moveItem(atPath: temporary, toPath: path)
        }
    }

    private static func newID() -> String {
        (0..<4).map { _ in String(format: "%02x", UInt8.random(in: 0...255)) }.joined()
    }

    private static let stopwords: Set<String> = [
        "the", "a", "an", "and", "or", "to", "of", "in", "for", "is", "are", "i",
        "me", "my", "you", "it", "with", "use", "using", "please", "prefer",
        "when", "how", "what", "can", "do", "does"
    ]

    /// Matches Go's `memory.keywordSet`: lowercase word runs of letters,
    /// digits, `.`, or `-`, at least 2 characters, minus stopwords.
    private static func keywordSet(_ text: String) -> Set<String> {
        var words = Set<String>()
        var current = ""
        func flush() {
            if current.count >= 2, !stopwords.contains(current) { words.insert(current) }
            current = ""
        }
        for scalar in text.lowercased().unicodeScalars {
            if ("a"..."z").contains(scalar) || ("0"..."9").contains(scalar) || scalar == "." || scalar == "-" {
                current.unicodeScalars.append(scalar)
            } else {
                flush()
            }
        }
        flush()
        return words
    }
}

/// A best-effort, content-based check for obvious credential material, so an
/// accidental "remember my API key sk-..." doesn't get written to a plain
/// local YAML file. Not a substitute for not pasting secrets into chat in
/// the first place — just a last line of defense for this one tool.
enum SecretHeuristics {
    private static let knownPrefixes = [
        "sk-", "pk-", "ghp_", "gho_", "ghu_", "ghs_", "ghr_", "github_pat_",
        "AKIA", "ASIA", "xox", "AIza", "glpat-", "npm_"
    ]

    static func looksLikeSecret(_ text: String) -> Bool {
        let compact = text.trimmingCharacters(in: .whitespacesAndNewlines)
        if knownPrefixes.contains(where: compact.contains) { return true }
        if compact.range(of: #"(?i)\b(api[_-]?key|secret|password|passwd|token|bearer)\b\s*[:=]\s*\S{6,}"#, options: .regularExpression) != nil {
            return true
        }
        // A single long run of base64/hex-alphabet characters with no
        // whitespace reads as a pasted key/token rather than a preference.
        if compact.range(of: #"^[A-Za-z0-9+/_=\-\.]{24,}$"#, options: .regularExpression) != nil {
            return true
        }
        return false
    }
}

/// A minimal reader/writer for the one shape of YAML this file ever holds: a
/// top-level block list of flat maps with a handful of known string/string-
/// array fields. Not a general YAML library — just enough to read back
/// whatever Go's yaml.v3 (or this file's own `render`) wrote for a
/// `[]memory.Snippet`, including quoted and block-scalar text values.
enum MemoryYAML {
    static func render(_ snippets: [MemorySnippet]) -> String {
        guard !snippets.isEmpty else { return "[]\n" }
        var lines: [String] = []
        for snippet in snippets {
            lines.append("- id: \(snippet.id)")
            lines.append("  text: \(quoted(snippet.text))")
            lines.append("  created_at: \(iso(snippet.createdAt))")
            lines.append("  updated_at: \(iso(snippet.updatedAt))")
            if !snippet.tags.isEmpty {
                lines.append("  tags:")
                for tag in snippet.tags {
                    lines.append("    - \(quoted(tag))")
                }
            }
        }
        return lines.joined(separator: "\n") + "\n"
    }

    static func parse(_ text: String) -> [MemorySnippet] {
        var records: [[String: Any]] = []
        var current: [String: Any] = [:]
        var recordIndent: Int?
        var listKey: String?
        var listIndent: Int?
        var blockKey: String?
        var blockStrip = false
        var blockIndent: Int?
        var blockLines: [String] = []

        func finishBlock() {
            guard let key = blockKey else { return }
            var value = blockLines.joined(separator: "\n")
            if !blockStrip, !value.isEmpty { value += "\n" }
            current[key] = value
            blockKey = nil
            blockIndent = nil
            blockLines = []
        }

        func finishRecord() {
            finishBlock()
            if !current.isEmpty { records.append(current) }
            current = [:]
            listKey = nil
            listIndent = nil
        }

        /// Handles one "key: value" fragment (value may be empty, a
        /// block-scalar marker, or a real scalar) whose key starts at column
        /// `keyIndent` — the column nested tag-list items and block-scalar
        /// content lines are compared against to know they belong to it.
        func handleField(_ fieldText: String, keyIndent: Int) {
            guard let colon = fieldText.firstIndex(of: ":") else { return }
            let key = String(fieldText[fieldText.startIndex..<colon]).trimmingCharacters(in: .whitespaces)
            let rawValue = String(fieldText[fieldText.index(after: colon)...]).trimmingCharacters(in: .whitespaces)
            listKey = nil
            listIndent = nil
            if rawValue.isEmpty {
                listKey = key
                listIndent = keyIndent
                current[key] = [String]()
            } else if ["|", "|-", ">", ">-"].contains(rawValue) {
                blockKey = key
                blockStrip = rawValue.hasSuffix("-")
                blockIndent = nil
                blockLines = []
            } else {
                current[key] = scalarValue(rawValue)
            }
        }

        for rawLine in text.components(separatedBy: "\n") {
            let indent = rawLine.prefix { $0 == " " }.count
            let trimmed = rawLine.trimmingCharacters(in: .whitespaces)

            if blockKey != nil {
                if trimmed.isEmpty {
                    blockLines.append("")
                    continue
                }
                if let bi = blockIndent, indent >= bi {
                    blockLines.append(String(rawLine.dropFirst(bi)))
                    continue
                }
                if blockIndent == nil, indent > (recordIndent ?? -1) {
                    blockIndent = indent
                    blockLines.append(String(rawLine.dropFirst(indent)))
                    continue
                }
                finishBlock()
            }

            if trimmed.isEmpty { continue }

            let isDash = trimmed.hasPrefix("- ") || trimmed == "-"
            if isDash, let activeKey = listKey, let li = listIndent, indent > li {
                let itemText = trimmed == "-" ? "" : String(trimmed.dropFirst(2))
                var array = (current[activeKey] as? [String]) ?? []
                array.append(scalarValue(itemText))
                current[activeKey] = array
                continue
            }

            if isDash {
                if recordIndent == nil { recordIndent = indent }
                guard indent == recordIndent else { continue }
                finishRecord()
                let rest = trimmed == "-" ? "" : String(trimmed.dropFirst(2))
                if !rest.isEmpty { handleField(rest, keyIndent: indent + 2) }
                continue
            }

            handleField(trimmed, keyIndent: indent)
        }
        finishRecord()

        return records.compactMap { record in
            guard let id = record["id"] as? String, let text = record["text"] as? String else { return nil }
            let created = (record["created_at"] as? String).flatMap(parseDate) ?? Date()
            let updated = (record["updated_at"] as? String).flatMap(parseDate) ?? created
            let tags = (record["tags"] as? [String]) ?? []
            return MemorySnippet(id: id, text: text, createdAt: created, updatedAt: updated, tags: tags)
        }
    }

    private static func iso(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.string(from: date)
    }

    private static func parseDate(_ value: String) -> Date? {
        let withFraction = ISO8601DateFormatter()
        withFraction.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        if let date = withFraction.date(from: value) { return date }
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        return plain.date(from: value)
    }

    /// Decodes one YAML scalar: double-quoted (JSON-style escapes),
    /// single-quoted (`''` is an escaped quote), or a bare plain scalar.
    private static func scalarValue(_ raw: String) -> String {
        guard let first = raw.first, raw.count >= 2, raw.last == first else { return raw }
        if first == "\"" {
            return unescapeDoubleQuoted(String(raw.dropFirst().dropLast()))
        }
        if first == "'" {
            return String(raw.dropFirst().dropLast()).replacingOccurrences(of: "''", with: "'")
        }
        return raw
    }

    /// Double-quotes `value` with JSON-style escaping. This is valid YAML
    /// for any content (colons, newlines, a leading "-", …) and round-trips
    /// through this file's own parser and through Go's yaml.v3 reader —
    /// it just isn't byte-identical to what yaml.v3 itself would emit for a
    /// plain scalar that needed no escaping.
    private static func quoted(_ value: String) -> String {
        var result = "\""
        for scalar in value.unicodeScalars {
            switch scalar {
            case "\"": result += "\\\""
            case "\\": result += "\\\\"
            case "\n": result += "\\n"
            case "\t": result += "\\t"
            default: result.unicodeScalars.append(scalar)
            }
        }
        result += "\""
        return result
    }

    private static func unescapeDoubleQuoted(_ value: String) -> String {
        var result = ""
        var iterator = value.makeIterator()
        while let char = iterator.next() {
            guard char == "\\" else { result.append(char); continue }
            guard let next = iterator.next() else { result.append(char); break }
            switch next {
            case "n": result.append("\n")
            case "t": result.append("\t")
            case "\"": result.append("\"")
            case "\\": result.append("\\")
            default: result.append(next)
            }
        }
        return result
    }
}
