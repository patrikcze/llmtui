import SwiftUI

enum UsageRange: String, CaseIterable, Identifiable {
    case all, last30, last7

    var id: String { rawValue }

    var title: String {
        switch self {
        case .all: "All"
        case .last30: "30d"
        case .last7: "7d"
        }
    }

    func cutoff(calendar: Calendar = .current) -> Date? {
        switch self {
        case .all: nil
        case .last30: calendar.date(byAdding: .day, value: -30, to: calendar.startOfDay(for: Date()))
        case .last7: calendar.date(byAdding: .day, value: -7, to: calendar.startOfDay(for: Date()))
        }
    }
}

private enum UsageTab: String, CaseIterable, Identifiable {
    case overview, models
    var id: String { rawValue }
    var title: String { self == .overview ? "Overview" : "Models" }
}

struct UsageDashboardView: View {
    let model: AppModel
    @State private var tab: UsageTab = .overview
    @State private var range: UsageRange = .all

    private var snapshot: UsageSnapshot? { model.usageSnapshot }

    private var filteredRecords: [UsageRecord] {
        guard let snapshot else { return [] }
        guard let cutoff = range.cutoff() else { return snapshot.records }
        return snapshot.records.filter { $0.time >= cutoff }
    }

    private var filteredSessions: [SessionMeta] {
        guard let snapshot else { return [] }
        guard let cutoff = range.cutoff() else { return snapshot.sessions }
        return snapshot.sessions.filter { $0.savedAt >= cutoff }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Text("Usage").font(.headline)
                Spacer()
                if let snapshot {
                    Text("Response cache: \(snapshot.cacheEntryCount) entries · \(formatBytes(snapshot.cacheSizeBytes))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                Button("Refresh", systemImage: "arrow.clockwise") {
                    model.loadUsageSnapshot()
                }
                .labelStyle(.iconOnly)
                .buttonStyle(.borderless)
            }

            if model.isLoadingUsage && snapshot == nil {
                HStack {
                    Spacer()
                    ProgressView("Reading llmtui history…")
                    Spacer()
                }
                .padding(.vertical, 24)
            } else if let snapshot, snapshot.historyDirExists, !snapshot.records.isEmpty {
                HStack {
                    Picker("Tab", selection: $tab) {
                        ForEach(UsageTab.allCases) { Text($0.title).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .frame(maxWidth: 220)
                    Spacer()
                    Picker("Range", selection: $range) {
                        ForEach(UsageRange.allCases) { Text($0.title).tag($0) }
                    }
                    .pickerStyle(.segmented)
                    .labelsHidden()
                    .frame(maxWidth: 180)
                }

                switch tab {
                case .overview:
                    overviewTab(snapshot: snapshot)
                case .models:
                    modelsTab
                }
            } else {
                emptyState(snapshot: snapshot)
            }
        }
        .padding(20)
        .themedCard()
    }

    private func overviewTab(snapshot: UsageSnapshot) -> some View {
        let summary = usageSummary(for: filteredRecords)
        let models = aggregateByModel(filteredRecords)
        let totalTokens = filteredRecords.reduce(0) { $0 + $1.totalTokens }
        let favoriteModel = models.first?.label ?? "—"
        let peak = peakHour(filteredRecords).map(formatHour) ?? "—"
        let allTimeDays = aggregateByDay(snapshot.records)
        let streak = longestStreak(allTimeDays)
        let largest = largestSession(snapshot.sessions)

        return VStack(alignment: .leading, spacing: 16) {
            LazyVGrid(columns: [GridItem(.flexible()), GridItem(.flexible()), GridItem(.flexible())], spacing: 12) {
                UsageStatTile(title: "Sessions", value: "\(filteredSessions.count)")
                UsageStatTile(title: "Messages", value: "\(filteredRecords.count)")
                UsageStatTile(title: "Total tokens", value: formatTokenCount(totalTokens))
                UsageStatTile(title: "Active days", value: "\(summary.activeDays)")
                UsageStatTile(title: "Peak hour", value: peak)
                UsageStatTile(title: "Favorite model", value: favoriteModel, truncates: true)
            }

            UsageHeatmapView(dayTotals: aggregateByDay(filteredRecords))

            if streak > 0 || largest != nil {
                HStack(spacing: 16) {
                    if streak > 0 {
                        Text("Longest streak: \(streak) day\(streak == 1 ? "" : "s")")
                    }
                    if let largest {
                        Text("Largest session: \(formatTokenCount(largest.tokens)) tokens (\(formatShortDate(largest.date)))")
                    }
                }
                .font(.caption)
                .foregroundStyle(.secondary)
            }

            Text(hobbitComparison(totalTokens: totalTokens))
                .font(.caption)
                .foregroundStyle(.secondary)
        }
    }

    private var modelsTab: some View {
        let models = aggregateByModel(filteredRecords)
        let grandTotal = max(models.reduce(0) { $0 + $1.totalTokens }, 1)
        return VStack(alignment: .leading, spacing: 10) {
            if models.isEmpty {
                Text("No requests in this range.").foregroundStyle(.secondary)
            } else {
                ForEach(Array(models.enumerated()), id: \.element.id) { index, usage in
                    UsageModelRow(rank: index, usage: usage, grandTotal: grandTotal)
                }
            }
        }
    }

    private func emptyState(snapshot: UsageSnapshot?) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("No usage recorded yet")
                .font(.subheadline.weight(.medium))
            Text(
                snapshot?.historyDirExists == false
                    ? "llmtui hasn't created its history directory yet. Chat with llmtui (with chat.save_history enabled) to see activity here."
                    : "The history directory exists but has no recorded requests yet."
            )
            .font(.caption)
            .foregroundStyle(.secondary)
            Text(model.configuration.rawSettings["chat.history_dir"] ?? "~/.local/share/llmtui/history")
                .font(.caption)
                .foregroundStyle(.secondary)
                .textSelection(.enabled)
        }
        .padding(.vertical, 12)
    }
}

