import Foundation

/// A line-ending-aware view of a text file's content.
///
/// Splitting on `\n` only (never `String.components(separatedBy: .newlines)`,
/// which also breaks on `\r` alone) and remembering whether the file used
/// `\r\n` keeps a CRLF file from gaining phantom blank lines or shifted line
/// numbers when it's read back a second time. The original trailing-newline
/// state is preserved too, so round-tripping an unchanged file produces byte
/// identical output.
struct TextDocument: Equatable {
    enum LineEnding: Equatable {
        case lf
        case crlf

        var string: String { self == .lf ? "\n" : "\r\n" }
    }

    /// Content lines, without their terminators.
    private(set) var lines: [String]
    let lineEnding: LineEnding
    /// Whether the original text ended with a line terminator after its last line.
    let trailingNewline: Bool

    var lineCount: Int { lines.count }

    init(_ text: String) {
        lineEnding = text.contains("\r\n") ? .crlf : .lf
        let normalized = text.replacingOccurrences(of: "\r\n", with: "\n")
        guard !normalized.isEmpty else {
            lines = []
            trailingNewline = false
            return
        }
        trailingNewline = normalized.hasSuffix("\n")
        var body = normalized
        if trailingNewline { body.removeLast() }
        lines = body.components(separatedBy: "\n")
    }

    /// The reconstructed file text, using the original line ending and
    /// trailing-newline state.
    var text: String {
        guard !lines.isEmpty else { return "" }
        var result = lines.joined(separator: lineEnding.string)
        if trailingNewline { result += lineEnding.string }
        return result
    }

    /// The 1-based line, or nil if out of range.
    func line(_ number: Int) -> String? {
        guard number >= 1, number <= lines.count else { return nil }
        return lines[number - 1]
    }

    /// Returns up to `limit` lines starting at the 1-based `offset`, clamped
    /// to the document, along with the 1-based line number the slice starts at.
    func slice(from offset: Int, limit: Int) -> (start: Int, lines: [String]) {
        let start = max(offset, 1)
        guard start <= lines.count else { return (start, []) }
        let endExclusive = min(start - 1 + max(limit, 0), lines.count)
        return (start, Array(lines[(start - 1)..<endExclusive]))
    }

    /// Replaces the inclusive 1-based range [start, end] with `newLines`.
    /// Both bounds must already exist in the document.
    mutating func replaceLines(start: Int, end: Int, with newLines: [String]) throws {
        guard start >= 1, end >= start, end <= lines.count else {
            throw FileEditingError.lineRangeOutOfBounds(requestedStart: start, requestedEnd: end, lineCount: lines.count)
        }
        lines.replaceSubrange((start - 1)..<end, with: newLines)
    }

    /// Inserts `newLines` immediately after the 1-based `lineNumber`. Use 0
    /// to insert at the top of the file.
    mutating func insertLines(after lineNumber: Int, newLines: [String]) throws {
        guard lineNumber >= 0, lineNumber <= lines.count else {
            throw FileEditingError.lineRangeOutOfBounds(requestedStart: lineNumber, requestedEnd: lineNumber, lineCount: lines.count)
        }
        lines.insert(contentsOf: newLines, at: lineNumber)
    }

    /// Deletes the inclusive 1-based range [start, end].
    mutating func deleteLines(start: Int, end: Int) throws {
        guard start >= 1, end >= start, end <= lines.count else {
            throw FileEditingError.lineRangeOutOfBounds(requestedStart: start, requestedEnd: end, lineCount: lines.count)
        }
        lines.removeSubrange((start - 1)..<end)
    }
}

/// One text replacement for `edit_file`. Several can be applied together as
/// one atomic edit — if any one fails to match, none of them are written.
struct StringEdit: Equatable {
    let oldText: String
    let newText: String
    let replaceAll: Bool
}

enum LineEditOperation: String {
    case replace
    case insert
    case delete
}

