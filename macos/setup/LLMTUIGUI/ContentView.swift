import SwiftUI
import Foundation
import UniformTypeIdentifiers
import AppKit

/// GUI-only display preference — independent of the llmtui YAML config,
/// which has no concept of a native appearance setting.
enum AppearanceMode: String, CaseIterable, Identifiable {
    case system
    case light
    case dark

    var id: String { rawValue }

    var title: String {
        switch self {
        case .system: "System"
        case .light: "Light"
        case .dark: "Dark"
        }
    }

    /// `.preferredColorScheme(nil)` has a long-standing AppKit-bridging quirk
    /// where reverting from an explicit scheme back to "follow system" doesn't
    /// reliably take effect. Driving `NSApp.appearance` directly is the
    /// AppKit-native mechanism and correctly resumes following the system
    /// when set back to `nil`.
    var nsAppearance: NSAppearance? {
        switch self {
        case .system: nil
        case .light: NSAppearance(named: .aqua)
        case .dark: NSAppearance(named: .darkAqua)
        }
    }
}

struct ContentView: View {
    @State private var model = AppModel()
    @AppStorage("appearanceMode") private var appearanceModeRaw = AppearanceMode.system.rawValue

    var body: some View {
        NavigationSplitView {
            SidebarView(selection: $model.selectedSection)
        } detail: {
            DetailContainer(model: model)
        }
        .frame(minWidth: 980, minHeight: 640)
        .task {
            model.loadConfiguration()
            applyAppearance()
        }
        .onChange(of: appearanceModeRaw) {
            applyAppearance()
        }
        .alert("Something went wrong", isPresented: Binding(
            get: { model.lastError != nil },
            set: { if !$0 { model.lastError = nil } }
        )) {
            Button("OK") { model.lastError = nil }
        } message: {
            Text(model.lastError ?? "")
        }
    }

    private func applyAppearance() {
        NSApp.appearance = (AppearanceMode(rawValue: appearanceModeRaw) ?? .system).nsAppearance
    }
}

struct SidebarView: View {
    @Binding var selection: AppSection

    var body: some View {
        List(AppSection.allCases, selection: $selection) { section in
            Label(section.title, systemImage: section.systemImage)
                .tag(section)
        }
        .navigationTitle("llmtui")
        .listStyle(.sidebar)
        .navigationSplitViewColumnWidth(
            min: 190,
            ideal: Self.idealWidth,
            max: 360
        )
    }

    private static let idealWidth: CGFloat = {
        let longestTitle = AppSection.allCases.map(\.title.count).max() ?? 0
        return min(max(CGFloat(longestTitle) * 8.5 + 78, 210), 300)
    }()
}

struct DetailContainer: View {
    let model: AppModel

    var body: some View {
        switch model.selectedSection {
        case .overview:
            ConfigurationOverviewView(model: model)
        case .chat:
            ChatView(model: model)
        case .providers:
            ProviderSettingsView(model: model)
        case .chatSettings:
            ChatSettingsView(model: model)
        case .tools:
            ToolsSettingsView(model: model)
        case .agentRuntime:
            AgentRuntimeSettingsView(model: model)
        case .profiles:
            ModelProfilesView(model: model)
        case .moreSettings:
            MoreSettingsView(model: model)
        case .personalApps:
            PersonalAppsSettingsView(model: model)
        }
    }
}

private struct PrintChatActionKey: FocusedValueKey {
    typealias Value = () -> Void
}

extension FocusedValues {
    var printChatAction: (() -> Void)? {
        get { self[PrintChatActionKey.self] }
        set { self[PrintChatActionKey.self] = newValue }
    }
}

struct ChatView: View {
    let model: AppModel

    private var queuedMessagesHeight: CGFloat {
        min(48 + CGFloat(model.queuedMessages.count) * 42, 180)
    }

    var body: some View {
        VStack(spacing: 0) {
            ChatHeader(model: model)
            Text(model.configuration.provider.type == .embedded
                ? "chat is unavailable for embedded models in llmtui; select Ollama, LM Studio, or another server provider"
                : "chat is available through Ollama, LM Studio, and other compatible model servers")
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(.horizontal, 24)
                .padding(.bottom, 10)
            Divider()
            MessageList(model: model)
                .safeAreaInset(edge: .bottom, spacing: 0) {
                    VStack(spacing: 0) {
                        // A long question or several options could in principle
                        // exceed the window height; scroll instead of letting it
                        // get clipped below the visible area with no way to reach it.
                        if let question = model.pendingUserQuestion {
                            ScrollView {
                                UserQuestionPanel(
                                    question: question,
                                    onAnswer: model.answerUserQuestion,
                                    onSkip: model.skipUserQuestion
                                )
                                .id(question.id)
                            }
                            .frame(maxHeight: 320)
                            .transition(.move(edge: .bottom).combined(with: .opacity))
                        }
                        if !model.queuedMessages.isEmpty {
                            ScrollView {
                                QueuedMessagesPanel(model: model)
                            }
                            .frame(height: queuedMessagesHeight)
                        }
                        Divider()
                        ComposerView(model: model)
                    }
                    .background(.background)
                }
        }
        .navigationTitle("Chat")
        .toolbar {
            ToolbarItemGroup {
                Button("Clear", systemImage: "trash") {
                    model.clearChat()
                }
                .disabled(model.messages.isEmpty)
                Button("Stop", systemImage: "stop.fill") {
                    model.stopGeneration()
                }
                .disabled(!model.isGenerating)
            }
        }
        .focusedSceneValue(\.printChatAction, model.messages.isEmpty ? nil : {
            ChatExport.printChat(
                messages: model.messages,
                providerName: model.configuration.provider.name,
                modelName: model.configuration.provider.model
            )
        })
        .sheet(isPresented: Binding(
            get: { !model.pendingToolRequests.isEmpty },
            set: { _ in }
        )) {
            ToolApprovalPanel(model: model)
        }
        .interactiveDismissDisabled(!model.pendingToolRequests.isEmpty)
    }
}

struct ChatHeader: View {
    let model: AppModel

    var body: some View {
        HStack(spacing: 12) {
            Circle()
                .fill(model.pendingUserQuestion != nil ? .blue : (model.isGenerating ? .orange : .green))
                .frame(width: 9, height: 9)
            Text(model.configuration.provider.name)
                .font(.headline)
            Text("•")
                .foregroundStyle(.secondary)
            Text(model.configuration.provider.model)
                .foregroundStyle(.secondary)
            if model.nativeAgentEnabled {
                Label("Agent", systemImage: "infinity")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.purple)
                    .help("Native bounded agent execution is enabled for new chat requests.")
            }
            if model.personalAppsRuntime.privateSession {
                Label("Private", systemImage: "lock.shield.fill")
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.orange)
                    .help("Personal Apps data is present. General-purpose tools and unapproved provider changes are blocked until the conversation is cleared.")
            }
            Spacer()
            ThinkingMascotView(
                isAnimating: model.isGenerating && model.pendingUserQuestion == nil,
                statusText: model.pendingUserQuestion != nil
                    ? "Waiting for you…"
                    : model.statusMessage
            )
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 6)
    }
}

private struct ThinkingMascotView: View {
    let isAnimating: Bool
    let statusText: String

    @Environment(\.accessibilityReduceMotion) private var accessibilityReduceMotion

    private let frameNames = [
        "ThinkingMascotFrame1",
        "ThinkingMascotFrame2",
        "ThinkingMascotFrame3",
        "ThinkingMascotFrame4"
    ]

    var body: some View {
        HStack(spacing: 6) {
            TimelineView(
                .animation(
                    minimumInterval: 0.14,
                    paused: !isAnimating || accessibilityReduceMotion
                )
            ) { context in
                Image(frameNames[frameIndex(at: context.date)])
                    .resizable()
                    .interpolation(.none)
                    .scaledToFit()
                    .frame(width: 24, height: 24)
            }

            Text(statusText)
                .font(.caption2)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
        .frame(minWidth: 82, alignment: .trailing)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(statusText)
        .accessibilityValue(isAnimating ? "Assistant is working" : "Assistant is idle")
    }

    private func frameIndex(at date: Date) -> Int {
        guard isAnimating, !accessibilityReduceMotion else { return 0 }
        return Int(date.timeIntervalSinceReferenceDate / 0.14) % frameNames.count
    }
}

struct MessageList: View {
    let model: AppModel
    var messages: [ChatMessage] { model.messages }

    var body: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    ForEach(messages.sorted { $0.createdAt < $1.createdAt }) { message in
                        MessageRow(model: model, message: message)
                            .id(message.id)
                    }
                }
                .frame(maxWidth: 820, alignment: .leading)
                .frame(maxWidth: .infinity)
                .padding(.horizontal, 28)
                .padding(.vertical, 20)
            }
            .onChange(of: messages.last?.id, initial: true) { _, _ in
                scrollToLatestMessage(using: proxy)
            }
            .onChange(of: messages.last?.text) { _, _ in
                // Streaming changes the text of the existing assistant row
                // without changing the message count.
                scrollToLatestMessage(using: proxy)
            }
        }
    }

    private func scrollToLatestMessage(using proxy: ScrollViewProxy) {
        guard let last = messages.max(by: { $0.createdAt < $1.createdAt }) else { return }
        Task { @MainActor in
            await Task.yield()
            withAnimation(.easeOut(duration: 0.18)) {
                proxy.scrollTo(last.id, anchor: .bottom)
            }
        }
    }
}

struct MessageRow: View {
    let model: AppModel
    let message: ChatMessage

    @State private var showToolActivity = true
    @State private var activityGeneration = 0
    @State private var previewedAttachment: ChatAttachment?

    var body: some View {
        HStack(alignment: .top, spacing: 12) {
            Image(systemName: iconName)
                .foregroundStyle(iconColor)
                .frame(width: 24)
            VStack(alignment: .leading, spacing: 6) {
                Text(roleTitle)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                if !message.attachments.isEmpty {
                    SentAttachmentGallery(
                        attachments: message.attachments,
                        onPreview: { previewedAttachment = $0 }
                    )
                }
                RichMessageView(text: message.text)
                if message.role == .assistant, let metrics = message.metrics {
                    ResponseMetricsView(metrics: metrics)
                }
                if !message.toolActivities.isEmpty {
                    VStack(alignment: .leading, spacing: 0) {
                        ToolActivityToggle(
                            isShown: $showToolActivity,
                            activityCount: message.toolActivities.count,
                            hasActiveActivity: message.toolActivities.contains(where: toolActivityIsActive)
                        )
                        if showToolActivity {
                            ToolActivityPanel(activities: message.toolActivities)
                        }
                    }
                    .clipShape(RoundedRectangle(cornerRadius: 10))
                }
            }
        }
        .padding(.leading, message.role == .assistant ? 28 : 0)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(14)
        .background(backgroundStyle, in: RoundedRectangle(cornerRadius: 14))
        .overlay(alignment: .topTrailing) {
            if message.role == .user {
                Text(message.createdAt, format: .dateTime.hour().minute())
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .padding(.top, 10)
                    .padding(.trailing, 12)
            } else if message.role == .assistant {
                CopyReplyButton(text: message.text)
                    .padding(.top, 8)
                    .padding(.trailing, 10)
            }
        }
        .onAppear { scheduleActivityCollapse() }
        .onChange(of: message.toolActivities) { _, _ in
            scheduleActivityCollapse()
        }
        .sheet(item: $previewedAttachment) { attachment in
            AttachmentPreviewSheet(attachment: attachment)
        }
    }

    /// Mirrors the collapse behavior tool activity used to have globally,
    /// now scoped to this turn: stay expanded while anything is running or
    /// awaiting approval, then auto-collapse a few seconds after it settles.
    private func scheduleActivityCollapse() {
        activityGeneration += 1
        let generation = activityGeneration

        guard !message.toolActivities.isEmpty else { return }

        showToolActivity = true
        guard model.pendingToolRequests.isEmpty else { return }
        guard !message.toolActivities.contains(where: toolActivityIsActive) else { return }

        Task { @MainActor in
            try? await Task.sleep(for: .seconds(4))
            guard generation == activityGeneration else { return }
            guard model.pendingToolRequests.isEmpty else { return }
            guard !message.toolActivities.contains(where: toolActivityIsActive) else { return }

            withAnimation(.easeOut(duration: 0.35)) {
                showToolActivity = false
            }
        }
    }

    private var roleTitle: String {
        switch message.role {
        case .user: "You"
        case .assistant: "Assistant"
        case .tool: "Tool"
        }
    }

    private var iconName: String {
        switch message.role {
        case .user: "person.fill"
        case .assistant: "sparkles"
        case .tool: "wrench.and.screwdriver.fill"
        }
    }

    private var iconColor: Color {
        switch message.role {
        case .user: .accentColor
        case .assistant: .purple
        case .tool: .orange
        }
    }

    private var backgroundStyle: AnyShapeStyle {
        switch message.role {
        case .user: AnyShapeStyle(Color.accentColor.opacity(0.10))
        case .assistant: AnyShapeStyle(.thinMaterial)
        case .tool: AnyShapeStyle(Color.orange.opacity(0.12))
        }
    }
}

private struct CopyReplyButton: View {
    let text: String

    @State private var didCopy = false

    var body: some View {
        Button {
            let pasteboard = NSPasteboard.general
            pasteboard.clearContents()
            pasteboard.setString(text, forType: .string)
            didCopy = true
            Task {
                try? await Task.sleep(for: .seconds(1.4))
                didCopy = false
            }
        } label: {
            Image(systemName: didCopy ? "checkmark" : "doc.on.doc")
                .foregroundStyle(didCopy ? .green : .secondary)
        }
        .buttonStyle(.plain)
        .disabled(text.isEmpty)
        .help(didCopy ? "Copied" : "Copy reply")
    }
}

struct InfoButton: View {
    let text: String
    @State private var isPresented = false

    var body: some View {
        Button {
            isPresented.toggle()
        } label: {
            Image(systemName: "info.circle")
        }
        .buttonStyle(.borderless)
        .foregroundStyle(.secondary)
        .help(text)
        .popover(isPresented: $isPresented, arrowEdge: .trailing) {
            Text(text)
                .font(.callout)
                .multilineTextAlignment(.leading)
                .frame(width: 280, alignment: .leading)
                .padding(16)
        }
    }
}

enum MessageContentBlock: Equatable {
    case markdown(String)
    case code(language: String, source: String)
    case table(headers: [String], rows: [[String]])
    case mermaid(String)
    case math(String)
    case latexDocument(String)
    case image(url: String, alt: String)
}

