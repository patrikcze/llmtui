import SwiftUI
import AppKit

/// An image attached to a chat turn. `imageData` is already downscaled and
/// JPEG-compressed by `load(from:)`, so it's ready to base64-encode into an
/// OpenAI-compatible `image_url` content part without further processing.
struct ChatAttachment: Identifiable, Equatable, Sendable {
    let id = UUID()
    let fileName: String
    let mimeType: String
    let imageData: Data

    var dataURLString: String {
        "data:\(mimeType);base64,\(imageData.base64EncodedString())"
    }
}

enum AttachmentImportError: LocalizedError {
    case unsupportedFile

    var errorDescription: String? {
        switch self {
        case .unsupportedFile: "That file couldn't be read as an image."
        }
    }
}

extension ChatAttachment {
    /// Loads an image from disk, downscales it to at most `maxDimension` on
    /// its longest side, and re-encodes it as JPEG. Local vision models have
    /// small context budgets, so a raw phone photo would otherwise dominate
    /// the request payload and the context window.
    static func load(
        from url: URL,
        maxDimension: CGFloat = 1024,
        jpegQuality: CGFloat = 0.7
    ) throws -> ChatAttachment {
        guard let image = NSImage(contentsOf: url) else {
            throw AttachmentImportError.unsupportedFile
        }
        return try load(
            image: image,
            fileName: url.lastPathComponent,
            maxDimension: maxDimension,
            jpegQuality: jpegQuality
        )
    }

    static func load(
        from pasteboard: NSPasteboard,
        maxDimension: CGFloat = 1024,
        jpegQuality: CGFloat = 0.7
    ) throws -> ChatAttachment {
        guard let image = NSImage(pasteboard: pasteboard) else {
            throw AttachmentImportError.unsupportedFile
        }
        return try load(
            image: image,
            fileName: "Pasted Image.jpg",
            maxDimension: maxDimension,
            jpegQuality: jpegQuality
        )
    }

    static func load(
        image: NSImage,
        fileName: String,
        maxDimension: CGFloat = 1024,
        jpegQuality: CGFloat = 0.7
    ) throws -> ChatAttachment {
        let resized = image.resizedToFit(maxDimension: maxDimension)
        guard let tiff = resized.tiffRepresentation,
              let bitmap = NSBitmapImageRep(data: tiff),
              let jpegData = bitmap.representation(
                using: .jpeg,
                properties: [.compressionFactor: jpegQuality]
              ) else {
            throw AttachmentImportError.unsupportedFile
        }
        return ChatAttachment(
            fileName: fileName,
            mimeType: "image/jpeg",
            imageData: jpegData
        )
    }
}

private extension NSImage {
    func resizedToFit(maxDimension: CGFloat) -> NSImage {
        let longestSide = max(size.width, size.height)
        guard longestSide > maxDimension, longestSide > 0 else { return self }
        let scale = maxDimension / longestSide
        let newSize = NSSize(width: size.width * scale, height: size.height * scale)
        let newImage = NSImage(size: newSize)
        newImage.lockFocus()
        draw(in: NSRect(origin: .zero, size: newSize), from: .zero, operation: .copy, fraction: 1.0)
        newImage.unlockFocus()
        return newImage
    }
}

/// A small thumbnail + filename pill, used both for pending attachments in
/// the composer (with a remove button) and for attachments already sent on
/// a user message (without one).

struct SentAttachmentGallery: View {
    let attachments: [ChatAttachment]
    let onPreview: (ChatAttachment) -> Void

    private let columns = [
        GridItem(.adaptive(minimum: 180, maximum: 280), spacing: 8)
    ]

    var body: some View {
        LazyVGrid(columns: columns, alignment: .leading, spacing: 8) {
            ForEach(attachments) { attachment in
                Button {
                    onPreview(attachment)
                } label: {
                    VStack(alignment: .leading, spacing: 6) {
                        attachmentImage(attachment)
                            .frame(maxWidth: .infinity)
                            .frame(height: 180)
                            .background(.black.opacity(0.04))
                            .clipShape(RoundedRectangle(cornerRadius: 10))

                        Text(attachment.fileName)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                    .padding(6)
                    .background(.quaternary.opacity(0.35), in: RoundedRectangle(cornerRadius: 12))
                }
                .buttonStyle(.plain)
                .help("Open \(attachment.fileName) at a larger size")
                .accessibilityLabel("Preview attached image \(attachment.fileName)")
            }
        }
    }

    @ViewBuilder
    private func attachmentImage(_ attachment: ChatAttachment) -> some View {
        if let image = NSImage(data: attachment.imageData) {
            Image(nsImage: image)
                .resizable()
                .scaledToFit()
        } else {
            ContentUnavailableView(
                "Preview unavailable",
                systemImage: "photo.badge.exclamationmark"
            )
        }
    }
}

struct AttachmentPreviewSheet: View {
    let attachment: ChatAttachment
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                Text(attachment.fileName)
                    .font(.headline)
                    .lineLimit(1)
                Spacer()
                Button("Close", systemImage: "xmark") {
                    dismiss()
                }
                .labelStyle(.iconOnly)
                .keyboardShortcut(.cancelAction)
            }
            .padding()

            Divider()

            if let image = NSImage(data: attachment.imageData) {
                Image(nsImage: image)
                    .resizable()
                    .scaledToFit()
                    .padding(20)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ContentUnavailableView(
                    "Preview unavailable",
                    systemImage: "photo.badge.exclamationmark",
                    description: Text("The attached image data could not be displayed.")
                )
            }
        }
        .frame(minWidth: 700, idealWidth: 900, minHeight: 520, idealHeight: 700)
    }
}

struct AttachmentChip: View {
    let attachment: ChatAttachment
    var onRemove: (() -> Void)?

    var body: some View {
        HStack(spacing: 6) {
            thumbnail
            Text(attachment.fileName)
                .font(.caption)
                .lineLimit(1)
                .foregroundStyle(.secondary)
            if let onRemove {
                Button(action: onRemove) {
                    Image(systemName: "xmark.circle.fill")
                        .foregroundStyle(.secondary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, 8)
        .padding(.vertical, 6)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 8))
    }

    @ViewBuilder
    private var thumbnail: some View {
        if let nsImage = NSImage(data: attachment.imageData) {
            Image(nsImage: nsImage)
                .resizable()
                .aspectRatio(contentMode: .fill)
                .frame(width: 32, height: 32)
                .clipShape(RoundedRectangle(cornerRadius: 6))
        } else {
            RoundedRectangle(cornerRadius: 6)
                .fill(.quaternary)
                .frame(width: 32, height: 32)
                .overlay(Image(systemName: "photo"))
        }
    }
}
