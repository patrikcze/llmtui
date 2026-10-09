import Foundation
import Testing
@testable import LLMTUIiOS

@MainActor @Suite(.serialized) struct MCPChatIntegrationTests {
    private func configured(_ mode: MCPFakeHTTP.Mode, runtime: MobileChatRuntime? = nil) async throws -> (MobileAppModel, MCPFakeHTTP, MobileMCPProfile, MobileMCPBinding, UUID) {
        let http = MCPFakeHTTP(mode)
        let service = MobileMCPService(factory: { url, token, modern in
            if modern { return MCPModernClient(endpoint: url, token: token, http: http) }
            return MCPLegacyClient(endpoint: url, token: token, http: http)
        })
        let defaults = try #require(UserDefaults(suiteName: "MCPChatTests.\(UUID())"))
        let controller = MobileMCPController(service: service, store: MobileMCPProfileStore(defaults: defaults), credentials: MCPMemoryCredentials())
        let server = MobileMCPProfile()
        try controller.save(server, bearer: nil)
        controller.connect(server.id)
        await controller.waitForConnectionForTesting(server.id)
        let binding = try #require(controller.bindings(for: [server.id]).values.first)
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let model = MobileAppModel(conversationStore: MobileConversationStore(directory: root), mcp: controller, runtime: runtime)
        let provider = MobileProviderProfile(model: "test-model")
        model.profiles = [provider]; model.activeProfileID = provider.id
        let id = model.newConversation()
        model.setMCPServer(server.id, selected: true)
        let assistant = MobileChatMessage(role: .assistant, text: "")
        let index = try #require(model.conversations.firstIndex(where: { $0.id == id }))
        model.conversations[index].messages = [assistant]
        model.prepareMCPForTesting(bindings: [binding.providerName: binding], conversationID: id, profile: provider)
        return (model, http, server, binding, assistant.id)
    }
    private func awaitApproval(_ model: MobileAppModel) async throws {
        for _ in 0..<10_000 {
            if model.pendingToolApproval != nil { return }
            await Task.yield()
        }
        throw MobileMCPError.message("Test did not observe an approval.")
    }
    @Test func bothProtocolsRequireApprovalBeforeObservedCall() async throws {
        for mode in [MCPFakeHTTP.Mode.modern, .legacy] {
            let (model, http, _, binding, assistant) = try await configured(mode)
            let call = ToolCall(id: "call", name: binding.providerName, arguments: "{}")
            let task = Task { try await model.executeMCP(call, arguments: [:], assistantID: assistant) }
            try await awaitApproval(model)
            #expect(!(await http.methods()).contains("tools/call"))
            #expect(model.pendingToolApproval?.summary.contains("Server:") == true)
            model.resolveApproval(true)
            let output = try await task.value
            #expect(output.contains("observed-value"))
            #expect((await http.methods()).filter { $0 == "tools/call" }.count == 1)
            #expect(model.messages.first?.toolActivities.last?.status == .completed)
            model.mcp.suspend()
        }
    }
    @Test func rejectionDeselectAndCancellationNeverTransmit() async throws {
        for scenario in ["reject", "deselect", "cancel"] {
            let (model, http, server, binding, assistant) = try await configured(.modern)
            let task = Task { try await model.executeMCP(ToolCall(id: "call", name: binding.providerName, arguments: "{}"), arguments: [:], assistantID: assistant) }
            try await awaitApproval(model)
            if scenario == "deselect" { model.setMCPServer(server.id, selected: false); model.resolveApproval(true) }
            else if scenario == "cancel" { task.cancel(); model.stop() }
            else { model.resolveApproval(false) }
            do { _ = try await task.value } catch { #expect(scenario == "cancel") }
            #expect(!(await http.methods()).contains("tools/call"))
            model.mcp.suspend()
        }
    }
    @Test func unknownToolAndProfileEditCannotInheritApproval() async throws {
        let (model, http, server, binding, assistant) = try await configured(.modern)
        #expect(try await model.executeMCP(ToolCall(id: "no", name: "mcp_unknown", arguments: "{}"), arguments: [:], assistantID: assistant).contains("not offered"))
        let task = Task { try await model.executeMCP(ToolCall(id: "call", name: binding.providerName, arguments: "{}"), arguments: [:], assistantID: assistant) }
        try await awaitApproval(model)
        var edited = server; edited.name = "Changed"
        try model.mcp.save(edited, bearer: nil)
        model.resolveApproval(true)
        #expect(try await task.value.contains("changed"))
        #expect(!(await http.methods()).contains("tools/call"))
    }
    @Test func MCPResultsForceOutboundApprovalAndExtendContextEstimate() async throws {
        let (model, _, _, binding, assistant) = try await configured(.modern)
        let key = MobileToolApprovalMode.storageKey
        let old = UserDefaults.standard.object(forKey: key)
        defer { UserDefaults.standard.set(old, forKey: key) }
        UserDefaults.standard.set(MobileToolApprovalMode.never.rawValue, forKey: key)
        #expect(!model.externalApprovalRequiredForTesting("web_fetch"))
        let task = Task { try await model.executeMCP(ToolCall(id: "call", name: binding.providerName, arguments: "{}"), arguments: [:], assistantID: assistant) }
        try await awaitApproval(model)
        model.resolveApproval(true)
        _ = try await task.value
        #expect(model.externalApprovalRequiredForTesting("web_fetch"))
        #expect(model.externalApprovalRequiredForTesting("memory_remember"))
        let definition = binding.tool.providerDefinition(serverID: binding.serverID, serverName: binding.serverName)
        #expect(ContextUsageEstimate.overhead(toolsEnabled: true, memoryEnabled: true, mcpDefinitions: [definition]) > ContextUsageEstimate.overhead(toolsEnabled: true, memoryEnabled: true))
        #expect(try model.mcp.bindings(for: []).isEmpty)
        model.mcp.suspend()
    }
    @Test func liveToolLoopSendsCorrelatedResultBackToProvider() async throws {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [MCPStreamURLProtocol.self]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        let (model, http, _, binding, _) = try await configured(.modern, runtime: MobileChatRuntime(session: session))
        let call = #"data: {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"mcp-call","function":{"name":"NAME","arguments":"{}"}}]},"finish_reason":"tool_calls"}]}"#.replacingOccurrences(of: "NAME", with: binding.providerName) + "\n\ndata: [DONE]\n\n"
        let answer = #"data: {"choices":[{"delta":{"content":"Observed the MCP result."},"finish_reason":"stop"}]}"# + "\n\ndata: [DONE]\n\n"
        MCPWireFixture.shared.set([call, answer])
        model.draft = "Use the external lookup tool."
        model.send()
        try await awaitApproval(model)
        #expect(!(await http.methods()).contains("tools/call"))
        model.resolveApproval(true)
        for _ in 0..<20_000 { if !model.isGenerating { break }; await Task.yield() }
        #expect(!model.isGenerating)
        #expect((await http.methods()).filter { $0 == "tools/call" }.count == 1)
        #expect(model.messages.last?.text.contains("Observed the MCP result.") == true)
        let requests = MCPWireFixture.shared.requests()
        #expect(requests.count == 2)
        let second = try #require(requests.last)
        var data = second.httpBody ?? Data()
        if data.isEmpty, let stream = second.httpBodyStream {
            stream.open(); defer { stream.close() }
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable { let count = stream.read(&buffer, maxLength: buffer.count); if count <= 0 { break }; data.append(contentsOf: buffer.prefix(count)) }
        }
        let body = try MCPJSON.parse(data)
        let result = try #require(body["messages"]?.array?.first(where: { $0["role"]?.string == "tool" }))
        #expect(result["tool_call_id"]?.string == "mcp-call")
        #expect(result["content"]?.string?.contains("observed-value") == true)
        model.mcp.suspend()
    }
    @Test func callCountLimitPreventsSeventeenthTransmission() async throws {
        let (model, http, _, binding, assistant) = try await configured(.modern)
        for index in 0..<17 {
            let task = Task { try await model.executeMCP(ToolCall(id: "call-\(index)", name: binding.providerName, arguments: "{}"), arguments: [:], assistantID: assistant) }
            if index < 16 { try await awaitApproval(model); model.resolveApproval(true) }
            let output = try await task.value
            if index == 16 { #expect(output.contains("16-call")) }
        }
        #expect((await http.methods()).filter { $0 == "tools/call" }.count == 16)
        model.mcp.suspend()
    }

}
