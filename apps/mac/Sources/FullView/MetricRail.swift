import SwiftUI

/// One button for each metric: value at the cursor, sub-label and sparkline.
struct MetricRail: View {
    let vm: ServerViewModel
    let frame: WorkbenchFrame
    let score: HealthScore
    let window: ClosedRange<Date>
    let cursor: Date?
    @Binding var selection: RailMetric

    var body: some View {
        ScrollView {
            VStack(spacing: 8) {
                ForEach(RailMetric.allCases) { m in tile(m) }
            }
            .padding(.vertical, 2)
        }
        .scrollIndicators(.hidden)
        .frame(width: 214)
    }

    private func tile(_ m: RailMetric) -> some View {
        let on = selection == m
        return Button {
            selection = m
        } label: {
            VStack(alignment: .leading, spacing: 3) {
                HStack(alignment: .firstTextBaseline) {
                    KLabel(m.displayName)
                    Spacer(minLength: 4)
                    value(m)
                }
                Text(sub(m))
                    .font(.system(size: 11, design: .monospaced))
                    .foregroundStyle(WB.faint)
                    .lineLimit(1)
                spark(m)
                    .frame(height: 26)
                    .overlay(alignment: .leading) { cursorLine }
            }
            .padding(.horizontal, 11).padding(.top, 9).padding(.bottom, 7)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(on ? WB.surface : WB.sidebar, in: RoundedRectangle(cornerRadius: 10))
            .overlay(RoundedRectangle(cornerRadius: 10).stroke(on ? Color.primary : WB.border, lineWidth: 1))
            .contentShape(RoundedRectangle(cornerRadius: 10))
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(on ? .isSelected : [])
    }

    @ViewBuilder
    private func value(_ m: RailMetric) -> some View {
        if m.hasTwoSeries {
            HStack(spacing: 6) {
                ForEach(Array((frame.series[m] ?? []).enumerated()), id: \.offset) { i, s in
                    Text(frame.value(m, i, at: cursor).map(m.shortFormat) ?? "—")
                        .foregroundStyle(s.color)
                }
            }
            .font(.system(size: 13, weight: .semibold).monospacedDigit())
        } else {
            let v = frame.value(m, at: cursor)
            Text(v.map(m.format) ?? "—")
                .font(.system(size: 15, weight: .semibold).monospacedDigit())
                .foregroundStyle(v.map { m.tint(of: $0, thresholds: vm.thresholds).valueColor } ?? WB.faint)
        }
    }

    private func sub(_ m: RailMetric) -> String {
        switch m {
        case .cpu: return vm.cpuCores.map { "of \($0) cores" } ?? "busy"
        case .mem:
            return vm.memTotalBytes.map { "of " + ByteCountFormatter.string(fromByteCount: $0, countStyle: .memory) } ?? "used"
        case .load: return "1 min average"
        case .disk: return "worst mount"
        case .net: return "in · out"
        case .io: return "read · write"
        case .health: return "worst: " + (score.worst?.label.lowercased() ?? "none")
        }
    }

    @ViewBuilder
    private func spark(_ m: RailMetric) -> some View {
        let all = frame.series[m] ?? []
        let peak = all.flatMap(\.points).map(\.v).max() ?? 0
        let domain = m.yDomain ?? 0...max(peak * 1.15, 0.0001)
        ZStack {
            ForEach(Array(all.enumerated()), id: \.offset) { i, s in
                CanvasSparkline(
                    samples: s.points, tint: s.color, warn: nil,
                    windowSeconds: window.upperBound.timeIntervalSince(window.lowerBound),
                    yDomain: domain, height: 26, showsArea: i == 0, xDomain: window
                )
            }
        }
    }

    @ViewBuilder
    private var cursorLine: some View {
        if let cursor {
            GeometryReader { geo in
                let span = window.upperBound.timeIntervalSince(window.lowerBound)
                let f = span > 0 ? cursor.timeIntervalSince(window.lowerBound) / span : 0
                if f >= 0, f <= 1 {
                    Rectangle().fill(Color.primary.opacity(0.5))
                        .frame(width: 1)
                        .offset(x: geo.size.width * f)
                }
            }
            .allowsHitTesting(false)
        }
    }
}

extension RailMetric {
    /// Value text of a tile with two numbers.
    func shortFormat(_ v: Double) -> String {
        ByteFormat.rate(bytesPerSec: v * 1_048_576)
            .replacingOccurrences(of: "/s", with: "")
    }
}
