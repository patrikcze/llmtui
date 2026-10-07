import SwiftUI
import PhotosUI
import UIKit
import UniformTypeIdentifiers

struct ContentView: View {
    @State private var model = MobileAppModel()
    @AppStorage("iosAppearanceMode") private var appearanceMode = MobileAppearanceMode.system.rawValue

    var body: some View {
        TabView {
            Tab("Chats", systemImage: "bubble.left.and.bubble.right") {
                ChatListScreen(model: model)
            }
            Tab("Providers", systemImage: "server.rack") {
                ProviderListScreen(model: model)
            }
            Tab("Settings", systemImage: "gearshape") {
                SettingsScreen(appearanceMode: $appearanceMode)
            }
        }
        .preferredColorScheme(MobileAppearanceMode(rawValue: appearanceMode)?.colorScheme)
        .alert("Something went wrong", isPresented: Binding(
            get: { model.errorMessage != nil },
            set: { if !$0 { model.errorMessage = nil } }
        )) {
            Button("OK") { model.errorMessage = nil }
        } message: {
            Text(model.errorMessage ?? "")
        }
    }
}

private enum MobileAppearanceMode: String, CaseIterable, Identifiable {
    case system
    case light
    case dark

    var id: String { rawValue }

    var title: LocalizedStringResource {
        switch self {
        case .system: "System"
        case .light: "Light"
        case .dark: "Dark"
        }
    }

    var icon: String {
        switch self {
        case .system: "circle.lefthalf.filled"
        case .light: "sun.max"
        case .dark: "moon"
        }
    }

    var colorScheme: ColorScheme? {
        switch self {
        case .system: nil
        case .light: .light
        case .dark: .dark
        }
    }
}

private struct SettingsScreen: View {
    @Binding var appearanceMode: String
    @AppStorage(MobileToolApprovalMode.storageKey) private var approvalMode = MobileToolApprovalMode.always.rawValue
    @AppStorage(MobileMemoryStore.enabledKey) private var memoryEnabled = true
    @State private var memoryCount = 0

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    Picker("Appearance", selection: $appearanceMode) {
                        ForEach(MobileAppearanceMode.allCases) { mode in
                            Label(mode.title, systemImage: mode.icon)
                                .tag(mode.rawValue)
                        }
                    }
                    .pickerStyle(.segmented)
                } header: {
                    Text("Appearance")
                } footer: {
                    Text("System follows the appearance selected in iOS Settings.")
                }

                Section {
                    Picker("Approval", selection: $approvalMode) {
                        ForEach(MobileToolApprovalMode.allCases) { mode in
                            Text(mode.title).tag(mode.rawValue)
                        }
                    }
                } header: {
                    Text("Tools")
                } footer: {
                    Text(approvalFooter)
                }

                Section {
                    Toggle("Use Memory", isOn: $memoryEnabled)
                    NavigationLink {
                        MemoryListScreen()
                    } label: {
                        LabeledContent("Saved Memories", value: "\(memoryCount)")
                    }
                } header: {
                    Text("Memory")
                } footer: {
                    Text("When on, every chat sees your saved memories, and the assistant can offer to save or forget one. When off, nothing is shared or saved. Memories stay on this device.")
                }
            }
            .navigationTitle("Settings")
            .task { memoryCount = await MobileMemoryStore.shared.entries().count }
            .onAppear {
                Task { memoryCount = await MobileMemoryStore.shared.entries().count }
            }
        }
    }

    private var approvalFooter: String {
        switch MobileToolApprovalMode(rawValue: approvalMode) ?? .always {
        case .always:
            "Web research, search and fetch, and memory changes ask before running."
        case .memoryChanges:
            "Web research, search and fetch run without asking. Saving or forgetting a memory still asks."
        case .never:
            "Nothing asks. A web page the model reads could steer it into fetching a URL that carries your chat or memories elsewhere, so use this only with sources you trust."
        }
    }
}

private struct MemoryListScreen: View {
    @State private var entries: [MobileMemoryStore.Entry] = []
    @State private var confirmDeleteAll = false

