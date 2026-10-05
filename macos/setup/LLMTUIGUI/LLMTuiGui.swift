import SwiftUI

@main
struct MyApp: App {
    var body: some Scene {
        WindowGroup("llmtui") {
            ContentView()
        }
        .commands {
            SidebarCommands()
            CommandGroup(replacing: .printItem) {
                PrintChatCommand()
            }
            CommandGroup(after: .appInfo) {
                Button("Open llmtui configuration folder") {
                    NSWorkspace.shared.open(
                        LLMTUIConfigurationStore.defaultURL.deletingLastPathComponent()
                    )
                }
                Button("Open LLMTUIGUI logs folder") {
                    try? FileManager.default.createDirectory(
                        at: DiagnosticsLogger.defaultDirectoryURL,
                        withIntermediateDirectories: true
                    )
                    NSWorkspace.shared.open(DiagnosticsLogger.defaultDirectoryURL)
                }
            }
        }
    }
}

/// Reads the print action `ChatView` publishes via `.focusedSceneValue` so
/// the File-menu command stays scoped to windows actually showing a chat
/// with messages, instead of always being enabled.
private struct PrintChatCommand: View {
    @FocusedValue(\.printChatAction) private var printChatAction

    var body: some View {
        Button("Print Chat…") {
            printChatAction?()
        }
        .keyboardShortcut("p", modifiers: .command)
        .disabled(printChatAction == nil)
    }
}
