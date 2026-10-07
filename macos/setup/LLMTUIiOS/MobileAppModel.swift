import Foundation
import Observation
import UIKit

@MainActor
@Observable
final class MobileAppModel {
    var profiles: [MobileProviderProfile] = []
    var activeProfileID: UUID?
    var messages: [MobileChatMessage] = []
    var draft = ""
    var toolsEnabled = true
    var agentEnabled = false
    var reasoning: MobileReasoning = .automatic
    var draftAttachments: [MobileAttachment] = []
    var isGenerating = false
    var isDiscoveringModels = false
    var discoveredModels: [String] = []
    var pendingToolApproval: PendingToolApproval?
    var pendingQuestion: String?
    var questionAnswer = ""
    var queuedMessages: [MobileQueuedMessage] = []
    var errorMessage: String?
    var hasRuntimeProblem = false

    private static let maxQueuedMessages = 10
    private let providerStore = MobileProviderStore()
    private let credentialStore = KeychainCredentialStore()
    private let runtime = MobileChatRuntime()
    private var generationTask: Task<Void, Never>?
    private var approvalContinuation: CheckedContinuation<Bool, Never>?
    private var questionContinuation: CheckedContinuation<String, Never>?

    init() {
        profiles = providerStore.loadProfiles()
        activeProfileID = providerStore.loadActiveID()
        if activeProfileID == nil || !profiles.contains(where: { $0.id == activeProfileID }) {
            activeProfileID = profiles.first?.id
        }
    }

    var activeProfile: MobileProviderProfile? {
        profiles.first { $0.id == activeProfileID }
    }

    func addProfile() -> MobileProviderProfile {
        let profile = MobileProviderProfile(name: "New provider")
        profiles.append(profile)
        activeProfileID = profile.id
        persistProfiles()
        return profile
    }

    func saveProfile(_ profile: MobileProviderProfile, apiKey: String) {
        do {
            let name = profile.name.trimmingCharacters(in: .whitespacesAndNewlines)
            guard !name.isEmpty else { throw ValidationError("Provider name cannot be empty.") }
            guard let url = URL(string: profile.baseURL),
                  let scheme = url.scheme?.lowercased(),
                  ["http", "https"].contains(scheme) else { throw MobileChatError.invalidBaseURL }

            var updated = profile
            updated.name = name
            if let index = profiles.firstIndex(where: { $0.id == profile.id }) {
                profiles[index] = updated
            } else {
                profiles.append(updated)
            }
            try credentialStore.setAPIKey(apiKey, for: profile.id)
            activeProfileID = profile.id
            hasRuntimeProblem = false
            persistProfiles()
        } catch {
            errorMessage = error.localizedDescription
        }
    }

    func deleteProfile(_ profile: MobileProviderProfile) {
        profiles.removeAll { $0.id == profile.id }
        try? credentialStore.removeAPIKey(for: profile.id)
        if activeProfileID == profile.id { activeProfileID = profiles.first?.id }
        persistProfiles()
    }

    func apiKey(for profile: MobileProviderProfile) -> String {
        (try? credentialStore.apiKey(for: profile.id)) ?? ""
    }

    func selectProfile(_ id: UUID?) {
        activeProfileID = id
        hasRuntimeProblem = false
        providerStore.saveActiveID(id)
        discoveredModels = []
    }

    func discoverModels() {
        guard let profile = activeProfile else {
            errorMessage = MobileChatError.noProvider.localizedDescription
            return
        }
        isDiscoveringModels = true
        hasRuntimeProblem = false
        Task {
            defer { isDiscoveringModels = false }
            do {
                discoveredModels = try await runtime.discoverModels(
                    profile: profile,
                    apiKey: apiKey(for: profile)
                )
                hasRuntimeProblem = false
            } catch {
                hasRuntimeProblem = true
                errorMessage = error.localizedDescription
            }
        }
    }

    func useModel(_ model: String) {
        guard let id = activeProfileID,
              let index = profiles.firstIndex(where: { $0.id == id }) else { return }
        profiles[index].model = model
        persistProfiles()
    }

