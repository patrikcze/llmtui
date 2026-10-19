import Foundation
import Testing
@testable import LLMTUIiOS

/// The reasoning (brain) choice: what each choice sends for the model
/// families whose templates disagree, and how streamed reasoning is kept
/// apart from the answer.
struct ReasoningRequestTests {
    private func fields(_ reasoning: MobileReasoning, _ model: String, _ type: MobileProviderType = .lmStudio, url: String = "http://192.168.1.2:1234/v1") -> [String: Any] {
        ReasoningRequest.fields(for: reasoning, model: model, providerType: type, baseURL: url)
    }

    private func kwargs(_ fields: [String: Any]) -> [String: Any]? {
        fields["chat_template_kwargs"] as? [String: Any]
    }

    @Test func automaticSendsNothing() {
        for type in MobileProviderType.allCases {
            #expect(fields(.automatic, "google/gemma-4-31b", type).isEmpty)
        }
    }

    /// Gemma 4's template treats a missing enable_thinking as off, so a bare
    /// reasoning_effort never turned thinking on.
    @Test func gemmaThinkingIsSwitchedThroughTheTemplate() {
        let on = fields(.medium, "google/gemma-4-31b")
        #expect(kwargs(on)?["enable_thinking"] as? Bool == true)
        #expect(kwargs(on)?["reasoning_effort"] == nil)
        #expect(on["reasoning_effort"] as? String == "medium")

        let off = fields(.off, "google/gemma-4-31b")
        #expect(kwargs(off)?["enable_thinking"] as? Bool == false)
        #expect(off["reasoning_effort"] == nil)
    }

    /// Qwen 3.8's template accepts low, medium and xhigh, and raises an error
    /// for "high".
    @Test func qwen38HighIsSentAsXHigh() {
        for model in ["qwen/qwen3.8-27b", "Qwen3.8-27B-GGUF", "qwen3.8:27b", "mlx-community/Qwen3_8-27B-4bit", "qwen-3.8"] {
            let high = fields(.high, model)
            #expect(high["reasoning_effort"] as? String == "xhigh", "\(model)")
            #expect(kwargs(high)?["reasoning_effort"] as? String == "xhigh", "\(model)")
            #expect(kwargs(high)?["enable_thinking"] as? Bool == true, "\(model)")
            #expect(kwargs(fields(.low, model))?["reasoning_effort"] as? String == "low", "\(model)")
        }
        // Other Qwen releases keep the plain names and get no template effort.
        for model in ["qwen3-32b", "qwen3.80-test", "qwen2.5-7b"] {
            let high = fields(.high, model)
            #expect(high["reasoning_effort"] as? String == "high", "\(model)")
            #expect(kwargs(high)?["reasoning_effort"] == nil, "\(model)")
        }
    }

    @Test func ollamaGetsReasoningEffortOnly() {
        let off = fields(.off, "gemma4:31b", .ollama, url: "http://192.168.1.2:11434")
        #expect(off["reasoning_effort"] as? String == "none")
        #expect(kwargs(off) == nil)
        let high = fields(.high, "qwen3.8:27b", .ollama, url: "http://192.168.1.2:11434")
        #expect(high["reasoning_effort"] as? String == "xhigh")
        #expect(kwargs(high) == nil)
    }

    @Test func openAIAndGPTOSSNeverGetTemplateKwargs() {
        let openAI = fields(.off, "gpt-5", .openAICompatible, url: "https://api.openai.com/v1")
        #expect(openAI.isEmpty)
        #expect(kwargs(fields(.high, "gpt-5", .openAICompatible, url: "https://api.openai.com/v1")) == nil)

        // GPT-OSS cannot turn reasoning off; Off asks for the least.
        let oss = fields(.off, "openai/gpt-oss-20b")
        #expect(oss["reasoning_effort"] as? String == "low")
        #expect(kwargs(oss) == nil)
        #expect(kwargs(fields(.high, "openai/gpt-oss-20b")) == nil)
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

@Suite(.serialized) struct ReasoningStreamTests {
    private func turn(_ stream: String, model: String = "test", reasoning: MobileReasoning = .automatic) async throws -> (ChatTurnResult, answer: String, reasoning: String) {
        ReasoningStreamFixture.shared.set(stream)
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ReasoningStreamURLProtocol.self]
        let session = URLSession(configuration: configuration)
        defer { session.invalidateAndCancel() }
        let answer = Collected()
        let thoughts = Collected()
        let result = try await MobileChatRuntime(session: session).streamTurn(
            profile: MobileProviderProfile(type: .lmStudio, baseURL: "https://stream.fixture/v1", model: model),
            apiKey: "",
            messages: [],
            tools: nil,
            reasoning: reasoning,
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

    @Test func anUnexpectedReasoningShapeDoesNotFailTheStream() async throws {
        let stream = line(#"{"reasoning":{"summary":"x"},"content":"Fine."}"#) + done
        let (result, _, _) = try await turn(stream)
        #expect(result.text == "Fine.")
        #expect(result.reasoning.isEmpty)
    }

    @Test func requestCarriesTheMappedFields() async throws {
        _ = try await turn(line(#"{"content":"ok"}"#) + done, model: "Qwen3.8-27B", reasoning: .high)
        let body = try #require(ReasoningStreamFixture.shared.lastBody())
        let json = try #require(try JSONSerialization.jsonObject(with: body) as? [String: Any])
        #expect(json["reasoning_effort"] as? String == "xhigh")
        let kwargs = try #require(json["chat_template_kwargs"] as? [String: Any])
        #expect(kwargs["enable_thinking"] as? Bool == true)
        #expect(kwargs["reasoning_effort"] as? String == "xhigh")
    }
}
