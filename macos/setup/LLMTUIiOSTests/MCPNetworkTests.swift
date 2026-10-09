import Foundation
import Network
import Testing
@testable import LLMTUIiOS

/// A real loopback HTTP server written in Swift. The production URLSession
/// transport, SDK handshake and approval path run against actual sockets.
nonisolated final class MCPNetworkFixture: @unchecked Sendable {
    private let legacy: Bool
    private let queue = DispatchQueue(label: "MCPNetworkFixture")
    private let lock = NSLock()
    private var listener: NWListener?
    private var toolCalls = 0
    init(legacy: Bool) { self.legacy = legacy }
    var calls: Int { lock.lock(); defer { lock.unlock() }; return toolCalls }
    func start() async throws -> UInt16 {
        let parameters = NWParameters.tcp
        parameters.requiredLocalEndpoint = .hostPort(host: .ipv4(.loopback), port: .any)
        let listener = try NWListener(using: parameters)
        self.listener = listener
        listener.newConnectionHandler = { [weak self] connection in
            connection.start(queue: self?.queue ?? .global())
            self?.receive(connection, buffer: Data())
        }
        return try await withCheckedThrowingContinuation { continuation in
            listener.stateUpdateHandler = { state in
                switch state {
                case .ready:
                    listener.stateUpdateHandler = nil
                    if let port = listener.port { continuation.resume(returning: port.rawValue) }
                    else { continuation.resume(throwing: MobileMCPError.message("Fixture port unavailable.")) }
                case .failed(let error): listener.stateUpdateHandler = nil; continuation.resume(throwing: error)
                default: break
                }
            }
            listener.start(queue: queue)
        }
    }
    func stop() { listener?.cancel() }
    private func receive(_ connection: NWConnection, buffer: Data) {
        connection.receive(minimumIncompleteLength: 1, maximumLength: MobileMCPLimits.message) { [weak self] data, _, done, error in
            guard let self, error == nil else { connection.cancel(); return }
            var buffer = buffer; buffer.append(data ?? Data())
            guard buffer.count <= MobileMCPLimits.message else { connection.cancel(); return }
            if let split = buffer.range(of: Data("\r\n\r\n".utf8)) {
                let headers = String(decoding: buffer[..<split.lowerBound], as: UTF8.self)
                let size = headers.components(separatedBy: "\r\n").first(where: { $0.lowercased().hasPrefix("content-length:") }).flatMap { Int($0.split(separator: ":", maxSplits: 1).last?.trimmingCharacters(in: .whitespaces) ?? "") } ?? 0
                let body = Data(buffer[split.upperBound...])
                if body.count >= size {
                    do { try self.respond(connection, headers: headers, body: Data(body.prefix(size))) }
                    catch { connection.cancel() }
                    return
                }
            }
            if done { connection.cancel() } else { self.receive(connection, buffer: buffer) }
        }
    }
    private func respond(_ connection: NWConnection, headers: String, body: Data) throws {
        let request = try MCPJSON.parse(body)
        let id = request["id"] ?? .null, method = request["method"]?.string ?? ""
        var status = 200, result = MCPJSON.object([:]), contentType = "application/json"
        let response: Data
        if legacy && headers.contains("2026-07-28") {
            status = 400
            response = try MCPJSON.object(["jsonrpc": .string("2.0"), "id": id, "error": .object(["code": .number(-32022), "message": .string("Unsupported protocol version"), "data": .object(["supported": .array([.string("2025-11-25")])])])]).data()
        } else if method == "notifications/initialized" {
            status = 202; response = Data()
        } else {
            switch method {
            case "initialize": result = .object(["protocolVersion": .string("2025-11-25"), "capabilities": .object(["tools": .object([:])]), "serverInfo": .object(["name": .string("socket-fixture"), "version": .string("1")])])
            case "tools/list": result = .object(["tools": .array([MCPFakeHTTP.tool()])])
            case "tools/call":
                lock.lock(); toolCalls += 1; lock.unlock()
                result = .object(["content": .array([.object(["type": .string("text"), "text": .string("socket-observed-result")])]), "structuredContent": .object(["source": .string("real-http")])])
            default: throw MobileMCPError.message("Unexpected fixture method.")
            }
            let encoded = try MCPJSON.object(["jsonrpc": .string("2.0"), "id": id, "result": result]).data()
            if method == "tools/call" {
                contentType = "text/event-stream"
                response = Data((": keep-alive\r\ndata: " + String(decoding: encoded, as: UTF8.self) + "\r\n\r\n").utf8)
            } else { response = encoded }
        }
        let session = legacy ? "Mcp-Session-Id: socket-session\r\n" : ""
        let head = "HTTP/1.1 \(status) \(status == 200 ? "OK" : status == 202 ? "Accepted" : "Bad Request")\r\nContent-Type: \(contentType)\r\nContent-Length: \(response.count)\r\n\(session)Connection: close\r\n\r\n"
        connection.send(content: Data(head.utf8) + response, completion: .contentProcessed { _ in connection.cancel() })
    }
}

@MainActor @Suite(.serialized) struct MCPNetworkTests {
    @Test func approvedToolsUseRealHTTPForBothEras() async throws {
        for legacy in [false, true] {
            let server = MCPNetworkFixture(legacy: legacy)
            let port = try await server.start()
            defer { server.stop() }
            let defaults = try #require(UserDefaults(suiteName: "MCPNetworkTests.\(UUID())"))
            let controller = MobileMCPController(store: MobileMCPProfileStore(defaults: defaults), credentials: MCPMemoryCredentials())
            var profile = MobileMCPProfile(); profile.name = "Socket fixture"; profile.endpoint = "http://127.0.0.1:\(port)/mcp"; profile.allowLANHTTP = true
            try controller.save(profile, bearer: nil)
            controller.connect(profile.id)
            await controller.waitForConnectionForTesting(profile.id)
            let info = try #require(controller.connections[profile.id], Comment(rawValue: controller.errors[profile.id] ?? "No connection"))
            #expect(info.version == (legacy ? "2025-11-25" : "2026-07-28"))
            let binding = try #require(controller.bindings(for: [profile.id]).values.first)
            let model = MobileAppModel(conversationStore: MobileConversationStore(directory: FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)), mcp: controller)
            let provider = MobileProviderProfile(model: "fixture")
            model.profiles = [provider]; model.activeProfileID = provider.id
            let chat = model.newConversation(); model.setMCPServer(profile.id, selected: true)
            let assistant = MobileChatMessage(role: .assistant, text: "")
            model.conversations[0].messages = [assistant]
            model.prepareMCPForTesting(bindings: [binding.providerName: binding], conversationID: chat, profile: provider)
            let task = Task { try await model.executeMCP(ToolCall(id: "socket-call", name: binding.providerName, arguments: "{}"), arguments: [:], assistantID: assistant.id) }
            for _ in 0..<10_000 { if model.pendingToolApproval != nil { break }; await Task.yield() }
            #expect(model.pendingToolApproval != nil)
            #expect(server.calls == 0)
            model.resolveApproval(true)
            let output = try await task.value
            #expect(server.calls == 1)
            #expect(output.contains("socket-observed-result"))
            #expect(output.contains("real-http"))
            controller.suspend()
        }
    }
}