struct RichMessageView: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .markdown(let source):
                    MarkdownText(source: source)
                case .code(let language, let source):
                    if language.lowercased() == "mermaid" {
                        MermaidBlockView(source: source)
                    } else {
                        CodeBlockView(language: language, source: source)
                    }
                case .table(let headers, let rows):
                    MarkdownTableView(headers: headers, rows: rows)
                case .mermaid(let source):
                    MermaidBlockView(source: source)
                case .math(let source):
                    LatexMathView(source: source, isDisplay: true)
                        .frame(maxWidth: .infinity, alignment: .center)
                case .latexDocument(let source):
                    LatexDocumentView(source: source)
                case .image(let url, let alt):
                    MarkdownImageView(url: url, alt: alt)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    var blocks: [MessageContentBlock] {
        var result: [MessageContentBlock] = []
        var markdownLines: [String] = []
        let lines = text.components(separatedBy: .newlines)
        var index = 0

        func appendMarkdown() {
            let source = markdownLines.joined(separator: "\n")
            if !source.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                result.append(.markdown(source))
            }
            markdownLines.removeAll(keepingCapacity: true)
        }

        while index < lines.count {
            let line = lines[index]

            if line.hasPrefix("```") {
                appendMarkdown()
                let language = String(line.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                index += 1
                var codeLines: [String] = []
                while index < lines.count, !lines[index].hasPrefix("```") {
                    codeLines.append(lines[index])
                    index += 1
                }
                if index < lines.count { index += 1 }
                let source = codeLines.joined(separator: "\n")
                if language.lowercased() == "mermaid" {
                    result.append(.mermaid(source))
                } else if ["latex", "tex"].contains(language.lowercased()) {
                    result.append(.latexDocument(source))
                } else {
                    result.append(.code(language: language, source: source))
                }
                continue
            }

            let trimmedLine = line.trimmingCharacters(in: .whitespaces)
            if let delimiters = Self.displayMathDelimiters(for: trimmedLine) {
                if delimiters.isSingleLine {
                    appendMarkdown()
                    result.append(.math(LatexSource.expression(from: trimmedLine)))
                    index += 1
                    continue
                }

                var closingIndex = index + 1
                while closingIndex < lines.count,
                      lines[closingIndex].trimmingCharacters(in: .whitespaces) != delimiters.closing {
                    closingIndex += 1
                }

                if closingIndex < lines.count {
                    appendMarkdown()
                    let mathLines = lines[(index + 1)..<closingIndex]
                    result.append(.math(mathLines.joined(separator: "\n")))
                    index = closingIndex + 1
                    continue
                }
            }

            if index + 1 < lines.count,
               Self.isTableRow(line),
               Self.isTableDelimiter(lines[index + 1]) {
                appendMarkdown()
                let headers = Self.tableCells(from: line)
                index += 2
                var rows: [[String]] = []
                while index < lines.count,
                      Self.isTableRow(lines[index]),
                      !Self.isTableDelimiter(lines[index]) {
                    rows.append(Self.tableCells(from: lines[index]))
                    index += 1
                }
                result.append(.table(headers: headers, rows: rows))
                continue
            }

            // Pull out `![alt](url)`/`<img src="...">` wherever they appear —
            // very commonly inline after a bold label inside a list item
            // ("* **View 1:** ![alt](url)"), not on their own line. This
            // matters more than it looks: AttributedString(markdown:), which
            // renders everything that isn't special-cased here, has no
            // concept of image syntax and silently drops it — no broken-image
            // icon, no alt text, nothing — so anything left for it to parse
            // needs the image syntax stripped out first.
            let imageMatches = Self.inlineImages(in: line)
            if imageMatches.isEmpty {
                markdownLines.append(line)
            } else {
                var cursor = line.startIndex
                for match in imageMatches {
                    let before = String(line[cursor..<match.range.lowerBound])
                    if !before.trimmingCharacters(in: .whitespaces).isEmpty {
                        markdownLines.append(before)
                    }
                    appendMarkdown()
                    result.append(.image(url: match.url, alt: match.alt))
                    cursor = match.range.upperBound
                }
                let after = String(line[cursor...])
                if !after.trimmingCharacters(in: .whitespaces).isEmpty {
                    markdownLines.append(after)
                }
            }
            index += 1
        }

        appendMarkdown()
        return result
    }

    private static func isTableRow(_ line: String) -> Bool {
        line.contains("|") && !line.trimmingCharacters(in: .whitespaces).isEmpty
    }

    private static func isTableDelimiter(_ line: String) -> Bool {
        let cells = tableCells(from: line)
        return !cells.isEmpty && cells.allSatisfy { cell in
            let value = cell.trimmingCharacters(in: .whitespaces)
            return value.count >= 3 && value.allSatisfy { $0 == "-" || $0 == ":" }
        }
    }

    private static func tableCells(from line: String) -> [String] {
        var value = line.trimmingCharacters(in: .whitespaces)
        if value.hasPrefix("|") { value.removeFirst() }
        if value.hasSuffix("|") { value.removeLast() }
        return value.split(separator: "|", omittingEmptySubsequences: false)
            .map { String($0).trimmingCharacters(in: .whitespaces) }
    }

    private static func displayMathDelimiters(
        for line: String
    ) -> (closing: String, isSingleLine: Bool)? {
        if line == "$$" { return ("$$", false) }
        if line == "\\[" { return ("\\]", false) }
        if line.hasPrefix("$$"), line.hasSuffix("$$"), line.count > 4 {
            return ("$$", true)
        }
        if line.hasPrefix("\\["), line.hasSuffix("\\]"), line.count > 4 {
            return ("\\]", true)
        }
        return nil
    }

    private static let markdownImageRegex = try! NSRegularExpression(pattern: #"!\[([^\]]*)\]\(([^)]+)\)"#)
    private static let htmlImageRegex = try! NSRegularExpression(
        pattern: #"<img\b[^>]*\bsrc\s*=\s*["']([^"']+)["'][^>]*>"#,
        options: [.caseInsensitive]
    )
    private static let htmlAltRegex = try! NSRegularExpression(
        pattern: #"\balt\s*=\s*["']([^"']*)["']"#,
        options: [.caseInsensitive]
    )

    /// Finds every `![alt](url)` and `<img src="...">` in a line, in order.
    /// Used instead of a whole-line-only check because models very commonly
    /// write the image after other text on the same line (a bold label
    /// inside a list item, for instance).
    private static func inlineImages(in line: String) -> [(range: Range<String.Index>, url: String, alt: String)] {
        guard line.contains("![") || line.localizedCaseInsensitiveContains("<img") else { return [] }
        var matches: [(range: Range<String.Index>, url: String, alt: String)] = []
        let fullRange = NSRange(line.startIndex..., in: line)

        for result in markdownImageRegex.matches(in: line, range: fullRange) {
            guard result.numberOfRanges >= 3,
                  let range = Range(result.range, in: line),
                  let altRange = Range(result.range(at: 1), in: line),
                  let urlRange = Range(result.range(at: 2), in: line) else { continue }
            var url = String(line[urlRange])
            if let spaceIndex = url.firstIndex(of: " ") {
                url = String(url[..<spaceIndex])
            }
            guard !url.isEmpty else { continue }
            matches.append((range, url, String(line[altRange])))
        }

        for result in htmlImageRegex.matches(in: line, range: fullRange) {
            guard result.numberOfRanges >= 2,
                  let range = Range(result.range, in: line),
                  let srcRange = Range(result.range(at: 1), in: line) else { continue }
            let tag = String(line[range])
            let alt = htmlAltRegex.firstMatch(in: tag, range: NSRange(tag.startIndex..., in: tag)).flatMap { match -> String? in
                Range(match.range(at: 1), in: tag).map { String(tag[$0]) }
            } ?? ""
            matches.append((range, String(line[srcRange]), alt))
        }

        return matches.sorted { $0.range.lowerBound < $1.range.lowerBound }
    }
}

/// Drives "Print Chat…" (File menu, ⌘P). The system print sheet's own "PDF"
/// button doubles as PDF export, so there's no separate export path to
/// maintain.
@MainActor
enum ChatExport {
    static func printChat(messages: [ChatMessage], providerName: String, modelName: String) {
        guard !messages.isEmpty else { return }

        let printInfo = (NSPrintInfo.shared.copy() as? NSPrintInfo) ?? NSPrintInfo.shared
        printInfo.topMargin = 36
        printInfo.bottomMargin = 36
        printInfo.leftMargin = 36
        printInfo.rightMargin = 36
        printInfo.horizontalPagination = .fit
        printInfo.verticalPagination = .automatic

        let pageWidth = printInfo.paperSize.width - printInfo.leftMargin - printInfo.rightMargin
        let pageHeight = printInfo.paperSize.height - printInfo.topMargin - printInfo.bottomMargin
        guard pageWidth > 0, pageHeight > 0 else { return }

        // Render each section (the header, then one per message) to its own
        // right-sized bitmap, then composite those onto fixed-size page
        // canvases with plain Core Graphics drawing below. A single
        // `ImageRenderer` call covering the whole, arbitrarily tall
        // transcript can exceed Core Animation's maximum texture dimensions
        // — which previously cut a tall Mermaid diagram off mid-render and
        // left everything after it blank. Scoping each render call to one
        // section keeps it well under that ceiling, and pagination becomes
        // bitmap slicing instead of SwiftUI layout.
        var sections: [NSImage] = []
        if let header = renderSection(
            PrintableTranscriptHeader(providerName: providerName, modelName: modelName)
                .padding(.horizontal, 24)
                .padding(.top, 24)
                .frame(width: pageWidth, alignment: .leading)
        ) {
            sections.append(header)
        }
        for message in messages.sorted(by: { $0.createdAt < $1.createdAt }) {
            if let row = renderSection(
                PrintableMessageRow(message: message)
                    .padding(.horizontal, 24)
                    .frame(width: pageWidth, alignment: .leading)
            ) {
                sections.append(row)
            }
        }
        guard !sections.isEmpty else { return }

        let pages = paginate(sections: sections, pageSize: CGSize(width: pageWidth, height: pageHeight))
        guard !pages.isEmpty else { return }

        let printView = PaginatedImagePrintView(
            pages: pages,
            pageSize: CGSize(width: pageWidth, height: pageHeight)
        )
        let operation = NSPrintOperation(view: printView, printInfo: printInfo)
        operation.showsPrintPanel = true
        operation.showsProgressPanel = true
        operation.run()
    }

    private static func renderSection<V: View>(_ view: V) -> NSImage? {
        let renderer = ImageRenderer(
            content: view
                .foregroundStyle(.black)
                .background(Color.white)
                .preferredColorScheme(.light)
        )
        renderer.scale = 2
        return renderer.nsImage
    }

    /// Flows pre-rendered section bitmaps top-to-bottom into fixed-size page
    /// canvases, slicing any section taller than one page across consecutive
    /// pages. Pure bitmap compositing — no SwiftUI layout involved — so it
    /// can't hit the texture-size ceiling described above.
    private static func paginate(sections: [NSImage], pageSize: CGSize) -> [NSImage] {
        var pages: [NSImage] = []
        var page = NSImage(size: pageSize)
        var cursor: CGFloat = 0
        let spacing: CGFloat = 14

        func fillWhite(_ image: NSImage) {
            image.lockFocus()
            NSColor.white.setFill()
            NSRect(origin: .zero, size: pageSize).fill()
            image.unlockFocus()
        }

        func startPage() {
            page = NSImage(size: pageSize)
            fillWhite(page)
            cursor = 0
        }

        startPage()

        for section in sections {
            var consumed: CGFloat = 0
            let totalHeight = section.size.height
            while consumed < totalHeight {
                let spaceLeft = pageSize.height - cursor
                if spaceLeft <= 0 {
                    pages.append(page)
                    startPage()
                    continue
                }
                let chunk = min(totalHeight - consumed, spaceLeft)
                let sourceRect = NSRect(
                    x: 0,
                    y: totalHeight - consumed - chunk,
                    width: section.size.width,
                    height: chunk
                )
                let destRect = NSRect(
                    x: 0,
                    y: pageSize.height - cursor - chunk,
                    width: pageSize.width,
                    height: chunk
                )
                page.lockFocus()
                section.draw(in: destRect, from: sourceRect, operation: .sourceOver, fraction: 1)
                page.unlockFocus()

                consumed += chunk
                cursor += chunk
            }
            if cursor + spacing <= pageSize.height {
                cursor += spacing
            } else {
                pages.append(page)
                startPage()
            }
        }

        pages.append(page)
        return pages
    }
}

/// Presents independently rendered page-sized images to AppKit. Keeping every
/// backing bitmap to one physical page avoids the maximum texture-size failure
/// caused by rasterizing an arbitrarily tall transcript into one image.
private final class PaginatedImagePrintView: NSView {
    private let pages: [NSImage]
    private let pageSize: CGSize

    init(pages: [NSImage], pageSize: CGSize) {
        self.pages = pages
        self.pageSize = pageSize
        super.init(frame: NSRect(
            origin: .zero,
            size: CGSize(width: pageSize.width, height: pageSize.height * CGFloat(pages.count))
        ))
    }

    required init?(coder: NSCoder) {
        fatalError("init(coder:) is not used for print-only views")
    }

    override var isFlipped: Bool { true }

    override func knowsPageRange(_ range: NSRangePointer) -> Bool {
        range.pointee = NSRange(location: 1, length: pages.count)
        return true
    }

    override func rectForPage(_ page: Int) -> NSRect {
        let y = CGFloat(page - 1) * pageSize.height
        return NSRect(x: 0, y: y, width: pageSize.width, height: pageSize.height)
    }

    override func draw(_ dirtyRect: NSRect) {
        NSColor.white.setFill()
        dirtyRect.fill()

        let pageIndex = min(
            max(Int(floor(dirtyRect.minY / pageSize.height)), 0),
            pages.count - 1
        )
        let destination = NSRect(
            x: 0,
            y: CGFloat(pageIndex) * pageSize.height,
            width: pageSize.width,
            height: pageSize.height
        )
        pages[pageIndex].draw(
            in: destination,
            from: .zero,
            operation: .sourceOver,
            fraction: 1,
            respectFlipped: true,
            hints: nil
        )
    }
}

/// The transcript title block rendered once at the top of "Print Chat…"
/// output. Kept separate from the live `MessageList`/`MessageRow` because
/// it's forced to light appearance for paper output and has no interactive
/// state (toggles, hover buttons) to carry. Rendered as its own section —
/// see `ChatExport.paginate` — rather than as part of one whole-transcript
/// view tree.
private struct PrintableTranscriptHeader: View {
    let providerName: String
    let modelName: String

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            Text("llmtui Chat Transcript")
                .font(.title2.weight(.semibold))
            Text("\(providerName) · \(modelName)")
                .font(.caption)
                .foregroundStyle(.secondary)
            Text(Date(), format: .dateTime.year().month().day().hour().minute())
                .font(.caption2)
                .foregroundStyle(.secondary)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct PrintableMessageRow: View {
    let message: ChatMessage

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text(roleTitle)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(.secondary)
                Spacer()
                Text(message.createdAt, format: .dateTime.year().month().day().hour().minute())
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
            PrintableRichMessageView(text: message.text)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(backgroundStyle, in: RoundedRectangle(cornerRadius: 10))
    }