    func addAttachment(data: Data) {
        guard draftAttachments.count < 4 else {
            errorMessage = "You can attach up to four images."
            return
        }
        guard let image = UIImage(data: data),
              let encoded = image.preparingThumbnail(of: CGSize(width: 1_600, height: 1_600))?
                .jpegData(compressionQuality: 0.82) ?? image.jpegData(compressionQuality: 0.82)
        else {
            errorMessage = "The selected image could not be read."
            return
        }
        draftAttachments.append(MobileAttachment(data: encoded))
    }

    func removeAttachment(_ attachment: MobileAttachment) {
        draftAttachments.removeAll { $0.id == attachment.id }
    }

    /// Sends the draft, or queues it while a reply is still generating. A
    /// queued message keeps the tools, agent and reasoning choices it was
    /// written with and sends automatically, in order, once the current
    /// reply finishes (the same behavior as the macOS app).
    func send() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || !draftAttachments.isEmpty else { return }
        let options = MobileTurnOptions(toolsEnabled: toolsEnabled, agentEnabled: agentEnabled, reasoning: reasoning)

        guard !isGenerating else {
            guard queuedMessages.count < Self.maxQueuedMessages else {
                errorMessage = "You can queue up to \(Self.maxQueuedMessages) messages."
                return
            }
            queuedMessages.append(MobileQueuedMessage(text: text, attachments: draftAttachments, options: options))
            draft = ""
            draftAttachments = []
            return
        }

