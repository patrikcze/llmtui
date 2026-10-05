import SwiftUI
import WebKit
import AppKit
import UniformTypeIdentifiers

@MainActor
enum MermaidPrintImageCache {
    private static var images: [String: NSImage] = [:]

    static func image(for source: String) -> NSImage? {
        images[source]
    }

    static func store(_ image: NSImage, for source: String) {
        images[source] = image
    }
}

@MainActor
private enum MermaidSnapshotRenderer {
    static func documentSize(from value: Any?, fallbackWidth: CGFloat) -> CGSize? {
        guard let values = value as? [String: Any],
              let height = (values["height"] as? NSNumber)?.doubleValue,
              height > 0 else { return nil }

        let reportedWidth = (values["width"] as? NSNumber)?.doubleValue ?? 0
        return CGSize(
            width: max(CGFloat(reportedWidth), fallbackWidth, 1),
            height: max(CGFloat(height), 1)
        )
    }

    static func capture(from webView: WKWebView, size: CGSize, completion: @escaping (NSImage?) -> Void) {
        let configuration = WKSnapshotConfiguration()
        configuration.rect = CGRect(origin: .zero, size: size)
        webView.takeSnapshot(with: configuration) { image, _ in
            completion(image)
        }
    }
}

struct MermaidBlockView: View {
    let source: String

    @State private var webView: WKWebView?
    @State private var isExportingPNG = false
    @State private var measuredHeight: CGFloat?
    @State private var fullContentSize: CGSize?

    /// Placeholder shown only until the WebView reports its real rendered
    /// height. A line-count guess doesn't track actual diagram size — a
    /// 30-line Gantt chart can render as compactly as a 5-line flowchart —
    /// which previously left a large blank gap below shorter diagrams.
    private var estimatedHeight: CGFloat {
        let lineCount = source.components(separatedBy: .newlines).count
        return min(max(CGFloat(lineCount) * 34, 240), 720)
    }

    /// The box's visible height on screen — capped so one huge diagram can't
    /// dominate the whole chat scroll feed.
    private var displayHeight: CGFloat {
        guard let measuredHeight else { return estimatedHeight }
        return min(max(measuredHeight, 120), 720)
    }

    /// The webview's own, uncapped height. Two earlier attempts tried to
    /// snapshot a *second*, independently-sized `WKWebView` (first detached
    /// entirely, then hosted in a hidden window) to get past `displayHeight`
    /// — both failed: a detached webview never pumps its compositor at all,
    /// and a hidden second window still hit WebKit process-assertion errors
    /// in this environment. Instead, the live webview itself is simply laid
    /// out at its *real* full size — `displayHeight` only caps the visible
    /// viewport via the `ScrollView` below, so what's on screen looks the
    /// same, but `takeSnapshot` on this already-working webview now sees the
    /// whole diagram instead of a clipped 720pt slice of it.
    private var fullHeight: CGFloat {
        fullContentSize?.height ?? estimatedHeight
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("MERMAID")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
                Spacer()
                Button {
                    exportPNG()
                } label: {
                    Image(systemName: "square.and.arrow.down")
                }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .disabled(webView == nil || fullContentSize == nil || isExportingPNG)
                .help("Export diagram as PNG")
            }

            GeometryReader { proxy in
                ScrollView([.horizontal, .vertical], showsIndicators: true) {
                    MermaidWebView(source: source, capturedWebView: $webView, measuredHeight: $measuredHeight, fullContentSize: $fullContentSize)
                        .frame(
                            width: max(fullContentSize?.width ?? proxy.size.width, proxy.size.width),
                            height: fullHeight
                        )
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
                .clipShape(RoundedRectangle(cornerRadius: 8))
            }
            .frame(maxWidth: .infinity)
            .frame(height: displayHeight)
        }
        .padding(12)
        .background(Color.black.opacity(0.24), in: RoundedRectangle(cornerRadius: 10))
    }

    private func exportPNG() {
        guard let webView, let fullContentSize else { return }
        isExportingPNG = true
        MermaidSnapshotRenderer.capture(from: webView, size: fullContentSize) { image in
            isExportingPNG = false
            guard let image else { return }
            Self.savePNG(image)
        }
    }

    private static func savePNG(_ image: NSImage) {
        let panel = NSSavePanel()
        panel.allowedContentTypes = [.png]
        panel.nameFieldStringValue = "mermaid-diagram.png"
        guard panel.runModal() == .OK, let url = panel.url else { return }
        guard let tiffData = image.tiffRepresentation,
              let bitmap = NSBitmapImageRep(data: tiffData),
              let pngData = bitmap.representation(using: .png, properties: [:]) else { return }
        try? pngData.write(to: url)
    }
}

private struct MermaidWebView: NSViewRepresentable {
    let source: String
    @Binding var capturedWebView: WKWebView?
    @Binding var measuredHeight: CGFloat?
    @Binding var fullContentSize: CGSize?

    @Environment(\.colorScheme) private var colorScheme

