import Foundation

enum MobileProviderType: String, Codable, CaseIterable, Identifiable {
    case ollama
    case lmStudio
    case openAICompatible

    var id: String { rawValue }

    var title: String {
        switch self {
        case .ollama: "Ollama"
        case .lmStudio: "LM Studio"
        case .openAICompatible: "OpenAI-compatible"
        }
    }

    var defaultBaseURL: String {
        switch self {
        case .ollama: "http://192.168.1.2:11434"
        case .lmStudio: "http://192.168.1.2:1234/v1"
        case .openAICompatible: "https://api.openai.com/v1"
        }
    }
}

struct MobileProviderProfile: Codable, Identifiable, Equatable, Sendable {
    var id: UUID
    var name: String
    var type: MobileProviderType
    var baseURL: String
    var model: String

    init(
        id: UUID = UUID(),
        name: String = "Local provider",
        type: MobileProviderType = .ollama,
        baseURL: String = MobileProviderType.ollama.defaultBaseURL,
        model: String = ""
    ) {
        self.id = id
        self.name = name
        self.type = type
        self.baseURL = baseURL
        self.model = model
    }
}

enum ChatRole: String, Codable, Sendable {
    case system
    case user
    case assistant
    case tool
}

struct MobileChatMessage: Codable, Identifiable, Equatable, Sendable {
    var id: UUID
    var role: ChatRole
    var text: String
    var createdAt: Date
    var isStreaming: Bool
    var attachments: [MobileAttachment]
    var toolActivities: [MobileToolActivity]

    init(
        id: UUID = UUID(),
        role: ChatRole,
        text: String,
        createdAt: Date = .now,
        isStreaming: Bool = false,
        attachments: [MobileAttachment] = [],
        toolActivities: [MobileToolActivity] = []
    ) {
        self.id = id
        self.role = role
        self.text = text
        self.createdAt = createdAt
        self.isStreaming = isStreaming
        self.attachments = attachments
        self.toolActivities = toolActivities
    }
}

enum MobileToolActivityStatus: String, Codable, Sendable {
    case waitingForApproval
    case running
    case completed
    case failed
    case rejected
}

struct MobileToolActivity: Codable, Identifiable, Equatable, Sendable {
    let id: UUID
    let name: String
    let detail: String
    var status: MobileToolActivityStatus
    var resultPreview: String?

    init(
        id: UUID = UUID(),
        name: String,
        detail: String,
        status: MobileToolActivityStatus,
        resultPreview: String? = nil
    ) {
        self.id = id
        self.name = name
        self.detail = detail
        self.status = status
        self.resultPreview = resultPreview
    }
}

struct MobileAttachment: Codable, Identifiable, Equatable, Sendable {
    let id: UUID
    let data: Data
    let mediaType: String

    init(id: UUID = UUID(), data: Data, mediaType: String = "image/jpeg") {
        self.id = id
        self.data = data
        self.mediaType = mediaType
    }
}

enum MobileReasoning: String, CaseIterable, Identifiable, Sendable {
    case automatic
    case low
    case medium
    case high

    var id: String { rawValue }

    var title: String {
        switch self {
        case .automatic: "Automatic"
        case .low: "Low"
        case .medium: "Medium"
        case .high: "High"
        }
    }
}

struct ToolCall: Sendable, Equatable {
    let id: String
    let name: String
    let arguments: String
}

struct ChatTurnResult: Sendable {
    let text: String
    let toolCalls: [ToolCall]
}

struct PendingToolApproval: Identifiable, Equatable {
    let id: UUID
    let name: String
    let summary: String
}

enum MobileChatError: LocalizedError {
    case invalidBaseURL
    case noProvider
    case noModel
    case invalidResponse
    case server(Int, String)
    case unsupportedTool(String)
    case unsafeURL
    case responseTooLarge

    var errorDescription: String? {
        switch self {
        case .invalidBaseURL: "Enter a valid HTTP or HTTPS provider URL."
        case .noProvider: "Configure a provider before starting a chat."
        case .noModel: "Select or enter a model before starting a chat."
        case .invalidResponse: "The provider returned an unreadable response."
        case .server(let status, let detail): "Provider error \(status): \(detail)"
        case .unsupportedTool(let name): "The model requested unsupported tool “\(name)”."
        case .unsafeURL: "The web tool only accepts public HTTP or HTTPS URLs."
        case .responseTooLarge: "The web response exceeded the 1 MB safety limit."
        }
    }
}
