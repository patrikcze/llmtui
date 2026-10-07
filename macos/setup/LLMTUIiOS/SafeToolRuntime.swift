import Foundation
import UIKit

actor MobileMemoryStore {
    static let shared = MobileMemoryStore()

    private struct Entry: Codable, Identifiable {
        let id: UUID
        let text: String
    }

    private let key = "iosChatMemories"

    func list() -> String {
        let entries = load()
        return entries.isEmpty
            ? "No saved memories."
            : entries.map { "\($0.id.uuidString): \($0.text)" }.joined(separator: "\n")
    }

    func remember(_ text: String) throws -> String {
        let cleaned = String(text.trimmingCharacters(in: .whitespacesAndNewlines).prefix(2_000))
        guard !cleaned.isEmpty else { throw MobileChatError.invalidResponse }
        var entries = load()
        let entry = Entry(id: UUID(), text: cleaned)
        entries.append(entry)
        save(entries)
        return "Saved memory \(entry.id.uuidString)."
    }

    func forget(_ id: String) -> String {
        var entries = load()
        let oldCount = entries.count
        entries.removeAll { $0.id.uuidString == id }
        save(entries)
        return entries.count == oldCount ? "Memory not found." : "Memory removed."
    }

    private func load() -> [Entry] {
        guard let data = UserDefaults.standard.data(forKey: key),
              let entries = try? JSONDecoder().decode([Entry].self, from: data) else { return [] }
        return entries
    }

    private func save(_ entries: [Entry]) {
        UserDefaults.standard.set(try? JSONEncoder().encode(entries), forKey: key)
    }
}

enum MobileLocalContext {
    static func value(now: Date = .now) -> String {
        let localFormatter = DateFormatter()
        localFormatter.locale = Locale(identifier: "en_US_POSIX")
        localFormatter.timeZone = .current
        localFormatter.dateFormat = "yyyy-MM-dd HH:mm:ss zzz"

        let utcFormatter = ISO8601DateFormatter()
        utcFormatter.timeZone = TimeZone(secondsFromGMT: 0)

        let timeZone = TimeZone.current
        let offset = timeZone.secondsFromGMT(for: now)
        let sign = offset >= 0 ? "+" : "-"
        let absoluteOffset = abs(offset)
        let offsetText = String(
            format: "%@%02d:%02d",
            sign,
            absoluteOffset / 3_600,
            (absoluteOffset % 3_600) / 60
        )
        let process = ProcessInfo.processInfo
        let disk = storageDescription()

        return """
        local_datetime: \(localFormatter.string(from: now))
        utc_datetime: \(utcFormatter.string(from: now))
        timezone: \(timeZone.identifier)
        utc_offset: \(offsetText)
        locale: \(Locale.current.identifier)
        calendar: \(Calendar.current.identifier)
        os: \(UIDevice.current.systemName) \(UIDevice.current.systemVersion)
        device_class: \(UIDevice.current.model)
        architecture: \(architecture)
        processor_count: \(process.processorCount) logical cores (\(process.activeProcessorCount) active)
        memory_total: \(formattedBytes(Int64(process.physicalMemory)))
        storage_total: \(disk.total)
        storage_available: \(disk.available)
        uptime: \(formattedUptime(process.systemUptime))
        thermal_state: \(thermalStateName(process.thermalState))
        low_power_mode: \(process.isLowPowerModeEnabled)
        """
    }

    private static var architecture: String {
#if arch(arm64)
        "arm64"
#elseif arch(x86_64)
        "x86_64"
#else
        "unknown"
#endif
    }

    private static func formattedBytes(_ bytes: Int64) -> String {
        ByteCountFormatter.string(fromByteCount: bytes, countStyle: .memory)
    }

    private static func formattedUptime(_ seconds: TimeInterval) -> String {
        let totalMinutes = Int(seconds) / 60
        let days = totalMinutes / (60 * 24)
        let hours = (totalMinutes / 60) % 24
        let minutes = totalMinutes % 60
        return "\(days)d \(hours)h \(minutes)m"
    }