    private var roleTitle: String {
        switch message.role {
        case .user: "You"
        case .assistant: "Assistant"
        case .tool: "Tool"
        }
    }

    private var backgroundStyle: Color {
        switch message.role {
        case .user: Color.accentColor.opacity(0.08)
        case .assistant: Color.gray.opacity(0.08)
        case .tool: Color.orange.opacity(0.08)
        }
    }
}

/// Mirrors `RichMessageView`'s block rendering for print, but never embeds a
/// live `WKWebView` (KaTeX, Mermaid) — `ImageRenderer` cannot rasterize a
/// webview at all, so content routed through one simply vanishes from the
/// printed page. Mermaid diagrams use the static snapshot captured by the
/// live renderer instead; LaTeX and plain code fall back to
/// `PrintableCodeBlockView`, a `ScrollView`-free sibling of `CodeBlockView`
/// (see its doc comment for why the live version can't be reused here).
private struct PrintableRichMessageView: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(Array(RichMessageView(text: text).blocks.enumerated()), id: \.offset) { _, block in
                switch block {
                case .markdown(let source):
                    PrintableMarkdownText(source: source)
                case .code(let language, let source):
                    PrintableCodeBlockView(language: language, source: source)
                case .table(let headers, let rows):
                    PrintableMarkdownTableView(headers: headers, rows: rows)
                case .mermaid(let source):
                    PrintableMermaidView(source: source)
                case .math(let source):
                    PrintableCodeBlockView(language: "latex", source: source)
                case .latexDocument(let source):
                    PrintableCodeBlockView(language: "latex", source: source)
                case .image(let url, let alt):
                    MarkdownImageView(url: url, alt: alt)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct PrintableMermaidView: View {
    let source: String

    var body: some View {
        if let image = MermaidPrintImageCache.image(for: source) {
            Image(nsImage: image)
                .resizable()
                .aspectRatio(contentMode: .fit)
                .frame(maxWidth: .infinity)
        } else {
            PrintableCodeBlockView(language: "mermaid", source: source)
        }
    }
}

/// Renders markdown blocks for print using the same `MarkdownDocument`
/// parsing as the live `MarkdownText`, but swaps `MathAwareText` for
/// `PrintableMathAwareText` so inline math never routes through a
/// `WKWebView` (see `PrintableRichMessageView`'s doc comment).
private struct PrintableMarkdownText: View {
    let source: String

    private var document: MarkdownDocument {
        MarkdownDocument(source: source)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(document.blocks) { block in
                PrintableMarkdownBlockView(block: block)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct PrintableMarkdownBlockView: View {
    let block: MarkdownDocument.Block

    @ViewBuilder
    var body: some View {
        switch block.kind {
        case .paragraph:
            PrintableMathAwareText(content: block.content)
                .font(.body)
                .padding(.bottom, 10)
        case .heading(let level):
            PrintableMathAwareText(
                content: block.content,
                fontSize: headingPointSize(level),
                fontWeight: level <= 2 ? 700 : 600
            )
                .font(headingFont(level))
                .padding(.top, level <= 2 ? 7 : 4)
                .padding(.bottom, level <= 2 ? 8 : 6)
        case .listItem(let marker, let depth):
            HStack(alignment: .firstTextBaseline, spacing: 7) {
                Text(markerText(marker))
                    .font(.body.monospacedDigit())
                    .frame(minWidth: 15, alignment: .trailing)
                    .accessibilityHidden(true)
                PrintableMathAwareText(content: block.content)
                    .font(.body)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.leading, CGFloat(max(depth - 1, 0)) * 22)
            .padding(.bottom, 4)
        case .blockQuote(let depth):
            HStack(spacing: 10) {
                RoundedRectangle(cornerRadius: 1)
                    .fill(Color.accentColor.opacity(0.65))
                    .frame(width: 3)
                PrintableMathAwareText(content: block.content, foreground: .secondary)
                    .font(.body)
                    .foregroundStyle(.secondary)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .padding(.leading, CGFloat(max(depth - 1, 0)) * 14)
            .padding(.vertical, 5)
            .padding(.bottom, 5)
        case .thematicBreak:
            Divider()
                .padding(.vertical, 9)
        }
    }

    private func markerText(_ marker: MarkdownDocument.ListMarker) -> String {
        switch marker {
        case .unordered:
            return "•"
        case .ordered(let ordinal):
            return "\(ordinal)."
        }
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1: .title2.bold()
        case 2: .title3.bold()
        case 3: .headline
        case 4: .headline.weight(.semibold)
        case 5: .subheadline.bold()
        default: .caption.bold()
        }
    }

    private func headingPointSize(_ level: Int) -> CGFloat {
        switch level {
        case 1: 22
        case 2: 20
        case 3: 17
        case 4: 16
        case 5: 14
        default: 12
        }
    }
}

/// A print-safe stand-in for `MathAwareText`: instead of rendering math
/// through a `WKWebView` (which `ImageRenderer` can't capture at all, so the
/// text would just vanish), it shows the raw LaTeX expression inline in a
/// monospaced, secondary-colored style — legible, if not typeset.
private struct PrintableMathAwareText: View {
    let content: AttributedString
    var fontSize: CGFloat = 15
    var fontWeight: Int = 400
    var foreground: KaTeXForeground = .primary

    var body: some View {
        Text(Self.renderable(content))
    }

    private static func renderable(_ content: AttributedString) -> AttributedString {
        let plainText = String(content.characters)
        let mathRanges = LatexSource.inlineRanges(in: plainText)
        guard !mathRanges.isEmpty else { return content }

        var result = AttributedString()
        var cursor = plainText.startIndex
        for mathRange in mathRanges {
            if cursor < mathRange.lowerBound, let attributedRange = Range(cursor..<mathRange.lowerBound, in: content) {
                result += content[attributedRange]
            }
            var mathFragment = AttributedString(LatexSource.expression(from: String(plainText[mathRange])))
            mathFragment.font = .system(.body, design: .monospaced)
            mathFragment.foregroundColor = .secondary
            result += mathFragment
            cursor = mathRange.upperBound
        }
        if cursor < plainText.endIndex, let attributedRange = Range(cursor..<plainText.endIndex, in: content) {
            result += content[attributedRange]
        }
        return result
    }
}

/// A fully eager table for `ImageRenderer`. The interactive table uses a
/// `LazyVGrid` inside a horizontal `ScrollView`, whose cells are intentionally
/// not created when rendered off-screen for printing.
private struct PrintableMarkdownTableView: View {
    let headers: [String]
    let rows: [[String]]

    private var columnCount: Int {
        max(headers.count, rows.map(\.count).max() ?? 0, 1)
    }

    var body: some View {
        Grid(horizontalSpacing: 0, verticalSpacing: 0) {
            GridRow {
                ForEach(0..<columnCount, id: \.self) { index in
                    cell(headers[safe: index] ?? "", isHeader: true)
                }
            }

            ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                GridRow {
                    ForEach(0..<columnCount, id: \.self) { index in
                        cell(row[safe: index] ?? "", isHeader: false)
                    }
                }
            }

            if rows.isEmpty {
                GridRow {
                    Text("No data rows were provided in this table.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(12)
                        .gridCellColumns(columnCount)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.quaternary.opacity(0.3))
        .overlay(Rectangle().stroke(Color.primary.opacity(0.12), lineWidth: 1))
    }

    private func cell(_ value: String, isHeader: Bool) -> some View {
        PrintableMathAwareText(content: tableContent(value), fontWeight: isHeader ? 600 : 400)
            .font(isHeader ? .body.weight(.semibold) : .body)
            .foregroundStyle(.primary)
            .frame(maxWidth: .infinity, minHeight: 38, alignment: .leading)
            .padding(.horizontal, 8)
            .padding(.vertical, 7)
            .background(isHeader ? Color.primary.opacity(0.08) : Color.clear)
            .overlay(Rectangle().stroke(Color.primary.opacity(0.08), lineWidth: 0.5))
    }

    private func tableContent(_ value: String) -> AttributedString {
        let options = AttributedString.MarkdownParsingOptions(
            interpretedSyntax: .inlineOnlyPreservingWhitespace,
            failurePolicy: .returnPartiallyParsedIfPossible
        )
        return (try? AttributedString(markdown: value, options: options)) ?? AttributedString(value)
    }
}

private struct LatexDocumentView: View {
    let source: String

    var body: some View {
        LatexMathView(source: source, isDisplay: true)
            .frame(maxWidth: .infinity, alignment: .center)
    }
}

/// Renders a standalone `![alt](url)` or `<img>` line as an actual image,
/// GitHub-README style, scaled to fit the message bubble instead of
/// overflowing it. Only http/https URLs are fetched — same restriction as
/// `WebToolRuntime.fetch` — since this is a network request the model's own
/// text can trigger without the user picking the URL themselves.
private struct MarkdownImageView: View {
    let url: String
    let alt: String

    private var imageURL: URL? {
        guard let url = URL(string: url), let scheme = url.scheme?.lowercased(),
              scheme == "http" || scheme == "https" else { return nil }
        return url
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if let imageURL {
                Button {
                    NSWorkspace.shared.open(imageURL)
                } label: {
                    AsyncImage(url: imageURL) { phase in
                        switch phase {
                        case .success(let image):
                            image
                                .resizable()
                                .scaledToFit()
                                .frame(maxWidth: .infinity, maxHeight: 360)
                                .clipShape(RoundedRectangle(cornerRadius: 10))
                        case .failure:
                            brokenImagePlaceholder
                        case .empty:
                            loadingPlaceholder
                        @unknown default:
                            loadingPlaceholder
                        }
                    }
                }
                .buttonStyle(.plain)
                .help("Open image in browser")
            } else {
                brokenImagePlaceholder
            }
            if !alt.isEmpty {
                Text(alt)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }

    private var loadingPlaceholder: some View {
        RoundedRectangle(cornerRadius: 10)
            .fill(.quaternary.opacity(0.4))
            .frame(height: 160)
            .overlay(ProgressView())
    }

    private var brokenImagePlaceholder: some View {
        HStack(spacing: 8) {
            Image(systemName: "photo.badge.exclamationmark")
                .foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text(alt.isEmpty ? "Image unavailable" : alt)
                    .font(.caption)
                Text(url)
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .textSelection(.enabled)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(.quaternary.opacity(0.4), in: RoundedRectangle(cornerRadius: 10))
    }
}

private struct CodeBlockView: View {
    let language: String
    let source: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !language.isEmpty {
                Text(language.uppercased())
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            ScrollView(.horizontal, showsIndicators: false) {
                SyntaxHighlightedCode(source: source, language: language)
                    .font(.system(.body, design: .monospaced))
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(12)
            }
        }
        .padding(10)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 10))
    }
}

/// A `ScrollView`-free sibling of `CodeBlockView` for print. `ImageRenderer`
/// does not reliably rasterize `ScrollView` content laid out off-screen — the
/// live code block was coming out as an empty rounded rectangle in printed
/// output. There's no horizontal scrolling on paper anyway, so this just lets
/// long lines wrap instead.
private struct PrintableCodeBlockView: View {
    let language: String
    let source: String

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if !language.isEmpty {
                Text(language.uppercased())
                    .font(.caption2.weight(.semibold))
                    .foregroundStyle(.secondary)
            }
            SyntaxHighlightedCode(source: source, language: language)
                .font(.system(.body, design: .monospaced))
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .padding(10)
        .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 10))
    }
}

private struct SyntaxHighlightedCode: View {
    let source: String
    let language: String

    var body: some View {
        Text(highlightedSource)
    }

    private var highlightedSource: AttributedString {
        var value = AttributedString(source)
        let patterns: [(String, Color)] = [
            (#"("|')(?:\\.|[^"'\\])*\1"#, .orange),
            (#"//.*|#.*|--.*"#, .green),
            (#"\b(class|struct|enum|func|return|if|else|for|while|in|let|var|const|def|import|from|package|type|interface|go|switch|case|true|false|nil|null|None)\b"#, .purple),
            (#"\b\d+(?:\.\d+)?\b"#, .cyan)
        ]

        for (pattern, color) in patterns {
            guard let expression = try? NSRegularExpression(pattern: pattern, options: []) else { continue }
            let range = NSRange(source.startIndex..<source.endIndex, in: source)
            for match in expression.matches(in: source, options: [], range: range).reversed() {
                guard let stringRange = Range(match.range, in: source),
                      let attributedRange = Range(stringRange, in: value) else { continue }
                value[attributedRange].foregroundColor = color
            }
        }
        return value
    }
}

private struct MarkdownTableView: View {
    let headers: [String]
    let rows: [[String]]

    private struct Cell: Identifiable {
        let id: String
        let value: String
        let isHeader: Bool
    }

    private var columnCount: Int {
        max(headers.count, rows.map(\.count).max() ?? 0)
    }

    private var cells: [Cell] {
        let headerCells = (0..<columnCount).map { index in
            Cell(id: "header-\(index)", value: headers[safe: index] ?? "", isHeader: true)
        }
        let rowCells = rows.enumerated().flatMap { rowIndex, row in
            (0..<columnCount).map { columnIndex in
                Cell(
                    id: "row-\(rowIndex)-\(columnIndex)",
                    value: row[safe: columnIndex] ?? "",
                    isHeader: false
                )
            }
        }
        return headerCells + rowCells
    }

    var body: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            LazyVGrid(
                columns: Array(repeating: GridItem(.flexible(minimum: 150, maximum: 340), spacing: 0, alignment: .leading), count: max(columnCount, 1)),
                spacing: 0
            ) {
                ForEach(cells) { item in
                    cell(item.value, isHeader: item.isHeader)
                }

                if rows.isEmpty {
                    Text("No data rows were provided in this table.")
                        .font(.callout)
                        .foregroundStyle(.secondary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(12)
                        .gridCellColumns(max(columnCount, 1))
                }
            }
            .frame(width: max(CGFloat(max(columnCount, 1)) * 180, 700), alignment: .leading)
        }
        .background(.quaternary.opacity(0.3))
        .overlay(Rectangle().stroke(Color.primary.opacity(0.12), lineWidth: 1))
    }

    private func cell(_ value: String, isHeader: Bool) -> some View {
        MathAwareText(content: tableContent(value), fontWeight: isHeader ? 600 : 400)
            .font(isHeader ? .body.weight(.semibold) : .body)
            .foregroundStyle(.primary)
            .frame(maxWidth: .infinity, minHeight: 38, alignment: .leading)
            .padding(.horizontal, 10)
            .padding(.vertical, 7)
            .background(isHeader ? Color.primary.opacity(0.08) : Color.clear)
            .overlay(Rectangle().stroke(Color.primary.opacity(0.08), lineWidth: 0.5))
    }

    private func tableContent(_ value: String) -> AttributedString {
        let options = AttributedString.MarkdownParsingOptions(
            interpretedSyntax: .inlineOnlyPreservingWhitespace,
            failurePolicy: .returnPartiallyParsedIfPossible
        )
        return (try? AttributedString(markdown: value, options: options)) ?? AttributedString(value)
    }
}

private extension Array {
    subscript(safe index: Int) -> Element? {
        indices.contains(index) ? self[index] : nil
    }
}

struct ComposerView: View {
    let model: AppModel

    @State private var isPickingAttachment = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if !model.draftAttachments.isEmpty {
                HStack(spacing: 6) {
                    ForEach(model.draftAttachments) { attachment in
                        AttachmentChip(attachment: attachment) {
                            model.removeAttachment(attachment)
                        }
                    }
                }
            }
            HStack(alignment: .bottom, spacing: 12) {
                VStack(spacing: 8) {
                    Button {
                        model.nativeAgentEnabled.toggle()
                    } label: {
                        Image(systemName: "infinity")
                            .frame(width: 24, height: 24)
                            .foregroundStyle(model.nativeAgentEnabled ? Color.accentColor : Color.secondary)
                            .background(
                                model.nativeAgentEnabled ? Color.accentColor.opacity(0.14) : Color.clear,
                                in: Circle()
                            )
                    }
                    .buttonStyle(.plain)
                    .font(.title2)
                    .accessibilityLabel("Native chat agent")
                    .accessibilityValue(model.nativeAgentEnabled ? "Enabled" : "Disabled")
                    .help(model.nativeAgentEnabled
                        ? "Disable the native GUI agent for new messages"
                        : "Enable the native GUI agent for new messages")

                    Button("Attach image", systemImage: "paperclip") {
                        isPickingAttachment = true
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.plain)
                    .font(.title2)
                    .disabled(model.draftAttachments.count >= 4)
                    .help("Attach up to 4 images, or paste an image into the prompt")
                }

                MultilinePromptEditor(
                    text: Bindable(model).draftMessage,
                    placeholder: "Ask llmtui anything…",
                    onSend: model.sendDraft,
                    onPasteAttachment: model.addAttachment
                )
                .frame(minHeight: 34, maxHeight: 120)
                .padding(.horizontal, 12)
                .background(.quaternary.opacity(0.7), in: RoundedRectangle(cornerRadius: 12))
                ZStack(alignment: .topTrailing) {
                    ZStack {
                        ContextUsageRing(usage: model.contextUsage)
                            .frame(width: 36, height: 36)
                        Button("Send", systemImage: "arrow.up.circle.fill") {
                            model.sendDraft()
                        }
                        .labelStyle(.iconOnly)
                        .font(.title)
                        .buttonStyle(.plain)
                        .disabled(model.draftMessage.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        .help(model.isGenerating
                            ? "Queue message — sends automatically once the current reply finishes"
                            : "Send")
                    }
                    if !model.queuedMessages.isEmpty {
                        Text("\(model.queuedMessages.count)")
                            .font(.caption2.weight(.bold))
                            .foregroundStyle(.white)
                            .padding(4)
                            .frame(minWidth: 16, minHeight: 16)
                            .background(Color.accentColor, in: Circle())
                            .offset(x: 4, y: -4)
                            .allowsHitTesting(false)
                    }
                }
            }
        }
        .padding(18)
        .fileImporter(
            isPresented: $isPickingAttachment,
            allowedContentTypes: [
                .png, .jpeg, .heic, .heif, .tiff, .bmp, .gif, .webP
            ],
            allowsMultipleSelection: true
        ) { result in
            guard case .success(let urls) = result else { return }
            for url in urls {
                model.addAttachment(from: url)
            }
        }
    }
}

/// A rough live estimate of context-window usage for the next request, shown
/// in the composer since there's no real token count until a provider reply
/// actually carries `usage` (none of the ones this app talks to do).
/// A thin ring hugging the send button, filling as the next request
/// approaches the model's context budget — deliberately understated rather
/// than a labeled gauge, since this is a secondary, rough estimate (see
/// `AppModel.contextUsage`), not a precise reading.
private struct ContextUsageRing: View {
    let usage: (used: Int, total: Int)

    private var fraction: Double {
        guard usage.total > 0 else { return 0 }
        return min(Double(usage.used) / Double(usage.total), 1.0)
    }

    private var tint: Color {
        switch fraction {
        case ..<0.7: Color(red: 0.4, green: 0.82, blue: 0.47)
        case ..<0.9: .orange
        default: .red
        }
    }

    var body: some View {
        ZStack {
            Circle()
                .stroke(Color.secondary.opacity(0.18), lineWidth: 2)
            Circle()
                .trim(from: 0, to: max(fraction, 0.0001))
                .stroke(tint, style: StrokeStyle(lineWidth: 2, lineCap: .round))
                .rotationEffect(.degrees(-90))
        }
        .help("Estimated \(usage.used) / \(usage.total) tokens (\(Int(fraction * 100))% of context window)")
    }
}

struct ToolApprovalPanel: View {
    let model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Label("Approval required", systemImage: "exclamationmark.shield.fill")
                .font(.title3.weight(.semibold))
                .foregroundStyle(.orange)

            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    ForEach(model.pendingToolRequests) { request in
                        VStack(alignment: .leading, spacing: 10) {
                            if request.name == "change_apply", let plan = model.personalAppsRuntime.mutationPlan(from: request.arguments) {
                                Text(plan.kind.title)
                                    .font(.title3.weight(.semibold))
                                Text("Review the exact operation below. Approval is bound to digest \(plan.digest.prefix(12))… and expires in five minutes.")
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                                VStack(alignment: .leading, spacing: 6) {
                                    ForEach(Array(plan.exactPreview.enumerated()), id: \.offset) { _, line in
                                        Text(line)
                                            .font(.callout)
                                            .textSelection(.enabled)
                                            .fixedSize(horizontal: false, vertical: true)
                                    }
                                }
                                .padding(10)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .background(.quaternary.opacity(0.4), in: RoundedRectangle(cornerRadius: 8))
                            }
                            VStack(alignment: .leading, spacing: 3) {
                                Text(request.name)
                                    .font(.subheadline.weight(.semibold))
                                Text(request.summary)
                                    .font(.caption)
                                    .foregroundStyle(.secondary)
                            }
                            HStack {
                                Spacer()
                                Button("Reject", role: .cancel) {
                                    model.resolveTool(request, approved: false)
                                }
                                Button("Approve") {
                                    model.resolveTool(request, approved: true)
                                }
                                .buttonStyle(.borderedProminent)
                                .keyboardShortcut(.defaultAction)
                            }
                        }
                    }
                }
            }
            .frame(maxHeight: 380)
        }
        .padding(24)
        .frame(minWidth: 460, idealWidth: 520, maxWidth: 560)
    }
}

/// Follow-up messages typed while a prior turn was still generating. Each
/// is dispatched automatically, in order, once the current turn finishes —
/// clicking the text loads it back into the composer for editing; the "x"
/// drops it entirely.
struct QueuedMessagesPanel: View {
    let model: AppModel

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Up next · \(model.queuedMessages.count)")
                .font(.caption.weight(.semibold))
                .foregroundStyle(.secondary)

