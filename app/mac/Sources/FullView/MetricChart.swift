import SwiftUI
import Charts

struct MetricChart: View {
    let samples: [MetricPoint]
    let tint: Color
    let warn: Double?
    let critical: Double?
    let hoverAt: Date?
    let pinnedAt: Date?
    /// When nil the Y axis auto-scales to the data's peak (+15%) so
    /// low-magnitude metrics like NET remain legible.
    var yDomain: ClosedRange<Double>? = 0...1
    /// Formats the Y axis tick labels. Defaults to a 0–100% formatter.
    var yAxisLabel: (Double) -> String = { v in "\(Int(v * 100))%" }
    var onHover: ((Date?) -> Void)? = nil
    var onPinTap: ((Date) -> Void)? = nil

    @State private var lastHoverEmit: Date = .distantPast

    var body: some View {
        Chart(samples) { p in
            AreaMark(x: .value("t", p.t), y: .value("v", p.v))
                .foregroundStyle(LinearGradient(
                    colors: [tint.opacity(0.35), tint.opacity(0.02)],
                    startPoint: .top, endPoint: .bottom))
                .interpolationMethod(.monotone)
            LineMark(x: .value("t", p.t), y: .value("v", p.v))
                .foregroundStyle(tint)
                .lineStyle(.init(lineWidth: 1.4))
                .interpolationMethod(.monotone)
            if let warn {
                RuleMark(y: .value("warn", warn))
                    .foregroundStyle(Color.orange.opacity(0.35))
                    .lineStyle(.init(lineWidth: 0.8, dash: [3, 3]))
            }
            if let critical {
                RuleMark(y: .value("critical", critical))
                    .foregroundStyle(Color.red.opacity(0.35))
                    .lineStyle(.init(lineWidth: 0.8, dash: [3, 3]))
            }
            if let pinnedAt {
                RuleMark(x: .value("pin", pinnedAt))
                    .foregroundStyle(Color.orange)
                    .lineStyle(.init(lineWidth: 1.2))
            }
            if let hoverAt {
                RuleMark(x: .value("hover", hoverAt))
                    .foregroundStyle(Color.primary.opacity(0.35))
                    .lineStyle(.init(lineWidth: 0.6, dash: [2, 2]))
            }
        }
        .chartYScale(domain: effectiveYDomain)
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 5))
        }
        .chartYAxis {
            AxisMarks(values: .automatic(desiredCount: 4)) { value in
                AxisGridLine()
                AxisValueLabel {
                    if let v = value.as(Double.self) {
                        Text(yAxisLabel(v))
                    }
                }
            }
        }
        .chartOverlay { proxy in
            GeometryReader { geo in
                Rectangle().fill(.clear).contentShape(.rect)
                    .onContinuousHover { phase in
                        switch phase {
                        case .active(let point):
                            let now = Date()
                            guard now.timeIntervalSince(lastHoverEmit) >= 1.0 / 30.0 else { return }
                            lastHoverEmit = now
                            let plotFrame = proxy.plotFrame.map { geo[$0] } ?? .zero
                            let x = point.x - plotFrame.origin.x
                            if let t: Date = proxy.value(atX: x) {
                                onHover?(clampToSamples(t))
                            }
                        case .ended:
                            lastHoverEmit = .distantPast
                            onHover?(nil)
                        }
                    }
                    .onTapGesture { location in
                        let plotFrame = proxy.plotFrame.map { geo[$0] } ?? .zero
                        let x = location.x - plotFrame.origin.x
                        if let t: Date = proxy.value(atX: x) {
                            onPinTap?(clampToSamples(t))
                        }
                    }
            }
        }
    }

    /// Swift Charts auto-pads the X-axis domain to fill the plot width, so
    /// `proxy.value(atX:)` at the right edge returns a timestamp past the
    /// last ingested sample. Clamp to the actual sample range so the
    /// hover/pin time never drifts into the future (or before the buffer).
    private func clampToSamples(_ t: Date) -> Date {
        guard let first = samples.first?.t, let last = samples.last?.t else { return t }
        if t < first { return first }
        if t > last { return last }
        return t
    }

    private var effectiveYDomain: ClosedRange<Double> {
        if let fixed = yDomain { return fixed }
        let peak = samples.map(\.v).max() ?? 0
        let ceiling = max(peak * 1.15, 0.0001)
        return 0...ceiling
    }
}