enum FileEditingError: LocalizedError {
    case noMatch(path: String, context: String)
    case multipleMatches(path: String, count: Int, lineNumbers: [Int])
    case emptyOldTextOnNonEmptyFile(path: String)
    case lineRangeOutOfBounds(requestedStart: Int, requestedEnd: Int, lineCount: Int)
    case staleRead(path: String)
    case notReadThisTurn(path: String)

    var errorDescription: String? {
        switch self {
        case .noMatch(let path, let context):
            "old_text did not match anything in \(path). \(context)"
        case .multipleMatches(let path, let count, let lineNumbers):
            "old_text matched \(count) times in \(path), at line\(count == 1 ? "" : "s") \(lineNumbers.map(String.init).joined(separator: ", ")). Make old_text longer and more specific so it matches only the intended spot, or pass replace_all: true to replace every occurrence."
        case .emptyOldTextOnNonEmptyFile(let path):
            "old_text is empty, which only sets the content of an empty file. \(path) is not empty — read it first, or use write_file to replace it entirely."
        case .lineRangeOutOfBounds(let start, let end, let lineCount):
            "Requested line\(start == end ? "" : "s") \(start)\(start == end ? "" : "-\(end)") \(start == end ? "is" : "are") out of range. The file has \(lineCount) line\(lineCount == 1 ? "" : "s")."
        case .staleRead(let path):
            "\(path) changed on disk since you last read it. Call read_file again before editing, so you're editing against the current content."
        case .notReadThisTurn(let path):
            "\(path) hasn't been read in this chat turn yet. Call read_file first so the edit can be checked against the current content."
        }
    }
}

/// Applies `StringEdit`s to in-memory text, with helpful errors describing
/// exactly where a replacement failed to match the intended one spot.
enum TextEditApplier {
    /// Applies every edit to `source` as one all-or-nothing operation. The
    /// first edit that doesn't match throws, and nothing is written for any
    /// of the edits — callers only persist the result of a fully successful
    /// call.
    static func apply(_ edits: [StringEdit], to source: String, path: String) throws -> String {
        var working = source
        for edit in edits {
            working = try applyOne(edit, to: working, path: path)
        }
        return working
    }

    private static func applyOne(_ edit: StringEdit, to source: String, path: String) throws -> String {
        if edit.oldText.isEmpty {
            guard source.isEmpty else { throw FileEditingError.emptyOldTextOnNonEmptyFile(path: path) }
            return edit.newText
        }

        let count = source.components(separatedBy: edit.oldText).count - 1
        guard count > 0 else {
            throw FileEditingError.noMatch(path: path, context: nearbyContext(for: edit.oldText, in: source))
        }
        guard count == 1 || edit.replaceAll else {
            throw FileEditingError.multipleMatches(
                path: path,
                count: count,
                lineNumbers: matchLineNumbers(of: edit.oldText, in: source)
            )
        }
        if edit.replaceAll {
            return source.replacingOccurrences(of: edit.oldText, with: edit.newText)
        }
        guard let range = source.range(of: edit.oldText) else {
            throw FileEditingError.noMatch(path: path, context: nearbyContext(for: edit.oldText, in: source))
        }
        return source.replacingCharacters(in: range, with: edit.newText)
    }

    /// The 1-based line number of each occurrence of `needle` in `haystack`.
    static func matchLineNumbers(of needle: String, in haystack: String) -> [Int] {
        guard !needle.isEmpty else { return [] }
        var results: [Int] = []
        var searchStart = haystack.startIndex
        while let range = haystack.range(of: needle, range: searchStart..<haystack.endIndex) {
            let before = haystack[haystack.startIndex..<range.lowerBound]
            let lineNumber = before.reduce(0) { $1 == "\n" ? $0 + 1 : $0 } + 1
            results.append(lineNumber)
            searchStart = range.upperBound
        }
        return results
    }