    private static func storageDescription() -> (total: String, available: String) {
        let home = URL(fileURLWithPath: NSHomeDirectory())
        guard let values = try? home.resourceValues(
            forKeys: [.volumeTotalCapacityKey, .volumeAvailableCapacityForImportantUsageKey]
        ) else {
            return ("unavailable", "unavailable")
        }
        let total = values.volumeTotalCapacity.map { formattedBytes(Int64($0)) } ?? "unavailable"
        let available: String
        if let availableCapacity = values.volumeAvailableCapacityForImportantUsage {
            available = formattedBytes(availableCapacity)
        } else {
            available = "unavailable"
        }
        return (total, available)
    }

    private static func thermalStateName(_ state: ProcessInfo.ThermalState) -> String {
        switch state {
        case .nominal: "nominal"
        case .fair: "fair"
        case .serious: "serious"
        case .critical: "critical"
        @unknown default: "unknown"
        }
    }
}

enum SafeWebFetcher {
    private static let browserUserAgent = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1"

    static func search(_ query: String) async throws -> String {
        let cleaned = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !cleaned.isEmpty,
              var components = URLComponents(string: "https://html.duckduckgo.com/html/") else {
            throw MobileChatError.invalidResponse
        }
        components.queryItems = [URLQueryItem(name: "q", value: cleaned)]
        guard let url = components.url else { throw MobileChatError.invalidResponse }
        let data = try await fetchData(url, browserHeaders: true)
        let html = String(decoding: data, as: UTF8.self)
        let titleRegex = try NSRegularExpression(
            pattern: #"<a[^>]*class="[^"]*result__a[^"]*"[^>]*href="([^"]+)"[^>]*>(.*?)</a>"#,
            options: [.caseInsensitive, .dotMatchesLineSeparators]
        )
        let snippetRegex = try NSRegularExpression(
            pattern: #"<a[^>]*class="[^"]*result__snippet[^"]*"[^>]*>(.*?)</a>"#,
            options: [.caseInsensitive, .dotMatchesLineSeparators]
        )
        let range = NSRange(html.startIndex..., in: html)
        let titles = titleRegex.matches(in: html, range: range)
        let snippets = snippetRegex.matches(in: html, range: range)
        var results: [String] = []
        for (index, match) in titles.prefix(8).enumerated() {
            guard let titleRange = Range(match.range(at: 2), in: html),
                  let urlRange = Range(match.range(at: 1), in: html) else { continue }
            let title = plainText(String(html[titleRange]))
            let resultURL = resolvedSearchURL(decodeHTMLEntities(String(html[urlRange])))
            var snippet = ""
            if index < snippets.count,
               let snippetRange = Range(snippets[index].range(at: 1), in: html) {
                snippet = plainText(String(html[snippetRange]))
            }
            results.append("\(index + 1). \(title)\n   \(resultURL)\n   \(snippet)")
        }
        guard !results.isEmpty else { return "No DuckDuckGo results found." }
        return results.joined(separator: "\n\n")
            + "\n\nNext step: call web_fetch on the 1-3 most relevant URLs before answering."
    }

    static func fetch(_ rawURL: String) async throws -> String {
        guard let url = URL(string: rawURL),
              let scheme = url.scheme?.lowercased(),
              ["http", "https"].contains(scheme),
              let host = url.host,
              isPublicHost(host) else { throw MobileChatError.unsafeURL }

        let data = try await fetchData(url, browserHeaders: true)
        let rawText = String(decoding: data, as: UTF8.self)
        return String(plainText(rawText).prefix(40_000))
    }

    static func isPublicHost(_ host: String) -> Bool {
        let normalized = host.lowercased().trimmingCharacters(in: CharacterSet(charactersIn: "[]"))
        if normalized == "localhost" || normalized.hasSuffix(".local") || normalized == "::1" { return false }
        let parts = normalized.split(separator: ".").compactMap { Int($0) }
        if parts.count == 4 {
            let a = parts[0]
            let b = parts[1]
            if a == 10 || a == 127 || a == 0 || a >= 224 { return false }
            if a == 169 && b == 254 { return false }
            if a == 172 && (16...31).contains(b) { return false }
            if a == 192 && b == 168 { return false }
            if a == 100 && (64...127).contains(b) { return false }
        }
        return !normalized.hasPrefix("fc") && !normalized.hasPrefix("fd") && !normalized.hasPrefix("fe80:")
    }

