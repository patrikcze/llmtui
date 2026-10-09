import SwiftUI
import AppKit

/// The app's colors and shared styles, the same palette as the iOS app
/// (`LLMTUIiOS/Theme.swift`), defined once for both appearances:
///
/// - Dark: near-black background, raised charcoal cards and a single vivid
///   magenta accent, used sparingly (send, selection, links, active toggles).
/// - Light: a soft lavender-grey background, white cards with gentle shadows
///   and a violet-to-pink gradient for the accent.
///
/// Every color follows the Light/Dark/System appearance chosen in More
/// Settings.
enum Theme {
    // MARK: Colors

    static let accent = Color(dynamic: NSColor(hex: 0x7A5CF0), dark: NSColor(hex: 0xE42DF2))
    static let accentSecondary = Color(dynamic: NSColor(hex: 0xE468B4), dark: NSColor(hex: 0xA51FE0))
    static let background = Color(dynamic: NSColor(hex: 0xF3F2F8), dark: NSColor(hex: 0x000000))
    static let surface = Color(dynamic: NSColor(hex: 0xFFFFFF), dark: NSColor(hex: 0x17171A))
    static let raisedSurface = Color(dynamic: NSColor(hex: 0xF6F5FB), dark: NSColor(hex: 0x232327))
    static let hairline = Color(dynamic: NSColor(white: 0, alpha: 0.06), dark: NSColor(white: 1, alpha: 0.08))
    static let shadow = Color(dynamic: NSColor(red: 0.25, green: 0.2, blue: 0.5, alpha: 0.10), dark: .clear)

    static let success = Color(dynamic: NSColor(hex: 0x1FA971), dark: NSColor(hex: 0x3DDC97))
    static let warning = Color(dynamic: NSColor(hex: 0xE08A00), dark: NSColor(hex: 0xFFB340))
    static let danger = Color(dynamic: NSColor(hex: 0xE5484D), dark: NSColor(hex: 0xFF6369))
    static let info = Color(dynamic: NSColor(hex: 0x3B82F6), dark: NSColor(hex: 0x6AA6FF))
    static let teal = Color(dynamic: NSColor(hex: 0x0FA39A), dark: NSColor(hex: 0x3DD6C6))
    static let orange = Color(dynamic: NSColor(hex: 0xF08A24), dark: NSColor(hex: 0xFFA14A))

    /// The accent as a fill: a violet-to-pink gradient in light mode, a
    /// magenta-to-purple glow in dark mode.
    static let accentGradient = LinearGradient(
        colors: [accent, accentSecondary],
        startPoint: .topLeading,
        endPoint: .bottomTrailing
    )

    static let cardRadius: CGFloat = 18
    static let controlRadius: CGFloat = 14

    /// A distinct tint per provider type, used for its icon tile.
    static func tint(for provider: ProviderType) -> Color {
        switch provider {
        case .ollama: orange
        case .openAICompatible: teal
        case .embedded: Color(dynamic: NSColor(hex: 0x6E5CF0), dark: NSColor(hex: 0x9C8CFF))
        case .mock: Color.secondary
        }
    }

    static func systemImage(for provider: ProviderType) -> String {
        switch provider {
        case .ollama: "cpu"
        case .openAICompatible: "cloud"
        case .embedded: "memorychip"
        case .mock: "theatermasks"
        }
    }

    /// A distinct tint per sidebar section.
    static func tint(for section: AppSection) -> Color {
        switch section {
        case .overview: info
        case .chat: accent
        case .providers: teal
        case .chatSettings: Color(dynamic: NSColor(hex: 0x6E5CF0), dark: NSColor(hex: 0x9C8CFF))
        case .tools: warning
        case .agentRuntime: accentSecondary
        case .profiles: orange
        case .moreSettings: Color.secondary
        case .personalApps: danger
        }
    }
}

// MARK: - Components

/// A rounded square with a tinted background and a symbol, for the sidebar,
/// cards, headers and tool activity.
struct IconTile: View {
    let systemName: String
    let tint: Color
    var size: CGFloat = 28

    var body: some View {
        Image(systemName: systemName)
            .font(.system(size: size * 0.46, weight: .semibold))
            .foregroundStyle(tint)
            .frame(width: size, height: size)
            .background(tint.opacity(0.16), in: RoundedRectangle(cornerRadius: size * 0.3, style: .continuous))
            .accessibilityHidden(true)
    }
}

/// A capsule button filled with the accent gradient, for the main action on
/// a screen (Save).
struct AccentButtonStyle: ButtonStyle {
    @Environment(\.isEnabled) private var isEnabled

    func makeBody(configuration: Configuration) -> some View {
        configuration.label
            .font(.body.weight(.semibold))
            .foregroundStyle(.white)
            .padding(.horizontal, 16)
            .padding(.vertical, 6)
            .background(Theme.accentGradient, in: Capsule())
            .opacity(isEnabled ? (configuration.isPressed ? 0.85 : 1) : 0.4)
            .scaleEffect(configuration.isPressed ? 0.97 : 1)
            .animation(.snappy(duration: 0.15), value: configuration.isPressed)
            .contentShape(Capsule())
    }
}

extension View {
    /// A card: the surface color, large continuous corners, a soft shadow in
    /// light mode and a hairline edge in dark mode.
    func themedCard(radius: CGFloat = Theme.cardRadius) -> some View {
        let shape = RoundedRectangle(cornerRadius: radius, style: .continuous)
        return background(Theme.surface, in: shape)
            .overlay(shape.strokeBorder(Theme.hairline, lineWidth: 1))
            .shadow(color: Theme.shadow, radius: 12, x: 0, y: 5)
    }

    /// The app background behind scroll views, lists and grouped forms.
    func themedBackground() -> some View {
        scrollContentBackground(.hidden)
            .background(Theme.background.ignoresSafeArea())
    }
}

// MARK: - Color helpers

extension Color {
    /// A color with separate light- and dark-mode values.
    init(dynamic light: NSColor, dark: NSColor) {
        self.init(nsColor: NSColor(name: nil) { appearance in
            appearance.bestMatch(from: [.darkAqua, .aqua]) == .darkAqua ? dark : light
        })
    }
}

extension NSColor {
    convenience init(hex: UInt32, alpha: CGFloat = 1) {
        self.init(
            srgbRed: CGFloat((hex >> 16) & 0xFF) / 255,
            green: CGFloat((hex >> 8) & 0xFF) / 255,
            blue: CGFloat(hex & 0xFF) / 255,
            alpha: alpha
        )
    }
}
