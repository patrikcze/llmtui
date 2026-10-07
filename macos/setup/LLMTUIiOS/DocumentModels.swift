import Foundation

/// The kinds of file the document tools read. Images are read with on-device
/// OCR, so only the text printed in them is available.
nonisolated enum ChatDocumentKind: String, Codable, Sendable {
    case pdf
    case text
    case markdown
    case image

    var title: String {
        switch self {
        case .pdf: "PDF"
        case .text: "Text"
        case .markdown: "Markdown"
        case .image: "Image (OCR)"
        }
    }

    var mediaType: String {
        switch self {
        case .pdf: "application/pdf"
        case .text: "text/plain"
        case .markdown: "text/markdown"
        case .image: "image"
        }
    }

    var systemImage: String {
        switch self {
        case .pdf: "doc.richtext"
        case .text: "doc.plaintext"
        case .markdown: "doc.text"
        case .image: "text.viewfinder"
        }
    }
}

/// Where an attachment is in its lifecycle. Only `ready` means every page or
/// line was searched; the other states are reported as partial coverage,
/// never as "no matches" for text that was not processed.
nonisolated enum ChatDocumentStatus: Codable, Equatable, Sendable {
    case importing
    case extracting(processed: Int, total: Int)
    case ready
    /// Some text is indexed, but limits left part of the file unprocessed.
    case partial(String)
    case failed(String)
    /// Extraction stopped (the app was closed during it) and can resume.
    case interrupted(processed: Int, total: Int)

    var label: String {
        switch self {
        case .importing: "importing"
        case .extracting(let processed, let total):
            total > 0 ? "extracting (\(processed) of \(total) pages)" : "extracting"
        case .ready: "ready"
        case .partial: "partial"
        case .failed: "failed"
        case .interrupted(let processed, let total):
            total > 0 ? "interrupted (\(processed) of \(total) pages)" : "interrupted"
        }
    }

    var canRetry: Bool {
        switch self {
        case .failed, .interrupted: true
        default: false
        }
    }

    var isWorking: Bool {
        switch self {
        case .importing, .extracting: true
        default: false
        }
    }
}

nonisolated enum DocumentExtractionMethod: String, Codable, Sendable {
    case textLayer = "text layer"
    case ocr = "OCR"
    case plainText = "plain text"
}

/// A stable, deterministic slice of an attachment's extracted text. PDF chunks
/// never cross a page; text chunks cover a line range. IDs ("c1", "c2", …) are
/// assigned in document order, so they stay the same for an imported file.
nonisolated struct DocumentChunk: Codable, Equatable, Sendable {
    let id: String
    /// 1-based PDF page.
    let page: Int?
    /// 1-based line range for text files and OCR'd images.
    let lineStart: Int?
    let lineEnd: Int?
    let method: DocumentExtractionMethod
    let lowConfidence: Bool
    let text: String

    var location: String {
        if let page { return "page \(page)" }
        if let lineStart, let lineEnd {
            return lineStart == lineEnd ? "line \(lineStart)" : "lines \(lineStart)-\(lineEnd)"
        }
        return "text"
    }

    /// The numeric part of the ID, for resuming numbering.
    var number: Int { Int(id.dropFirst()) ?? 0 }
}

/// An attachment owned by one conversation. Its ID is opaque, short (easy for
/// local models to copy) and resolved only within that conversation.
nonisolated struct ChatDocument: Codable, Identifiable, Equatable, Sendable {
    static let idLength = 6

    let id: String
    let conversationID: UUID
    var displayName: String
    let kind: ChatDocumentKind
    let fileExtension: String
    var byteCount: Int
    let importedAt: Date
    var status: ChatDocumentStatus
    var pageCount: Int?
    var lineCount: Int?
    /// PDF pages processed so far; extraction resumes after them.
    var processedPages: Int
    var warnings: [String]

    init(
        id: String,
        conversationID: UUID,
        displayName: String,
        kind: ChatDocumentKind,
        fileExtension: String,
        byteCount: Int = 0,
        importedAt: Date = .now,
        status: ChatDocumentStatus = .importing
    ) {
        self.id = id
        self.conversationID = conversationID
        self.displayName = displayName
        self.kind = kind
        self.fileExtension = fileExtension
        self.byteCount = byteCount
        self.importedAt = importedAt
        self.status = status
        self.pageCount = nil
        self.lineCount = nil
        self.processedPages = 0
        self.warnings = []
    }

    /// "PDF, 12 pages", "Markdown, 340 lines", or just the kind.
    var sizeDescription: String {
        if let pageCount { return "\(kind.title), \(pageCount) \(pageCount == 1 ? "page" : "pages")" }
        if let lineCount { return "\(kind.title), \(lineCount) \(lineCount == 1 ? "line" : "lines")" }
        return kind.title
    }

    static func isValidID(_ id: String) -> Bool {
        id.count == idLength && id.allSatisfy { ("a"..."z").contains($0) || ("0"..."9").contains($0) }
    }

    static func makeID(avoiding existing: Set<String>) -> String {
        let alphabet = Array("abcdefghijkmnpqrstuvwxyz23456789")
        while true {
            let id = String((0..<idLength).map { _ in alphabet.randomElement()! })
            if !existing.contains(id) { return id }
        }
    }
}

/// A passage location returned to the model, which a citation may refer to.
nonisolated struct DocumentSourceRef: Hashable, Sendable {
    let documentID: String
    let chunkID: String

    /// The citation the model writes, for example `[doc:d4k9x2:c7]`.
    var token: String { "[doc:\(documentID):\(chunkID)]" }
    /// The form stored with an assistant message.
    var key: String { "\(documentID):\(chunkID)" }

    init(documentID: String, chunkID: String) {
        self.documentID = documentID
        self.chunkID = chunkID
    }

    init?(key: String) {
        let parts = key.split(separator: ":", omittingEmptySubsequences: false)
        guard parts.count == 2 else { return nil }
        self.init(documentID: String(parts[0]), chunkID: String(parts[1]))
    }
}

/// Hard limits, applied before expensive work where possible.
nonisolated enum DocumentLimits {
    static let maxPDFBytes = 50 * 1_024 * 1_024
    static let maxTextBytes = 5 * 1_024 * 1_024
    static let maxImageBytes = 25 * 1_024 * 1_024
    static let maxDocumentsPerChat = 10
    static let maxPDFPages = 300
    static let maxOCRPagesPerPDF = 40
    /// Longest side, in pixels, of a PDF page rendered for OCR.
    static let renderLongSide = 2_000.0
    /// Longest side, in pixels, an image is decoded at for OCR.
    static let maxImagePixels = 3_000
    static let maxIndexedCharacters = 2_000_000
    static let chunkCharacters = 1_200
    static let chunkLines = 40
    static let searchDefaultLimit = 5
    static let searchMaxLimit = 10
    static let excerptCharacters = 240
    static let readDefaultLimit = 1_500
    static let readMaxLimit = 4_000
    static let listMaxDocuments = 20
    /// PDF pages between progress checkpoints.
    static let checkpointPages = 5

    static func maxBytes(for kind: ChatDocumentKind) -> Int {
        switch kind {
        case .pdf: maxPDFBytes
        case .text, .markdown: maxTextBytes
        case .image: maxImageBytes
        }
    }
}
