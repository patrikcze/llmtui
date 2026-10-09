import Foundation

struct MobileChatRuntime: Sendable {
    var session: URLSession = .shared
    private struct StreamChoice: Decodable {
        struct Delta: Decodable {
            struct StreamToolCall: Decodable {
                struct Function: Decodable {
                    let name: String?
                    let arguments: String?
                }

                let index: Int
                let id: String?
                let function: Function?
            }

            let content: String?
            let toolCalls: [StreamToolCall]?

            enum CodingKeys: String, CodingKey {
                case content
                case toolCalls = "tool_calls"
            }
        }

        let delta: Delta
        let finishReason: String?
        enum CodingKeys: String, CodingKey { case delta; case finishReason = "finish_reason" }
    }

    private struct StreamChunk: Decodable {
        let choices: [StreamChoice]
    }

    private struct ToolAccumulator {
        var id = ""
        var name = ""
        var arguments = ""
    }

    func discoverModels(profile: MobileProviderProfile, apiKey: String) async throws -> [String] {
        let endpoint = try endpointURL(profile: profile, path: "models")
        var request = URLRequest(url: endpoint)
        request.timeoutInterval = 15
        applyAuthorization(apiKey, to: &request)
        let (data, response) = try await session.data(for: request)
        try validate(response: response, data: data)

        let object = try JSONSerialization.jsonObject(with: data)
        if let dictionary = object as? [String: Any],
           let items = dictionary["data"] as? [[String: Any]] {
            return items.compactMap { $0["id"] as? String }.sorted()
        }
        if let dictionary = object as? [String: Any],
           let items = dictionary["models"] as? [[String: Any]] {
            return items.compactMap { ($0["name"] as? String) ?? ($0["model"] as? String) }.sorted()
        }
        throw MobileChatError.invalidResponse
    }

    func streamTurn(
        profile: MobileProviderProfile,
        apiKey: String,
        messages: [[String: Any]],
        tools: [[String: Any]]?,
        reasoning: MobileReasoning,
        onDelta: @escaping @Sendable (String) async -> Void
    ) async throws -> ChatTurnResult {
        let endpoint = try endpointURL(profile: profile, path: "chat/completions")
        var body: [String: Any] = [
            "model": profile.model,
            "messages": messages,
            "stream": true
        ]
        if let tools, !tools.isEmpty {
            body["tools"] = tools
            body["tool_choice"] = "auto"
        }
        if reasoning != .automatic {
            body["reasoning_effort"] = reasoning.rawValue
        }

        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        request.timeoutInterval = 120
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        applyAuthorization(apiKey, to: &request)
        request.httpBody = try JSONSerialization.data(withJSONObject: body)

        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse else { throw MobileChatError.invalidResponse }
        guard (200..<300).contains(http.statusCode) else {
            var errorData = Data()
            for try await byte in bytes.prefix(32_000) { errorData.append(byte) }
            throw MobileChatError.server(http.statusCode, String(decoding: errorData, as: UTF8.self))
        }

        var fullText = ""
        var accumulators: [Int: ToolAccumulator] = [:]
        var finished = false
        var finishReason: String?
        for try await line in bytes.lines {
            try Task.checkCancellation()
            guard line.hasPrefix("data:") else { continue }
            let payload = line.dropFirst(5).trimmingCharacters(in: .whitespaces)
            if payload == "[DONE]" { finished = true; break }
            guard let data = payload.data(using: .utf8),
                  let chunk = try? JSONDecoder().decode(StreamChunk.self, from: data) else { throw MobileChatError.invalidResponse }
            for choice in chunk.choices {
                if let reason = choice.finishReason { finishReason = reason }
                if let content = choice.delta.content, !content.isEmpty {
                    fullText += content
                    await onDelta(content)
                }
                for call in choice.delta.toolCalls ?? [] {
                    var value = accumulators[call.index] ?? ToolAccumulator()
                    if let id = call.id { value.id += id }
                    if let name = call.function?.name { value.name += name }
                    if let arguments = call.function?.arguments { value.arguments += arguments }
                    accumulators[call.index] = value
                }
            }
        }

        guard finished || finishReason != nil else { throw MobileChatError.invalidResponse }
        if !accumulators.isEmpty {
            guard ["tool_calls", "stop"].contains(finishReason ?? "") else { throw MobileChatError.invalidResponse }
            // A call needs a name and complete JSON-object arguments. An empty
            // arguments string is a call without arguments ("{}"), and a
            // missing id is generated below: some servers send neither, and
            // both are complete calls, unlike a truncated stream.
            for call in accumulators.values {
                let arguments = call.arguments.isEmpty ? "{}" : call.arguments
                guard !call.name.isEmpty,
                      let data = arguments.data(using: .utf8),
                      (try? JSONSerialization.jsonObject(with: data)) is [String: Any] else { throw MobileChatError.invalidResponse }
            }
        }
        let calls = accumulators.keys.sorted().compactMap { index -> ToolCall? in
            guard let value = accumulators[index], !value.name.isEmpty else { return nil }
            return ToolCall(
                id: value.id.isEmpty ? UUID().uuidString : value.id,
                name: value.name,
                arguments: value.arguments.isEmpty ? "{}" : value.arguments
            )
        }
        return ChatTurnResult(text: fullText, toolCalls: calls)
    }

