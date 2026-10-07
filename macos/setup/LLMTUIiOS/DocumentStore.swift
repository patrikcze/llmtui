import Foundation
import UniformTypeIdentifiers

/// Conversation-owned attachment storage:
///
///     Application Support/Attachments/<conversation>/<document>/
///         original.<ext>   read-only copy of the imported file
///         document.json    metadata and status
///         chunks.json      cached extraction (pages or lines), so searches
///                          and follow-ups never repeat OCR
///
/// Everything is written atomically with the same data protection as saved
/// chats. Deleting a conversation deletes its folder.
nonisolated struct DocumentStore: Sendable {
    let root: URL

    init(root: URL? = nil) {
        self.root = root ?? URL.applicationSupportDirectory.appending(path: "Attachments", directoryHint: .isDirectory)
    }

    func directory(for conversationID: UUID) -> URL {
        root.appending(path: conversationID.uuidString, directoryHint: .isDirectory)
    }

    func directory(for document: ChatDocument) -> URL {
        directory(for: document.conversationID).appending(path: document.id, directoryHint: .isDirectory)
    }

    func originalURL(for document: ChatDocument) -> URL {
        directory(for: document).appending(path: "original.\(document.fileExtension)")
    }

    /// Every saved attachment, grouped by conversation, in import order.
    func loadAll() -> [UUID: [ChatDocument]] {
        let manager = FileManager.default
        guard let conversations = try? manager.contentsOfDirectory(at: root, includingPropertiesForKeys: nil) else {
            return [:]
        }
        var result: [UUID: [ChatDocument]] = [:]
        let decoder = JSONDecoder()
        for folder in conversations {
            guard let conversationID = UUID(uuidString: folder.lastPathComponent),
                  let documents = try? manager.contentsOfDirectory(at: folder, includingPropertiesForKeys: nil) else { continue }
            let loaded = documents.compactMap { url -> ChatDocument? in
                guard let data = try? Data(contentsOf: url.appending(path: "document.json")) else { return nil }
                return try? decoder.decode(ChatDocument.self, from: data)
            }
            .filter { $0.conversationID == conversationID }
            .sorted { $0.importedAt < $1.importedAt }
            if !loaded.isEmpty { result[conversationID] = loaded }
        }
        return result
    }

    func conversationIDsOnDisk() -> [UUID] {
        (try? FileManager.default.contentsOfDirectory(at: root, includingPropertiesForKeys: nil))?
            .compactMap { UUID(uuidString: $0.lastPathComponent) } ?? []
    }

    func saveMetadata(_ document: ChatDocument) throws {
        try Self.write(JSONEncoder().encode(document), to: directory(for: document).appending(path: "document.json"))
    }

    func loadChunks(for document: ChatDocument) -> [DocumentChunk]? {
        guard let data = try? Data(contentsOf: directory(for: document).appending(path: "chunks.json")) else { return nil }
        return try? JSONDecoder().decode([DocumentChunk].self, from: data)
    }

    func saveChunks(_ chunks: [DocumentChunk], for document: ChatDocument) throws {
        try Self.write(JSONEncoder().encode(chunks), to: directory(for: document).appending(path: "chunks.json"))
    }

    func delete(_ document: ChatDocument) {
        Self.removeTree(directory(for: document))
    }

    func deleteConversation(_ conversationID: UUID) {
        Self.removeTree(directory(for: conversationID))
    }

    /// Writes `data` as the document's read-only original.
    func installOriginal(data: Data, for document: ChatDocument) throws {
        let destination = originalURL(for: document)
        try Self.write(data, to: destination)
        try Self.makeReadOnly(destination)
    }

    static func write(_ data: Data, to url: URL) throws {
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try data.write(to: url, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
    }

    static func makeReadOnly(_ url: URL) throws {
        try FileManager.default.setAttributes(
            [.posixPermissions: 0o444, .protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
            ofItemAtPath: url.path
        )
    }

    /// Removes a folder, including read-only files inside it.
    private static func removeTree(_ url: URL) {
        let manager = FileManager.default
        if let enumerator = manager.enumerator(at: url, includingPropertiesForKeys: [.isDirectoryKey]) {
            for case let item as URL in enumerator {
                // Directories need their execute bit to be traversed and emptied.
                let isDirectory = (try? item.resourceValues(forKeys: [.isDirectoryKey]).isDirectory) == true
                try? manager.setAttributes([.posixPermissions: isDirectory ? 0o755 : 0o644], ofItemAtPath: item.path)
            }
        }
        try? manager.removeItem(at: url)
    }
}

nonisolated enum DocumentImportError: LocalizedError, Equatable {
    case unsupportedType(String)
    case tooLarge(ChatDocumentKind)
    case unreadable
    case tooManyDocuments

    var errorDescription: String? {
        switch self {
        case .unsupportedType(let name):
            "“\(name)” is not a PDF, plain-text, Markdown or image file."
        case .tooLarge(let kind):
            "This \(kind.title) is larger than the \(DocumentLimits.maxBytes(for: kind) / 1_048_576) MB limit."
        case .unreadable:
            "The file could not be read. If it is stored in the cloud, make sure it has downloaded, then try again."
        case .tooManyDocuments:
            "A chat can hold up to \(DocumentLimits.maxDocumentsPerChat) attachments. Remove one first."
        }
    }
}

/// Imports external files into conversation-owned storage.
nonisolated enum DocumentImporter {
    /// The document kind for a file, from its type identifier or extension.
    static func kind(for url: URL) -> ChatDocumentKind? {
        let type = (try? url.resourceValues(forKeys: [.contentTypeKey]))?.contentType
            ?? UTType(filenameExtension: url.pathExtension)
        guard let type else { return nil }
        if type.conforms(to: .pdf) { return .pdf }
        if let markdown = UTType("net.daringfireball.markdown"), type.conforms(to: markdown) { return .markdown }
        if ["md", "markdown"].contains(url.pathExtension.lowercased()) { return .markdown }
        if type.conforms(to: .image) { return .image }
        // Plain text only: RTF, HTML and other rich text are not supported.
        if type.conforms(to: .plainText) { return .text }
        return nil
    }

    /// Copies an external file to `destination` as an immutable, protected
    /// copy and returns its size. Uses security-scoped access and file
    /// coordination (which waits for a file provider to download the file),
    /// checks the size before copying, and stops waiting when cancelled.
    static func copy(from url: URL, to destination: URL, kind: ChatDocumentKind) async throws -> Int {
        let coordinator = CoordinatorBox()
        let maxBytes = DocumentLimits.maxBytes(for: kind)
        let work = Task.detached(priority: .userInitiated) { () throws -> Int in
            let accessing = url.startAccessingSecurityScopedResource()
            defer { if accessing { url.stopAccessingSecurityScopedResource() } }
            var coordinationError: NSError?
            var outcome: Result<Int, Error> = .failure(DocumentImportError.unreadable)
            coordinator.value.coordinate(readingItemAt: url, options: [.withoutChanges], error: &coordinationError) { readURL in
                outcome = Result { try copyChecked(readURL, to: destination, kind: kind, maxBytes: maxBytes) }
            }
            if let coordinationError {
                if coordinationError.domain == NSCocoaErrorDomain, coordinationError.code == NSUserCancelledError {
                    throw CancellationError()
                }
                throw DocumentImportError.unreadable
            }
            return try outcome.get()
        }
        return try await withTaskCancellationHandler {
            try await work.value
        } onCancel: {
            coordinator.value.cancel()
            work.cancel()
        }
    }

    private static func copyChecked(_ source: URL, to destination: URL, kind: ChatDocumentKind, maxBytes: Int) throws -> Int {
        let values = try? source.resourceValues(forKeys: [.fileSizeKey, .isRegularFileKey, .isSymbolicLinkKey])
        // Only a regular file is copied; a symbolic link would make the
        // "copy" point back outside the app.
        guard values?.isRegularFile != false, values?.isSymbolicLink != true else { throw DocumentImportError.unreadable }
        if let size = values?.fileSize, size > maxBytes { throw DocumentImportError.tooLarge(kind) }
        try Task.checkCancellation()

        let manager = FileManager.default
        let folder = destination.deletingLastPathComponent()
        try manager.createDirectory(at: folder, withIntermediateDirectories: true)
        let temporary = folder.appending(path: ".import-\(UUID().uuidString)")
        do {
            try manager.copyItem(at: source, to: temporary)
        } catch {
            throw DocumentImportError.unreadable
        }
        let copied = (try? manager.attributesOfItem(atPath: temporary.path)[.size] as? NSNumber)?.intValue ?? 0
        guard copied <= maxBytes else {
            try? manager.removeItem(at: temporary)
            throw DocumentImportError.tooLarge(kind)
        }
        try DocumentStore.makeReadOnly(temporary)
        try manager.moveItem(at: temporary, to: destination)
        return copied
    }

    /// Lets the coordinator cross into the detached task and the cancellation
    /// handler; `NSFileCoordinator.cancel()` is documented as thread-safe.
    private final class CoordinatorBox: @unchecked Sendable {
        let value = NSFileCoordinator()
    }
}
