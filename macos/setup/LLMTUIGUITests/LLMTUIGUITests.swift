import Foundation
import Testing
@testable import LLMTUIGUI

@MainActor
struct LLMTUIGUITests {
    @Test func defaultConfigurationHasSafeToolingDefaults() {
        let configuration = LLMTUIConfiguration()

        #expect(configuration.toolsEnabled == false)
        #expect(configuration.approvalPolicy == .ask)
        #expect(configuration.agent.enabled == false)
        #expect(configuration.entities.enabled == true)
    }

    @Test func nativeChatAgentDoesNotChangeGoConfiguration() {
        let model = AppModel(nativeAgentEnabled: true)
        model.configuration.agent.enabled = false

        let agentRequest = model.chatRuntimeOptions(usesNativeAgent: true)
        #expect(agentRequest.agentEnabled)
        #expect(model.configuration.agent.enabled == false)

        model.configuration.agent.enabled = true
        let ordinaryChatRequest = model.chatRuntimeOptions(usesNativeAgent: false)
        #expect(!ordinaryChatRequest.agentEnabled)
        #expect(model.configuration.agent.enabled)
    }

    @Test func nativeAgentBudgetUsesIndependentPolicy() {
        let policy = NativeChatRuntimePolicy.standard
        let budget = AgentExecutionBudget(policy: policy)

        #expect(budget.maximumCycles == 8)
        #expect(budget.maximumToolCalls == 32)
        #expect(budget.maximumRepeatedFailures == 3)
        #expect(budget.maximumElapsedSeconds == 1800)
    }

    @Test func llmtuiGuideIsScopedToHelpRequests() {
        #expect(LLMTUIDocumentationContext.helpContext(for: "Configure LLMTUI tools").contains("Assistant role and boundaries"))
        #expect(LLMTUIDocumentationContext.helpContext(for: "Research current Swift releases").isEmpty)
    }

    @Test func webRetryClassificationSeparatesTransientFailures() {
        #expect(WebToolRuntime.isTransient(URLError(.timedOut)))
        #expect(WebToolRuntime.isTransient(URLError(.secureConnectionFailed)))
        #expect(WebToolRuntime.isTransient(WebToolRuntime.HTTPError.status(429)))
        #expect(WebToolRuntime.isTransient(WebToolRuntime.HTTPError.status(503)))
        #expect(!WebToolRuntime.isTransient(WebToolRuntime.HTTPError.status(404)))
        #expect(!WebToolRuntime.isTransient(ToolRuntimeError.unsupportedURL))
    }

    @Test func agentBudgetUsesConfiguredLimits() {
        var configuration = AgentConfiguration()
        configuration.maxCycles = 2
        configuration.maxToolCalls = 1
        configuration.maxTokens = 10
        configuration.maxElapsed = "90s"
        configuration.maxRepeatedFailures = 2
        var budget = AgentExecutionBudget(configuration: configuration)

        let beganFirstCycle = budget.beginCycle()
        let beganSecondCycle = budget.beginCycle()
        let beganThirdCycle = budget.beginCycle()
        let acceptedFirstToolCall = budget.recordToolCall()
        let acceptedSecondToolCall = budget.recordToolCall()

        #expect(beganFirstCycle)
        #expect(beganSecondCycle)
        #expect(!beganThirdCycle)
        #expect(acceptedFirstToolCall)
        #expect(!acceptedSecondToolCall)
        #expect(budget.maximumElapsedSeconds == 90)
        #expect(budget.stopReason(elapsed: 0) == "maximum tool-call count reached")
    }

    @Test func agentBudgetParsesHumanDurations() {
        #expect(AgentExecutionBudget.duration(from: "30s") == 30)
        #expect(AgentExecutionBudget.duration(from: "5m") == 300)
        #expect(AgentExecutionBudget.duration(from: "2h") == 7200)
        #expect(AgentExecutionBudget.duration(from: "1d") == 86400)
        #expect(AgentExecutionBudget.duration(from: "invalid") == nil)
    }

    @Test func agentBudgetTracksFailuresAndEstimatedTokens() {
        var configuration = AgentConfiguration()
        configuration.maxToolCalls = 10
        configuration.maxTokens = 4
        configuration.maxRepeatedFailures = 2
        var budget = AgentExecutionBudget(configuration: configuration)
        let failure = ToolResult(toolCallID: "call", toolName: "test", content: "12345678", isError: true)

        budget.recordToolResult(failure)
        #expect(budget.repeatedFailures == 1)
        budget.recordToolResult(failure)
        #expect(budget.stopReason(elapsed: 0) != nil)
    }

    @Test func chatMessagesHaveStableIdentityAndChronology() {
        let first = ChatMessage(role: .user, text: "first")
        let second = ChatMessage(role: .assistant, text: "second")

        #expect(first.id != second.id)
        #expect(first.createdAt <= second.createdAt)
    }

    @Test func askUserRemainsAvailableWhenWorkspaceToolsAreDisabled() {
        let registry = ToolRegistry.shared

        #expect(registry.chatDefinitions(includeWorkspaceTools: false).map(\.function.name) == ["ask_user"])
        #expect(registry.chatDefinitions(includeWorkspaceTools: true).contains { $0.function.name == "ask_user" })
        #expect(registry.askUserDefinition.safety == .interaction)
    }

    @Test func userQuestionParsesAndBoundsModelArguments() throws {
        let arguments = """
        {
          "question": " Which environment should I deploy to? ",
          "options": ["Development", "Staging", "Staging", "Production", "Another"],
          "allow_free_text": true,
          "placeholder": "Enter an environment"
        }
        """
        let request = ToolRequest(
            providerToolCallID: "question-1",
            name: "ask_user",
            arguments: arguments,
            safetyClass: .interaction
        )

        let question = try UserQuestion(request: request)

        #expect(question.question == "Which environment should I deploy to?")
        #expect(question.options == ["Development", "Staging", "Production", "Another"])
        #expect(question.allowsFreeText)
        #expect(question.placeholder == "Enter an environment")
    }

