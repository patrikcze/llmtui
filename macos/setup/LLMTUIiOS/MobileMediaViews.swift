import SwiftUI
import UIKit
import WebKit

// MARK: - Remote image policy

/// Whether images in a reply load automatically. A reply that read
/// attachment or web content gets `false`: text from a document or page
/// could tell the model to write an image whose URL carries chat data to
/// another server, and loading it would send that request without anyone
/// choosing to. Such images wait for a tap that names the host first.
private struct AutoloadRemoteImagesKey: EnvironmentKey {
    static let defaultValue = true
}

extension EnvironmentValues {
    var autoloadRemoteImages: Bool {
        get { self[AutoloadRemoteImagesKey.self] }
        set { self[AutoloadRemoteImagesKey.self] = newValue }
    }
}

enum RemoteImagePolicy {
    /// Only http and https images are ever fetched.
    static func url(from raw: String) -> URL? {
        guard let url = URL(string: raw.trimmingCharacters(in: .whitespacesAndNewlines)),
              let scheme = url.scheme?.lowercased(), ["http", "https"].contains(scheme),
              url.host() != nil else { return nil }
        return url
    }

    /// Tools whose results bring outside text into a reply.
    static func readsUntrustedContent(_ activities: [MobileToolActivity]) -> Bool {
        activities.contains { $0.name.hasPrefix("web_") || $0.name.hasPrefix("document_") }
    }
}

// MARK: - Markdown images

