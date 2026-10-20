import Foundation

/// Resolves the provider's API key from the environment variable named by
/// `api_key_env`, like the Go app's `ProviderConfig.ResolveAPIKey`, and adds
/// it to requests sent to that provider's own server. The key is never
/// stored, logged, or sent to any other host (web tools build their own
/// requests and never call this).
///
/// A GUI app does not see variables exported in a shell profile. It does
/// see variables set with `launchctl setenv` when launched afterwards, so
/// the app's own environment is read first; if the variable is missing
/// there (for example the app was opened before `launchctl setenv` ran),
/// launchd is asked directly.
nonisolated enum ProviderAPIKey {
    /// The key for `environmentName`, or nil when the name is empty or invalid
    /// or the variable is unset or empty.
    static func resolve(
        environmentName: String,
        environment: [String: String] = ProcessInfo.processInfo.environment,
        launchd: (String) -> String? = launchdValue
    ) -> String? {
        let name = environmentName.trimmingCharacters(in: .whitespaces)
        guard isValidName(name) else { return nil }
        if let value = nonEmpty(environment[name]) { return value }
        return nonEmpty(launchd(name))
    }

    /// The key for the chat provider in `configuration`.
    static func resolve(for configuration: LLMTUIConfiguration) -> String? {
        resolve(environmentName: configuration.provider.apiKeyEnvironment)
    }

    /// Adds `Authorization: Bearer <key>` when a key resolves.
    static func authorize(_ request: inout URLRequest, apiKey: String?) {
        guard let apiKey else { return }
        request.setValue("Bearer \(apiKey)", forHTTPHeaderField: "Authorization")
    }

    /// A POSIX environment variable name, so nothing else is ever passed to
    /// `launchctl`.
    static func isValidName(_ name: String) -> Bool {
        guard let first = name.unicodeScalars.first, first == "_" || (first.isASCII && CharacterSet.letters.contains(first)) else {
            return false
        }
        return name.unicodeScalars.allSatisfy { $0 == "_" || ($0.isASCII && CharacterSet.alphanumerics.contains($0)) }
    }

    /// `launchctl getenv <name>` for the user's GUI session; nil on any failure.
    static func launchdValue(_ name: String) -> String? {
        guard isValidName(name) else { return nil }
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
        return String(decoding: data, as: UTF8.self).trimmingCharacters(in: .newlines)
    }

    private static func nonEmpty(_ value: String?) -> String? {
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else { return nil }
        return value
    }
}
