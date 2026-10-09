import Foundation
import Testing
@testable import LLMTUIiOS

/// Connection lifecycle: backgrounding keeps idle connections, an expired
/// session is replaced once before a call is sent, and a sent call is never
/// retried. Uses a scripted fake client; no network.
@MainActor @Suite(.serialized) struct MCPLifecycleTests {
    /// One fake session. `failListing` makes its next tools/list fail like an
    /// expired session; `catalog` is what it lists.
    actor FakeSession: MobileMCPClient {
        let catalog: [MobileMCPTool]
        var failListing: Bool
        let failCall: Bool
        let log: CallLog
        init(catalog: [MobileMCPTool], failListing: Bool = false, failCall: Bool = false, log: CallLog) {
            self.catalog = catalog; self.failListing = failListing; self.failCall = failCall; self.log = log
        }
        func connect() async throws -> String { await log.add("connect"); return "fake" }
        func listTools() async throws -> ([MobileMCPTool], [String]) {
            await log.add("list")
            if failListing { failListing = false; throw MobileMCPError.http(404, nil) }
            return (catalog, [])
        }
        func callTool(_ tool: MobileMCPTool, arguments: MCPJSON) async throws -> MobileMCPResult {
            await log.add("call")
            if failCall { throw URLError(.networkConnectionLost) }
            return MobileMCPResult(content: .object(["content": .array([.object(["type": .string("text"), "text": .string("ok")])])]))
        }
        func disconnect() async { await log.add("disconnect") }
        func expireSession() { failListing = true }
    }

    actor CallLog {
        private(set) var events: [String] = []
        func add(_ event: String) { events.append(event) }
        func count(_ event: String) -> Int { events.filter { $0 == event }.count }
    }

    private static let tool = MobileMCPTool(name: "lookup", description: "Look up", inputSchema: .object(["type": .string("object")]))
    private static let otherTool = MobileMCPTool(name: "lookup", description: "Changed", inputSchema: .object(["type": .string("object")]))

    /// A service whose sessions are created in order from `sessions`.
    private func service(_ sessions: [FakeSession]) -> MobileMCPService {
        let queue = SessionQueue(sessions)
        return MobileMCPService(factory: { _, _, _ in queue.next() })
    }

    final class SessionQueue: @unchecked Sendable {
        private var sessions: [FakeSession]
        private let lock = NSLock()
        init(_ sessions: [FakeSession]) { self.sessions = sessions }
        func next() -> FakeSession { lock.withLock { sessions.removeFirst() } }
    }

    @Test func anExpiredSessionIsReplacedOnceBeforeTheCallIsSent() async throws {
        let log = CallLog()
        let first = FakeSession(catalog: [Self.tool], log: log)
        let second = FakeSession(catalog: [Self.tool], log: log)
        let service = service([first, second])
        let profile = MobileMCPProfile()
        let info = try await service.connect(profile, token: "")
        await first.expireSession()

        let binding = MobileMCPBinding(serverID: profile.id, serverName: profile.name, generation: info.generation, tool: Self.tool)
        let result = try await service.call(binding, arguments: .object([:]))
        #expect(try result.modelText().contains("ok"))
        #expect(await log.count("call") == 1)
        #expect(await log.count("connect") == 2)

        // The fresh session stays: the next call needs no further reconnect.
        _ = try await service.call(binding, arguments: .object([:]))
        #expect(await log.count("connect") == 2)
        #expect(await log.count("call") == 2)
    }

    @Test func aFreshSessionWithAChangedCatalogSendsNothing() async throws {
        let log = CallLog()
        let first = FakeSession(catalog: [Self.tool], log: log)
        let second = FakeSession(catalog: [Self.otherTool], log: log)
        let service = service([first, second])
        let profile = MobileMCPProfile()
        let info = try await service.connect(profile, token: "")
        await first.expireSession()

        let binding = MobileMCPBinding(serverID: profile.id, serverName: profile.name, generation: info.generation, tool: Self.tool)
        await #expect(throws: MobileMCPError.self) { try await service.call(binding, arguments: .object([:])) }
        #expect(await log.count("call") == 0)
    }

    @Test func aSentCallIsNeverRetried() async throws {
        let log = CallLog()
        let session = FakeSession(catalog: [Self.tool], failCall: true, log: log)
        let service = service([session])
        let profile = MobileMCPProfile()
        let info = try await service.connect(profile, token: "")
        let binding = MobileMCPBinding(serverID: profile.id, serverName: profile.name, generation: info.generation, tool: Self.tool)
        await #expect(throws: (any Error).self) { try await service.call(binding, arguments: .object([:])) }
        #expect(await log.count("call") == 1)
        #expect(await log.count("connect") == 1)
        // The interrupted server now needs an explicit reconnect.
        await #expect(throws: MobileMCPError.self) { try await service.call(binding, arguments: .object([:])) }
        #expect(await log.count("call") == 1)
    }

    @Test func authenticationFailuresAreNotRetried() {
        #expect(!MobileMCPService.sessionMayHaveExpired(MobileMCPError.authentication))
        #expect(!MobileMCPService.sessionMayHaveExpired(MobileMCPError.scopeRequired("read")))
        #expect(!MobileMCPService.sessionMayHaveExpired(CancellationError()))
        #expect(MobileMCPService.sessionMayHaveExpired(MobileMCPError.http(404, nil)))
        #expect(MobileMCPService.sessionMayHaveExpired(URLError(.networkConnectionLost)))
    }

    @Test func backgroundKeepsIdleConnectionsAndSuspendClosesThem() async throws {
        let log = CallLog()
        let service = service([FakeSession(catalog: [Self.tool], log: log)])
        let defaults = try #require(UserDefaults(suiteName: "MCPLifecycleTests.\(UUID())"))
        let controller = MobileMCPController(service: service, store: MobileMCPProfileStore(defaults: defaults), credentials: MCPMemoryCredentials())
        let profile = MobileMCPProfile()
        try controller.save(profile, bearer: nil)
        controller.connect(profile.id)
        await controller.waitForConnectionForTesting(profile.id)
        #expect(controller.connections[profile.id] != nil)

        let root = FileManager.default.temporaryDirectory.appending(path: "MCPLifecycleTests-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: root) }
        let model = MobileAppModel(
            conversationStore: MobileConversationStore(directory: root.appending(path: "Conversations")),
            documentStore: DocumentStore(root: root.appending(path: "Attachments")),
            mcp: controller
        )
        model.suspendMCP()
        #expect(controller.connections[profile.id] != nil)
        #expect(controller.errors[profile.id] == nil)

        controller.suspend()
        #expect(controller.connections[profile.id] == nil)
    }
}
