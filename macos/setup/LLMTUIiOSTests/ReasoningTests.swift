import Foundation
import Testing
@testable import LLMTUIiOS

/// The reasoning (brain) choice: what each choice sends for the model
/// families whose templates disagree, and how streamed reasoning is kept
/// apart from the answer.
struct ReasoningRequestTests {
    private func fields(
        _ reasoning: MobileReasoning,
        _ model: String,
        _ type: MobileProviderType = .openAICompatible,
        capability: ReasoningCapability? = nil,
        url: String = "http://192.168.1.2:8000/v1"
    ) -> [String: Any] {
        ReasoningRequest.fields(
            for: reasoning,
            capability: capability ?? .family(model: model),
            model: model,
            providerType: type,
            baseURL: url
        )
    }

    private func kwargs(_ fields: [String: Any]) -> [String: Any]? {
        fields["chat_template_kwargs"] as? [String: Any]
    }

    @Test func automaticSendsNothing() {
        for type in MobileProviderType.allCases {
            #expect(fields(.automatic, "google/gemma-4-e4b", type).isEmpty)
            #expect(fields(.automatic, "openai/gpt-oss-20b", type).isEmpty)
        }
    }

    // MARK: LM Studio (measured on 0.4: "none" is off, kwargs are ignored)

    /// The reported bug: Off on Gemma 4 E4B in LM Studio sent only
    /// chat_template_kwargs, which LM Studio ignores, so it kept thinking.
    @Test func lmStudioOffIsReasoningEffortNone() {
        let toggle = ReasoningCapability.toggle(defaultOn: true)
        let off = fields(.off, "google/gemma-4-e4b", .lmStudio, capability: toggle)
        #expect(off["reasoning_effort"] as? String == "none")
        #expect(kwargs(off) == nil)
    }

    @Test func lmStudioOnAndLevelsOnAnOnOffModel() {
        // On by default: On needs nothing.
        #expect(fields(.on, "google/gemma-4-e4b", .lmStudio, capability: .toggle(defaultOn: true)).isEmpty)
        // Off by default: a level name turns it on ("on" itself is a 400).
        #expect(fields(.on, "x", .lmStudio, capability: .toggle(defaultOn: false))["reasoning_effort"] as? String == "medium")
        // A level on an on/off model resolves to On.
        #expect(fields(.high, "qwen3.8-27b", .lmStudio, capability: .toggle(defaultOn: true)).isEmpty)
        for value in [MobileReasoning.on, .off, .low, .medium, .high] {
            let sent = fields(value, "x", .lmStudio, capability: .toggle(defaultOn: false))["reasoning_effort"] as? String
            #expect(sent != "on" && sent != "off", "\(value)")
        }
    }

    @Test func lmStudioLevelsAreSentByName() {
        let levels = ReasoningCapability.levels(canDisable: false)
        #expect(fields(.high, "openai/gpt-oss-20b", .lmStudio, capability: levels)["reasoning_effort"] as? String == "high")
        #expect(fields(.off, "openai/gpt-oss-20b", .lmStudio, capability: levels)["reasoning_effort"] as? String == "low")
        #expect(fields(.on, "openai/gpt-oss-20b", .lmStudio, capability: levels)["reasoning_effort"] as? String == "medium")
    }

    // MARK: Other servers

    /// Gemma 4's template treats a missing enable_thinking as off.
    @Test func openAICompatibleSwitchesThroughTheTemplate() {
        #expect(kwargs(fields(.on, "google/gemma-4-31b"))?["enable_thinking"] as? Bool == true)
        #expect(kwargs(fields(.off, "google/gemma-4-31b"))?["enable_thinking"] as? Bool == false)
        let medium = fields(.medium, "google/gemma-4-31b")
        #expect(kwargs(medium)?["enable_thinking"] as? Bool == true)
        #expect(medium["reasoning_effort"] as? String == "medium")
        #expect(kwargs(medium)?["reasoning_effort"] == nil)
    }

