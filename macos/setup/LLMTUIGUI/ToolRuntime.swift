import Foundation
import Darwin
import Metal

enum ToolSafetyClass: String, Codable, Sendable {
    case interaction
    case readOnly
    case mutating
    case command
    case network
}

enum ToolExecutionStatus: String, Codable, Sendable {
    case requested
    case awaitingApproval
    case running
    case succeeded
    case failed
    case rejected
}

struct ToolResult: Sendable, Equatable {
    let toolCallID: String
    let toolName: String
    let content: String
    let isError: Bool
}

struct ToolDefinition: Encodable, Sendable, Equatable {
    struct Function: Codable, Sendable, Equatable {
        let name: String
        let description: String
        let parameters: JSONValue
    }

    let type = "function"
    let function: Function
    let safety: ToolSafetyClass

    enum CodingKeys: String, CodingKey {
        case type
        case function
    }
}

indirect enum JSONValue: Codable, Sendable, Equatable {
    case string(String)
    case number(Double)
    case boolean(Bool)
    case object([String: JSONValue])
    case array([JSONValue])
    case null

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if container.decodeNil() { self = .null }
        else if let value = try? container.decode(Bool.self) { self = .boolean(value) }
        else if let value = try? container.decode(Double.self) { self = .number(value) }
        else if let value = try? container.decode(String.self) { self = .string(value) }
        else if let value = try? container.decode([String: JSONValue].self) { self = .object(value) }
        else { self = .array(try container.decode([JSONValue].self)) }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .string(let value): try container.encode(value)
        case .number(let value): try container.encode(value)
        case .boolean(let value): try container.encode(value)
        case .object(let value): try container.encode(value)
        case .array(let value): try container.encode(value)
        case .null: try container.encodeNil()
        }
    }
}

struct ToolExecutionContext: Sendable {
    let workspaceURL: URL
    let timeout: Duration
    /// Scoped to one chat turn (see `OpenAICompatibleChatService.send`), so
    /// edit_lines' staleness check only ever compares against what this turn
    /// itself has read or written, never a value left over from an earlier
    /// message. Defaulted so existing call sites (tests, callers that only
    /// ever use read-only tools) don't need to construct one explicitly.
    let fileReadTracker: FileReadTracker
    /// Defaults to the one real `~/.local/share/llmtui/memory.yaml` store;
    /// tests inject one pointed at a throwaway file instead.
    let memoryStore: MemoryStore

    init(
        workspaceURL: URL,
        timeout: Duration,
        fileReadTracker: FileReadTracker = FileReadTracker(),
        memoryStore: MemoryStore = .shared
    ) {
        self.workspaceURL = workspaceURL
        self.timeout = timeout
        self.fileReadTracker = fileReadTracker
        self.memoryStore = memoryStore
    }
}

enum ToolRuntimeError: LocalizedError {
    case unknownTool(String)
    case invalidArguments(String)
    case outsideWorkspace
    case symlinkEscape
    case binaryFile
    case unsupportedURL
    case privateAddress
    case outputLimit
    case timedOut
    case commandFailed(Int32, String)

    var errorDescription: String? {
        switch self {
        case .unknownTool(let name): "Unknown tool: \(name)"
        case .invalidArguments(let message): "Invalid tool arguments: \(message)"
        case .outsideWorkspace: "The requested path is outside the configured workspace."
        case .symlinkEscape: "The requested path escapes the configured workspace through a symlink."
        case .binaryFile: "Binary files are not supported by this tool."
        case .unsupportedURL: "Only http and https URLs are supported."
        case .privateAddress: "Local, private, or link-local network addresses are not allowed."
        case .outputLimit: "Tool output exceeded the configured limit."
        case .timedOut: "The command timed out."
        case .commandFailed(let code, let output): "Command exited with \(code): \(output)"
        }
    }
}

struct ToolRegistry: Sendable {
    static let shared = ToolRegistry()

    let definitions: [ToolDefinition]
    let askUserDefinition: ToolDefinition

    init() {
        let askUserDefinition = Self.definition(
            "ask_user",
            "Ask the user a short clarifying question and wait for their answer before continuing. Use this when the request is ambiguous in a way that changes the answer (for example, which provider, which file or profile, or which of several options). Call it at most once per turn and do not call it together with other tools.",
            [
                "question": Self.stringParam("The single, concise question to ask the user."),
                "options": Self.stringArrayParam("Up to 4 short, mutually exclusive answer choices to present as buttons."),
                "allow_free_text": Self.booleanParam("Set to true to let the user type a custom answer instead of, or in addition to, the options. Defaults to true when no options are given.", default: true),
                "placeholder": Self.stringParam("Optional placeholder text shown in the free-text answer field.")
            ],
            required: ["question"],
            safety: .interaction
        )
        self.askUserDefinition = askUserDefinition
        definitions = [
            askUserDefinition,
            Self.definition(
                "list_dir",
                "List immediate entries in a workspace directory.",
                ["path": Self.stringParam("Workspace-relative directory path to list, for example '.' or 'src'.")],
                required: ["path"],
                safety: .readOnly
            ),
            Self.definition(
                "read_file",
                "Read a bounded range of lines from a text file. The result reports the file's total line count and shows each returned line prefixed 'N| ' for reference when editing.",
                [
                    "path": Self.stringParam("Workspace-relative path to the file to read."),
                    "offset": Self.numberParam("1-based line number to start reading from. Defaults to 1.", default: 1),
                    "limit": Self.numberParam("Maximum number of lines to return, up to 500. Defaults to 200.", default: 200)
                ],
                required: ["path"],
                safety: .readOnly
            ),
            Self.definition(
                "write_file",
                "Create a new UTF-8 text file, or explicitly replace an entire existing file only when the user asked for a full rewrite. For targeted changes to an existing file, use edit_file or edit_lines instead.",
                [
                    "path": Self.stringParam("Workspace-relative path of the file to create or fully replace."),
                    "content": Self.stringParam("Full UTF-8 text content to write.")
                ],
                required: ["path", "content"],
                safety: .mutating
            ),
            Self.definition(
                "edit_file",
                "Update an existing UTF-8 text file by replacing old_text with new_text. Provide either a single old_text/new_text pair, or a non-empty 'edits' array to apply several replacements to the same file as one atomic edit (if any one fails to match, none are applied). Each old_text must match exactly once in the file unless replace_all is true. Read the file first with read_file.",
                [
                    "path": Self.stringParam("Workspace-relative path to the existing file to edit."),
                    "old_text": Self.stringParam("The exact existing text to replace. Omit this and new_text when using 'edits' instead."),
                    "new_text": Self.stringParam("The replacement text for old_text."),
                    "replace_all": Self.booleanParam("Replace every occurrence of old_text instead of requiring exactly one match. Defaults to false.", default: false),
                    "edits": Self.objectArrayParam(
                        "A list of old_text/new_text replacements to apply to the same file as one atomic edit, instead of the single old_text/new_text pair above.",
                        itemProperties: [
                            "old_text": Self.stringParam("The exact existing text to replace."),
                            "new_text": Self.stringParam("The replacement text."),
                            "replace_all": Self.booleanParam("Replace every occurrence of this entry's old_text. Defaults to false.", default: false)
                        ],
                        itemRequired: ["old_text", "new_text"]
                    )
                ],
                required: ["path"],
                safety: .mutating
            ),
            Self.definition(
                "edit_lines",
                "Replace, insert after, or delete a specific 1-based line range in an existing UTF-8 text file. Call read_file on this exact file earlier in this chat turn first — the edit is refused if the file hasn't been read this turn, or changed on disk since.",
                [
                    "path": Self.stringParam("Workspace-relative path to the existing file to edit."),
                    "operation": Self.stringParam("One of 'replace', 'insert', or 'delete'."),
                    "start_line": Self.numberParamRequired("1-based line number. For 'insert', new_text is inserted after this line — use 0 to insert at the top of the file."),
                    "end_line": Self.numberParam("1-based inclusive end line for 'replace'/'delete'. Defaults to start_line, i.e. a single line. Ignored for 'insert'.", default: 0),
                    "new_text": Self.stringParam("Replacement or inserted text, as one or more lines joined by '\\n'. Omit or leave empty for 'delete'.")
                ],
                required: ["path", "operation", "start_line"],
                safety: .mutating
            ),
            Self.definition(
                "glob",
                "Find bounded workspace-relative paths matching a glob pattern.",
                [
                    "pattern": Self.stringParam("Glob pattern to match, for example '**/*.swift'."),
                    "path": Self.stringParam("Workspace-relative directory to search from. Defaults to '.'.")
                ],
                required: ["pattern"],
                safety: .readOnly
            ),
            Self.definition(
                "grep",
                "Search text files in the workspace.",
                [
                    "pattern": Self.stringParam("Regular expression to search for."),
                    "path": Self.stringParam("Workspace-relative directory to search from. Defaults to '.'."),
                    "case_sensitive": Self.booleanParam("Whether the search is case-sensitive. Defaults to true.", default: true),
                    "max_results": Self.numberParam("Maximum number of matching lines to return, up to 200. Defaults to 50.", default: 50)
                ],
                required: ["pattern"],
                safety: .readOnly
            ),
            Self.definition(
                "run_command",
                "Run one structured developer command in the workspace.",
                [
                    "command": Self.stringParam("Executable name such as 'git' or 'ls'. Must exist in a standard system bin directory."),
                    "arguments": Self.stringArrayParam("Command-line arguments to pass to the executable."),
                    "working_directory": Self.stringParam("Workspace-relative working directory. Defaults to '.'."),
                    "timeout_seconds": Self.numberParam("Maximum seconds to wait before the command is killed, up to 120. Defaults to 30.", default: 30)
                ],
                required: ["command"],
                safety: .command
            ),
            Self.definition(
                "web_search",
                "Search DuckDuckGo without an API key.",
                [
                    "query": Self.stringParam("The search query."),
                    "max_results": Self.numberParam("Maximum number of results to return, up to 10. Defaults to 5.", default: 5)
                ],
                required: ["query"],
                safety: .network
            ),
            Self.definition(
                "web_fetch",
                "Fetch the readable text content of an HTTP or HTTPS page. Use this after web_search to read the 1-3 most relevant results before answering; do not answer from search snippets alone.",
                [
                    "url": Self.stringParam("The full http or https URL to fetch."),
                    "max_chars": Self.numberParam("Maximum characters of readable text to return, up to 20000. Defaults to 8000.", default: 8000)
                ],
                required: ["url"],
                safety: .network
            ),
            Self.definition(
                "memory_search",
                "Search previously remembered user preferences and facts by keyword. Call this when the user refers to something they told you before, or before asking a question your earlier conversation may already answer.",
                [
                    "query": Self.stringParam("Words to match against remembered text."),
                    "limit": Self.numberParam("Maximum number of snippets to return, up to 20. Defaults to 5.", default: 5)
                ],
                required: ["query"],
                safety: .readOnly
            ),
            Self.definition(
                "memory_save",
                "Remember a short, durable user preference or fact for future conversations. Only call this when the user explicitly asks you to remember something, not speculatively. Never save secrets, API keys, or passwords.",
                [
                    "text": Self.stringParam("The preference or fact to remember, written as a short, self-contained statement."),
                    "tags": Self.stringArrayParam("Optional short tags to help find this later.")
                ],
                required: ["text"],
                safety: .mutating
            ),
            Self.definition(
                "memory_delete",
                "Forget a previously remembered snippet by its id (from memory_search results), or by an unambiguous 4+ character prefix of it.",
                ["id": Self.stringParam("The snippet id, or an unambiguous prefix of at least 4 characters.")],
                required: ["id"],
                safety: .mutating
            ),
            Self.definition(
                "local_context",
                "Return non-sensitive local context: date/time, OS, hardware (CPU, GPU, memory, disk), and the workspace folder. Use this before answering questions about the machine itself, or to decide whether a task is feasible given available memory/disk/CPU.",
                [:],
                required: [],
                safety: .readOnly
            )
        ]
    }