        let attachments = draftAttachments
        draft = ""
        draftAttachments = []
        dispatch(text: text, attachments: attachments, options: options)
    }

    func removeQueuedMessage(_ queued: MobileQueuedMessage) {
        queuedMessages.removeAll { $0.id == queued.id }
    }

    /// Takes a queued message back into the composer for editing.
    func editQueuedMessage(_ queued: MobileQueuedMessage) {
        queuedMessages.removeAll { $0.id == queued.id }
        draft = queued.text
        draftAttachments = queued.attachments
        toolsEnabled = queued.options.toolsEnabled
        agentEnabled = queued.options.agentEnabled
        reasoning = queued.options.reasoning
    }

    private func dispatch(text: String, attachments: [MobileAttachment], options: MobileTurnOptions) {
        guard let profile = activeProfile else {
            errorMessage = MobileChatError.noProvider.localizedDescription
            return
        }
        guard !profile.model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            errorMessage = MobileChatError.noModel.localizedDescription
            return
        }

        messages.append(MobileChatMessage(role: .user, text: text, attachments: attachments))
        let assistantID = UUID()
        messages.append(MobileChatMessage(id: assistantID, role: .assistant, text: "", isStreaming: true))
        isGenerating = true
        hasRuntimeProblem = false
        separateNextRound = false

        let history = messages.filter { $0.id != assistantID }
        generationTask = Task {
            do {
                try await runToolLoop(
                    profile: profile,
                    apiKey: apiKey(for: profile),
                    initialHistory: history,
                    assistantID: assistantID,
                    options: options
                )
                hasRuntimeProblem = false
            } catch is CancellationError {
                appendDelta("\n\nStopped.", to: assistantID)
            } catch {
                hasRuntimeProblem = true
                if message(id: assistantID)?.text.isEmpty == true {
                    removeMessage(id: assistantID)
                }
                errorMessage = error.localizedDescription
            }
            isGenerating = false
            markStreamingFinished(id: assistantID)
            pendingToolApproval = nil
            pendingQuestion = nil
            // Started from inside the finished task, after its state is
            // reset, so the next reply never races the previous one.
            dequeueNextIfNeeded()
        }
    }

    private func dequeueNextIfNeeded() {
        guard !queuedMessages.isEmpty else { return }
        let next = queuedMessages.removeFirst()
        dispatch(text: next.text, attachments: next.attachments, options: next.options)
    }

    func stop() {
        generationTask?.cancel()
        generationTask = nil
        approvalContinuation?.resume(returning: false)
        approvalContinuation = nil
        questionContinuation?.resume(returning: "")
        questionContinuation = nil
    }

    func clearChat() {
        queuedMessages.removeAll()
        stop()
        messages.removeAll()
    }

    func resolveApproval(_ approved: Bool) {
        pendingToolApproval = nil
        approvalContinuation?.resume(returning: approved)
        approvalContinuation = nil
    }

    func submitQuestion() {
        let answer = questionAnswer.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !answer.isEmpty else { return }
        questionAnswer = ""
        pendingQuestion = nil
        questionContinuation?.resume(returning: answer)
        questionContinuation = nil
    }

    private func runToolLoop(
        profile: MobileProviderProfile,
        apiKey: String,
        initialHistory: [MobileChatMessage],
        assistantID: UUID,
        options: MobileTurnOptions
    ) async throws {
        var wireMessages = initialHistory.map(wireMessage)
        var systemInstructions = """
        Format answers for a narrow mobile display using concise paragraphs and valid Markdown. Put blank lines between paragraphs, headings, and lists. Use fenced code blocks for code. For Mermaid, emit a fenced block beginning with ```mermaid and valid Mermaid syntax. Quote every human-readable node or edge label containing whitespace or punctuation, for example A["Validate Input (Required)"] and A -->|"Valid"| B. Mermaid comments must use %% on their own line. For mathematics, emit only KaTeX-compatible expressions using $...$ or \\(...\\) inline and $$...$$ or \\[...\\] for display math. Never emit a complete LaTeX document, preamble, or text-layout environment such as document, itemize, enumerate, verbatim, table, or figure.
        """
        if options.agentEnabled {
            systemInstructions += """

            You are in bounded agent mode. Work toward the user's goal using available tools, inspect tool results before continuing, ask when an important choice is missing, and stop after completing the task. Never claim a tool ran unless its result is present.
            """
        }
        let memoryEnabled = MobileMemoryStore.isEnabled
        let usesTools = options.toolsEnabled || options.agentEnabled
        if memoryEnabled {
            let memories = await MobileMemoryStore.shared.entries()
            if let section = MobileMemoryStore.promptSection(for: memories) {
                systemInstructions += "\n\n" + section
            }
            if usesTools {
                systemInstructions += "\n\n" + MobileMemoryStore.toolInstructions
            }
        }
        wireMessages.insert(["role": "system", "content": systemInstructions], at: 0)
        let tools = usesTools ? MobileChatRuntime.toolDefinitions(memoryEnabled: memoryEnabled) : nil
        let maximumRounds = options.agentEnabled ? 10 : 4
        for _ in 0..<maximumRounds {
            let result = try await runtime.streamTurn(
                profile: profile,
                apiKey: apiKey,
                messages: wireMessages,
                tools: tools,
                reasoning: options.reasoning
            ) { [weak self] delta in
                await self?.appendRoundDelta(delta, to: assistantID)
            }
            if result.toolCalls.isEmpty { return }

            wireMessages.append([
                "role": "assistant",
                "content": result.text,
                "tool_calls": result.toolCalls.map { call in
                    [
                        "id": call.id,
                        "type": "function",
                        "function": ["name": call.name, "arguments": call.arguments]
                    ] as [String: Any]
                }
            ])
            for call in result.toolCalls {
                let output = try await execute(call, assistantID: assistantID)
                wireMessages.append([
                    "role": "tool",
                    "tool_call_id": call.id,
                    "content": output
                ])
            }
            separateNextRound = true
        }

        // Out of tool rounds: ask once more without tools, so the reply ends
        // with an answer built from the results gathered so far rather than
        // stopping right after a tool call.
        wireMessages.append([
            "role": "user",
            "content": "[Tool round limit reached] Answer now using only the tool results above, without calling more tools. Say briefly what is still unknown."
        ])
        _ = try await runtime.streamTurn(
            profile: profile,
            apiKey: apiKey,
            messages: wireMessages,
            tools: nil,
            reasoning: options.reasoning
        ) { [weak self] delta in
            await self?.appendRoundDelta(delta, to: assistantID)
        }
    }

    /// Runs one tool call. A failure is returned to the model as the tool's
    /// result, so it can recover or explain, instead of ending the whole
    /// reply; only cancellation (Stop) propagates.
    private func execute(_ call: ToolCall, assistantID: UUID) async throws -> String {
        let arguments: [String: Any]
        do {
            arguments = try decodeArguments(call.arguments)
        } catch {
            let activityID = appendToolActivity(to: assistantID, name: call.name, detail: call.name, status: .failed)
            updateToolActivity(activityID, in: assistantID, status: .failed, result: error.localizedDescription)
            return "Error: \(error.localizedDescription) Send the arguments as a JSON object."
        }
        let detail = toolDetail(name: call.name, arguments: arguments)
        let activityID = appendToolActivity(
            to: assistantID,
            name: call.name,
            detail: detail,
            status: requiresApproval(call.name) ? .waitingForApproval : .running
        )
        do {
            let output = try await executeApproved(call, arguments: arguments, activityID: activityID, assistantID: assistantID)
            if output != Self.rejectedOutput {
                updateToolActivity(activityID, in: assistantID, status: .completed, result: output)
            }
            return output
        } catch {
            if error is CancellationError || Task.isCancelled { throw CancellationError() }
            updateToolActivity(activityID, in: assistantID, status: .failed, result: error.localizedDescription)
            return "Error: \(error.localizedDescription)"
        }
    }

    private static let rejectedOutput = "User rejected the tool."

    /// Asks for approval when the Settings mode requires it for this tool.
    /// Returns false (and marks the activity rejected) when the user declines.
    private func approveIfNeeded(
        _ name: String,
        summary: String,
        activityID: UUID,
        assistantID: UUID
    ) async -> Bool {
        if requiresApproval(name) {
            guard await requestApproval(name: name, summary: summary) else {
                updateToolActivity(activityID, in: assistantID, status: .rejected, result: nil)
                return false
            }
        }
        updateToolActivity(activityID, in: assistantID, status: .running, result: nil)
        return true
    }

    private func executeApproved(
        _ call: ToolCall,
        arguments: [String: Any],
        activityID: UUID,
        assistantID: UUID
    ) async throws -> String {
        switch call.name {
        case "ask_user":
            guard let question = arguments["question"] as? String else {
                throw ValidationError("ask_user requires a question.")
            }
            pendingQuestion = String(question.prefix(1_000))
            return await withCheckedContinuation { questionContinuation = $0 }
        case "memory_list":
            guard MobileMemoryStore.isEnabled else { return Self.memoryOffOutput }
            return await MobileMemoryStore.shared.list()
        case "web_search":
            guard let query = arguments["query"] as? String else {
                throw ValidationError("web_search requires a query.")
            }
            guard await approveIfNeeded(call.name, summary: query, activityID: activityID, assistantID: assistantID) else {
                return Self.rejectedOutput
            }
            return try await SafeWebFetcher.search(query)
        case "web_fetch":
            guard let url = arguments["url"] as? String else {
                throw ValidationError("web_fetch requires a URL.")
            }
            guard await approveIfNeeded(call.name, summary: url, activityID: activityID, assistantID: assistantID) else {
                return Self.rejectedOutput
            }
            return try await SafeWebFetcher.fetch(url)
        case "memory_remember":
            guard MobileMemoryStore.isEnabled else { return Self.memoryOffOutput }
            guard let text = arguments["text"] as? String else {
                throw ValidationError("memory_remember requires text.")
            }
            guard await approveIfNeeded(call.name, summary: String(text.prefix(180)), activityID: activityID, assistantID: assistantID) else {
                return Self.rejectedOutput
            }
            return try await MobileMemoryStore.shared.remember(text)
        case "memory_forget":
            guard MobileMemoryStore.isEnabled else { return Self.memoryOffOutput }
            guard let id = arguments["id"] as? String else {
                throw ValidationError("memory_forget requires an identifier.")
            }
            guard await approveIfNeeded(call.name, summary: id, activityID: activityID, assistantID: assistantID) else {
                return Self.rejectedOutput
            }
            return await MobileMemoryStore.shared.forget(id)
        case "local_context":
            return MobileLocalContext.value()
        default:
            throw MobileChatError.unsupportedTool(call.name)
        }
    }

    private static let memoryOffOutput = "Memory is turned off in Settings."

    private func requiresApproval(_ name: String) -> Bool {
        ["web_search", "web_fetch", "memory_remember", "memory_forget"].contains(name)
            && MobileToolApprovalMode.current.requiresApproval(name)
    }

    private func toolDetail(name: String, arguments: [String: Any]) -> String {
        switch name {
        case "web_search": arguments["query"] as? String ?? "Search"
        case "web_fetch": arguments["url"] as? String ?? "URL"
        case "memory_remember": arguments["text"] as? String ?? "Memory"
        case "memory_forget": arguments["id"] as? String ?? "Memory"
        default: name.replacingOccurrences(of: "_", with: " ")
        }
    }

    private func appendToolActivity(
        to messageID: UUID,
        name: String,
        detail: String,
        status: MobileToolActivityStatus
    ) -> UUID {
        let activity = MobileToolActivity(name: name, detail: String(detail.prefix(500)), status: status)
        guard let index = messages.firstIndex(where: { $0.id == messageID }) else { return activity.id }
        messages[index].toolActivities.append(activity)
        return activity.id
    }

    private func updateToolActivity(
        _ activityID: UUID,
        in messageID: UUID,
        status: MobileToolActivityStatus,
        result: String?
    ) {
        guard let messageIndex = messages.firstIndex(where: { $0.id == messageID }),
              let activityIndex = messages[messageIndex].toolActivities.firstIndex(where: { $0.id == activityID }) else { return }
        messages[messageIndex].toolActivities[activityIndex].status = status
        messages[messageIndex].toolActivities[activityIndex].resultPreview = result.map { String($0.prefix(500)) }
    }

    private func requestApproval(name: String, summary: String) async -> Bool {
        pendingToolApproval = PendingToolApproval(id: UUID(), name: name, summary: summary)
        return await withCheckedContinuation { approvalContinuation = $0 }
    }

    private func decodeArguments(_ value: String) throws -> [String: Any] {
        guard let data = value.data(using: .utf8),
              let object = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw ValidationError("The tool supplied invalid arguments.")
        }
        return object
    }

    private func wireMessage(_ message: MobileChatMessage) -> [String: Any] {
        guard message.role == .user, !message.attachments.isEmpty else {
            return ["role": message.role.rawValue, "content": message.text]
        }
        var content: [[String: Any]] = []
        if !message.text.isEmpty {
            content.append(["type": "text", "text": message.text])
        }
        content.append(contentsOf: message.attachments.map { attachment in
            [
                "type": "image_url",
                "image_url": [
                    "url": "data:\(attachment.mediaType);base64,\(attachment.data.base64EncodedString())"
                ]
            ]
        })
        return ["role": message.role.rawValue, "content": content]
    }

    /// Set after a tool round, so the next round's text starts on a new
    /// paragraph instead of running into the previous round's last sentence.
    private var separateNextRound = false

    private func appendRoundDelta(_ delta: String, to id: UUID) {
        if separateNextRound {
            separateNextRound = false
            if message(id: id)?.text.isEmpty == false {
                appendDelta("\n\n", to: id)
            }
        }
        appendDelta(delta, to: id)
    }

    private func appendDelta(_ delta: String, to id: UUID) {
        guard let index = messages.firstIndex(where: { $0.id == id }) else { return }
        messages[index].text += delta
    }

    private func markStreamingFinished(id: UUID) {
        guard let index = messages.firstIndex(where: { $0.id == id }) else { return }
        messages[index].isStreaming = false
    }

    private func message(id: UUID) -> MobileChatMessage? {
        messages.first { $0.id == id }
    }

    private func removeMessage(id: UUID) {
        messages.removeAll { $0.id == id }
    }

    private func persistProfiles() {
        do {
            try providerStore.saveProfiles(profiles)
            providerStore.saveActiveID(activeProfileID)
        } catch {
            errorMessage = error.localizedDescription
        }
    }
}

private struct ValidationError: LocalizedError {
    let message: String

    init(_ message: String) {
        self.message = message
    }

    var errorDescription: String? { message }
}
