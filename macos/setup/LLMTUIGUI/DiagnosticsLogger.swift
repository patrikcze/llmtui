import Foundation
import OSLog

nonisolated enum DiagnosticLevel: Int, CaseIterable, Identifiable, Codable, Sendable {
    case debug = 0
    case info = 1
    case warning = 2
    case error = 3

    var id: Int { rawValue }

    var title: String {
        switch self {
        case .debug: "Debug"
        case .info: "Info"
        case .warning: "Warning"
        case .error: "Error"
        }
    }

    var osLogType: OSLogType {
        switch self {
        case .debug: .debug
        case .info: .info
        case .warning: .default
        case .error: .error
        }
    }

    init(from decoder: Decoder) throws {
        let value = try decoder.singleValueContainer().decode(String.self)
        guard let level = Self.allCases.first(where: { $0.title.lowercased() == value }) else {
            throw DecodingError.dataCorruptedError(
                in: try decoder.singleValueContainer(),
                debugDescription: "Unknown diagnostic level."
            )
        }
        self = level
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        try container.encode(title.lowercased())
    }
}

nonisolated enum DiagnosticCategory: String, Codable, Sendable {
    case application
    case configuration
    case provider
    case chat
    case tools
    case personalApps = "personal_apps"
    case usage
    case rendering
}

nonisolated enum DiagnosticValue: Codable, Equatable, Sendable {
    case string(String)
    case integer(Int)
    case double(Double)
    case boolean(Bool)

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if let value = try? container.decode(Bool.self) {
            self = .boolean(value)
        } else if let value = try? container.decode(Int.self) {
            self = .integer(value)
        } else if let value = try? container.decode(Double.self) {
            self = .double(value)
        } else {
            self = .string(try container.decode(String.self))
        }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .string(let value): try container.encode(value)
        case .integer(let value): try container.encode(value)
        case .double(let value): try container.encode(value)
        case .boolean(let value): try container.encode(value)
        }
    }
}

nonisolated struct SafeDiagnosticError: Codable, Equatable, Sendable {
    let type: String
    let domain: String?
    let code: Int?
    let classification: String
    let summary: String

    static func describe(_ error: Error, operation: String) -> SafeDiagnosticError {
        let nsError = error as NSError
        let classification: String
        let summary: String

        if error is CancellationError || nsError.code == NSUserCancelledError {
            classification = "cancelled"
            summary = "The operation was cancelled."
        } else if nsError.domain == NSURLErrorDomain {
            switch nsError.code {
            case NSURLErrorTimedOut:
                classification = "timeout"
                summary = "The network operation timed out."
            case NSURLErrorCannotConnectToHost, NSURLErrorCannotFindHost:
                classification = "connection"
                summary = "The provider could not be reached."
            case NSURLErrorNotConnectedToInternet:
                classification = "offline"
                summary = "The network is unavailable."
            case NSURLErrorCancelled:
                classification = "cancelled"
                summary = "The network operation was cancelled."
            default:
                classification = "network"
                summary = "The network operation failed."
            }
        } else if let requestError = error as? OpenAIRequestError {
            switch requestError {
            case .http(let status, _):
                classification = "http_\(status)"
                summary = "The provider returned HTTP status \(status)."
            }
        } else if error is DecodingError {
            classification = "decoding"
            summary = "A response could not be decoded."
        } else {
            classification = "operation"
            summary = "The \(operation) operation failed."
        }

        return SafeDiagnosticError(
            type: String(reflecting: Swift.type(of: error)),
            domain: nsError.domain.isEmpty ? nil : nsError.domain,
            code: nsError.code,
            classification: classification,
            summary: summary
        )
    }
}

nonisolated struct DiagnosticRecord: Codable, Equatable, Sendable {
    let timestamp: String
    let level: DiagnosticLevel
    let category: DiagnosticCategory
    let event: String
    let sessionID: String
    let requestID: String?
    let metadata: [String: DiagnosticValue]
    let error: SafeDiagnosticError?

    enum CodingKeys: String, CodingKey {
        case timestamp
        case level
        case category
        case event
        case sessionID = "session_id"
        case requestID = "request_id"
        case metadata
        case error
    }
}

nonisolated protocol DiagnosticsLogging: Sendable {
    func log(
        level: DiagnosticLevel,
        category: DiagnosticCategory,
        event: String,
        requestID: UUID?,
        metadata: [String: DiagnosticValue],
        error: SafeDiagnosticError?
    ) async

    func clear() async throws
}

