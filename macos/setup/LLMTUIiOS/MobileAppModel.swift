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
    var errorMessage: String?
    var hasRuntimeProblem = false

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

    func send() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || !draftAttachments.isEmpty, !isGenerating else { return }
        guard let profile = activeProfile else {
            errorMessage = MobileChatError.noProvider.localizedDescription
            return
        }
        guard !profile.model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else {
            errorMessage = MobileChatError.noModel.localizedDescription
            return
        }

        let attachments = draftAttachments
        draft = ""
        draftAttachments = []
        messages.append(MobileChatMessage(role: .user, text: text, attachments: attachments))
        let assistantID = UUID()
        messages.append(MobileChatMessage(id: assistantID, role: .assistant, text: "", isStreaming: true))
        isGenerating = true
        hasRuntimeProblem = false

        let history = messages.filter { $0.id != assistantID }
        generationTask = Task {
            defer {
                isGenerating = false
                markStreamingFinished(id: assistantID)
                pendingToolApproval = nil
                pendingQuestion = nil
            }
            do {
                try await runToolLoop(
                    profile: profile,
                    apiKey: apiKey(for: profile),
                    initialHistory: history,
                    assistantID: assistantID
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
        }
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
        assistantID: UUID
    ) async throws {
        var wireMessages = initialHistory.map(wireMessage)
        var systemInstructions = """
        Format answers for a narrow mobile display using concise paragraphs and valid Markdown. Put blank lines between paragraphs, headings, and lists. Use fenced code blocks for code. For Mermaid, emit a fenced block beginning with ```mermaid and valid Mermaid syntax. Quote every human-readable node or edge label containing whitespace or punctuation, for example A["Validate Input (Required)"] and A -->|"Valid"| B. Mermaid comments must use %% on their own line. For mathematics, emit only KaTeX-compatible expressions using $...$ or \\(...\\) inline and $$...$$ or \\[...\\] for display math. Never emit a complete LaTeX document, preamble, or text-layout environment such as document, itemize, enumerate, verbatim, table, or figure.
        """
        if agentEnabled {
            systemInstructions += """

            You are in bounded agent mode. Work toward the user's goal using available tools, inspect tool results before continuing, ask when an important choice is missing, and stop after completing the task. Never claim a tool ran unless its result is present.
            """
        }
        wireMessages.insert(["role": "system", "content": systemInstructions], at: 0)
        let maximumRounds = agentEnabled ? 10 : 4
        for _ in 0..<maximumRounds {
            let result = try await runtime.streamTurn(
                profile: profile,
                apiKey: apiKey,
                messages: wireMessages,
                toolsEnabled: toolsEnabled || agentEnabled,
                reasoning: reasoning
            ) { [weak self] delta in
                await self?.appendDelta(delta, to: assistantID)
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
        }
        appendDelta("\n\nAgent/tool round limit reached.", to: assistantID)
    }

    private func execute(_ call: ToolCall, assistantID: UUID) async throws -> String {
        let arguments = try decodeArguments(call.arguments)
        let detail = toolDetail(name: call.name, arguments: arguments)
        let activityID = appendToolActivity(
            to: assistantID,
            name: call.name,
            detail: detail,
            status: requiresApproval(call.name) ? .waitingForApproval : .running
        )
        do {
            let output = try await executeApproved(call, arguments: arguments, activityID: activityID, assistantID: assistantID)
            if output != "User rejected the tool." {
                updateToolActivity(activityID, in: assistantID, status: .completed, result: output)
            }
            return output
        } catch {
            updateToolActivity(activityID, in: assistantID, status: .failed, result: error.localizedDescription)
            throw error
        }
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
            return await MobileMemoryStore.shared.list()
        case "web_search":
            guard let query = arguments["query"] as? String else {
                throw ValidationError("web_search requires a query.")
            }
            guard await requestApproval(name: call.name, summary: query) else {
                updateToolActivity(activityID, in: assistantID, status: .rejected, result: nil)
                return "User rejected the tool."
            }
            updateToolActivity(activityID, in: assistantID, status: .running, result: nil)
            return try await SafeWebFetcher.search(query)
        case "web_fetch":
            guard let url = arguments["url"] as? String else {
                throw ValidationError("web_fetch requires a URL.")
            }
            guard await requestApproval(name: call.name, summary: url) else {
                updateToolActivity(activityID, in: assistantID, status: .rejected, result: nil)
                return "User rejected the tool."
            }
            updateToolActivity(activityID, in: assistantID, status: .running, result: nil)
            return try await SafeWebFetcher.fetch(url)
        case "memory_remember":
            guard let text = arguments["text"] as? String else {
                throw ValidationError("memory_remember requires text.")
            }
            guard await requestApproval(name: call.name, summary: String(text.prefix(180))) else {
                updateToolActivity(activityID, in: assistantID, status: .rejected, result: nil)
                return "User rejected the tool."
            }
            updateToolActivity(activityID, in: assistantID, status: .running, result: nil)
            return try await MobileMemoryStore.shared.remember(text)
        case "memory_forget":
            guard let id = arguments["id"] as? String else {
                throw ValidationError("memory_forget requires an identifier.")
            }
            guard await requestApproval(name: call.name, summary: id) else {
                updateToolActivity(activityID, in: assistantID, status: .rejected, result: nil)
                return "User rejected the tool."
            }
            updateToolActivity(activityID, in: assistantID, status: .running, result: nil)
            return await MobileMemoryStore.shared.forget(id)
        case "local_context":
            return MobileLocalContext.value()
        default:
            throw MobileChatError.unsupportedTool(call.name)
        }
    }

    private func requiresApproval(_ name: String) -> Bool {
        ["web_search", "web_fetch", "memory_remember", "memory_forget"].contains(name)
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