/// An image from a reply's `![alt](url)` or `<img src>`, scaled to fit.
/// Tapping it opens a zoomable viewer with Share and Open in Safari.
struct MobileMarkdownImageView: View {
    let source: String
    let alt: String
    @Environment(\.autoloadRemoteImages) private var autoload
    @State private var approved = false
    @State private var showViewer = false

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let url = RemoteImagePolicy.url(from: source) {
                if autoload || approved {
                    Button { showViewer = true } label: {
                        AsyncImage(url: url) { phase in
                            switch phase {
                            case .success(let image):
                                image
                                    .resizable()
                                    .scaledToFit()
                                    .frame(maxWidth: .infinity, maxHeight: 360)
                                    .clipShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
                            case .failure:
                                placeholder(icon: "photo.badge.exclamationmark", text: "The image could not be loaded.", url: url)
                            default:
                                RoundedRectangle(cornerRadius: 14, style: .continuous)
                                    .fill(Theme.raisedSurface)
                                    .frame(height: 160)
                                    .overlay(ProgressView())
                            }
                        }
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(alt.isEmpty ? "Image" : alt)
                    .accessibilityHint("Opens the image")
                    .sheet(isPresented: $showViewer) {
                        ImageViewer(url: url, title: alt)
                    }
                } else {
                    Button { approved = true } label: {
                        placeholder(
                            icon: "photo.badge.arrow.down",
                            text: "Tap to load the image from \(url.host() ?? "the web"). This reply read outside content, so it is not loaded automatically.",
                            url: nil
                        )
                    }
                    .buttonStyle(.plain)
                }
            } else {
                placeholder(icon: "photo.badge.exclamationmark", text: "Only web (http or https) images can be shown.", url: nil)
            }
            if !alt.isEmpty {
                Text(alt)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private func placeholder(icon: String, text: String, url: URL?) -> some View {
        HStack(alignment: .top, spacing: 10) {
            IconTile(systemName: icon, tint: Theme.warning, size: 30)
            VStack(alignment: .leading, spacing: 4) {
                Text(text)
                    .font(.callout)
                    .foregroundStyle(.primary)
                if let url {
                    Link("Open in Safari", destination: url)
                        .font(.callout.weight(.semibold))
                }
            }
            Spacer(minLength: 0)
        }
        .padding(12)
        .background(Theme.raisedSurface, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }
}

/// A full-screen, zoomable image with Share (Save Image, Files, AirDrop)
/// and Open in Safari.
private struct ImageViewer: View {
    let url: URL
    let title: String
    @Environment(\.dismiss) private var dismiss
    @State private var image: UIImage?
    @State private var failed = false
    @State private var zoom: CGFloat = 1
    @State private var lastZoom: CGFloat = 1
    @State private var sharing: ShareItem?

    var body: some View {
        NavigationStack {
            ZStack {
                Color.black.ignoresSafeArea()
                if let image {
                    ScrollView([.horizontal, .vertical]) {
                        Image(uiImage: image)
                            .resizable()
                            .scaledToFit()
                            .frame(
                                width: UIScreen.main.bounds.width * zoom,
                                height: UIScreen.main.bounds.height * 0.75 * zoom
                            )
                    }
                    .gesture(
                        MagnifyGesture()
                            .onChanged { zoom = min(max(lastZoom * $0.magnification, 1), 6) }
                            .onEnded { _ in lastZoom = zoom }
                    )
                    .onTapGesture(count: 2) {
                        withAnimation(.snappy) { zoom = zoom > 1 ? 1 : 2.5 }
                        lastZoom = zoom
                    }
                } else if failed {
                    ContentUnavailableView("Image Unavailable", systemImage: "photo.badge.exclamationmark",
                                           description: Text("The image could not be downloaded."))
                } else {
                    ProgressView().tint(.white)
                }
            }
            .navigationTitle(title.isEmpty ? (url.host() ?? "Image") : title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbarBackground(.visible, for: .navigationBar)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Done") { dismiss() }
                }
                ToolbarItemGroup(placement: .primaryAction) {
                    Link(destination: url) { Image(systemName: "safari") }
                        .accessibilityLabel("Open in Safari")
                    Button("Share", systemImage: "square.and.arrow.up") {
                        if let file = image.flatMap({ ShareItem.pngFile($0, name: "Image") }) { sharing = file }
                    }
                    .disabled(image == nil)
                }
            }
            .sheet(item: $sharing) { item in
                ActivityView(items: [item.url])
                    .presentationDetents([.medium, .large])
            }
            .task { await load() }
        }
    }

    private func load() async {
        do {
            let (data, response) = try await URLSession.shared.data(from: url)
            guard (response as? HTTPURLResponse).map({ (200..<300).contains($0.statusCode) }) ?? false,
                  data.count <= 25 * 1_024 * 1_024,
                  let decoded = UIImage(data: data) else {
                failed = true
                return
            }
            image = decoded
        } catch {
            failed = true
        }
    }
}

// MARK: - Mermaid

/// A weak reference to a rendered web view, so its diagram can be exported.
final class WebViewHandle {
    weak var webView: WKWebView?
}

/// A Mermaid diagram with an Export PNG button, like the macOS app.
struct MobileMermaidBlock: View {
    let source: String
    @State private var handle = WebViewHandle()
    @State private var exporting = false
    @State private var sharing: ShareItem?
    @State private var exportFailed = false
    @Environment(\.colorScheme) private var colorScheme

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("MERMAID")
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
                Spacer()
                if exporting {
                    ProgressView().controlSize(.small)
                } else {
                    Button("Export PNG", systemImage: "square.and.arrow.up") { export() }
                        .labelStyle(.titleAndIcon)
                        .font(.caption.weight(.semibold))
                        .buttonStyle(.borderless)
                }
            }
            MobileWebRenderView(kind: .mermaid, source: source, handle: handle)
        }
        .padding(12)
        .background(Theme.raisedSurface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
        .sheet(item: $sharing) { item in
            ActivityView(items: [item.url])
                .presentationDetents([.medium, .large])
        }
        .alert("The diagram could not be exported.", isPresented: $exportFailed) {
            Button("OK", role: .cancel) {}
        }
    }

