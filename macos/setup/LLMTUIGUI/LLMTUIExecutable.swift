import Foundation

/// Locates the llmtui command-line binary the app can call (for example
/// `llmtui config path`, `config show`, or `doctor`).
///
/// Resolution order:
/// 1. `LLMTUI_EXECUTABLE`, when set to an executable file.
/// 2. The copy bundled in the app at `Contents/Helpers/llmtui` (release builds).
/// 3. The usual install locations: `~/.local/bin`, `~/go/bin`,
///    `/opt/homebrew/bin`, `/usr/local/bin`.
nonisolated enum LLMTUIExecutable {
    static let environmentKey = "LLMTUI_EXECUTABLE"

    /// The first existing executable in resolution order, or nil.
    static func locate(
        environment: [String: String] = ProcessInfo.processInfo.environment,
        bundle: Bundle = .main,
        fileManager: FileManager = .default
    ) -> URL? {
        candidates(environment: environment, bundle: bundle, homeDirectory: fileManager.homeDirectoryForCurrentUser)
            .first { fileManager.isExecutableFile(atPath: $0.path) }
    }

    /// Every location checked, in order.
    static func candidates(environment: [String: String], bundle: Bundle, homeDirectory: URL) -> [URL] {
        var urls: [URL] = []
        if let override = environment[environmentKey], !override.isEmpty {
            urls.append(URL(fileURLWithPath: (override as NSString).expandingTildeInPath))
        }
        urls.append(bundle.bundleURL.appending(path: "Contents/Helpers/llmtui"))
        urls.append(homeDirectory.appending(path: ".local/bin/llmtui"))
        urls.append(homeDirectory.appending(path: "go/bin/llmtui"))
        urls.append(URL(fileURLWithPath: "/opt/homebrew/bin/llmtui"))
        urls.append(URL(fileURLWithPath: "/usr/local/bin/llmtui"))
        return urls
    }
}
