import Foundation
import SwiftUI

struct MarkdownDocument: Equatable {
    enum ListMarker: Equatable {
        case unordered
        case ordered(Int)
    }

    enum Kind: Equatable {
        case paragraph
        case heading(Int)
        case listItem(marker: ListMarker, depth: Int)
        case blockQuote(depth: Int)
        case thematicBreak
    }

    struct Block: Identifiable, Equatable {
        let id: Int
        let kind: Kind
        let content: AttributedString
    }

    let blocks: [Block]

    init(source: String) {
        let options = AttributedString.MarkdownParsingOptions(
            interpretedSyntax: .full,
            failurePolicy: .returnPartiallyParsedIfPossible
        )
        guard let parsed = try? AttributedString(markdown: source, options: options) else {
            blocks = source.isEmpty ? [] : [
                Block(id: 0, kind: .paragraph, content: AttributedString(source))
            ]
            return
        }

        var result: [Block] = []
        var currentIdentity: Int?
        var currentKind: Kind?
        var currentContent = AttributedString()
        var seenListItems = Set<Int>()

        func appendCurrent() {
            guard let identity = currentIdentity, let kind = currentKind else { return }
            var content = currentContent
            Self.styleInlineContent(&content)
            result.append(Block(id: identity, kind: kind, content: content))
            currentContent = AttributedString()
        }

        for run in parsed.runs {
            let descriptor = Self.blockDescriptor(
                for: run.presentationIntent,
                seenListItems: &seenListItems
            )
            let identity = descriptor?.identity ?? Int.min
            let kind = descriptor?.kind ?? .paragraph

            if currentIdentity != identity {
                appendCurrent()
                currentIdentity = identity
                currentKind = kind
            }
            currentContent.append(AttributedString(parsed[run.range]))
        }
        appendCurrent()

        blocks = result.filter { block in
            block.kind == .thematicBreak ||
                !String(block.content.characters).trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        }
    }

    private static func blockDescriptor(
        for intent: PresentationIntent?,
        seenListItems: inout Set<Int>
    ) -> (identity: Int, kind: Kind)? {
        guard let intent else { return nil }
        var paragraphIdentity: Int?
        var quoteDepth = 0
        var listDepth = 0
        var deepestListItem: (identity: Int, ordinal: Int)?
        var heading: (identity: Int, level: Int)?
        var thematicBreakIdentity: Int?
        var innermostListIsOrdered = false
        var foundInnermostList = false

        for component in intent.components {
            switch component.kind {
            case .paragraph:
                paragraphIdentity = component.identity
            case .header(let level):
                heading = (component.identity, level)
            case .orderedList:
                listDepth += 1
                if !foundInnermostList {
                    innermostListIsOrdered = true
                    foundInnermostList = true
                }
            case .unorderedList:
                listDepth += 1
                if !foundInnermostList {
                    foundInnermostList = true
                }
            case .listItem(let ordinal):
                if deepestListItem == nil {
                    deepestListItem = (component.identity, ordinal)
                }
            case .blockQuote:
                quoteDepth += 1
            case .thematicBreak:
                thematicBreakIdentity = component.identity
            default:
                break
            }
        }

        if let thematicBreakIdentity {
            return (thematicBreakIdentity, .thematicBreak)
        }
        if let heading {
            return (heading.identity, .heading(heading.level))
        }
        if let item = deepestListItem {
            let isFirstParagraph = seenListItems.insert(item.identity).inserted
            if isFirstParagraph {
                let marker: ListMarker = innermostListIsOrdered
                    ? .ordered(item.ordinal)
                    : .unordered
                return (paragraphIdentity ?? item.identity, .listItem(marker: marker, depth: max(listDepth, 1)))
            }
            return (paragraphIdentity ?? item.identity, .paragraph)
        }
        if quoteDepth > 0 {
            return (paragraphIdentity ?? intent.components.last?.identity ?? 0, .blockQuote(depth: quoteDepth))
        }
        return (paragraphIdentity ?? intent.components.last?.identity ?? 0, .paragraph)
    }

    private static func styleInlineContent(_ content: inout AttributedString) {
        for run in content.runs {
            if run.inlinePresentationIntent?.contains(.code) == true {
                content[run.range].font = .system(.body, design: .monospaced)
                content[run.range].backgroundColor = Color.secondary.opacity(0.14)
            }
            if let link = run.link,
               let scheme = link.scheme?.lowercased(),
               scheme != "http",
               scheme != "https" {
                content[run.range].link = nil
            }
        }
    }
}

struct MarkdownText: View {
    let source: String

    private var document: MarkdownDocument {
        MarkdownDocument(source: source)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            ForEach(document.blocks) { block in
                MarkdownBlockView(block: block)
            }
        }
        .textSelection(.enabled)
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct MarkdownBlockView: View {
    let block: MarkdownDocument.Block

    @ViewBuilder
    var body: some View {
        switch block.kind {
        case .paragraph:
            MathAwareText(content: block.content)
                .font(.body)
                .padding(.bottom, 10)
        case .heading(let level):
            MathAwareText(
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
                MathAwareText(content: block.content)
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
                MathAwareText(content: block.content, foreground: .secondary)
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