    var body: some View {
        List {
            if entries.isEmpty {
                ContentUnavailableView(
                    "No Memories",
                    systemImage: "brain",
                    description: Text("Ask the assistant to remember something, for example \u{201C}Remember that I prefer metric units.\u{201D}")
                )
            }
            ForEach(entries.reversed()) { entry in
                VStack(alignment: .leading, spacing: 4) {
                    Text(entry.text)
                    if let createdAt = entry.createdAt {
                        Text(createdAt, format: .dateTime.day().month().year())
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
                .swipeActions {
                    Button("Delete", role: .destructive) {
                        Task {
                            await MobileMemoryStore.shared.delete(entry.id)
                            await reload()
                        }
                    }
                }
            }
        }
        .navigationTitle("Saved Memories")
        .toolbar {
            Button("Delete All", role: .destructive) { confirmDeleteAll = true }
                .disabled(entries.isEmpty)
        }
        .confirmationDialog("Delete all saved memories?", isPresented: $confirmDeleteAll, titleVisibility: .visible) {
            Button("Delete All", role: .destructive) {
                Task {
                    await MobileMemoryStore.shared.deleteAll()
                    await reload()
                }
            }
        }
        .task { await reload() }
    }

    private func reload() async {
        entries = await MobileMemoryStore.shared.entries()
    }
}

private struct ChatListScreen: View {
    let model: MobileAppModel
    @State private var path: [UUID] = []
    @State private var renaming: UUID?
    @State private var deleting: UUID?

    var body: some View {
        NavigationStack(path: $path) {
            Group {
                if model.profiles.isEmpty {
                    ContentUnavailableView {
                        Label("No Provider", systemImage: "server.rack")
                    } description: {
                        Text("Add a provider before starting a conversation.")
                    } actions: {
                        Button("Create Provider") { _ = model.addProfile() }
                    }
                } else if sortedConversations.isEmpty {
                    ContentUnavailableView {
                        Label("No Chats", systemImage: "bubble.left.and.bubble.right")
                    } description: {
                        Text("Chats and tool activity stay on this device.")
                    } actions: {
                        Button("New Chat", action: startNewChat)
                            .buttonStyle(.borderedProminent)
                    }
                } else {
                    List {
                        ForEach(sortedConversations) { conversation in
                            NavigationLink(value: conversation.id) {
                                ConversationRow(
                                    conversation: conversation,
                                    providerName: model.profile(for: conversation)?.name,
                                    isGenerating: model.generatingConversationID == conversation.id
                                )
                            }
                            .swipeActions {
                                Button("Delete", systemImage: "trash", role: .destructive) {
                                    deleting = conversation.id
                                }
                                Button("Rename", systemImage: "pencil") { renaming = conversation.id }
                                    .tint(.orange)
                            }
                            .contextMenu {
                                Button("Rename", systemImage: "pencil") { renaming = conversation.id }
                                Button("Delete", systemImage: "trash", role: .destructive) {
                                    deleting = conversation.id
                                }
                            }
                        }
                    }
                }
            }
            .navigationTitle("Chats")
            .chatsSubtitle()
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button("New Chat", systemImage: "square.and.pencil", action: startNewChat)
                        .disabled(model.profiles.isEmpty)
                }
            }
            .navigationDestination(for: UUID.self) { id in
                ChatScreen(model: model, conversationID: id) {
                    path.removeAll { $0 == id }
                }
            }
            .onChange(of: path) { _, newPath in
                if newPath.isEmpty { model.discardEmptyConversations() }
            }
            .renameConversationAlert(model: model, conversationID: $renaming)
            .deleteConversationConfirmation(model: model, conversationID: $deleting)
            .sheet(item: Binding(
                get: { model.pendingToolApproval },
                set: { if $0 == nil { model.resolveApproval(false) } }
            )) { approval in
                ToolApprovalView(approval: approval, model: model)
                    .presentationDetents([.medium])
                    .interactiveDismissDisabled()
            }
        }
    }

    private var sortedConversations: [MobileConversation] {
        model.conversations
            .filter { model.hasContent($0) }
            .sorted { $0.updatedAt > $1.updatedAt }
    }

    private func startNewChat() {
        path = [model.newConversation()]
    }
}

private struct ConversationRow: View {
    let conversation: MobileConversation
    let providerName: String?
    let isGenerating: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(alignment: .firstTextBaseline) {
                Text(conversation.title)
                    .font(.headline)
                    .lineLimit(1)
                Spacer()
                if isGenerating {
                    ProgressView()
                        .controlSize(.small)
                } else {
                    Text(conversation.updatedAt, format: .relative(presentation: .named))
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            if !conversation.preview.isEmpty {
                Text(conversation.preview)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                    .lineLimit(2)
            }
            if let providerName {
                Label(providerName, systemImage: "server.rack")
                    .font(.caption)
                    .foregroundStyle(.tertiary)
                    .lineLimit(1)
            }
        }
        .padding(.vertical, 2)
    }
}

