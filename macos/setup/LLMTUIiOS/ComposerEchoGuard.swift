import Foundation

/// Keeps a sent message from coming back into the composer.
///
/// The message field is a multi-line `TextField`, which UIKit backs with a
/// `UITextView`. When Send clears the draft while the keyboard still holds
/// uncommitted text (an inline prediction, an autocorrection or other marked
/// text), the text view commits it a moment later and writes its whole old
/// contents back through the binding, so the message is both sent and still
/// in the field. It happens only when such text is pending at the tap, which
/// is why it is intermittent, and more often while a reply streams, because
/// every streamed delta redraws the composer.
///
/// After `armed(with:)`, a write from the field shortly after the clear that
/// still starts with what was sent is that echo, and `accepts` rejects it.
/// The first write that is not an echo, or the end of the window, disarms
/// the guard, so typing (or pasting the same text again later) is never
/// lost.
struct ComposerEchoGuard {
    /// How long after a send a write from the field can still be its echo.
    static let window: Duration = .seconds(1)

    private var sent: String?
    private var sentAt: ContinuousClock.Instant?

    /// Arms the guard with the raw draft that was just sent or queued.
    mutating func arm(with sentDraft: String, at now: ContinuousClock.Instant) {
        let trimmed = sentDraft.trimmingCharacters(in: .whitespacesAndNewlines)
        sent = trimmed.isEmpty ? nil : trimmed
        sentAt = sent == nil ? nil : now
    }

    mutating func disarm() {
        sent = nil
        sentAt = nil
    }

    /// Whether a write of `text` from the message field should reach the
    /// draft. Rejects the echo of the last sent draft; any other write
    /// disarms the guard.
    mutating func accepts(_ text: String, at now: ContinuousClock.Instant) -> Bool {
        guard let sent, let sentAt else { return true }
        guard now - sentAt <= Self.window else {
            disarm()
            return true
        }
        if Self.isEcho(text, of: sent) { return false }
        disarm()
        return true
    }

    /// Whether `text` is `sent` written back: equal to it, or starting with
    /// it (a committed inline prediction adds to the end), or, when the last
    /// word was autocorrected, starting with everything before that word.
    static func isEcho(_ text: String, of sent: String) -> Bool {
        let candidate = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !candidate.isEmpty else { return false }
        if candidate.hasPrefix(sent) { return true }
        guard let lastBreak = sent.lastIndex(where: \.isWhitespace) else { return false }
        let head = sent[..<lastBreak]
        guard !head.isEmpty else { return false }
        return candidate.hasPrefix(head) && candidate.count >= head.count + 2
    }
}