    func makeCoordinator() -> Coordinator {
        Coordinator(measuredHeight: $measuredHeight, fullContentSize: $fullContentSize)
    }

    func makeNSView(context: Context) -> WKWebView {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .nonPersistent()
        configuration.defaultWebpagePreferences.allowsContentJavaScript = true
        configuration.preferences.javaScriptCanOpenWindowsAutomatically = false
        configuration.userContentController.add(context.coordinator, name: "heightReporter")
        let webView = MermaidScrollingWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = context.coordinator
        webView.setValue(false, forKey: "drawsBackground")
        webView.allowsMagnification = true
        DispatchQueue.main.async { capturedWebView = webView }
        return webView
    }

    func updateNSView(_ webView: WKWebView, context: Context) {
        let renderKey = "\(colorScheme)-\(source)"
        guard context.coordinator.renderKey != renderKey else { return }
        context.coordinator.renderKey = renderKey
        context.coordinator.source = source

        guard let libraryURL = Bundle.main.url(
            forResource: "mermaid.min",
            withExtension: "js"
        ),
              let runtime = try? String(contentsOf: libraryURL, encoding: .utf8) else {
            webView.loadHTMLString(Self.missingRuntimeHTML, baseURL: nil)
            return
        }

        let html = Self.document(
            source: source,
            runtime: runtime,
            isDark: colorScheme == .dark
        )
        webView.loadHTMLString(html, baseURL: nil)
    }

    static func dismantleNSView(_ webView: WKWebView, coordinator: Coordinator) {
        webView.configuration.userContentController.removeScriptMessageHandler(forName: "heightReporter")
        webView.stopLoading()
        webView.navigationDelegate = nil
    }

    static func document(
        source: String,
        runtime: String,
        isDark: Bool
    ) -> String {
        let background = isDark ? "#211f1f" : "#f6f5f4"
        let foreground = isDark ? "#f2efed" : "#242222"
        let secondary = isDark ? "#aaa5a2" : "#696461"
        let surface = isDark ? "#302d2c" : "#ffffff"
        let accent = isDark ? "#4f9cff" : "#146edb"

        return """
        <!doctype html>
        <html>
        <head>
          <meta charset="utf-8">
          <meta name="viewport" content="width=device-width, initial-scale=1">
          <meta http-equiv="Content-Security-Policy"
                content="default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; img-src data:; font-src data:">
          <style>
            :root { color-scheme: \(isDark ? "dark" : "light"); }
            * { box-sizing: border-box; }
            html, body {
              margin: 0;
              background: \(background);
              color: \(foreground);
              font-family: -apple-system, BlinkMacSystemFont, sans-serif;
              /* The webview is now sized to fit its content exactly (see
                 `MermaidBlockView.fullHeight`), so the page itself never
                 needs to scroll — scrolling is handled by the SwiftUI
                 `ScrollView` wrapping the webview instead. Without this,
                 WKWebView still claims scroll-wheel events for itself to
                 handle page rubber-banding, which stops them from ever
                 reaching that outer `ScrollView`. */
              overflow: hidden;
            }
            body { padding: 16px; }
            #diagram {
              display: flex;
              min-width: min-content;
              align-items: flex-start;
              justify-content: center;
            }
            #diagram svg {
              display: block;
              max-width: none !important;
              height: auto;
              margin: 0 auto;
            }
            #error {
              display: none;
              margin: 0;
              padding: 12px;
              color: \(secondary);
              white-space: pre-wrap;
              overflow-wrap: anywhere;
              font: 12px ui-monospace, SFMono-Regular, Menlo, monospace;
            }
          </style>
          <script>
          \(runtime.replacingOccurrences(
              of: "</script",
              with: #"<\/script"#,
              options: .caseInsensitive
          ))
          </script>
        </head>
        <body>
          <pre id="source" hidden>\(htmlEscaped(source))</pre>
          <div id="diagram" role="img" aria-label="Mermaid diagram"></div>
          <pre id="error"></pre>
          <script>
            const source = document.getElementById('source').textContent;
            const diagram = document.getElementById('diagram');
            const error = document.getElementById('error');

            mermaid.initialize({
              startOnLoad: false,
              securityLevel: 'strict',
              suppressErrorRendering: true,
              theme: 'base',
              flowchart: { htmlLabels: false, useMaxWidth: false },
              sequence: { useMaxWidth: false },
              themeVariables: {
                darkMode: \(isDark ? "true" : "false"),
                background: '\(background)',
                primaryColor: '\(surface)',
                primaryTextColor: '\(foreground)',
                primaryBorderColor: '\(accent)',
                lineColor: '\(secondary)',
                secondaryColor: '\(surface)',
                tertiaryColor: '\(background)',
                textColor: '\(foreground)',
                mainBkg: '\(surface)',
                nodeBorder: '\(accent)',
                clusterBkg: '\(background)',
                clusterBorder: '\(secondary)',
                edgeLabelBackground: '\(background)',
                actorBkg: '\(surface)',
                actorBorder: '\(accent)',
                actorTextColor: '\(foreground)',
                signalColor: '\(foreground)',
                signalTextColor: '\(foreground)',
                labelBoxBkgColor: '\(surface)',
                labelBoxBorderColor: '\(secondary)',
                labelTextColor: '\(foreground)',
                loopTextColor: '\(foreground)',
                noteBkgColor: '\(surface)',
                noteBorderColor: '\(secondary)',
                noteTextColor: '\(foreground)'
              }
            });

            function reportSize() {
              requestAnimationFrame(() => {
                const content = error.style.display === 'block' ? error : diagram;
                const bounds = content.getBoundingClientRect();
                window.webkit?.messageHandlers?.heightReporter?.postMessage({
                  width: Math.ceil(Math.max(
                    document.documentElement.scrollWidth,
                    document.body.scrollWidth,
                    bounds.width + 32
                  )),
                  height: Math.ceil(Math.max(
                    document.documentElement.scrollHeight,
                    document.body.scrollHeight,
                    bounds.height + 32
                  ))
                });
              });
            }

            mermaid.render('llmtui-mermaid-diagram', source)
              .then(({ svg, bindFunctions }) => {
                diagram.innerHTML = svg;
                bindFunctions?.(diagram);
                reportSize();
              })
              .catch(renderError => {
                diagram.style.display = 'none';
                error.style.display = 'block';
                error.textContent =
                  'Unable to render this Mermaid diagram.\\n\\n' +
                  (renderError?.message ?? String(renderError)) +
                  '\\n\\n' + source;
                reportSize();
              });
          </script>
        </body>
        </html>
        """
    }

