import SwiftUI
import UIKit

/// The app's colors and shared styles, defined once for both appearances:
///
/// - Dark: near-black background, raised charcoal cards and a single vivid
///   magenta accent, used sparingly (send, selection, links, active toggles).
/// - Light: a soft lavender-grey background, white cards with gentle shadows
///   and a violet-to-pink gradient for the accent.
///
/// Every color adapts automatically to the Light/Dark setting in Settings.
enum Theme {
    // MARK: Colors

    static let accent = Color(dynamic: UIColor(hex: 0x7A5CF0), dark: UIColor(hex: 0xE42DF2))
    static let accentSecondary = Color(dynamic: UIColor(hex: 0xE468B4), dark: UIColor(hex: 0xA51FE0))
    static let background = Color(dynamic: UIColor(hex: 0xF3F2F8), dark: UIColor(hex: 0x000000))
    static let surface = Color(dynamic: UIColor(hex: 0xFFFFFF), dark: UIColor(hex: 0x17171A))
    static let raisedSurface = Color(dynamic: UIColor(hex: 0xF6F5FB), dark: UIColor(hex: 0x232327))
    static let hairline = Color(dynamic: UIColor(white: 0, alpha: 0.06), dark: UIColor(white: 1, alpha: 0.08))
    static let shadow = Color(dynamic: UIColor(red: 0.25, green: 0.2, blue: 0.5, alpha: 0.10), dark: .clear)

    static let success = Color(dynamic: UIColor(hex: 0x1FA971), dark: UIColor(hex: 0x3DDC97))
    static let warning = Color(dynamic: UIColor(hex: 0xE08A00), dark: UIColor(hex: 0xFFB340))
    static let danger = Color(dynamic: UIColor(hex: 0xE5484D), dark: UIColor(hex: 0xFF6369))

    /// The accent as a fill: a violet-to-pink gradient in light mode, a
    /// magenta-to-purple glow in dark mode.
    static let accentGradient = LinearGradient(
        colors: [accent, accentSecondary],
        startPoint: .topLeading,
        endPoint: .bottomTrailing
    )

    static let cardRadius: CGFloat = 22
    static let controlRadius: CGFloat = 18

    /// A distinct tint per provider type, used for its icon tile.
    static func tint(for provider: MobileProviderType) -> Color {
        switch provider {
        case .ollama: Color(dynamic: UIColor(hex: 0xF08A24), dark: UIColor(hex: 0xFFA14A))
        case .lmStudio: Color(dynamic: UIColor(hex: 0x6E5CF0), dark: UIColor(hex: 0x9C8CFF))
        case .openAICompatible: Color(dynamic: UIColor(hex: 0x0FA39A), dark: UIColor(hex: 0x3DD6C6))
        }
    }

    static func systemImage(for provider: MobileProviderType) -> String {
        switch provider {
        case .ollama: "cpu"
        case .lmStudio: "desktopcomputer"
        case .openAICompatible: "cloud"
        }
    }

    /// A distinct tint per attachment kind.
    static func tint(for kind: ChatDocumentKind) -> Color {
        switch kind {
        case .pdf: danger
        case .text: Color(dynamic: UIColor(hex: 0x3B82F6), dark: UIColor(hex: 0x6AA6FF))
        case .markdown: Color(dynamic: UIColor(hex: 0x0FA39A), dark: UIColor(hex: 0x3DD6C6))
        case .image: Color(dynamic: UIColor(hex: 0xC2410C), dark: UIColor(hex: 0xFF8A4C))
        }
    }
}

// MARK: - Components

/// A rounded square with a tinted background and a symbol, for list rows,
/// settings and attachments.
struct IconTile: View {
    let systemName: String
    let tint: Color
    var size: CGFloat = 34

    var body: some View {
        Image(systemName: systemName)
            .font(.system(size: size * 0.46, weight: .semibold))
            .foregroundStyle(tint)
            .frame(width: size, height: size)
            .background(tint.opacity(0.16), in: RoundedRectangle(cornerRadius: size * 0.3, style: .continuous))
            .accessibilityHidden(true)
    }
}

/// A full-width capsule button filled with the accent gradient.
struct AccentButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.headline)
            .foregroundStyle(.white)
            .padding(.horizontal, 22)
            .padding(.vertical, 12)
            .background(Theme.accentGradient, in: Capsule())
            .opacity(isEnabled ? (configuration.isPressed ? 0.85 : 1) : 0.4)
            .scaleEffect(configuration.isPressed ? 0.97 : 1)
            .animation(.snappy(duration: 0.15), value: configuration.isPressed)
    }
}

extension View {
    /// A card: the surface color, large continuous corners, a soft shadow in
    /// light mode and a hairline edge in dark mode.
    func themedCard(radius: CGFloat = Theme.cardRadius) -> some View {
        let shape = RoundedRectangle(cornerRadius: radius, style: .continuous)
        return background(Theme.surface, in: shape)
            .overlay(shape.strokeBorder(Theme.hairline, lineWidth: 1))
            .shadow(color: Theme.shadow, radius: 14, x: 0, y: 6)
    }

    /// The app background behind lists and forms.
    func themedBackground() -> some View {
        scrollContentBackground(.hidden)
            .background(Theme.background.ignoresSafeArea())
    }
}

// MARK: - Color helpers

extension Color {
    /// A color with separate light- and dark-mode values.
    init(dynamic light: UIColor, dark: UIColor) {
        self.init(uiColor: UIColor { traits in
            traits.userInterfaceStyle == .dark ? dark : light
        })
    }
}

extension UIColor {
    convenience init(hex: UInt32, alpha: CGFloat = 1) {
        self.init(
            red: CGFloat((hex >> 16) & 0xFF) / 255,
            green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255,
            alpha: alpha
        )
    }
}