            ForEach(model.queuedMessages) { message in
                HStack(spacing: 8) {
                    Image(systemName: "clock")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Text(message.text)
                        .font(.callout)
                        .lineLimit(1)
                        .foregroundStyle(.primary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .contentShape(Rectangle())
                        .onTapGesture {
                            model.editQueuedMessage(message)
                        }
                    if !message.attachments.isEmpty {
                        Label("\(message.attachments.count)", systemImage: "photo")
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .labelStyle(.titleAndIcon)
                    }
                    Button {
                        model.removeQueuedMessage(message)
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.secondary)
                    }
                    .buttonStyle(.plain)
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 7)
                .background(.quaternary.opacity(0.4), in: RoundedRectangle(cornerRadius: 8))
                .help("Click to edit this queued message — it sends automatically once the current reply finishes")
            }
        }
        .padding(16)
    }
}

struct UserQuestionPanel: View {
    let question: UserQuestion
    let onAnswer: (String) -> Void
    let onSkip: () -> Void

    @State private var answer = ""
    @FocusState private var answerIsFocused: Bool

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("Assistant needs your input", systemImage: "questionmark.bubble.fill")
                .font(.headline)
                .foregroundStyle(.tint)

            Text(question.question)
                .font(.body)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)

            if !question.options.isEmpty {
                VStack(alignment: .leading, spacing: 7) {
                    ForEach(question.options, id: \.self) { option in
                        Button {
                            onAnswer(option)
                        } label: {
                            HStack {
                                Text(option)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                Image(systemName: "arrow.right.circle")
                            }
                        }
                        .buttonStyle(.bordered)
                        .controlSize(.regular)
                    }
                }
            }

            if question.allowsFreeText {
                HStack(spacing: 8) {
                    TextField(question.placeholder ?? "Type your answer…", text: $answer)
                        .textFieldStyle(.roundedBorder)
                        .focused($answerIsFocused)
                        .onSubmit(submitAnswer)

                    Button("Continue", action: submitAnswer)
                        .buttonStyle(.borderedProminent)
                        .disabled(answer.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }

            HStack {
                Text("The current chat turn will resume after your response.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Skip", action: onSkip)
                    .buttonStyle(.borderless)
            }
        }
        .padding(16)
        .background(Color.accentColor.opacity(0.08), in: RoundedRectangle(cornerRadius: 12))
        .overlay {
            RoundedRectangle(cornerRadius: 12)
                .stroke(Color.accentColor.opacity(0.28), lineWidth: 1)
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 10)
        .onAppear {
            if question.allowsFreeText && question.options.isEmpty {
                answerIsFocused = true
            }
        }
    }

    private func submitAnswer() {
        let value = answer.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty else { return }
        onAnswer(value)
    }
}

private func toolActivityIsActive(_ activity: ToolActivity) -> Bool {
    switch activity.status {
    case .requested, .awaitingApproval, .running:
        true
    case .succeeded, .failed, .rejected:
        false
    }
}

struct ToolActivityToggle: View {
    @Binding var isShown: Bool
    let activityCount: Int
    let hasActiveActivity: Bool

    var body: some View {
        HStack {
            Button {
                withAnimation(.easeInOut(duration: 0.2)) {
                    isShown.toggle()
                }
            } label: {
                Label(
                    isShown ? "Hide tool activity" : "Show tool activity",
                    systemImage: isShown ? "chevron.down" : "wrench.and.screwdriver.fill"
                )
            }
            .buttonStyle(.borderless)
            .font(.caption.weight(.semibold))

            Text("\(activityCount) event\(activityCount == 1 ? "" : "s")")
                .font(.caption2)
                .foregroundStyle(.secondary)

            Spacer()

            if hasActiveActivity {
                ProgressView()
                    .controlSize(.small)
                Text("Working")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
        .padding(.horizontal, 28)
        .padding(.vertical, 5)
        .background(.thinMaterial)
    }
}

struct ToolActivityPanel: View {
    let activities: [ToolActivity]

    var body: some View {
        ScrollView(.vertical, showsIndicators: true) {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(Array(activities.enumerated()), id: \.element.id) { index, activity in
                    ToolActivityRow(activity: activity)
                    if index < activities.count - 1 {
                        Divider().opacity(0.5)
                    }
                }
            }
            .padding(.horizontal, 24)
            .padding(.vertical, 8)
        }
        .frame(maxHeight: 360)
        .background(.thinMaterial)
    }
}

/// A single tool call, collapsed to one line by default. Expanding it
/// reveals the exact arguments the model called it with ("Command") and
/// either the raw result text or — for `web_search` — a card per source,
/// so a failure is as inspectable here as it already is in what gets sent
/// back to the model (see `ToolRegistry.execute`'s `"Error: "`-prefixed
/// content).
struct ToolActivityRow: View {
    let activity: ToolActivity
    @State private var isExpanded = false

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                withAnimation(.easeInOut(duration: 0.15)) { isExpanded.toggle() }
            } label: {
                HStack(spacing: 10) {
                    ZStack {
                        Circle().fill(toolStatusColor(activity.status).opacity(0.15))
                        Image(systemName: toolGlyph(for: activity.name))
                            .font(.caption2)
                            .foregroundStyle(toolStatusColor(activity.status))
                    }
                    .frame(width: 22, height: 22)

                    VStack(alignment: .leading, spacing: 2) {
                        Text(activity.name)
                            .font(.caption.weight(.semibold))
                        Text(activity.summary)
                            .font(.caption2)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }

                    Spacer()
                    statusBadge
                    Image(systemName: "chevron.right")
                        .font(.system(size: 9, weight: .bold))
                        .foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(isExpanded ? 90 : 0))
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .padding(.vertical, 7)

            if isExpanded {
                ToolActivityDetail(activity: activity)
                    .padding(.leading, 32)
                    .padding(.bottom, 10)
                    .transition(.opacity)
            }
        }
    }

    @ViewBuilder
    private var statusBadge: some View {
        HStack(spacing: 5) {
            if activity.status == .running || activity.status == .awaitingApproval {
                ProgressView().controlSize(.mini)
            } else {
                Image(systemName: toolStatusIcon(activity.status))
                    .font(.caption2)
                    .foregroundStyle(toolStatusColor(activity.status))
            }
            Text(toolStatusLabel(activity.status))
                .font(.caption2)
                .foregroundStyle(.secondary)
            if let duration = activity.duration {
                Text(String(format: "%.1fs", duration))
                    .font(.caption2.monospacedDigit())
                    .foregroundStyle(.secondary)
            }
        }
    }
}

private struct ToolActivityDetail: View {
    let activity: ToolActivity

    private static let displayLimit = 4000

    private var commandLines: [String] {
        guard let data = activity.arguments.data(using: .utf8),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              !object.isEmpty else { return [] }
        return object.sorted { $0.key < $1.key }.map { "\($0.key): \(Self.stringify($0.value))" }
    }

    private static let argumentValueLimit = 500

    private static func stringify(_ value: Any) -> String {
        let string: String
        if let raw = value as? String {
            string = raw
        } else if JSONSerialization.isValidJSONObject(value), let data = try? JSONSerialization.data(withJSONObject: value, options: [.sortedKeys]) {
            string = String(data: data, encoding: .utf8) ?? "\(value)"
        } else {
            string = "\(value)"
        }
        guard string.count > argumentValueLimit else { return string }
        return String(string.prefix(argumentValueLimit)) + "…"
    }

    private var webSources: [WebSearchSource]? {
        guard activity.name == "web_search", let detail = activity.detail else { return nil }
        let sources = WebSearchSource.parse(detail)
        return sources.isEmpty ? nil : sources
    }

