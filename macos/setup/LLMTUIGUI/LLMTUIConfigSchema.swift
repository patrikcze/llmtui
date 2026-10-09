import Foundation

/// Every key llmtui's config.yaml can set, as reported by the llmtui binary
/// itself (`llmtui config schema`), so this app offers exactly the settings
/// of the llmtui it runs alongside instead of a hand-kept list.
///
/// A "*" key segment stands for a user-chosen name (providers.*.type).
nonisolated struct LLMTUIConfigSchema: Sendable, Equatable {
    nonisolated struct Field: Sendable, Equatable, Identifiable {
        enum Kind: String, Sendable {
            case bool, int, float, string, duration, list, map
        }

        var id: String { key }
        let key: String
        let kind: Kind
        /// llmtui's default as YAML scalar text ("true", "4096", "24h"), or
        /// nil when it has none or the default is a list.
        let defaultValue: String?
        let listDefault: [String]
        let allowedValues: [String]
        /// Values that must never be shown or written by this app (provider
        /// API keys, MCP server environment values).
        let isSecret: Bool

        /// The top-level section, e.g. "tools" for tools.web.enabled.
        var section: String { String(key.prefix { $0 != "." }) }
    }

    let fields: [Field]
    private let byKey: [String: Field]

    init(fields: [Field]) {
        self.fields = fields
        self.byKey = Dictionary(fields.map { ($0.key, $0) }, uniquingKeysWith: { first, _ in first })
    }

    static func == (lhs: Self, rhs: Self) -> Bool { lhs.fields == rhs.fields }

    /// Decodes the JSON printed by `llmtui config schema`.
    static func decode(_ data: Data) throws -> LLMTUIConfigSchema {
        struct Raw: Decodable {
            struct RawField: Decodable {
                let key: String
                let type: String
                let `default`: SchemaDefaultValue?
                let `enum`: [String]?
                let secret: Bool?
            }
            let fields: [RawField]
        }
        let raw = try JSONDecoder().decode(Raw.self, from: data)
        return LLMTUIConfigSchema(fields: raw.fields.map { field in
            let kind = Field.Kind(rawValue: field.type) ?? .string
            var scalar: String?
            var list: [String] = []
            switch field.default {
            case .string(let value): scalar = value
            case .bool(let value): scalar = value ? "true" : "false"
            case .number(let value):
                scalar = kind == .int ? String(Int(value)) : String(value)
            case .list(let values): list = values
            case nil: break
            }
            return Field(
                key: field.key,
                kind: kind,
                defaultValue: scalar,
                listDefault: list,
                allowedValues: field.enum ?? [],
                isSecret: field.secret ?? false
            )
        })
    }

    /// The field for a concrete key: providers.lmstudio.type matches
    /// providers.*.type, and a key inside a map field (an MCP server's env)
    /// matches that map field.
    func field(for key: String) -> Field? {
        if let exact = byKey[key] { return exact }
        let parts = key.split(separator: ".").map(String.init)
        return match(parts, index: 0, prefix: [])
    }

    private func match(_ parts: [String], index: Int, prefix: [String]) -> Field? {
        let candidate = prefix.joined(separator: ".")
        if !prefix.isEmpty, let field = byKey[candidate], field.kind == .map {
            return field
        }
        guard index < parts.count else { return byKey[candidate] }
        return match(parts, index: index + 1, prefix: prefix + [parts[index]])
            ?? match(parts, index: index + 1, prefix: prefix + ["*"])
    }

    /// Whether this app must never show or write `key`. Keys the schema does
    /// not know are judged by name, so an unknown `*.api_key` stays hidden.
    func isSecret(_ key: String) -> Bool {
        Self.looksSecret(key) || field(for: key)?.isSecret == true
    }

    static func looksSecret(_ key: String) -> Bool {
        let leaf = key.split(separator: ".").last.map(String.init) ?? key
        return leaf == "api_key" || key.hasPrefix("mcp.servers.") && key.contains(".env.")
            || key.hasPrefix("mcp.servers.") && key.hasSuffix(".env")
    }

    /// llmtui's default for a concrete key with no "*" segment, as YAML text.
    func defaultValue(for key: String) -> String? {
        byKey[key]?.defaultValue
    }

    /// Scalar defaults for every key without a "*" segment.
    var scalarDefaults: [String: String] {
        var result: [String: String] = [:]
        for field in fields where !field.key.contains("*") {
            if let value = field.defaultValue { result[field.key] = value }
        }
        return result
    }
}

/// A JSON scalar or string list, enough for schema defaults.
private nonisolated enum SchemaDefaultValue: Decodable {
    case string(String)
    case bool(Bool)
    case number(Double)
    case list([String])

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if let value = try? container.decode(Bool.self) {
            self = .bool(value)
        } else if let value = try? container.decode(Double.self) {
            self = .number(value)
        } else if let value = try? container.decode(String.self) {
            self = .string(value)
        } else if let value = try? container.decode([String].self) {
            self = .list(value)
        } else {
            self = .list([])
        }
    }
}

