import PDFKit
import SwiftUI
import UIKit

/// The open chat's attachments with their status, above the composer. Each
/// can be opened, retried (after a failure or interruption) or removed. The
/// footer says where the text goes: extraction is local, but the passages the
/// assistant reads are sent to the chat's provider.
struct DocumentsPanel: View {
    let model: MobileAppModel
    let open: (ChatDocument) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            ScrollView(.horizontal) {
                HStack(spacing: 8) {
                    ForEach(model.currentDocuments) { document in
                        DocumentChip(
                            document: document,
                            open: { open(document) },
                            retry: { model.retryDocument(document) },
                            remove: { model.removeDocument(document) }
                        )
                    }
                }
            }
            .scrollIndicators(.hidden)
            Text(footer)
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .padding(.horizontal)
        .padding(.top, 8)
        .background(.bar)
    }

    private var footer: String {
        let provider = model.currentConversation.flatMap { model.profile(for: $0)?.name } ?? "the model provider"
        return "Text is extracted on this iPhone. Passages the assistant searches or reads are sent to \(provider), after you allow it."
    }
}

private struct DocumentChip: View {
    let document: ChatDocument
    let open: () -> Void
    let retry: () -> Void
    let remove: () -> Void

    var body: some View {
        Menu {
            Button("Open", systemImage: "doc.viewfinder", action: open)
                .disabled(document.status == .importing)
            if document.status.canRetry {
                Button("Retry", systemImage: "arrow.clockwise", action: retry)
            }
            Button("Remove", systemImage: "trash", role: .destructive, action: remove)
        } label: {
            HStack(spacing: 8) {
                Image(systemName: document.kind.systemImage)
                    .foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 1) {
                    Text(document.displayName)
                        .font(.caption.weight(.semibold))
                        .lineLimit(1)
                    HStack(spacing: 4) {
                        if document.status.isWorking {
                            ProgressView().controlSize(.mini)
                        } else {
                            Image(systemName: statusIcon).foregroundStyle(statusColor)
                        }
                        Text(statusText)
                    }
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                }
            }
            .frame(maxWidth: 220, alignment: .leading)
            .padding(.horizontal, 10)
            .padding(.vertical, 6)
            .background(Color.secondary.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
        }
        .accessibilityLabel("\(document.displayName), \(statusText)")
    }

    private var statusText: String {
        switch document.status {
        case .importing: "Importing…"
        case .extracting(let processed, let total): total > 0 ? "Extracting \(processed)/\(total)" : "Extracting…"
        case .ready: document.warnings.isEmpty ? document.sizeDescription : "\(document.sizeDescription) · see notes"
        case .partial: "Partial · \(document.sizeDescription)"
        case .failed(let reason): reason
        case .interrupted: "Interrupted · tap to retry"
        }
    }

    private var statusIcon: String {
        switch document.status {
        case .ready: document.warnings.isEmpty ? "checkmark.circle.fill" : "exclamationmark.circle"
        case .partial, .interrupted: "exclamationmark.triangle.fill"
        case .failed: "xmark.octagon.fill"
        case .importing, .extracting: "clock"
        }
    }

    private var statusColor: Color {
        switch document.status {
        case .ready: document.warnings.isEmpty ? .green : .orange
        case .partial, .interrupted: .orange
        case .failed: .red
        case .importing, .extracting: .secondary
        }
    }
}

/// What the source viewer shows: an attachment, optionally at one passage.
struct DocumentSourceSelection: Identifiable {
    let document: ChatDocument
    let chunk: DocumentChunk?
    let fileURL: URL

    var id: String { "\(document.id):\(chunk?.id ?? "-")" }
}