    private static func definition(_ name: String, _ description: String, _ properties: [String: JSONValue], required: [String], safety: ToolSafetyClass) -> ToolDefinition {
        return ToolDefinition(function: .init(name: name, description: description, parameters: .object([
            "type": .string("object"),
            "properties": .object(properties),
            "required": .array(required.map(JSONValue.string)),
            "additionalProperties": .boolean(false)
        ])), safety: safety)
    }

    private static func stringParam(_ description: String) -> JSONValue {
        .object(["type": .string("string"), "description": .string(description)])
    }

    private static func numberParam(_ description: String, default defaultValue: Double) -> JSONValue {
        .object(["type": .string("number"), "description": .string(description), "default": .number(defaultValue)])
    }

    private static func numberParamRequired(_ description: String) -> JSONValue {
        .object(["type": .string("number"), "description": .string(description)])
    }

    private static func objectArrayParam(_ description: String, itemProperties: [String: JSONValue], itemRequired: [String]) -> JSONValue {
        .object([
            "type": .string("array"),
            "description": .string(description),
            "items": .object([
                "type": .string("object"),
                "properties": .object(itemProperties),
                "required": .array(itemRequired.map(JSONValue.string)),
                "additionalProperties": .boolean(false)
            ])
        ])
    }

    private static func booleanParam(_ description: String, default defaultValue: Bool) -> JSONValue {
        .object(["type": .string("boolean"), "description": .string(description), "default": .boolean(defaultValue)])
    }

    private static func stringArrayParam(_ description: String) -> JSONValue {
        .object([
            "type": .string("array"),
            "description": .string(description),
            "items": .object(["type": .string("string")])
        ])
    }

    func definition(for name: String) -> ToolDefinition? {
        definitions.first { $0.function.name == name }
            ?? PersonalAppsToolDefinitions.all.first { $0.function.name == name }
    }

    func chatDefinitions(includeWorkspaceTools: Bool) -> [ToolDefinition] {
        includeWorkspaceTools ? definitions : [askUserDefinition]
    }

    func execute(name: String, arguments: Data, context: ToolExecutionContext, toolCallID: String) async -> ToolResult {
        do {
            guard let definition = definition(for: name) else { throw ToolRuntimeError.unknownTool(name) }
            let object = try decodeObject(arguments)
            let content: String
            switch name {
            case "list_dir": content = try FileToolRuntime.listDir(path: object.string("path"), context: context)
            case "read_file": content = try await FileToolRuntime.readFile(path: object.string("path"), offset: object.int("offset", default: 1), limit: min(object.int("limit", default: 200), 500), context: context)
            case "write_file": content = try await FileToolRuntime.writeFile(path: object.string("path"), content: object.string("content"), context: context)
            case "edit_file":
                let rawEdits = object.objectArray("edits")
                let parsedEdits: [StringEdit] = try rawEdits.map { raw in
                    guard let oldText = raw["old_text"] as? String, let newText = raw["new_text"] as? String else {
                        throw ToolRuntimeError.invalidArguments("each edits[] entry needs old_text and new_text strings")
                    }
                    let replaceAll = (raw["replace_all"] as? NSNumber)?.boolValue ?? false
                    return StringEdit(oldText: oldText, newText: newText, replaceAll: replaceAll)
                }
                content = try await FileToolRuntime.editFile(
                    path: object.string("path"),
                    oldText: try? object.string("old_text"),
                    newText: try? object.string("new_text"),
                    replaceAll: object.bool("replace_all", default: false),
                    edits: parsedEdits.isEmpty ? nil : parsedEdits,
                    context: context
                )
            case "edit_lines":
                guard let operation = LineEditOperation(rawValue: try object.string("operation")) else {
                    throw ToolRuntimeError.invalidArguments("operation must be one of 'replace', 'insert', or 'delete'")
                }
                let startLine = object.int("start_line", default: -1)
                guard startLine >= 0 else { throw ToolRuntimeError.invalidArguments("start_line is required") }
                let endLineRaw = object.int("end_line", default: 0)
                content = try await FileToolRuntime.editLines(
                    path: object.string("path"),
                    operation: operation,
                    startLine: startLine,
                    endLine: endLineRaw > 0 ? endLineRaw : nil,
                    newText: object.string("new_text", default: ""),
                    context: context
                )
            case "glob": content = try FileToolRuntime.glob(pattern: object.string("pattern"), path: object.string("path", default: "."), context: context)
            case "grep": content = try FileToolRuntime.grep(pattern: object.string("pattern"), path: object.string("path", default: "."), caseSensitive: object.bool("case_sensitive", default: true), maxResults: min(object.int("max_results", default: 50), 200), context: context)
            case "run_command": content = try await CommandToolRuntime.run(command: object.string("command"), arguments: object.stringArray("arguments"), directory: object.string("working_directory", default: "."), timeout: min(object.double("timeout_seconds", default: 30), 120), context: context)
            case "web_search": content = try await WebToolRuntime.search(query: object.string("query"), maxResults: min(object.int("max_results", default: 5), 10))
            case "web_fetch": content = try await WebToolRuntime.fetch(urlString: object.string("url"), maxChars: min(object.int("max_chars", default: 50000), 100000))
            case "memory_search": content = try await MemoryToolRuntime.search(query: object.string("query"), limit: min(object.int("limit", default: 5), 20), store: context.memoryStore)
            case "memory_save": content = try await MemoryToolRuntime.save(text: object.string("text"), tags: object.stringArray("tags"), store: context.memoryStore)
            case "memory_delete": content = try await MemoryToolRuntime.delete(id: object.string("id"), store: context.memoryStore)
            case "local_context": content = LocalContextToolRuntime.value(context: context)
            default: throw ToolRuntimeError.unknownTool(name)
            }
            _ = definition
            return ToolResult(toolCallID: toolCallID, toolName: name, content: content, isError: false)
        } catch {
            // Explicitly label this as an error in the content itself — it's
            // the only signal the model gets that the call failed, since the
            // OpenAI-compatible `tool` message role carries no error flag.
            await DiagnosticsLogger.shared.log(
                level: .warning,
                category: .tools,
                event: "tool_failed",
                requestID: nil,
                metadata: ["tool": .string(name)],
                error: SafeDiagnosticError.describe(error, operation: "tool execution")
            )
            return ToolResult(toolCallID: toolCallID, toolName: name, content: "Error: \(error.localizedDescription)", isError: true)
        }
    }

    private func decodeObject(_ data: Data) throws -> [String: AnyJSON] {
        // Some local servers send an empty-string (or whitespace-only)
        // arguments payload for a tool that takes no parameters, rather than
        // the literal "{}". Treat that the same as an empty object instead
        // of failing a tool like local_context that has nothing to decode.
        let text = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        if text.isEmpty { return [:] }
        guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw ToolRuntimeError.invalidArguments("arguments must be a JSON object")
        }
        return object.mapValues(AnyJSON.init)
    }
}

private struct AnyJSON: Sendable {
    let value: Any
}

private extension Dictionary where Key == String, Value == AnyJSON {
    func string(_ key: String, default fallback: String? = nil) throws -> String {
        if let value = self[key]?.value as? String { return value }
        if let fallback { return fallback }
        throw ToolRuntimeError.invalidArguments("missing string argument '\(key)'")
    }
    func int(_ key: String, default fallback: Int) -> Int { (self[key]?.value as? NSNumber)?.intValue ?? fallback }
    func double(_ key: String, default fallback: Double) -> Double { (self[key]?.value as? NSNumber)?.doubleValue ?? fallback }
    func bool(_ key: String, default fallback: Bool) -> Bool { (self[key]?.value as? NSNumber)?.boolValue ?? fallback }
    func stringArray(_ key: String) -> [String] { (self[key]?.value as? [Any])?.compactMap { $0 as? String } ?? [] }
    func objectArray(_ key: String) -> [[String: Any]] { (self[key]?.value as? [Any])?.compactMap { $0 as? [String: Any] } ?? [] }
}

