import Foundation
import Testing
@testable import LLMTUIiOS

actor MCPFakeHTTP: MCPHTTPFetching {
    enum Mode: Sendable { case modern, legacy, auth, modernError, pagination, changed }
    let mode: Mode
    var requests: [URLRequest] = []
    init(_ mode: Mode) { self.mode = mode }
    nonisolated func invalidate() {}
    func fetch(_ request: URLRequest) async throws -> MCPHTTPResponse {
        try Task.checkCancellation()
        requests.append(request)
        let message = try MCPJSON.parse(request.httpBody ?? Data())
        let id = message["id"] ?? .null
        let method = message["method"]?.string ?? ""
        let version = request.value(forHTTPHeaderField: "MCP-Protocol-Version")
        if mode == .auth { return MCPHTTPResponse(status: 401, headers: [:], data: Data()) }
        if mode == .legacy, method == "tools/list", version == "2026-07-28" {
            return MCPHTTPResponse(status: 400, headers: [:], data: Data())
        }
        if mode == .modernError {
            return MCPHTTPResponse(status: 400, headers: ["content-type": "application/json"], data: try MCPJSON.object(["jsonrpc": .string("2.0"), "id": id, "error": .object(["code": .number(-32020), "message": .string("Header mismatch")])]).data())
        }
        if method == "notifications/initialized" { return MCPHTTPResponse(status: 202, headers: [:], data: Data()) }
        let result: MCPJSON
        if method == "initialize" {
            result = .object(["protocolVersion": .string("2025-11-25"), "capabilities": .object(["tools": .object(["listChanged": .bool(true)])]), "serverInfo": .object(["name": .string("test"), "version": .string("1")])])
        } else if method == "tools/list" {
            let description = mode == .changed && requests.count > 1 ? "changed" : "lookup"
            var page: [String: MCPJSON] = ["tools": .array([Self.tool(description: description)])]
            if mode == .pagination { page["nextCursor"] = .string("same-cursor") }
            result = .object(page)
        } else if method == "tools/call" {
            result = .object(["content": .array([.object(["type": .string("text"), "text": .string("observed-value")])]), "structuredContent": .object(["count": .number(7)]), "isError": .bool(false)])
        } else { throw MobileMCPError.message("Unexpected test request.") }
        return MCPHTTPResponse(status: 200, headers: ["content-type": "application/json", "mcp-session-id": "test-session"], data: try MCPJSON.object(["jsonrpc": .string("2.0"), "id": id, "result": result]).data())
    }
    static func tool(description: String = "lookup") -> MCPJSON {
        .object(["name": .string("lookup"), "description": .string(description), "inputSchema": .object(["type": .string("object"), "properties": .object(["region": .object(["type": .string("string"), "x-mcp-header": .string("Region")])])])])
    }
    func methods() -> [String] { requests.compactMap { (try? MCPJSON.parse($0.httpBody ?? Data()))?["method"]?.string } }
}

