import Foundation
import Observation

/// The attachments of every conversation, their import and extraction work,
/// and the cached extracted text.
///
/// State lives on the main actor; copying and extraction run in detached
/// tasks. Progress is saved every few PDF pages, so after the app is closed
/// mid-extraction the attachment comes back as "interrupted" and Retry
/// resumes after the last checkpoint. Nothing here runs in the background
/// beyond what iOS allows while the app is suspended.
@MainActor
@Observable
final class DocumentLibrary {
    private(set) var documentsByConversation: [UUID: [ChatDocument]] = [:]
    /// The last import problem, for the app to show.
    var lastError: String?

    @ObservationIgnored private let store: DocumentStore
    @ObservationIgnored private let recognizer = TextRecognizer()
    @ObservationIgnored private var tasks: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private var chunkCache: [String: [DocumentChunk]] = [:]

    init(store: DocumentStore = DocumentStore()) {
        self.store = store
        var loaded = store.loadAll()
        for (conversationID, documents) in loaded {
            loaded[conversationID] = documents.map { document in
                // Work that was running when the app stopped cannot still be
                // running now: make the state consistent and offer Retry.
                var recovered = document
                switch document.status {
                case .importing:
                    recovered.status = .failed("The import was interrupted. Remove it and attach the file again.")
                case .extracting(_, let total):
                    recovered.status = .interrupted(processed: document.processedPages, total: total)
                default:
                    return document
                }
                try? store.saveMetadata(recovered)
                return recovered
            }
        }
        documentsByConversation = loaded
    }

    func documents(in conversationID: UUID?) -> [ChatDocument] {
        conversationID.flatMap { documentsByConversation[$0] } ?? []
    }

    func document(_ id: String, in conversationID: UUID) -> ChatDocument? {
        documents(in: conversationID).first { $0.id == id }
    }

    /// Extracted chunks, from memory or the on-disk cache (never re-extracted).
    func chunks(for document: ChatDocument) -> [DocumentChunk] {
        let key = Self.key(document)
        if let cached = chunkCache[key] { return cached }
        let loaded = store.loadChunks(for: document) ?? []
        chunkCache[key] = loaded
        return loaded
    }

    /// A tool runner over one conversation's attachments.
    func toolRunner(for conversationID: UUID) -> DocumentToolRunner {
        let documents = documents(in: conversationID)
        var chunks: [String: [DocumentChunk]] = [:]
        for document in documents { chunks[document.id] = self.chunks(for: document) }
        return DocumentToolRunner(documents: documents, chunksByDocument: chunks)
    }

    func originalURL(for document: ChatDocument) -> URL {
        store.originalURL(for: document)
    }

    var hasWork: Bool { !tasks.isEmpty }

    // MARK: - Import

    /// Imports files picked with the document picker.
    func importFiles(_ urls: [URL], into conversationID: UUID) {
        for url in urls {
            guard let kind = DocumentImporter.kind(for: url) else {
                lastError = DocumentImportError.unsupportedType(url.lastPathComponent).localizedDescription
                continue
            }
            guard let document = addPlaceholder(name: url.lastPathComponent, kind: kind, fileExtension: url.pathExtension, to: conversationID) else { return }
            let destination = store.originalURL(for: document)
            start(document) { [weak self] in
                let bytes = try await DocumentImporter.copy(from: url, to: destination, kind: kind)
                await self?.finishImport(document, byteCount: bytes)
            }
        }
    }

    /// Imports images (screenshots, photos) to be read with OCR.
    func importImages(_ images: [Data], into conversationID: UUID) {
        // Distinct names, so the model and the user can tell images apart.
        let stamp = Date.now.formatted(date: .omitted, time: .standard)
        for (index, data) in images.enumerated() {
            let name = images.count == 1 ? "Image \(stamp)" : "Image \(stamp) (\(index + 1))"
            guard data.count <= DocumentLimits.maxImageBytes else {
                lastError = DocumentImportError.tooLarge(.image).localizedDescription
                continue
            }
            guard let document = addPlaceholder(name: name, kind: .image, fileExtension: "img", to: conversationID) else { return }
            let store = store
            start(document) { [weak self] in
                try await Task.detached(priority: .userInitiated) {
                    try store.installOriginal(data: data, for: document)
                }.value
                await self?.finishImport(document, byteCount: data.count)
            }
        }
    }

    // MARK: - Retry and removal

    func retry(_ document: ChatDocument) {
        guard let current = self.document(document.id, in: document.conversationID), current.status.canRetry else { return }
        // A failed import left no copy to extract from; only attaching the
        // file again can fix that.
        guard FileManager.default.fileExists(atPath: store.originalURL(for: current).path) else {
            update(current) { $0.status = .failed("The file was not imported. Remove it and attach it again.") }
            return
        }
        if case .failed = current.status {
            // A failed extraction starts over; the cached text is discarded.
            update(current) {
                $0.processedPages = 0
                $0.warnings = []
                $0.notes = nil
            }
            chunkCache[Self.key(current)] = nil
            try? store.saveChunks([], for: current)
        }
        guard let fresh = self.document(document.id, in: document.conversationID) else { return }
        start(fresh) { [weak self] in
            await self?.extract(fresh)
        }
    }