/// One finding of `llmtui config validate`.
nonisolated struct LLMTUIConfigProblem: Decodable, Sendable, Equatable, Identifiable {
    var id: String { key + message }
    let key: String
    let message: String
    let error: Bool?

    var isError: Bool { error == true }
}

/// Runs the llmtui binary for the schema and for validation. Every call has
/// a timeout and passes only fixed arguments plus a path this app chose.
nonisolated enum LLMTUIConfigCommands {
    enum Outcome: Sendable, Equatable {
        /// llmtui checked the configuration.
        case checked([LLMTUIConfigProblem])
        /// llmtui could not load it at all (YAML or load error).
        case failed(String)
        /// No llmtui was found, or it is too old to have the command.
        case unavailable(String)
    }

    struct Result: Sendable {
        let status: Int32
        let stdout: Data
        let stderr: String
    }

    /// The schema of the llmtui at `executable`, or nil when it is missing
    /// or predates `config schema`.
    static func loadSchema(executable: URL? = LLMTUIExecutable.locate()) async -> LLMTUIConfigSchema? {
        guard let executable,
              let result = try? await run(executable, arguments: ["config", "schema"]),
              result.status == 0
        else { return nil }
        return try? LLMTUIConfigSchema.decode(result.stdout)
    }

    /// Runs `llmtui --config <file> config validate --json` on `file`.
    static func validate(file: URL, executable: URL? = LLMTUIExecutable.locate()) async -> Outcome {
        guard let executable else {
            return .unavailable("llmtui was not found, so the file was not checked.")
        }
        let result: Result
        do {
            result = try await run(executable, arguments: ["--config", file.path, "config", "validate", "--json"])
        } catch {
            return .unavailable("llmtui could not be run: \(error.localizedDescription)")
        }
        struct Report: Decodable { let problems: [LLMTUIConfigProblem] }
        if let report = try? JSONDecoder().decode(Report.self, from: result.stdout) {
            return .checked(report.problems)
        }
        if result.stderr.contains("unknown command") || result.stderr.contains("unknown flag") {
            return .unavailable("This llmtui is too old to check configurations; update it to check before saving.")
        }
        let message = result.stderr
            .split(separator: "\n")
            .first { $0.hasPrefix("Error:") }
            .map { String($0.dropFirst("Error:".count)).trimmingCharacters(in: .whitespaces) }
        return .failed(message ?? "llmtui could not load the configuration (exit status \(result.status)).")
    }

    /// Validates `text` without touching config.yaml: it is written to a
    /// user-only (0600) file next to it, checked, and removed.
    static func validate(text: String, nextTo configURL: URL, executable: URL? = LLMTUIExecutable.locate()) async -> Outcome {
        let directory = configURL.deletingLastPathComponent()
        let draft = directory.appending(path: ".config.yaml.check.\(UUID().uuidString).yaml")
        defer { try? FileManager.default.removeItem(at: draft) }
        do {
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
            guard FileManager.default.createFile(
                atPath: draft.path,
                contents: Data(text.utf8),
                attributes: [.posixPermissions: NSNumber(value: 0o600)]
            ) else {
                return .unavailable("The draft could not be written for checking.")
            }
        } catch {
            return .unavailable("The draft could not be written for checking: \(error.localizedDescription)")
        }
        return await validate(file: draft, executable: executable)
    }

    static func run(_ executable: URL, arguments: [String], timeout: Duration = .seconds(15)) async throws -> Result {
        try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .userInitiated).async {
                let process = Process()
                let stdout = Pipe()
                let stderr = Pipe()
                process.executableURL = executable
                process.arguments = arguments
                process.standardOutput = stdout
                process.standardError = stderr
                process.standardInput = FileHandle.nullDevice
                do {
                    try process.run()
                } catch {
                    continuation.resume(throwing: error)
                    return
                }
                let deadline = DispatchTime.now() + .milliseconds(Int(timeout.components.seconds * 1000))
                DispatchQueue.global().asyncAfter(deadline: deadline) {
                    if process.isRunning { process.terminate() }
                }
                // Read both pipes concurrently so a large schema on stdout
                // cannot block behind a full stderr pipe or the other way round.
                var errorData = Data()
                let group = DispatchGroup()
                group.enter()
                DispatchQueue.global().async {
                    errorData = stderr.fileHandleForReading.readDataToEndOfFile()
                    group.leave()
                }
                let outputData = stdout.fileHandleForReading.readDataToEndOfFile()
                group.wait()
                process.waitUntilExit()
                continuation.resume(returning: Result(
                    status: process.terminationStatus,
                    stdout: outputData,
                    stderr: String(decoding: errorData, as: UTF8.self)
                ))
            }
        }
    }
}
