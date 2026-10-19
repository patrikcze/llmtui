import Foundation
import Testing
@testable import LLMTUIiOS

/// How web responses become model-readable text, and how long ones are paged.
struct WebPageTextTests {
    /// An Open-Meteo style hourly forecast: two parallel arrays of 168 values.
    static let forecastJSON: String = {
        let times = (0..<168).map { "\"2026-10-\(19 + $0 / 24)T\(String(format: "%02d", $0 % 24)):00\"" }
        let temperatures = (0..<168).map { String(format: "%.1f", 8 + Double($0 % 24) / 3) }
        return """
        {
          "latitude": 49.2, "longitude": 16.6,
          "hourly_units": { "time": "iso8601", "temperature_2m": "°C" },
          "hourly": {
            "time": [\(times.joined(separator: ", "))],
            "temperature_2m": [\(temperatures.joined(separator: ", "))]
          }
        }
        """
    }()

    @Test func jsonKeepsKeyOrderAndDropsOnlyInsignificantWhitespace() throws {
        let json = try #require(WebPageText.compactJSON(#"{ "b": 1, "a": { "x": [1, 2], "y": "two  words, kept" }, "c": [ {"d": 3} ] }"#))
        #expect(json == "{\"b\":1,\n\"a\":{\"x\":[1,2],\n\"y\":\"two  words, kept\"},\n\"c\":[{\"d\":3}]}")
        #expect(WebPageText.compactJSON("{not json") == nil)
        #expect(WebPageText.compactJSON(#"{"s": "a \"quoted\" , value"}"#) == #"{"s":"a \"quoted\" , value"}"#)
    }

    @Test func theResponseKindComesFromTheMimeTypeOrTheContent() {
        #expect(WebPageText.document(body: Self.forecastJSON, mimeType: "application/json").kind == .json)
        // A JSON API that labels its output text/plain is still JSON.
        #expect(WebPageText.document(body: #"[1, 2]"#, mimeType: "text/plain").kind == .json)
        #expect(WebPageText.document(body: "<html><body><p>Hi</p></body></html>", mimeType: nil).kind == .html)
        #expect(WebPageText.document(body: "<p>Hi</p>", mimeType: "text/html; charset=utf-8").kind == .html)

        let csv = WebPageText.document(body: "time,temp\r\n23:00,10.5\r\n00:00,9.8\r\n", mimeType: "text/csv")
        #expect(csv == WebPageText.Document(kind: .text, text: "time,temp\n23:00,10.5\n00:00,9.8"))
        // Broken JSON falls back to text rather than failing.
        #expect(WebPageText.document(body: "{oops", mimeType: "application/json").kind == .text)
    }