    private var resultText: String? {
        guard let detail = activity.detail else { return nil }
        guard detail.count > Self.displayLimit else { return detail }
        return String(detail.prefix(Self.displayLimit)) + "\n\n[truncated for display — the full result was sent to the model]"
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            if !commandLines.isEmpty {
                detailSection(title: "Command", icon: "chevron.left.forwardslash.chevron.right") {
                    VStack(alignment: .leading, spacing: 3) {
                        ForEach(commandLines, id: \.self) { line in
                            Text(line)
                                .font(.caption2.monospaced())
                                .textSelection(.enabled)
                        }
                    }
                }
            }

            if let webSources {
                detailSection(title: "\(webSources.count) web source\(webSources.count == 1 ? "" : "s")", icon: "globe") {
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(webSources) { source in
                            WebSourceRow(source: source)
                        }
                    }
                }
            } else if let resultText {
                detailSection(
                    title: activity.status == .failed ? "Error" : "Result",
                    icon: activity.status == .failed ? "exclamationmark.triangle.fill" : "doc.plaintext"
                ) {
                    Text(resultText)
                        .font(.caption2.monospaced())
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
            } else if activity.status == .running || activity.status == .requested || activity.status == .awaitingApproval {
                Text("No result yet…")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        }
    }

    @ViewBuilder
    private func detailSection<Content: View>(title: String, icon: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            Label(title, systemImage: icon)
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.secondary)
            content()
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(8)
                .background(.quaternary.opacity(0.5), in: RoundedRectangle(cornerRadius: 7))
        }
    }
}

/// Parsed from `WebToolRuntime.search`'s `"N. Title\n   url\n   snippet"`
/// text blocks so results can render as cards instead of raw text.
private struct WebSearchSource: Identifiable {
    let id = UUID()
    let title: String
    let url: String

    var host: String {
        URL(string: url)?.host?.replacingOccurrences(of: "www.", with: "") ?? url
    }

    static func parse(_ content: String) -> [WebSearchSource] {
        content
            .components(separatedBy: "\n\n")
            .compactMap { block -> WebSearchSource? in
                let lines = block.components(separatedBy: "\n").map { $0.trimmingCharacters(in: .whitespaces) }
                guard lines.count >= 2,
                      let dotRange = lines[0].range(of: ". "),
                      Int(lines[0][lines[0].startIndex..<dotRange.lowerBound]) != nil,
                      lines[1].hasPrefix("http")
                else { return nil }
                return WebSearchSource(title: String(lines[0][dotRange.upperBound...]), url: lines[1])
            }
    }
}

private struct WebSourceRow: View {
    let source: WebSearchSource

    var body: some View {
        Button {
            guard let url = URL(string: source.url) else { return }
            NSWorkspace.shared.open(url)
        } label: {
            HStack(spacing: 8) {
                SourceGlyph(host: source.host)
                VStack(alignment: .leading, spacing: 1) {
                    Text(source.title)
                        .font(.caption.weight(.medium))
                        .foregroundStyle(.primary)
                        .lineLimit(1)
                    Text(source.host)
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }
                Spacer()
                Image(systemName: "arrow.up.right")
                    .font(.caption2)
                    .foregroundStyle(.tertiary)
            }
            .padding(7)
            .background(.quaternary.opacity(0.35), in: RoundedRectangle(cornerRadius: 7))
        }
        .buttonStyle(.plain)
    }
}

/// A deterministic, per-domain colored stand-in for a favicon — fetching
/// real favicons would mean telling a third party (e.g. Google's favicon
/// service) which domains every search turned up, which this app otherwise
/// never does on the user's behalf.
private struct SourceGlyph: View {
    let host: String

    private var tint: Color {
        var hasher = Hasher()
        hasher.combine(host)
        let hue = Double(abs(hasher.finalize()) % 360) / 360
        return Color(hue: hue, saturation: 0.5, brightness: 0.85)
    }

    var body: some View {
        Circle()
            .fill(tint.opacity(0.22))
            .overlay(
                Image(systemName: "globe")
                    .font(.system(size: 9, weight: .semibold))
                    .foregroundStyle(tint)
            )
            .frame(width: 18, height: 18)
    }
}

private func toolGlyph(for name: String) -> String {
    switch name {
    case "web_search": "magnifyingglass"
    case "web_fetch": "globe"
    case "run_command": "terminal.fill"
    case "read_file", "write_file", "edit_file": "doc.text.fill"
    case "list_dir", "glob", "grep": "folder.fill"
    case "ask_user": "questionmark.bubble.fill"
    case "local_context": "info.circle.fill"
    default: "wrench.and.screwdriver.fill"
    }
}

private func toolStatusIcon(_ status: ToolExecutionStatus) -> String {
    switch status {
    case .requested: "circle.dotted"
    case .awaitingApproval: "exclamationmark.shield.fill"
    case .running: "arrow.triangle.2.circlepath"
    case .succeeded: "checkmark.circle.fill"
    case .failed: "xmark.circle.fill"
    case .rejected: "hand.raised.fill"
    }
}

private func toolStatusColor(_ status: ToolExecutionStatus) -> Color {
    switch status {
    case .requested: .secondary
    case .awaitingApproval: .orange
    case .running: .blue
    case .succeeded: .green
    case .failed: .red
    case .rejected: .orange
    }
}

private func toolStatusLabel(_ status: ToolExecutionStatus) -> String {
    switch status {
    case .requested: "Requested"
    case .awaitingApproval: "Approval needed"
    case .running: "Running"
    case .succeeded: "Completed"
    case .failed: "Failed"
    case .rejected: "Rejected"
    }
}

struct ResponseMetricsView: View {
    let metrics: ChatMetrics

    var body: some View {
        Text("Completed in \(formattedDuration) · ~\(metrics.outputTokens) tokens · \(rateText) tok/s")
            .font(.caption2.monospacedDigit())
            .foregroundStyle(.secondary)
            .padding(.top, 2)
    }

    private var formattedDuration: String {
        metrics.duration < 60
            ? String(format: "%.1fs", metrics.duration)
            : String(format: "%.0fm %.0fs", metrics.duration / 60, metrics.duration.truncatingRemainder(dividingBy: 60))
    }

    private var rateText: String {
        String(format: "%.1f", metrics.tokensPerSecond)
    }
}

struct ProviderSettingsView: View {
    let model: AppModel

    var body: some View {
        @Bindable var model = model

        Form {
            Section("Active provider") {
                if !model.configuration.providers.isEmpty {
                    Picker("Configured profile", selection: Binding(
                        get: { model.configuration.provider.name },
                        set: { model.selectProvider($0) }
                    )) {
                        ForEach(model.configuration.providers) { profile in
                            Text(profile.name).tag(profile.name)
                        }
                    }
                }
                TextField("Active profile", text: Binding(
                    get: { model.configuration.provider.name },
                    set: { model.renameActiveProvider(to: $0) }
                ))
                    .help("The active profile is selected by default_provider in ~/.config/llmtui/config.yaml.")
                Picker("Type", selection: $model.configuration.provider.type) {
                    ForEach(ProviderType.allCases) { type in
                        Text(type.title).tag(type)
                    }
                }
                TextField("Base URL", text: $model.configuration.provider.baseURL)
                TextField("Model", text: $model.configuration.provider.model)
                ProviderModelDiscoveryRow(model: model)
                TextField("API key environment variable", text: $model.configuration.provider.apiKeyEnvironment)
                    .help("Store the secret in the environment; the app never writes the API key to YAML.")
                Button("Add provider profile", systemImage: "plus") {
                    model.createProviderProfile()
                }
                Text("Profiles are stored under providers: in the same llmtui configuration file. Saving keeps every other provider and section intact.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            if model.configuration.provider.type == .embedded {
                EmbeddedProviderFieldsView(model: model, providerName: model.configuration.provider.name)
            }

            Section {
                HStack {
                    Text(model.configFilePath)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                    Spacer()
                    Button("Reload") {
                        model.loadConfiguration()
                    }
                    Button("Save") {
                        model.saveConfiguration()
                    }
                    .buttonStyle(.borderedProminent)
                }
            }
        }
        .formStyle(.grouped)
        .padding(24)
        .navigationTitle("Providers")
    }
}

/// Asks the active provider for its real model list instead of the user
/// typing a name by hand — reliable on Ollama and any OpenAI-compatible
/// server (LM Studio included); disabled for embedded/mock, which have no
/// server to ask.
struct ProviderModelDiscoveryRow: View {
    let model: AppModel

    var body: some View {
        HStack(spacing: 10) {
            Button {
                model.discoverModels()
            } label: {
                if model.isDiscoveringModels {
                    ProgressView()
                        .controlSize(.small)
                } else {
                    Label("Discover Models", systemImage: "arrow.triangle.2.circlepath")
                }
            }
            .disabled(!model.configuration.provider.type.supportsModelDiscovery || model.isDiscoveringModels)
            .help(model.configuration.provider.type.supportsModelDiscovery
                ? "Fetch the real list of models this provider currently offers"
                : "Model discovery isn't available for this provider type")

            if !model.discoveredModels.isEmpty {
                // The picker shows a blank label whenever the bound value
                // doesn't exactly match one of its options' tags — which is
                // exactly the state right after a fetch if the typed-in
                // model name doesn't precisely match what discovery found.
                // Including the current value as an option guarantees a
                // match, so the label always shows something.
                Picker("", selection: Binding(
                    get: { model.configuration.provider.model },
                    set: { model.configuration.provider.model = $0 }
                )) {
                    ForEach(pickerOptions, id: \.self) { name in
                        Text(name).tag(name)
                    }
                }
                .pickerStyle(.menu)
                .labelsHidden()
                .help("Pick a model this provider actually has, instead of typing the name above")
            }
        }
    }

    private var pickerOptions: [String] {
        let current = model.configuration.provider.model
        guard !current.isEmpty, !model.discoveredModels.contains(current) else {
            return model.discoveredModels
        }
        return [current] + model.discoveredModels
    }
}

struct ChatSettingsView: View {
    let model: AppModel

    var body: some View {
        @Bindable var model = model

        Form {
            Section("Instructions") {
                TextField("System prompt", text: $model.configuration.systemPrompt, axis: .vertical)
                    .lineLimit(3...8)
            }

            Section("Generation") {
                LabeledContent {
                    Slider(value: $model.configuration.temperature, in: 0...2, step: 0.05) {
                        Text(model.configuration.temperature, format: .number.precision(.fractionLength(2)))
                    }
                } label: {
                    HStack(spacing: 5) {
                        Text("Temperature")
                        InfoButton(text: "Controls randomness. Lower values are more deterministic; higher values produce more varied responses. llmtui writes this as chat.temperature.")
                    }
                }
                LabeledContent {
                    Slider(value: $model.configuration.topP, in: 0...1, step: 0.05) {
                        Text(model.configuration.topP, format: .number.precision(.fractionLength(2)))
                    }
                } label: {
                    HStack(spacing: 5) {
                        Text("Top P")
                        InfoButton(text: "Limits token sampling to the most likely cumulative probability mass. It is sent as chat.top_p.")
                    }
                }
                HStack {
                    Stepper("Maximum tokens: \(model.configuration.maxTokens)", value: $model.configuration.maxTokens, in: 256...32768, step: 256)
                    InfoButton(text: "Maximum number of output tokens requested from the provider. This is chat.max_tokens.")
                }
                Toggle(isOn: $model.configuration.stream) {
                    HStack(spacing: 5) {
                        Text("Stream responses")
                        InfoButton(text: "Displays generated tokens as they arrive instead of waiting for the complete response. This is chat.stream.")
                    }
                }
                Picker(selection: $model.configuration.reasoning) {
                    ForEach(ReasoningMode.allCases) { mode in
                        Text(mode.title).tag(mode)
                    }
                } label: {
                    HStack(spacing: 5) {
                        Text("Reasoning")
                        InfoButton(text: "Controls whether the provider is asked to expose or suppress reasoning, when supported by the model.")
                    }
                }
            }

            Section("History & vision") {
                Toggle(isOn: Binding(
                    get: { model.configuration.rawSettings["chat.save_history"] != "false" },
                    set: { model.configuration.rawSettings["chat.save_history"] = $0 ? "true" : "false" }
                )) {
                    HStack(spacing: 5) {
                        Text("Save chat history")
                        InfoButton(text: "Enables persistent conversation history in llmtui. The setting is chat.save_history.")
                    }
                }
                HStack {
                    TextField("History folder", text: Binding(
                    get: { model.configuration.rawSettings["chat.history_dir"] ?? "~/.local/share/llmtui/history" },
                    set: { model.configuration.rawSettings["chat.history_dir"] = $0 }
                    ))
                    InfoButton(text: "Directory where llmtui stores chat history. The setting is chat.history_dir.")
                }
                Toggle(isOn: Binding(
                    get: { model.configuration.rawSettings["chat.force_vision"] == "true" },
                    set: { model.configuration.rawSettings["chat.force_vision"] = $0 ? "true" : "false" }
                )) {
                    HStack(spacing: 5) {
                        Text("Force vision for unrecognized models")
                        InfoButton(text: "Allows image/vision handling even when a model profile does not explicitly declare vision support.")
                    }
                }
                Toggle(isOn: Binding(
                    get: { model.configuration.rawSettings["chat.strip_leaked_thinking"] != "false" },
                    set: { model.configuration.rawSettings["chat.strip_leaked_thinking"] = $0 ? "true" : "false" }
                )) {
                    HStack(spacing: 5) {
                        Text("Strip leaked thinking blocks")
                        InfoButton(text: "Removes accidentally exposed reasoning markers from the displayed answer when a provider leaks them into normal output.")
                    }
                }
            }

            SettingsSaveRow(model: model)
        }
        .formStyle(.grouped)
        .padding(24)
        .navigationTitle("Chat Settings")
    }
}

struct ToolsSettingsView: View {
    let model: AppModel