    /// Qwen 3.8's template accepts low, medium and xhigh, and raises an error
    /// for "high".
    @Test func qwen38HighIsSentAsXHigh() {
        for model in ["qwen/qwen3.8-27b", "Qwen3.8-27B-GGUF", "qwen3.8:27b", "mlx-community/Qwen3_8-27B-4bit", "qwen-3.8"] {
            let high = fields(.high, model)
            #expect(high["reasoning_effort"] as? String == "xhigh", "\(model)")
            #expect(kwargs(high)?["reasoning_effort"] as? String == "xhigh", "\(model)")
            #expect(kwargs(fields(.off, model))?["enable_thinking"] as? Bool == false, "\(model)")
        }
        #expect(ReasoningCapability.family(model: "qwen3.80-test") == .unknown)
        #expect(fields(.high, "qwen3-32b")["reasoning_effort"] as? String == "high")
    }

    @Test func ollamaGetsReasoningEffortOnly() {
        let url = "http://192.168.1.2:11434"
        #expect(fields(.off, "gemma4:e4b", .ollama, url: url)["reasoning_effort"] as? String == "none")
        #expect(fields(.on, "gemma4:e4b", .ollama, url: url)["reasoning_effort"] as? String == "medium")
        #expect(fields(.high, "qwen3.8:27b", .ollama, url: url)["reasoning_effort"] as? String == "xhigh")
        #expect(kwargs(fields(.high, "qwen3.8:27b", .ollama, url: url)) == nil)
        // GPT-OSS cannot stop reasoning: Off is Low.
        #expect(fields(.off, "gpt-oss:20b", .ollama, url: url)["reasoning_effort"] as? String == "low")
    }

    @Test func openAIAndGPTOSSNeverGetTemplateKwargs() {
        let openAI = "https://api.openai.com/v1"
        #expect(fields(.off, "gpt-5", url: openAI).isEmpty)
        #expect(fields(.on, "gpt-5", url: openAI)["reasoning_effort"] as? String == "medium")
        #expect(kwargs(fields(.high, "gpt-5", url: openAI)) == nil)
        #expect(fields(.off, "openai/gpt-oss-20b")["reasoning_effort"] as? String == "low")
        #expect(kwargs(fields(.on, "openai/gpt-oss-20b")) == nil)
    }
}

struct ReasoningCapabilityTests {
    @Test func offeredChoicesAndResolution() {
        let toggle = ReasoningCapability.toggle(defaultOn: true)
        #expect(toggle.offered == [.automatic, .off, .on])
        #expect(toggle.resolve(.high) == .on)
        #expect(toggle.resolve(.off) == .off)

        let gptOSS = ReasoningCapability.levels(canDisable: false)
        #expect(!gptOSS.offered.contains(.off))
        #expect(gptOSS.resolve(.off) == .low)
        #expect(gptOSS.resolve(.on) == .medium)
        #expect(gptOSS.resolve(.high) == .high)

        #expect(ReasoningCapability.unsupported.offered == [.automatic])
        #expect(ReasoningCapability.unsupported.resolve(.high) == .automatic)
        #expect(ReasoningCapability.unknown.offered == MobileReasoning.allCases)
        #expect(ReasoningCapability.unknown.resolve(.off) == .off)
        for capability in [ReasoningCapability.unknown, .unsupported, toggle, gptOSS] {
            #expect(capability.resolve(.automatic) == .automatic)
        }
    }