private enum WorkspacePath {
    static func resolve(_ path: String, context: ToolExecutionContext, allowMissing: Bool = false) throws -> URL {
        let raw = path.isEmpty ? "." : path
        let candidate = raw.hasPrefix("/") ? URL(fileURLWithPath: raw) : context.workspaceURL.appendingPathComponent(raw)
        let workspace = context.workspaceURL.standardizedFileURL.resolvingSymlinksInPath()
        let standardized = candidate.standardizedFileURL
        guard standardized.path == workspace.path || standardized.path.hasPrefix(workspace.path + "/") else {
            throw ToolRuntimeError.outsideWorkspace
        }

        let resolved: URL
        if FileManager.default.fileExists(atPath: standardized.path) {
            resolved = standardized.resolvingSymlinksInPath()
        } else if allowMissing {
            let parent = standardized.deletingLastPathComponent().resolvingSymlinksInPath()
            resolved = parent.appendingPathComponent(standardized.lastPathComponent).standardizedFileURL
        } else {
            throw CocoaError(.fileNoSuchFile)
        }
        guard resolved.path == workspace.path || resolved.path.hasPrefix(workspace.path + "/") else {
            throw ToolRuntimeError.outsideWorkspace
        }
        return resolved
    }
}

private enum FileToolRuntime {
    static func listDir(path: String, context: ToolExecutionContext) throws -> String {
        let url = try WorkspacePath.resolve(path, context: context)
        var isDirectory: ObjCBool = false
        guard FileManager.default.fileExists(atPath: url.path, isDirectory: &isDirectory), isDirectory.boolValue else { throw CocoaError(.fileReadCorruptFile) }
        let values = try FileManager.default.contentsOfDirectory(at: url, includingPropertiesForKeys: [.isDirectoryKey, .fileSizeKey], options: [.skipsHiddenFiles])
        return values.sorted { $0.lastPathComponent.localizedStandardCompare($1.lastPathComponent) == .orderedAscending }.prefix(200).map { item in
            let type = (try? item.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true ? "directory" : "file"
            let size = (try? item.resourceValues(forKeys: [.fileSizeKey]).fileSize) ?? 0
            return "\(item.lastPathComponent)\t\(type)\t\(size) bytes"
        }.joined(separator: "\n")
    }

    static func readFile(path: String, offset: Int, limit: Int, context: ToolExecutionContext) async throws -> String {
        let url = try WorkspacePath.resolve(path, context: context)
        let data = try Data(contentsOf: url, options: [.mappedIfSafe])
        guard data.prefix(4096).allSatisfy({ $0 == 9 || $0 == 10 || $0 == 13 || $0 >= 32 }) else { throw ToolRuntimeError.binaryFile }
        let text = String(decoding: data, as: UTF8.self)
        // Record the *full* content read, regardless of the requested
        // offset/limit, so edit_lines' staleness check reflects what the
        // file actually looked like when this turn last looked at it.
        await context.fileReadTracker.recordRead(path: path, content: text)

        let document = TextDocument(text)
        let (start, selected) = document.slice(from: offset, limit: limit)
        let endLine = start + selected.count - 1
        let hasMore = endLine < document.lineCount

        var header = "path: \(path)\ntotal_lines: \(document.lineCount)\n"
        header += selected.isEmpty
            ? "lines: (none in range)\n"
            : "lines: \(start)-\(endLine) of \(document.lineCount)\n"
        if hasMore {
            header += "more lines follow — call read_file again with offset=\(endLine + 1) to continue.\n"
        }

        let body = selected.enumerated().map { index, line -> String in
            let capped = line.count > 2000 ? String(line.prefix(2000)) + "…[line truncated]" : line
            return "\(start + index)| \(capped)"
        }.joined(separator: "\n")

        return header
            + "(Each line is shown as 'N| text' for reference only — do not include the 'N| ' prefix in old_text.)\n"
            + body
    }

    static func writeFile(path: String, content: String, context: ToolExecutionContext) async throws -> String {
        let url = try WorkspacePath.resolve(path, context: context, allowMissing: true)
        let fileManager = FileManager.default
        let existed = fileManager.fileExists(atPath: url.path)

        if existed {
            var directory: ObjCBool = false
            fileManager.fileExists(atPath: url.path, isDirectory: &directory)
            if directory.boolValue { throw CocoaError(.fileWriteInvalidFileName) }
        }

        let parent = url.deletingLastPathComponent()
        try fileManager.createDirectory(at: parent, withIntermediateDirectories: true)
        let temporary = parent.appendingPathComponent(".\(url.lastPathComponent).\(UUID().uuidString).tmp")
        try content.write(to: temporary, atomically: true, encoding: .utf8)

        if existed {
            _ = try fileManager.replaceItemAt(url, withItemAt: temporary)
        } else {
            try fileManager.moveItem(at: temporary, to: url)
        }

        let verified = try String(contentsOf: url, encoding: .utf8)
        guard verified == content else {
            throw ToolRuntimeError.invalidArguments("The file write could not be verified after saving.")
        }
        await context.fileReadTracker.recordWrite(path: path, content: verified)

        let lineCount = TextDocument(verified).lineCount
        let lineWord = "\(lineCount) line\(lineCount == 1 ? "" : "s")"
        return existed ? "Overwrote \(path) (\(lineWord))." : "Created \(path) (\(lineWord))."
    }

    /// `oldText`/`newText` are the single-pair form; `edits` (when non-nil
    /// and non-empty) takes precedence and is applied atomically — see
    /// `TextEditApplier`.
    static func editFile(
        path: String,
        oldText: String?,
        newText: String?,
        replaceAll: Bool,
        edits: [StringEdit]?,
        context: ToolExecutionContext
    ) async throws -> String {
        let url = try WorkspacePath.resolve(path, context: context)
        let source = try String(contentsOf: url, encoding: .utf8)

        if await context.fileReadTracker.verify(path: path, currentContent: source) == .stale {
            throw FileEditingError.staleRead(path: path)
        }

        let resolvedEdits: [StringEdit]
        if let edits, !edits.isEmpty {
            resolvedEdits = edits
        } else if let oldText, let newText {
            resolvedEdits = [StringEdit(oldText: oldText, newText: newText, replaceAll: replaceAll)]
        } else {
            throw ToolRuntimeError.invalidArguments("edit_file requires either old_text and new_text, or a non-empty edits array")
        }

        let updated = try TextEditApplier.apply(resolvedEdits, to: source, path: path)
        let writeResult = try await writeFile(path: path, content: updated, context: context)
        let count = resolvedEdits.count
        return "\(writeResult)\n\(count) edit\(count == 1 ? "" : "s") applied to \(path).\n\(editedRegionPreview(oldSource: source, newSource: updated))"
    }

    static func editLines(
        path: String,
        operation: LineEditOperation,
        startLine: Int,
        endLine: Int?,
        newText: String,
        context: ToolExecutionContext
    ) async throws -> String {
        let url = try WorkspacePath.resolve(path, context: context)
        let source = try String(contentsOf: url, encoding: .utf8)

        switch await context.fileReadTracker.verify(path: path, currentContent: source) {
        case .notRead: throw FileEditingError.notReadThisTurn(path: path)
        case .stale: throw FileEditingError.staleRead(path: path)
        case .upToDate: break
        }

        var document = TextDocument(source)
        let newLines = newText.isEmpty ? [] : newText.components(separatedBy: "\n")

        let changedStart: Int
        switch operation {
        case .replace:
            let end = endLine ?? startLine
            try document.replaceLines(start: startLine, end: end, with: newLines)
            changedStart = startLine
        case .insert:
            try document.insertLines(after: startLine, newLines: newLines)
            changedStart = startLine + 1
        case .delete:
            let end = endLine ?? startLine
            try document.deleteLines(start: startLine, end: end)
            changedStart = startLine
        }

        let updated = document.text
        let writeResult = try await writeFile(path: path, content: updated, context: context)
        let changedCount = operation == .delete ? 0 : max(newLines.count, 1)
        let contextStart = max(1, changedStart - 3)
        let contextEnd = min(document.lineCount, changedStart + changedCount - 1 + 3)

        let actionDescription: String
        switch operation {
        case .replace:
            let end = endLine ?? startLine
            actionDescription = startLine == end ? "Replaced line \(startLine)" : "Replaced lines \(startLine)-\(end)"
        case .insert:
            actionDescription = startLine == 0 ? "Inserted at the top of the file" : "Inserted after line \(startLine)"
        case .delete:
            let end = endLine ?? startLine
            actionDescription = startLine == end ? "Deleted line \(startLine)" : "Deleted lines \(startLine)-\(end)"
        }
        var result = "\(writeResult)\n\(actionDescription) in \(path)."
        if document.lineCount > 0, contextStart <= contextEnd {
            let body = (contextStart...contextEnd).map { "\($0)| \(document.line($0) ?? "")" }.joined(separator: "\n")
            result += "\nUpdated region (lines \(contextStart)-\(contextEnd) of \(document.lineCount)):\n\(body)"
        }
        return result
    }

    /// Shows the changed region of a text-replacement edit with its *new*
    /// line numbers and a few lines of surrounding context, by diffing the
    /// common prefix/suffix of lines before and after the edit — enough for
    /// the model to make a follow-up edit without reading the file again.
    private static func editedRegionPreview(oldSource: String, newSource: String) -> String {
        let oldLines = TextDocument(oldSource).lines
        let newLines = TextDocument(newSource).lines

        var prefix = 0
        while prefix < oldLines.count, prefix < newLines.count, oldLines[prefix] == newLines[prefix] {
            prefix += 1
        }
        var suffix = 0
        while suffix < oldLines.count - prefix,
              suffix < newLines.count - prefix,
              oldLines[oldLines.count - 1 - suffix] == newLines[newLines.count - 1 - suffix] {
            suffix += 1
        }

        let changedStart = prefix
        let changedEnd = newLines.count - suffix
        let contextStart = max(0, changedStart - 3)
        let contextEnd = min(newLines.count, changedEnd + 3)
        guard contextStart < contextEnd else { return "" }

        let body = (contextStart..<contextEnd).map { "\($0 + 1)| \(newLines[$0])" }.joined(separator: "\n")
        return "Updated region (lines \(contextStart + 1)-\(contextEnd) of \(newLines.count)):\n\(body)"
    }

    static func glob(pattern: String, path: String, context: ToolExecutionContext) throws -> String {
        let root = try WorkspacePath.resolve(path, context: context)
        let enumerator = FileManager.default.enumerator(at: root, includingPropertiesForKeys: [.isDirectoryKey], options: [.skipsHiddenFiles])
        var results: [String] = []
        while let item = enumerator?.nextObject() as? URL, results.count < 200 {
            let relative = item.path.replacingOccurrences(of: context.workspaceURL.path + "/", with: "")
            if GlobMatcher.matches(pattern: pattern, value: relative) { results.append(relative) }
        }
        return results.joined(separator: "\n")
    }

    static func grep(pattern: String, path: String, caseSensitive: Bool, maxResults: Int, context: ToolExecutionContext) throws -> String {
        let root = try WorkspacePath.resolve(path, context: context)
        guard let expression = try? NSRegularExpression(pattern: pattern, options: caseSensitive ? [] : [.caseInsensitive]) else { throw ToolRuntimeError.invalidArguments("invalid regular expression") }
        let enumerator = FileManager.default.enumerator(at: root, includingPropertiesForKeys: [.isDirectoryKey], options: [.skipsHiddenFiles])
        var output: [String] = []
        while let item = enumerator?.nextObject() as? URL, output.count < maxResults {
            guard (try? item.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) != true else { continue }
            guard let data = try? Data(contentsOf: item, options: [.mappedIfSafe]), data.prefix(1024).allSatisfy({ $0 == 9 || $0 == 10 || $0 == 13 || $0 >= 32 }) else { continue }
            let text = String(decoding: data, as: UTF8.self)
            let relative = item.path.replacingOccurrences(of: context.workspaceURL.path + "/", with: "")
            for (index, line) in text.components(separatedBy: .newlines).enumerated() where expression.firstMatch(in: line, range: NSRange(line.startIndex..., in: line)) != nil {
                output.append("\(relative):\(index + 1): \(line)")
                if output.count == maxResults { break }
            }
        }
        return output.joined(separator: "\n")
    }
}

private enum GlobMatcher {
    /// Translates a shell-style glob into a regular expression. `**/` is
    /// handled as its own case — `(?:.*/)?`, i.e. zero or more path segments
    /// — rather than folding into the generic `*` → `.*` substitution the
    /// old implementation used; that version always left a literal `/`
    /// right after `.*` in the compiled pattern, so `**/*.swift` could never
    /// match a top-level file like `a.swift`, only something already inside
    /// a subdirectory.
    static func matches(pattern: String, value: String) -> Bool {
        var regex = ""
        let characters = Array(pattern)
        var index = 0
        while index < characters.count {
            if characters[index] == "*", index + 1 < characters.count, characters[index + 1] == "*" {
                if index + 2 < characters.count, characters[index + 2] == "/" {
                    regex += "(?:.*/)?"
                    index += 3
                } else {
                    regex += ".*"
                    index += 2
                }
            } else if characters[index] == "*" {
                regex += "[^/]*"
                index += 1
            } else if characters[index] == "?" {
                regex += "[^/]"
                index += 1
            } else {
                regex += NSRegularExpression.escapedPattern(for: String(characters[index]))
                index += 1
            }
        }
        return value.range(of: "^\(regex)$", options: .regularExpression) != nil
    }
}

/// Collects a running process's combined stdout/stderr as it arrives. Without
/// this, reading each pipe only after the process exits
/// (`readDataToEndOfFile()`) can deadlock the whole tool call: a pipe's
/// kernel buffer is bounded, so a command that prints more than that before
/// exiting blocks on write forever while nothing is draining the read end —
/// which then surfaces as a confusing timeout rather than the real cause.
private actor CommandOutputCollector {
    private(set) var data = Data()

    func append(_ chunk: Data) {
        guard data.count < 65536 else { return }
        data.append(chunk)
    }
}

private enum CommandToolRuntime {
    static func run(command: String, arguments: [String], directory: String, timeout: Double, context: ToolExecutionContext) async throws -> String {
        let executable = try locate(command)
        let working = try WorkspacePath.resolve(directory, context: context)
        let process = Process()
        let output = Pipe()
        let error = Pipe()
        process.executableURL = executable
        process.arguments = arguments
        process.currentDirectoryURL = working
        process.standardOutput = output
        process.standardError = error

        let collector = CommandOutputCollector()
        let drain: (FileHandle) -> Void = { handle in
            let chunk = handle.availableData
            guard !chunk.isEmpty else { return }
            Task { await collector.append(chunk) }
        }
        output.fileHandleForReading.readabilityHandler = drain
        error.fileHandleForReading.readabilityHandler = drain
        defer {
            output.fileHandleForReading.readabilityHandler = nil
            error.fileHandleForReading.readabilityHandler = nil
        }

        try process.run()

        do {
            try await withTaskCancellationHandler {
                let deadline = Date().addingTimeInterval(timeout)
                while process.isRunning {
                    try Task.checkCancellation()
                    if Date() >= deadline {
                        process.terminate()
                        throw ToolRuntimeError.timedOut
                    }
                    try await Task.sleep(for: .milliseconds(100))
                }
            } onCancel: {
                process.terminate()
            }
        } catch is CancellationError {
            process.terminate()
            throw CancellationError()
        }

        // Let the readability handlers drain whatever the process flushed on
        // exit before reading the final total.
        try? await Task.sleep(for: .milliseconds(20))
        let text = String(decoding: await collector.data, as: UTF8.self)
        let limited = String(text.prefix(65536)) + (text.count >= 65536 ? "\n[output truncated]" : "")
        guard process.terminationStatus == 0 else { throw ToolRuntimeError.commandFailed(process.terminationStatus, limited) }
        return "exit code: 0\n\(limited)"
    }

