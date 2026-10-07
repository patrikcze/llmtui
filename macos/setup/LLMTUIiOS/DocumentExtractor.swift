import CoreGraphics
import Foundation
import ImageIO
import PDFKit
import UIKit
import Vision

/// The state of an extraction, reported at checkpoints and at the end.
nonisolated struct DocumentExtractionUpdate: Sendable {
    var chunks: [DocumentChunk]
    var pageCount: Int?
    var lineCount: Int?
    var processedPages: Int
    var warnings: [String]
    var notes: [String] = []
    var status: ChatDocumentStatus
}

/// Extracts searchable text from an imported attachment, entirely on device.
///
/// Runs outside the main actor. A PDF is processed one page at a time: the
/// text layer when it has one, otherwise the page is rendered at a bounded
/// resolution and read with Vision OCR. Progress is reported every few pages
/// so an interrupted extraction can resume after the last checkpoint, and
/// cancellation is checked between pages.
nonisolated enum DocumentExtractor {
    static func extract(
        _ document: ChatDocument,
        fileURL: URL,
        resume: [DocumentChunk],
        recognizer: TextRecognizer,
        checkpoint: @escaping @Sendable (DocumentExtractionUpdate) async -> Void
    ) async throws -> DocumentExtractionUpdate {
        switch document.kind {
        case .pdf:
            try await extractPDF(document, fileURL: fileURL, resume: resume, recognizer: recognizer, checkpoint: checkpoint)
        case .text, .markdown:
            try extractText(fileURL: fileURL)
        case .image:
            try await extractImage(fileURL: fileURL, recognizer: recognizer)
        }
    }

    // MARK: - PDF

    private static func extractPDF(
        _ document: ChatDocument,
        fileURL: URL,
        resume: [DocumentChunk],
        recognizer: TextRecognizer,
        checkpoint: @Sendable (DocumentExtractionUpdate) async -> Void
    ) async throws -> DocumentExtractionUpdate {
        guard let pdf = PDFDocument(url: fileURL) else {
            return failed("This PDF could not be opened. It may be damaged or not really a PDF.")
        }
        if pdf.isLocked, !pdf.unlock(withPassword: "") {
            return failed("This PDF is password-protected. Remove the password and attach it again.")
        }
        let total = pdf.pageCount
        guard total > 0 else { return failed("This PDF has no pages.") }
        let limit = min(total, DocumentLimits.maxPDFPages)

        // Resume after the last checkpoint, keeping only complete pages.
        var processed = min(max(document.processedPages, 0), limit)
        var chunks = resume.filter { ($0.page ?? .max) <= processed }
        if chunks.isEmpty { processed = 0 }
        var nextNumber = (chunks.last?.number ?? 0) + 1
        var ocrPages = Set(chunks.filter { $0.method == .ocr }.compactMap(\.page))
        var lowConfidencePages = Set(chunks.filter(\.lowConfidence).compactMap(\.page))
        var skippedOCRPages: [Int] = []
        var characters = chunks.reduce(0) { $0 + $1.text.count }
        var languageNote: String?

        func update(_ status: ChatDocumentStatus) -> DocumentExtractionUpdate {
            DocumentExtractionUpdate(
                chunks: chunks,
                pageCount: total,
                lineCount: nil,
                processedPages: processed,
                warnings: pdfWarnings(
                    total: total, limit: limit, lowConfidencePages: lowConfidencePages,
                    skippedOCRPages: skippedOCRPages, languageNote: languageNote
                ),
                notes: ocrPages.isEmpty ? [] : [
                    "\(pagesHave(ocrPages)) no text layer and were read with OCR, which reads printed text only, not pictures or diagrams."
                ],
                status: status
            )
        }

        while processed < limit {
            try Task.checkCancellation()
            let pageNumber = processed + 1
            guard let page = pdf.page(at: processed) else {
                processed += 1
                continue
            }
            var text = autoreleasepool { page.string ?? "" }
            var method = DocumentExtractionMethod.textLayer
            var lowConfidence = false
            if usefulCharacterCount(text) < 25 {
                if ocrPages.count < DocumentLimits.maxOCRPagesPerPDF {
                    if let image = render(page) {
                        let recognition = try await recognizer.recognize(image)
                        languageNote = languageNote ?? recognition.languageNote
                        if !recognition.lines.isEmpty {
                            text = recognition.lines.joined(separator: "\n")
                            method = .ocr
                            lowConfidence = recognition.confidence < 0.5
                            ocrPages.insert(pageNumber)
                            if lowConfidence { lowConfidencePages.insert(pageNumber) }
                        }
                    }
                } else {
                    skippedOCRPages.append(pageNumber)
                }
            }

            for piece in split(text, maxLength: DocumentLimits.chunkCharacters) {
                chunks.append(DocumentChunk(
                    id: "c\(nextNumber)", page: pageNumber, lineStart: nil, lineEnd: nil,
                    method: method, lowConfidence: lowConfidence, text: piece
                ))
                nextNumber += 1
                characters += piece.count
            }
            processed = pageNumber
            // Past the indexing limit there is no point reading (or OCR'ing)
            // further pages; the result is reported as partial.
            if characters >= DocumentLimits.maxIndexedCharacters { break }
            if processed % DocumentLimits.checkpointPages == 0, processed < limit {
                await checkpoint(update(.extracting(processed: processed, total: total)))
            }
        }

        if chunks.isEmpty {
            return failed(
                ocrPages.isEmpty && skippedOCRPages.isEmpty
                    ? "No text could be extracted from this PDF."
                    : "No readable text was found, even with OCR. Scans that are blurry, handwritten or mostly images may not be readable."
            )
        }
        var reasons: [String] = []
        if processed < limit { reasons.append("only the first \(processed) of \(total) pages were indexed (text size limit)") }
        if total > limit { reasons.append("only the first \(limit) of \(total) pages were processed") }
        if !skippedOCRPages.isEmpty { reasons.append("\(skippedOCRPages.count) scanned pages exceeded the OCR limit") }
        return update(reasons.isEmpty ? .ready : .partial(reasons.joined(separator: "; ").capitalizedFirst + "."))
    }

    private static func pdfWarnings(
        total: Int,
        limit: Int,
        lowConfidencePages: Set<Int>,
        skippedOCRPages: [Int],
        languageNote: String?
    ) -> [String] {
        var warnings: [String] = []
        if !lowConfidencePages.isEmpty {
            warnings.append("OCR confidence is low on \(lowConfidencePages.count == 1 ? "page" : "pages") \(ranges(lowConfidencePages)); quote those passages with care.")
        }
        if !skippedOCRPages.isEmpty {
            warnings.append("\(pagesHave(Set(skippedOCRPages))) no text layer and were not read (OCR limit of \(DocumentLimits.maxOCRPagesPerPDF) pages).")
        }
        if total > limit {
            warnings.append("Pages \(limit + 1)-\(total) were not processed (limit of \(limit) pages).")
        }
        if let languageNote { warnings.append(languageNote) }
        return warnings
    }

    /// Renders a page for OCR with its longest side at most
    /// `DocumentLimits.renderLongSide` pixels; the page's rotation is applied.
    private static func render(_ page: PDFPage) -> CGImage? {
        let bounds = page.bounds(for: .mediaBox)
        var width = bounds.width
        var height = bounds.height
        if page.rotation % 180 != 0 { swap(&width, &height) }
        guard width > 0, height > 0 else { return nil }
        let scale = min(DocumentLimits.renderLongSide / max(width, height), 4)
        let size = CGSize(width: (width * scale).rounded(), height: (height * scale).rounded())
        return autoreleasepool { page.thumbnail(of: size, for: .mediaBox).cgImage }
    }

    // MARK: - Text

    static func extractText(fileURL: URL) throws -> DocumentExtractionUpdate {
        let data = try Data(contentsOf: fileURL, options: .mappedIfSafe)
        guard data.count <= DocumentLimits.maxTextBytes else {
            return failed("This file is larger than the \(DocumentLimits.maxTextBytes / 1_048_576) MB text limit.")
        }
        let bytes = data.starts(with: [0xEF, 0xBB, 0xBF]) ? data.dropFirst(3) : data[...]
        guard let decoded = String(data: Data(bytes), encoding: .utf8) else {
            return failed("This file is not UTF-8 text. Save it with UTF-8 encoding and attach it again.")
        }
        guard !decoded.contains("\u{0}") else {
            return failed("This file contains binary data, not text.")
        }
        var lines = decoded
            .replacingOccurrences(of: "\r\n", with: "\n")
            .replacingOccurrences(of: "\r", with: "\n")
            .components(separatedBy: "\n")
        if lines.last == "" { lines.removeLast() }
        guard lines.contains(where: { !$0.trimmingCharacters(in: .whitespaces).isEmpty }) else {
            return failed("This file is empty.")
        }

        var kept = lines
        var status = ChatDocumentStatus.ready
        var characters = 0
        for (index, line) in lines.enumerated() {
            characters += line.count + 1
            if characters > DocumentLimits.maxIndexedCharacters {
                kept = Array(lines[..<index])
                status = .partial("Only the first \(index) of \(lines.count) lines were indexed (size limit).")
                break
            }
        }
        return DocumentExtractionUpdate(
            chunks: lineChunks(kept, method: .plainText, lowConfidence: false),
            pageCount: nil,
            lineCount: lines.count,
            processedPages: 0,
            warnings: [],
            status: status
        )
    }

    // MARK: - Images

    private static func extractImage(fileURL: URL, recognizer: TextRecognizer) async throws -> DocumentExtractionUpdate {
        guard let source = CGImageSourceCreateWithURL(fileURL as CFURL, nil), CGImageSourceGetCount(source) > 0 else {
            return failed("This image could not be read.")
        }
        // Decode straight to a bounded size, with the EXIF orientation
        // applied, instead of decoding the full-resolution image first.
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceThumbnailMaxPixelSize: DocumentLimits.maxImagePixels,
            kCGImageSourceShouldCacheImmediately: true
        ]
        guard let image = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else {
            return failed("This image could not be decoded.")
        }
        try Task.checkCancellation()
        let recognition = try await recognizer.recognize(image)
        guard !recognition.lines.isEmpty else {
            return failed("No text was found in this image. Image reading uses OCR: it finds printed text, not pictures, charts or diagrams.")
        }
        var warnings: [String] = []
        if recognition.confidence < 0.5 {
            warnings.append("OCR confidence is low (blurry, small, skewed or handwritten text); quote this text with care.")
        }
        if let note = recognition.languageNote { warnings.append(note) }
        return DocumentExtractionUpdate(
            chunks: lineChunks(recognition.lines, method: .ocr, lowConfidence: recognition.confidence < 0.5),
            pageCount: nil,
            lineCount: recognition.lines.count,
            processedPages: 0,
            warnings: warnings,
            notes: ["Read with OCR: only the printed text is available, not pictures, charts or layout."],
            status: .ready
        )
    }

    // MARK: - Chunking

    /// Groups lines into chunks of at most `chunkLines` lines and about
    /// `chunkCharacters` characters (a single longer line is its own chunk).
    /// Blank-only chunks are skipped; IDs are sequential.
    static func lineChunks(_ lines: [String], method: DocumentExtractionMethod, lowConfidence: Bool) -> [DocumentChunk] {
        var chunks: [DocumentChunk] = []
        var start = 0
        while start < lines.count {
            var end = start
            var length = 0
            while end < lines.count, end - start < DocumentLimits.chunkLines,
                  end == start || length + lines[end].count + 1 <= DocumentLimits.chunkCharacters {
                length += lines[end].count + 1
                end += 1
            }
            let text = lines[start..<end].joined(separator: "\n")
            if !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                chunks.append(DocumentChunk(
                    id: "c\(chunks.count + 1)", page: nil, lineStart: start + 1, lineEnd: end,
                    method: method, lowConfidence: lowConfidence, text: text
                ))
            }
            start = end
        }
        return chunks
    }

    /// Splits page text into pieces of at most `maxLength` characters at line
    /// boundaries; a longer line is split at a space where possible.
    static func split(_ text: String, maxLength: Int) -> [String] {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return [] }
        guard trimmed.count > maxLength else { return [trimmed] }
        var pieces: [String] = []
        var current = ""
        for line in trimmed.components(separatedBy: .newlines) {
            var line = line
            while line.count > maxLength {
                let head = line.prefix(maxLength)
                let cut = head.lastIndex(of: " ").map { line.distance(from: line.startIndex, to: $0) } ?? maxLength
                let size = max(cut, maxLength / 2)
                if !current.isEmpty { pieces.append(current); current = "" }
                pieces.append(String(line.prefix(size)).trimmingCharacters(in: .whitespaces))
                line = String(line.dropFirst(size))
            }
            if current.isEmpty {
                current = line
            } else if current.count + 1 + line.count <= maxLength {
                current += "\n" + line
            } else {
                pieces.append(current)
                current = line
            }
        }
        if !current.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { pieces.append(current) }
        return pieces
    }

    static func usefulCharacterCount(_ text: String) -> Int {
        text.unicodeScalars.filter { CharacterSet.alphanumerics.contains($0) }.count
    }

    /// "Page 3 has" or "Pages 3, 5-7 have".
    static func pagesHave(_ pages: Set<Int>) -> String {
        pages.count == 1 ? "Page \(ranges(pages)) has" : "Pages \(ranges(pages)) have"
    }

    /// "3, 5-7, 10" for a set of page numbers.
    static func ranges(_ pages: Set<Int>) -> String {
        var parts: [String] = []
        var run: (Int, Int)?
        for page in pages.sorted() {
            if let current = run, page == current.1 + 1 {
                run = (current.0, page)
            } else {
                if let current = run { parts.append(current.0 == current.1 ? "\(current.0)" : "\(current.0)-\(current.1)") }
                run = (page, page)
            }
        }
        if let current = run { parts.append(current.0 == current.1 ? "\(current.0)" : "\(current.0)-\(current.1)") }
        return parts.joined(separator: ", ")
    }

    private static func failed(_ reason: String) -> DocumentExtractionUpdate {
        DocumentExtractionUpdate(chunks: [], pageCount: nil, lineCount: nil, processedPages: 0, warnings: [], status: .failed(reason))
    }
}