private struct ChatScreen: View {
    let model: MobileAppModel
    let conversationID: UUID
    let close: () -> Void
    @FocusState private var composerFocused: Bool
    @State private var renaming: UUID?
    @State private var deleting: UUID?
    @State private var openSource: DocumentSourceSelection?

    var body: some View {
        VStack(spacing: 0) {
            ChatTranscript(model: model, conversationID: conversationID) { composerFocused = false }
            Divider()
            if let question = model.pendingQuestion, model.generatingConversationID == conversationID {
                UserQuestionView(question: question, model: model)
            }
            if !model.currentQueuedMessages.isEmpty {
                QueuedMessagesPanel(model: model)
            }
            if !model.currentDocuments.isEmpty {
                DocumentsPanel(model: model) { document in
                    openSource = DocumentSourceSelection(
                        document: document, chunk: nil, fileURL: model.documents.originalURL(for: document),
                        fullText: document.kind == .image
                            ? model.documents.chunks(for: document).map(\.text).joined(separator: "\n")
                            : nil
                    )
                }
            }
            ChatComposer(model: model, isFocused: $composerFocused)
        }
        // Citation links (llmtui-cite://document/chunk) open the cited
        // passage here; other links open normally.
        .environment(\.openURL, OpenURLAction { url in
            guard let ref = DocumentCitations.ref(from: url) else { return .systemAction }
            if let source = model.source(for: ref, in: conversationID) {
                openSource = DocumentSourceSelection(
                    document: source.document, chunk: source.chunk,
                    fileURL: model.documents.originalURL(for: source.document)
                )
            }
            return .handled
        })
        .sheet(item: $openSource) { selection in
            DocumentSourceView(selection: selection)
        }
        .navigationTitle(model.currentConversation?.title ?? MobileConversation.defaultTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .principal) {
                Button {
                    renaming = conversationID
                } label: {
                    HStack(spacing: 8) {
                        AssistantStatusView(
                            isWorking: model.isGenerating || model.isDiscoveringModels,
                            hasProblem: model.hasRuntimeProblem
                                || model.activeProfile == nil
                                || model.activeProfile?.model.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty != false
                        )
                        Text(model.currentConversation?.title ?? MobileConversation.defaultTitle)
                            .font(.headline)
                            .lineLimit(1)
                            .foregroundStyle(.primary)
                    }
                }
                .buttonStyle(.plain)
                .accessibilityHint("Renames this chat")
            }
            ToolbarItem(placement: .topBarLeading) {
                ProviderMenu(model: model)
            }
            ToolbarItemGroup(placement: .topBarTrailing) {
                if model.isGenerating {
                    Button("Stop", systemImage: "stop.fill") { model.stop() }
                }
                Menu("Chat Options", systemImage: "ellipsis") {
                    Button("Rename", systemImage: "pencil") { renaming = conversationID }
                    Button("Clear Messages", systemImage: "eraser") { model.clearChat() }
                        .disabled(model.messages.isEmpty)
                    Divider()
                    Button("Delete Chat", systemImage: "trash", role: .destructive) { deleting = conversationID }
                }
            }
        }
        .renameConversationAlert(model: model, conversationID: $renaming)
        .deleteConversationConfirmation(model: model, conversationID: $deleting, onDelete: close)
        .onAppear { model.openConversation(conversationID) }
    }
}

