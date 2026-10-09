import Foundation
import Observation

/// UI state contains profiles and catalog metadata only, never credentials.
@MainActor @Observable
final class MobileMCPController {
    private(set) var profiles: [MobileMCPProfile]
    private(set) var connections: [UUID: MobileMCPConnection] = [:]
    private(set) var busy = Set<UUID>()
    var errors: [UUID: String] = [:]
    private let service: MobileMCPService
    private let store: MobileMCPProfileStore
    private let credentials: any MCPCredentialStoring
    private let oauth: MCPOAuth
    private let browser = MCPOAuthBrowser()
    private var tasks: [UUID: Task<Void, Never>] = [:]
    private var revocations: [UUID: Task<Void, Never>] = [:]
    private var attempts: [UUID: UUID] = [:]
    private var requiredScopes: [UUID: String] = [:]

    init(service: MobileMCPService = MobileMCPService(), store: MobileMCPProfileStore = MobileMCPProfileStore(), credentials: any MCPCredentialStoring = MCPCredentialStore(), oauth: MCPOAuth = MCPOAuth()) {
        self.service = service; self.store = store; self.credentials = credentials; self.oauth = oauth
        profiles = store.load()
    }
    func save(_ input: MobileMCPProfile, bearer: String?) throws {
        var profile = input
        profile.name = String(profile.name.trimmingCharacters(in: .whitespacesAndNewlines).prefix(100))
        profile.endpoint = profile.endpoint.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !profile.name.isEmpty else { throw MobileMCPError.message("Enter a server name.") }
        _ = try profile.validatedURL()
        profile.revision = UUID()
        disconnect(profile.id)
        let old = profiles.first(where: { $0.id == profile.id })
        if let bearer {
            let token = bearer.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !token.contains("\r"), !token.contains("\n") else { throw MobileMCPError.message("Invalid bearer token.") }
            try credentials.save(MCPCredentials(accessToken: token), id: profile.id)
        } else if let old, old.authentication != .none, old.endpoint != profile.endpoint || old.authentication != profile.authentication || old.clientID != profile.clientID || old.clientMetadataURL != profile.clientMetadataURL {
            try credentials.remove(profile.id)
        }
        if profile.authentication == .none, old?.authentication != nil, old?.authentication != MobileMCPAuth.none { try credentials.remove(profile.id) }
        var updated = profiles.filter { $0.id != profile.id }; updated.append(profile)
        try store.save(updated)
        profiles = updated
        disconnect(profile.id)
    }
    func remove(_ id: UUID) throws {
        disconnect(id)
        let updated = profiles.filter { $0.id != id }
        if profiles.first(where: { $0.id == id })?.authentication != MobileMCPAuth.none { try credentials.remove(id) }
        try store.save(updated)
        profiles = updated; disconnect(id); errors[id] = nil
    }
    func disconnect(_ id: UUID) {
        tasks.removeValue(forKey: id)?.cancel(); attempts[id] = UUID()
        busy.remove(id); connections[id] = nil
        let prior = revocations[id]
        revocations[id] = Task { await prior?.value; await service.disconnect(id) }
    }
    /// Closes every connection (sign-out of the session as a whole).
    func suspend() {
        browser.cancel()
        for id in profiles.map(\.id) { disconnect(id) }
    }
    /// The app moved to the background (screen lock, app switch). Work in
    /// flight is cancelled — connection attempts and sign-ins; a running reply
    /// and its approvals are stopped by the chat model — but established,
    /// idle connections stay, so returning does not need a manual reconnect.
    /// Nothing is resumed or replayed on return, and every tool call still
    /// re-checks the server's tool schema before it is sent.
    func enterBackground() {
        browser.cancel()
        for id in Array(tasks.keys) { disconnect(id) }
    }
    func signOut(_ id: UUID) {
        disconnect(id)
        do { try credentials.remove(id) } catch { errors[id] = "Could not remove MCP credentials from Keychain." }
    }
    func connect(_ id: UUID, signIn: Bool = false) {
        guard let profile = profiles.first(where: { $0.id == id }), !busy.contains(id) else { return }
        let attempt = UUID(); attempts[id] = attempt
        busy.insert(id); connections[id] = nil; errors[id] = nil
        tasks[id] = Task { [weak self] in
            guard let self else { return }
            do {
                await revocations[id]?.value
                try Task.checkCancellation()
                guard attempts[id] == attempt else { throw CancellationError() }
                var secret = profile.authentication == .none ? MCPCredentials() : try credentials.load(id)
                if profile.authentication == .oauth {
                    if signIn {
                        secret = try await oauth.authorize(profile, previousScopes: secret.scopes + " " + (requiredScopes[id] ?? "")) { [weak self] url in
                            guard let self else { throw CancellationError() }
                            return try await self.browser.open(url)
                        }
                    } else if let expires = secret.expiresAt, expires <= .now {
                        secret = try await oauth.refresh(secret, profile: profile)
                    }
                    try Task.checkCancellation()
                    guard attempts[id] == attempt else { throw CancellationError() }
                    try credentials.save(secret, id: id)
                }
                guard profile.authentication == .none || !secret.accessToken.isEmpty else { throw MobileMCPError.authentication }
                let token = profile.authentication == .none ? "" : secret.accessToken
                let info = try await service.connect(profile, token: token)
                try Task.checkCancellation()
                guard attempts[id] == attempt, profiles.contains(profile) else { throw CancellationError() }
                connections[id] = info
                requiredScopes[id] = nil
            } catch {
                if attempts[id] == attempt {
                    // No server body or SDK diagnostic (which may echo credentials)
                    // is surfaced. App-owned errors have deliberately fixed wording.
                    if case MobileMCPError.scopeRequired(let scope) = error { requiredScopes[id] = scope }
                    errors[id] = error is CancellationError ? "Connection interrupted. Reconnect explicitly." : (error as? MobileMCPError)?.localizedDescription ?? "MCP connection failed. Check the endpoint and authentication."
                }
            }
            if attempts[id] == attempt { busy.remove(id); tasks[id] = nil }
        }
    }
    #if DEBUG
    func waitForConnectionForTesting(_ id: UUID) async { await tasks[id]?.value }
    #endif
    func bindings(for selected: [UUID]) throws -> [String: MobileMCPBinding] {
        var result: [String: MobileMCPBinding] = [:], bytes = 0
        for profile in profiles where selected.contains(profile.id) {
            guard let info = connections[profile.id] else { continue }
            for tool in info.tools {
                let binding = MobileMCPBinding(serverID: profile.id, serverName: profile.name, generation: info.generation, tool: tool)
                guard result[binding.providerName] == nil else { throw MobileMCPError.message("MCP tool name collision.") }
                bytes += try tool.providerDefinition(serverID: profile.id, serverName: profile.name).data().count
                result[binding.providerName] = binding
            }
        }
        guard result.count <= MobileMCPLimits.tools, bytes <= MobileMCPLimits.catalog else { throw MobileMCPError.message("Selected MCP servers exceed the 64-tool or 256 KiB catalog limit. Select fewer servers.") }
        return result
    }
    func call(_ binding: MobileMCPBinding, arguments: MCPJSON) async throws -> MobileMCPResult {
        try Task.checkCancellation()
        guard connections[binding.serverID]?.generation == binding.generation else { throw MobileMCPError.changed }
        do {
            let result = try await service.call(binding, arguments: arguments)
            try Task.checkCancellation()
            guard connections[binding.serverID]?.generation == binding.generation else { throw MobileMCPError.changed }
            return result
        }
        catch {
            if case MobileMCPError.scopeRequired(let scope) = error { requiredScopes[binding.serverID] = scope }
            connections[binding.serverID] = nil
            await service.disconnect(binding.serverID)
            errors[binding.serverID] = "Call interrupted or failed. Effects may have occurred; no automatic replay was made. Reconnect explicitly."
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            throw (error as? MobileMCPError) ?? MobileMCPError.message("MCP call failed. Its effects may be unknown; no automatic retry was made.")
        }
    }
}