/// The text Vision found in one image.
nonisolated struct TextRecognition: Sendable {
    let lines: [String]
    /// Mean confidence of the recognized lines, 0-1.
    let confidence: Double
    /// Set when a preferred language is not supported for OCR on this device.
    let languageNote: String?
}

/// On-device text recognition with Vision. The languages are chosen at run
/// time from the device's preferred languages that Vision supports here; when
/// none are supported it falls back to automatic language detection and says
/// so, rather than assuming any language (Czech, for example) is available.
nonisolated struct TextRecognizer: Sendable {
    func recognize(_ image: CGImage) async throws -> TextRecognition {
        var request = RecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.usesLanguageCorrection = true
        let (languages, note) = Self.languages(
            supported: request.supportedRecognitionLanguages,
            preferred: Locale.preferredLanguages
        )
        if languages.isEmpty {
            request.automaticallyDetectsLanguage = true
        } else {
            request.recognitionLanguages = languages
        }
        let observations = try await request.perform(on: image)
        var lines: [String] = []
        var confidence = 0.0
        for observation in observations {
            guard let best = observation.topCandidates(1).first else { continue }
            let text = best.string.trimmingCharacters(in: .whitespaces)
            guard !text.isEmpty else { continue }
            lines.append(text)
            confidence += Double(best.confidence)
        }
        return TextRecognition(
            lines: lines,
            confidence: lines.isEmpty ? 0 : confidence / Double(lines.count),
            languageNote: note
        )
    }

    /// The supported languages matching the preferred ones (by language code,
    /// in preference order), and a note naming preferred languages OCR does
    /// not support on this device.
    static func languages(supported: [Locale.Language], preferred: [String]) -> ([Locale.Language], String?) {
        var chosen: [Locale.Language] = []
        var missing: [String] = []
        for identifier in preferred {
            let code = Locale.Language(identifier: identifier).languageCode?.identifier
            guard let code else { continue }
            if let match = supported.first(where: { $0.languageCode?.identifier == code }) {
                if !chosen.contains(match) { chosen.append(match) }
            } else if !missing.contains(code) {
                missing.append(code)
            }
        }
        let names = missing.map { Locale.current.localizedString(forLanguageCode: $0) ?? $0 }
        let note = names.isEmpty ? nil
            : "On-device OCR does not support \(names.joined(separator: ", ")) here; text in that language may be misread."
        return (chosen, note)
    }
}

nonisolated private extension String {
    var capitalizedFirst: String { prefix(1).uppercased() + dropFirst() }
}
