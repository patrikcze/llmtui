import Foundation

/// Mirrors llmtui's `internal/history.UsageRecord` — one line in
/// `<history_dir>/usage.jsonl` per completed request.
struct UsageRecord: Decodable {
    let time: Date
    let provider: String
    let model: String
    let promptTokens: Int
    let completionTokens: Int

    private enum CodingKeys: String, CodingKey {
        case time, provider, model
        case promptTokens = "prompt_tokens"
        case completionTokens = "completion_tokens"
    }

    var totalTokens: Int { promptTokens + completionTokens }
    var modelKey: String { "\(provider)/\(model)" }
}

/// Mirrors the fields llmtui's `internal/history.Session` stores per saved
/// chat transcript (`<history_dir>/session-*.json`) — the `messages` array
/// is deliberately omitted; `Decodable` simply skips keys we don't declare.
struct SessionMeta: Decodable {
    let savedAt: Date
    let provider: String
    let model: String
    let promptTokens: Int
    let completionTokens: Int

    private enum CodingKeys: String, CodingKey {
        case savedAt = "saved_at"
        case provider, model
        case promptTokens = "prompt_tokens"
        case completionTokens = "completion_tokens"
    }

    var totalTokens: Int { promptTokens + completionTokens }
}

struct ModelUsage: Identifiable {
    let id: String
    let label: String
    let promptTokens: Int
    let completionTokens: Int
    let requests: Int
    var totalTokens: Int { promptTokens + completionTokens }
}

struct DayTotal: Identifiable {
    let day: Date
    let tokens: Int
    var id: Date { day }
}

struct UsageSummary {
    let activeDays: Int
    let mostActiveDay: (day: Date, tokens: Int)?
    let currentStreak: Int
}

struct UsageSnapshot {
    let records: [UsageRecord]
    let sessions: [SessionMeta]
    let cacheEntryCount: Int
    let cacheSizeBytes: Int64
    let historyDirExists: Bool
}

enum UsageStore {
    /// Go's `time.Time` marshals as RFC3339 with nanosecond-precision
    /// fractional seconds when nonzero — `JSONDecoder`'s plain `.iso8601`
    /// strategy can't parse that, so try with fractional seconds first,
    /// then without.
    private static let dateDecoder: JSONDecoder = {
        let withFractional = ISO8601DateFormatter()
        withFractional.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        let plain = ISO8601DateFormatter()
        plain.formatOptions = [.withInternetDateTime]
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .custom { decoder in
            let container = try decoder.singleValueContainer()
            let string = try container.decode(String.self)
            if let date = withFractional.date(from: string) { return date }
            if let date = plain.date(from: string) { return date }
            throw DecodingError.dataCorruptedError(in: container, debugDescription: "Unrecognized date: \(string)")
        }
        return decoder
    }()

    static func load(historyDir: String, cachePath: String) async -> UsageSnapshot {
        let expandedHistory = (historyDir as NSString).expandingTildeInPath
        let expandedCache = (cachePath as NSString).expandingTildeInPath
        let fm = FileManager.default

        var isDir: ObjCBool = false
        let historyDirExists = fm.fileExists(atPath: expandedHistory, isDirectory: &isDir) && isDir.boolValue

        var records: [UsageRecord] = []
        let usageFileURL = URL(fileURLWithPath: expandedHistory).appendingPathComponent("usage.jsonl")
        if let data = try? Data(contentsOf: usageFileURL), let text = String(data: data, encoding: .utf8) {
            for line in text.split(separator: "\n") {
                guard let lineData = line.data(using: .utf8),
                      let record = try? dateDecoder.decode(UsageRecord.self, from: lineData) else { continue }
                records.append(record)
            }
        }

        var sessions: [SessionMeta] = []
        if let entries = try? fm.contentsOfDirectory(at: URL(fileURLWithPath: expandedHistory), includingPropertiesForKeys: nil) {
            for url in entries where url.lastPathComponent.hasPrefix("session-") && url.pathExtension == "json" {
                guard let data = try? Data(contentsOf: url),
                      let meta = try? dateDecoder.decode(SessionMeta.self, from: data) else { continue }
                sessions.append(meta)
            }
        }

        var cacheEntryCount = 0
        var cacheSizeBytes: Int64 = 0
        if let entries = try? fm.contentsOfDirectory(at: URL(fileURLWithPath: expandedCache), includingPropertiesForKeys: [.fileSizeKey]) {
            for url in entries where url.pathExtension == "json" {
                cacheEntryCount += 1
                if let size = try? url.resourceValues(forKeys: [.fileSizeKey]).fileSize {
                    cacheSizeBytes += Int64(size)
                }
            }
        }

        return UsageSnapshot(
            records: records,
            sessions: sessions,
            cacheEntryCount: cacheEntryCount,
            cacheSizeBytes: cacheSizeBytes,
            historyDirExists: historyDirExists
        )
    }
}

