import SwiftUI
import UIKit
import WebKit

struct MobileRichMessageView: View {
    let source: String

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(MobileRichContentParser.blocks(from: source)) { block in
                switch block.kind {
                case .markdown(let text):
                    MobileMarkdownView(source: text)
                case .code(let language, let text):
                    MobileCodeBlockView(language: language, source: text)
                case .mermaid(let text):
                    MobileMermaidBlock(source: text)
                case .image(let url, let alt):
                    MobileMarkdownImageView(source: url, alt: alt)
                case .math(let text):
                    if MobileRenderResources.isSupportedMath(text) {
                        MobileWebRenderView(
                            kind: .math,
                            source: MobileRenderResources.mathExpression(text)
                        )
                    } else {
                        MobileCodeBlockView(language: "latex", source: text)
                    }
                case .table(let headers, let rows):
                    MobileMarkdownTableView(headers: headers, rows: rows)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct MobileRichContentBlock: Identifiable {
    enum Kind {
        case markdown(String)
        case code(language: String, source: String)
        case mermaid(String)
        case math(String)
        case table(headers: [String], rows: [[String]])
        case image(url: String, alt: String)
    }

    let id: Int
    let kind: Kind
}

private enum MobileRichContentParser {
    static func blocks(from source: String) -> [MobileRichContentBlock] {
        let lines = source.components(separatedBy: .newlines)
        var result: [MobileRichContentBlock] = []
        var markdown: [String] = []
        var index = 0

        func appendMarkdown() {
            let text = markdown.joined(separator: "\n")
            if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                result.append(MobileRichContentBlock(id: result.count, kind: .markdown(text)))
            }
            markdown.removeAll(keepingCapacity: true)
        }

        while index < lines.count {
            let line = lines[index]
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("```") {
                appendMarkdown()
                let language = String(trimmed.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                index += 1
                var content: [String] = []
                while index < lines.count, !lines[index].trimmingCharacters(in: .whitespaces).hasPrefix("```") {
                    content.append(lines[index])
                    index += 1
                }
                if index < lines.count { index += 1 }
                let text = content.joined(separator: "\n")
                switch language.lowercased() {
                case "mermaid":
                    result.append(MobileRichContentBlock(id: result.count, kind: .mermaid(text)))
                case "latex", "tex":
                    result.append(MobileRichContentBlock(id: result.count, kind: .math(text)))
                default:
                    result.append(MobileRichContentBlock(id: result.count, kind: .code(language: language, source: text)))
                }
                continue
            }

            if trimmed == "$$" || trimmed == "\\[" {
                let closing = trimmed == "$$" ? "$$" : "\\]"
                appendMarkdown()
                index += 1
                var content: [String] = []
                while index < lines.count, lines[index].trimmingCharacters(in: .whitespaces) != closing {
                    content.append(lines[index])
                    index += 1
                }
                if index < lines.count { index += 1 }
                result.append(MobileRichContentBlock(id: result.count, kind: .math(content.joined(separator: "\n"))))
                continue
            }

            if trimmed.hasPrefix("$$"), trimmed.hasSuffix("$$"), trimmed.count > 4 {
                appendMarkdown()
                result.append(MobileRichContentBlock(
                    id: result.count,
                    kind: .math(String(trimmed.dropFirst(2).dropLast(2)))
                ))
                index += 1
                continue
            }

            if index + 1 < lines.count,
               isTableRow(line),
               isTableDelimiter(lines[index + 1]) {
                appendMarkdown()
                let headers = tableCells(line)
                index += 2
                var rows: [[String]] = []
                while index < lines.count, isTableRow(lines[index]), !isTableDelimiter(lines[index]) {
                    rows.append(tableCells(lines[index]))
                    index += 1
                }
                result.append(MobileRichContentBlock(
                    id: result.count,
                    kind: .table(headers: headers, rows: rows)
                ))
                continue
            }

            // `![alt](url)` and `<img src>` are pulled out wherever they
            // appear (often inline after a label in a list item), because
            // the Markdown renderer has no image support and would drop them.
            let images = inlineImages(in: line)
            if images.isEmpty {
                markdown.append(line)
            } else {
                var cursor = line.startIndex
                for image in images {
                    let before = String(line[cursor..<image.range.lowerBound])
                    if !before.trimmingCharacters(in: .whitespaces).isEmpty { markdown.append(before) }
                    appendMarkdown()
                    result.append(MobileRichContentBlock(id: result.count, kind: .image(url: image.url, alt: image.alt)))
                    cursor = image.range.upperBound
                }
                let after = String(line[cursor...])
                if !after.trimmingCharacters(in: .whitespaces).isEmpty { markdown.append(after) }
            }
            index += 1
        }
        appendMarkdown()
        return result
    }

    private static let markdownImage = try? NSRegularExpression(pattern: #"!\[([^\]]*)\]\(([^)\s]+)(?:\s+"[^"]*")?\)"#)
    private static let htmlImage = try? NSRegularExpression(
        pattern: #"<img\b[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*>"#, options: [.caseInsensitive]
    )
    private static let htmlAlt = try? NSRegularExpression(pattern: #"\balt\s*=\s*["']([^"']*)["']"#, options: [.caseInsensitive])

    /// Every `![alt](url)` and `<img src="…">` in a line, in order.
    static func inlineImages(in line: String) -> [(range: Range<String.Index>, url: String, alt: String)] {
        guard line.contains("![") || line.localizedCaseInsensitiveContains("<img") else { return [] }
        let full = NSRange(line.startIndex..., in: line)
        var found: [(range: Range<String.Index>, url: String, alt: String)] = []
        for match in markdownImage?.matches(in: line, range: full) ?? [] {
            guard let range = Range(match.range, in: line),
                  let alt = Range(match.range(at: 1), in: line),
                  let url = Range(match.range(at: 2), in: line) else { continue }
            found.append((range, String(line[url]), String(line[alt])))
        }
        for match in htmlImage?.matches(in: line, range: full) ?? [] {
            guard let range = Range(match.range, in: line),
                  let src = Range(match.range(at: 1), in: line) else { continue }
            let tag = String(line[range])
            let alt = htmlAlt?.firstMatch(in: tag, range: NSRange(tag.startIndex..., in: tag))
                .flatMap { Range($0.range(at: 1), in: tag).map { String(tag[$0]) } } ?? ""
            found.append((range, String(line[src]), alt))
        }
        return found.sorted { $0.range.lowerBound < $1.range.lowerBound }
    }

    private static func isTableRow(_ line: String) -> Bool {
        line.contains("|") && !line.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private static func isTableDelimiter(_ line: String) -> Bool {
        let cells = tableCells(line)
        return !cells.isEmpty && cells.allSatisfy {
            let value = $0.trimmingCharacters(in: .whitespaces)
            return value.count >= 3 && value.allSatisfy { $0 == "-" || $0 == ":" }
        }
    }

    private static func tableCells(_ line: String) -> [String] {
        var value = line.trimmingCharacters(in: .whitespaces)
        if value.hasPrefix("|") { value.removeFirst() }
        if value.hasSuffix("|") { value.removeLast() }
        return value.split(separator: "|", omittingEmptySubsequences: false).map {
            $0.trimmingCharacters(in: .whitespaces)
        }
    }
}

private struct MobileMarkdownView: View {
    let source: String

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(MobileMarkdownDocument(source: source).blocks) { block in
                MobileMarkdownBlockView(block: block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .textSelection(.enabled)
    }
}

private struct MobileMarkdownDocument {
    enum ListMarker {
        case unordered
        case ordered(Int)
    }

    enum Kind {
        case paragraph
        case heading(Int)
        case listItem(marker: ListMarker, depth: Int)
        case blockQuote(depth: Int)
        case thematicBreak
    }

    struct Block: Identifiable {
        let id: Int
        let kind: Kind
        let content: AttributedString
    }

    let blocks: [Block]

    init(source: String) {
        let options = AttributedString.MarkdownParsingOptions(
            interpretedSyntax: .full,
            failurePolicy: .returnPartiallyParsedIfPossible
        )
        guard let parsed = try? AttributedString(markdown: source, options: options) else {
            blocks = source.isEmpty ? [] : [Block(id: 0, kind: .paragraph, content: AttributedString(source))]
            return
        }

        var result: [Block] = []
        var currentID: Int?
        var currentKind: Kind?
        var currentContent = AttributedString()
        var seenListItems = Set<Int>()

        func appendCurrent() {
            guard let id = currentID, let kind = currentKind else { return }
            var content = currentContent
            Self.styleInlineContent(&content)
            if kind.isThematicBreak || !String(content.characters).trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                result.append(Block(id: id, kind: kind, content: content))
            }
            currentContent = AttributedString()
        }

        for run in parsed.runs {
            let descriptor = Self.blockDescriptor(for: run.presentationIntent, seenListItems: &seenListItems)
            let id = descriptor?.id ?? Int.min
            let kind = descriptor?.kind ?? .paragraph
            if currentID != id {
                appendCurrent()
                currentID = id
                currentKind = kind
            }
            currentContent.append(AttributedString(parsed[run.range]))
        }
        appendCurrent()
        blocks = result
    }

    private static func blockDescriptor(
        for intent: PresentationIntent?,
        seenListItems: inout Set<Int>
    ) -> (id: Int, kind: Kind)? {
        guard let intent else { return nil }
        var paragraphID: Int?
        var quoteDepth = 0
        var listDepth = 0
        var listItem: (id: Int, ordinal: Int)?
        var heading: (id: Int, level: Int)?
        var thematicBreakID: Int?
        var ordered = false
        var foundList = false

        for component in intent.components {
            switch component.kind {
            case .paragraph:
                paragraphID = component.identity
            case .header(let level):
                heading = (component.identity, level)
            case .orderedList:
                listDepth += 1
                if !foundList { ordered = true; foundList = true }
            case .unorderedList:
                listDepth += 1
                if !foundList { foundList = true }
            case .listItem(let ordinal):
                if listItem == nil { listItem = (component.identity, ordinal) }
            case .blockQuote:
                quoteDepth += 1
            case .thematicBreak:
                thematicBreakID = component.identity
            default:
                break
            }
        }

        if let thematicBreakID { return (thematicBreakID, .thematicBreak) }
        if let heading { return (heading.id, .heading(heading.level)) }
        if let listItem {
            let firstParagraph = seenListItems.insert(listItem.id).inserted
            if firstParagraph {
                let marker: ListMarker = ordered ? .ordered(listItem.ordinal) : .unordered
                return (paragraphID ?? listItem.id, .listItem(marker: marker, depth: max(listDepth, 1)))
            }
        }
        if quoteDepth > 0 {
            return (paragraphID ?? intent.components.last?.identity ?? 0, .blockQuote(depth: quoteDepth))
        }
        return (paragraphID ?? intent.components.last?.identity ?? 0, .paragraph)
    }

    private static func styleInlineContent(_ content: inout AttributedString) {
        for run in content.runs {
            if run.inlinePresentationIntent?.contains(.code) == true {
                content[run.range].font = .system(.body, design: .monospaced)
                content[run.range].backgroundColor = Color.secondary.opacity(0.14)
            }
            // Only web links and the app's own validated citation links
            // (created by DocumentCitations.render) stay tappable.
            if let link = run.link, !["http", "https", DocumentCitations.scheme].contains(link.scheme?.lowercased() ?? "") {
                content[run.range].link = nil
            }
        }
    }
}

private extension MobileMarkdownDocument.Kind {
    var isThematicBreak: Bool {
        if case .thematicBreak = self { return true }
        return false
    }
}

private struct MobileMarkdownBlockView: View {
    let block: MobileMarkdownDocument.Block

    @ViewBuilder
    var body: some View {
        switch block.kind {
        case .paragraph:
            contentView
                .font(.body)
                .padding(.bottom, 10)
        case .heading(let level):
            contentView
                .font(headingFont(level))
                .padding(.top, level <= 2 ? 6 : 3)
                .padding(.bottom, level <= 2 ? 8 : 5)
        case .listItem(let marker, let depth):
            HStack(alignment: .firstTextBaseline, spacing: 7) {
                Text(markerText(marker))
                    .font(.body.monospacedDigit())
                    .frame(minWidth: 15, alignment: .trailing)
                    .accessibilityHidden(true)
                contentView
                    .font(.body)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.leading, CGFloat(max(depth - 1, 0)) * 18)
            .padding(.bottom, 5)
        case .blockQuote(let depth):
            HStack(alignment: .top, spacing: 9) {
                RoundedRectangle(cornerRadius: 1)
                    .fill(Color.accentColor.opacity(0.65))
                    .frame(width: 3)
                contentView
                    .font(.body)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.leading, CGFloat(max(depth - 1, 0)) * 12)
            .padding(.vertical, 5)
            .padding(.bottom, 5)
        case .thematicBreak:
            Divider().padding(.vertical, 9)
        }
    }

    @ViewBuilder
    private var contentView: some View {
        let plain = String(block.content.characters)
        if MobileRenderResources.containsInlineMath(plain) {
            MobileWebRenderView(
                kind: .richMath,
                source: MobileRenderResources.richTextHTML(block.content)
            )
        } else {
            Text(block.content)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func markerText(_ marker: MobileMarkdownDocument.ListMarker) -> String {
        switch marker {
        case .unordered: "•"
        case .ordered(let number): "\(number)."
        }
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1: .title2.bold()
        case 2: .title3.bold()
        case 3: .headline
        case 4: .headline.weight(.semibold)
        case 5: .subheadline.bold()
        default: .caption.bold()
        }
    }
}

private struct MobileMarkdownTableView: View {
    let headers: [String]
    let rows: [[String]]

    var body: some View {
        ScrollView(.horizontal) {
            Grid(alignment: .leading, horizontalSpacing: 0, verticalSpacing: 0) {
                GridRow {
                    ForEach(Array(headers.enumerated()), id: \.offset) { _, header in
                        cell(header, emphasized: true)
                    }
                }
                Divider()
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    GridRow {
                        ForEach(Array(normalized(row).enumerated()), id: \.offset) { _, value in
                            cell(value, emphasized: false)
                        }
                    }
                }
            }
        }
        .scrollIndicators(.visible)
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func normalized(_ row: [String]) -> [String] {
        Array((row + Array(repeating: "", count: headers.count)).prefix(headers.count))
    }

    private func cell(_ value: String, emphasized: Bool) -> some View {
        Text(markdown(value))
            .font(emphasized ? .subheadline.bold() : .subheadline)
            .fixedSize(horizontal: false, vertical: true)
            .frame(minWidth: 90, maxWidth: 220, alignment: .leading)
            .padding(8)
            .background(emphasized ? Color.secondary.opacity(0.12) : Color.clear)
            .overlay { Rectangle().stroke(Color.secondary.opacity(0.2), lineWidth: 0.5) }
    }

    private func markdown(_ value: String) -> AttributedString {
        (try? AttributedString(
            markdown: value,
            options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        )) ?? AttributedString(value)
    }
}

private struct MobileCodeBlockView: View {
    let language: String
    let source: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !language.isEmpty {
                Text(language.uppercased())
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            ScrollView(.horizontal) {
                Text(source)
                    .font(.system(.callout, design: .monospaced))
                    .textSelection(.enabled)
                    .padding(10)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Color.secondary.opacity(0.1), in: RoundedRectangle(cornerRadius: 10))
    }
}

struct MobileWebRenderView: UIViewRepresentable {
    enum Kind: Hashable {
        case mermaid
        case math
        case richMath
    }

    let kind: Kind
    let source: String
    /// Receives the web view, so a Mermaid diagram can be exported.
    var handle: WebViewHandle?
    @State private var measuredHeight: CGFloat = 60

    func makeCoordinator() -> Coordinator {
        Coordinator(height: $measuredHeight)
    }

    func makeUIView(context: Context) -> WKWebView {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        configuration.defaultWebpagePreferences.allowsContentJavaScript = true
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        configuration.userContentController.add(context.coordinator, name: "contentHeight")
        let view = WKWebView(frame: .zero, configuration: configuration)
        view.isOpaque = false
        view.backgroundColor = .clear
        view.scrollView.backgroundColor = .clear
        view.scrollView.isScrollEnabled = kind == .mermaid
        view.navigationDelegate = context.coordinator
        handle?.webView = view
        return view
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        let key = "\(kind)-\(source)-\(UITraitCollection.current.userInterfaceStyle.rawValue)"
        guard context.coordinator.renderKey != key else { return }
        context.coordinator.renderKey = key
        let dark = webView.traitCollection.userInterfaceStyle == .dark
        webView.loadHTMLString(MobileRenderResources.document(kind: kind, source: source, dark: dark), baseURL: Bundle.main.resourceURL)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: WKWebView, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? 320, height: measuredHeight)
    }

    static func dismantleUIView(_ webView: WKWebView, coordinator: Coordinator) {
        webView.stopLoading()
        webView.navigationDelegate = nil
        webView.configuration.userContentController.removeScriptMessageHandler(forName: "contentHeight")
    }

    final class Coordinator: NSObject, WKScriptMessageHandler, WKNavigationDelegate {
        var renderKey: String?
        private var height: Binding<CGFloat>

        init(height: Binding<CGFloat>) {
            self.height = height
        }

        func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
            guard message.name == "contentHeight", let number = message.body as? NSNumber else { return }
            let newHeight = min(max(CGFloat(truncating: number), 32), 700)
            if abs(height.wrappedValue - newHeight) > 0.5 {
                height.wrappedValue = newHeight
            }
        }

        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationAction: WKNavigationAction,
            decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
        ) {
            guard let url = navigationAction.request.url,
                  let scheme = url.scheme?.lowercased() else {
                decisionHandler(.cancel)
                return
            }
            if ["http", "https"].contains(scheme) {
                UIApplication.shared.open(url)
                decisionHandler(.cancel)
            } else {
                decisionHandler(["about", "applewebdata", "file"].contains(scheme) ? .allow : .cancel)
            }
        }
    }
}

private enum MobileRenderResources {
    static let mermaid = textResource(name: "mermaid.min", extension: "js", subdirectory: "Rendering/Mermaid")
    static let katex = textResource(name: "katex.min", extension: "js", subdirectory: "Rendering/KaTeX")
    static let katexCSS: String? = {
        textResource(name: "katex.min", extension: "css", subdirectory: "Rendering/KaTeX")?
            .replacingOccurrences(of: "url(fonts/", with: "url(")
    }()

    static func containsInlineMath(_ source: String) -> Bool {
        source.range(of: #"(?<!\\)\$(?!\$)(?:\\.|[^$\n])+?(?<!\\)\$|\\\((?:\\.|[^\n])+?\\\)"#, options: .regularExpression) != nil
    }

    static func mathExpression(_ source: String) -> String {
        var value = source.trimmingCharacters(in: .whitespacesAndNewlines)
        let delimiters = [("$$", "$$"), ("\\[", "\\]"), ("\\(", "\\)"), ("$", "$")]
        for (opening, closing) in delimiters
        where value.hasPrefix(opening) && value.hasSuffix(closing)
            && value.count >= opening.count + closing.count {
            value.removeFirst(opening.count)
            value.removeLast(closing.count)
            break
        }
        return value
            .replacingOccurrences(of: "\\begin{align}", with: "\\begin{aligned}")
            .replacingOccurrences(of: "\\end{align}", with: "\\end{aligned}")
            .trimmingCharacters(in: .whitespacesAndNewlines)
    }

    static func isSupportedMath(_ source: String) -> Bool {
        let value = mathExpression(source).lowercased()
        let unsupported = [
            "\\documentclass", "\\usepackage", "\\begin{document}",
            "\\begin{itemize}", "\\begin{enumerate}", "\\begin{verbatim}",
            "\\begin{lstlisting}", "\\begin{table}", "\\begin{figure}"
        ]
        return !value.isEmpty && !unsupported.contains(where: value.contains)
    }

    static func richTextHTML(_ content: AttributedString) -> String {
        let plainText = String(content.characters)
        let ranges = inlineMathRanges(in: plainText)
        var result = ""
        var cursor = plainText.startIndex
        for range in ranges {
            if cursor < range.lowerBound {
                result += styledHTML(content, stringRange: cursor..<range.lowerBound, in: plainText)
            }
            result += #"<span class="math-source">"#
                + htmlEscaped(mathExpression(String(plainText[range])))
                + "</span>"
            cursor = range.upperBound
        }
        if cursor < plainText.endIndex {
            result += styledHTML(content, stringRange: cursor..<plainText.endIndex, in: plainText)
        }
        return result
    }

    private static func inlineMathRanges(in source: String) -> [Range<String.Index>] {
        guard let expression = try? NSRegularExpression(
            pattern: #"(?<!\\)\$(?!\$)(?:\\.|[^$\n])+?(?<!\\)\$|\\\((?:\\.|[^\n])+?\\\)"#
        ) else { return [] }
        return expression.matches(
            in: source,
            range: NSRange(source.startIndex..., in: source)
        ).compactMap { Range($0.range, in: source) }
    }

    private static func styledHTML(
        _ content: AttributedString,
        stringRange: Range<String.Index>,
        in plainText: String
    ) -> String {
        guard let attributedRange = Range(stringRange, in: content) else {
            return htmlEscaped(String(plainText[stringRange]))
        }
        var result = ""
        for run in content[attributedRange].runs {
            var fragment = htmlEscaped(String(content[run.range].characters))
                .replacingOccurrences(of: "\n", with: "<br>")
            let intent = run.inlinePresentationIntent
            if intent?.contains(.code) == true { fragment = "<code>\(fragment)</code>" }
            if intent?.contains(.stronglyEmphasized) == true { fragment = "<strong>\(fragment)</strong>" }
            if intent?.contains(.emphasized) == true { fragment = "<em>\(fragment)</em>" }
            if intent?.contains(.strikethrough) == true { fragment = "<s>\(fragment)</s>" }
            if let link = run.link, ["http", "https"].contains(link.scheme?.lowercased() ?? "") {
                fragment = #"<a href=""# + attributeEscaped(link.absoluteString) + #"">"# + fragment + "</a>"
            }
            result += fragment
        }
        return result
    }

    private static func htmlEscaped(_ value: String) -> String {
        value
            .replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
    }

    private static func attributeEscaped(_ value: String) -> String {
        htmlEscaped(value)
            .replacingOccurrences(of: "\"", with: "&quot;")
            .replacingOccurrences(of: "'", with: "&#39;")
    }

    static func document(kind: MobileWebRenderView.Kind, source: String, dark: Bool) -> String {
        let foreground = dark ? "#f2f2f7" : "#1c1c1e"
        let secondary = dark ? "#aeaeb2" : "#636366"
        let escapedSource = jsonString(source)
        let runtime: String
        let body: String
        let script: String

        switch kind {
        case .mermaid:
            runtime = mermaid ?? ""
            // The class name "mermaid" enables Mermaid's page-load scanner.
            // This view renders through the API, so adding that class causes a
            // second pass that attempts to parse the generated SVG as source.
            body = #"<div id="content" role="img" aria-label="Mermaid diagram"></div>"#
            let mermaidSource = normalizedMermaidSource(source)
            let escapedMermaidSource = jsonString(mermaidSource)
            script = """
            mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: '\(dark ? "dark" : "default")' });
            mermaid.render('mobile-mermaid', \(escapedMermaidSource)).then(({svg}) => {
              document.getElementById('content').innerHTML = svg;
              reportHeight();
            }).catch(error => showError(error, \(escapedMermaidSource)));
            """
        case .math:
            runtime = katex ?? ""
            body = #"<div id="content"></div>"#
            script = """
            try {
              katex.render(\(escapedSource), document.getElementById('content'), {
                displayMode: true, throwOnError: true, trust: false, output: 'htmlAndMathml'
              });
            } catch (error) { showError(error); }
            reportHeight();
            """
        case .richMath:
            runtime = katex ?? ""
            body = #"<div id="content">"# + source + "</div>"
            script = """
            for (const element of document.querySelectorAll('.math-source')) {
              const expression = element.textContent;
              try {
                katex.render(expression, element, {
                  displayMode: false, throwOnError: true, trust: false, output: 'htmlAndMathml'
                });
              } catch (_) {
                element.textContent = expression;
                element.style.fontFamily = 'ui-monospace, Menlo, monospace';
              }
            }
            reportHeight();
            """
        }

        guard !runtime.isEmpty else {
            return "<html><body style='color:\(secondary);font:15px -apple-system'>Renderer resource unavailable.</body></html>"
        }

        return """
        <!doctype html><html><head>
        <meta name="viewport" content="width=device-width,initial-scale=1">
        <meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; font-src file: data:; img-src data:">
        <style>
        \(katexCSS ?? "")
        * { box-sizing: border-box; }
        html, body { margin: 0; padding: 0; background: transparent; color: \(foreground); }
        body { font: 16px/1.42 -apple-system, BlinkMacSystemFont, sans-serif; overflow-wrap: anywhere; }
        #content { width: 100%; }
        svg { max-width: 100%; height: auto; }
        code { padding: 1px 4px; border-radius: 4px; background: rgba(128,128,128,.16); font-family: ui-monospace, Menlo, monospace; }
        h1, h2, h3 { margin: 7px 0; }
        #error { color: \(secondary); white-space: pre-wrap; font-family: ui-monospace, Menlo, monospace; }
        </style>
        <script>\(runtime.replacingOccurrences(of: "</script", with: "<\\/script", options: .caseInsensitive))</script>
        </head><body>\(body)<div id="error"></div><script>
        const reportHeight = () => window.webkit?.messageHandlers?.contentHeight?.postMessage(Math.ceil(document.documentElement.scrollHeight));
        const showError = (error, source = '') => {
          document.getElementById('error').textContent = 'Unable to render.\\n' + (error?.message || String(error)) + (source ? '\\n\\n' + source : '');
          reportHeight();
        };
        \(script)
        new ResizeObserver(reportHeight).observe(document.body);
        document.fonts.ready.then(reportHeight);
        requestAnimationFrame(reportHeight);
        </script></body></html>
        """
    }

    private static func textResource(name: String, extension fileExtension: String, subdirectory: String) -> String? {
        let url = Bundle.main.url(forResource: name, withExtension: fileExtension)
            ?? Bundle.main.url(forResource: name, withExtension: fileExtension, subdirectory: subdirectory)
        return url.flatMap { try? String(contentsOf: $0, encoding: .utf8) }
    }

    private static func normalizedMermaidSource(_ source: String) -> String {
        var value = source
            .replacingOccurrences(of: "\u{00A0}", with: " ")
            .replacingOccurrences(of: "\u{200B}", with: "")
            .replacingOccurrences(of: "“", with: "\"")
            .replacingOccurrences(of: "”", with: "\"")
            .replacingOccurrences(of: "‘", with: "'")
            .replacingOccurrences(of: "’", with: "'")
            .trimmingCharacters(in: .whitespacesAndNewlines)
        if let expression = try? NSRegularExpression(
            pattern: #"\b([A-Za-z_][A-Za-z0-9_-]*)\[(?!\[)([^\]"\n]+)\]"#
        ) {
            let range = NSRange(value.startIndex..., in: value)
            value = expression.stringByReplacingMatches(
                in: value,
                range: range,
                withTemplate: #"$1["$2"]"#
            )
        }
        return value
    }

    private static func jsonString(_ value: String) -> String {
        guard let data = try? JSONSerialization.data(withJSONObject: [value]),
              let array = String(data: data, encoding: .utf8) else { return #""""# }
        return String(array.dropFirst().dropLast())
    }
}