    private func endpointURL(profile: MobileProviderProfile, path: String) throws -> URL {
        guard var url = URL(string: profile.baseURL.trimmingCharacters(in: .whitespacesAndNewlines)),
              let scheme = url.scheme?.lowercased(),
              ["http", "https"].contains(scheme) else { throw MobileChatError.invalidBaseURL }

        if profile.type == .ollama {
            if path == "models" {
                url.append(path: "api/tags")
            } else {
                url.append(path: "v1")
                url.append(path: path)
            }
        } else {
            let normalizedPath = url.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
            if !normalizedPath.hasSuffix("v1") { url.append(path: "v1") }
            url.append(path: path)
        }
        return url
    }

    private func applyAuthorization(_ apiKey: String, to request: inout URLRequest) {
        let trimmed = apiKey.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty {
            request.setValue("Bearer \(trimmed)", forHTTPHeaderField: "Authorization")
        }
    }

    private func validate(response: URLResponse, data: Data) throws {
        guard let http = response as? HTTPURLResponse else { throw MobileChatError.invalidResponse }
        guard (200..<300).contains(http.statusCode) else {
            throw MobileChatError.server(http.statusCode, String(decoding: data.prefix(32_000), as: UTF8.self))
        }
    }

    static let toolDefinitions: [[String: Any]] = [
        definition(
            name: "ask_user",
            description: "Ask the user one concise clarifying question.",
            properties: ["question": stringProperty("Question to show the user.")],
            required: ["question"]
        ),
        definition(
            name: "web_research",
            description: "Research a question on the public web: runs up to 3 searches, reads the best few pages from different sites, and returns numbered notes with only the relevant passages, to cite as [1], [2]. Prefer this over web_search + web_fetch for facts, current events, prices, schedules, or anything that may have changed.",
            properties: [
                "question": stringProperty("The question to answer, in full."),
                "queries": stringArrayProperty("1-3 distinct search queries, for example different phrasings, the official source, or a recent-news angle. Defaults to the question."),
                "max_sources": integerProperty("How many pages to read, 1-6. Defaults to 4.")
            ],
            required: ["question"]
        ),
        definition(
            name: "web_search",
            description: "Search the public web and list result titles, URLs and snippets. Use it to find a specific page or site; for answering a question, use web_research.",
            properties: ["query": stringProperty("Search query.")],
            required: ["query"]
        ),
        definition(
            name: "web_fetch",
            description: "Read text from one public HTTP or HTTPS URL. Give a focus to get only the passages about it instead of the start of the page.",
            properties: [
                "url": stringProperty("Public URL to fetch."),
                "focus": stringProperty("Optional: what you are looking for on the page.")
            ],
            required: ["url"]
        ),
        definition(
            name: "memory_search",
            description: "Search all saved memories by keywords and return matches with their ids. The system prompt shows only the newest memories; use this before asking the user or searching the web for something they may have told you before.",
            properties: ["query": stringProperty("Keywords to look for.")],
            required: ["query"]
        ),
        definition(
            name: "memory_list",
            description: "List every saved memory with its id. Prefer memory_search; use this to get an id for memory_forget.",
            properties: [:],
            required: []
        ),
        definition(
            name: "memory_remember",
            description: "Save one short, durable fact or preference about the user for future chats, for example \"Prefers metric units\" or \"Lives in Prague\". Call it when the user asks you to remember something, and on your own when they share something that will matter in later chats. Never save secrets, passwords, API keys, health or financial details, or one-off task details. The user may be asked to approve it.",
            properties: ["text": stringProperty("One short fact, written in the third person.")],
            required: ["text"]
        ),
        definition(
            name: "memory_forget",
            description: "Delete one saved memory when the user asks you to forget it, or when it is clearly outdated. Use the id from memory_search or memory_list. The user may be asked to approve it.",
            properties: ["id": stringProperty("The memory id from memory_search or memory_list.")],
            required: ["id"]
        ),
        definition(
            name: "local_context",
            description: "Return non-sensitive local iPhone or iPad context: current local and UTC date/time, timezone, locale, OS, device class, architecture, processor count, memory, storage, uptime, thermal state, and Low Power Mode. Use this before answering questions about the current date, time, timezone, or device capabilities.",
            properties: [:],
            required: []
        )
    ]

