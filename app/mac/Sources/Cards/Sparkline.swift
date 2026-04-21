import SwiftUI
import Charts

struct Sparkline: View {
    let samples: [MetricPoint]
    let tint: Color
    let warn: Double?
    let windowSeconds: TimeInterval
    var onHover: ((Date?) -> Void)? = nil

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
        .chartYScale(domain: 0...1)
        .chartXScale(domain: xDomain)
        .frame(height: 28)
        .drawingGroup()
        .chartOverlay { proxy in
            GeometryReader { geo in
                Rectangle().fill(.clear).contentShape(.rect)
                    .onContinuousHover { phase in
                        switch phase {
                        case .active(let point):
                            let plotFrame = proxy.plotFrame.map { geo[$0] } ?? .zero
                            let x = point.x - plotFrame.origin.x
                            if let t: Date = proxy.value(atX: x) {
                                onHover?(t)
                            }
                        case .ended:
                            onHover?(nil)
                        }
                    }
            }
        }
    }

    private var xDomain: ClosedRange<Date> {
        let latest = samples.last?.t ?? Date()
        let earliest = latest.addingTimeInterval(-max(1, windowSeconds))
        return earliest...latest
    }
}