    var body: some View {
        @Bindable var model = model

        Form {
            Section("Workspace tools") {
                Toggle(isOn: $model.configuration.toolsEnabled) {
                    HStack(spacing: 5) {
                        Text("Enable tools")
                        InfoButton(text: "Enables llmtui workspace tools. Tool schemas are then available to the model, subject to the approval policy and guardrails.")
                    }
                }
                Picker(selection: $model.configuration.approvalPolicy) {
                    ForEach(ApprovalPolicy.allCases) { policy in
                        Text(policy.title).tag(policy)
                    }
                } label: {
                    HStack(spacing: 5) {
                        Text("Approval policy")
                        InfoButton(text: "Controls whether tool calls require approval. Always ask is the safer default; Automatic allows configured tool calls without a prompt.")
                    }
                }
                Text(model.configuration.approvalPolicy.detail)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                HStack {
                    Stepper("Maximum tool rounds: \(model.configuration.maxToolIterations)", value: $model.configuration.maxToolIterations, in: 1...50)
                    InfoButton(text: "Maximum tool-call rounds allowed for one chat message. This is tools.max_iterations.")
                }
                TextField("Chat tool workspace", text: $model.configuration.toolWorkspacePath)
                    .help("Native Chat tools can only access this directory and its descendants.")
                Text("Available to Chat: \(ToolRegistry.shared.definitions.map { $0.function.name }.joined(separator: ", "))")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("Safety model") {
                Label("Tool calls are displayed in the conversation before they execute.", systemImage: "checkmark.shield")
                Label("API keys are referenced through environment variables.", systemImage: "key")
                Label("The app does not enable tools automatically.", systemImage: "lock")
            }

            SettingsSaveRow(model: model)
        }
        .formStyle(.grouped)
        .padding(24)
        .navigationTitle("Tools & Safety")
    }
}


struct AgentRuntimeSettingsView: View {
    let model: AppModel