    private func export() {
        guard let webView = handle.webView else {
            exportFailed = true
            return
        }
        exporting = true
        let background: UIColor = colorScheme == .dark ? UIColor(hex: 0x17171A) : .white
        Task {
            defer { exporting = false }
            do {
                let pdf = try await webView.pdf(configuration: WKPDFConfiguration())
                guard let image = MermaidExport.image(fromPDF: pdf, background: background),
                      let file = ShareItem.pngFile(image, name: "Mermaid Diagram") else {
                    exportFailed = true
                    return
                }
                sharing = file
            } catch {
                exportFailed = true
            }
        }
    }
}

enum MermaidExport {
    /// The longest side of an exported PNG, in pixels.
    static let maxPixels: CGFloat = 4_096

    /// Rasterizes every page of a web view's PDF (the whole diagram, not
    /// just the visible part) onto one image at up to 3x, on `background`.
    static func image(fromPDF data: Data, background: UIColor) -> UIImage? {
        guard let provider = CGDataProvider(data: data as CFData),
              let document = CGPDFDocument(provider), document.numberOfPages > 0 else { return nil }
        let pages = (1...document.numberOfPages).compactMap { document.page(at: $0) }
        let boxes = pages.map { $0.getBoxRect(.mediaBox) }
        let width = boxes.map(\.width).max() ?? 0
        let height = boxes.map(\.height).reduce(0, +)
        guard width > 0, height > 0 else { return nil }
        let scale = min(3, maxPixels / max(width, height))

        let format = UIGraphicsImageRendererFormat()
        format.scale = scale
        format.opaque = true
        let size = CGSize(width: width, height: height)
        return UIGraphicsImageRenderer(size: size, format: format).image { context in
            background.setFill()
            context.fill(CGRect(origin: .zero, size: size))
            let cg = context.cgContext
            var top: CGFloat = 0
            for (page, box) in zip(pages, boxes) {
                cg.saveGState()
                // PDF space has its origin bottom-left; flip into place.
                cg.translateBy(x: 0, y: top + box.height)
                cg.scaleBy(x: 1, y: -1)
                cg.drawPDFPage(page)
                cg.restoreGState()
                top += box.height
            }
        }
    }
}

// MARK: - Sharing

/// A file prepared for the share sheet.
struct ShareItem: Identifiable {
    let url: URL
    var id: URL { url }

    /// Writes `image` as a PNG in a temporary folder (cleared by iOS).
    static func pngFile(_ image: UIImage, name: String) -> ShareItem? {
        guard let data = image.pngData() else { return nil }
        let folder = FileManager.default.temporaryDirectory.appending(path: "Share-\(UUID().uuidString)")
        let url = folder.appending(path: "\(name).png")
        do {
            try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
            try data.write(to: url, options: .atomic)
            return ShareItem(url: url)
        } catch {
            return nil
        }
    }
}

/// The system share sheet (Save Image, Save to Files, AirDrop, Copy, …).
struct ActivityView: UIViewControllerRepresentable {
    let items: [Any]

    func makeUIViewController(context: Context) -> UIActivityViewController {
        UIActivityViewController(activityItems: items, applicationActivities: nil)
    }

    func updateUIViewController(_ controller: UIActivityViewController, context: Context) {}
}

// MARK: - Copy and share a reply

/// Copy and Share buttons under a finished reply. Citation links become
/// plain "(Report.pdf, page 2)" labels in the copied text.
struct ReplyActions: View {
    let text: String
    @State private var copied = false

    var body: some View {
        HStack(spacing: 16) {
            Button {
                UIPasteboard.general.string = text
                copied = true
                Task {
                    try? await Task.sleep(for: .seconds(1.5))
                    copied = false
                }
            } label: {
                Label(copied ? "Copied" : "Copy", systemImage: copied ? "checkmark" : "doc.on.doc")
            }
            .foregroundStyle(copied ? Theme.success : .secondary)
            ShareLink(item: text) {
                Label("Share", systemImage: "square.and.arrow.up")
            }
            .foregroundStyle(.secondary)
            Spacer()
        }
        .font(.caption.weight(.semibold))
        .buttonStyle(.borderless)
        .sensoryFeedback(.success, trigger: copied) { _, new in new }
    }
}