    @Test func userQuestionDefaultsToFreeTextWithoutOptions() throws {
        let request = ToolRequest(
            providerToolCallID: "question-2",
            name: "ask_user",
            arguments: #"{"question":"What filename should I use?","allow_free_text":false}"#,
            safetyClass: .interaction
        )

        let question = try UserQuestion(request: request)

        #expect(question.options.isEmpty)
        #expect(question.allowsFreeText)
    }

    @Test func userQuestionResponseProducesStructuredToolResult() throws {
        let answeredData = try #require(UserQuestionResponse.answered("Staging").toolContent.data(using: .utf8))
        let answered = try #require(JSONSerialization.jsonObject(with: answeredData) as? [String: Any])
        let skippedData = try #require(UserQuestionResponse.skipped.toolContent.data(using: .utf8))
        let skipped = try #require(JSONSerialization.jsonObject(with: skippedData) as? [String: Any])

        #expect(answered["answered"] as? Bool == true)
        #expect(answered["answer"] as? String == "Staging")
        #expect(skipped["answered"] as? Bool == false)
        #expect(skipped["answer"] is NSNull)
    }

    @Test func providerProfilesCanBeAddedAndSelected() {
        var configuration = LLMTUIConfiguration()
        let profile = ProviderProfile(
            name: "Test LM Studio",
            type: .openAICompatible,
            baseURL: "http://127.0.0.1:1234/v1",
            model: "test-model",
            apiKeyEnvironment: ""
        )

        configuration.providers.append(profile)
        configuration.provider = ProviderConfiguration(
            name: profile.name,
            type: profile.type,
            baseURL: profile.baseURL,
            model: profile.model,
            apiKeyEnvironment: profile.apiKeyEnvironment
        )

        #expect(configuration.providers.contains(profile))
        #expect(configuration.provider.name == "Test LM Studio")
    }

    @Test func markdownHeadingsPreserveLevels() {
        let document = MarkdownDocument(source: "# Heading 1\n## Heading 2\n### Heading 3")
        #expect(document.blocks.map(\.kind) == [.heading(1), .heading(2), .heading(3)])
    }

    @Test func markdownParagraphsRemainDistinctBlocks() {
        let document = MarkdownDocument(source: "First paragraph.\n\nSecond paragraph.")
        #expect(document.blocks.map(\.kind) == [.paragraph, .paragraph])
    }