    private static func locate(_ command: String) throws -> URL {
        if command.hasPrefix("/") && FileManager.default.isExecutableFile(atPath: command) { return URL(fileURLWithPath: command) }
        for root in ["/usr/bin", "/bin", "/usr/local/bin", "/opt/homebrew/bin"] {
            let url = URL(fileURLWithPath: root).appendingPathComponent(command)
            if FileManager.default.isExecutableFile(atPath: url.path) { return url }
        }
        throw ToolRuntimeError.invalidArguments("command is not an allowed executable: \(command)")
    }
}

// Not file-private: readableText/isPrivate are exercised directly by tests.
enum WebToolRuntime {
    enum HTTPError: LocalizedError {
        case status(Int)

        var errorDescription: String? {
            switch self {
            case .status(let code): "The server returned HTTP status \(code)."
            }
        }
    }

    // DuckDuckGo's HTML endpoint (and many other sites) bot-detect on a
    // custom User-Agent and reject the request before it ever reaches
    // search/content logic. Presenting as a real desktop Safari browser,
    // with the Accept/Accept-Language headers Safari actually sends,
    // avoids that fast-fail path.
    //
    // The "Intel Mac OS X 10_15_7" token is intentional, not stale: Safari
    // freezes it for web-compat reasons and sends it verbatim even on
    // Apple Silicon (M1–M5) running the current OS. Only Version/Safari
    // track the real browser release, so that's what's bumped here.
    private static let browserUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/27.0 Safari/605.1.15"

    private static func applyBrowserHeaders(to request: inout URLRequest) {
        request.setValue(browserUserAgent, forHTTPHeaderField: "User-Agent")
        request.setValue("text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8", forHTTPHeaderField: "Accept")
        request.setValue("en-US,en;q=0.9", forHTTPHeaderField: "Accept-Language")
    }

    static func search(query: String, maxResults: Int) async throws -> String {
        try await withTransientRetry {
            try await searchOnce(query: query, maxResults: maxResults)
        }
    }

    private static func searchOnce(query: String, maxResults: Int) async throws -> String {
        var components = URLComponents(string: "https://html.duckduckgo.com/html/")!
        components.queryItems = [URLQueryItem(name: "q", value: query)]
        var request = URLRequest(url: components.url!)
        applyBrowserHeaders(to: &request)
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard http.statusCode == 200 else { throw HTTPError.status(http.statusCode) }
        let html = String(decoding: data.prefix(1_000_000), as: UTF8.self)
        let titlePattern = "<a[^>]*class=\"[^\"]*result__a[^\"]*\"[^>]*href=\"([^\"]+)\"[^>]*>(.*?)</a>"
        let titleRegex = try NSRegularExpression(pattern: titlePattern, options: [.caseInsensitive, .dotMatchesLineSeparators])
        let snippetPattern = "<a[^>]*class=\"[^\"]*result__snippet[^\"]*\"[^>]*>(.*?)</a>"
        let snippetRegex = try NSRegularExpression(pattern: snippetPattern, options: [.caseInsensitive, .dotMatchesLineSeparators])
        let titleMatches = titleRegex.matches(in: html, range: NSRange(html.startIndex..., in: html))
        let snippetMatches = snippetRegex.matches(in: html, range: NSRange(html.startIndex..., in: html))
        var results: [String] = []
        for (index, match) in titleMatches.prefix(maxResults).enumerated() {
            let title = html.substring(with: match.range(at: 2)).strippingHTML
            let rawURL = html.substring(with: match.range(at: 1)).decodingHTMLEntities
            let resultURL = Self.resolveSearchURL(rawURL)
            let snippet = index < snippetMatches.count
                ? html.substring(with: snippetMatches[index].range(at: 1)).strippingHTML
                : ""
            results.append("\(index + 1). \(title)\n   \(resultURL)\n   \(snippet)")
        }
        guard !results.isEmpty else { return "No results found." }
        return results.joined(separator: "\n\n")
            + "\n\nNext step: call web_fetch on the 1-3 most relevant URLs above before answering. Do not answer from snippets alone."
    }