    private static func fetchData(_ url: URL, browserHeaders: Bool = false) async throws -> Data {
        guard let host = url.host, isPublicHost(host) else { throw MobileChatError.unsafeURL }
        var request = URLRequest(url: url)
        request.timeoutInterval = 20
        request.setValue("text/plain, text/html, application/json", forHTTPHeaderField: "Accept")
        if browserHeaders {
            request.setValue(browserUserAgent, forHTTPHeaderField: "User-Agent")
            request.setValue("en-US,en;q=0.9", forHTTPHeaderField: "Accept-Language")
        }
        let (data, response) = try await URLSession.shared.data(for: request)
        guard data.count <= 1_000_000 else { throw MobileChatError.responseTooLarge }
        guard let http = response as? HTTPURLResponse,
              let finalHost = http.url?.host,
              isPublicHost(finalHost) else { throw MobileChatError.unsafeURL }
        guard (200..<300).contains(http.statusCode) else {
            throw MobileChatError.server(http.statusCode, "Web request failed")
        }
        return data
    }

    private static func resolvedSearchURL(_ rawURL: String) -> String {
        let absolute = rawURL.hasPrefix("//") ? "https:\(rawURL)" : rawURL
        guard let url = URL(string: absolute),
              let components = URLComponents(url: url, resolvingAgainstBaseURL: true),
              let target = components.queryItems?.first(where: { $0.name == "uddg" })?.value,
              !target.isEmpty else { return absolute }
        return target
    }

    private static func plainText(_ html: String) -> String {
        let removableBlocks = [
            #"<head[^>]*>[\s\S]*?</head>"#,
            #"<script[^>]*>[\s\S]*?</script>"#,
            #"<style[^>]*>[\s\S]*?</style>"#
        ]
        let withoutNoise = removableBlocks.reduce(html) { partial, pattern in
            partial.replacingOccurrences(
                of: pattern,
                with: " ",
                options: [.regularExpression, .caseInsensitive]
            )
        }
        let content = firstHTMLBlock(named: "main", in: withoutNoise)
            ?? firstHTMLBlock(named: "article", in: withoutNoise)
            ?? firstHTMLBlock(named: "body", in: withoutNoise)
            ?? withoutNoise
        let withBreaks = content.replacingOccurrences(
            of: #"</?(p|div|section|article|main|h[1-6]|li|tr|br)[^>]*>"#,
            with: "\n",
            options: [.regularExpression, .caseInsensitive]
        )
        let withoutTags = withBreaks.replacingOccurrences(
            of: #"<[^>]+>"#,
            with: " ",
            options: .regularExpression
        )
        let decoded = decodeHTMLEntities(withoutTags)
            .replacingOccurrences(of: #"[ \t]+"#, with: " ", options: .regularExpression)
            .replacingOccurrences(of: #"\n\s*\n+"#, with: "\n", options: .regularExpression)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return decoded
    }

    private static func firstHTMLBlock(named tag: String, in html: String) -> String? {
        guard let regex = try? NSRegularExpression(
            pattern: "<\(tag)[^>]*>(.*?)</\(tag)>",
            options: [.caseInsensitive, .dotMatchesLineSeparators]
        ), let match = regex.firstMatch(in: html, range: NSRange(html.startIndex..., in: html)),
              let range = Range(match.range(at: 1), in: html) else { return nil }
        return String(html[range])
    }

    private static func decodeHTMLEntities(_ text: String) -> String {
        let newlineMarker = "__LLMTUI_NEWLINE__"
        let markedText = text.replacingOccurrences(of: "\n", with: newlineMarker)
        guard let data = markedText.data(using: .utf8),
              let decoded = try? NSAttributedString(
                data: data,
                options: [.documentType: NSAttributedString.DocumentType.html, .characterEncoding: String.Encoding.utf8.rawValue],
                documentAttributes: nil
              ).string else { return text }
        return decoded.replacingOccurrences(of: newlineMarker, with: "\n")
    }
}