actor DiagnosticsLogger: DiagnosticsLogging {
    static let shared = DiagnosticsLogger()

    static let enabledDefaultsKey = "diagnosticLoggingEnabled"
    static let levelDefaultsKey = "diagnosticLogLevel"
    static let defaultMaximumFileSize = 2 * 1_024 * 1_024
    static let defaultArchiveCount = 4

    static let defaultDirectoryURL = FileManager.default.homeDirectoryForCurrentUser
        .appending(path: ".local")
        .appending(path: "share")
        .appending(path: "llmtui")
        .appending(path: "logs")

    static let defaultFileURL = defaultDirectoryURL.appending(path: "llmtuigui.log")

    private let fileManager: FileManager
    private let defaults: UserDefaults
    private let directoryURL: URL
    private let fileURL: URL
    private let maximumFileSize: Int
    private let archiveCount: Int
    private let sessionID: String
    private let unifiedLogger = Logger(
        subsystem: Bundle.main.bundleIdentifier ?? "com.patrikcze.llmtuigui",
        category: "diagnostics"
    )

    init(
        fileManager: FileManager = .default,
        defaults: UserDefaults = .standard,
        directoryURL: URL = DiagnosticsLogger.defaultDirectoryURL,
        fileName: String = "llmtuigui.log",
        maximumFileSize: Int = DiagnosticsLogger.defaultMaximumFileSize,
        archiveCount: Int = DiagnosticsLogger.defaultArchiveCount,
        sessionID: UUID = UUID()
    ) {
        self.fileManager = fileManager
        self.defaults = defaults
        self.directoryURL = directoryURL
        self.fileURL = directoryURL.appending(path: fileName)
        self.maximumFileSize = maximumFileSize
        self.archiveCount = archiveCount
        self.sessionID = sessionID.uuidString
        defaults.register(defaults: [
            Self.enabledDefaultsKey: true,
            Self.levelDefaultsKey: DiagnosticLevel.info.rawValue
        ])
    }

    func log(
        level: DiagnosticLevel,
        category: DiagnosticCategory,
        event: String,
        requestID: UUID? = nil,
        metadata: [String: DiagnosticValue] = [:],
        error: SafeDiagnosticError? = nil
    ) async {
        guard defaults.bool(forKey: Self.enabledDefaultsKey),
              level.rawValue >= configuredLevel.rawValue else {
            return
        }

        let record = DiagnosticRecord(
            timestamp: Self.timestamp(),
            level: level,
            category: category,
            event: Self.safeEventName(event),
            sessionID: sessionID,
            requestID: requestID?.uuidString,
            metadata: metadata,
            error: error
        )

        unifiedLogger.log(
            level: level.osLogType,
            "\(category.rawValue, privacy: .public).\(record.event, privacy: .public) request_id=\(record.requestID ?? "-", privacy: .public)"
        )

        do {
            try append(record)
        } catch {
            unifiedLogger.error(
                "diagnostic_file_write_failed type=\(String(reflecting: Swift.type(of: error)), privacy: .public)"
            )
        }
    }

    func clear() async throws {
        for index in 0...archiveCount {
            let candidate = index == 0
                ? fileURL
                : URL(fileURLWithPath: fileURL.path + ".\(index)")
            if fileManager.fileExists(atPath: candidate.path) {
                try fileManager.removeItem(at: candidate)
            }
        }
    }

    private var configuredLevel: DiagnosticLevel {
        DiagnosticLevel(rawValue: defaults.integer(forKey: Self.levelDefaultsKey)) ?? .info
    }

    private func append(_ record: DiagnosticRecord) throws {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        var line = try encoder.encode(record)
        line.append(0x0A)

        try prepareDirectory()
        try rotateIfNeeded(adding: line.count)

        if !fileManager.fileExists(atPath: fileURL.path) {
            guard fileManager.createFile(atPath: fileURL.path, contents: nil) else {
                throw CocoaError(.fileWriteUnknown)
            }
            try fileManager.setAttributes(
                [.posixPermissions: NSNumber(value: Int16(0o600))],
                ofItemAtPath: fileURL.path
            )
        }

        let handle = try FileHandle(forWritingTo: fileURL)
        defer { try? handle.close() }
        try handle.seekToEnd()
        try handle.write(contentsOf: line)
    }

    private func prepareDirectory() throws {
        if !fileManager.fileExists(atPath: directoryURL.path) {
            try fileManager.createDirectory(
                at: directoryURL,
                withIntermediateDirectories: true,
                attributes: [.posixPermissions: NSNumber(value: Int16(0o700))]
            )
        }
    }

    private func rotateIfNeeded(adding byteCount: Int) throws {
        let currentSize = (try? fileManager.attributesOfItem(atPath: fileURL.path)[.size] as? NSNumber)?.intValue ?? 0
        guard currentSize > 0, currentSize + byteCount > maximumFileSize else { return }

        if archiveCount > 0 {
            let oldest = URL(fileURLWithPath: fileURL.path + ".\(archiveCount)")
            if fileManager.fileExists(atPath: oldest.path) {
                try fileManager.removeItem(at: oldest)
            }

            if archiveCount > 1 {
                for index in stride(from: archiveCount - 1, through: 1, by: -1) {
                    let source = URL(fileURLWithPath: fileURL.path + ".\(index)")
                    let destination = URL(fileURLWithPath: fileURL.path + ".\(index + 1)")
                    if fileManager.fileExists(atPath: source.path) {
                        try fileManager.moveItem(at: source, to: destination)
                    }
                }
            }

            if fileManager.fileExists(atPath: fileURL.path) {
                try fileManager.moveItem(
                    at: fileURL,
                    to: URL(fileURLWithPath: fileURL.path + ".1")
                )
            }
        } else if fileManager.fileExists(atPath: fileURL.path) {
            try fileManager.removeItem(at: fileURL)
        }
    }

    private static func timestamp() -> String {
        Date.now.formatted(
            .iso8601
                .year()
                .month()
                .day()
                .time(includingFractionalSeconds: true)
                .timeZone(separator: .colon)
        )
    }

    private static func safeEventName(_ value: String) -> String {
        let allowed = CharacterSet.alphanumerics.union(CharacterSet(charactersIn: "._-"))
        return String(value.unicodeScalars.map { allowed.contains($0) ? Character(String($0)) : "_" })
    }
}