    private static func resolveSearchURL(_ rawURL: String) -> String {
        guard let url = URL(string: rawURL),
              let components = URLComponents(url: url, resolvingAgainstBaseURL: true),
              let target = components.queryItems?.first(where: { $0.name == "uddg" })?.value,
              !target.isEmpty else {
            return rawURL.hasPrefix("//") ? "https:\(rawURL)" : rawURL
        }
        return target
    }

    static func fetch(urlString: String, maxChars: Int) async throws -> String {
        try await withTransientRetry {
            try await fetchOnce(urlString: urlString, maxChars: maxChars)
        }
    }

    private static func fetchOnce(urlString: String, maxChars: Int) async throws -> String {
        guard let url = URL(string: urlString), let scheme = url.scheme?.lowercased(), ["http", "https"].contains(scheme), let host = url.host, !isPrivate(host) else {
            throw ToolRuntimeError.unsupportedURL
        }
        var request = URLRequest(url: url)
        applyBrowserHeaders(to: &request)
        request.timeoutInterval = 20
        let (data, response) = try await URLSession.shared.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else { throw HTTPError.status(http.statusCode) }
        let mime = http.value(forHTTPHeaderField: "Content-Type")?.lowercased() ?? "text/plain"
        guard mime.contains("text/") || mime.contains("json") || mime.contains("xml") else { throw ToolRuntimeError.invalidArguments("unsupported content type") }

        let boundedChars = min(max(maxChars, 1), 20_000)
        // Cap the raw download before any processing; pages can be huge and we
        // only ever keep a small bounded excerpt of readable text from them.
        let rawHTML = String(decoding: data.prefix(2_000_000), as: UTF8.self)
        let isHTML = mime.contains("html") || rawHTML.localizedCaseInsensitiveContains("<html")
        let body = isHTML ? readableText(fromHTML: rawHTML) : rawHTML.strippingHTML
        let title = isHTML ? extractTitle(from: rawHTML) : nil

        var header = "url: \(urlString)"
        if let title, !title.isEmpty { header += "\ntitle: \(title)" }

        let truncated = body.count > boundedChars
        let boundedBody = String(body.prefix(boundedChars))
        let footer = truncated ? "\n\n[truncated — call web_fetch again with a larger max_chars if more is needed]" : ""
        return "\(header)\n\n\(boundedBody)\(footer)"
    }

    static func isTransient(_ error: Error) -> Bool {
        if case HTTPError.status(let code) = error {
            return code == 408 || code == 429 || (500...599).contains(code)
        }
        guard let urlError = error as? URLError else { return false }
        return [
            .timedOut,
            .cannotFindHost,
            .cannotConnectToHost,
            .networkConnectionLost,
            .dnsLookupFailed,
            .notConnectedToInternet,
            .secureConnectionFailed,
            .serverCertificateHasBadDate,
            .serverCertificateNotYetValid
        ].contains(urlError.code)
    }

    private static func withTransientRetry<T>(
        maximumAttempts: Int = 3,
        operation: () async throws -> T
    ) async throws -> T {
        var attempt = 1
        while true {
            do {
                return try await operation()
            } catch {
                guard attempt < maximumAttempts, isTransient(error) else { throw error }
                await DiagnosticsLogger.shared.log(
                    level: .warning,
                    category: .tools,
                    event: "web_retry_scheduled",
                    requestID: nil,
                    metadata: [
                        "attempt": .integer(attempt),
                        "maximum_attempts": .integer(maximumAttempts)
                    ],
                    error: SafeDiagnosticError.describe(error, operation: "web request")
                )
                let backoffMilliseconds = 750 * (1 << (attempt - 1))
                try await Task.sleep(for: .milliseconds(backoffMilliseconds))
                attempt += 1
            }
        }
    }

    private static func extractTitle(from html: String) -> String? {
        guard let regex = try? NSRegularExpression(pattern: "<title[^>]*>(.*?)</title>", options: [.caseInsensitive, .dotMatchesLineSeparators]),
              let match = regex.firstMatch(in: html, range: NSRange(html.startIndex..., in: html)) else { return nil }
        return html.substring(with: match.range(at: 1)).strippingHTML
    }

    /// Extracts readable body text from a raw HTML document: drops
    /// non-content elements entirely (scripts, styles, nav, forms, …),
    /// prefers a `<main>`/`<article>` region when present, and turns
    /// block-level boundaries into newlines before stripping remaining tags
    /// so paragraphs and list items don't run together into one line.
    static func readableText(fromHTML html: String) -> String {
        // String's own replacingOccurrences(options: .regularExpression) only
        // forwards .caseInsensitive, not dot-matches-line-separators, so tags
        // whose content spans multiple lines need NSRegularExpression directly.
        var work = removingMatches(of: "<!--.*?-->", in: html, options: [.dotMatchesLineSeparators])
        for tag in ["script", "style", "noscript", "svg", "nav", "header", "footer", "form", "iframe"] {
            work = removingMatches(of: "<\(tag)\\b[^>]*>.*?</\(tag)>", in: work, options: [.caseInsensitive, .dotMatchesLineSeparators])
        }

        for tag in ["main", "article"] {
            if let regex = try? NSRegularExpression(pattern: "<\(tag)\\b[^>]*>(.*?)</\(tag)>", options: [.caseInsensitive, .dotMatchesLineSeparators]),
               let match = regex.firstMatch(in: work, range: NSRange(work.startIndex..., in: work)) {
                work = work.substring(with: match.range(at: 1))
                break
            }
        }

        for tag in ["p", "li", "h1", "h2", "h3", "h4", "h5", "h6", "tr", "div", "section"] {
            work = replacingMatches(of: "</\(tag)\\s*>", with: "\n", in: work, options: [.caseInsensitive])
        }
        work = replacingMatches(of: "<br\\s*/?>", with: "\n", in: work, options: [.caseInsensitive])

        let lines = work.strippingHTMLKeepingLines
            .components(separatedBy: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        return lines.joined(separator: "\n")
    }

    private static func removingMatches(of pattern: String, in text: String, options: NSRegularExpression.Options = []) -> String {
        replacingMatches(of: pattern, with: " ", in: text, options: options)
    }

    private static func replacingMatches(of pattern: String, with template: String, in text: String, options: NSRegularExpression.Options = []) -> String {
        guard let regex = try? NSRegularExpression(pattern: pattern, options: options) else { return text }
        return regex.stringByReplacingMatches(in: text, range: NSRange(text.startIndex..., in: text), withTemplate: template)
    }

    static func isPrivate(_ host: String) -> Bool {
        let lower = host.lowercased()
        if lower == "localhost" || lower == "0.0.0.0" || lower == "::1" || lower.hasSuffix(".local") {
            return true
        }
        if lower.hasPrefix("127.") || lower.hasPrefix("10.") || lower.hasPrefix("192.168.") || lower.hasPrefix("169.254.") {
            return true
        }
        if lower.hasPrefix("172.") {
            let segments = lower.split(separator: ".")
            if segments.count > 1, let second = Int(segments[1]), (16...31).contains(second) {
                return true
            }
        }
        // IPv6 unique-local (fc00::/7) and link-local (fe80::/10) ranges.
        if lower.hasPrefix("fc") || lower.hasPrefix("fd") {
            return true
        }
        if ["fe8", "fe9", "fea", "feb"].contains(where: lower.hasPrefix) {
            return true
        }
        return false
    }
}

/// Formats `MemoryStore` results as tool output text. Separate from
/// `MemoryStore` itself so the store stays a plain data layer shared with
/// whatever else might want it, while this owns only presentation.
private enum MemoryToolRuntime {
    static func search(query: String, limit: Int, store: MemoryStore) async throws -> String {
        let matches = try await store.relevant(to: query, limit: limit)
        guard !matches.isEmpty else { return "No remembered snippets matched \"\(query)\"." }
        return matches.map(describe).joined(separator: "\n")
    }

    static func save(text: String, tags: [String], store: MemoryStore) async throws -> String {
        let snippet = try await store.add(text: text, tags: tags)
        return "Remembered (id \(snippet.id)): \(snippet.text)"
    }

    static func delete(id: String, store: MemoryStore) async throws -> String {
        try await store.remove(id: id)
        return "Forgot snippet \(id)."
    }

    private static func describe(_ snippet: MemorySnippet) -> String {
        let tagSuffix = snippet.tags.isEmpty ? "" : " [\(snippet.tags.joined(separator: ", "))]"
        return "\(snippet.id): \(snippet.text)\(tagSuffix)"
    }
}

/// Hardware/OS facts a local model can use to judge what's actually
/// feasible on this machine (e.g. "is there enough free RAM/disk for that"),
/// not just the date/OS/workspace the tool originally reported.
private enum LocalContextToolRuntime {
    static func value(context: ToolExecutionContext) -> String {
        let now = Date()
        let localFormatter = DateFormatter()
        localFormatter.locale = Locale(identifier: "en_US_POSIX")
        localFormatter.timeZone = .current
        localFormatter.dateFormat = "yyyy-MM-dd HH:mm:ss zzz"

        let utcFormatter = ISO8601DateFormatter()
        utcFormatter.timeZone = TimeZone(secondsFromGMT: 0)

        let timeZone = TimeZone.current
        let offset = timeZone.secondsFromGMT(for: now)
        let sign = offset >= 0 ? "+" : "-"
        let absoluteOffset = abs(offset)
        let offsetText = String(
            format: "%@%02d:%02d",
            sign,
            absoluteOffset / 3600,
            (absoluteOffset % 3600) / 60
        )

        let workspaceItemCount = try? FileManager.default.contentsOfDirectory(atPath: context.workspaceURL.path).count
        let workspaceItemsText = workspaceItemCount.map { "\($0) top-level item\($0 == 1 ? "" : "s")" } ?? "unreadable"
        let disk = diskDescription(for: context.workspaceURL)

        return """
        local_datetime: \(localFormatter.string(from: now))
        utc_datetime: \(utcFormatter.string(from: now))
        timezone: \(timeZone.identifier)
        utc_offset: \(offsetText)
        os: \(ProcessInfo.processInfo.operatingSystemVersionString)
        architecture: \(ProcessInfo.processInfo.machineArchitecture)
        hostname: \(ProcessInfo.processInfo.hostName)
        username: \(NSUserName())
        uptime: \(formattedUptime(ProcessInfo.processInfo.systemUptime))
        locale: \(Locale.current.identifier)
        cpu: \(cpuBrand) — \(ProcessInfo.processInfo.processorCount) logical cores (\(ProcessInfo.processInfo.activeProcessorCount) active)
        gpu: \(gpuDescription)
        memory_total: \(formattedBytes(Int64(ProcessInfo.processInfo.physicalMemory)))
        memory_usage: \(memoryUsageDescription)
        disk_total: \(disk.total)
        disk_free: \(disk.free)
        workspace: \(context.workspaceURL.path) (\(workspaceItemsText))
        """
    }

