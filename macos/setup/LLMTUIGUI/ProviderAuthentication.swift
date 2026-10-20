import Foundation

/// Credentials are resolved for each request, never cached in a chat service.
enum ProviderAuthentication {
    struct MissingCredential: LocalizedError {
        let provider: String
        let variable: String
        var errorDescription: String? {
            "Provider \(provider) requires \(variable). Set it in the app's process environment and relaunch the app, or configure this provider's api_key in YAML."
        }
    }

    static func apply(
        to request: inout URLRequest,
        provider: ProviderConfiguration,
        environment: [String: String] = ProcessInfo.processInfo.environment
    ) throws {
        let variable = provider.apiKeyEnvironment.trimmingCharacters(in: .whitespacesAndNewlines)
        let fromEnvironment = environment[variable]?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let explicit = provider.apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        var key = fromEnvironment.isEmpty ? explicit : fromEnvironment
        let source = !fromEnvironment.isEmpty ? "environment" : (!explicit.isEmpty ? "explicit configuration" : "none")
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
}