    @Test func htmlBecomesCompactMarkdown() throws {
        let html = """
        <html><head><title>T</title><style>p{}</style></head><body>
        <nav><a href="/home">Home</a> <a href="/about">About</a></nav>
        <main>
          <header><h1>Brno &amp; region <small>today</small></h1></header>
          <script>var tracking = 1;</script>
          <p>Mild night,&nbsp;then rain. <img src="https://cdn.example.com/x.png" alt="radar"></p>
          <ul><li>Wind 10&#160;km/h</li><li></li><li>Rain <b>2 mm</b></li></ul>
          <table><tr><th>Hour</th><th>°C</th></tr><tr><td>23:00</td><td><span>10.5</span></td></tr><tr><td></td><td></td></tr></table>
          <p>More on the <a href="/hourly?day=1&amp;units=c">hourly page</a>, <a href="javascript:void(0)">print</a>, <a href="#top">top</a>.</p>
          <!-- <p>hidden comment</p> -->
          <form><input name="q"><button>Search</button></form>
        </main>
        <footer>Cookie settings</footer>
        </body></html>
        """
        let text = WebPageText.markdown(fromHTML: html, baseURL: URL(string: "https://weather.example.com/brno/"))
        #expect(text == """
        # Brno & region today
        Mild night, then rain.
        - Wind 10 km/h
        - Rain 2 mm
        | Hour | °C |
        | --- | --- |
        | 23:00 | 10.5 |
        More on the [hourly page](https://weather.example.com/hourly?day=1&units=c) , print , top .
        """)
        for dropped in ["Home", "tracking", "Cookie", "Search", "hidden", "png", "radar"] {
            #expect(!text.contains(dropped), "\(dropped) should be dropped")
        }
    }

    @Test func aPageHeaderOutsideMainIsDroppedButTheBodyIsKept() {
        let html = "<body><header><a href='/'>Site</a></header><h2>Hourly</h2><p>10.5 °C</p></body>"
        #expect(WebPageText.markdown(fromHTML: html) == "## Hourly\n10.5 °C")
    }

    @Test func pagesCoverTheWholeTextAndBreakBetweenLines() {
        let text = (1...200).map { "line \($0) of the forecast" }.joined(separator: "\n")
        var offset = 0
        var pieces: [String] = []
        while true {
            let page = WebPageText.page(text, offset: offset, limit: 500)
            #expect(page.start == offset)
            #expect(page.text.count <= 500)
            pieces.append(page.text)
            guard let next = page.nextOffset else { break }
            #expect(page.text.hasSuffix("\n"), "a cut should fall after a line break")
            offset = next
        }
        #expect(pieces.joined() == text)
        #expect(pieces.count > 1)
        #expect(WebPageText.page(text, offset: 0, limit: 100_000).isComplete)
    }

    @Test func aCutOffJSONResultSaysHowToReadOn() throws {
        let document = WebPageText.document(body: Self.forecastJSON, mimeType: "application/json")
        let url = "https://api.open-meteo.com/v1/forecast?latitude=49.2&longitude=16.6&hourly=temperature_2m"
        let first = WebPageText.fetchOutput(url: url, document: document, offset: 0, limit: 4_000)
        let page = WebPageText.page(document.text, offset: 0, limit: 4_000)
        let next = try #require(page.nextOffset)
        #expect(first.hasPrefix("\(url) (JSON, characters 0-\(next) of \(document.text.count)):\n{\"latitude\":49.2,"))
        #expect(first.contains("call web_fetch with the same url and offset=\(next).]"))
        #expect(first.hasSuffix(WebPageText.untrustedNote))

        let rest = WebPageText.fetchOutput(url: url, document: document, offset: next, limit: 4_000)
        #expect(rest.contains("characters \(next)-\(document.text.count) of \(document.text.count)"))
        #expect(rest.contains("\"temperature_2m\":[8.0,"))
        #expect(rest.contains("[End of content.]"))

        let whole = WebPageText.fetchOutput(url: url, document: document, offset: 0, limit: 24_000)
        #expect(whole.contains("characters, complete):"))
        #expect(!whole.contains("offset="))

        let past = WebPageText.fetchOutput(url: url, document: document, offset: 999_999, limit: 4_000)
        #expect(past.hasPrefix("Offset \(document.text.count) is past the end"))
    }

    @Test func thePageSizeFollowsTheContextWindow() {
        #expect(WebPageText.pageSize(contextWindow: 2_048) == 4_000)
        #expect(WebPageText.pageSize(contextWindow: 8_192) == 6_144)
        #expect(WebPageText.pageSize(contextWindow: 131_072) == 24_000)
    }

    @Test func entitiesAndLinkTargetsAreHandled() {
        #expect(WebPageText.decodeEntities("a &lt;b&gt; &#x26; &#38; &deg;C &unknown;") == "a <b> & & °C &unknown;")
        #expect(WebPageText.withoutLinkTargets("See [the table](https://x.cz/t) and [more](https://y.cz).") == "See the table and more.")
    }

    @Test func researchURLsAreDistinctWebPagesInOrder() {
        let urls = WebResearch.normalizedURLs([
            " https://b.cz/x ", "https://www.b.cz/x/", "ftp://c.cz/file", "notaurl", "https://a.cz",
            "https://1.cz", "https://2.cz", "https://3.cz", "https://4.cz", "https://5.cz"
        ])
        #expect(urls == ["https://b.cz/x", "https://a.cz", "https://1.cz", "https://2.cz", "https://3.cz", "https://4.cz"])
    }

    @Test func webToolDefinitionsOfferPagingAndURLs() {
        let definitions = MobileChatRuntime.toolDefinitions(memoryEnabled: false)
        func parameters(_ name: String) -> [String: Any] {
            let function = definitions.compactMap { $0["function"] as? [String: Any] }.first { $0["name"] as? String == name }
            return (function?["parameters"] as? [String: Any])?["properties"] as? [String: Any] ?? [:]
        }
        #expect(parameters("web_fetch")["offset"] != nil)
        #expect(parameters("web_research")["urls"] != nil)
        let research = definitions.compactMap { $0["function"] as? [String: Any] }
            .first { $0["name"] as? String == "web_research" }?["description"] as? String ?? ""
        #expect(research.contains("For exact data from one site or API, use web_search and web_fetch"))
    }
}

/// `web_fetch` and `web_research` against a scripted web server: no network.
@MainActor @Suite(.serialized)
struct WebFetchLoopTests {
    private static func stream(_ text: String) -> String {
        let chunk = ["choices": [["delta": ["content": text], "finish_reason": "stop"]]]
        return "data: \(String(decoding: try! JSONSerialization.data(withJSONObject: chunk), as: UTF8.self))\n\ndata: [DONE]\n\n"
    }

    private static func call(_ name: String, _ arguments: String) -> String {
        let chunk: [String: Any] = ["choices": [["delta": ["tool_calls": [["index": 0, "id": "c1", "function": ["name": name, "arguments": arguments]]]], "finish_reason": "tool_calls"]]]
        return "data: \(String(decoding: try! JSONSerialization.data(withJSONObject: chunk), as: UTF8.self))\n\ndata: [DONE]\n\n"
    }

    private static let forecastURL = "https://api.example.com/v1/forecast?hourly=temperature_2m"

    private func withStubWeb<T>(_ body: () async throws -> T) async rethrows -> T {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [WebStubProtocol.self]
        WebStubProtocol.reset(pages: [
            "api.example.com": ("application/json", WebPageTextTests.forecastJSON),
            "html.duckduckgo.com": ("text/html", "<html><body>no results</body></html>"),
            "weather.example.com": ("text/html", "<html><body><main><h1>Brno hourly</h1><p>Tonight 23:00 temperature 10.5 °C, dry.</p></main></body></html>")
        ])
        SafeWebFetcher.clearRecentDocuments()
        SafeWebFetcher.session = URLSession(configuration: configuration)
        defer {
            SafeWebFetcher.session = .shared
            SafeWebFetcher.clearRecentDocuments()
        }
        return try await body()
    }

    @Test func jsonIgnoresFocusAndPagesFromOneDownload() async throws {
        try await withStubWeb {
            let first = try await SafeWebFetcher.fetch(Self.forecastURL, focus: "temperature", pageSize: 4_000)
            #expect(first.contains("(JSON, characters 0-"))
            let marker = try #require(first.range(of: "offset="))
            let next = try #require(Int(first[marker.upperBound...].prefix { $0.isNumber }))
            let second = try await SafeWebFetcher.fetch(Self.forecastURL, offset: next, pageSize: 4_000)
            #expect(second.contains("[End of content.]"))
            #expect(WebStubProtocol.requestedHosts() == ["api.example.com"])
        }
    }

    @Test func researchWithURLsReadsThemWithoutSearching() async throws {
        try await withStubWeb {
            let notes = try await WebResearch.run(
                question: "Temperature in Brno tonight at 23:00?",
                queries: [],
                urls: ["https://weather.example.com/brno"],
                maxSources: 4
            )
            #expect(notes.contains("Read the given URLs"))
            #expect(notes.contains("[1] https://weather.example.com/brno - https://weather.example.com/brno"))
            #expect(notes.contains("10.5 °C"))
            #expect(notes.contains("call web_fetch with a source's URL"))
            #expect(WebStubProtocol.requestedHosts() == ["weather.example.com"])
        }
    }

    @Test func theModelPagesWithTheOffsetArgument() async throws {
        try await withStubWeb {
            let base = FileManager.default.temporaryDirectory.appending(path: "WebFetchLoopTests-\(UUID().uuidString)")
            defer { try? FileManager.default.removeItem(at: base) }
            let configuration = URLSessionConfiguration.ephemeral
            configuration.protocolClasses = [WebStubProtocol.self]
            let model = MobileAppModel(
                conversationStore: MobileConversationStore(directory: base.appending(path: "Conversations")),
                documentStore: DocumentStore(root: base.appending(path: "Attachments")),
                runtime: MobileChatRuntime(session: URLSession(configuration: configuration))
            )
            let provider = MobileProviderProfile(model: "test-model", contextWindow: 2_048)
            model.profiles = [provider]
            model.activeProfileID = provider.id
            model.toolsEnabled = true
            WebStubProtocol.setModelReplies([
                Self.call("web_fetch", #"{"url":"\#(Self.forecastURL)","offset":"3000"}"#),
                Self.stream("Done.")
            ])
            _ = model.newConversation()
            model.draft = "Hourly temperatures?"
            model.send()
            // Approve the fetch whatever the approval setting is.
            #expect(await waitUntil { model.pendingToolApproval != nil || !model.isGenerating })
            if model.pendingToolApproval != nil { model.resolveApproval(true) }
            #expect(await waitUntil { !model.isGenerating })

            let requests = WebStubProtocol.modelRequests()
            try #require(requests.count == 2)
            let stream = try #require(requests[1].httpBodyStream ?? requests[1].httpBody.map { InputStream(data: $0) })
            stream.open(); defer { stream.close() }
            var data = Data(); var buffer = [UInt8](repeating: 0, count: 4096)
            while stream.hasBytesAvailable { let n = stream.read(&buffer, maxLength: buffer.count); if n <= 0 { break }; data.append(contentsOf: buffer.prefix(n)) }
            let body = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
            let messages = body["messages"] as? [[String: Any]] ?? []
            let result = try #require(messages.last { $0["role"] as? String == "tool" }?["content"] as? String)
            // A 2,048-token window gets the smallest page, starting at 3000.
            #expect(result.contains("(JSON, characters 3000-"))
            #expect(result.contains("[End of content.]"))
            #expect(WebStubProtocol.requestedHosts() == ["api.example.com"])
        }
    }
}

/// A scripted public web and model server private to `WebFetchLoopTests`:
/// one response per web host; any other host is the model, answered from a
/// queue of event streams.
nonisolated final class WebStubProtocol: URLProtocol {
    private static let lock = NSLock()
    nonisolated(unsafe) private static var pages: [String: (String, String)] = [:]
    nonisolated(unsafe) private static var hosts: [String] = []
    nonisolated(unsafe) private static var replies: [String] = []
    nonisolated(unsafe) private static var captured: [URLRequest] = []

    static func reset(pages newPages: [String: (String, String)]) {
        lock.lock(); defer { lock.unlock() }
        pages = newPages
        hosts = []
        replies = []
        captured = []
    }

    static func setModelReplies(_ texts: [String]) {
        lock.lock(); defer { lock.unlock() }
        replies = texts
    }

    static func modelRequests() -> [URLRequest] {
        lock.lock(); defer { lock.unlock() }
        return captured
    }

    private static func modelReply(_ request: URLRequest) -> String {
        lock.lock(); defer { lock.unlock() }
        captured.append(request)
        return replies.isEmpty ? "" : replies.removeFirst()
    }

    private static func isWebHost(_ host: String) -> Bool {
        lock.lock(); defer { lock.unlock() }
        return pages[host] != nil
    }

    static func requestedHosts() -> [String] {
        lock.lock(); defer { lock.unlock() }
        return hosts
    }

    private static func page(for host: String) -> (String, String)? {
        lock.lock(); defer { lock.unlock() }
        hosts.append(host)
        return pages[host]
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        guard Self.isWebHost(url.host ?? "") else {
            let reply = Self.modelReply(request)
            let response = HTTPURLResponse(url: url, statusCode: 200, httpVersion: "HTTP/1.1", headerFields: ["Content-Type": "text/event-stream"])!
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            client?.urlProtocol(self, didLoad: Data(reply.utf8))
            client?.urlProtocolDidFinishLoading(self)
            return
        }
        let page = Self.page(for: url.host ?? "")
        let response = HTTPURLResponse(
            url: url,
            statusCode: page == nil ? 404 : 200,
            httpVersion: "HTTP/1.1",
            headerFields: ["Content-Type": page?.0 ?? "text/plain"]
        )!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data((page?.1 ?? "").utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
