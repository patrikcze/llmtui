import SwiftUI

/// The ∞ agent run behind a reply: phase and pass, the goal, the completion
/// criteria ticked off by the last check, and every check's findings.
struct AgentRunCard: View {
    let run: MobileAgentRun
    @State private var showChecks = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 10) {
                IconTile(systemName: "infinity", tint: tint, size: 30)
                VStack(alignment: .leading, spacing: 1) {
                    Text("Agent")
                        .font(.subheadline.weight(.semibold))
                    Text(status)
                        .font(.caption)
                        .foregroundStyle(tint)
                }
                Spacer()
                if run.phase != .finished {
                    ProgressView()
                        .controlSize(.small)
                }
            }

            if let plan = run.plan {
                Text(plan.goal)
                    .font(.footnote)
                    .foregroundStyle(.secondary)
                VStack(alignment: .leading, spacing: 6) {
                    ForEach(Array(plan.criteria.enumerated()), id: \.offset) { index, criterion in
                        let met = run.met.contains(index + 1)
                        Label {
                            Text(criterion)
                                .font(.footnote)
                                .foregroundStyle(met ? .primary : .secondary)
                        } icon: {
                            Image(systemName: met ? "checkmark.circle.fill" : "circle")
                                .foregroundStyle(met ? Theme.success : Color.secondary)
                        }
                    }
                }
                .accessibilityElement(children: .combine)
                .accessibilityLabel("Done when")
            }

            if !run.checks.isEmpty {
                DisclosureGroup(isExpanded: $showChecks) {
                    VStack(alignment: .leading, spacing: 6) {
                        ForEach(run.checks, id: \.pass) { check in
                            Text(checkText(check))
                                .font(.caption)
                                .foregroundStyle(.secondary)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }
                    .padding(.top, 4)
                } label: {
                    Text(run.checks.count == 1 ? "1 check" : "\(run.checks.count) checks")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(.secondary)
                }
                .tint(.secondary)
            }

            if let reason = run.stopReason, reason != .verified {
                Text(reason.summary)
                    .font(.caption)
                    .foregroundStyle(reason == .cancelled ? Color.secondary : Theme.warning)
            }
        }
        .padding(12)
        .background(Theme.raisedSurface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
    }

    private var status: String {
        switch run.phase {
        case .planning: "Planning…"
        case .acting: "Pass \(run.pass) of \(run.maxPasses) · working"
        case .verifying: "Pass \(run.pass) of \(run.maxPasses) · checking the answer"
        case .finished:
            switch run.stopReason {
            case .verified?: run.pass == 1 ? "Done · checked" : "Done · checked after \(run.pass) passes"
            case .cancelled?: "Stopped"
            default: "Finished without full confirmation"
            }
        }
    }

    private var tint: Color {
        switch run.phase {
        case .finished: run.stopReason == .verified ? Theme.success : (run.stopReason == .cancelled ? .secondary : Theme.warning)
        default: Theme.accent
        }
    }

    private func checkText(_ check: MobileAgentRun.Check) -> String {
        if check.complete { return "Pass \(check.pass): every criterion met." }
        let missing = check.missing.isEmpty ? "no details" : check.missing.joined(separator: "; ")
        return "Pass \(check.pass): missing \(missing)."
    }
}