private extension View {
    /// Shows "with Local LLMs" under the Chats title on iOS 26 and later.
    @ViewBuilder
    func chatsSubtitle() -> some View {
        if #available(iOS 26.0, *) {
            navigationSubtitle("with Local LLMs")
        } else {
            self
        }
    }

    func renameConversationAlert(model: MobileAppModel, conversationID: Binding<UUID?>) -> some View {
        modifier(RenameConversationAlert(model: model, conversationID: conversationID))
    }

    func deleteConversationConfirmation(
        model: MobileAppModel,
        conversationID: Binding<UUID?>,
        onDelete: @escaping () -> Void = {}
    ) -> some View {
        confirmationDialog(
            "Delete this chat?",
            isPresented: Binding(
                get: { conversationID.wrappedValue != nil },
                set: { if !$0 { conversationID.wrappedValue = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button("Delete Chat", role: .destructive) {
                if let id = conversationID.wrappedValue {
                    model.deleteConversation(id)
                    onDelete()
                }
                conversationID.wrappedValue = nil
            }
        } message: {
            Text("Its messages are removed from this device.")
        }
    }
}

private struct RenameConversationAlert: ViewModifier {
    let model: MobileAppModel
    @Binding var conversationID: UUID?
    @State private var title = ""

    func body(content: Content) -> some View {
        content
            .alert(
                "Rename Chat",
                isPresented: Binding(
                    get: { conversationID != nil },
                    set: { if !$0 { conversationID = nil } }
                )
            ) {
                TextField("Title", text: $title)
                Button("Save") {
                    if let id = conversationID { model.renameConversation(id, to: title) }
                    conversationID = nil
                }
                Button("Cancel", role: .cancel) { conversationID = nil }
            } message: {
                Text("Leave it empty to name the chat after its first message.")
            }
            .onChange(of: conversationID) { _, id in
                title = id.flatMap { id in model.conversations.first { $0.id == id }?.title } ?? ""
            }
    }
}

private struct AssistantStatusView: View {
    let isWorking: Bool
    let hasProblem: Bool

    private let frames = [
        "ThinkingMascotFrame1",
        "ThinkingMascotFrame2",
        "ThinkingMascotFrame3",
        "ThinkingMascotFrame4"
    ]

    var body: some View {
        ZStack(alignment: .bottomTrailing) {
            if isWorking {
                TimelineView(.periodic(from: .now, by: 0.22)) { context in
                    mascot(frameName(at: context.date))
                }
            } else {
                mascot(frames[0])
            }

            Circle()
                .fill(statusColor)
                .frame(width: 10, height: 10)
                .overlay {
                    Circle().stroke(Color(uiColor: .systemBackground), lineWidth: 2)
                }
                .shadow(radius: 1)
        }
        .frame(width: 34, height: 34)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityLabel)
    }

    private func mascot(_ name: String) -> some View {
        Image(name)
            .resizable()
            .scaledToFit()
            .frame(width: 32, height: 32)
    }

    private func frameName(at date: Date) -> String {
        let index = Int(date.timeIntervalSinceReferenceDate / 0.22) % frames.count
        return frames[index]
    }

    private var statusColor: Color {
        if isWorking { return .orange }
        return hasProblem ? .red : .green
    }

    private var accessibilityLabel: String {
        if isWorking { return "Assistant is working" }
        return hasProblem ? "Assistant unavailable" : "Assistant ready"
    }
}

private struct ProviderMenu: View {
    let model: MobileAppModel

    var body: some View {
        Menu {
            ForEach(model.profiles) { profile in
                Button {
                    model.selectProfile(profile.id)
                } label: {
                    if profile.id == model.activeProfileID {
                        Label(profile.name, systemImage: "checkmark")
                    } else {
                        Text(profile.name)
                    }
                }
            }
        } label: {
            Label(model.activeProfile?.name ?? "Provider", systemImage: "server.rack")
                .lineLimit(1)
        }
        .accessibilityLabel("Active provider")
        .disabled(model.profiles.isEmpty || model.isGenerating)
    }
}

private struct ChatTranscript: View {
    private static let bottomID = "transcript-bottom"
    let model: MobileAppModel
    let conversationID: UUID
    @State private var isNearBottom = true
    /// Hides the keyboard. Tapping anywhere in the conversation calls it, in
    /// addition to swiping the conversation down.
    let dismissKeyboard: () -> Void

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 14) {
                    if model.messages.isEmpty {
                        ContentUnavailableView(
                            "Start a Conversation",
                            systemImage: "sparkles",
                            description: Text("Messages and tool activity stay on this device.")
                        )
                        .padding(.top, 60)
                    }
                    ForEach(model.messages) { message in
                        MessageBubble(message: message, displayText: displayText(for: message))
                            .id(message.id)
                    }
                    Color.clear
                        .frame(height: 1)
                        .id(Self.bottomID)
                }
                .padding()
            }
            .scrollDismissesKeyboard(.interactively)
            // Simultaneous, so links, text selection and tool disclosure
            // groups in the transcript keep working.
            .simultaneousGesture(TapGesture().onEnded(dismissKeyboard))
            // Follow a streaming reply only while the reader is at the
            // bottom; scrolling up to read stops the auto-scroll.
            .onScrollGeometryChange(for: Bool.self) { geometry in
                geometry.contentOffset.y + geometry.containerSize.height >= geometry.contentSize.height - 120
            } action: { _, nearBottom in
                isNearBottom = nearBottom
            }
            .onChange(of: model.messages.last?.text) {
                guard isNearBottom else { return }
                proxy.scrollTo(Self.bottomID, anchor: .bottom)
            }
            .onChange(of: model.messages.count) {
                // A new message (yours, or the reply starting) always shows.
                proxy.scrollTo(Self.bottomID, anchor: .bottom)
            }
        }
    }
}

