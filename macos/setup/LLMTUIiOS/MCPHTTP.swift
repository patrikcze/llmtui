import Foundation

/// No cookies, disk cache, or cross-origin redirect credential forwarding.
nonisolated final class MCPRedirectGuard: NSObject, URLSessionTaskDelegate, @unchecked Sendable {
    func urlSession(_ session: URLSession, task: URLSessionTask,
                    willPerformHTTPRedirection response: HTTPURLResponse, newRequest request: URLRequest,
                    completionHandler: @escaping (URLRequest?) -> Void) {
        // Explicit endpoints simplify both authentication binding and LAN policy.
        completionHandler(nil)
    }
}
nonisolated struct MCPHTTPResponse: Sendable { let status: Int; let headers: [String: String]; let data: Data }
nonisolated protocol MCPHTTPFetching: Sendable {
    func fetch(_ request: URLRequest) async throws -> MCPHTTPResponse
    func invalidate()
}
nonisolated final class MCPHTTP: MCPHTTPFetching, @unchecked Sendable {
    private let session: URLSession
    init(configuration: URLSessionConfiguration = .ephemeral) {
        configuration.httpCookieStorage = nil
        configuration.urlCache = nil
        configuration.timeoutIntervalForRequest = 60
        configuration.timeoutIntervalForResource = 120
        session = URLSession(configuration: configuration, delegate: MCPRedirectGuard(), delegateQueue: nil)
    }
    func invalidate() { session.invalidateAndCancel() }
    func fetch(_ request: URLRequest) async throws -> MCPHTTPResponse {
        try Task.checkCancellation()
        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else { throw MobileMCPError.message("Invalid MCP HTTP response.") }
        var headers: [String: String] = [:]
        for (key, value) in http.allHeaderFields { headers[String(describing: key).lowercased()] = String(describing: value) }
        var data = Data()
        for try await byte in bytes {
            try Task.checkCancellation()
            guard data.count < MobileMCPLimits.message else { bytes.task.cancel(); throw MobileMCPError.message("MCP message exceeded 1 MiB.") }
            data.append(byte)
            // A final response terminates a request-scoped SSE stream even if
            // the server forgets to close the socket. No GET/resumption/replay.
            if byte == 10, headers["content-type"]?.contains("text/event-stream") == true,
               data.suffix(2) == Data([10, 10]) || data.suffix(4) == Data([13, 10, 13, 10]) {
                if let frames = try? MCPSSE.frames(data), frames.contains(where: { $0["id"] != nil && ($0["result"] != nil || $0["error"] != nil) }) {
                    bytes.task.cancel()
                    break
                }
            }
        }
        return MCPHTTPResponse(status: http.statusCode, headers: headers, data: data)
    }
}

