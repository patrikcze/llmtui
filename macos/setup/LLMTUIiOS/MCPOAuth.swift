import Foundation
import CryptoKit
import AuthenticationServices
import UIKit

@MainActor
final class MCPOAuthBrowser: NSObject, ASWebAuthenticationPresentationContextProviding {
    private var session: ASWebAuthenticationSession?
    private var pending: CheckedContinuation<URL, Error>?
    private var attempt: UUID?
    func open(_ url: URL) async throws -> URL {
        try Task.checkCancellation()
        guard pending == nil else { throw MobileMCPError.message("Another MCP sign-in is already in progress.") }
        let id = UUID(); attempt = id
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { continuation in
                self.pending = continuation
                let session = ASWebAuthenticationSession(url: url, callbackURLScheme: "llmtui-ios-mcp") { [weak self] url, error in
                    Task { @MainActor in
                        guard self?.attempt == id, let pending = self?.pending else { return }
                        self?.pending = nil; self?.session = nil; self?.attempt = nil
                        if let url { pending.resume(returning: url) }
                        else { pending.resume(throwing: error ?? CancellationError()) }
                    }
                }
                self.session = session
                session.presentationContextProvider = self
                session.prefersEphemeralWebBrowserSession = true
                if !session.start() { self.session = nil; self.pending = nil; self.attempt = nil; continuation.resume(throwing: MobileMCPError.message("Browser sign-in could not start.")) }
            }
        } onCancel: { Task { @MainActor in if self.attempt == id { self.cancel() } } }
    }
    func cancel() {
        let waiter = pending; pending = nil; attempt = nil
        session?.cancel(); session = nil
        waiter?.resume(throwing: CancellationError())
    }
    func presentationAnchor(for session: ASWebAuthenticationSession) -> ASPresentationAnchor {
        UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.flatMap(\.windows).first(where: \.isKeyWindow) ?? ASPresentationAnchor()
    }
}

