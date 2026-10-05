import AppKit
import SwiftUI
import WebKit

struct LatexMathView: View {
    let source: String
    let isDisplay: Bool

    @State private var measuredHeight: CGFloat

    init(source: String, isDisplay: Bool) {
        self.source = source
        self.isDisplay = isDisplay
        _measuredHeight = State(initialValue: isDisplay ? 52 : 28)
    }

    var body: some View {
        KaTeXWebView(
            bodyHTML: KaTeXHTML.math(source: source),
            isDisplay: isDisplay,
            fontSize: isDisplay ? 17 : 15,
            foreground: .primary,
            measuredHeight: $measuredHeight
        )
        .frame(maxWidth: .infinity, alignment: isDisplay ? .center : .leading)
        .frame(height: measuredHeight)
    }
}

struct MathAwareText: View {
    let content: AttributedString
    var fontSize: CGFloat = 15
    var fontWeight: Int = 400
    var foreground: KaTeXForeground = .primary

    @State private var measuredHeight: CGFloat = 24

    private var containsMath: Bool {
        LatexSource.inlineRanges(in: String(content.characters)).isEmpty == false
    }

    var body: some View {
        if containsMath {
            KaTeXWebView(
                bodyHTML: KaTeXHTML.richText(content),
                isDisplay: false,
                fontSize: fontSize,
                fontWeight: fontWeight,
                foreground: foreground,
                measuredHeight: $measuredHeight
            )
            .frame(maxWidth: .infinity, alignment: .leading)
            .frame(height: measuredHeight)
        } else {
            Text(content)
        }
    }
}

enum KaTeXForeground {
    case primary
    case secondary
}

enum LatexSource {
    static func inlineRanges(in source: String) -> [Range<String.Index>] {
        guard let expression = try? NSRegularExpression(
            pattern: #"(?<!\\)\$(?!\$)(?:\\.|[^$\n])+?(?<!\\)\$|\\\((?:\\.|[^\n])+?\\\)"#
        ) else { return [] }
        let range = NSRange(source.startIndex..<source.endIndex, in: source)
        return expression.matches(in: source, range: range).compactMap {
            Range($0.range, in: source)
        }
    }

    static func expression(from delimitedSource: String) -> String {
        var value = delimitedSource.trimmingCharacters(in: .whitespacesAndNewlines)
        let pairs = [("$$", "$$"), ("\\[", "\\]"), ("\\(", "\\)"), ("$", "$")]
        for (opening, closing) in pairs where value.hasPrefix(opening) && value.hasSuffix(closing) {
            value.removeFirst(opening.count)
            value.removeLast(closing.count)
            break
        }
        return value.trimmingCharacters(in: .whitespacesAndNewlines)
    }
}

private enum KaTeXHTML {
    static func math(source: String) -> String {
        #"<span class="math-source">"# + htmlEscaped(LatexSource.expression(from: source)) + "</span>"
    }