    private static func formattedUptime(_ seconds: TimeInterval) -> String {
        let totalMinutes = Int(seconds) / 60
        let days = totalMinutes / (60 * 24)
        let hours = (totalMinutes / 60) % 24
        let minutes = totalMinutes % 60
        var parts: [String] = []
        if days > 0 { parts.append("\(days)d") }
        if days > 0 || hours > 0 { parts.append("\(hours)h") }
        parts.append("\(minutes)m")
        return parts.joined(separator: " ")
    }

    private static func formattedBytes(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .memory)
    }

    private static var cpuBrand: String {
        // Apple Silicon doesn't expose a brand string via sysctl; fall back
        // to the hardware model identifier (e.g. "Mac15,3") in that case.
        sysctlString("machdep.cpu.brand_string") ?? sysctlString("hw.model") ?? "unknown"
    }

    private static func sysctlString(_ name: String) -> String? {
        var size = 0
        guard sysctlbyname(name, nil, &size, nil, 0) == 0, size > 0 else { return nil }
        var buffer = [CChar](repeating: 0, count: size)
        guard sysctlbyname(name, &buffer, &size, nil, 0) == 0 else { return nil }
        return String(cString: buffer)
    }

    private static var gpuDescription: String {
        let devices = MTLCopyAllDevices()
        guard !devices.isEmpty else { return "unavailable" }
        let names = devices.map(\.name).joined(separator: ", ")
        return "\(names) (\(devices.count) device\(devices.count == 1 ? "" : "s"))"
    }

    private static var memoryUsageDescription: String {
        guard let stats = hostVMStatistics() else { return "unavailable" }
        let pageSize = UInt64(vm_page_size)
        let used = (UInt64(stats.active_count) + UInt64(stats.inactive_count) + UInt64(stats.wire_count)) * pageSize
        let free = UInt64(stats.free_count) * pageSize
        return "\(formattedBytes(Int64(used))) used, \(formattedBytes(Int64(free))) free"
    }

    private static func hostVMStatistics() -> vm_statistics64? {
        var size = mach_msg_type_number_t(MemoryLayout<vm_statistics64>.size / MemoryLayout<integer_t>.size)
        var stats = vm_statistics64()
        let result = withUnsafeMutablePointer(to: &stats) { pointer -> kern_return_t in
            pointer.withMemoryRebound(to: integer_t.self, capacity: Int(size)) { reboundPointer in
                host_statistics64(mach_host_self(), HOST_VM_INFO64, reboundPointer, &size)
            }
        }
        return result == KERN_SUCCESS ? stats : nil
    }

    private static func diskDescription(for url: URL) -> (total: String, free: String) {
        guard let values = try? url.resourceValues(forKeys: [.volumeTotalCapacityKey, .volumeAvailableCapacityForImportantUsageKey]) else {
            return (total: "unavailable", free: "unavailable")
        }
        let total = values.volumeTotalCapacity.map { formattedBytes(Int64($0)) } ?? "unavailable"
        let free = values.volumeAvailableCapacityForImportantUsage.map { formattedBytes($0) } ?? "unavailable"
        return (total: total, free: free)
    }
}

private extension String {
    var strippingHTML: String {
        replacingOccurrences(of: "<[^>]+>", with: " ", options: .regularExpression)
            .replacingOccurrences(of: "&nbsp;", with: " ")
            .replacingOccurrences(of: "&amp;", with: "&")
            .replacingOccurrences(of: "&quot;", with: "\"")
            .replacingOccurrences(of: "&lt;", with: "<")
            .replacingOccurrences(of: "&gt;", with: ">")
            .replacingOccurrences(of: "\\s+", with: " ", options: .regularExpression)
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    /// Like `strippingHTML`, but collapses only horizontal whitespace so the
    /// newlines inserted at block-tag boundaries survive for readability.
    var strippingHTMLKeepingLines: String {
        replacingOccurrences(of: "<[^>]+>", with: "", options: .regularExpression)
            .replacingOccurrences(of: "&nbsp;", with: " ")
            .replacingOccurrences(of: "&amp;", with: "&")
            .replacingOccurrences(of: "&quot;", with: "\"")
            .replacingOccurrences(of: "&lt;", with: "<")
            .replacingOccurrences(of: "&gt;", with: ">")
            .replacingOccurrences(of: "[ \t]+", with: " ", options: .regularExpression)
    }

    var decodingHTMLEntities: String {
        strippingHTML
            .replacingOccurrences(of: "&#x2F;", with: "/", options: .caseInsensitive)
            .replacingOccurrences(of: "&#x3A;", with: ":", options: .caseInsensitive)
            .replacingOccurrences(of: "&#x3F;", with: "?", options: .caseInsensitive)
            .replacingOccurrences(of: "&#x3D;", with: "=", options: .caseInsensitive)
            .replacingOccurrences(of: "&#x26;", with: "&", options: .caseInsensitive)
    }

    func substring(with range: NSRange) -> String {
        guard let swiftRange = Range(range, in: self) else { return "" }
        return String(self[swiftRange])
    }
}

private extension ProcessInfo {
    var machineArchitecture: String {
#if arch(arm64)
        "arm64"
#else
        "x86_64"
#endif
    }
}

struct OpenAIToolCall: Codable, Sendable, Equatable {
    struct Function: Codable, Sendable, Equatable {
        let name: String
        let arguments: String
    }

    let id: String
    let type: String
    let function: Function
}

/// A request message's `content` is either a plain string, or — for a user
/// turn with image attachments — an array of text/image parts per the
/// OpenAI-compatible vision convention. Kept distinct from the streamed
/// response text (`OpenAIChatResponse.content`), which is always plain text.
enum OpenAIMessageContent: Encodable, Sendable, Equatable {
    case text(String)
    case parts([ContentPart])

    struct ContentPart: Encodable, Sendable, Equatable {
        struct ImageURL: Encodable, Sendable, Equatable { let url: String }
        let type: String
        let text: String?
        let imageURL: ImageURL?

        enum CodingKeys: String, CodingKey {
            case type, text
            case imageURL = "image_url"
        }

        static func text(_ value: String) -> ContentPart {
            .init(type: "text", text: value, imageURL: nil)
        }

        static func imageURL(_ dataURLString: String) -> ContentPart {
            .init(type: "image_url", text: nil, imageURL: .init(url: dataURLString))
        }
    }

    /// The plain-text portion, used anywhere the request-side distinction
    /// between a string and a parts array doesn't matter — counting
    /// characters for the context-budget estimate, or truncating an old
    /// tool result.
    var plainText: String? {
        switch self {
        case .text(let value): value
        case .parts(let values): values.first(where: { $0.type == "text" })?.text
        }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .text(let value): try container.encode(value)
        case .parts(let values): try container.encode(values)
        }
    }
}

struct OpenAIMessage: Encodable, Sendable, Equatable {
    let role: String
    let content: OpenAIMessageContent?
    let toolCalls: [OpenAIToolCall]?
    let toolCallID: String?

    enum CodingKeys: String, CodingKey {
        case role
        case content
        case toolCalls = "tool_calls"
        case toolCallID = "tool_call_id"
    }

    init(role: String, content: OpenAIMessageContent?, toolCalls: [OpenAIToolCall]? = nil, toolCallID: String? = nil) {
        self.role = role
        self.content = content
        self.toolCalls = toolCalls
        self.toolCallID = toolCallID
    }

    init(role: String, content: String?, toolCalls: [OpenAIToolCall]? = nil, toolCallID: String? = nil) {
        self.init(role: role, content: content.map(OpenAIMessageContent.text), toolCalls: toolCalls, toolCallID: toolCallID)
    }

    static func assistant(content: String?, toolCalls: [OpenAIToolCall]) -> OpenAIMessage {
        .init(role: "assistant", content: content, toolCalls: toolCalls)
    }

    static func tool(content: String, toolCallID: String) -> OpenAIMessage {
        .init(role: "tool", content: content, toolCallID: toolCallID)
    }

    /// Builds the current turn's user message, encoding it as a multi-part
    /// vision payload when attachments are present and as a plain string
    /// otherwise. Historical turns never carry attachments (see `history`),
    /// so only the message for the turn being sent right now goes through
    /// this factory.
    static func user(text: String, attachments: [ChatAttachment]) -> OpenAIMessage {
        guard !attachments.isEmpty else {
            return .init(role: "user", content: text)
        }
        var parts: [OpenAIMessageContent.ContentPart] = []
        if !text.isEmpty { parts.append(.text(text)) }
        parts.append(contentsOf: attachments.map { .imageURL($0.dataURLString) })
        return .init(role: "user", content: .parts(parts))
    }