extension ChatTranscript {
    /// The assistant text with attachment citations resolved: a citation of
    /// a passage returned in this reply becomes a link to it; anything else
    /// is marked unverified.
    func displayText(for message: MobileChatMessage) -> String {
        guard message.role == .assistant else { return message.text }
        return DocumentCitations.render(message.text, returned: Set(message.sourceRefs ?? [])) { ref in
            model.citationLabel(ref, in: conversationID)
        }
    }
}

private struct MessageBubble: View {
    let message: MobileChatMessage
    let displayText: String

    var body: some View {
        HStack {
            if message.role == .user { Spacer(minLength: 40) }
            VStack(alignment: .leading, spacing: 6) {
                Text(message.role == .user ? "You" : "Assistant")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                if message.role == .assistant {
                    MobileRichMessageView(
                        source: message.text.isEmpty && message.isStreaming ? "Thinking…" : displayText
                    )
                } else {
                    Text(message.text)
                        .textSelection(.enabled)
                }
                if !message.attachments.isEmpty {
                    ScrollView(.horizontal) {
                        HStack {
                            ForEach(message.attachments) { attachment in
                                if let image = UIImage(data: attachment.data) {
                                    Image(uiImage: image)
                                        .resizable()
                                        .scaledToFill()
                                        .frame(width: 120, height: 90)
                                        .clipShape(RoundedRectangle(cornerRadius: 10))
                                        .accessibilityLabel("Attached image")
                                }
                            }
                        }
                    }
                    .scrollIndicators(.hidden)
                }
                if !message.toolActivities.isEmpty {
                    VStack(spacing: 8) {
                        ForEach(message.toolActivities) { activity in
                            ToolActivityView(activity: activity)
                        }
                    }
                    .padding(.top, 4)
                }
            }
            .padding(12)
            .background(
                message.role == .user ? Color.accentColor.opacity(0.16) : Color.secondary.opacity(0.1),
                in: RoundedRectangle(cornerRadius: 16)
            )
            if message.role != .user { Spacer(minLength: 40) }
        }
        .accessibilityElement(children: .combine)
    }
}

private struct ToolActivityView: View {
    let activity: MobileToolActivity