    var body: some View {
        @Bindable var model = model

        Form {
            Section("Entities") {
                Toggle(isOn: $model.configuration.entities.enabled) {
                    HStack(spacing: 5) {
                        Text("Enable session entities")
                        InfoButton(text: "Enables llmtui's entity extraction and session context expansion. Entity limits below bound memory and prompt growth.")
                    }
                }
                Stepper("Maximum session entities: \(model.configuration.entities.maxSessionEntities)", value: $model.configuration.entities.maxSessionEntities, in: 1...4096)
                Stepper("Maximum context tokens: \(model.configuration.entities.maxContextTokens)", value: $model.configuration.entities.maxContextTokens, in: 128...16384, step: 128)
                Stepper("Maximum full expansions: \(model.configuration.entities.maxFullExpansions)", value: $model.configuration.entities.maxFullExpansions, in: 0...64)
                Toggle("Vision observations", isOn: $model.configuration.entities.visionEnabled)
                Stepper("Vision token budget: \(model.configuration.entities.visionMaxTokens)", value: $model.configuration.entities.visionMaxTokens, in: 128...4096, step: 128)
                Picker("Output storage", selection: $model.configuration.entities.outputStorage) {
                    ForEach(EntityOutputStorage.allCases) { storage in
                        Text(storage.title).tag(storage)
                    }
                }
                TextField("Disk storage path", text: $model.configuration.entities.outputStoragePath)
            }

            Section("Go LLMTUI agent mode") {
                Toggle(isOn: $model.configuration.agent.enabled) {
                    HStack(spacing: 5) {
                        Text("Enable Go LLMTUI agent runs")
                        InfoButton(text: "Writes agent.enabled to config.yaml for the Go LLMTUI application. It does not enable the native GUI chat agent; use the infinity button beside the chat composer for that.")
                    }
                }
                Stepper("Maximum cycles: \(model.configuration.agent.maxCycles)", value: $model.configuration.agent.maxCycles, in: 1...64)
                Stepper("Maximum tool calls: \(model.configuration.agent.maxToolCalls)", value: $model.configuration.agent.maxToolCalls, in: 1...256)
                Stepper("Token budget: \(model.configuration.agent.maxTokens)", value: $model.configuration.agent.maxTokens, in: 1024...1_000_000, step: 1024)
                TextField("Elapsed-time limit", text: $model.configuration.agent.maxElapsed)
                Stepper("Repeated failures before stopping: \(model.configuration.agent.maxRepeatedFailures)", value: $model.configuration.agent.maxRepeatedFailures, in: 1...10)
                Toggle("Persist resumable runs", isOn: $model.configuration.agent.persist)
                Toggle("Enforce budgets live", isOn: $model.configuration.agent.enforceBudgetsLive)
            }

            Section("Verifier") {
                Toggle(isOn: $model.configuration.agent.verifier.enabled) {
                    HStack(spacing: 5) {
                        Text("Verifier enabled")
                        InfoButton(text: "Allows the agent loop to use a fresh-context verifier before deciding whether work is complete.")
                    }
                }
                Picker(selection: $model.configuration.agent.verifier.mode) {
                    ForEach(VerificationMode.allCases) { mode in
                        Text(mode.title).tag(mode)
                    }
                } label: {
                    HStack(spacing: 5) {
                        Text("Verification mode")
                        InfoButton(text: "Controls when the verifier runs: off, deterministic, adaptive, or always. The exact behavior follows llmtui's agent verifier settings.")
                    }
                }
                TextField("Verifier model (optional)", text: $model.configuration.agent.verifier.model)
                Stepper("Verifier tokens: \(model.configuration.agent.verifier.maxTokens)", value: $model.configuration.agent.verifier.maxTokens, in: 256...16384, step: 256)
                TextField("Verifier timeout", text: $model.configuration.agent.verifier.timeout)
                Stepper("Verifier attempts: \(model.configuration.agent.verifier.maxAttempts)", value: $model.configuration.agent.verifier.maxAttempts, in: 1...5)
            }

            Section("Yield continuation") {
                Toggle(isOn: $model.configuration.agent.yield.enabled) {
                    HStack(spacing: 5) {
                        Text("Allow same-episode yield")
                        InfoButton(text: "Allows a bounded follow-up request when an agent has not yet satisfied an exact-read criterion.")
                    }
                }
                Stepper("Maximum episode requests: \(model.configuration.agent.yield.maxEpisodeRequests)", value: $model.configuration.agent.yield.maxEpisodeRequests, in: 1...256)
                Stepper("Maximum no-progress nudges: \(model.configuration.agent.yield.maxNudgesWithoutProgress)", value: $model.configuration.agent.yield.maxNudgesWithoutProgress, in: 0...10)
                Text("Yield continuation lets an agent make a bounded follow-up request when an exact-read criterion is still incomplete.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            SettingsSaveRow(model: model)
        }
        .formStyle(.grouped)
        .padding(24)
        .navigationTitle("Agent & Runtime")
    }
}

struct EmbeddedProviderFieldsView: View {
    let model: AppModel
    let providerName: String
    @State private var draftStop = ""
    @State private var draftBreakers = ""

    private func path(_ suffix: String) -> String {
        "providers.\(providerName).\(suffix)"
    }

    var body: some View {
        Group {
            Section("Embedded model files") {
                EmbeddedField(model: model, path: path("model_path"), title: "Model path")
                    .help("Local GGUF file; required unless --model/LLMTUI_MODEL supplies it.")
                EmbeddedField(model: model, path: path("mmproj_path"), title: "Projector path (mmproj)")
                    .help("Optional multimodal projector GGUF; fixes this profile to one model/projector pair.")
                EmbeddedField(model: model, path: path("library_path"), title: "Library path")
                    .help("Advanced trusted override; omit to use bundled/managed runtime discovery.")
            }

            Section("Embedded performance") {
                EmbeddedField(model: model, path: path("context_size"), title: "Context size")
                    .help("Default: min(trained context, 8192).")
                EmbeddedField(model: model, path: path("gpu_layers"), title: "GPU layers")
                    .help("-1 offloads all layers, 0 is CPU-only, a positive value is an exact count. Default: -1.")
                EmbeddedField(model: model, path: path("threads"), title: "Threads")
                    .help("0 is automatic (one thread per performance core). Default: 0.")
                EmbeddedField(model: model, path: path("threads_batch"), title: "Batch threads")
                    .help("Optional prompt/batch thread count; omit to reuse Threads.")
                EmbeddedField(model: model, path: path("batch_size"), title: "Batch size")
                    .help("Prompt-decode batch size, capped by context size. Default: 512.")
                EmbeddedField(model: model, path: path("ubatch_size"), title: "Micro-batch size")
                    .help("Optional physical micro-batch size; must not exceed Batch size.")
                EmbeddedField(model: model, path: path("swa_full"), title: "Full-size SWA cache")
                    .help("True restores llama.cpp's full-size sliding-window-attention KV cache. Default: false.")
                EmbeddedPicker(model: model, path: path("tool_format"), title: "Tool format",
                               options: ["auto", "standard", "qwen", "glm", "mistral", "gemma", "gpt", "phi"])
                    .help("Native tool-call grammar. Default: auto.")
                EmbeddedField(model: model, path: path("chat_template"), title: "Chat template override")
                    .help("Inline Jinja chat template text, not a filename. Leave blank to use model metadata.")
            }

            Section("KV cache") {
                EmbeddedPicker(model: model, path: path("kv_cache_type"), title: "KV cache type", options: ["f16", "q8_0", "q4_0"])
                    .help("Applies to both K and V when the per-side overrides below are unset. Default: f16.")
                EmbeddedPicker(model: model, path: path("kv_cache.type_k"), title: "Key cache type", options: ["f16", "q8_0", "q4_0"])
                EmbeddedPicker(model: model, path: path("kv_cache.type_v"), title: "Value cache type", options: ["f16", "q8_0", "q4_0"])
                    .help("Quantized values require Flash attention not to be Off.")
                EmbeddedPicker(model: model, path: path("kv_cache.offload"), title: "KV GPU offload", options: ["true", "false"])
                    .help("Optional true/false override; Default leaves llama.cpp's native behavior.")
                EmbeddedPicker(model: model, path: path("flash_attention"), title: "Flash attention", options: ["auto", "on", "off"])
                    .help("Default: auto (llama.cpp decides per model/backend).")
            }

            Section("RoPE / YaRN") {
                DisclosureGroup("Advanced context-extension overrides") {
                    EmbeddedPicker(model: model, path: path("rope_scaling_type"), title: "RoPE scaling type", options: ["none", "linear", "yarn", "longrope"])
                    EmbeddedField(model: model, path: path("rope_freq_base"), title: "RoPE frequency base")
                    EmbeddedField(model: model, path: path("rope_freq_scale"), title: "RoPE frequency scale")
                    EmbeddedField(model: model, path: path("yarn_ext_factor"), title: "YaRN extrapolation factor")
                    EmbeddedField(model: model, path: path("yarn_attn_factor"), title: "YaRN attention factor")
                    EmbeddedField(model: model, path: path("yarn_beta_fast"), title: "YaRN beta fast")
                    EmbeddedField(model: model, path: path("yarn_beta_slow"), title: "YaRN beta slow")
                    EmbeddedField(model: model, path: path("yarn_orig_ctx"), title: "YaRN original context")
                }
                Text("Leave unset when the requested context fits the GGUF's trained window; incorrect values can sharply reduce quality even when the model loads.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("Reasoning & speculative") {
                EmbeddedPicker(model: model, path: path("reasoning.effort"), title: "Reasoning effort", options: ["auto", "low", "medium", "high", "xhigh"])
                EmbeddedField(model: model, path: path("reasoning.preserve"), title: "Preserve reasoning")
                    .help("Only compatible GGUF Jinja templates consume this. Default: false.")
                EmbeddedPicker(model: model, path: path("speculative.type"), title: "Speculative decoding", options: ["off", "draft-mtp"])
                    .help("draft-mtp currently fails before model initialization with an actionable error.")
            }

            Section("Sampling") {
                EmbeddedField(model: model, path: path("sampling.top_k"), title: "Top-k").help("Default: 40.")
                EmbeddedField(model: model, path: path("sampling.min_p"), title: "Min-p").help("Default: 0.05.")
                EmbeddedField(model: model, path: path("sampling.repeat_penalty"), title: "Repeat penalty").help("Default: 1.1.")
                EmbeddedField(model: model, path: path("sampling.repeat_last_n"), title: "Repeat last N").help("Default: 64.")
                EmbeddedField(model: model, path: path("sampling.presence_penalty"), title: "Presence penalty").help("Default: 0.0.")
                EmbeddedField(model: model, path: path("sampling.dry_multiplier"), title: "DRY multiplier").help("Default: 0.0 (disabled).")
                EmbeddedField(model: model, path: path("sampling.dry_base"), title: "DRY base").help("Default: 1.75.")
                EmbeddedField(model: model, path: path("sampling.dry_allowed_length"), title: "DRY allowed length").help("Default: 2.")
                EmbeddedField(model: model, path: path("sampling.dry_penalty_last_n"), title: "DRY penalty last N").help("Default: -1 (active context size).")
                EmbeddedField(model: model, path: path("sampling.seed"), title: "Seed").help("0 selects a random seed.")
                ManualIDListEditor(model: model, path: path("sampling.stop"), title: "Stop strings", draft: $draftStop)
                ManualIDListEditor(model: model, path: path("sampling.dry_sequence_breakers"), title: "DRY sequence breakers", draft: $draftBreakers)
            }

            Section("About embedded fields") {
                Text("These map directly to llmtui's embedded-provider schema (docs/embedded.md). A field stays out of the YAML until you set it; clearing it removes the key entirely rather than writing an empty value.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

struct EmbeddedField: View {
    let model: AppModel
    let path: String
    let title: String

    var body: some View {
        HStack {
            SettingEditor(model: model, path: path, title: title)
            if model.configuration.rawSettings[path] != nil {
                Button("Clear", systemImage: "xmark.circle") {
                    model.configuration.rawSettings.removeValue(forKey: path)
                }
                .labelStyle(.iconOnly)
                .buttonStyle(.borderless)
                .foregroundStyle(.secondary)
            }
        }
    }
}

struct EmbeddedPicker: View {
    let model: AppModel
    let path: String
    let title: String
    let options: [String]

    var body: some View {
        Picker(title, selection: Binding(
            get: { model.configuration.rawSettings[path] ?? "" },
            set: { newValue in
                if newValue.isEmpty {
                    model.configuration.rawSettings.removeValue(forKey: path)
                } else {
                    model.configuration.rawSettings[path] = newValue
                }
            }
        )) {
            Text("Default").tag("")
            ForEach(options, id: \.self) { option in
                Text(option).tag(option)
            }
        }
    }
}

struct ModelProfilesView: View {
    let model: AppModel
    @State private var newMatchByProfile: [String: String] = [:]
    @State private var isAddingProfile = false

    /// Prefer the provider's canonical model ID over a shorter profile alias.
    /// For example, LM Studio reports "google/gemma-4-e4b" while a profile may
    /// also contain the shorthand "gemma-4-e4b".
    private func exactMatchModel(for profileName: String) -> String? {
        let matches = model.configuration.listSettings[
            "model_profiles.\(profileName).match",
            default: []
        ].filter { !$0.contains("*") && !$0.isEmpty }

        if matches.contains(model.configuration.provider.model) {
            return model.configuration.provider.model
        }
        if let discovered = model.discoveredModels.first(where: matches.contains) {
            return discovered
        }
        return matches.max { left, right in
            let leftIsCanonical = left.contains("/")
            let rightIsCanonical = right.contains("/")
            if leftIsCanonical != rightIsCanonical {
                return !leftIsCanonical && rightIsCanonical
            }
            return left.count < right.count
        }
    }

    private var profileNames: [String] {
        Set(model.configuration.rawSettings.keys.compactMap(LLMTUIConfiguration.modelProfileName(fromKey:))).sorted()
    }

    var body: some View {
        Form {
            Section("Configured model profiles") {
                Button("Add model profile", systemImage: "plus") {
                    isAddingProfile = true
                }
                if profileNames.isEmpty {
                    Text("No model profiles are configured.")
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(profileNames, id: \.self) { name in
                        VStack(alignment: .leading, spacing: 8) {
                            HStack {
                                Image(systemName: "square.stack.3d.up")
                                    .foregroundStyle(.secondary)
                                ModelProfileNameField(model: model, name: name)
                                Spacer()
                                Button("Remove", systemImage: "trash") {
                                    model.removeModelProfile(name)
                                }
                                .labelStyle(.iconOnly)
                                .buttonStyle(.borderless)
                                .foregroundStyle(.red)
                            }
                            MatchListEditor(model: model, profileName: name, draft: Binding(
                                get: { newMatchByProfile[name, default: ""] },
                                set: { newMatchByProfile[name] = $0 }
                            ))
                            HStack {
                                SettingEditor(model: model, path: "model_profiles.\(name).context_window", title: "Context window")
                                if let exactModel = exactMatchModel(for: name) {
                                    Button {
                                        model.readModelProfileSettings(profileName: name, model: exactModel)
                                    } label: {
                                        if model.profileSettingsBeingRead == name {
                                            ProgressView().controlSize(.small)
                                        } else {
                                            Label("Read settings from provider", systemImage: "arrow.down.circle")
                                        }
                                    }
                                    .buttonStyle(.bordered)
                                    .controlSize(.small)
                                    .disabled(model.profileSettingsBeingRead != nil)
                                    .help("Read profile settings and model capabilities exposed by \(model.configuration.provider.name)")
                                }
                            }
                            if let metadata = model.discoveredProfileMetadata[name] {
                                ProviderModelMetadataView(metadata: metadata)
                            }
                            SettingEditor(model: model, path: "model_profiles.\(name).preferred_temperature", title: "Preferred temperature")
                            SettingEditor(model: model, path: "model_profiles.\(name).supports_json_mode", title: "Supports JSON mode")
                            SettingEditor(model: model, path: "model_profiles.\(name).prompt_style", title: "Prompt style")
                            SettingEditor(model: model, path: "model_profiles.\(name).reasoning_hint", title: "Reasoning hint")
                        }
                        .padding(.vertical, 6)
                    }
                }
            }
            Section("How profiles work") {
                Text("llmtui matches profiles by model-ID substring before applying these preferences. The profile name is the YAML map key — edit it and press Return to rename. Match lists are preserved exactly from YAML; the scalar settings above are editable here.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            SettingsSaveRow(model: model)
        }
        .formStyle(.grouped)
        .padding(24)
        .navigationTitle("Model Profiles")
        .sheet(isPresented: $isAddingProfile) {
            NewModelProfileSheet(model: model)
        }
    }
}

private struct ProviderModelMetadataView: View {
    let metadata: ProviderModelMetadata

    private var details: [String] {
        var values: [String] = []
        if let loadedInstance = metadata.loadedInstance {
            values.append("Loaded: \(loadedInstance)")
        }
        if let maximum = metadata.maximumContextWindow {
            values.append("Maximum context: \(maximum.formatted())")
        }
        if let architecture = metadata.architecture {
            values.append("Architecture: \(architecture)")
        }
        if let quantization = metadata.quantization {
            values.append("Quantization: \(quantization)")
        }
        if let parameterCount = metadata.parameterCount {
            values.append("Parameters: \(parameterCount)")
        }
        return values
    }

    private var capabilities: [String] {
        var values: [String] = []
        if metadata.supportsVision == true {
            values.append("Vision")
        }
        if metadata.trainedForToolUse == true {
            values.append("Tool use")
        }
        if metadata.supportsReasoning == true {
            let suffix = metadata.defaultReasoningEnabled == true ? " (default on)" : ""
            values.append("Reasoning\(suffix)")
        }
        return values
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            if !details.isEmpty {
                Text(details.joined(separator: " • "))
            }
            if !capabilities.isEmpty {
                Text("Capabilities: \(capabilities.joined(separator: ", "))")
            }
        }
        .font(.caption)
        .foregroundStyle(.secondary)
        .textSelection(.enabled)
    }
}

/// A guided "wizard" for creating a model profile fully configured up
/// front, instead of dropping a bare default into an alphabetically-sorted
/// list that the user then has to scroll to find and edit.
struct NewModelProfileSheet: View {
    let model: AppModel
    @Environment(\.dismiss) private var dismiss

    @State private var name = ""
    @State private var matches: [String] = []
    @State private var matchDraft = ""
    @State private var contextWindow = "32768"
    @State private var preferredTemperature = "0.7"
    @State private var supportsJSONMode = true
    @State private var promptStyle = "direct"
    @State private var reasoningHint = false
    @State private var errorMessage: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text("New Model Profile")
                .font(.title3.weight(.semibold))
                .padding([.top, .horizontal], 20)
                .padding(.bottom, 12)

            Form {
                Section("Name") {
                    TextField("Profile name", text: $name)
                        .help("The YAML map key under model_profiles: — no \".\" characters.")
                }
                Section("Model ID matches") {
                    ForEach(matches, id: \.self) { match in
                        HStack {
                            Text(match)
                            Spacer()
                            Button(role: .destructive) {
                                matches.removeAll { $0 == match }
                            } label: {
                                Image(systemName: "minus.circle")
                            }
                            .buttonStyle(.plain)
                        }
                    }
                    HStack {
                        TextField("Add a match", text: $matchDraft)
                        if !model.discoveredModels.isEmpty {
                            Menu {
                                ForEach(model.discoveredModels, id: \.self) { discovered in
                                    Button(discovered) { matches.append(discovered) }
                                }
                            } label: {
                                Image(systemName: "list.bullet.circle")
                            }
                            .menuStyle(.borderlessButton)
                            .frame(width: 24)
                            .help("Add an exact model name discovered from the active provider")
                        }
                        Button("Add") {
                            let trimmed = matchDraft.trimmingCharacters(in: .whitespacesAndNewlines)
                            guard !trimmed.isEmpty else { return }
                            matches.append(trimmed)
                            matchDraft = ""
                        }
                        .disabled(matchDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                    }
                }
                Section("Settings") {
                    TextField("Context window", text: $contextWindow)
                    TextField("Preferred temperature", text: $preferredTemperature)
                    Toggle("Supports JSON mode", isOn: $supportsJSONMode)
                    TextField("Prompt style", text: $promptStyle)
                    Toggle("Reasoning hint", isOn: $reasoningHint)
                }
            }
            .formStyle(.grouped)

            if let errorMessage {
                Text(errorMessage)
                    .font(.caption)
                    .foregroundStyle(.red)
                    .padding(.horizontal, 20)
                    .padding(.top, 8)
            }

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                Button("Create") { createProfile() }
                    .buttonStyle(.borderedProminent)
                    .disabled(name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
            .padding(20)
        }
        .frame(width: 520, height: 560)
    }

    private func createProfile() {
        let trimmed = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.contains(".") else {
            errorMessage = "Profile names can't contain \".\" — it's used internally as a path separator."
            return
        }
        let created = model.createModelProfile(
            name: trimmed,
            matches: matches,
            contextWindow: contextWindow,
            preferredTemperature: preferredTemperature,
            supportsJSONMode: supportsJSONMode,
            promptStyle: promptStyle,
            reasoningHint: reasoningHint
        )
        guard created else {
            errorMessage = "A profile named \"\(trimmed)\" already exists."
            return
        }
        dismiss()
    }
}

/// Editable profile name. The profile list is keyed by name, so renaming on
/// every keystroke would recreate the row and drop keyboard focus; instead
/// the edit is buffered locally and committed on Return or when focus leaves.
/// A rejected rename (empty, duplicate, or containing ".") snaps back to the
/// current name.
struct ModelProfileNameField: View {
    let model: AppModel
    let name: String
    @State private var draft: String
    @FocusState private var isFocused: Bool

    init(model: AppModel, name: String) {
        self.model = model
        self.name = name
        _draft = State(initialValue: name)
    }

    var body: some View {
        TextField("Profile name", text: $draft)
            .textFieldStyle(.plain)
            .font(.headline)
            .focused($isFocused)
            .onSubmit(commit)
            .onChange(of: isFocused) { _, focused in
                if !focused { commit() }
            }
            .help("Used as the model_profiles key in config.yaml. Press Return to rename.")
    }

    private func commit() {
        guard draft != name else { return }
        if !model.renameModelProfile(name, to: draft) {
            draft = name
        }
    }
}

struct MatchListEditor: View {
    let model: AppModel
    let profileName: String
    @Binding var draft: String

    private var path: String { "model_profiles.\(profileName).match" }

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Model ID matches")
                .font(.subheadline.weight(.medium))
            ForEach(Array(model.configuration.listSettings[path, default: []].enumerated()), id: \.offset) { index, value in
                HStack {
                    TextField("Model ID substring", text: Binding(
                        get: { value },
                        set: { newValue in
                            var values = model.configuration.listSettings[path, default: []]
                            guard values.indices.contains(index) else { return }
                            values[index] = newValue
                            model.configuration.listSettings[path] = values
                        }
                    ))
                    Button("Remove match", systemImage: "minus.circle") {
                        var values = model.configuration.listSettings[path, default: []]
                        values.remove(at: index)
                        model.configuration.listSettings[path] = values
                    }
                    .labelStyle(.iconOnly)
                    .buttonStyle(.borderless)
                }
            }
            HStack {
                TextField("Add another match", text: $draft)
                if !model.discoveredModels.isEmpty {
                    Menu {
                        ForEach(model.discoveredModels, id: \.self) { name in
                            Button(name) {
                                model.configuration.listSettings[path, default: []].append(name)
                            }
                        }
                    } label: {
                        Image(systemName: "list.bullet.circle")
                    }
                    .menuStyle(.borderlessButton)
                    .frame(width: 24)
                    .help("Add an exact model name discovered from the active provider, instead of typing it")
                }
                Button("Add") {
                    let value = draft.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !value.isEmpty else { return }
                    model.configuration.listSettings[path, default: []].append(value)
                    draft = ""
                }
                .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
    }
}

struct PersonalAppsRuntimeControls: View {
    let model: AppModel

    var body: some View {
        Section("GUI chat runtime") {
            LabeledContent("Privacy state") {
                Label(
                    model.personalAppsRuntime.privateSession ? "Private conversation" : "No personal data loaded",
                    systemImage: model.personalAppsRuntime.privateSession ? "lock.shield.fill" : "lock.open"
                )
                .foregroundStyle(model.personalAppsRuntime.privateSession ? .orange : .secondary)
            }

            PersonalAppsAdapterConnectionRow(
                title: "Mail runtime",
                status: model.personalAppsRuntime.mailStatus,
                scopeCount: model.personalAppsRuntime.savedScope.mailAccountIDs.count,
                connect: model.connectGUIMail,
                disconnect: { model.disconnectGUIAdapter(.mail) }
            )

            PersonalAppsAdapterConnectionRow(
                title: "Calendar runtime",
                status: model.personalAppsRuntime.calendarStatus,
                scopeCount: model.personalAppsRuntime.savedScope.calendarIDs.count,
                connect: model.connectGUICalendar,
                disconnect: { model.disconnectGUIAdapter(.calendar) }
            )

            if PersonalAppsRuntime.isTrustedLocalEndpoint(model.configuration.provider.baseURL) {
                Label("The active endpoint is a direct local HTTP endpoint.", systemImage: "checkmark.shield")
                    .foregroundStyle(.green)
            } else if model.personalAppsRuntime.providerCanReceivePrivateData(model.configuration) {
                HStack {
                    Label("Private disclosure is allowed for this provider in this session.", systemImage: "exclamationmark.shield")
                        .foregroundStyle(.orange)
                    Spacer()
                    Button("Revoke") { model.revokePrivateDisclosure() }
                }
            } else {
                VStack(alignment: .leading, spacing: 8) {
                    Label("Private data is blocked from the active provider.", systemImage: "hand.raised.fill")
                        .foregroundStyle(.orange)
                    Text("LM Studio or Ollama names do not prove locality. Only a direct localhost/loopback HTTP endpoint is trusted automatically. Remote/custom endpoints require explicit per-session disclosure consent before Personal Apps tools are advertised.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                    Button("Allow private disclosure to \(model.configuration.provider.name) for this session") {
                        model.allowPrivateDisclosureForCurrentProvider()
                    }
                }
            }

            if model.personalAppsRuntime.savedScope.mutationsEnabled {
                Label(
                    "GUI mutations are enabled. Saving drafts, sending mail, and creating events always require a separate exact-plan approval and are recorded in the recovery journal.",
                    systemImage: "checkmark.shield"
                )
                .font(.caption)
                .foregroundStyle(.green)
            } else {
                Label("GUI Personal Apps access is read-only.", systemImage: "eye")
                    .foregroundStyle(.secondary)
            }

            Text(model.personalAppsRuntime.statusDetail)
                .font(.caption)
                .foregroundStyle(.secondary)

            Text("These controls affect only GUI chat. Connect is explicit and may prompt macOS for this app. The saved YAML allowlists are the maximum scope; unsaved edits do not broaden runtime access. The terminal Calendar helper path below is preserved and is not used by GUI chat.")
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }
}

struct PersonalAppsAdapterConnectionRow: View {
    let title: String
    let status: PersonalAppsConnectionStatus
    let scopeCount: Int
    let connect: () -> Void
    let disconnect: () -> Void

    var body: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(title)
                Text("\(scopeCount) saved ID\(scopeCount == 1 ? "" : "s") • \(status.rawValue.replacingOccurrences(of: "_", with: " ").capitalized)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            Spacer()
            if status == .connected || status == .connecting {
                Button("Disconnect", action: disconnect)
                    .disabled(status == .connecting)
            } else {
                Button("Connect", action: connect)
                    .disabled(scopeCount == 0)
            }
        }
    }
}

struct PersonalAppsSettingsView: View {
    let model: AppModel
    @State private var showCalendarResetConfirmation = false
    @State private var newMailAccountID = ""
    @State private var newCalendarID = ""

    var body: some View {
        Form {
            PersonalAppsRuntimeControls(model: model)

            Section("Terminal LLMTUI configuration") {
                Toggle("Enable Personal Apps integration", isOn: rawToggle("personal_apps.enabled", defaultValue: false))
                Text("Mail and Calendar access is macOS privacy-sensitive. Discovery is read-only; IDs are added to the YAML allowlist only when you explicitly save the configuration.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Section("Mail.app") {
                Toggle("Enable Mail access", isOn: rawToggle("personal_apps.mail.enabled", defaultValue: false))
                Button("Discover Mail accounts", systemImage: "person.crop.circle.badge.plus") {
                    model.discoverMailAccounts()
                }
                if model.discoveredMailAccounts.isEmpty {
                    Text("Discovery runs a fixed read-only JXA query and may request Automation permission for Mail.app.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(model.discoveredMailAccounts) { account in
                        Toggle(isOn: Binding(
                            get: { model.isMailAccountAllowed(account.id) },
                            set: { model.setMailAccountAllowed(account.id, allowed: $0) }
                        )) {
                            VStack(alignment: .leading) {
                                Text(account.name)
                                Text(account.id)
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                                    .textSelection(.enabled)
                            }
                        }
                    }
                }
                ManualIDListEditor(
                    model: model,
                    path: "personal_apps.mail.allowed_accounts",
                    title: "Allowed Mail account IDs",
                    draft: $newMailAccountID
                )
            }

            Section("Calendar.app") {
                Toggle("Enable Calendar access", isOn: rawToggle("personal_apps.calendar.enabled", defaultValue: false))
                TextField("Calendar helper executable", text: rawText("personal_apps.calendar.helper_path"))
                    .help("Absolute executable path inside the signed EventKit helper app bundle.")
                Button("Discover Calendars", systemImage: "calendar.badge.plus") {
                    model.discoverCalendars()
                }
                Button("Open Calendar privacy settings", systemImage: "lock.shield") {
                    model.openCalendarPrivacySettings()
                }
                Button("Reset Calendar permission and retry", systemImage: "arrow.clockwise") {
                    showCalendarResetConfirmation = true
                }
                if model.discoveredCalendars.isEmpty {
                    Text("Enable the signed llmtui Calendar helper under System Settings → Privacy & Security → Calendars. Discovery may request Full Calendar Access.")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                } else {
                    ForEach(model.discoveredCalendars) { calendar in
                        Toggle(isOn: Binding(
                            get: { model.isCalendarAllowed(calendar.id) },
                            set: { model.setCalendarAllowed(calendar.id, allowed: $0) }
                        )) {
                            VStack(alignment: .leading) {
                                Text(calendar.title)
                                Text("\(calendar.source) • \(calendar.id)")
                                    .font(.caption2)
                                    .foregroundStyle(.secondary)
                                    .textSelection(.enabled)
                            }
                        }
                    }
                }
                ManualIDListEditor(
                    model: model,
                    path: "personal_apps.calendar.allowed_calendars",
                    title: "Allowed Calendar IDs",
                    draft: $newCalendarID
                )
            }

            Section("Mutations") {
                Toggle("Allow approved mutations", isOn: rawToggle("personal_apps.mutations.enabled", defaultValue: false))
                Text("Keep this disabled unless you intentionally want approved Mail moves/drafts or Calendar event changes. Every mutation still requires llmtui's fresh approval flow.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            SettingsSaveRow(model: model)
        }
        .formStyle(.grouped)
        .padding(24)
        .navigationTitle("Personal Apps")
        .confirmationDialog(
            "Reset Calendar permission?",
            isPresented: $showCalendarResetConfirmation,
            titleVisibility: .visible
        ) {
            Button("Reset and discover calendars", role: .destructive) {
                model.resetCalendarPermissionAndDiscover()
            }
            Button("Cancel", role: .cancel) { }
        } message: {
            Text("This clears macOS's saved Calendar decision for the llmtui helper. macOS should ask for Full Calendar Access again.")
        }
    }

    private func rawToggle(_ path: String, defaultValue: Bool) -> Binding<Bool> {
        Binding(
            get: { model.configuration.rawSettings[path].map { $0 == "true" } ?? defaultValue },
            set: { model.configuration.rawSettings[path] = $0 ? "true" : "false" }
        )
    }

    private func rawText(_ path: String) -> Binding<String> {
        Binding(
            get: { model.configuration.rawSettings[path] ?? "" },
            set: { model.configuration.rawSettings[path] = $0 }
        )
    }
}

/// A macOS-native text field with explicit left alignment.
struct LeadingAlignedTextField: NSViewRepresentable {
    @Binding var text: String
    let placeholder: String

    func makeCoordinator() -> Coordinator {
        Coordinator(text: $text)
    }

    func makeNSView(context: Context) -> NSTextField {
        let field = NSTextField()
        field.placeholderString = placeholder
        field.isEditable = true
        field.isSelectable = true
        field.isBordered = true
        field.bezelStyle = .roundedBezel
        field.alignment = .left
        field.delegate = context.coordinator
        return field
    }

    func updateNSView(_ field: NSTextField, context: Context) {
        if field.stringValue != text {
            field.stringValue = text
        }
        field.alignment = .left
    }

    final class Coordinator: NSObject, NSTextFieldDelegate {
        private var text: Binding<String>

        init(text: Binding<String>) {
            self.text = text
        }

        func controlTextDidChange(_ notification: Notification) {
            guard let field = notification.object as? NSTextField else { return }
            text.wrappedValue = field.stringValue
        }
    }
}

struct ManualIDListEditor: View {
    let model: AppModel
    let path: String
    let title: String
    @Binding var draft: String

    private var values: [String] {
        model.configuration.listSettings[path, default: []]
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.subheadline.weight(.medium))

            if values.isEmpty {
                Text("None configured. You can add an ID manually if discovery is unavailable.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                ForEach(Array(values.enumerated()), id: \.element) { index, value in
                    HStack {
                        Text("Native identifier")
                        LeadingAlignedTextField(
                            text: Binding(
                                get: { value },
                                set: { newValue in
                                    var updated = model.configuration.listSettings[path, default: []]
                                    guard updated.indices.contains(index) else { return }
                                    updated[index] = newValue
                                    model.configuration.listSettings[path] = updated
                                }
                            ),
                            placeholder: "Native identifier"
                        )
                        .frame(maxWidth: .infinity)
                        Button("Remove", systemImage: "minus.circle") {
                            var updated = model.configuration.listSettings[path, default: []]
                            guard updated.indices.contains(index) else { return }
                            updated.remove(at: index)
                            model.configuration.listSettings[path] = updated
                        }
                        .labelStyle(.iconOnly)
                        .buttonStyle(.borderless)
                        .foregroundStyle(.red)
                    }
                }
            }

            HStack {
                TextField("Add native identifier", text: $draft)
                    .textFieldStyle(.roundedBorder)
                Button("Add", systemImage: "plus") {
                    let value = draft.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !value.isEmpty else { return }
                    var updated = model.configuration.listSettings[path, default: []]
                    guard !updated.contains(value) else {
                        draft = ""
                        return
                    }
                    updated.append(value)
                    model.configuration.listSettings[path] = updated
                    draft = ""
                }
                .disabled(draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }
        }
        .padding(.top, 6)
    }
}

struct MoreSettingsView: View {
    let model: AppModel
    @AppStorage("appearanceMode") private var appearanceModeRaw = AppearanceMode.system.rawValue
    @AppStorage(DiagnosticsLogger.enabledDefaultsKey) private var diagnosticLoggingEnabled = true
    @AppStorage(DiagnosticsLogger.levelDefaultsKey) private var diagnosticLogLevel = DiagnosticLevel.info.rawValue
    @State private var showClearLogsConfirmation = false
    @State private var loggingError: String?
    private let groups: [(String, String, String)] = [
        ("MCP", "mcp", "Model Context Protocol master switch and server controls."),
        ("Tools & Guardrails", "tools", "Native protocol, command limits, discovery, web, and safety guardrails."),
        ("RAG", "rag", "Local workspace indexing and retrieval limits."),
        ("Memory", "memory", "Local memory storage and retrieval budgets."),
        ("Context & Network", "context", "Context reservation, summarization, and related runtime controls."),
        ("Interface", "ui", "Terminal-style presentation preferences."),
        ("Privacy", "privacy", "Local-first behavior, prompt storage, and log redaction."),
        ("Response cache", "cache", "Local response cache location, size, and expiry."),
        ("Decision engine", "decision_engine", "Routing model used to pick which local model answers a request."),
        ("Templates", "templates", "Named conversation templates available via /template use."),
        ("Other runtime", "network", "Timeouts, retry policy, skills, plugins, and tool registry.")
    ]

    var body: some View {
        Form {
            Section {
                Picker("Appearance", selection: $appearanceModeRaw) {
                    ForEach(AppearanceMode.allCases) { mode in
                        Text(mode.title).tag(mode.rawValue)
                    }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                Text("Controls this app's own window appearance only — separate from the llmtui configuration file.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } header: {
                Text("Appearance")
            }
            Section("Diagnostic logging") {
                Toggle("Write diagnostic logs", isOn: $diagnosticLoggingEnabled)
                Picker("Minimum level", selection: $diagnosticLogLevel) {
                    ForEach(DiagnosticLevel.allCases) { level in
                        Text(level.title).tag(level.rawValue)
                    }
                }
                .disabled(!diagnosticLoggingEnabled)

                Text(DiagnosticsLogger.defaultFileURL.path)
                    .font(.caption.monospaced())
                    .foregroundStyle(.secondary)
                    .textSelection(.enabled)

                Text("Logs contain operational metadata only. Prompts, replies, tool arguments and results, provider response bodies, paths, Mail account IDs, and Calendar IDs are never written.")
                    .font(.caption)
                    .foregroundStyle(.secondary)

                HStack {
                    Button("Open Logs Folder", systemImage: "folder") {
                        do {
                            try FileManager.default.createDirectory(
                                at: DiagnosticsLogger.defaultDirectoryURL,
                                withIntermediateDirectories: true
                            )
                            NSWorkspace.shared.open(DiagnosticsLogger.defaultDirectoryURL)
                        } catch {
                            loggingError = error.localizedDescription
                        }
                    }
                    Button("Clear Logs", systemImage: "trash", role: .destructive) {
                        showClearLogsConfirmation = true
                    }
                }
            }
            ForEach(groups, id: \.1) { title, prefix, detail in
                let keys = model.configuration.rawSettings.keys
                    .filter { $0 == prefix || $0.hasPrefix(prefix + ".") }
                    .sorted()
                Section {
                    if keys.isEmpty {
                        Text("Not present in the current YAML; llmtui defaults apply.")
                            .foregroundStyle(.secondary)
                    } else {
                        Text(detail)
                            .font(.caption)
                            .foregroundStyle(.secondary)
                        // mcp.servers.<name>.* and templates.<name>.* hold
                        // multiple sibling entries (e.g. two MCP servers)
                        // whose settings otherwise interleave as identically
                        // titled rows ("Timeout", "Timeout", …) with no way
                        // to tell which entry each one belongs to.
                        ForEach(subsections(for: keys, groupPrefix: prefix)) { subsection in
                            if let label = subsection.label {
                                Text(label)
                                    .font(.headline.weight(.semibold))
                                    .padding(.top, 6)
                                    
                            }
                            ForEach(subsection.keys, id: \.self) { key in
                                SettingEditor(model: model, path: key, title: prettyKey(key))
                            }
                        }
                    }
                } header: {
                    Text(title)
                }
            }
            Section("Safe editing") {
                Text("Other list-valued YAML is preserved unchanged. Use the dedicated editors for model matches and Mail/Calendar IDs, then Save configuration.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            SettingsSaveRow(model: model)
        }
        .formStyle(.grouped)
        .padding(24)
        .navigationTitle("More Settings")
        .confirmationDialog(
            "Clear LLMTUIGUI diagnostic logs?",
            isPresented: $showClearLogsConfirmation,
            titleVisibility: .visible
        ) {
            Button("Clear Logs", role: .destructive) {
                Task {
                    do {
                        try await DiagnosticsLogger.shared.clear()
                    } catch {
                        loggingError = error.localizedDescription
                    }
                }
            }
            Button("Cancel", role: .cancel) { }
        } message: {
            Text("This permanently removes the active log and all four rotated archives.")
        }
        .alert("Logging Error", isPresented: Binding(
            get: { loggingError != nil },
            set: { if !$0 { loggingError = nil } }
        )) {
            Button("OK") { loggingError = nil }
        } message: {
            Text(loggingError ?? "")
        }
    }

    private func prettyKey(_ key: String) -> String {
        key.split(separator: ".").last.map(String.init)?.replacingOccurrences(of: "_", with: " ").capitalized ?? key
    }

    private struct SettingsSubsection: Identifiable {
        let id: String
        let label: String?
        let keys: [String]
    }

    private func subsections(for keys: [String], groupPrefix: String) -> [SettingsSubsection] {
        var ungrouped: [String] = []
        var grouped: [String: [String]] = [:]
        for key in keys {
            if let label = subsectionLabel(for: key, groupPrefix: groupPrefix) {
                grouped[label, default: []].append(key)
            } else {
                ungrouped.append(key)
            }
        }
        var result: [SettingsSubsection] = []
        if !ungrouped.isEmpty {
            result.append(SettingsSubsection(id: "_ungrouped", label: nil, keys: ungrouped))
        }
        for label in grouped.keys.sorted() {
            result.append(SettingsSubsection(id: label, label: label, keys: grouped[label, default: []].sorted()))
        }
        return result
    }

    private func subsectionLabel(for key: String, groupPrefix: String) -> String? {
        guard key.hasPrefix(groupPrefix + ".") else { return nil }
        let parts = key.dropFirst(groupPrefix.count + 1).split(separator: ".")
        if groupPrefix == "mcp", parts.count >= 3, parts[0] == "servers" {
            return String(parts[1])
        }
        if groupPrefix == "templates", parts.count >= 2 {
            return String(parts[0])
        }
        return nil
    }
}

struct SettingEditor: View {
    let model: AppModel
    let path: String
    let title: String

    var body: some View {
        if model.configuration.rawSettings[path] == "true" || model.configuration.rawSettings[path] == "false" {
            Toggle(title, isOn: Binding(
                get: { model.configuration.rawSettings[path] == "true" },
                set: { model.configuration.rawSettings[path] = $0 ? "true" : "false" }
            ))
        } else {
            TextField(title, text: Binding(
                get: { model.configuration.rawSettings[path] ?? "" },
                set: { model.configuration.rawSettings[path] = $0 }
            ))
        }
    }
}

struct SettingsSaveRow: View {
    let model: AppModel

    var body: some View {
        Section {
            HStack {
                Text(model.statusMessage)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Button("Reload") {
                    model.loadConfiguration()
                }
                Button("Reset draft") {
                    model.resetConfigurationDraft()
                }
                Button("Revert last save") {
                    model.revertConfiguration()
                }
                .disabled(!model.hasConfigurationBackup)
                Button("Save configuration") {
                    model.saveConfiguration()
                }
                .buttonStyle(.borderedProminent)
            }
        }
    }
}

#Preview {
    ContentView()
}