    static func history(_ history: [ChatMessage]) -> [OpenAIMessage] {
        history.flatMap { message -> [OpenAIMessage] in
            switch message.role {
            case .user:
                // Attachments are only sent on the turn they were added to;
                // replaying images on every later turn would balloon the
                // request payload and the context budget for little benefit.
                return [.init(role: "user", content: message.text)]
            case .assistant:
                // Replay this turn's tool calls/results ahead of its final answer
                // so a follow-up question ("open the second link") has something
                // to work from. These are from completed turns, so their tool
                // results are truncated to keep old turns from crowding out the
                // current one.
                var expanded = message.toolTranscript.map { entry -> OpenAIMessage in
                    guard entry.role == "tool", let content = entry.content?.plainText, content.count > 1500 else { return entry }
                    return .init(
                        role: entry.role,
                        content: String(content.prefix(1500)) + "\n[older tool result truncated]",
                        toolCalls: entry.toolCalls,
                        toolCallID: entry.toolCallID
                    )
                }
                expanded.append(.init(role: "assistant", content: message.text))
                return expanded
            case .tool:
                return []
            }
        }
    }
}

struct OpenAIResponseAccumulator: Sendable {
    var content = ""
    /// Reasoning/"thinking" text some providers (Ollama, LM Studio) stream in
    /// a separate field alongside — or instead of — real content. Kept apart
    /// from `content` so it's never mistaken for the final answer.
    var reasoning = ""
    var toolCalls: [Int: OpenAIToolCall] = [:]

    mutating func append(_ event: OpenAIStreamEvent) -> (content: String?, reasoning: String?) {
        guard let choice = event.choices.first else { return (nil, nil) }
        // Some providers (notably Ollama) send an empty content string in the
        // same chunk as tool_calls. Process both instead of returning early,
        // or the tool call is silently dropped.
        var emittedText: String?
        if let content = choice.delta.content, !content.isEmpty {
            self.content += content
            emittedText = content
        }
        var emittedReasoning: String?
        if let fragment = choice.delta.reasoning ?? choice.delta.reasoningContent, !fragment.isEmpty {
            self.reasoning += fragment
            emittedReasoning = fragment
        }
        for delta in choice.delta.toolCalls ?? [] {
            ingest(delta)
        }
        return (emittedText, emittedReasoning)
    }

    private mutating func ingest(_ delta: OpenAIStreamEvent.ToolCallDelta) {
        let previous = toolCalls[delta.index]
        // Some local servers restart or reuse tool-call indices when a new call
        // begins. If this delta carries a fresh id that doesn't match what is
        // already stored at this index, treat it as a separate call instead of
        // concatenating two different tool calls' names/arguments together.
        if let newID = delta.id, let previous, previous.id != newID, !previous.function.name.isEmpty {
            let newIndex = (toolCalls.keys.max() ?? delta.index) + 1
            toolCalls[newIndex] = OpenAIToolCall(
                id: newID,
                type: delta.type ?? "function",
                function: .init(name: delta.function?.name ?? "", arguments: delta.function?.arguments ?? "")
            )
            return
        }
        let name = (previous?.function.name ?? "") + (delta.function?.name ?? "")
        let arguments = (previous?.function.arguments ?? "") + (delta.function?.arguments ?? "")
        toolCalls[delta.index] = OpenAIToolCall(
            id: previous?.id ?? delta.id ?? "tool-\(delta.index)",
            type: previous?.type ?? delta.type ?? "function",
            function: .init(name: name, arguments: arguments)
        )
    }
}

struct OpenAIChatResponse: Sendable {
    let content: String?
    let toolCalls: [OpenAIToolCall]
}

enum OpenAIRequestError: LocalizedError {
    case http(Int, String)

    var errorDescription: String? {
        switch self {
        case .http(let status, let message):
            let trimmed = message.trimmingCharacters(in: .whitespacesAndNewlines)
            return "The provider returned HTTP \(status)\(trimmed.isEmpty ? "." : ": \(trimmed)")"
        }
    }
}

/// Remembers, per provider+model, that the server or model rejected a request
/// that included tool definitions (HTTP 400), so later turns in this session
/// skip sending tools instead of failing every message.
actor ToolSupportTracker {
    static let shared = ToolSupportTracker()
    private var unsupported: Set<String> = []

    func isUnsupported(_ key: String) -> Bool { unsupported.contains(key) }
    func markUnsupported(_ key: String) { unsupported.insert(key) }
}

enum OpenAIRequest {
    static func send(
        configuration: LLMTUIConfiguration,
        messages: [OpenAIMessage],
        tools: [ToolDefinition],
        onText: @escaping (String) -> Void,
        onReasoning: ((String) -> Void)? = nil
    ) async throws -> OpenAIChatResponse {
        let endpoint = configuration.provider.baseURL.trimmingCharacters(in: CharacterSet(charactersIn: "/")) + "/chat/completions"
        guard let url = URL(string: endpoint) else { throw URLError(.badURL) }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        var body: [String: Any] = [
            "model": configuration.provider.model,
            "messages": try JSONSerialization.jsonObject(with: JSONEncoder().encode(messages)),
            "temperature": configuration.temperature,
            "top_p": configuration.topP,
            "max_tokens": configuration.maxTokens,
            "stream": true
        ]
        if !tools.isEmpty {
            body["tools"] = try JSONSerialization.jsonObject(with: JSONEncoder().encode(tools))
        }
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let (bytes, response) = try await URLSession.shared.bytes(for: request)
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else {
            var collected = ""
            for try await line in bytes.lines {
                collected += line
                if collected.utf8.count > 4096 { break }
            }
            throw OpenAIRequestError.http(http.statusCode, String(collected.prefix(2000)))
        }
        var accumulator = OpenAIResponseAccumulator()
        for try await line in bytes.lines {
            try Task.checkCancellation()
            guard line.hasPrefix("data: ") else { continue }
            let payload = String(line.dropFirst(6))
            if payload == "[DONE]" { break }
            guard let data = payload.data(using: .utf8), let event = try? JSONDecoder().decode(OpenAIStreamEvent.self, from: data) else { continue }
            let delta = accumulator.append(event)
            if let chunk = delta.content { onText(chunk) }
            if let reasoning = delta.reasoning { onReasoning?(reasoning) }
        }
        return OpenAIChatResponse(content: accumulator.content.isEmpty ? nil : accumulator.content, toolCalls: accumulator.toolCalls.keys.sorted().compactMap { accumulator.toolCalls[$0] })
    }
}

struct ProviderModelMetadata: Equatable, Sendable {
    let model: String
    let loadedInstance: String?
    let configuredContextWindow: Int?
    let maximumContextWindow: Int?
    let architecture: String?
    let quantization: String?
    let parameterCount: String?
    let supportsVision: Bool?
    let trainedForToolUse: Bool?
    let supportsReasoning: Bool?
    let defaultReasoningEnabled: Bool?

    var profileValues: [String: String] {
        var values: [String: String] = [:]
        if let configuredContextWindow {
            values["context_window"] = String(configuredContextWindow)
        } else if let maximumContextWindow {
            values["context_window"] = String(maximumContextWindow)
        }
        if let supportsReasoning {
            values["reasoning_hint"] = String(supportsReasoning)
        }
        return values
    }
}

enum ProviderDiscoveryError: LocalizedError {
    case unsupportedProviderType
    case profileSettingsUnavailable

    var errorDescription: String? {
        switch self {
        case .unsupportedProviderType:
            "Model discovery isn't available for this provider type."
        case .profileSettingsUnavailable:
            "This server doesn't expose model-profile information for this model."
        }
    }
}

/// Asks a provider directly for what it actually has, instead of the user
/// typing a model name/context-window value by hand. Deliberately separate
/// from `WebToolRuntime` — that type's `isPrivate` check rejects localhost
/// and LAN hosts on purpose, which is exactly where Ollama/LM Studio live.
enum ProviderModelDiscovery {
    static func fetchModels(configuration: LLMTUIConfiguration) async throws -> [String] {
        switch configuration.provider.type {
        case .ollama:
            return try await fetchOllamaModels(baseURL: configuration.provider.baseURL)
        case .openAICompatible:
            return try await fetchOpenAICompatibleModels(baseURL: configuration.provider.baseURL)
        case .embedded, .mock:
            throw ProviderDiscoveryError.unsupportedProviderType
        }
    }

    /// Reads the provider metadata that maps cleanly to an llmtui model profile.
    /// LM Studio exposes richer metadata through `/api/v1/models`; Ollama's
    /// `/api/show` currently contributes the model's maximum context length.
    static func fetchProfileSettings(
        configuration: LLMTUIConfiguration,
        model: String
    ) async throws -> ProviderModelMetadata {
        switch configuration.provider.type {
        case .ollama:
            let contextWindow = try await fetchOllamaContextLength(
                baseURL: configuration.provider.baseURL,
                model: model
            )
            return ProviderModelMetadata(
                model: model,
                loadedInstance: nil,
                configuredContextWindow: nil,
                maximumContextWindow: contextWindow,
                architecture: nil,
                quantization: nil,
                parameterCount: nil,
                supportsVision: nil,
                trainedForToolUse: nil,
                supportsReasoning: nil,
                defaultReasoningEnabled: nil
            )
        case .openAICompatible:
            return try await fetchLMStudioMetadata(
                baseURL: configuration.provider.baseURL,
                model: model
            )
        case .embedded, .mock:
            throw ProviderDiscoveryError.unsupportedProviderType
        }
    }

    static func fetchContextWindow(configuration: LLMTUIConfiguration, model: String) async throws -> Int {
        let metadata = try await fetchProfileSettings(configuration: configuration, model: model)
        guard let contextWindow = metadata.configuredContextWindow ?? metadata.maximumContextWindow else {
            throw ProviderDiscoveryError.profileSettingsUnavailable
        }
        return contextWindow
    }

    private static func trimmedBaseURL(_ baseURL: String) -> String {
        baseURL.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
    }

    private static func fetchOllamaModels(baseURL: String) async throws -> [String] {
        let data = try await get(trimmedBaseURL(baseURL) + "/api/tags")
        let decoded = try JSONDecoder().decode(OllamaTagsResponse.self, from: data)
        return Array(Set(decoded.models.map(\.name))).sorted()
    }