    static func richText(_ content: AttributedString) -> String {
        let plainText = String(content.characters)
        let mathRanges = LatexSource.inlineRanges(in: plainText)
        var result = ""
        var cursor = plainText.startIndex

        for mathRange in mathRanges {
            if cursor < mathRange.lowerBound {
                result += styledHTML(
                    content,
                    stringRange: cursor..<mathRange.lowerBound,
                    in: plainText
                )
            }
            let expression = LatexSource.expression(from: String(plainText[mathRange]))
            result += #"<span class="math-source">"# + htmlEscaped(expression) + "</span>"
            cursor = mathRange.upperBound
        }

        if cursor < plainText.endIndex {
            result += styledHTML(content, stringRange: cursor..<plainText.endIndex, in: plainText)
        }
        return result
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
            let intent = run.inlinePresentationIntent
            if intent?.contains(.code) == true {
                fragment = "<code>\(fragment)</code>"
            }
            if intent?.contains(.stronglyEmphasized) == true {
                fragment = "<strong>\(fragment)</strong>"
            }
            if intent?.contains(.emphasized) == true {
                fragment = "<em>\(fragment)</em>"
            }
            if intent?.contains(.strikethrough) == true {
                fragment = "<s>\(fragment)</s>"
            }
            if let link = run.link,
               let scheme = link.scheme?.lowercased(),
               scheme == "http" || scheme == "https" {
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
}

private struct KaTeXWebView: NSViewRepresentable {
    let bodyHTML: String
    let isDisplay: Bool
    let fontSize: CGFloat
    var fontWeight: Int = 400
    let foreground: KaTeXForeground
    @Binding var measuredHeight: CGFloat

    @Environment(\.colorScheme) private var colorScheme

    func makeCoordinator() -> Coordinator {
        Coordinator(measuredHeight: $measuredHeight)
    }

    func makeNSView(context: Context) -> WKWebView {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        configuration.defaultWebpagePreferences.allowsContentJavaScript = true
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        configuration.userContentController.add(context.coordinator, name: "contentHeight")

        let webView = WKWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = context.coordinator
        webView.setValue(false, forKey: "drawsBackground")
        return webView
    }

    func updateNSView(_ webView: WKWebView, context: Context) {
        let renderKey = "\(colorScheme)-\(isDisplay)-\(fontSize)-\(fontWeight)-\(foreground)-\(bodyHTML)"
        guard context.coordinator.renderKey != renderKey else { return }
        context.coordinator.renderKey = renderKey

        guard let resources = KaTeXResources.shared else {
            webView.loadHTMLString(Self.missingRuntimeHTML, baseURL: nil)
            return
        }

        let html = Self.document(
            bodyHTML: bodyHTML,
            runtime: resources.runtime,
            stylesheet: resources.stylesheet,
            isDisplay: isDisplay,
            fontSize: fontSize,
            fontWeight: fontWeight,
            foreground: foreground,
            isDark: colorScheme == .dark
        )
        webView.loadHTMLString(html, baseURL: Bundle.main.resourceURL)
    }

    static func dismantleNSView(_ webView: WKWebView, coordinator: Coordinator) {
        webView.stopLoading()
        webView.navigationDelegate = nil
        webView.configuration.userContentController.removeScriptMessageHandler(forName: "contentHeight")
    }

    private static func document(
        bodyHTML: String,
        runtime: String,
        stylesheet: String,
        isDisplay: Bool,
        fontSize: CGFloat,
        fontWeight: Int,
        foreground: KaTeXForeground,
        isDark: Bool
    ) -> String {
        let primary = isDark ? "#f2efed" : "#242222"
        let secondary = isDark ? "#aaa5a2" : "#696461"
        let textColor = foreground == .primary ? primary : secondary

        return """
        <!doctype html>
        <html>
        <head>
          <meta charset="utf-8">
          <meta name="viewport" content="width=device-width, initial-scale=1">
          <meta http-equiv="Content-Security-Policy"
                content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; font-src file: data:; img-src data:">
          <style>
          \(stylesheet)
          :root { color-scheme: \(isDark ? "dark" : "light"); }
          * { box-sizing: border-box; }
          html, body { margin: 0; padding: 0; background: transparent; color: \(textColor); }
          body {
            overflow: hidden;
            font: \(fontWeight) \(fontSize)px/1.35 -apple-system, BlinkMacSystemFont, sans-serif;
            text-align: \(isDisplay ? "center" : "left");
            overflow-wrap: anywhere;
          }
          #content { display: \(isDisplay ? "block" : "inline"); }
          .katex-display { margin: 4px 0; overflow-x: auto; overflow-y: hidden; }
          code { padding: 1px 4px; border-radius: 4px; background: rgba(128,128,128,.16); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; }
          a { color: #2688e8; text-decoration: underline; }
          #error { display: none; color: \(secondary); font-family: ui-monospace, SFMono-Regular, Menlo, monospace; white-space: pre-wrap; }
          </style>
          <script>
          \(runtime.replacingOccurrences(of: "</script", with: #"<\/script"#, options: .caseInsensitive))
          </script>
        </head>
        <body>
          <span id="content">\(bodyHTML)</span>
          <span id="error"></span>
          <script>
            const content = document.getElementById('content');
            const error = document.getElementById('error');
            const displayMode = \(isDisplay ? "true" : "false");

            for (const element of content.querySelectorAll('.math-source')) {
              const source = element.textContent;
              try {
                katex.render(source, element, {
                  displayMode,
                  throwOnError: true,
                  strict: 'warn',
                  trust: false,
                  output: 'htmlAndMathml'
                });
              } catch (renderError) {
                element.textContent = source;
                element.style.fontFamily = 'ui-monospace, SFMono-Regular, Menlo, monospace';
                element.title = renderError?.message ?? String(renderError);
                if (displayMode) {
                  error.style.display = 'block';
                  error.textContent = 'Unable to render this LaTeX expression.\\n' +
                    (renderError?.message ?? String(renderError));
                }
              }
            }

            const reportHeight = () => {
              const height = Math.ceil(document.documentElement.scrollHeight);
              window.webkit.messageHandlers.contentHeight.postMessage(height);
            };
            new ResizeObserver(reportHeight).observe(document.body);
            document.fonts.ready.then(reportHeight);
            requestAnimationFrame(reportHeight);
          </script>
        </body>
        </html>
        """
    }

    private static let missingRuntimeHTML = """
    <!doctype html><html><body style="margin:0;color:#888;font:13px -apple-system,sans-serif">
    LaTeX renderer is unavailable.
    </body></html>
    """

    final class Coordinator: NSObject, WKNavigationDelegate, WKScriptMessageHandler {
        var renderKey: String?
        private var measuredHeight: Binding<CGFloat>

        init(measuredHeight: Binding<CGFloat>) {
            self.measuredHeight = measuredHeight
        }

        func userContentController(
            _ userContentController: WKUserContentController,
            didReceive message: WKScriptMessage
        ) {
            guard message.name == "contentHeight",
                  let number = message.body as? NSNumber else { return }
            let height = max(CGFloat(truncating: number), 20)
            if abs(measuredHeight.wrappedValue - height) > 0.5 {
                measuredHeight.wrappedValue = height
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
            if scheme == "http" || scheme == "https" {
                NSWorkspace.shared.open(url)
                decisionHandler(.cancel)
                return
            }
            decisionHandler(["about", "applewebdata", "file"].contains(scheme) ? .allow : .cancel)
        }
    }
}

private struct KaTeXResources {
    let runtime: String
    let stylesheet: String

    static let shared: KaTeXResources? = {
        guard let runtimeURL = resourceURL(name: "katex.min", extension: "js"),
              let stylesheetURL = resourceURL(name: "katex.min", extension: "css"),
              let runtime = try? String(contentsOf: runtimeURL, encoding: .utf8),
              var stylesheet = try? String(contentsOf: stylesheetURL, encoding: .utf8) else {
            return nil
        }
        // Xcode flattens synchronized resource groups into the app bundle.
        stylesheet = stylesheet.replacingOccurrences(of: "url(fonts/", with: "url(")
        return KaTeXResources(runtime: runtime, stylesheet: stylesheet)
    }()

    private static func resourceURL(name: String, extension fileExtension: String) -> URL? {
        Bundle.main.url(forResource: name, withExtension: fileExtension)
            ?? Bundle.main.url(
                forResource: name,
                withExtension: fileExtension,
                subdirectory: "Rendering/KaTeX"
            )
    }
}