nonisolated struct MCPOAuth {
    static let redirect = "llmtui-ios-mcp://oauth/callback"
    let http: any MCPHTTPFetching
    init(http: any MCPHTTPFetching = MCPHTTP()) { self.http = http }
    static func base64url(_ data: Data) -> String { data.base64EncodedString().replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "") }
    static func random() throws -> String {
        var bytes = [UInt8](repeating: 0, count: 32)
        guard SecRandomCopyBytes(kSecRandomDefault, bytes.count, &bytes) == errSecSuccess else { throw MobileMCPError.message("Secure OAuth randomness is unavailable.") }
        return base64url(Data(bytes))
    }
    static func https(_ value: String) throws -> URL {
        guard let url = URL(string: value), url.scheme == "https", url.host != nil, url.user == nil, url.password == nil, url.fragment == nil else {
            throw MobileMCPError.message("OAuth metadata and endpoints must use HTTPS without embedded credentials.")
        }
        return url
    }
    static func validateCallback(_ url: URL, state: String, issuer: String, issuerRequired: Bool) throws -> String {
        guard url.scheme == "llmtui-ios-mcp", url.host == "oauth", url.path == "/callback",
              let components = URLComponents(url: url, resolvingAgainstBaseURL: false) else { throw MobileMCPError.message("Invalid OAuth callback.") }
        let items = components.queryItems ?? []
        guard Set(items.map(\.name)).count == items.count else { throw MobileMCPError.message("Duplicate OAuth callback parameters.") }
        let values = Dictionary(uniqueKeysWithValues: items.map { ($0.name, $0.value ?? "") })
        guard values["state"] == state, values["error"] == nil,
              (!issuerRequired || values["iss"] != nil), values["iss"] == nil || values["iss"] == issuer,
              let code = values["code"], !code.isEmpty else { throw MobileMCPError.message("OAuth callback state or issuer validation failed.") }
        return code
    }
    private func json(_ url: URL, body: MCPJSON? = nil) async throws -> MCPJSON {
        var request = URLRequest(url: url)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let body { request.httpMethod = "POST"; request.setValue("application/json", forHTTPHeaderField: "Content-Type"); request.httpBody = try body.data() }
        let response = try await http.fetch(request)
        guard (200..<300).contains(response.status) else { throw MobileMCPError.message("OAuth discovery or registration failed (HTTP \(response.status)).") }
        return try MCPJSON.parse(response.data)
    }
    private func metadata(_ profile: MobileMCPProfile) async throws -> (MCPJSON, MCPJSON, String) {
        let endpoint = try profile.validatedURL()
        guard endpoint.scheme == "https" else { throw MobileMCPError.authentication }
        var probe = URLRequest(url: endpoint)
        probe.httpMethod = "POST"
        probe.setValue("application/json", forHTTPHeaderField: "Content-Type")
        probe.setValue("application/json, text/event-stream", forHTTPHeaderField: "Accept")
        probe.setValue("2026-07-28", forHTTPHeaderField: "MCP-Protocol-Version")
        probe.setValue("tools/list", forHTTPHeaderField: "Mcp-Method")
        probe.httpBody = try MCPJSON.object(["jsonrpc": .string("2.0"), "id": .string("auth-discovery"), "method": .string("tools/list"), "params": .object(["_meta": .object(["io.modelcontextprotocol/protocolVersion": .string("2026-07-28"), "io.modelcontextprotocol/clientCapabilities": .object([:])])])]).data()
        let challenge = try await http.fetch(probe)
        let header = challenge.headers["www-authenticate"] ?? ""
        var resourceURL: URL?
        if let range = header.range(of: #"resource_metadata="[^"]+""#, options: .regularExpression) {
            let fragment = String(header[range]); resourceURL = try Self.https(String(fragment.dropFirst("resource_metadata=\"".count).dropLast()))
        }
        var root = URLComponents(url: endpoint, resolvingAgainstBaseURL: false)!
        root.query = nil
        let path = root.path
        root.path = "/.well-known/oauth-protected-resource" + path
        let resource: MCPJSON
        if let resourceURL { resource = try await json(resourceURL) }
        else {
            do { resource = try await json(root.url!) }
            catch { root.path = "/.well-known/oauth-protected-resource"; resource = try await json(root.url!) }
        }
        guard let identity = resource["resource"]?.string, let resourceIdentity = try? Self.https(identity),
              resourceIdentity.host == endpoint.host, resourceIdentity.port == endpoint.port,
              endpoint.path == resourceIdentity.path || endpoint.path.hasPrefix(resourceIdentity.path.hasSuffix("/") ? resourceIdentity.path : resourceIdentity.path + "/"),
              let issuer = resource["authorization_servers"]?.array?.first?.string else { throw MobileMCPError.message("OAuth resource metadata does not identify this MCP server.") }
        let issuerURL = try Self.https(issuer)
        var authorization = URLComponents(url: issuerURL, resolvingAgainstBaseURL: false)!
        authorization.path = "/.well-known/oauth-authorization-server" + issuerURL.path
        let server: MCPJSON
        do { server = try await json(authorization.url!) }
        catch { authorization.path = issuerURL.path.trimmingCharacters(in: CharacterSet(charactersIn: "/")); authorization.path = "/" + authorization.path + (authorization.path.isEmpty ? "" : "/") + ".well-known/openid-configuration"; server = try await json(authorization.url!) }
        guard server["issuer"]?.string == issuer else { throw MobileMCPError.message("OAuth metadata issuer mismatch.") }
        let scope: String
        if let range = header.range(of: #"scope="[^"]*""#, options: .regularExpression) { scope = String(header[range].dropFirst(7).dropLast()) }
        else { scope = (resource["scopes_supported"]?.array ?? []).compactMap(\.string).joined(separator: " ") }
        return (resource, server, scope)
    }
    func authorize(_ profile: MobileMCPProfile, previousScopes: String = "", browser: @Sendable (URL) async throws -> URL) async throws -> MCPCredentials {
        let (resource, metadata, requestedScope) = try await self.metadata(profile)
        guard let auth = metadata["authorization_endpoint"]?.string, let token = metadata["token_endpoint"]?.string,
              let issuer = metadata["issuer"]?.string, let resourceID = resource["resource"]?.string else { throw MobileMCPError.message("OAuth authorization metadata is incomplete.") }
        let tokenURL = try Self.https(token)
        var clientID = profile.clientID
        if !profile.clientMetadataURL.isEmpty, metadata["client_id_metadata_document_supported"]?.bool == true {
            clientID = try Self.https(profile.clientMetadataURL).absoluteString
        }
        if clientID.isEmpty {
            guard let registration = metadata["registration_endpoint"]?.string else { throw MobileMCPError.message("Configure an OAuth client ID or a supported client metadata URL for this server.") }
            let registered = try await json(Self.https(registration), body: .object([
                "client_name": .string("LLMTUIiOS"), "application_type": .string("native"), "token_endpoint_auth_method": .string("none"),
                "redirect_uris": .array([.string(Self.redirect)]), "grant_types": .array([.string("authorization_code"), .string("refresh_token")]), "response_types": .array([.string("code")])
            ]))
            guard let id = registered["client_id"]?.string, !id.isEmpty else { throw MobileMCPError.message("OAuth client registration failed.") }
            clientID = id
        }
        guard (metadata["code_challenge_methods_supported"]?.array ?? []).contains(.string("S256")) else { throw MobileMCPError.message("OAuth server must support PKCE S256.") }
        let verifier = try Self.random(), state = try Self.random()
        let scopes = Set((previousScopes + " " + requestedScope).split(separator: " ").map(String.init)).sorted().joined(separator: " ")
        var components = URLComponents(url: try Self.https(auth), resolvingAgainstBaseURL: false)!
        let reserved = Set(["response_type", "client_id", "redirect_uri", "state", "code_challenge", "code_challenge_method", "resource", "scope"])
        guard !(components.queryItems ?? []).contains(where: { reserved.contains($0.name) }) else { throw MobileMCPError.message("OAuth endpoint contains conflicting authorization parameters.") }
        components.queryItems = (components.queryItems ?? []) + [
            URLQueryItem(name: "response_type", value: "code"), URLQueryItem(name: "client_id", value: clientID),
            URLQueryItem(name: "redirect_uri", value: Self.redirect), URLQueryItem(name: "state", value: state),
            URLQueryItem(name: "code_challenge", value: Self.base64url(Data(SHA256.hash(data: Data(verifier.utf8))))),
            URLQueryItem(name: "code_challenge_method", value: "S256"), URLQueryItem(name: "resource", value: resourceID)
        ]
        if !scopes.isEmpty { components.queryItems?.append(URLQueryItem(name: "scope", value: scopes)) }
        let callback = try await browser(components.url!)
        try Task.checkCancellation()
        let code = try Self.validateCallback(callback, state: state, issuer: issuer, issuerRequired: metadata["authorization_response_iss_parameter_supported"]?.bool == true)
        let fields = ["grant_type": "authorization_code", "code": code, "code_verifier": verifier, "client_id": clientID, "redirect_uri": Self.redirect, "resource": resourceID]
        let response = try await tokenRequest(tokenURL, fields: fields)
        return try credentials(response, issuer: issuer, resource: resourceID, tokenEndpoint: token, clientID: clientID, scopes: scopes)
    }
    func refresh(_ old: MCPCredentials, profile: MobileMCPProfile) async throws -> MCPCredentials {
        guard !old.refreshToken.isEmpty else { throw MobileMCPError.authentication }
        let (resource, metadata, _) = try await self.metadata(profile)
        guard metadata["issuer"]?.string == old.issuer, resource["resource"]?.string == old.resource,
              metadata["token_endpoint"]?.string == old.tokenEndpoint else { throw MobileMCPError.message("OAuth credential binding changed. Sign in again.") }
        let response = try await tokenRequest(Self.https(old.tokenEndpoint), fields: ["grant_type": "refresh_token", "refresh_token": old.refreshToken, "client_id": old.clientID, "resource": old.resource])
        var value = try credentials(response, issuer: old.issuer, resource: old.resource, tokenEndpoint: old.tokenEndpoint, clientID: old.clientID, scopes: old.scopes)
        if value.refreshToken.isEmpty { value.refreshToken = old.refreshToken }
        return value
    }
    private func tokenRequest(_ url: URL, fields: [String: String]) async throws -> MCPJSON {
        var components = URLComponents()
        components.queryItems = fields.sorted(by: { $0.key < $1.key }).map { URLQueryItem(name: $0.key, value: $0.value) }
        var request = URLRequest(url: url)
        request.httpMethod = "POST"; request.setValue("application/x-www-form-urlencoded", forHTTPHeaderField: "Content-Type")
        request.httpBody = Data((components.percentEncodedQuery ?? "").replacingOccurrences(of: "+", with: "%2B").utf8)
        let response = try await http.fetch(request)
        guard response.status == 200 else { throw MobileMCPError.authentication }
        return try MCPJSON.parse(response.data)
    }
    private func credentials(_ result: MCPJSON, issuer: String, resource: String, tokenEndpoint: String, clientID: String, scopes: String) throws -> MCPCredentials {
        guard let token = result["access_token"]?.string, !token.isEmpty, result["token_type"]?.string?.lowercased() == "bearer",
              !token.contains("\r"), !token.contains("\n") else { throw MobileMCPError.message("OAuth returned invalid bearer credentials.") }
        let expiration: Date?
        if let seconds = result["expires_in"]?.number { expiration = Date().addingTimeInterval(max(0, seconds - 30)) } else { expiration = nil }
        return MCPCredentials(accessToken: token, refreshToken: result["refresh_token"]?.string ?? "", expiresAt: expiration, issuer: issuer, resource: resource, tokenEndpoint: tokenEndpoint, clientID: clientID, scopes: result["scope"]?.string ?? scopes)
    }
}