    func remove(_ document: ChatDocument) {
        let key = Self.key(document)
        tasks[key]?.cancel()
        tasks[key] = nil
        chunkCache[key] = nil
        documentsByConversation[document.conversationID]?.removeAll { $0.id == document.id }
        if documentsByConversation[document.conversationID]?.isEmpty == true {
            documentsByConversation[document.conversationID] = nil
        }
        store.delete(document)
    }

    /// Cancels work and deletes every attachment, cache and index of a chat.
    func removeAll(in conversationID: UUID) {
        for document in documents(in: conversationID) {
            let key = Self.key(document)
            tasks[key]?.cancel()
            tasks[key] = nil
            chunkCache[key] = nil
        }
        documentsByConversation[conversationID] = nil
        store.deleteConversation(conversationID)
    }

    /// Deletes attachment folders whose conversation no longer exists.
    func removeOrphans(keeping conversationIDs: Set<UUID>) {
        for conversationID in store.conversationIDsOnDisk() where !conversationIDs.contains(conversationID) {
            removeAll(in: conversationID)
        }
    }

    /// Waits until no import or extraction is running (used by tests).
    func waitUntilIdle() async {
        while let task = tasks.values.first {
            await task.value
        }
    }

    // MARK: - Work

    private func addPlaceholder(name: String, kind: ChatDocumentKind, fileExtension: String, to conversationID: UUID) -> ChatDocument? {
        let existing = documents(in: conversationID)
        guard existing.count < DocumentLimits.maxDocumentsPerChat else {
            lastError = DocumentImportError.tooManyDocuments.localizedDescription
            return nil
        }
        let safeExtension = fileExtension.lowercased().filter { $0.isLetter || $0.isNumber }.prefix(8)
        let document = ChatDocument(
            id: ChatDocument.makeID(avoiding: Set(existing.map(\.id))),
            conversationID: conversationID,
            displayName: String(name.prefix(120)),
            kind: kind,
            fileExtension: safeExtension.isEmpty ? "bin" : String(safeExtension)
        )
        documentsByConversation[conversationID, default: []].append(document)
        return document
    }

    private func start(_ document: ChatDocument, _ work: @escaping @MainActor () async throws -> Void) {
        let key = Self.key(document)
        tasks[key]?.cancel()
        tasks[key] = Task { [weak self] in
            do {
                try await work()
            } catch is CancellationError {
                // Removed, or the chat was deleted: nothing left to update.
            } catch {
                self?.markFailed(document, reason: error.localizedDescription)
            }
            self?.tasks[key] = nil
        }
    }

    private func finishImport(_ document: ChatDocument, byteCount: Int) async {
        guard self.document(document.id, in: document.conversationID) != nil else { return }
        update(document) {
            $0.byteCount = byteCount
            $0.status = .extracting(processed: 0, total: 0)
        }
        await extract(document)
    }

    private func extract(_ document: ChatDocument) async {
        guard var current = self.document(document.id, in: document.conversationID) else { return }
        let total: Int
        if case .interrupted(_, let pages) = current.status { total = pages } else { total = current.pageCount ?? 0 }
        current.status = .extracting(processed: current.processedPages, total: total)
        replace(current)

        let fileURL = store.originalURL(for: current)
        let resume = current.kind == .pdf ? chunks(for: current) : []
        let store = store
        let recognizer = recognizer
        let snapshot = current
        let checkpoint: @Sendable (DocumentExtractionUpdate) async -> Void = { [weak self] update in
            try? store.saveChunks(update.chunks, for: snapshot)
            await self?.apply(update, to: snapshot, final: false)
        }
        let work = Task.detached(priority: .utility) {
            try await DocumentExtractor.extract(snapshot, fileURL: fileURL, resume: resume, recognizer: recognizer, checkpoint: checkpoint)
        }
        do {
            let result = try await withTaskCancellationHandler {
                try await work.value
            } onCancel: {
                work.cancel()
            }
            guard self.document(snapshot.id, in: snapshot.conversationID) != nil else { return }
            try? store.saveChunks(result.chunks, for: snapshot)
            apply(result, to: snapshot, final: true)
        } catch is CancellationError {
            // Removed while extracting; the folder is already gone.
        } catch {
            markFailed(snapshot, reason: "Text extraction failed: \(error.localizedDescription)")
        }
    }

    private func apply(_ update: DocumentExtractionUpdate, to document: ChatDocument, final: Bool) {
        guard self.document(document.id, in: document.conversationID) != nil else { return }
        chunkCache[Self.key(document)] = update.chunks
        self.update(document) {
            $0.status = update.status
            $0.pageCount = update.pageCount
            $0.lineCount = update.lineCount
            $0.processedPages = update.processedPages
            $0.warnings = update.warnings
            $0.notes = update.notes
        }
    }

    private func markFailed(_ document: ChatDocument, reason: String) {
        guard self.document(document.id, in: document.conversationID) != nil else { return }
        update(document) { $0.status = .failed(reason) }
    }

    private func update(_ document: ChatDocument, _ change: (inout ChatDocument) -> Void) {
        guard var current = self.document(document.id, in: document.conversationID) else { return }
        change(&current)
        replace(current)
    }

    private func replace(_ document: ChatDocument) {
        guard let index = documentsByConversation[document.conversationID]?.firstIndex(where: { $0.id == document.id }) else { return }
        documentsByConversation[document.conversationID]?[index] = document
        try? store.saveMetadata(document)
    }

    private static func key(_ document: ChatDocument) -> String {
        "\(document.conversationID.uuidString)/\(document.id)"
    }
}