    var body: some View {
        DisclosureGroup {
            VStack(alignment: .leading, spacing: 6) {
                Text(activity.detail)
                    .foregroundStyle(.secondary)
                if let preview = activity.resultPreview, !preview.isEmpty {
                    Divider()
                    Text(preview)
                        .font(.caption.monospaced())
                        .textSelection(.enabled)
                        .lineLimit(8)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.top, 6)
        } label: {
            HStack(spacing: 8) {
                Image(systemName: icon)
                    .foregroundStyle(statusColor)
                VStack(alignment: .leading, spacing: 1) {
                    Text(displayName)
                        .font(.subheadline.weight(.semibold))
                    Text(statusText)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                if activity.status == .running || activity.status == .waitingForApproval {
                    ProgressView()
                        .controlSize(.small)
                }
            }
        }
        .padding(10)
        .background(.background.opacity(0.7), in: RoundedRectangle(cornerRadius: 12))
        .overlay {
            RoundedRectangle(cornerRadius: 12)
                .stroke(.separator.opacity(0.5), lineWidth: 0.5)
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(displayName), \(statusText)")
    }

    private var displayName: String {
        activity.name.replacingOccurrences(of: "_", with: " ").capitalized
    }

    private var icon: String {
        switch activity.name {
        case "web_research": "doc.text.magnifyingglass"
        case "document_list", "document_search", "document_read": "doc.text"
        case "memory_search", "memory_list", "memory_remember", "memory_forget": "brain"
        case "web_search": "magnifyingglass"
        case "web_fetch": "globe"
        case "local_context": "location"
        case "ask_user": "questionmark.bubble"
        default: "wrench.and.screwdriver"
        }
    }

    private var statusText: String {
        switch activity.status {
        case .waitingForApproval: "Waiting for approval"
        case .running: "Running"
        case .completed: "Completed"
        case .failed: "Failed"
        case .rejected: "Rejected"
        }
    }

    private var statusColor: Color {
        switch activity.status {
        case .waitingForApproval, .running: .accentColor
        case .completed: .green
        case .failed: .red
        case .rejected: .secondary
        }
    }
}

private struct ChatComposer: View {
    let model: MobileAppModel
    var isFocused: FocusState<Bool>.Binding
    @State private var selectedPhotos: [PhotosPickerItem] = []
    @State private var readingPhotos: [PhotosPickerItem] = []
    @State private var showVisionPicker = false
    @State private var showReadingPicker = false
    @State private var showFileImporter = false

    private static let documentTypes: [UTType] = [
        .pdf, .plainText, .utf8PlainText, UTType("net.daringfireball.markdown")
    ].compactMap { $0 }

    var body: some View {
        let hasAttachments = !model.draftAttachments.isEmpty || !model.currentDocuments.isEmpty
        VStack(spacing: 8) {
            if !model.draftAttachments.isEmpty {
                ScrollView(.horizontal) {
                    HStack {
                        ForEach(model.draftAttachments) { attachment in
                            AttachmentThumbnail(attachment: attachment) {
                                model.removeAttachment(attachment)
                            }
                        }
                    }
                }
                .scrollIndicators(.hidden)
            }
            HStack(spacing: 18) {
                ReasoningMenu(model: model)
                Menu {
                    Button("PDF, Text or Markdown File", systemImage: "doc") { showFileImporter = true }
                    Button("Read Text from Screenshot or Image", systemImage: "text.viewfinder") { showReadingPicker = true }
                    Button("Photo for the Model to See", systemImage: "photo") { showVisionPicker = true }
                        .disabled(model.draftAttachments.count >= 4)
                } label: {
                    ComposerIcon(
                        systemName: "paperclip",
                        active: hasAttachments,
                        accessibilityLabel: "Attach"
                    )
                }
                Button {
                    model.toolsEnabled.toggle()
                } label: {
                    ComposerIcon(
                        systemName: "wrench.and.screwdriver",
                        active: model.toolsEnabled,
                        accessibilityLabel: "Tools: web search, fetch, memory and device context"
                    )
                }
                .buttonStyle(.plain)
                .accessibilityValue(model.toolsEnabled ? "Enabled" : "Disabled")
                Spacer()
                Text(model.toolsEnabled ? "Tools" : "Chat")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                if isFocused.wrappedValue {
                    // Lives in this row, above the message field, so it never
                    // covers the text or the Send button.
                    Button {
                        isFocused.wrappedValue = false
                    } label: {
                        ComposerIcon(
                            systemName: "keyboard.chevron.compact.down",
                            active: false,
                            accessibilityLabel: "Hide keyboard"
                        )
                    }
                    .buttonStyle(.plain)
                    .transition(.opacity)
                }
            }
            .animation(.default, value: isFocused.wrappedValue)
            HStack(alignment: .bottom, spacing: 10) {
                TextField("Message", text: Bindable(model).draft, axis: .vertical)
                    .lineLimit(2...8)
                    .textFieldStyle(.plain)
                    .padding(.horizontal, 12)
                    .padding(.vertical, 10)
                    .background(Color(uiColor: .secondarySystemBackground), in: RoundedRectangle(cornerRadius: 14))
                    .overlay {
                        RoundedRectangle(cornerRadius: 14)
                            .stroke(Color.secondary.opacity(0.25), lineWidth: 0.5)
                    }
                    .submitLabel(.return)
                    .focused(isFocused)
                // While a reply is generating, Send queues the message instead.
                Button(
                    model.isGenerating ? "Queue message" : "Send",
                    systemImage: model.isGenerating ? "text.badge.plus" : "arrow.up.circle.fill"
                ) { model.send() }
                    .labelStyle(.iconOnly)
                    .font(.title)
                    .disabled(
                        model.draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                            && model.draftAttachments.isEmpty
                    )
            }
        }
        .padding()
        .background(.bar)
        .photosPicker(
            isPresented: $showVisionPicker,
            selection: $selectedPhotos,
            maxSelectionCount: max(1, 4 - model.draftAttachments.count),
            matching: .images
        )
        .photosPicker(isPresented: $showReadingPicker, selection: $readingPhotos, maxSelectionCount: 5, matching: .images)
        .fileImporter(isPresented: $showFileImporter, allowedContentTypes: Self.documentTypes, allowsMultipleSelection: true) { result in
            switch result {
            case .success(let urls): model.attachFiles(urls)
            case .failure(let error): model.errorMessage = error.localizedDescription
            }
        }
        .onChange(of: readingPhotos) { _, items in
            guard !items.isEmpty else { return }
            Task {
                var images: [Data] = []
                for item in items {
                    if let data = try? await item.loadTransferable(type: Data.self) { images.append(data) }
                }
                readingPhotos = []
                if !images.isEmpty { model.attachImagesForReading(images) }
            }
        }
        .onChange(of: selectedPhotos) { _, items in
            Task {
                for item in items {
                    if let data = try? await item.loadTransferable(type: Data.self) {
                        model.addAttachment(data: data)
                    }
                }
                selectedPhotos = []
            }
        }
    }
}

private struct QueuedMessagesPanel: View {
    let model: MobileAppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Up next \u{00B7} \(model.currentQueuedMessages.count)")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)
            ScrollView {
                VStack(spacing: 6) {
                    ForEach(model.currentQueuedMessages) { queued in
                        HStack(spacing: 8) {
                            Image(systemName: "clock")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                            Button {
                                model.editQueuedMessage(queued)
                            } label: {
                                Text(queued.text.isEmpty ? "Image" : queued.text)
                                    .font(.callout)
                                    .lineLimit(1)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                            }
                            .buttonStyle(.plain)
                            .accessibilityHint("Moves this message back into the composer to edit it")
                            if !queued.attachments.isEmpty {
                                Label("\(queued.attachments.count)", systemImage: "photo")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                            }
                            Button("Remove queued message", systemImage: "xmark.circle.fill") {
                                model.removeQueuedMessage(queued)
                            }
                            .labelStyle(.iconOnly)
                            .foregroundStyle(.secondary)
                            .buttonStyle(.plain)
                        }
                        .padding(.horizontal, 10)
                        .padding(.vertical, 7)
                        .background(Color.secondary.opacity(0.12), in: RoundedRectangle(cornerRadius: 10))
                    }
                }
            }
            .frame(maxHeight: 120)
            .fixedSize(horizontal: false, vertical: model.currentQueuedMessages.count < 3)
        }
        .padding(.horizontal)
        .padding(.top, 10)
        .background(.bar)
    }
}

private struct ComposerIcon: View {
    let systemName: String
    let active: Bool
    let accessibilityLabel: String

