import SwiftUI
import Charts

struct Sparkline: View {
    let samples: [MetricPoint]
    let tint: Color
    let warn: Double?
    let windowSeconds: TimeInterval
    /// When nil, the Y axis auto-scales to the visible window's peak so
    /// low-magnitude activity is still legible (used by the NET sparkline).
    var yDomain: ClosedRange<Double>? = 0...1
    var onHover: ((Date?) -> Void)? = nil

    @State private var lastHoverEmit: Date = .distantPast

    var body: some View {
        Chart(samples) { s in
            AreaMark(x: .value("t", s.t), y: .value("v", s.v))
                .foregroundStyle(LinearGradient(
                    colors: [tint.opacity(0.35), tint.opacity(0.02)],
                    startPoint: .top, endPoint: .bottom))
                .interpolationMethod(.monotone)
            LineMark(x: .value("t", s.t), y: .value("v", s.v))
                .foregroundStyle(tint)
                .lineStyle(.init(lineWidth: 1.2))
                .interpolationMethod(.monotone)
            if let warn {
                RuleMark(y: .value("warn", warn))
                    .foregroundStyle(tint.opacity(0.15))
                    .lineStyle(.init(lineWidth: 0.5, dash: [2, 2]))
            }
        }
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartPlotStyle { $0.background(.clear) }
        .chartYScale(domain: effectiveYDomain)
        .chartXScale(domain: xDomain)
        .frame(height: 28)
        .drawingGroup()
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
            }
        }
    }

    /// Clamp a hover timestamp to the first/last sample so the right-edge
    /// readout never drifts past "now" (Charts extrapolates a few pixels
    /// of padding into the future otherwise).
    private func clampToSamples(_ t: Date) -> Date {
        guard let first = samples.first?.t, let last = samples.last?.t else { return t }
        if t < first { return first }
        if t > last { return last }
        return t
    }

    private var xDomain: ClosedRange<Date> {
        let latest = samples.last?.t ?? Date()
        let earliest = latest.addingTimeInterval(-max(1, windowSeconds))
        return earliest...latest
    }

    /// Auto-scale to the peak of the visible window (+15% headroom) when the
    /// caller passes `yDomain: nil`. Keeps a small floor so a flat-zero line
    /// still renders a visible baseline instead of vanishing.
    private var effectiveYDomain: ClosedRange<Double> {
        if let fixed = yDomain { return fixed }
        let x = xDomain
        let visible = samples.filter { $0.t >= x.lowerBound && $0.t <= x.upperBound }
        let peak = visible.map(\.v).max() ?? 0
        let ceiling = max(peak * 1.15, 0.0001)
        return 0...ceiling
    }
}