struct MCPProtocolTests {
    @Test func excludedDefinitionsStillCountTowardDiscoveryLimit() throws {
        let definition = MCPJSON.object([
            "name": .string("credential-echo"),
            "description": .string("private-token" + String(repeating: "x", count: MobileMCPLimits.catalog)),
            "inputSchema": .object(["type": .string("object")])
        ])
        #expect(throws: MobileMCPError.self) {
            _ = try MCPCatalog.decode([definition], secret: "private-token")
        }
    }
    @Test func modernAndSDKLegacyCallReturnObservedStructuredResults() async throws {
        for mode in [MCPFakeHTTP.Mode.modern, .legacy] {
            let http = MCPFakeHTTP(mode)
            let service = MobileMCPService(factory: { url, token, modern in
                if modern { return MCPModernClient(endpoint: url, token: token, http: http) }
                return MCPLegacyClient(endpoint: url, token: token, http: http)
            })
            let profile = MobileMCPProfile()
            let info = try await service.connect(profile, token: "")
            #expect(info.version == (mode == .modern ? "2026-07-28" : "2025-11-25"))
            let tool = try #require(info.tools.first)
            let binding = MobileMCPBinding(serverID: profile.id, serverName: profile.name, generation: info.generation, tool: tool)
            let result = try await service.call(binding, arguments: .object(["region": .string("EU")]))
            let text = try result.modelText()
            #expect(text.contains("observed-value"))
            #expect(text.contains("\"count\":7"))
            let requests = await http.requests
            #expect(requests.filter { (try? MCPJSON.parse($0.httpBody ?? Data()))?["method"]?.string == "tools/call" }.count == 1)
            if mode == .modern {
                let call = try #require(requests.last)
                #expect(call.value(forHTTPHeaderField: "Mcp-Method") == "tools/call")
                #expect(call.value(forHTTPHeaderField: "Mcp-Name") == "lookup")
                #expect(call.value(forHTTPHeaderField: "Mcp-Param-Region") == "EU")
                #expect(call.value(forHTTPHeaderField: "Mcp-Session-Id") == nil)
            } else {
                #expect(await http.methods().contains("initialize"))
                #expect(requests.last?.value(forHTTPHeaderField: "Mcp-Session-Id") == "test-session")
            }
            await service.suspend()
        }
    }
    @Test func authAndModernProtocolErrorsNeverDowngrade() async throws {
        for mode in [MCPFakeHTTP.Mode.auth, .modernError] {
            let http = MCPFakeHTTP(mode)
            let client = MCPModernClient(endpoint: URL(string: "https://example.com/mcp")!, token: "", http: http)
            await #expect(throws: MobileMCPError.self) { try await client.connect() }
            #expect(await http.requests.count == 1)
        }
    }
    @Test func changedDefinitionPreventsTransmission() async throws {
        let http = MCPFakeHTTP(.changed)
        let service = MobileMCPService(factory: { url, token, _ in MCPModernClient(endpoint: url, token: token, http: http) })
        let profile = MobileMCPProfile()
        let info = try await service.connect(profile, token: "")
        let binding = MobileMCPBinding(serverID: profile.id, serverName: profile.name, generation: info.generation, tool: try #require(info.tools.first))
        await #expect(throws: MobileMCPError.self) { try await service.call(binding, arguments: .object([:])) }
        #expect(!(await http.methods()).contains("tools/call"))
    }
    @Test func cancelledTaskAndDisconnectedBindingCannotSend() async throws {
        let http = MCPFakeHTTP(.modern)
        let service = MobileMCPService(factory: { url, token, _ in MCPModernClient(endpoint: url, token: token, http: http) })
        let profile = MobileMCPProfile()
        let info = try await service.connect(profile, token: "")
        let binding = MobileMCPBinding(serverID: profile.id, serverName: profile.name, generation: info.generation, tool: try #require(info.tools.first))
        let task = Task {
            withUnsafeCurrentTask { $0?.cancel() }
            try await service.call(binding, arguments: .object([:]))
        }
        do { _ = try await task.value; Issue.record("Cancelled call was sent") } catch {}
        await service.disconnect(profile.id)
        await #expect(throws: MobileMCPError.self) { try await service.call(binding, arguments: .object([:])) }
        #expect(!(await http.methods()).contains("tools/call"))
    }
    @Test func repeatedPaginationCursorIsRejected() async throws {
        let client = MCPModernClient(endpoint: URL(string: "https://example.com/mcp")!, token: "", http: MCPFakeHTTP(.pagination))
        _ = try await client.connect()
        await #expect(throws: MobileMCPError.self) { try await client.listTools() }
    }
    @Test func SSEFramingIDsErrorsAndLimits() throws {
        let data = Data(": comment\r\ndata: {\"jsonrpc\":\"2.0\",\r\ndata: \"id\":\"r\",\"result\":{\"tools\":[]}}\r\n\r\n".utf8)
        let response = MCPHTTPResponse(status: 200, headers: ["content-type": "text/event-stream"], data: data)
        #expect(try MCPSSE.response(response, id: .string("r"))["tools"] == .array([]))
        #expect(throws: MobileMCPError.self) { try MCPSSE.response(response, id: .string("wrong")) }
        #expect(throws: MobileMCPError.self) { try MCPSSE.frames(Data(repeating: 65, count: MobileMCPLimits.message + 1)) }
        #expect(throws: MobileMCPError.self) { try MCPSSE.response(MCPHTTPResponse(status: 200, headers: ["content-type": "text/event-stream"], data: Data("data: {}".utf8)), id: .string("r")) }
    }
    @Test func schemasHeadersCollisionAndCatalogLimits() throws {
        let (tools, _) = try MCPCatalog.decode([MCPFakeHTTP.tool()])
        let tool = try #require(tools.first)
        let server = UUID()
        #expect(tool.providerName(serverID: server).count <= 64)
        #expect(tool.providerName(serverID: server) != tool.providerName(serverID: UUID()))
        #expect(throws: MobileMCPError.self) { try MCPCatalog.decode([MCPFakeHTTP.tool(), MCPFakeHTTP.tool()]) }
        #expect(throws: MobileMCPError.self) { try MCPCatalog.decode(Array(repeating: MCPFakeHTTP.tool(), count: 65)) }
        #expect(MCPHeaders.encode(" padded ") == "=?base64?IHBhZGRlZCA=?=")
        #expect(MCPHeaders.encode("line1\nline2").hasPrefix("=?base64?"))
        #expect(MCPHeaders.encode("=?base64?literal?=") != "=?base64?literal?=")
        let invalid = MCPJSON.object(["type": .string("object"), "items": .object(["x-mcp-header": .string("unsafe"), "type": .string("string")])])
        #expect(throws: MobileMCPError.self) { try MCPHeaders.bindings(invalid) }
        var request = URLRequest(url: URL(string: "https://example.com/mcp")!)
        try MCPHeaders.apply(tool: tool, arguments: .object(["region": .null]), to: &request)
        #expect(request.value(forHTTPHeaderField: "Mcp-Param-Region") == nil)
    }
    @Test func unsupportedMediaAndToolErrorAreExplicit() throws {
        let result = MobileMCPResult(content: .object(["isError": .bool(true), "content": .array([.object(["type": .string("image"), "data": .string("not-rendered")])])]))
        #expect(result.isError)
        #expect(try result.modelText().contains("Unsupported MCP content: image"))
        #expect(!(try result.modelText()).contains("not-rendered"))
        let large = MobileMCPResult(content: .object(["content": .array([.object(["type": .string("text"), "text": .string(String(repeating: "x", count: 40_000))])])]))
        #expect(try large.modelText().contains("truncated"))
        #expect(throws: MobileMCPError.self) { try MobileMCPResult(content: .object(["resultType": .string("input_required")])).modelText() }
    }
    @Test func HTTPSAndLANCredentialPolicy() throws {
        var profile = MobileMCPProfile(); profile.endpoint = "http://192.168.1.10/mcp"
        #expect(throws: MobileMCPError.self) { try profile.validatedURL() }
        profile.allowLANHTTP = true
        #expect(try profile.validatedURL().scheme == "http")
        profile.authentication = .bearer
        #expect(throws: MobileMCPError.self) { try profile.validatedURL() }
        profile.authentication = .none; profile.endpoint = "http://public.example/mcp"
        #expect(throws: MobileMCPError.self) { try profile.validatedURL() }
        profile.endpoint = "https://user:password@example.com/mcp"
        #expect(throws: MobileMCPError.self) { try profile.validatedURL() }
    }

}