    var body: some View {
        Image(systemName: systemName)
            .font(.title3)
            .foregroundStyle(active ? Color.accentColor : Color.secondary)
            .frame(width: 32, height: 32)
            .background(active ? Color.accentColor.opacity(0.14) : Color.clear, in: Circle())
            .accessibilityLabel(accessibilityLabel)
    }
}

private struct ReasoningMenu: View {
    let model: MobileAppModel

    var body: some View {
        Menu {
            ForEach(MobileReasoning.allCases) { choice in
                Button {
                    model.reasoning = choice
                } label: {
                    if model.reasoning == choice {
                        Label(choice.title, systemImage: "checkmark")
                    } else {
                        Text(choice.title)
                    }
                }
            }
        } label: {
            ComposerIcon(
                systemName: "brain",
                active: model.reasoning != .automatic,
                accessibilityLabel: "Reasoning"
            )
        }
        .accessibilityValue(model.reasoning.title)
    }
}

private struct AttachmentThumbnail: View {
    let attachment: MobileAttachment
    let remove: () -> Void

    var body: some View {
        if let image = UIImage(data: attachment.data) {
            ZStack(alignment: .topTrailing) {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFill()
                    .frame(width: 76, height: 58)
                    .clipShape(RoundedRectangle(cornerRadius: 10))
                Button("Remove attachment", systemImage: "xmark.circle.fill", action: remove)
                    .labelStyle(.iconOnly)
                    .foregroundStyle(.white, .black.opacity(0.65))
                    .offset(x: 5, y: -5)
            }
        }
    }
}

private struct UserQuestionView: View {
    let question: String
    let model: MobileAppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(question).font(.headline)
            HStack {
                TextField("Your answer", text: Bindable(model).questionAnswer)
                    .textFieldStyle(.roundedBorder)
                    .onSubmit(model.submitQuestion)
                Button("Answer") { model.submitQuestion() }
                    .buttonStyle(.borderedProminent)
                    .disabled(model.questionAnswer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding()
        .background(Color.accentColor.opacity(0.08))
    }
}

private struct ToolApprovalView: View {
    let approval: PendingToolApproval
    let model: MobileAppModel

