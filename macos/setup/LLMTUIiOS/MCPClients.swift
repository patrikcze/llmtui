import Foundation
import MCP
import Logging

nonisolated enum MCPCatalog {
    static func validateSize(_ values: [MCPJSON]) throws {
        guard values.count <= MobileMCPLimits.tools else { throw MobileMCPError.message("MCP discovery exceeded 64 tools.") }
        var size = 0
        for value in values {
            size += try value.data().count
            guard size <= MobileMCPLimits.catalog else { throw MobileMCPError.message("MCP tool definitions exceeded 256 KiB.") }
        }
    }
    static func decode(_ values: [MCPJSON], secret: String = "") throws -> ([MobileMCPTool], [String]) {
        try validateSize(values)
        var tools: [MobileMCPTool] = [], warnings: [String] = []
        var names = Set<String>()
        for value in values {
            if value.containsSecret(secret) { warnings.append("Excluded a tool definition containing credentials."); continue }
            guard let tool = try? JSONDecoder().decode(MobileMCPTool.self, from: value.data()),
                  !tool.name.isEmpty, tool.name.utf8.count <= 256, tool.inputSchema.object != nil else {
                warnings.append("A malformed tool definition was excluded."); continue
            }
            guard names.insert(tool.name).inserted else { throw MobileMCPError.message("MCP server returned duplicate tool names.") }
            do { _ = try MCPHeaders.bindings(tool.inputSchema); tools.append(tool) }
            catch { warnings.append("Excluded \(String(tool.name.prefix(80))): invalid header annotation.") }
        }
        return (tools, warnings)
    }
}