private struct UsageStatTile: View {
    let title: String
    let value: String
    var truncates: Bool = false

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title).font(.caption).foregroundStyle(.secondary)
            Text(value)
                .font(.title3.weight(.semibold))
                .lineLimit(1)
                .truncationMode(truncates ? .middle : .tail)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(.quaternary.opacity(0.4), in: RoundedRectangle(cornerRadius: 10))
    }
}

private struct UsageModelRow: View {
    let rank: Int
    let usage: ModelUsage
    let grandTotal: Int

    private static let dotColors: [Color] = [.orange, .green, .blue, .purple, .yellow, .gray]

    private var percent: Double { Double(usage.totalTokens) / Double(grandTotal) * 100 }

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Circle()
                    .fill(Self.dotColors[rank % Self.dotColors.count])
                    .frame(width: 8, height: 8)
                Text(usage.label)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Spacer()
                Text(String(format: "%.1f%%", percent))
                    .foregroundStyle(.secondary)
            }
            Text("in: \(formatTokenCount(usage.promptTokens)) · out: \(formatTokenCount(usage.completionTokens)) · \(usage.requests) requests")
                .font(.caption)
                .foregroundStyle(.secondary)
                .padding(.leading, 14)
        }
    }
}

private struct UsageHeatmapView: View {
    let dayTotals: [DayTotal]
    @Environment(\.colorScheme) private var colorScheme
    @State private var hoveredDate: Date?
    private let calendar = Calendar.current

    private var maxTokens: Int { dayTotals.map(\.tokens).max() ?? 0 }

    private var totalsByDay: [Date: Int] {
        Dictionary(uniqueKeysWithValues: dayTotals.map { ($0.day, $0.tokens) })
    }

    private var gridStart: Date? {
        guard let first = dayTotals.first?.day else { return nil }
        let firstWeekday = calendar.component(.weekday, from: first)
        return calendar.date(byAdding: .day, value: -(firstWeekday - 1), to: first)
    }

    private var weeks: [[DayTotal?]] {
        guard let gridStart, let last = dayTotals.last?.day else { return [] }
        var columns: [[DayTotal?]] = []
        var cursor = gridStart
        while cursor <= last {
            var column: [DayTotal?] = []
            for _ in 0..<7 {
                column.append(totalsByDay[cursor].map { DayTotal(day: cursor, tokens: $0) })
                cursor = calendar.date(byAdding: .day, value: 1, to: cursor) ?? cursor
            }
            columns.append(column)
        }
        return columns
    }