    var body: some View {
        NavigationStack {
            VStack(alignment: .leading, spacing: 18) {
                Label("Tool approval required", systemImage: "checkmark.shield")
                    .font(.title2.weight(.semibold))
                LabeledContent("Tool", value: approval.name)
                Text(approval.summary)
                    .font(.callout)
                    .textSelection(.enabled)
                Spacer()
                HStack {
                    Button("Reject", role: .cancel) { model.resolveApproval(false) }
                        .buttonStyle(.bordered)
                    Spacer()
                    Button(approval.allowTitle) { model.resolveApproval(true) }
                        .buttonStyle(.borderedProminent)
                }
            }
            .padding()
            .navigationTitle("Approval")
            .navigationBarTitleDisplayMode(.inline)
        }
    }
}

private struct ProviderListScreen: View {
    let model: MobileAppModel
    @State private var editingProfile: MobileProviderProfile?

    var body: some View {
        NavigationStack {
            List {
                if model.profiles.isEmpty {
                    ContentUnavailableView(
                        "No Providers",
                        systemImage: "server.rack",
                        description: Text("Add Ollama, LM Studio, or another OpenAI-compatible server.")
                    )
                }
                ForEach(model.profiles) { profile in
                    Button {
                        editingProfile = profile
                    } label: {
                        ProviderRow(profile: profile, isActive: profile.id == model.activeProfileID)
                    }
                    .buttonStyle(.plain)
                    .swipeActions {
                        Button("Delete", role: .destructive) { model.deleteProfile(profile) }
                    }
                }
            }
            .navigationTitle("Providers")
            .toolbar {
                Button("Add Provider", systemImage: "plus") {
                    editingProfile = model.addProfile()
                }
            }
            .sheet(item: $editingProfile) { profile in
                ProviderEditor(model: model, profile: profile)
            }
        }
    }
}

private struct ProviderRow: View {
    let profile: MobileProviderProfile
    let isActive: Bool

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 4) {
                Text(profile.name).font(.headline)
                Text(profile.model.isEmpty ? "No model selected" : profile.model)
                    .font(.subheadline)
                    .foregroundStyle(.secondary)
                Text(profile.baseURL)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
            }
            Spacer()
            if isActive {
                Image(systemName: "checkmark.circle.fill")
                    .foregroundStyle(.tint)
                    .accessibilityLabel("Active")
            }
        }
        .contentShape(Rectangle())
    }
}

private struct ProviderEditor: View {
    let model: MobileAppModel
    @Environment(\.dismiss) private var dismiss
    @State private var profile: MobileProviderProfile
    @State private var apiKey: String

    init(model: MobileAppModel, profile: MobileProviderProfile) {
        self.model = model
        _profile = State(initialValue: profile)
        _apiKey = State(initialValue: model.apiKey(for: profile))
    }

    var body: some View {
        NavigationStack {
            Form {
                Section("Provider") {
                    TextField("Name", text: $profile.name)
                    Picker("Type", selection: $profile.type) {
                        ForEach(MobileProviderType.allCases) { type in
                            Text(type.title).tag(type)
                        }
                    }
                    .onChange(of: profile.type) { oldType, newType in
                        if profile.baseURL == oldType.defaultBaseURL || profile.baseURL.isEmpty {
                            profile.baseURL = newType.defaultBaseURL
                        }
                    }
                    TextField("Base URL", text: $profile.baseURL)
                        .textInputAutocapitalization(.never)
                        .keyboardType(.URL)
                        .autocorrectionDisabled()
                    SecureField("API key (optional)", text: $apiKey)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                }

                Section("Model") {
                    TextField("Model ID", text: $profile.model)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                    Button {
                        model.saveProfile(profile, apiKey: apiKey)
                        model.discoverModels()
                    } label: {
                        if model.isDiscoveringModels {
                            ProgressView()
                        } else {
                            Label("Discover Models", systemImage: "arrow.triangle.2.circlepath")
                        }
                    }
                    .disabled(model.isDiscoveringModels)
                    ForEach(model.discoveredModels, id: \.self) { discovered in
                        Button(discovered) {
                            profile.model = discovered
                            model.useModel(discovered)
                        }
                    }
                }

                Section {
                    Text("On iPhone, localhost points to the phone. For a provider on your Mac, use its Wi-Fi address and allow the server to listen on your local network.")
                        .font(.footnote)
                        .foregroundStyle(.secondary)
                }
            }
            .navigationTitle("Provider")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Save") {
                        model.saveProfile(profile, apiKey: apiKey)
                        dismiss()
                    }
                }
            }
        }
    }
}

#Preview {
    ContentView()
}