    private static func fetchOpenAICompatibleModels(baseURL: String) async throws -> [String] {
        let data = try await get(trimmedBaseURL(baseURL) + "/models")
        let decoded = try JSONDecoder().decode(OpenAIModelsListResponse.self, from: data)
        return Array(Set(decoded.data.map(\.id))).sorted()
    }

    private static func fetchOllamaContextLength(baseURL: String, model: String) async throws -> Int {
        guard let url = URL(string: trimmedBaseURL(baseURL) + "/api/show") else { throw URLError(.badURL) }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["name": model])
        let (data, response) = try await URLSession.shared.data(for: request)
        try checkStatus(response, data: data)

        guard let object = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let modelInfo = object["model_info"] as? [String: Any] else {
            throw ProviderDiscoveryError.profileSettingsUnavailable
        }
        // The key is family-prefixed ("llama.context_length",
        // "qwen2.context_length", ...) rather than a fixed name.
        for (key, value) in modelInfo where key.hasSuffix(".context_length") {
            if let intValue = value as? Int { return intValue }
            if let numberValue = value as? NSNumber { return numberValue.intValue }
        }
        throw ProviderDiscoveryError.profileSettingsUnavailable
    }

    private static func fetchLMStudioMetadata(
        baseURL: String,
        model: String
    ) async throws -> ProviderModelMetadata {
        // LM Studio's native REST API lives one path segment above the
        // OpenAI-compatible "/v1" root used for chat completions.
        var base = trimmedBaseURL(baseURL)
        if base.hasSuffix("/v1") {
            base = String(base.dropLast(3)).trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        }

        do {
            let data = try await get(base + "/api/v1/models")
            return try parseLMStudioMetadata(data, model: model)
        } catch {
            // LM Studio versions predating the v1 native API exposed a smaller
            // v0 response. Keep context discovery working for those versions.
            let data = try await get(base + "/api/v0/models")
            return try parseLegacyLMStudioMetadata(data, model: model)
        }
    }

    static func parseLMStudioMetadata(
        _ data: Data,
        model: String
    ) throws -> ProviderModelMetadata {
        let response = try JSONDecoder().decode(LMStudioModelsResponse.self, from: data)
        let languageModels = response.models.filter { $0.type == "llm" }
        let directMatch = languageModels.first {
            $0.key == model || $0.loadedInstances.contains { $0.id == model }
        }
        let aliasMatches = languageModels.filter {
            $0.key.split(separator: "/").last.map(String.init) == model
                || $0.loadedInstances.contains {
                    $0.id.split(separator: "/").last.map(String.init) == model
                }
        }
        guard let entry = directMatch ?? (aliasMatches.count == 1 ? aliasMatches[0] : nil) else {
            throw ProviderDiscoveryError.profileSettingsUnavailable
        }
        let instance = entry.loadedInstances.first(where: {
            $0.id == model || $0.id.split(separator: "/").last.map(String.init) == model
        }) ?? entry.loadedInstances.first
        let reasoningOptions = entry.capabilities?.reasoning?.allowedOptions
        let supportsReasoning = reasoningOptions.map { $0.contains("on") }

        return ProviderModelMetadata(
            model: entry.key,
            loadedInstance: instance?.id,
            configuredContextWindow: instance?.config.contextLength,
            maximumContextWindow: entry.maxContextLength,
            architecture: entry.architecture,
            quantization: entry.quantization?.name,
            parameterCount: entry.paramsString,
            supportsVision: entry.capabilities?.vision,
            trainedForToolUse: entry.capabilities?.trainedForToolUse,
            supportsReasoning: supportsReasoning,
            defaultReasoningEnabled: entry.capabilities?.reasoning?.defaultOption.map { $0 == "on" }
        )
    }

    private static func parseLegacyLMStudioMetadata(
        _ data: Data,
        model: String
    ) throws -> ProviderModelMetadata {
        let response = try JSONDecoder().decode(LegacyLMStudioModelsResponse.self, from: data)
        guard let entry = response.data.first(where: { $0.id == model }) else {
            throw ProviderDiscoveryError.profileSettingsUnavailable
        }
        return ProviderModelMetadata(
            model: entry.id,
            loadedInstance: nil,
            configuredContextWindow: entry.loadedContextLength,
            maximumContextWindow: entry.maxContextLength,
            architecture: nil,
            quantization: nil,
            parameterCount: nil,
            supportsVision: nil,
            trainedForToolUse: nil,
            supportsReasoning: nil,
            defaultReasoningEnabled: nil
        )
    }

    private static func get(_ endpoint: String) async throws -> Data {
        guard let url = URL(string: endpoint) else { throw URLError(.badURL) }
        let (data, response) = try await URLSession.shared.data(for: URLRequest(url: url))
        try checkStatus(response, data: data)
        return data
    }

    private static func checkStatus(_ response: URLResponse, data: Data) throws {
        guard let http = response as? HTTPURLResponse else { throw URLError(.badServerResponse) }
        guard (200..<300).contains(http.statusCode) else {
            throw OpenAIRequestError.http(http.statusCode, String(decoding: data.prefix(2000), as: UTF8.self))
        }
    }
}

private struct OllamaTagsResponse: Decodable {
    let models: [OllamaModelTag]
}

private struct OllamaModelTag: Decodable {
    let name: String
}

private struct OpenAIModelsListResponse: Decodable {
    let data: [OpenAIModelListEntry]
}

private struct OpenAIModelListEntry: Decodable {
    let id: String
}

private struct LMStudioModelsResponse: Decodable {
    let models: [LMStudioModelEntry]
}

private struct LMStudioModelEntry: Decodable {
    struct Quantization: Decodable {
        let name: String
    }

    struct LoadedInstance: Decodable {
        struct Configuration: Decodable {
            let contextLength: Int?

            enum CodingKeys: String, CodingKey {
                case contextLength = "context_length"
            }
        }

        let id: String
        let config: Configuration
    }

    struct Capabilities: Decodable {
        struct Reasoning: Decodable {
            let allowedOptions: [String]
            let defaultOption: String?

            enum CodingKeys: String, CodingKey {
                case allowedOptions = "allowed_options"
                case defaultOption = "default"
            }
        }

        let vision: Bool?
        let trainedForToolUse: Bool?
        let reasoning: Reasoning?

        enum CodingKeys: String, CodingKey {
            case vision
            case trainedForToolUse = "trained_for_tool_use"
            case reasoning
        }
    }

    let type: String
    let key: String
    let architecture: String?
    let quantization: Quantization?
    let paramsString: String?
    let loadedInstances: [LoadedInstance]
    let maxContextLength: Int?
    let capabilities: Capabilities?

    enum CodingKeys: String, CodingKey {
        case type, key, architecture, quantization, capabilities
        case paramsString = "params_string"
        case loadedInstances = "loaded_instances"
        case maxContextLength = "max_context_length"
    }
}

private struct LegacyLMStudioModelsResponse: Decodable {
    struct Entry: Decodable {
        let id: String
        let loadedContextLength: Int?
        let maxContextLength: Int?

        enum CodingKeys: String, CodingKey {
            case id
            case loadedContextLength = "loaded_context_length"
            case maxContextLength = "max_context_length"
        }
    }

    let data: [Entry]
}

struct OpenAIStreamEvent: Decodable, Sendable {
    struct Choice: Decodable, Sendable {
        let delta: Delta
        let finishReason: String?
        enum CodingKeys: String, CodingKey { case delta; case finishReason = "finish_reason" }
    }
    struct Delta: Decodable, Sendable {
        let content: String?
        // Thinking models stream reasoning text under one of these keys
        // depending on the provider (LM Studio/vLLM use "reasoning", Ollama
        // and some OpenAI-compatible gateways use "reasoning_content").
        let reasoning: String?
        let reasoningContent: String?
        let toolCalls: [ToolCallDelta]?
        enum CodingKeys: String, CodingKey {
            case content
            case reasoning
            case reasoningContent = "reasoning_content"
            case toolCalls = "tool_calls"
        }
    }
    struct ToolCallDelta: Decodable, Sendable {
        struct Function: Decodable, Sendable { let name: String?; let arguments: String? }
        let index: Int
        let id: String?
        let type: String?
        let function: Function?
    }
    let choices: [Choice]
}

actor ToolApprovalCenter {
    private var continuations: [UUID: CheckedContinuation<Bool, Never>] = [:]
    private var earlyResults: [UUID: Bool] = [:]

    func wait(for id: UUID) async -> Bool {
        // A resolve() can arrive before wait() is called (the request and its
        // approval/rejection race across tasks). Check for an early result
        // first, or the continuation below would never be resumed.
        if let result = earlyResults.removeValue(forKey: id) { return result }
        return await withCheckedContinuation { continuation in continuations[id] = continuation }
    }

    func resolve(_ id: UUID, approved: Bool) {
        if let continuation = continuations.removeValue(forKey: id) {
            continuation.resume(returning: approved)
        } else {
            earlyResults[id] = approved
        }
    }

    func cancelAll() {
        let pending = continuations.values
        continuations.removeAll()
        earlyResults.removeAll()
        for continuation in pending { continuation.resume(returning: false) }
    }
}

actor UserQuestionCenter {
    private var continuations: [UUID: CheckedContinuation<UserQuestionResponse, Never>] = [:]
    private var earlyResults: [UUID: UserQuestionResponse] = [:]

    func wait(for id: UUID) async -> UserQuestionResponse {
        if let result = earlyResults.removeValue(forKey: id) { return result }
        return await withCheckedContinuation { continuation in
            continuations[id] = continuation
        }
    }

    func resolve(_ id: UUID, response: UserQuestionResponse) {
        if let continuation = continuations.removeValue(forKey: id) {
            continuation.resume(returning: response)
        } else {
            earlyResults[id] = response
        }
    }

    func cancelAll() {
        let pending = continuations.values
        continuations.removeAll()
        earlyResults.removeAll()
        for continuation in pending {
            continuation.resume(returning: .skipped)
        }
    }
}