    @Test func markdownUnorderedListPreservesItems() {
        let document = MarkdownDocument(source: "- one\n- two\n- three")
        #expect(document.blocks.map(\.kind) == [
            .listItem(marker: .unordered, depth: 1),
            .listItem(marker: .unordered, depth: 1),
            .listItem(marker: .unordered, depth: 1)
        ])
    }

    @Test func markdownOrderedListPreservesOrdinals() {
        let document = MarkdownDocument(source: "1. first\n2. second\n3. third")
        #expect(document.blocks.map(\.kind) == [
            .listItem(marker: .ordered(1), depth: 1),
            .listItem(marker: .ordered(2), depth: 1),
            .listItem(marker: .ordered(3), depth: 1)
        ])
    }

    @Test func markdownNestedListPreservesDepth() {
        let document = MarkdownDocument(source: "- parent\n  - child\n    - grandchild")
        #expect(document.blocks.map(\.kind) == [
            .listItem(marker: .unordered, depth: 1),
            .listItem(marker: .unordered, depth: 2),
            .listItem(marker: .unordered, depth: 3)
        ])
    }

    @Test func markdownMixedListPreservesMarkerKinds() {
        let document = MarkdownDocument(source: "1. first\n   - child\n2. second")
        #expect(document.blocks.map(\.kind) == [
            .listItem(marker: .ordered(1), depth: 1),
            .listItem(marker: .unordered, depth: 2),
            .listItem(marker: .ordered(2), depth: 1)
        ])
    }

    @Test func markdownQuoteAndRuleProduceSemanticBlocks() {
        let document = MarkdownDocument(source: "> quoted text\n\n---")
        #expect(document.blocks.map(\.kind) == [.blockQuote(depth: 1), .thematicBreak])
    }

    @Test func markdownInlineFormattingAndSafeLinkArePreserved() {
        let document = MarkdownDocument(
            source: "**bold** *italic* `code` [example](https://example.com)"
        )
        let runs = document.blocks[0].content.runs

        #expect(runs.contains { $0.inlinePresentationIntent?.contains(.stronglyEmphasized) == true })
        #expect(runs.contains { $0.inlinePresentationIntent?.contains(.emphasized) == true })
        #expect(runs.contains { $0.inlinePresentationIntent?.contains(.code) == true })
        #expect(runs.contains { $0.link?.absoluteString == "https://example.com" })
    }

    @Test func markdownRejectsUnsafeLinkSchemes() {
        let document = MarkdownDocument(source: "[unsafe](javascript:alert(1))")
        #expect(document.blocks[0].content.runs.allSatisfy { $0.link == nil })
    }

    @Test func markdownInlineMathPreservesSourceForTypesetter() {
        let document = MarkdownDocument(source: "Value $\\alpha^2$ here.")
        let text = String(document.blocks[0].content.characters)

        #expect(text == "Value $\\alpha^2$ here.")
        #expect(LatexSource.inlineRanges(in: text).map { String(text[$0]) } == ["$\\alpha^2$"])
    }

    @Test func latexSourcePreservesComplexExpressions() {
        let expression = #"\frac{-b \pm \sqrt{b^2 - 4ac}}{2a}"#

        #expect(LatexSource.expression(from: "$$\(expression)$$") == expression)
        #expect(LatexSource.expression(from: "\\[\(expression)\\]") == expression)
    }

    @Test func richMessageRecognizesSingleAndMultilineDisplayMath() {
        let source = #"""
        Before.

        $$x^2 + y^2 = z^2$$

        \[
        \begin{aligned}
        a &= b + c \\
        d &= e + f
        \end{aligned}
        \]

        After.
        """#
        let blocks = RichMessageView(text: source).blocks

        #expect(blocks.contains(.math("x^2 + y^2 = z^2")))
        #expect(blocks.contains(.math(#"""
        \begin{aligned}
        a &= b + c \\
        d &= e + f
        \end{aligned}
        """#)))
    }

    @Test func incompleteDisplayMathRemainsMarkdownDuringStreaming() {
        let source = "Intro.\n\n$$\n\\frac{1}{2}"

        #expect(RichMessageView(text: source).blocks == [.markdown(source)])
    }

    @Test func fencedLatexUsesDedicatedRendererPath() {
        let source = "```latex\n\\frac{1}{2}\n```"

        #expect(RichMessageView(text: source).blocks == [.latexDocument("\\frac{1}{2}")])
    }

    @Test func realisticMarkdownDocumentPreservesAllOrdinaryBlocks() {
        let source = """
        # Deployment Guide

        This guide explains the process.

        ## Requirements

        - Xcode
        - Swift
        - macOS

        ## Steps

        1. Open the project.
        2. Build the application.
        3. Run the tests.

        > Note: keep the existing configuration file backed up.

        ### Example
        """
        let kinds = MarkdownDocument(source: source).blocks.map(\.kind)

        #expect(kinds.contains(.heading(1)))
        #expect(kinds.contains(.heading(2)))
        #expect(kinds.contains(.heading(3)))
        #expect(kinds.filter { $0 == .listItem(marker: .unordered, depth: 1) }.count == 3)
        #expect(kinds.contains(.listItem(marker: .ordered(3), depth: 1)))
        #expect(kinds.contains(.blockQuote(depth: 1)))
    }

    @Test func richMessageKeepsCodeAndTableOnDedicatedPaths() {
        let source = """
        # Deployment Guide

        1. Build.
        2. Run.

        ```swift
        print("Hello")
        ```

        | Name | Value |
        | --- | --- |
        | Mode | Debug |
        """
        let blocks = RichMessageView(text: source).blocks

        #expect(blocks.count == 3)
        #expect(blocks[0] == .markdown("# Deployment Guide\n\n1. Build.\n2. Run.\n"))
        #expect(blocks[1] == .code(language: "swift", source: "print(\"Hello\")"))
        #expect(blocks[2] == .table(headers: ["Name", "Value"], rows: [["Mode", "Debug"]]))
    }

    // MARK: - Tool harness

    @Test func everyToolParameterHasATypedSchemaWithDescription() {
        for definition in ToolRegistry.shared.definitions {
            guard case .object(let schema) = definition.function.parameters,
                  case .object(let properties)? = schema["properties"] else {
                Issue.record("Tool \(definition.function.name) has no object properties schema")
                continue
            }
            for (name, value) in properties {
                guard case .object(let property) = value else {
                    Issue.record("Tool \(definition.function.name) parameter \(name) is not an object schema")
                    continue
                }
                guard case .string? = property["type"] else {
                    Issue.record("Tool \(definition.function.name) parameter \(name) is missing a 'type'")
                    continue
                }
                guard case .string(let description)? = property["description"], !description.isEmpty else {
                    Issue.record("Tool \(definition.function.name) parameter \(name) is missing a description")
                    continue
                }
            }
        }
    }

    @Test func accumulatorKeepsToolCallsWhenContentIsEmptyString() throws {
        // Ollama sends an empty content string alongside tool_calls in the
        // same chunk; the accumulator must not drop the call because of it.
        let json = #"""
        {"choices":[{"delta":{"content":"","tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{\"query\":\"swift\"}"}}]},"finish_reason":null}]}
        """#
        let event = try JSONDecoder().decode(OpenAIStreamEvent.self, from: Data(json.utf8))
        var accumulator = OpenAIResponseAccumulator()
        let delta = accumulator.append(event)

        #expect(delta.content == nil)
        #expect(accumulator.toolCalls.count == 1)
        #expect(accumulator.toolCalls[0]?.function.name == "web_search")
    }

    @Test func accumulatorSeparatesToolCallsThatReuseAnIndexWithANewID() throws {
        var accumulator = OpenAIResponseAccumulator()

        let first = try JSONDecoder().decode(OpenAIStreamEvent.self, from: Data(#"""
        {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"web_search","arguments":"{}"}}]},"finish_reason":null}]}
        """#.utf8))
        _ = accumulator.append(first)

        let second = try JSONDecoder().decode(OpenAIStreamEvent.self, from: Data(#"""
        {"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_2","type":"function","function":{"name":"web_fetch","arguments":"{}"}}]},"finish_reason":null}]}
        """#.utf8))
        _ = accumulator.append(second)

        let names = accumulator.toolCalls.values.map(\.function.name).sorted()
        #expect(names == ["web_fetch", "web_search"])
    }

    @Test func openAIRequestErrorProducesAReadableDescription() {
        let error = OpenAIRequestError.http(400, #"{"error":"tools not supported"}"#)

        #expect(error.errorDescription?.contains("400") == true)
        #expect(error.errorDescription?.contains("tools not supported") == true)
    }

    @Test func readableTextStripsScriptsAndStylesButKeepsParagraphs() {
        let html = """
        <html><head><style>body{color:red}</style><script>var x=1;</script></head>
        <body><nav>Home</nav><main><h1>Title</h1><p>First paragraph.</p><p>Second paragraph.</p></main><footer>Bye</footer></body></html>
        """
        let text = WebToolRuntime.readableText(fromHTML: html)

        #expect(!text.contains("color:red"))
        #expect(!text.contains("var x=1"))
        #expect(!text.contains("Home"))
        #expect(!text.contains("Bye"))
        #expect(text.contains("Title"))
        #expect(text.contains("First paragraph."))
        #expect(text.contains("Second paragraph."))
        #expect(!text.contains("First paragraph.Second paragraph."))
    }

    @Test func isPrivateCoversExpandedRangesAndIPv6() {
        #expect(WebToolRuntime.isPrivate("172.20.0.5"))
        #expect(!WebToolRuntime.isPrivate("172.32.0.5"))
        #expect(WebToolRuntime.isPrivate("0.0.0.0"))
        #expect(WebToolRuntime.isPrivate("printer.local"))
        #expect(WebToolRuntime.isPrivate("fe80::1"))
        #expect(WebToolRuntime.isPrivate("fc00::1"))
        #expect(!WebToolRuntime.isPrivate("example.com"))
    }

    @Test func toolInstructionsMentionWebFetchOnlyWhenOffered() {
        let withoutFetch = LLMTUIDocumentationContext.toolInstructions(for: [ToolRegistry.shared.askUserDefinition])
        let withFetch = LLMTUIDocumentationContext.toolInstructions(for: ToolRegistry.shared.definitions)

        #expect(!withoutFetch.contains("web_fetch"))
        #expect(withFetch.contains("web_fetch"))
    }

    @Test func historyExpandsToolTranscriptAheadOfFinalAnswerWithTruncation() {
        let longResult = String(repeating: "x", count: 2000)
        var message = ChatMessage(role: .assistant, text: "Here is the answer.")
        message.toolTranscript = [
            .assistant(content: nil, toolCalls: [OpenAIToolCall(id: "call_1", type: "function", function: .init(name: "web_fetch", arguments: "{}"))]),
            .tool(content: longResult, toolCallID: "call_1")
        ]

        let expanded = OpenAIMessage.history([message])

        #expect(expanded.count == 3)
        #expect(expanded[0].role == "assistant")
        #expect(expanded[0].toolCalls?.first?.function.name == "web_fetch")
        #expect(expanded[1].role == "tool")
        #expect((expanded[1].content?.plainText?.count ?? 0) < longResult.count)
        #expect(expanded[1].content?.plainText?.contains("[older tool result truncated]") == true)
        #expect(expanded[2].role == "assistant")
        #expect(expanded[2].content?.plainText == "Here is the answer.")
    }

    @Test func fitHistoryToBudgetDropsOldestTurnsToFitContextWindow() {
        var history: [ChatMessage] = []
        for index in 0..<10 {
            history.append(ChatMessage(role: .user, text: "Question \(index) " + String(repeating: "a", count: 500)))
            history.append(ChatMessage(role: .assistant, text: "Answer \(index) " + String(repeating: "b", count: 500)))
        }

        let trimmed = OpenAICompatibleChatService.fitHistoryToBudget(history, contextWindow: 2048)

        #expect(trimmed.count < history.count)
        #expect(trimmed.last?.text.contains("Answer 9") == true)
    }

    @Test func contextWindowReadsMatchingModelProfile() {
        var configuration = LLMTUIConfiguration()
        configuration.provider.model = "llama3.1:8b"
        configuration.rawSettings["model_profiles.llama31.context_window"] = "16384"
        configuration.listSettings["model_profiles.llama31.match"] = ["llama3.1*"]

        #expect(OpenAICompatibleChatService.contextWindow(for: configuration) == 16384)

        configuration.provider.model = "some-other-model"
        #expect(OpenAICompatibleChatService.contextWindow(for: configuration) == 8192)
    }

    @Test func toolApprovalCenterReturnsAnEarlyResolveWithoutHanging() async {
        // The approval/rejection can arrive before wait() is called for it;
        // the center must not lose that result.
        let center = ToolApprovalCenter()
        let id = UUID()

        await center.resolve(id, approved: true)
        let result = await center.wait(for: id)

        #expect(result == true)
    }

    @Test func toolApprovalCenterCancelAllRejectsPendingWaits() async {
        let center = ToolApprovalCenter()
        let id = UUID()

        async let result = center.wait(for: id)
        try? await Task.sleep(for: .milliseconds(50))
        await center.cancelAll()

        #expect(await result == false)
    }

    @Test func userQuestionCenterReturnsAnEarlyResolveWithoutHanging() async {
        let center = UserQuestionCenter()
        let id = UUID()

        await center.resolve(id, response: .answered("Ollama"))
        let result = await center.wait(for: id)

        #expect(result == .answered("Ollama"))
    }

    @Test func diagnosticsLoggerWritesStructuredMetadataWithoutContent() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let suiteName = "LLMTUIGUITests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defaults.set(true, forKey: DiagnosticsLogger.enabledDefaultsKey)
        defaults.set(DiagnosticLevel.debug.rawValue, forKey: DiagnosticsLogger.levelDefaultsKey)
        defer {
            try? FileManager.default.removeItem(at: root)
            defaults.removePersistentDomain(forName: suiteName)
        }

        let logger = DiagnosticsLogger(
            defaults: defaults,
            directoryURL: root,
            maximumFileSize: 10_000,
            sessionID: UUID(uuidString: "11111111-1111-1111-1111-111111111111")!
        )
        await logger.log(
            level: .info,
            category: .chat,
            event: "request started",
            requestID: UUID(uuidString: "22222222-2222-2222-2222-222222222222"),
            metadata: [
                "provider_type": .string("openai_compatible"),
                "attachment_count": .integer(2)
            ],
            error: nil
        )

        let logURL = root.appending(path: "llmtuigui.log")
        let text = try String(contentsOf: logURL, encoding: .utf8)
        let line = try #require(text.split(separator: "\n").first)
        let record = try JSONDecoder().decode(DiagnosticRecord.self, from: Data(line.utf8))

        #expect(record.category == .chat)
        #expect(record.event == "request_started")
        #expect(record.metadata["attachment_count"] == .integer(2))
        #expect(!text.contains("prompt"))
        #expect(!text.contains("reply"))
    }

    @Test func diagnosticsLoggerFiltersLevelsAndRotates() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let suiteName = "LLMTUIGUITests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defaults.set(true, forKey: DiagnosticsLogger.enabledDefaultsKey)
        defaults.set(DiagnosticLevel.info.rawValue, forKey: DiagnosticsLogger.levelDefaultsKey)
        defer {
            try? FileManager.default.removeItem(at: root)
            defaults.removePersistentDomain(forName: suiteName)
        }

        let logger = DiagnosticsLogger(
            defaults: defaults,
            directoryURL: root,
            maximumFileSize: 350,
            archiveCount: 2
        )
        await logger.log(level: .debug, category: .application, event: "filtered", requestID: nil, metadata: [:], error: nil)
        for index in 0..<8 {
            await logger.log(
                level: .info,
                category: .application,
                event: "rotation_\(index)",
                requestID: nil,
                metadata: ["index": .integer(index)],
                error: nil
            )
        }

        let active = root.appending(path: "llmtuigui.log")
        #expect(FileManager.default.fileExists(atPath: active.path))
        #expect(FileManager.default.fileExists(atPath: active.path + ".1"))
        let files = try FileManager.default.contentsOfDirectory(atPath: root.path)
        #expect(files.count <= 3)
        let allText = try files.map {
            try String(contentsOf: root.appending(path: $0), encoding: .utf8)
        }.joined()
        #expect(!allText.contains("filtered"))
    }

    @Test func diagnosticsLoggerSanitizesProviderErrorsAndClearsArchives() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let suiteName = "LLMTUIGUITests.\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suiteName))
        defaults.set(true, forKey: DiagnosticsLogger.enabledDefaultsKey)
        defaults.set(DiagnosticLevel.info.rawValue, forKey: DiagnosticsLogger.levelDefaultsKey)
        defer {
            try? FileManager.default.removeItem(at: root)
            defaults.removePersistentDomain(forName: suiteName)
        }

        let logger = DiagnosticsLogger(
            defaults: defaults,
            directoryURL: root,
            maximumFileSize: 300,
            archiveCount: 4
        )
        let secret = "Bearer private-token at /Users/person/private https://secret.example"
        let safeError = SafeDiagnosticError.describe(
            OpenAIRequestError.http(401, secret),
            operation: "chat request"
        )
        for _ in 0..<4 {
            await logger.log(
                level: .error,
                category: .chat,
                event: "request_failed",
                requestID: nil,
                metadata: [:],
                error: safeError
            )
        }

        let files = try FileManager.default.contentsOfDirectory(atPath: root.path)
        let allText = try files.map {
            try String(contentsOf: root.appending(path: $0), encoding: .utf8)
        }.joined()
        #expect(!allText.contains(secret))
        #expect(!allText.contains("private-token"))
        #expect(allText.contains("http_401"))

        try await logger.clear()
        #expect((try FileManager.default.contentsOfDirectory(atPath: root.path)).isEmpty)
    }

    @Test func modelProfileNamesPreserveDotsAndRejectUnknownFields() {
        #expect(
            LLMTUIConfiguration.modelProfileName(
                fromKey: "model_profiles.qwen3.8-27b.context_window"
            ) == "qwen3.8-27b"
        )
        #expect(LLMTUIConfiguration.modelProfileName(fromKey: "providers.local.type") == nil)
        #expect(LLMTUIConfiguration.modelProfileName(fromKey: "model_profiles.local.unknown") == nil)
    }

    @Test func configurationValidationFindsDuplicateAndAmbiguousKeys() {
        let source = """
        default_provider: ollama
        default_provider: lmstudio
        model_profiles:
          qwen3.8-27b:
            context_window: 8192
        """

        let issues = LLMTUIConfigurationStore.validate(source)

        #expect(issues.count == 2)
        #expect(issues.contains { $0.contains("Duplicate key") && $0.contains("default_provider") })
        #expect(issues.contains { $0.contains("qwen3.8-27b") && $0.contains("path separator") })
    }

    @Test func configurationValidationAcceptsDistinctNestedKeys() {
        let source = """
        default_provider: ollama
        providers:
          ollama:
            type: ollama
            base_url: http://localhost:11434
        model_profiles:
          llama31:
            context_window: 8192
        """

        #expect(LLMTUIConfigurationStore.validate(source).isEmpty)
    }

    @Test func usageAggregationsGroupAndSortDeterministically() {
        let calendar = utcCalendar
        let dayOne = Date(timeIntervalSince1970: 1_700_000_000)
        let dayTwo = calendar.date(byAdding: .day, value: 1, to: dayOne)!
        let records = [
            UsageRecord(time: dayOne, provider: "ollama", model: "llama", promptTokens: 10, completionTokens: 20),
            UsageRecord(time: dayOne.addingTimeInterval(60), provider: "ollama", model: "llama", promptTokens: 5, completionTokens: 5),
            UsageRecord(time: dayTwo, provider: "lmstudio", model: "gemma", promptTokens: 50, completionTokens: 50)
        ]

        let days = aggregateByDay(records, calendar: calendar)
        let models = aggregateByModel(records)

        #expect(days.map(\.tokens) == [40, 100])
        #expect(models.map(\.id) == ["lmstudio/gemma", "ollama/llama"])
        #expect(models.map(\.requests) == [1, 2])
        #expect(peakHour(records, calendar: calendar) == calendar.component(.hour, from: dayTwo))
    }

    @Test func usageHelpersHandleStreaksSessionsAndEmptyInput() {
        let calendar = utcCalendar
        let start = calendar.startOfDay(for: Date(timeIntervalSince1970: 1_700_000_000))
        let consecutiveDays = (0..<3).map {
            DayTotal(day: calendar.date(byAdding: .day, value: $0, to: start)!, tokens: 10)
        }
        let laterDay = DayTotal(
            day: calendar.date(byAdding: .day, value: 5, to: start)!,
            tokens: 20
        )
        let sessions = [
            SessionMeta(savedAt: start, provider: "ollama", model: "small", promptTokens: 10, completionTokens: 5),
            SessionMeta(savedAt: laterDay.day, provider: "ollama", model: "large", promptTokens: 50, completionTokens: 75)
        ]

        #expect(longestStreak(consecutiveDays + [laterDay], calendar: calendar) == 3)
        #expect(longestStreak([], calendar: calendar) == 0)
        #expect(largestSession(sessions)?.tokens == 125)
        #expect(largestSession([]) == nil)
        #expect(peakHour([], calendar: calendar) == nil)
    }

    @Test func usageStoreLoadsValidFilesAndSkipsMalformedEntries() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let history = root.appending(path: "history")
        let cache = root.appending(path: "cache")
        try FileManager.default.createDirectory(at: history, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: cache, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }

        let usage = """
        {"time":"2026-10-03T10:20:30.123456789Z","provider":"ollama","model":"llama","prompt_tokens":10,"completion_tokens":20}
        malformed
        {"time":"2026-10-03T11:20:30Z","provider":"lmstudio","model":"gemma","prompt_tokens":3,"completion_tokens":7}
        """
        try usage.write(to: history.appending(path: "usage.jsonl"), atomically: true, encoding: .utf8)
        let session = """
        {"saved_at":"2026-10-03T12:00:00Z","provider":"ollama","model":"llama","prompt_tokens":40,"completion_tokens":60,"messages":[]}
        """
        try session.write(to: history.appending(path: "session-valid.json"), atomically: true, encoding: .utf8)
        try "invalid".write(to: history.appending(path: "session-invalid.json"), atomically: true, encoding: .utf8)
        try "{}".write(to: cache.appending(path: "one.json"), atomically: true, encoding: .utf8)
        try "ignored".write(to: cache.appending(path: "note.txt"), atomically: true, encoding: .utf8)

        let snapshot = await UsageStore.load(historyDir: history.path, cachePath: cache.path)

        #expect(snapshot.historyDirExists)
        #expect(snapshot.records.count == 2)
        #expect(snapshot.records.map(\.totalTokens) == [30, 10])
        #expect(snapshot.sessions.count == 1)
        #expect(snapshot.sessions.first?.totalTokens == 100)
        #expect(snapshot.cacheEntryCount == 1)
        #expect(snapshot.cacheSizeBytes > 0)
    }

    @Test func workspaceToolsRoundTripTextAndRejectEscapes() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let context = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))

        let writeResult = await ToolRegistry.shared.execute(
            name: "write_file",
            arguments: Data(#"{"path":"notes/test.txt","content":"first\nsecond"}"#.utf8),
            context: context,
            toolCallID: "write-1"
        )
        let readResult = await ToolRegistry.shared.execute(
            name: "read_file",
            arguments: Data(#"{"path":"notes/test.txt","offset":2,"limit":1}"#.utf8),
            context: context,
            toolCallID: "read-1"
        )
        let editResult = await ToolRegistry.shared.execute(
            name: "edit_file",
            arguments: Data(#"{"path":"notes/test.txt","old_text":"second","new_text":"updated"}"#.utf8),
            context: context,
            toolCallID: "edit-1"
        )
        let escapeResult = await ToolRegistry.shared.execute(
            name: "read_file",
            arguments: Data(#"{"path":"../../outside.txt"}"#.utf8),
            context: context,
            toolCallID: "read-escape"
        )

        #expect(!writeResult.isError)
        #expect(readResult.content.contains("2: second"))
        #expect(!editResult.isError)
        #expect(try String(contentsOf: root.appending(path: "notes/test.txt"), encoding: .utf8) == "first\nupdated")
        #expect(escapeResult.isError)
        #expect(escapeResult.content.contains("outside the configured workspace"))
    }

    @Test func toolRegistryReturnsStructuredErrorsForBadCalls() async {
        let context = ToolExecutionContext(
            workspaceURL: FileManager.default.temporaryDirectory,
            timeout: .seconds(1)
        )

        let unknown = await ToolRegistry.shared.execute(
            name: "not_a_tool",
            arguments: Data("{}".utf8),
            context: context,
            toolCallID: "unknown"
        )
        let nonObject = await ToolRegistry.shared.execute(
            name: "read_file",
            arguments: Data("[]".utf8),
            context: context,
            toolCallID: "invalid"
        )

        #expect(unknown.isError)
        #expect(unknown.toolCallID == "unknown")
        #expect(unknown.content.contains("Unknown tool"))
        #expect(nonObject.isError)
        #expect(nonObject.content.contains("arguments must be a JSON object"))
    }

    @Test func bundledAssistantReferenceCoversCoreNavigationTasks() {
        let prompt = LLMTUIDocumentationContext.systemPrompt

        #expect(prompt.contains("Personal Apps"))
        #expect(prompt.contains("provider"))
        #expect(prompt.contains("model profile"))
        #expect(prompt.contains("Mail"))
        #expect(prompt.contains("Calendar"))
        #expect(prompt.contains("~/.config/llmtui/config.yaml"))
    }

    @Test func lmStudioMetadataImportsConfiguredRuntimeSettingsAndCapabilities() throws {
        let payload = #"""
        {
          "models": [{
            "type": "llm",
            "key": "google/gemma-4-e4b",
            "architecture": "gemma4",
            "quantization": {"name": "Q4_K_M", "bits_per_weight": 4},
            "params_string": "7.5B",
            "loaded_instances": [{
              "id": "google/gemma-4-e4b",
              "config": {"context_length": 82688}
            }],
            "max_context_length": 131072,
            "capabilities": {
              "vision": true,
              "trained_for_tool_use": true,
              "reasoning": {
                "allowed_options": ["off", "on"],
                "default": "on"
              }
            }
          }]
        }
        """#

        let metadata = try ProviderModelDiscovery.parseLMStudioMetadata(
            Data(payload.utf8),
            model: "google/gemma-4-e4b"
        )

        #expect(metadata.configuredContextWindow == 82688)
        #expect(metadata.maximumContextWindow == 131072)
        #expect(metadata.loadedInstance == "google/gemma-4-e4b")
        #expect(metadata.architecture == "gemma4")
        #expect(metadata.quantization == "Q4_K_M")
        #expect(metadata.parameterCount == "7.5B")
        #expect(metadata.supportsVision == true)
        #expect(metadata.trainedForToolUse == true)
        #expect(metadata.supportsReasoning == true)
        #expect(metadata.defaultReasoningEnabled == true)
        #expect(metadata.profileValues == [
            "context_window": "82688",
            "reasoning_hint": "true"
        ])

        let aliasMetadata = try ProviderModelDiscovery.parseLMStudioMetadata(
            Data(payload.utf8),
            model: "gemma-4-e4b"
        )
        #expect(aliasMetadata.model == "google/gemma-4-e4b")
        #expect(aliasMetadata.configuredContextWindow == 82688)
    }

    @Test func lmStudioMetadataUsesMaximumContextWhenModelIsNotLoaded() throws {
        let payload = #"""
        {
          "models": [{
            "type": "llm",
            "key": "unloaded/model",
            "loaded_instances": [],
            "max_context_length": 65536
          }]
        }
        """#

        let metadata = try ProviderModelDiscovery.parseLMStudioMetadata(
            Data(payload.utf8),
            model: "unloaded/model"
        )

        #expect(metadata.configuredContextWindow == nil)
        #expect(metadata.profileValues["context_window"] == "65536")
        #expect(metadata.profileValues["reasoning_hint"] == nil)
    }

    @Test func personalAppsSavedScopeFailsClosedAndUsesNativeIDs() {
        var configuration = LLMTUIConfiguration()
        configuration.rawSettings["personal_apps.enabled"] = "true"
        configuration.rawSettings["personal_apps.mail.enabled"] = "true"
        configuration.rawSettings["personal_apps.calendar.enabled"] = "true"
        configuration.listSettings["personal_apps.mail.allowed_accounts"] = ["mail-native-1", " "]
        configuration.listSettings["personal_apps.calendar.allowed_calendars"] = ["calendar-native-1"]

        let scope = PersonalAppsSavedScope(configuration: configuration)

        #expect(scope.enabled)
        #expect(scope.mailEnabled)
        #expect(scope.calendarEnabled)
        #expect(scope.mailAccountIDs == ["mail-native-1"])
        #expect(scope.calendarIDs == ["calendar-native-1"])

        configuration.listSettings["personal_apps.mail.allowed_accounts"] = []
        #expect(PersonalAppsSavedScope(configuration: configuration).mailAccountIDs.isEmpty)
    }

    @Test func personalAppsLocalityRequiresDirectLoopbackHTTP() {
        #expect(PersonalAppsRuntime.isTrustedLocalEndpoint("http://localhost:1234/v1"))
        #expect(PersonalAppsRuntime.isTrustedLocalEndpoint("http://127.0.0.1:11434/v1"))
        #expect(PersonalAppsRuntime.isTrustedLocalEndpoint("http://[::1]:1234/v1"))
        #expect(!PersonalAppsRuntime.isTrustedLocalEndpoint("https://localhost:1234/v1"))
        #expect(!PersonalAppsRuntime.isTrustedLocalEndpoint("http://192.168.1.20:1234/v1"))
        #expect(!PersonalAppsRuntime.isTrustedLocalEndpoint("https://api.openai.com/v1"))
    }

    @Test func personalAppsDisclosureConsentIsBoundToExactProvider() {
        let runtime = PersonalAppsRuntime()
        var first = LLMTUIConfiguration()
        first.provider.name = "Remote A"
        first.provider.baseURL = "https://example.invalid/v1"
        first.provider.model = "model-a"

        runtime.allowRemoteDisclosure(for: first)
        #expect(runtime.providerCanReceivePrivateData(first))

        var second = first
        second.provider.model = "model-b"
        #expect(!runtime.providerCanReceivePrivateData(second))

        runtime.revokeRemoteDisclosure()
        #expect(!runtime.providerCanReceivePrivateData(first))
    }

    @Test func unsavedPersonalAppsDraftCannotBroadenRuntimeScope() {
        let runtime = PersonalAppsRuntime()
        var saved = LLMTUIConfiguration()
        saved.rawSettings["personal_apps.enabled"] = "true"
        saved.rawSettings["personal_apps.mail.enabled"] = "true"
        saved.listSettings["personal_apps.mail.allowed_accounts"] = ["saved-account"]
        runtime.applySavedConfiguration(saved)

        var draft = saved
        draft.listSettings["personal_apps.mail.allowed_accounts"] = ["saved-account", "unsaved-account"]

        #expect(runtime.savedScope.mailAccountIDs == ["saved-account"])
        #expect(PersonalAppsSavedScope(configuration: draft).mailAccountIDs.contains("unsaved-account"))
        #expect(!runtime.savedScope.mailAccountIDs.contains("unsaved-account"))
    }

    @Test func personalAppsMutationGateNeverEnablesRuntimeWrites() {
        let runtime = PersonalAppsRuntime()
        var configuration = LLMTUIConfiguration()
        configuration.rawSettings["personal_apps.enabled"] = "true"
        configuration.rawSettings["personal_apps.mutations.enabled"] = "true"
        runtime.applySavedConfiguration(configuration)

        #expect(runtime.savedScope.mutationsEnabled)
        #expect(!runtime.mutationsAvailable)
    }

    @Test func personalAppsFreeSlotsMergeOverlapsAndUseHalfOpenBounds() {
        let start = Date(timeIntervalSince1970: 0)
        let interval = DateInterval(start: start, duration: 6 * 3600)
        let busy = [
            DateInterval(start: start.addingTimeInterval(3600), duration: 2 * 3600),
            DateInterval(start: start.addingTimeInterval(2 * 3600), duration: 2 * 3600),
            DateInterval(start: start.addingTimeInterval(6 * 3600), duration: 3600)
        ]

        let free = PersonalAppsCalendarMath.freeSlots(
            within: interval,
            busyIntervals: busy,
            minimumDuration: 30 * 60
        )

        #expect(free == [
            DateInterval(start: start, end: start.addingTimeInterval(3600)),
            DateInterval(start: start.addingTimeInterval(4 * 3600), end: start.addingTimeInterval(6 * 3600))
        ])
    }

    @Test func personalAppsCalendarMathRemainsCorrectAcrossDSTAbsoluteInterval() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try #require(TimeZone(identifier: "Europe/Prague"))
        let dayStart = try #require(calendar.date(from: DateComponents(year: 2026, month: 3, day: 29)))
        let dayEnd = try #require(calendar.date(byAdding: .day, value: 1, to: dayStart))
        let interval = DateInterval(start: dayStart, end: dayEnd)

        let free = PersonalAppsCalendarMath.freeSlots(
            within: interval,
            busyIntervals: [],
            minimumDuration: 60
        )

        #expect(interval.duration == 23 * 3600)
        #expect(free == [interval])
    }

    @Test func mutationPlanDigestChangesWithExactMailContent() throws {
        let first = MailMutationPayload(accountID: "account", accountName: "Private", to: ["person@example.com"], cc: [], bcc: [], subject: "Subject", body: "First")
        let second = MailMutationPayload(accountID: "account", accountName: "Private", to: ["person@example.com"], cc: [], bcc: [], subject: "Subject", body: "Second")

        let firstPlan = try PersonalAppsMutationPlan.make(kind: .mailDraft, payload: .mail(first))
        let secondPlan = try PersonalAppsMutationPlan.make(kind: .mailDraft, payload: .mail(second))

        #expect(firstPlan.digest != secondPlan.digest)
    }

    @Test func mutationPlanIsConsumedOnceAndRejectsDigestSpoofing() throws {
        let journal = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).appendingPathComponent("journal.json")
        let store = PersonalAppsMutationStore(journalURL: journal)
        let payload = MailMutationPayload(accountID: "account", accountName: "Private", to: ["person@example.com"], cc: [], bcc: [], subject: "Subject", body: "Body")
        let plan = try PersonalAppsMutationPlan.make(kind: .mailSend, payload: .mail(payload))
        try store.insert(plan)

        #expect(throws: (any Error).self) { try store.consume(id: plan.id, expectedDigest: "spoofed") }
        #expect(try store.consume(id: plan.id, expectedDigest: plan.digest) == plan)
        #expect(throws: (any Error).self) { try store.consume(id: plan.id, expectedDigest: plan.digest) }
    }

    @Test func expiredMutationPlanCannotBeApplied() throws {
        let journal = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString).appendingPathComponent("journal.json")
        let store = PersonalAppsMutationStore(journalURL: journal)
        let payload = MailMutationPayload(accountID: "account", accountName: "Private", to: ["person@example.com"], cc: [], bcc: [], subject: "Subject", body: "Body")
        let expired = try PersonalAppsMutationPlan.make(kind: .mailDraft, payload: .mail(payload), now: Date(timeIntervalSinceNow: -600))
        try store.insert(expired)

        #expect(store.plan(id: expired.id) == nil)
        #expect(throws: (any Error).self) { try store.consume(id: expired.id, expectedDigest: expired.digest) }
    }

    private var utcCalendar: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(secondsFromGMT: 0)!
        return calendar
    }

}

struct LLMTUIExecutableTests {
    @Test func overrideThenBundledHelperThenInstallLocations() {
        let home = URL(fileURLWithPath: "/Users/someone")
        let urls = LLMTUIExecutable.candidates(
            environment: [LLMTUIExecutable.environmentKey: "~/bin/llmtui-dev"],
            bundle: .main,
            homeDirectory: home
        ).map(\.path)

        #expect(urls.first?.hasSuffix("/bin/llmtui-dev") == true)
        #expect(urls.first?.hasPrefix("~") == false)
        #expect(urls[1].hasSuffix("Contents/Helpers/llmtui"))
        #expect(urls.contains("/Users/someone/.local/bin/llmtui"))
        #expect(urls.last == "/usr/local/bin/llmtui")
    }

    @Test func noOverrideStartsWithBundledHelper() {
        let urls = LLMTUIExecutable.candidates(environment: [:], bundle: .main, homeDirectory: URL(fileURLWithPath: "/tmp"))
        #expect(urls.first?.path.hasSuffix("Contents/Helpers/llmtui") == true)
    }

    @Test func locatePrefersAnExecutableOverride() throws {
        let directory = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: directory) }
        let binary = directory.appending(path: "llmtui")
        try Data("#!/bin/sh\n".utf8).write(to: binary)
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: binary.path)

        let found = LLMTUIExecutable.locate(environment: [LLMTUIExecutable.environmentKey: binary.path])
        #expect(found?.path == binary.path)
    }
}