    var body: some View {
        if weeks.isEmpty {
            EmptyView()
        } else {
            GeometryReader { geometry in
                let spacing: CGFloat = 4
                let cellSide = max(1, (geometry.size.width - CGFloat(weeks.count - 1) * spacing) / CGFloat(weeks.count))

                ZStack(alignment: .topLeading) {
                    HStack(alignment: .top, spacing: spacing) {
                        ForEach(weeks.indices, id: \.self) { weekIndex in
                            VStack(spacing: spacing) {
                                ForEach(0..<7, id: \.self) { dayIndex in
                                    let date = date(for: weekIndex, dayIndex: dayIndex)
                                    cell(for: weeks[weekIndex][dayIndex], date: date, side: cellSide)
                                }
                            }
                            .frame(width: cellSide)
                        }
                    }

                    if let hoveredDate {
                        Text(hoverText(for: hoveredDate))
                            .font(.caption)
                            .foregroundStyle(.primary)
                            .padding(.horizontal, 8)
                            .padding(.vertical, 5)
                            .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 6))
                            .shadow(radius: 4, y: 2)
                            .allowsHitTesting(false)
                            .offset(y: -30)
                    }
                }
            }
            .aspectRatio(CGFloat(weeks.count) / 7, contentMode: .fit)
        }
    }

    private func date(for weekIndex: Int, dayIndex: Int) -> Date {
        guard let gridStart else { return Date.distantPast }
        return calendar.date(byAdding: .day, value: weekIndex * 7 + dayIndex, to: gridStart) ?? gridStart
    }

    private func cell(for day: DayTotal?, date: Date, side: CGFloat) -> some View {
        RoundedRectangle(cornerRadius: min(5, side * 0.18))
            .fill(color(for: day?.tokens ?? 0))
            .frame(width: side, height: side)
            .contentShape(Rectangle())
            .onHover { isHovered in
                hoveredDate = isHovered ? date : nil
            }
            .help(hoverText(for: date))
    }

    private func hoverText(for date: Date) -> String {
        let tokens = totalsByDay[calendar.startOfDay(for: date)] ?? 0
        return "\(formatHeatmapDate(date)) — \(tokens.formatted()) tokens"
    }

    // Steps are the validated sequential-blue ramp from the dataviz skill's
    // reference palette (references/palette.md). Dark mode walks the ramp
    // in the opposite direction (dark→light as intensity increases) so a
    // high-usage day still pops against the near-black surface instead of
    // blending into it the way the light-mode ordering would.
    private func color(for tokens: Int) -> Color {
        guard tokens > 0, maxTokens > 0 else {
            return colorScheme == .dark ? Color(hex: "#2c2c2a") : Color(hex: "#e1e0d9")
        }
        let lightSteps = ["#9ec5f4", "#5598e7", "#256abf", "#104281"]
        let darkSteps = ["#104281", "#1c5cab", "#3987e5", "#86b6ef"]
        let steps = colorScheme == .dark ? darkSteps : lightSteps
        let ratio = Double(tokens) / Double(maxTokens)
        let index = min(steps.count - 1, Int(ratio * Double(steps.count)))
        return Color(hex: steps[index])
    }
}

private func formatHeatmapDate(_ date: Date) -> String {
    let formatter = DateFormatter()
    formatter.dateFormat = "MMM d, yyyy"
    return formatter.string(from: date)
}

private extension Color {
    init(hex: String) {
        var value: UInt64 = 0
        Scanner(string: hex.trimmingCharacters(in: CharacterSet(charactersIn: "#"))).scanHexInt64(&value)
        let r = Double((value >> 16) & 0xFF) / 255
        let g = Double((value >> 8) & 0xFF) / 255
        let b = Double(value & 0xFF) / 255
        self = Color(red: r, green: g, blue: b)
    }
}

private func formatTokenCount(_ value: Int) -> String {
    let n = Double(value)
    switch abs(n) {
    case 1_000_000...:
        return String(format: "%.1fM", n / 1_000_000)
    case 1_000...:
        return String(format: "%.1fk", n / 1_000)
    default:
        return "\(value)"
    }
}

private func formatBytes(_ bytes: Int64) -> String {
    ByteCountFormatter.string(fromByteCount: bytes, countStyle: .file)
}

private func formatHour(_ hour: Int) -> String {
    let date = Calendar.current.date(bySettingHour: hour, minute: 0, second: 0, of: Date()) ?? Date()
    let formatter = DateFormatter()
    formatter.dateFormat = "h a"
    return formatter.string(from: date)
}

private func formatShortDate(_ date: Date) -> String {
    let formatter = DateFormatter()
    formatter.dateFormat = "MMM d"
    return formatter.string(from: date)
}

/// A playful comparison, not an `llmtui` stat — The Hobbit is ~95,356 words,
/// approximated here at ~1.3 tokens/word (~127,000 tokens).
private func hobbitComparison(totalTokens: Int) -> String {
    let hobbitTokens = 127_000
    guard totalTokens > 0 else { return "Start chatting to see how your token usage stacks up." }
    let multiple = max(1, totalTokens / hobbitTokens)
    return "You've used ~\(multiple)× more tokens than The Hobbit."
}

#Preview {
    let model = AppModel()
    model.loadConfiguration()
    model.loadUsageSnapshot()
    return UsageDashboardView(model: model)
        .frame(width: 700)
        .padding()
}
