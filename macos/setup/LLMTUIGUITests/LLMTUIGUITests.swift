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

    @Test func elideOldestToolResultsIfOverBudgetTrimsOldestFirstWithinTheTurn() {
        var messages: [OpenAIMessage] = [
            .init(role: "system", content: "system prompt"),
            .init(role: "user", content: "do the thing")
        ]
        let turnStartIndex = messages.count
        for index in 0..<5 {
            messages.append(.init(role: "assistant", content: nil as String?, toolCalls: [
                OpenAIToolCall(id: "call-\(index)", type: "function", function: .init(name: "web_fetch", arguments: "{}"))
            ]))
            messages.append(.tool(content: "result \(index) " + String(repeating: "x", count: 2000), toolCallID: "call-\(index)"))
        }

        OpenAICompatibleChatService.elideOldestToolResultsIfOverBudget(&messages, from: turnStartIndex, contextWindow: 1024)

        let toolContents = messages.filter { $0.role == "tool" }.map { $0.content?.plainText ?? "" }
        #expect(toolContents.first?.contains("elided") == true)
        // The budget is tight enough that more than one old result needs
        // trimming, but the most recent one should survive untouched so
        // the model still has its latest evidence to work from.
        #expect(toolContents.last?.contains("result 4") == true)
        #expect(OpenAICompatibleChatService.estimatedTokens(forRequestMessages: messages) <= Int(Double(1024) * 0.85) || toolContents.allSatisfy { $0.contains("elided") })
        // Messages before turnStartIndex (system/user) are never touched.
        #expect(messages[0].content?.plainText == "system prompt")
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
        #expect(writeResult.content.contains("Created"))
        #expect(readResult.content.contains("2| second"))
        #expect(!editResult.isError)
        #expect(try String(contentsOf: root.appending(path: "notes/test.txt"), encoding: .utf8) == "first\nupdated")
        #expect(escapeResult.isError)
        #expect(escapeResult.content.contains("outside the configured workspace"))
    }

    @Test func textDocumentPreservesCRLFLineCountAndRoundTrips() {
        let withoutTrailingNewline = TextDocument("a\r\nb\r\nc")
        #expect(withoutTrailingNewline.lineCount == 3)
        #expect(withoutTrailingNewline.lines == ["a", "b", "c"])
        #expect(withoutTrailingNewline.lineEnding == .crlf)
        #expect(withoutTrailingNewline.text == "a\r\nb\r\nc")

        let withTrailingNewline = TextDocument("a\r\nb\r\n")
        #expect(withTrailingNewline.lineCount == 2)
        #expect(withTrailingNewline.text == "a\r\nb\r\n")

        let lfDocument = TextDocument("x\ny\n")
        #expect(lfDocument.lineEnding == .lf)
        #expect(lfDocument.text == "x\ny\n")

        let empty = TextDocument("")
        #expect(empty.lineCount == 0)
        #expect(empty.text == "")
    }

    @Test func textDocumentLineOperationsReplaceInsertAndDelete() throws {
        var document = TextDocument("1\n2\n3\n")
        try document.replaceLines(start: 2, end: 2, with: ["X", "Y"])
        #expect(document.lines == ["1", "X", "Y", "3"])

        try document.insertLines(after: 0, newLines: ["top"])
        #expect(document.lines == ["top", "1", "X", "Y", "3"])

        try document.deleteLines(start: 2, end: 3)
        #expect(document.lines == ["top", "Y", "3"])
    }

    @Test func textDocumentLineOperationsRejectOutOfRangeRequests() {
        var document = TextDocument("1\n2\n")
        #expect(throws: FileEditingError.self) {
            try document.replaceLines(start: 1, end: 5, with: ["x"])
        }
        #expect(throws: FileEditingError.self) {
            try document.insertLines(after: 10, newLines: ["x"])
        }
        #expect(throws: FileEditingError.self) {
            try document.deleteLines(start: 0, end: 1)
        }
    }

    @Test func textEditApplierAppliesMultipleEditsAtomically() throws {
        let updated = try TextEditApplier.apply(
            [
                StringEdit(oldText: "a", newText: "A", replaceAll: false),
                StringEdit(oldText: "c", newText: "C", replaceAll: false)
            ],
            to: "a\nb\nc",
            path: "f.txt"
        )
        #expect(updated == "A\nb\nC")
    }

    @Test func textEditApplierFailsWithoutApplyingAnyEditWhenOneFails() {
        #expect(throws: FileEditingError.self) {
            _ = try TextEditApplier.apply(
                [
                    StringEdit(oldText: "a", newText: "A", replaceAll: false),
                    StringEdit(oldText: "does-not-exist", newText: "Z", replaceAll: false)
                ],
                to: "a\nb\nc",
                path: "f.txt"
            )
        }
    }

    @Test func textEditApplierReplaceAllReplacesEveryOccurrence() throws {
        let updated = try TextEditApplier.apply(
            [StringEdit(oldText: "x", newText: "y", replaceAll: true)],
            to: "x-x-x",
            path: "f.txt"
        )
        #expect(updated == "y-y-y")
    }

    @Test func textEditApplierReportsLineNumbersForZeroAndMultipleMatches() {
        do {
            _ = try TextEditApplier.apply([StringEdit(oldText: "missing", newText: "z", replaceAll: false)], to: "one\ntwo\n", path: "f.txt")
            Issue.record("expected a no-match error")
        } catch {
            #expect(error.localizedDescription.contains("did not match"))
        }

        do {
            _ = try TextEditApplier.apply([StringEdit(oldText: "dup", newText: "z", replaceAll: false)], to: "dup\nother\ndup\n", path: "f.txt")
            Issue.record("expected a multiple-match error")
        } catch {
            #expect(error.localizedDescription.contains("matched 2 times"))
            #expect(error.localizedDescription.contains("1, 3"))
        }
    }

    @Test func nearbyContextFindsAFuzzyMatchWhenOldTextIsAParaphrase() {
        // Reproduces the real failure this was added for: a model's old_text
        // is a slightly reworded memory of a paragraph, not a literal copy
        // of it, and the real paragraph is nowhere near the top of the file.
        let source = """
        ### Unrelated heading
        some other line
        another unrelated line
        The day was clear with sunny periods, reaching an estimated high of 22 degrees earlier today.
        trailing line
        """
        let paraphrase = "The day has been mostly clear with sunny periods, reaching an estimated high of 22 degrees earlier today."
        let context = TextEditApplier.nearbyContext(for: paraphrase, in: source)
        #expect(context.contains("most similar existing line is line 4"))
        #expect(context.contains("The day was clear with sunny periods"))
    }

    @Test func editFileRefusesWithoutAReadInTheSameTurn() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        try "one\ntwo\n".write(to: root.appending(path: "a.txt"), atomically: true, encoding: .utf8)

        // Neither read_file nor write_file has touched this path in this
        // context/turn — edit_file must refuse rather than let old_text be
        // checked against content the model never actually saw.
        let context = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))
        let result = await ToolRegistry.shared.execute(
            name: "edit_file",
            arguments: Data(#"{"path":"a.txt","old_text":"one","new_text":"ONE"}"#.utf8),
            context: context,
            toolCallID: "edit-unread"
        )
        #expect(result.isError)
        #expect(result.content.contains("hasn't been read"))
    }

    @Test func writeFileEchoesContentSoAFollowUpEditCanCopyItExactly() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let context = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))

        let writeResult = await ToolRegistry.shared.execute(
            name: "write_file",
            arguments: Data(#"{"path":"a.txt","content":"first line\nsecond line\n"}"#.utf8),
            context: context,
            toolCallID: "write-1"
        )
        #expect(!writeResult.isError)
        #expect(writeResult.content.contains("1| first line"))
        #expect(writeResult.content.contains("2| second line"))

        // write_file's own record satisfies edit_file's same-turn-read
        // requirement, so a model can go straight from write_file to
        // edit_file on the same path without an extra read_file round trip.
        let editResult = await ToolRegistry.shared.execute(
            name: "edit_file",
            arguments: Data(#"{"path":"a.txt","old_text":"second line","new_text":"SECOND LINE"}"#.utf8),
            context: context,
            toolCallID: "edit-1"
        )
        #expect(!editResult.isError)
        #expect(try String(contentsOf: root.appending(path: "a.txt"), encoding: .utf8) == "first line\nSECOND LINE\n")
    }

    @Test func editLinesRequiresAReadInTheSameTurnAndDetectsStaleContent() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        try "one\ntwo\nthree\n".write(to: root.appending(path: "a.txt"), atomically: true, encoding: .utf8)

        // The file exists on disk, but this turn never read or wrote it
        // through a tool call — edit_lines must refuse outright.
        let unreadContext = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))
        let unreadResult = await ToolRegistry.shared.execute(
            name: "edit_lines",
            arguments: Data(#"{"path":"a.txt","operation":"replace","start_line":2,"new_text":"TWO"}"#.utf8),
            context: unreadContext,
            toolCallID: "edit-unread"
        )
        #expect(unreadResult.isError)
        #expect(unreadResult.content.contains("hasn't been read"))

        // Read it through a tracked context, then change it on disk outside
        // that tracker — the next edit_lines call must see it as stale.
        let trackedContext = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))
        _ = await ToolRegistry.shared.execute(
            name: "read_file",
            arguments: Data(#"{"path":"a.txt"}"#.utf8),
            context: trackedContext,
            toolCallID: "r"
        )
        try "changed\nexternally\n".write(to: root.appending(path: "a.txt"), atomically: true, encoding: .utf8)
        let staleResult = await ToolRegistry.shared.execute(
            name: "edit_lines",
            arguments: Data(#"{"path":"a.txt","operation":"replace","start_line":1,"new_text":"X"}"#.utf8),
            context: trackedContext,
            toolCallID: "edit-stale"
        )
        #expect(staleResult.isError)
        #expect(staleResult.content.contains("changed on disk"))
    }

    @Test func editLinesReplacesInsertsAndDeletesByLineNumber() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let context = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))

        _ = await ToolRegistry.shared.execute(
            name: "write_file",
            arguments: Data(#"{"path":"a.txt","content":"one\ntwo\nthree\n"}"#.utf8),
            context: context,
            toolCallID: "w"
        )
        _ = await ToolRegistry.shared.execute(
            name: "read_file",
            arguments: Data(#"{"path":"a.txt"}"#.utf8),
            context: context,
            toolCallID: "r"
        )

        let replaceResult = await ToolRegistry.shared.execute(
            name: "edit_lines",
            arguments: Data(#"{"path":"a.txt","operation":"replace","start_line":2,"new_text":"TWO"}"#.utf8),
            context: context,
            toolCallID: "edit-replace"
        )
        #expect(!replaceResult.isError)
        #expect(try String(contentsOf: root.appending(path: "a.txt"), encoding: .utf8) == "one\nTWO\nthree\n")

        let insertResult = await ToolRegistry.shared.execute(
            name: "edit_lines",
            arguments: Data(#"{"path":"a.txt","operation":"insert","start_line":0,"new_text":"ZERO"}"#.utf8),
            context: context,
            toolCallID: "edit-insert"
        )
        #expect(!insertResult.isError)
        #expect(try String(contentsOf: root.appending(path: "a.txt"), encoding: .utf8) == "ZERO\none\nTWO\nthree\n")

        let deleteResult = await ToolRegistry.shared.execute(
            name: "edit_lines",
            arguments: Data(#"{"path":"a.txt","operation":"delete","start_line":1,"end_line":1}"#.utf8),
            context: context,
            toolCallID: "edit-delete"
        )
        #expect(!deleteResult.isError)
        #expect(try String(contentsOf: root.appending(path: "a.txt"), encoding: .utf8) == "one\nTWO\nthree\n")
    }

    @Test func editFileAppliesMultipleEditsAsOneAtomicToolCall() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let context = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))

        _ = await ToolRegistry.shared.execute(
            name: "write_file",
            arguments: Data(#"{"path":"a.txt","content":"alpha\nbeta\ngamma\n"}"#.utf8),
            context: context,
            toolCallID: "w"
        )

        let result = await ToolRegistry.shared.execute(
            name: "edit_file",
            arguments: Data(#"{"path":"a.txt","edits":[{"old_text":"alpha","new_text":"ALPHA"},{"old_text":"gamma","new_text":"GAMMA"}]}"#.utf8),
            context: context,
            toolCallID: "edit-multi"
        )
        #expect(!result.isError)
        #expect(result.content.contains("2 edits applied"))
        #expect(try String(contentsOf: root.appending(path: "a.txt"), encoding: .utf8) == "ALPHA\nbeta\nGAMMA\n")

        let atomicFailure = await ToolRegistry.shared.execute(
            name: "edit_file",
            arguments: Data(#"{"path":"a.txt","edits":[{"old_text":"beta","new_text":"BETA"},{"old_text":"not-there","new_text":"Z"}]}"#.utf8),
            context: context,
            toolCallID: "edit-multi-fail"
        )
        #expect(atomicFailure.isError)
        // Neither edit from the failed call took effect.
        #expect(try String(contentsOf: root.appending(path: "a.txt"), encoding: .utf8) == "ALPHA\nbeta\nGAMMA\n")
    }

    @Test func globMatchesTopLevelFilesWithDoubleStarPattern() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root.appending(path: "dir"), withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        try "top".write(to: root.appending(path: "a.swift"), atomically: true, encoding: .utf8)
        try "nested".write(to: root.appending(path: "dir/b.swift"), atomically: true, encoding: .utf8)
        let context = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))

        let result = await ToolRegistry.shared.execute(
            name: "glob",
            arguments: Data(#"{"pattern":"**/*.swift"}"#.utf8),
            context: context,
            toolCallID: "glob-1"
        )
        #expect(!result.isError)
        #expect(result.content.contains("a.swift"))
        #expect(result.content.contains("dir/b.swift"))
    }

    @Test func grepOutputUsesPathColonLineFormat() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        try "first\nneedle here\nlast\n".write(to: root.appending(path: "notes.txt"), atomically: true, encoding: .utf8)
        let context = ToolExecutionContext(workspaceURL: root, timeout: .seconds(2))

        let result = await ToolRegistry.shared.execute(
            name: "grep",
            arguments: Data(#"{"pattern":"needle"}"#.utf8),
            context: context,
            toolCallID: "grep-1"
        )
        #expect(!result.isError)
        #expect(result.content.contains("notes.txt:2: needle here"))
    }

    @Test func toolRegistryTreatsEmptyArgumentsAsNoArguments() async {
        let context = ToolExecutionContext(
            workspaceURL: FileManager.default.temporaryDirectory,
            timeout: .seconds(1)
        )
        let result = await ToolRegistry.shared.execute(
            name: "local_context",
            arguments: Data("".utf8),
            context: context,
            toolCallID: "empty-args"
        )
        #expect(!result.isError)
    }

    @Test func memoryStoreRoundTripsSnippetsAndEvictsOldestOverCap() async throws {
        let path = FileManager.default.temporaryDirectory.appending(path: "\(UUID().uuidString).yaml").path
        defer { try? FileManager.default.removeItem(atPath: path) }
        let store = MemoryStore(path: path, maxSnippets: 2)

        let first = try await store.add(text: "Prefers dark mode", tags: ["ui"])
        try await Task.sleep(for: .milliseconds(10))
        _ = try await store.add(text: "Uses Swift and Go")
        try await Task.sleep(for: .milliseconds(10))
        let third = try await store.add(text: "Lives in Prague")

        let loaded = try await store.load()
        #expect(loaded.count == 2)
        // The oldest ("Prefers dark mode") was evicted once the cap of 2 was exceeded.
        #expect(!loaded.contains { $0.id == first.id })
        #expect(loaded.contains { $0.id == third.id })

        let attributes = try FileManager.default.attributesOfItem(atPath: path)
        #expect((attributes[.posixPermissions] as? NSNumber)?.uint16Value == 0o600)
    }

    @Test func memoryStoreParsesFixtureWrittenInYAMLV3Style() async throws {
        let path = FileManager.default.temporaryDirectory.appending(path: "\(UUID().uuidString).yaml").path
        defer { try? FileManager.default.removeItem(atPath: path) }
        // Representative of what Go's yaml.v3 actually emits for a
        // []Snippet: unquoted plain scalars where no escaping is needed, a
        // double-quoted scalar when the text needs it, a block scalar for
        // embedded newlines, and a nested tags list.
        let fixture = #"""
        - id: a1b2c3d4
          text: Prefers concise answers
          created_at: 2025-01-15T10:30:00Z
          updated_at: 2025-01-15T10:30:00Z
        - id: e5f6a7b8
          text: "Uses a colon: like this"
          created_at: 2025-02-01T00:00:00Z
          updated_at: 2025-02-01T00:00:00Z
          tags:
            - style
            - ui
        - id: c9d0e1f2
          text: |
            Line one
            Line two
          created_at: 2025-03-01T00:00:00Z
          updated_at: 2025-03-01T00:00:00Z
        """#
        try fixture.write(toFile: path, atomically: true, encoding: .utf8)
        let store = MemoryStore(path: path)

        let loaded = try await store.load()
        #expect(loaded.count == 3)
        #expect(loaded[0].text == "Prefers concise answers")
        #expect(loaded[1].text == "Uses a colon: like this")
        #expect(loaded[1].tags == ["style", "ui"])
        #expect(loaded[2].text == "Line one\nLine two\n")
    }

    @Test func memoryStoreRemovesByExactIDOrUnambiguousPrefix() async throws {
        let path = FileManager.default.temporaryDirectory.appending(path: "\(UUID().uuidString).yaml").path
        defer { try? FileManager.default.removeItem(atPath: path) }
        let store = MemoryStore(path: path)
        let snippet = try await store.add(text: "Prefers tabs over spaces")

        try await store.remove(id: String(snippet.id.prefix(4)))
        let loaded = try await store.load()
        #expect(loaded.isEmpty)

        await #expect(throws: MemoryStoreError.self) {
            try await store.remove(id: "doesnotexist")
        }
    }

    @Test func memoryStoreRelevantScoresByKeywordOverlapLikeGo() async throws {
        let path = FileManager.default.temporaryDirectory.appending(path: "\(UUID().uuidString).yaml").path
        defer { try? FileManager.default.removeItem(atPath: path) }
        let store = MemoryStore(path: path)
        _ = try await store.add(text: "Prefers dark mode in the editor")
        _ = try await store.add(text: "Uses Swift for iOS development")
        _ = try await store.add(text: "Lives in Prague")

        let results = try await store.relevant(to: "What editor mode do they prefer?", limit: 5)
        #expect(results.first?.text == "Prefers dark mode in the editor")
        #expect(!results.contains { $0.text == "Lives in Prague" })
    }

    @Test func memoryStoreRejectsEmptyTextAndObviousSecrets() async throws {
        let path = FileManager.default.temporaryDirectory.appending(path: "\(UUID().uuidString).yaml").path
        defer { try? FileManager.default.removeItem(atPath: path) }
        let store = MemoryStore(path: path)

        await #expect(throws: MemoryStoreError.self) {
            try await store.add(text: "   ")
        }
        await #expect(throws: MemoryStoreError.self) {
            try await store.add(text: "my api key is sk-abc123def456ghi789jkl012")
        }
        let loaded = try await store.load()
        #expect(loaded.isEmpty)
    }

    @Test func textToolCallParserExtractsAClosedBlockAndStripsItFromRemainingText() {
        let text = #"""
        Let me check that for you.
        <tool_call>{"name": "web_search", "arguments": {"query": "swift concurrency"}}</tool_call>
        """#
        let (calls, remaining) = TextToolCallParser.extract(from: text)
        #expect(calls.count == 1)
        #expect(calls.first?.name == "web_search")
        #expect(calls.first?.argumentsJSON.contains("swift concurrency") == true)
        #expect(!remaining.contains("<tool_call>"))
        #expect(remaining.contains("Let me check"))
    }

    @Test func textToolCallParserToleratesAJSONFenceInsideTheBlock() {
        let text = #"""
        <tool_call>
        ```json
        {"name": "read_file", "arguments": {"path": "a.txt"}}
        ```
        </tool_call>
        """#
        let (calls, _) = TextToolCallParser.extract(from: text)
        #expect(calls.count == 1)
        #expect(calls.first?.name == "read_file")
    }

    @Test func textToolCallParserHandlesADanglingOpenTagFromATruncatedResponse() {
        let text = #"""
        I'll look that up now.
        <tool_call>{"name": "web_search", "arguments": {"query": "trunc
        """#
        let (calls, remaining) = TextToolCallParser.extract(from: text)
        // The JSON itself is incomplete, so it can't be parsed — this just
        // confirms the dangling-tag path doesn't crash or hang, and the
        // ordinary closed-block path remains the common case.
        #expect(calls.isEmpty)
        #expect(remaining.contains("I'll look that up now") || remaining.contains("<tool_call>"))
    }

    @Test func textToolCallParserReturnsNoCallsForPlainProse() {
        let (calls, remaining) = TextToolCallParser.extract(from: "Just a normal answer, no tool needed.")
        #expect(calls.isEmpty)
        #expect(remaining == "Just a normal answer, no tool needed.")
    }

    @Test func textToolCallParserRejectsABlockWithNoName() {
        let (calls, _) = TextToolCallParser.extract(from: #"<tool_call>{"arguments": {}}</tool_call>"#)
        #expect(calls.isEmpty)
    }

    @Test func textToolCallParserAcceptsAStringArgumentsValue() {
        let (calls, _) = TextToolCallParser.extract(from: #"<tool_call>{"name": "memory_search", "arguments": "{\"query\":\"x\"}"}</tool_call>"#)
        #expect(calls.first?.argumentsJSON == #"{"query":"x"}"#)
    }

    @Test func textToolCallParserInstructionsListEachToolWithItsParameters() {
        let tool = ToolRegistry.shared.definitions.first { $0.function.name == "read_file" }!
        let instructions = TextToolCallParser.instructions(for: [tool])
        #expect(instructions.contains("<tool_call>"))
        #expect(instructions.contains("read_file"))
        #expect(instructions.contains("offset"))
        // Shared category guidance (file edits) is included, not just the
        // calling-convention section.
        #expect(instructions.contains("File edits:"))
    }

    @Test func memoryToolsRoundTripThroughToolRegistry() async throws {
        let path = FileManager.default.temporaryDirectory.appending(path: "\(UUID().uuidString).yaml").path
        defer { try? FileManager.default.removeItem(atPath: path) }
        let context = ToolExecutionContext(
            workspaceURL: FileManager.default.temporaryDirectory,
            timeout: .seconds(2),
            memoryStore: MemoryStore(path: path)
        )

        let saveResult = await ToolRegistry.shared.execute(
            name: "memory_save",
            arguments: Data(#"{"text":"Prefers concise answers","tags":["style"]}"#.utf8),
            context: context,
            toolCallID: "save-1"
        )
        #expect(!saveResult.isError)
        #expect(saveResult.content.contains("Remembered"))

        let searchResult = await ToolRegistry.shared.execute(
            name: "memory_search",
            arguments: Data(#"{"query":"concise"}"#.utf8),
            context: context,
            toolCallID: "search-1"
        )
        #expect(!searchResult.isError)
        #expect(searchResult.content.contains("Prefers concise answers"))

        let id = String(saveResult.content.split(separator: " ")[2].dropLast(2))
        let deleteResult = await ToolRegistry.shared.execute(
            name: "memory_delete",
            arguments: Data(#"{"id":"\#(id)"}"#.utf8),
            context: context,
            toolCallID: "delete-1"
        )
        #expect(!deleteResult.isError)

        let afterDelete = await ToolRegistry.shared.execute(
            name: "memory_search",
            arguments: Data(#"{"query":"concise"}"#.utf8),
            context: context,
            toolCallID: "search-2"
        )
        #expect(afterDelete.content.contains("No remembered snippets"))
    }

    @Test func wordBoundaryMatcherDetectsGPTOSSButNotLookalikes() {
        #expect(WordBoundaryMatcher.matches("gpt-oss", in: "gpt-oss-20b"))
        #expect(WordBoundaryMatcher.matches("gpt-oss", in: "openai/gpt-oss-120b"))
        #expect(WordBoundaryMatcher.matches("gpt-oss", in: "gpt-oss20b"))
        #expect(WordBoundaryMatcher.matches("gpt-oss", in: "GPT-OSS-20B"))
        #expect(!WordBoundaryMatcher.matches("gpt-oss", in: "xgpt-ossy"))
        #expect(!WordBoundaryMatcher.matches("gpt-oss", in: "llama-3.1-8b"))
    }

    @Test func modelFamilyDetectsGPTOSSFromIDOrArchitecture() {
        #expect(ModelFamily.detect(modelID: "gpt-oss-20b", architecture: nil) == .gptOSS)
        #expect(ModelFamily.detect(modelID: "local-model", architecture: "gpt-oss") == .gptOSS)
        #expect(ModelFamily.detect(modelID: "qwen3-8b", architecture: "qwen3") == .other)
    }

    @Test func reasoningCapabilityDetectionPrefersGPTOSSOverReportedMetadata() {
        let toggleMetadata = ProviderModelMetadata(
            model: "gpt-oss-20b", loadedInstance: nil, configuredContextWindow: nil,
            maximumContextWindow: nil, architecture: nil, quantization: nil, parameterCount: nil,
            supportsVision: nil, trainedForToolUse: nil, supportsReasoning: true, defaultReasoningEnabled: nil
        )
        // Even if a server mistakenly reports a plain on/off, GPT-OSS always
        // gets the level-based menu — it has no real "off".
        #expect(ReasoningCapabilityDetector.capability(modelID: "gpt-oss-20b", metadata: toggleMetadata) == .levels)
        #expect(ReasoningCapabilityDetector.capability(modelID: "gpt-oss-20b", metadata: nil) == .levels)

        let qwenToggle = ProviderModelMetadata(
            model: "qwen3-8b", loadedInstance: nil, configuredContextWindow: nil,
            maximumContextWindow: nil, architecture: nil, quantization: nil, parameterCount: nil,
            supportsVision: nil, trainedForToolUse: nil, supportsReasoning: true, defaultReasoningEnabled: nil
        )
        #expect(ReasoningCapabilityDetector.capability(modelID: "qwen3-8b", metadata: qwenToggle) == .toggle)

        let noReasoning = ProviderModelMetadata(
            model: "plain-model", loadedInstance: nil, configuredContextWindow: nil,
            maximumContextWindow: nil, architecture: nil, quantization: nil, parameterCount: nil,
            supportsVision: nil, trainedForToolUse: nil, supportsReasoning: false, defaultReasoningEnabled: nil
        )
        #expect(ReasoningCapabilityDetector.capability(modelID: "plain-model", metadata: noReasoning) == .unsupported)
        #expect(ReasoningCapabilityDetector.capability(modelID: "unknown-model", metadata: nil) == .unknown)
    }

    @Test func reasoningChoicesOfferedMatchCapability() {
        #expect(ChatReasoningChoice.offered(for: .levels) == [.automatic, .low, .medium, .high])
        #expect(ChatReasoningChoice.offered(for: .toggle) == [.automatic, .on, .off])
        #expect(ChatReasoningChoice.offered(for: .unsupported) == [.automatic])
        #expect(ChatReasoningChoice.offered(for: .unknown).contains(.low))
        #expect(ChatReasoningChoice.offered(for: .unknown).contains(.on))
    }

    @Test func reasoningRequestEncoderProducesEffortForLevelsAndTemplateKwargsForToggle() {
        #expect(ReasoningRequestEncoder.requestBody(for: .automatic, capability: .levels).isEmpty)
        let levelBody = ReasoningRequestEncoder.requestBody(for: .high, capability: .levels)
        #expect(levelBody["reasoning_effort"] as? String == "high")
        #expect(levelBody["chat_template_kwargs"] == nil)

        // A level-only model has no real "off" — sending .off produces no
        // extra body keys rather than an invalid field.
        #expect(ReasoningRequestEncoder.requestBody(for: .off, capability: .levels).isEmpty)

        let onBody = ReasoningRequestEncoder.requestBody(for: .on, capability: .toggle)
        let onKwargs = onBody["chat_template_kwargs"] as? [String: Any]
        #expect(onKwargs?["enable_thinking"] as? Bool == true)

        let offBody = ReasoningRequestEncoder.requestBody(for: .off, capability: .toggle)
        let offKwargs = offBody["chat_template_kwargs"] as? [String: Any]
        #expect(offKwargs?["enable_thinking"] as? Bool == false)

        #expect(ReasoningRequestEncoder.requestBody(for: .on, capability: .unsupported).isEmpty)
    }

    @Test func reasoningSupportTrackerRemembersUnsupportedModels() async {
        let tracker = ReasoningSupportTracker()
        #expect(await !tracker.isUnsupported("model-a"))
        await tracker.markUnsupported("model-a")
        #expect(await tracker.isUnsupported("model-a"))
        #expect(await !tracker.isUnsupported("model-b"))
    }

    @Test func contextLengthErrorParserExtractsTheReportedAvailableTokenCount() {
        let body = #"{"error":{"message":"Prompt exceeds maximum context length: 3142 tokens requested, 3072 available","type":"invalid_request_error","param":null,"code":400}}"#
        #expect(ContextLengthErrorParser.availableTokens(in: body) == 3072)
    }

    @Test func contextLengthErrorParserReturnsNilForUnrelated400Bodies() {
        #expect(ContextLengthErrorParser.availableTokens(in: #"{"error":"tools are not supported"}"#) == nil)
        #expect(ContextLengthErrorParser.availableTokens(in: "") == nil)
    }

    @Test func contextWindowTrackerOnlyEverShrinksARememberedWindow() async {
        let tracker = ContextWindowTracker()
        #expect(await tracker.discovered(for: "model-a") == nil)
        await tracker.record(3072, for: "model-a")
        #expect(await tracker.discovered(for: "model-a") == 3072)
        // A later, larger report never overrides an already-proven-correct
        // smaller one from earlier this session.
        await tracker.record(8192, for: "model-a")
        #expect(await tracker.discovered(for: "model-a") == 3072)
        // A genuinely smaller later report is adopted.
        await tracker.record(2048, for: "model-a")
        #expect(await tracker.discovered(for: "model-a") == 2048)
        #expect(await tracker.discovered(for: "model-b") == nil)
    }

    @Test func chatRuntimeOptionsCarryToolsAndReasoningChoiceIndependently() {
        let model = AppModel(nativeAgentEnabled: false)
        model.configuration.provider.model = "gpt-oss-20b"

        let options = model.chatRuntimeOptions(usesNativeAgent: false, usesTools: true, reasoning: .high)
        #expect(!options.agentEnabled)
        #expect(options.toolsEnabled)
        #expect(options.reasoningChoice == .high)
        #expect(options.reasoningCapability == .levels)
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
