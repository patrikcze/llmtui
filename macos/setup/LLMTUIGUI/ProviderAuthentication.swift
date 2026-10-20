import Foundation

/// Credentials are resolved for each request, never cached in a chat service.
enum ProviderAuthentication {
    struct MissingCredential: LocalizedError {
        let provider: String
        let variable: String
        var errorDescription: String? {
            "Provider \(provider) requires \(variable). Set it with `launchctl setenv \(variable) <key>`, or configure this provider's api_key in YAML."
        }
    }

    static func apply(
        to request: inout URLRequest,
        provider: ProviderConfiguration,
        environment: [String: String] = ProcessInfo.processInfo.environment,
        launchd: (String) -> String? = launchdValue
    ) throws {
        let variable = provider.apiKeyEnvironment.trimmingCharacters(in: .whitespacesAndNewlines)
        var fromEnvironment = environment[variable]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        var source = "environment"
        // An app opened from Finder or the Dock does not see shell exports,
        // and only sees `launchctl setenv` values set before it launched, so
        // ask launchd when its own environment has nothing.
        if fromEnvironment.isEmpty, isValidVariableName(variable) {
            fromEnvironment = launchd(variable)?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            source = "launchd"
        }
        let explicit = provider.apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        var key = fromEnvironment.isEmpty ? explicit : fromEnvironment
        if fromEnvironment.isEmpty { source = explicit.isEmpty ? "none" : "explicit configuration" }
        if key.lowercased().hasPrefix("bearer ") {
            key = String(key.dropFirst(7)).trimmingCharacters(in: .whitespacesAndNewlines)
        }
        let resolved = !key.isEmpty
        request.setValue(resolved ? "Bearer \(key)" : nil, forHTTPHeaderField: "Authorization")
        Task {
            await DiagnosticsLogger.shared.log(
                level: .debug, category: .provider, event: "provider.authentication",
                metadata: ["provider": .string(provider.name), "credential_source": .string(source),
                           "credential_resolved": .boolean(resolved), "authorization_attached": .boolean(resolved)]
            )
        }
        if !variable.isEmpty && !resolved {
            throw MissingCredential(provider: provider.name, variable: variable)
        }
    }

    /// A POSIX environment variable name; nothing else is passed to launchctl.
    nonisolated static func isValidVariableName(_ name: String) -> Bool {
        guard let first = name.unicodeScalars.first,
              first == "_" || (first.isASCII && CharacterSet.letters.contains(first)) else { return false }
        return name.unicodeScalars.allSatisfy { $0 == "_" || ($0.isASCII && CharacterSet.alphanumerics.contains($0)) }
    }

    /// `launchctl getenv <name>` for the user's session; nil on any failure.
    nonisolated static func launchdValue(_ name: String) -> String? {
        guard isValidVariableName(name) else { return nil }
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/bin/launchctl")
        process.arguments = ["getenv", name]
        let output = Pipe()
        process.standardOutput = output
        process.standardError = FileHandle.nullDevice
        do { try process.run() } catch { return nil }
        let data = output.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()
        guard process.terminationStatus == 0 else { return nil }
        return String(decoding: data, as: UTF8.self)
    }
}