    private static func htmlEscaped(_ value: String) -> String {
        value
            .replacingOccurrences(of: "&", with: "&amp;")
            .replacingOccurrences(of: "<", with: "&lt;")
            .replacingOccurrences(of: ">", with: "&gt;")
            .replacingOccurrences(of: "\"", with: "&quot;")
            .replacingOccurrences(of: "'", with: "&#39;")
    }

    private static let missingRuntimeHTML = """
    <!doctype html>
    <html>
    <body style="margin:0;padding:16px;background:transparent;color:#888;
                 font:13px -apple-system,sans-serif">
      Mermaid runtime is unavailable.
    </body>
    </html>
    """

    final class Coordinator: NSObject, WKNavigationDelegate, WKScriptMessageHandler {
        var renderKey: String?
        var source: String?
        @Binding var measuredHeight: CGFloat?
        @Binding var fullContentSize: CGSize?

        init(measuredHeight: Binding<CGFloat?>, fullContentSize: Binding<CGSize?>) {
            self._measuredHeight = measuredHeight
            self._fullContentSize = fullContentSize
        }

        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationAction: WKNavigationAction,
            decisionHandler: @escaping (WKNavigationActionPolicy) -> Void
        ) {
            guard let scheme = navigationAction.request.url?.scheme?.lowercased() else {
                decisionHandler(.cancel)
                return
            }
            let localSchemes = ["about", "applewebdata", "file"]
            decisionHandler(localSchemes.contains(scheme) ? .allow : .cancel)
        }

        func userContentController(_ userContentController: WKUserContentController, didReceive message: WKScriptMessage) {
            guard let size = MermaidSnapshotRenderer.documentSize(
                from: message.body,
                fallbackWidth: message.webView?.bounds.width ?? 1
            ) else { return }
            measuredHeight = min(max(size.height, 120), 720)
            fullContentSize = size

            guard let webView = message.webView, let source else { return }
            // Setting `fullContentSize` above only *schedules* a SwiftUI
            // layout pass that resizes this webview to `size` — it hasn't
            // happened yet. Give it a moment to actually apply before
            // snapshotting, or this captures the webview at its old
            // (possibly still-720pt-capped) frame.
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.1) {
                MermaidSnapshotRenderer.capture(from: webView, size: size) { image in
                    guard let image else { return }
                    MermaidPrintImageCache.store(image, for: source)
                }
            }
        }
    }
}

/// WKWebView consumes scroll-wheel events even when its HTML document cannot
/// scroll. The diagram's SwiftUI ScrollView is the viewport, so forward wheel
/// and trackpad gestures to the nearest enclosing AppKit scroll view.
private final class MermaidScrollingWebView: WKWebView {
    override func scrollWheel(with event: NSEvent) {
        var ancestor = superview

        while let view = ancestor {
            if let enclosingScrollView = view as? NSScrollView {
                enclosingScrollView.scrollWheel(with: event)
                return
            }
            ancestor = view.superview
        }

        super.scrollWheel(with: event)
    }
}

#Preview("Mermaid Flowchart") {
    MermaidBlockView(
        source: """
        flowchart LR
            input[User Input] --> mode{Agent enabled?}
            mode -->|No| chat[Ordinary Chat]
            mode -->|Yes| agent[Agent Runtime]
            agent --> tools[Tool Execution]
            tools --> response[Final Response]
            chat --> response
        """
    )
    .frame(width: 760)
    .padding()
}