    /// Tools over the current chat's attachments, offered only when it has any.
    /// Arguments are opaque ids from document_list and document_search, never
    /// paths or URLs.
    static let documentToolDefinitions: [[String: Any]] = [
        definition(
            name: "document_list",
            description: "List the files attached to this chat: id, name, type, pages or lines, extraction status and warnings.",
            properties: [:],
            required: []
        ),
        definition(
            name: "document_search",
            description: "Search the extracted text of the attached files and return the best matching passages with their document_id, chunk_id and page or lines. Search before reading. Omit document_id to search every attachment. The result says how much of each file was searched.",
            properties: [
                "query": stringProperty("Words to look for, for example \"authentication token expiry\"."),
                "document_id": stringProperty("Optional: one attachment id from document_list, like \"d4k9x2\"."),
                "limit": integerProperty("How many matches to return, 1-10. Defaults to 5.")
            ],
            required: ["query"]
        ),
        definition(
            name: "document_read",
            description: "Read the text of one passage (chunk) found by document_search. Text is paged by character offset: offset 0 is the start of the chunk, at most limit characters are returned, and a truncated result gives the next offset to continue from.",
            properties: [
                "document_id": stringProperty("The attachment id, like \"d4k9x2\"."),
                "chunk_id": stringProperty("The chunk id from document_search, like \"c7\"."),
                "offset": integerProperty("Character offset in the chunk, starting at 0. Defaults to 0."),
                "limit": integerProperty("Characters to return, 1-4000. Defaults to 1500.")
            ],
            required: ["document_id", "chunk_id"]
        )
    ]

    /// The tools offered to the model; the memory tools only while memory is on.
    static func toolDefinitions(memoryEnabled: Bool) -> [[String: Any]] {
        guard !memoryEnabled else { return toolDefinitions }
        return toolDefinitions.filter { definition in
            let name = (definition["function"] as? [String: Any])?["name"] as? String ?? ""
            return !name.hasPrefix("memory_")
        }
    }

    private static func definition(
        name: String,
        description: String,
        properties: [String: Any],
        required: [String]
    ) -> [String: Any] {
        [
            "type": "function",
            "function": [
                "name": name,
                "description": description,
                "parameters": [
                    "type": "object",
                    "properties": properties,
                    "required": required,
                    "additionalProperties": false
                ] as [String: Any]
            ] as [String: Any]
        ]
    }

    private static func stringProperty(_ description: String) -> [String: Any] {
        ["type": "string", "description": description]
    }

    private static func stringArrayProperty(_ description: String) -> [String: Any] {
        ["type": "array", "items": ["type": "string"], "description": description]
    }

    private static func integerProperty(_ description: String) -> [String: Any] {
        ["type": "integer", "description": description]
    }
}