    /// Finds the single longest non-empty line of `oldText` that appears
    /// verbatim (ignoring surrounding whitespace) in `source`, and shows a
    /// small window of context around it — giving the model something
    /// concrete to adjust `old_text` against instead of a bare "no match".
    /// Falls back to a word-overlap fuzzy match, and then to the start of
    /// the file, when nothing lines up exactly.
    static func nearbyContext(for oldText: String, in source: String) -> String {
        let sourceLines = source.components(separatedBy: "\n")
        let candidateLines = oldText.components(separatedBy: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
            .sorted { $0.count > $1.count }

        for candidate in candidateLines {
            guard let matchIndex = sourceLines.firstIndex(where: { $0.trimmingCharacters(in: .whitespaces) == candidate }) else { continue }
            return windowDescription(around: matchIndex, in: sourceLines, label: "Closest matching line is line \(matchIndex + 1)")
        }

        // Nothing lines up exactly. A model's old_text is often a paraphrase
        // reconstructed from memory rather than a literal copy of the file
        // (e.g. it forgot the exact wording of something it itself wrote a
        // moment earlier) — the source line sharing the most distinctive
        // words with old_text's longest line is far more useful to show
        // than an arbitrary "file starts with" dump when the real target is
        // somewhere in the middle of a long file.
        if let longestCandidate = candidateLines.first,
           let bestIndex = bestFuzzyMatchLine(for: longestCandidate, in: sourceLines) {
            return windowDescription(around: bestIndex, in: sourceLines, label: "No exact match. The most similar existing line is line \(bestIndex + 1)")
        }

        guard !sourceLines.isEmpty, !(sourceLines.count == 1 && sourceLines[0].isEmpty) else {
            return "The file is empty."
        }
        let headCount = min(5, sourceLines.count)
        let head = (0..<headCount).map { "\($0 + 1)| \(sourceLines[$0])" }.joined(separator: "\n")
        return "No similar line was found. The file starts with:\n\(head)"
    }

    private static func windowDescription(around index: Int, in lines: [String], label: String) -> String {
        let start = max(0, index - 2)
        let end = min(lines.count - 1, index + 2)
        let window = (start...end).map { "\($0 + 1)| \(lines[$0])" }.joined(separator: "\n")
        return "\(label):\n\(window)"
    }

    private static func bestFuzzyMatchLine(for candidate: String, in lines: [String]) -> Int? {
        let candidateWords = wordSet(candidate)
        guard !candidateWords.isEmpty else { return nil }
        var bestIndex: Int?
        var bestScore = 0
        for (index, line) in lines.enumerated() {
            let score = wordSet(line).intersection(candidateWords).count
            if score > bestScore {
                bestScore = score
                bestIndex = index
            }
        }
        // Require a few shared distinctive (3+ letter) words so an
        // unrelated line isn't offered as if it were meaningfully similar.
        guard bestScore >= 3 else { return nil }
        return bestIndex
    }

    private static func wordSet(_ text: String) -> Set<String> {
        Set(text.lowercased().split { !$0.isLetter && !$0.isNumber }.map(String.init).filter { $0.count >= 3 })
    }
}

/// Tracks, per workspace-relative path, the content hash this chat turn last
/// saw for that file — from a `read_file`, or from this turn's own
/// `write_file`/`edit_file`/`edit_lines`. A fresh instance is created per
/// chat turn (see `ToolExecutionContext`), so the guard only ever compares
/// against what *this* turn has actually looked at, not a stale value left
/// over from an earlier message.
actor FileReadTracker {
    enum Status: Equatable {
        case notRead
        case upToDate
        case stale
    }

    private var hashesByPath: [String: Int] = [:]

    func recordRead(path: String, content: String) {
        hashesByPath[path] = content.hashValue
    }

    func recordWrite(path: String, content: String) {
        hashesByPath[path] = content.hashValue
    }

    func verify(path: String, currentContent: String) -> Status {
        guard let hash = hashesByPath[path] else { return .notRead }
        return hash == currentContent.hashValue ? .upToDate : .stale
    }
}