/// Opens an attachment at a cited passage: a PDF at the page (with the
/// passage highlighted when it is in the text layer), a text file at the line
/// range, an image with its OCR text.
struct DocumentSourceView: View {
    let selection: DocumentSourceSelection
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            content
                .navigationTitle(selection.document.displayName)
                .navigationBarTitleDisplayMode(.inline)
                .toolbar {
                    ToolbarItem(placement: .confirmationAction) {
                        Button("Done") { dismiss() }
                    }
                }
                .safeAreaInset(edge: .top) {
                    if let header {
                        Text(header)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .frame(maxWidth: .infinity, alignment: .leading)
                            .padding(.horizontal)
                            .padding(.vertical, 6)
                            .background(.bar)
                    }
                }
        }
    }

    private var header: String? {
        var parts: [String] = []
        if let chunk = selection.chunk {
            parts.append("Cited: \(chunk.location) (\(chunk.method.rawValue)\(chunk.lowConfidence ? ", low OCR confidence" : ""))")
        }
        parts.append(contentsOf: selection.document.warnings)
        return parts.isEmpty ? nil : parts.joined(separator: "\n")
    }

    @ViewBuilder
    private var content: some View {
        switch selection.document.kind {
        case .pdf:
            PDFSourceView(url: selection.fileURL, page: selection.chunk?.page, highlight: selection.chunk)
        case .text, .markdown:
            TextSourceView(url: selection.fileURL, lineStart: selection.chunk?.lineStart, lineEnd: selection.chunk?.lineEnd)
        case .image:
            ImageSourceView(url: selection.fileURL, text: selection.chunk?.text)
        }
    }
}

private struct PDFSourceView: UIViewRepresentable {
    let url: URL
    let page: Int?
    let highlight: DocumentChunk?

    func makeUIView(context: Context) -> PDFView {
        let view = PDFView()
        view.autoScales = true
        view.displayMode = .singlePageContinuous
        guard let document = PDFDocument(url: url) else { return view }
        view.document = document
        if let page, let target = document.page(at: page - 1) {
            view.go(to: target)
            if let highlight, highlight.method == .textLayer, let selection = Self.find(highlight, on: target, in: document) {
                selection.color = .systemYellow
                view.highlightedSelections = [selection]
                view.go(to: selection)
            }
        }
        return view
    }

    func updateUIView(_ uiView: PDFView, context: Context) {}

    /// The cited passage's opening words, located on its page.
    private static func find(_ chunk: DocumentChunk, on page: PDFPage, in document: PDFDocument) -> PDFSelection? {
        let firstLine = chunk.text.split(whereSeparator: \.isNewline).first.map(String.init) ?? ""
        let words = firstLine.split(separator: " ").prefix(8).joined(separator: " ")
        guard words.count >= 8 else { return nil }
        return document.findString(words, withOptions: .caseInsensitive).first { $0.pages.contains(page) }
    }
}

private struct TextSourceView: View {
    let url: URL
    let lineStart: Int?
    let lineEnd: Int?
    @State private var lines: [String] = []

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 2) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { index, line in
                        HStack(alignment: .top, spacing: 8) {
                            Text("\(index + 1)")
                                .font(.caption.monospacedDigit())
                                .foregroundStyle(.tertiary)
                                .frame(width: 44, alignment: .trailing)
                            Text(line.isEmpty ? " " : line)
                                .font(.callout.monospaced())
                                .textSelection(.enabled)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                        .padding(.vertical, 1)
                        .background(isCited(index + 1) ? Color.yellow.opacity(0.25) : Color.clear)
                        .id(index + 1)
                    }
                }
                .padding()
            }
            .task {
                // The file was validated as UTF-8 at import.
                let text = (try? String(contentsOf: url, encoding: .utf8)) ?? ""
                lines = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n")
                if let lineStart {
                    try? await Task.sleep(for: .milliseconds(50))
                    proxy.scrollTo(lineStart, anchor: .top)
                }
            }
        }
    }

    private func isCited(_ line: Int) -> Bool {
        guard let lineStart, let lineEnd else { return false }
        return (lineStart...lineEnd).contains(line)
    }
}

private struct ImageSourceView: View {
    let url: URL
    let text: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                if let image = UIImage(contentsOfFile: url.path) {
                    Image(uiImage: image)
                        .resizable()
                        .scaledToFit()
                        .clipShape(RoundedRectangle(cornerRadius: 12))
                }
                if let text {
                    Text("Recognized text (OCR)")
                        .font(.headline)
                    Text(text)
                        .font(.callout.monospaced())
                        .textSelection(.enabled)
                }
            }
            .padding()
        }
    }
}