actor MCPModernClient: MobileMCPClient {
    static let version = "2026-07-28"
    private let endpoint: URL
    private let token: String
    private let http: any MCPHTTPFetching
    private var connected = false
    private var firstPage: MCPJSON?
    init(endpoint: URL, token: String, http: any MCPHTTPFetching = MCPHTTP()) {
        self.endpoint = endpoint; self.token = token; self.http = http
    }
    func connect() async throws -> String {
        firstPage = try await rpc("tools/list", params: [:], probe: true)
        connected = true
        return Self.version
    }
    func listTools() async throws -> ([MobileMCPTool], [String]) {
        guard connected else { throw MobileMCPError.changed }
        var values: [MCPJSON] = [], cursor: String?, seen = Set<String>()
        repeat {
            let page: MCPJSON
            if let first = firstPage { page = first; firstPage = nil }
            else { page = try await rpc("tools/list", params: cursor.map { ["cursor": .string($0)] } ?? [:]) }
            guard let items = page["tools"]?.array else { throw MobileMCPError.message("MCP tools/list omitted its tools array.") }
            values += items
            try MCPCatalog.validateSize(values)
            cursor = page["nextCursor"]?.string
            if let cursor { guard seen.insert(cursor).inserted, seen.count <= 64 else { throw MobileMCPError.message("MCP pagination did not terminate.") } }
        } while cursor != nil
        return try MCPCatalog.decode(values, secret: token)
    }
    func callTool(_ tool: MobileMCPTool, arguments: MCPJSON) async throws -> MobileMCPResult {
        guard connected, arguments.object != nil else { throw MobileMCPError.changed }
        let result = try await rpc("tools/call", params: ["name": .string(tool.name), "arguments": arguments], tool: tool)
        return MobileMCPResult(content: result.redacting(token))
    }
    func disconnect() async { connected = false; firstPage = nil; http.invalidate() }
    private func rpc(_ method: String, params: [String: MCPJSON], tool: MobileMCPTool? = nil, probe: Bool = false) async throws -> MCPJSON {
        try Task.checkCancellation()
        let id = MCPJSON.string(UUID().uuidString)
        var params = params
        params["_meta"] = .object([
            "io.modelcontextprotocol/protocolVersion": .string(Self.version),
            "io.modelcontextprotocol/clientInfo": .object(["name": .string("LLMTUIiOS"), "version": .string("1.0")]),
            "io.modelcontextprotocol/clientCapabilities": .object([:])
        ])
        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        request.timeoutInterval = 60
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json, text/event-stream", forHTTPHeaderField: "Accept")
        request.setValue(Self.version, forHTTPHeaderField: "MCP-Protocol-Version")
        request.setValue(method, forHTTPHeaderField: "Mcp-Method")
        if !token.isEmpty { request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization") }
        if let tool {
            request.setValue(MCPHeaders.encode(tool.name), forHTTPHeaderField: "Mcp-Name")
            try MCPHeaders.apply(tool: tool, arguments: params["arguments"] ?? .object([:]), to: &request)
        }
        request.httpBody = try MCPJSON.object(["jsonrpc": .string("2.0"), "id": id, "method": .string(method), "params": .object(params)]).data()
        guard (request.httpBody?.count ?? 0) <= MobileMCPLimits.message else { throw MobileMCPError.message("MCP request exceeded 1 MiB.") }
        let response = try await http.fetch(request)
        if probe && response.status == 400 {
            // Recognized modern errors must never trigger an era downgrade.
            let body = try? MCPJSON.parse(response.data)
            let code = body?["error"]?["code"]?.number
            let advertisedLegacy = code == -32022 && (body?["error"]?["data"]?["supported"]?.array ?? []).contains(where: { ["2025-03-26", "2025-06-18", "2025-11-25"].contains($0.string ?? "") })
            if advertisedLegacy { throw MobileMCPError.legacyRequired }
            let legacyInitialization = code == -32000 && (body?["error"]?["message"]?.string?.lowercased().contains("initializ") == true)
            if code == nil || legacyInitialization { throw MobileMCPError.legacyRequired }
        }
        return try MCPSSE.response(response, id: id)
    }
}

/// SDK owns legacy initialization, capability checks, request correlation and
/// decoding. This transport supplies bounded, cancellable Streamable HTTP.
actor MCPLegacyTransport: Transport {
    let logger = Logger(label: "LLMTUIiOS.MCP", factory: { _ in SwiftLogNoOpLogHandler() })
    private let endpoint: URL
    private let token: String
    private let http: any MCPHTTPFetching
    private var sessionID: String?
    private var version = "2025-11-25"
    private var active = false
    private let stream: AsyncThrowingStream<Data, Error>
    private let continuation: AsyncThrowingStream<Data, Error>.Continuation
    init(endpoint: URL, token: String, http: any MCPHTTPFetching) {
        self.endpoint = endpoint; self.token = token; self.http = http
        let pair = AsyncThrowingStream<Data, Error>.makeStream()
        stream = pair.stream; continuation = pair.continuation
    }
    func connect() async throws { active = true }
    func receive() -> AsyncThrowingStream<Data, Error> { stream }
    func disconnect() async { active = false; http.invalidate(); continuation.finish() }
    func send(_ data: Data) async throws {
        guard active else { throw CancellationError() }
        try Task.checkCancellation()
        let body = try MCPJSON.parse(data)
        let method = body["method"]?.string ?? ""
        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"; request.httpBody = data
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json, text/event-stream", forHTTPHeaderField: "Accept")
        request.setValue(version, forHTTPHeaderField: "MCP-Protocol-Version")
        if let sessionID { request.setValue(sessionID, forHTTPHeaderField: "Mcp-Session-Id") }
        if !token.isEmpty { request.setValue("Bearer " + token, forHTTPHeaderField: "Authorization") }
        let response = try await http.fetch(request)
        if response.status == 401 || response.status == 403 { throw MCPSSE.authenticationError(response.headers) }
        if body["id"] == nil {
            guard response.status == 202 else { throw MobileMCPError.http(response.status, nil) }
            return
        }
        let id = body["id"] ?? .null
        let result = try MCPSSE.response(response, id: id)
        if method == "initialize" {
            if let selected = result["protocolVersion"]?.string { version = selected }
            sessionID = response.headers["mcp-session-id"]
        }
        // Return only the validated correlated result; unsupported independent
        // server requests have already failed rather than acquiring authority.
        continuation.yield(try MCPJSON.object(["jsonrpc": .string("2.0"), "id": id, "result": result]).data())
    }
}

actor MCPLegacyClient: MobileMCPClient {
    private let client = Client(name: "LLMTUIiOS", version: "1.0", configuration: .strict)
    private let transport: MCPLegacyTransport
    private let token: String
    init(endpoint: URL, token: String, http: any MCPHTTPFetching = MCPHTTP()) {
        self.token = token
        transport = MCPLegacyTransport(endpoint: endpoint, token: token, http: http)
    }
    func connect() async throws -> String {
        try Task.checkCancellation()
        return try await withTaskCancellationHandler {
            let result = try await client.connect(transport: transport)
            guard ["2025-03-26", "2025-06-18", "2025-11-25"].contains(result.protocolVersion) else {
                throw MobileMCPError.message("Unsupported MCP protocol version.")
            }
            return result.protocolVersion
        } onCancel: { Task { await self.disconnect() } }
    }
    func listTools() async throws -> ([MobileMCPTool], [String]) {
        try await withTaskCancellationHandler {
            var values: [MCPJSON] = [], cursor: String?, seen = Set<String>()
            repeat {
                try Task.checkCancellation()
                let page = try await client.listTools(cursor: cursor)
                values += try page.tools.map { try MCPJSON.parse(JSONEncoder().encode($0)) }
                try MCPCatalog.validateSize(values)
                cursor = page.nextCursor
                if let cursor { guard seen.insert(cursor).inserted, seen.count <= 64 else { throw MobileMCPError.message("MCP pagination did not terminate.") } }
            } while cursor != nil
            return try MCPCatalog.decode(values, secret: token)
        } onCancel: { Task { await self.disconnect() } }
    }
    func callTool(_ tool: MobileMCPTool, arguments: MCPJSON) async throws -> MobileMCPResult {
        try Task.checkCancellation()
        guard arguments.object != nil else { throw MobileMCPError.message("MCP arguments must be an object.") }
        let args = try JSONDecoder().decode([String: MCP.Value].self, from: arguments.data())
        return try await withTaskCancellationHandler {
            let context: RequestContext<CallTool.Result> = try await client.callTool(name: tool.name, arguments: args)
            let result = try await context.value
            return MobileMCPResult(content: try MCPJSON.parse(JSONEncoder().encode(result)).redacting(token))
        } onCancel: { Task { await self.disconnect() } }
    }
    func disconnect() async { await transport.disconnect(); await client.disconnect() }
}

actor MobileMCPService {
    typealias Factory = @Sendable (URL, String, Bool) -> any MobileMCPClient
    private let factory: Factory
    private var connections: [UUID: (MobileMCPProfile, any MobileMCPClient, MobileMCPConnection)] = [:]
    private var epochs: [UUID: UUID] = [:]
    init(factory: @escaping Factory = { url, token, modern in
        if modern { return MCPModernClient(endpoint: url, token: token) }
        return MCPLegacyClient(endpoint: url, token: token)
    }) { self.factory = factory }
    func connect(_ profile: MobileMCPProfile, token: String) async throws -> MobileMCPConnection {
        await disconnect(profile.id)
        let epoch = UUID(); epochs[profile.id] = epoch
        let url = try profile.validatedURL()
        guard token.isEmpty || url.scheme == "https" else { throw MobileMCPError.authentication }
        var client = factory(url, token, true)
        do {
            let version: String
            do { version = try await client.connect() }
            catch MobileMCPError.legacyRequired {
                await client.disconnect()
                try Task.checkCancellation()
                guard epochs[profile.id] == epoch else { throw MobileMCPError.changed }
                client = factory(url, token, false)
                version = try await client.connect()
            }
            let (tools, warnings) = try await client.listTools()
            try Task.checkCancellation()
            guard epochs[profile.id] == epoch else { throw MobileMCPError.changed }
            let info = MobileMCPConnection(generation: epoch, version: version, tools: tools, warnings: warnings)
            connections[profile.id] = (profile, client, info)
            return info
        } catch { await client.disconnect(); throw error }
    }
    func disconnect(_ id: UUID) async {
        epochs[id] = UUID()
        let old = connections.removeValue(forKey: id)
        await old?.1.disconnect()
    }
    func suspend() async { for id in Array(epochs.keys) { await disconnect(id) } }
    func call(_ binding: MobileMCPBinding, arguments: MCPJSON) async throws -> MobileMCPResult {
        try Task.checkCancellation()
        guard let (profile, client, info) = connections[binding.serverID], info.generation == binding.generation,
              info.tools.contains(binding.tool) else { throw MobileMCPError.changed }
        // Refresh before transmission: changed schemas never inherit an approval.
        let (current, _) = try await client.listTools()
        try Task.checkCancellation()
        guard connections[profile.id]?.2.generation == info.generation,
              current.first(where: { $0.name == binding.tool.name }) == binding.tool else { throw MobileMCPError.changed }
        do { return try await client.callTool(binding.tool, arguments: arguments) }
        catch { await disconnect(profile.id); throw error }
    }
}