func aggregateByDay(_ records: [UsageRecord], calendar: Calendar = .current) -> [DayTotal] {
    var totals: [Date: Int] = [:]
    for record in records {
        let day = calendar.startOfDay(for: record.time)
        totals[day, default: 0] += record.totalTokens
    }
    return totals.map { DayTotal(day: $0.key, tokens: $0.value) }.sorted { $0.day < $1.day }
}

func aggregateByModel(_ records: [UsageRecord]) -> [ModelUsage] {
    struct Accumulator { var prompt = 0; var completion = 0; var requests = 0 }
    var byKey: [String: Accumulator] = [:]
    for record in records {
        var acc = byKey[record.modelKey] ?? Accumulator()
        acc.prompt += record.promptTokens
        acc.completion += record.completionTokens
        acc.requests += 1
        byKey[record.modelKey] = acc
    }
    return byKey.map { key, acc in
        ModelUsage(id: key, label: key, promptTokens: acc.prompt, completionTokens: acc.completion, requests: acc.requests)
    }.sorted { $0.totalTokens > $1.totalTokens }
}

func longestStreak(_ dayTotals: [DayTotal], calendar: Calendar = .current) -> Int {
    let activeDays = Set(dayTotals.filter { $0.tokens > 0 }.map(\.day)).sorted()
    guard !activeDays.isEmpty else { return 0 }
    var longest = 1
    var current = 1
    for index in 1..<activeDays.count {
        if let next = calendar.date(byAdding: .day, value: 1, to: activeDays[index - 1]), next == activeDays[index] {
            current += 1
            longest = max(longest, current)
        } else {
            current = 1
        }
    }
    return longest
}

func usageSummary(for records: [UsageRecord], calendar: Calendar = .current) -> UsageSummary {
    let dayTotals = aggregateByDay(records, calendar: calendar)
    let byDay = Dictionary(uniqueKeysWithValues: dayTotals.map { ($0.day, $0.tokens) })
    let activeDays = dayTotals.filter { $0.tokens > 0 }.count
    let mostActive = dayTotals.max { $0.tokens < $1.tokens }.map { (day: $0.day, tokens: $0.tokens) }

    var cursor = calendar.startOfDay(for: Date())
    if (byDay[cursor] ?? 0) == 0 {
        cursor = calendar.date(byAdding: .day, value: -1, to: cursor) ?? cursor
    }
    var streak = 0
    while (byDay[cursor] ?? 0) > 0 {
        streak += 1
        guard let prior = calendar.date(byAdding: .day, value: -1, to: cursor) else { break }
        cursor = prior
    }
    return UsageSummary(activeDays: activeDays, mostActiveDay: mostActive, currentStreak: streak)
}

func largestSession(_ sessions: [SessionMeta]) -> (tokens: Int, date: Date)? {
    guard let top = sessions.max(by: { $0.totalTokens < $1.totalTokens }) else { return nil }
    return (tokens: top.totalTokens, date: top.savedAt)
}

/// Not something llmtui's own `/usage` overlay computes — a bonus tile
/// derived from the same `UsageRecord.time` timestamps already on disk.
func peakHour(_ records: [UsageRecord], calendar: Calendar = .current) -> Int? {
    var totals: [Int: Int] = [:]
    for record in records {
        let hour = calendar.component(.hour, from: record.time)
        totals[hour, default: 0] += record.totalTokens
    }
    return totals.max { $0.value < $1.value }?.key
}

extension AppModel {
    func loadUsageSnapshot() {
        isLoadingUsage = true
        let historyDir = configuration.rawSettings["chat.history_dir"] ?? "~/.local/share/llmtui/history"
        let cachePath = configuration.rawSettings["cache.path"] ?? "~/.cache/llmtui/responses"
        Task {
            let snapshot = await UsageStore.load(historyDir: historyDir, cachePath: cachePath)
            usageSnapshot = snapshot
            isLoadingUsage = false
        }
    }
}
