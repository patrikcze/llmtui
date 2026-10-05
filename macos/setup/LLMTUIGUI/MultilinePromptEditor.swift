import SwiftUI
import AppKit

struct MultilinePromptEditor: NSViewRepresentable {
    @Binding var text: String
    let placeholder: String
    let onSend: () -> Void
    let onPasteAttachment: (ChatAttachment) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(text: $text, onSend: onSend)
    }

    func makeNSView(context: Context) -> NSScrollView {
        let scrollView = NSScrollView()
        scrollView.drawsBackground = false
        scrollView.hasVerticalScroller = true
        scrollView.hasHorizontalScroller = false
        scrollView.borderType = .noBorder
        scrollView.autohidesScrollers = true

        let textView = PromptTextView()
        textView.delegate = context.coordinator
        textView.onSend = onSend
        textView.onPasteAttachment = onPasteAttachment
        textView.string = text
        textView.font = .systemFont(ofSize: NSFont.systemFontSize)
        textView.textColor = .labelColor
        textView.insertionPointColor = .controlAccentColor
        textView.backgroundColor = .clear
        textView.drawsBackground = false
        textView.isRichText = false
        textView.importsGraphics = false
        textView.allowsUndo = true
        textView.isVerticallyResizable = true
        textView.isHorizontallyResizable = false
        textView.autoresizingMask = [.width]
        textView.textContainerInset = NSSize(width: 0, height: 7)
        textView.textContainer?.widthTracksTextView = true
        textView.textContainer?.lineFragmentPadding = 0
        textView.textContainer?.containerSize = NSSize(
            width: CGFloat.greatestFiniteMagnitude,
            height: CGFloat.greatestFiniteMagnitude
        )
        textView.setAccessibilityLabel(placeholder)
        textView.setAccessibilityRole(.textArea)

        scrollView.documentView = textView
        return scrollView
    }

    func updateNSView(_ scrollView: NSScrollView, context: Context) {
        guard let textView = scrollView.documentView as? PromptTextView else {
            return
        }

        textView.onSend = onSend
        textView.onPasteAttachment = onPasteAttachment
        if textView.string != text {
            textView.string = text
        }
    }

    final class Coordinator: NSObject, NSTextViewDelegate {
        private var text: Binding<String>
        private let onSend: () -> Void

        init(text: Binding<String>, onSend: @escaping () -> Void) {
            self.text = text
            self.onSend = onSend
        }

        func textDidChange(_ notification: Notification) {
            guard let textView = notification.object as? NSTextView else {
                return
            }
            text.wrappedValue = textView.string
        }
    }

    final class PromptTextView: NSTextView {
        var onSend: (() -> Void)?
        var onPasteAttachment: ((ChatAttachment) -> Void)?

        override var readablePasteboardTypes: [NSPasteboard.PasteboardType] {
            Array(Set(super.readablePasteboardTypes + NSImage.readableTypes(for: .general)))
        }

        override func validateUserInterfaceItem(
            _ item: any NSValidatedUserInterfaceItem
        ) -> Bool {
            if item.action == #selector(paste(_:)),
               isEditable,
               onPasteAttachment != nil,
               NSImage.canInit(with: .general) {
                return true
            }
            return super.validateUserInterfaceItem(item)
        }

        override func validateMenuItem(_ menuItem: NSMenuItem) -> Bool {
            if menuItem.action == #selector(paste(_:)),
               isEditable,
               onPasteAttachment != nil,
               NSImage.canInit(with: .general) {
                return true
            }
            return super.validateMenuItem(menuItem)
        }

        override func paste(_ sender: Any?) {
            if let attachment = try? ChatAttachment.load(from: .general) {
                onPasteAttachment?(attachment)
                return
            }
            super.paste(sender)
        }

        override func keyDown(with event: NSEvent) {
            let modifiers = event.modifierFlags.intersection(.deviceIndependentFlagsMask)
            let isReturn = event.keyCode == 36 || event.keyCode == 76

            guard isReturn else {
                super.keyDown(with: event)
                return
            }

            if modifiers.contains(.shift) {
                insertText("\n", replacementRange: selectedRange())
            } else {
                onSend?()
            }
        }
    }
}