nonisolated enum MCPSSE {
    static func frames(_ data: Data) throws -> [MCPJSON] {
        guard data.count <= MobileMCPLimits.message, let text = String(data: data, encoding: .utf8) else {
            throw MobileMCPError.message("Invalid or oversized MCP SSE response.")
        }
        let normalized = text.replacingOccurrences(of: "\r\n", with: "\n").replacingOccurrences(of: "\r", with: "\n")
        var result: [MCPJSON] = []
        for event in normalized.components(separatedBy: "\n\n").dropLast() {
            let payload = event.components(separatedBy: "\n").compactMap { line -> String? in
                guard line.hasPrefix("data:") else { return nil }
                let tail = String(line.dropFirst(5))
                return tail.hasPrefix(" ") ? String(tail.dropFirst()) : tail
            }.joined(separator: "\n")
            if !payload.isEmpty { result.append(try MCPJSON.parse(Data(payload.utf8))) }
        }
        return result
    }
    static func authenticationError(_ headers: [String: String]) -> MobileMCPError {
        let value = headers["www-authenticate"] ?? ""
        if let range = value.range(of: #"scope="[^"]*""#, options: .regularExpression) {
            return .scopeRequired(String(value[range].dropFirst(7).dropLast()))
        }
        return .authentication
    }
    static func response(_ http: MCPHTTPResponse, id: MCPJSON) throws -> MCPJSON {
        guard http.data.count <= MobileMCPLimits.message else { throw MobileMCPError.message("MCP message exceeded 1 MiB.") }
        if http.status == 401 || http.status == 403 { throw authenticationError(http.headers) }
        let frames: [MCPJSON]
        if http.headers["content-type"]?.contains("text/event-stream") == true { frames = try self.frames(http.data) }
        else if http.headers["content-type"]?.contains("application/json") == true { frames = [try MCPJSON.parse(http.data)] }
        else { throw MobileMCPError.http(http.status, nil) }
        for frame in frames {
            guard frame["jsonrpc"]?.string == "2.0" else { throw MobileMCPError.message("Invalid MCP JSON-RPC envelope.") }
            if frame["method"] != nil {
                guard frame["id"] == nil else { throw MobileMCPError.message("Unsupported MCP server-initiated request.") }
                continue
            }
            guard frame["id"] == id else { throw MobileMCPError.message("MCP response ID did not match the request.") }
            if let error = frame["error"] {
                let raw: Double
                raw = error["code"]?.number ?? -32603
                guard let code = Int(exactly: raw) else { throw MobileMCPError.message("Invalid MCP error code.") }
                throw MobileMCPError.rpc(code, error["data"])
            }
            guard (200..<300).contains(http.status), let result = frame["result"], result.object != nil else {
                throw MobileMCPError.http(http.status, nil)
            }
            return result
        }
        throw MobileMCPError.message("MCP response ended without a correlated result.")
    }
}

nonisolated struct MCPHeaderBinding: Sendable { let path: [String]; let header: String; let type: String }
nonisolated enum MCPHeaders {
    static func bindings(_ schema: MCPJSON) throws -> [MCPHeaderBinding] {
        var found: [MCPHeaderBinding] = []
        func visit(_ node: MCPJSON, path: [String], reachable: Bool) throws {
            if let raw = node["x-mcp-header"] {
                guard reachable, !path.isEmpty, let name = raw.string, !name.isEmpty,
                      name.utf8.allSatisfy({ "!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ".utf8.contains($0) }),
                      let type = node["type"]?.string, ["string", "integer", "boolean"].contains(type),
                      !found.contains(where: { $0.header.lowercased() == name.lowercased() }) else {
                    throw MobileMCPError.message("Invalid x-mcp-header annotation.")
                }
                found.append(MCPHeaderBinding(path: path, header: name, type: type))
            }
            if let object = node.object {
                for (key, child) in object where key != "x-mcp-header" {
                    if key == "properties", let properties = child.object {
                        for (name, value) in properties { try visit(value, path: path + [name], reachable: reachable) }
                    } else if child.object != nil || child.array != nil { try visit(child, path: path, reachable: false) }
                }
            } else if let array = node.array { for child in array { try visit(child, path: path, reachable: false) } }
        }
        try visit(schema, path: [], reachable: true)
        return found
    }
    static func encode(_ value: String) -> String {
        if value.utf8.allSatisfy({ (32...126).contains($0) }), value == value.trimmingCharacters(in: .whitespacesAndNewlines), !(value.hasPrefix("=?base64?") && value.hasSuffix("?=")) { return value }
        return "=?base64?" + Data(value.utf8).base64EncodedString() + "?="
    }
    static func apply(tool: MobileMCPTool, arguments: MCPJSON, to request: inout URLRequest) throws {
        for binding in try bindings(tool.inputSchema) {
            var value: MCPJSON? = arguments
            for key in binding.path { value = value?[key] }
            guard let value, value != .null else { continue }
            let text: String
            switch (binding.type, value) {
            case ("string", .string(let v)): text = v
            case ("boolean", .bool(let v)): text = v ? "true" : "false"
            case ("integer", .integer(let v)) where abs(Double(v)) <= 9_007_199_254_740_991: text = String(v)
            case ("integer", .number(let v)) where v.rounded() == v && abs(v) <= 9_007_199_254_740_991: text = String(format: "%.0f", v)
            default: throw MobileMCPError.message("A mirrored MCP argument has an invalid type or integer range.")
            }
            request.setValue(encode(text), forHTTPHeaderField: "Mcp-Param-" + binding.header)
        }
    }
}
