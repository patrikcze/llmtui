import Foundation

/// The composer's context ring: a rough estimate of how much of the model's
/// context window the next request uses, the same estimate as the macOS app
/// (characters / 4 plus a small per-message overhead).
///
/// The window size comes, in order, from the provider's "Context size"
/// setting, from the server (see `ContextWindowProbe`), or 8192.
enum ContextUsageEstimate {
    static let fallbackWindow = 8192

    /// Tokens for the open chat's history, the draft, and the system
    /// instructions and tool definitions sent with it.
    static func tokens(messages: [MobileChatMessage], draft: String, toolsEnabled: Bool, memoryEnabled: Bool, mcpDefinitions: [MCPJSON] = []) -> Int {
        let history = messages.reduce(0) { total, message in
            let toolText = message.toolActivities.reduce(0) { $0 + ($1.resultPreview?.count ?? 0) }
            return total + (message.text.count + toolText) / 4 + 8
        }
        return history + draft.count / 4 + overhead(toolsEnabled: toolsEnabled, memoryEnabled: memoryEnabled, mcpDefinitions: mcpDefinitions)
    }

    /// The system instructions (about 300 tokens) plus, with tools on, the
    /// tool instructions and definitions.
    static func overhead(toolsEnabled: Bool, memoryEnabled: Bool, mcpDefinitions: [MCPJSON] = []) -> Int {
        guard toolsEnabled else { return 300 }
        let definitions = MobileChatRuntime.toolDefinitions(memoryEnabled: memoryEnabled)
        let size = (try? JSONSerialization.data(withJSONObject: definitions).count) ?? 0
        let mcpSize = (try? MCPJSON.array(mcpDefinitions).data().count) ?? 0
        return 600 + size / 4 + (mcpDefinitions.isEmpty ? 0 : mcpSize / 4)
    }
}

/// Asks a provider's server for the context window of a model. Only the
/// server the provider is configured with is contacted, with the provider's
/// own API key, and a failure simply means "unknown".
///
/// - LM Studio: the loaded context length from `/api/v0/models`, else the
///   model's maximum.
/// - Ollama: the context length of the loaded model from `/api/ps`, else
///   `num_ctx` or the model's maximum from `/api/show`.
/// - OpenAI-compatible: `max_model_len`, `context_length` or
///   `max_context_length` in `/v1/models`, else `n_ctx` from llama.cpp's
///   `/props`.
struct ContextWindowProbe {
    var session: URLSession = .shared

    func contextWindow(profile: MobileProviderProfile, apiKey: String) async -> Int? {
        guard !profile.model.isEmpty, let base = serverRoot(profile.baseURL) else { return nil }
        switch profile.type {
        case .lmStudio:
            if let value = await lmStudio(base: base, model: profile.model, apiKey: apiKey) { return value }
            return await openAICompatible(base: base, model: profile.model, apiKey: apiKey)
        case .ollama:
            return await ollama(base: base, model: profile.model, apiKey: apiKey)
        case .openAICompatible:
            return await openAICompatible(base: base, model: profile.model, apiKey: apiKey)
        }
    }

    /// The server root: the base URL without a trailing "/v1".
    func serverRoot(_ baseURL: String) -> URL? {
        var text = baseURL.trimmingCharacters(in: .whitespacesAndNewlines)
        while text.hasSuffix("/") { text.removeLast() }
        if text.hasSuffix("/v1") { text.removeLast(3) }
        guard let url = URL(string: text), let scheme = url.scheme?.lowercased(),
              ["http", "https"].contains(scheme) else { return nil }
        return url
    }

    private func lmStudio(base: URL, model: String, apiKey: String) async -> Int? {
        guard let object = await json(base.appending(path: "api/v0/models"), apiKey: apiKey) as? [String: Any],
              let items = object["data"] as? [[String: Any]],
              let item = items.first(where: { $0["id"] as? String == model })
        else { return nil }
        return Self.int(item["loaded_context_length"]) ?? Self.int(item["max_context_length"])
    }

    private func ollama(base: URL, model: String, apiKey: String) async -> Int? {
        if let object = await json(base.appending(path: "api/ps"), apiKey: apiKey) as? [String: Any],
           let running = object["models"] as? [[String: Any]],
           let item = running.first(where: { ($0["name"] as? String) == model || ($0["model"] as? String) == model }),
           let value = Self.int(item["context_length"]) {
            return value
        }
        guard let show = await json(base.appending(path: "api/show"), apiKey: apiKey, body: ["model": model]) as? [String: Any]
        else { return nil }
        if let parameters = show["parameters"] as? String,
           let line = parameters.split(separator: "\n").first(where: { $0.trimmingCharacters(in: .whitespaces).hasPrefix("num_ctx") }),
           let value = line.split(separator: " ", omittingEmptySubsequences: true).last.flatMap({ Int($0) }) {
            return value
        }
        if let info = show["model_info"] as? [String: Any],
           let key = info.keys.first(where: { $0.hasSuffix(".context_length") }) {
            return Self.int(info[key])
        }
        return nil
    }

    private func openAICompatible(base: URL, model: String, apiKey: String) async -> Int? {
        if let object = await json(base.appending(path: "v1/models"), apiKey: apiKey) as? [String: Any],
           let items = object["data"] as? [[String: Any]],
           let item = items.first(where: { $0["id"] as? String == model }) {
            for key in ["max_model_len", "context_length", "max_context_length", "loaded_context_length"] {
                if let value = Self.int(item[key]) { return value }
            }
        }
        if let props = await json(base.appending(path: "props"), apiKey: apiKey) as? [String: Any],
           let settings = props["default_generation_settings"] as? [String: Any],
           let value = Self.int(settings["n_ctx"]) {
            return value
        }
        return nil
    }

    private func json(_ url: URL, apiKey: String, body: [String: Any]? = nil) async -> Any? {
        var request = URLRequest(url: url)
        request.timeoutInterval = 10
        let key = apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if !key.isEmpty { request.setValue("Bearer \(key)", forHTTPHeaderField: "Authorization") }
        if let body {
            request.httpMethod = "POST"
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
            request.httpBody = try? JSONSerialization.data(withJSONObject: body)
        }
        guard let (data, response) = try? await session.data(for: request),
              let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode)
        else { return nil }
        return try? JSONSerialization.jsonObject(with: data)
    }

    /// A positive integer from a JSON number.
    static func int(_ value: Any?) -> Int? {
        guard let number = value as? NSNumber, number.intValue > 0 else { return nil }
        return number.intValue
    }
}