    @Test func lmStudioReportIsParsed() throws {
        let json = #"""
        {"models":[
          {"type":"llm","key":"google/gemma-4-e4b","loaded_instances":[{"id":"google/gemma-4-e4b"}],
           "capabilities":{"reasoning":{"allowed_options":["off","on"],"default":"on"}}},
          {"type":"llm","key":"openai/gpt-oss-20b","loaded_instances":[],
           "capabilities":{"reasoning":{"allowed_options":["low","medium","high"],"default":"medium"}}},
          {"type":"llm","key":"qwen/qwen3.8-27b-mlx","loaded_instances":[],"capabilities":{"vision":false}},
          {"type":"embedding","key":"text-embedding-nomic-embed-text-v1.5"}
        ]}
        """#
        let data = Data(json.utf8)
        #expect(ReasoningProbe.parse(data, model: "google/gemma-4-e4b") == .toggle(defaultOn: true))
        #expect(ReasoningProbe.parse(data, model: "gpt-oss-20b") == .levels(canDisable: false))
        // No reasoning configuration exposed: LM Studio offers no control.
        #expect(ReasoningProbe.parse(data, model: "qwen/qwen3.8-27b-mlx") == .unsupported)
        #expect(ReasoningProbe.parse(data, model: "missing") == nil)
        #expect(ReasoningCapability.lmStudio(allowedOptions: ["off", "low", "high"], defaultOption: "low") == .levels(canDisable: true))
    }

    @Test func iconShowsTheSettingInEffect() {
        #expect(MobileReasoning.automatic.badge == nil)
        #expect(!MobileReasoning.off.isThinking)
        #expect(MobileReasoning.off.badge == "Off")
        #expect(MobileReasoning.on.isThinking)
        #expect(MobileReasoning.high.badge == "H")
    }
}

struct ReasoningSplitterTests {
    private func split(_ chunks: [String]) -> (answer: String, reasoning: String) {
        var splitter = ReasoningSplitter()
        var answer = ""
        var reasoning = ""
        for chunk in chunks {
            let part = splitter.feed(chunk)
            answer += part.answer
            reasoning += part.reasoning
        }
        let rest = splitter.finish()
        return (answer + rest.answer, reasoning + rest.reasoning)
    }

    @Test func leadingThinkBlockIsReasoningEvenAcrossSplitTags() {
        let result = split(["  <th", "ink>Let me ", "check.</thi", "nk>\n\nThe answer is 4."])
        #expect(result.reasoning == "Let me check.")
        #expect(result.answer == "The answer is 4.")
    }

    @Test func gemmaThoughtChannelIsReasoning() {
        let result = split(["<|channel>thought\nWeigh options", "<chan", "nel|>Pick B."])
        #expect(result.reasoning == "\nWeigh options")
        #expect(result.answer == "Pick B.")
        // Thinking off on the larger Gemma 4 models: an empty thought block.
        #expect(split(["<|channel>thought\n<channel|>Done."]) == ("Done.", "\n"))
    }

    @Test func tagsLaterInTheAnswerAreLeftAlone() {
        let text = "Use <think> tags like this: <think>x</think>."
        #expect(split([text]) == (text, ""))
        #expect(split(["<", "b>bold</b>"]) == ("<b>bold</b>", ""))
    }

    @Test func unclosedBlockStaysReasoningAndShortTextIsKept() {
        #expect(split(["<think>still going <"]) == ("", "still going <"))
        #expect(split(["<thi"]) == ("<thi", ""))
    }
}

nonisolated final class ReasoningStreamFixture: @unchecked Sendable {
    static let shared = ReasoningStreamFixture()
    private let lock = NSLock()
    private var payload = Data()
    private var bodies: [Data] = []
    func set(_ text: String) { lock.lock(); defer { lock.unlock() }; payload = Data(text.utf8); bodies = [] }
    func lastBody() -> Data? { lock.lock(); defer { lock.unlock() }; return bodies.last }
    func respond(to request: URLRequest) -> Data {
        var body = request.httpBody ?? Data()
        if body.isEmpty, let stream = request.httpBodyStream {
            stream.open()
            defer { stream.close() }
            var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable {
                let count = stream.read(&buffer, maxLength: buffer.count)
                if count <= 0 { break }
                body.append(buffer, count: count)
            }
        }
        lock.lock(); defer { lock.unlock() }
        bodies.append(body)
        return payload
    }
}

nonisolated final class ReasoningStreamURLProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let data = ReasoningStreamFixture.shared.respond(to: request)
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "text/event-stream"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: data)
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

nonisolated final class Collected: @unchecked Sendable {
    private let lock = NSLock()
    private var parts: [String] = []
    func add(_ part: String) { lock.lock(); parts.append(part); lock.unlock() }
    var text: String { lock.lock(); defer { lock.unlock() }; return parts.joined() }
}

@MainActor @Suite(.serialized) struct ReasoningStreamTests {
    private func turn(_ stream: String, model: String = "test", type: MobileProviderType = .lmStudio, reasoning: MobileReasoning = .automatic, capability: ReasoningCapability = .unknown) async throws -> (ChatTurnResult, answer: String, reasoning: String) {
        ReasoningStreamFixture.shared.set(stream)
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ReasoningStreamURLProtocol.self]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        let answer = Collected()
        let thoughts = Collected()
        let result = try await MobileChatRuntime(session: session).streamTurn(
            profile: MobileProviderProfile(type: type, baseURL: "https://stream.fixture/v1", model: model),
            apiKey: "",
            messages: [],
            tools: nil,
            reasoning: reasoning,
            reasoningCapability: capability,
            onReasoning: { thoughts.add($0) }
        ) { answer.add($0) }
        return (result, answer.text, thoughts.text)
    }

    private func line(_ delta: String) -> String { "data: {\"choices\":[{\"delta\":\(delta)}]}\n\n" }
    private let done = "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"

    @Test func reasoningFieldsAreKeptOutOfTheAnswer() async throws {
        let stream = line(#"{"reasoning":"First, "}"#) + line(#"{"reasoning_content":"then."}"#) + line(#"{"content":"Answer."}"#) + done
        let (result, answer, reasoning) = try await turn(stream)
        #expect(result.text == "Answer.")
        #expect(answer == "Answer.")
        #expect(result.reasoning == "First, then.")
        #expect(reasoning == "First, then.")
    }

    @Test func leakedThinkTagsInContentAreSplit() async throws {
        let stream = line(#"{"content":"<think>hmm"}"#) + line(#"{"content":"</think>Yes."}"#) + done
        let (result, answer, reasoning) = try await turn(stream)
        #expect(result.text == "Yes.")
        #expect(answer == "Yes.")
        #expect(reasoning == "hmm")
    }

    /// The request LM Studio got for Off on Gemma 4 E4B.
    @Test func lmStudioOffRequestBody() async throws {
        _ = try await turn(line(#"{"content":"ok"}"#) + done, model: "google/gemma-4-e4b", reasoning: .off, capability: .toggle(defaultOn: true))
        let body = try #require(ReasoningStreamFixture.shared.lastBody())
        let json = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(json["reasoning_effort"] as? String == "none")
        #expect(json["chat_template_kwargs"] == nil)
    }

    @Test func anUnexpectedReasoningShapeDoesNotFailTheStream() async throws {
        let stream = line(#"{"reasoning":{"summary":"x"},"content":"Fine."}"#) + done
        let (result, _, _) = try await turn(stream)
        #expect(result.text == "Fine.")
        #expect(result.reasoning.isEmpty)
    }

    @Test func requestCarriesTheMappedFields() async throws {
        _ = try await turn(line(#"{"content":"ok"}"#) + done, model: "Qwen3.8-27B", type: .openAICompatible, reasoning: .high, capability: .family(model: "Qwen3.8-27B"))
        let body = try #require(ReasoningStreamFixture.shared.lastBody())
        let json = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(json["reasoning_effort"] as? String == "xhigh")
        let kwargs = try #require(json["chat_template_kwargs"] as? [String: Any])
        #expect(kwargs["enable_thinking"] as? Bool == true)
        #expect(kwargs["reasoning_effort"] as? String == "xhigh")
    }
}
