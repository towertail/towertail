import SwiftUI
import Charts

struct MetricChart: View {
    let samples: [MetricPoint]
    let tint: Color
    let warn: Double?
    let critical: Double?
    let hoverAt: Date?
    let pinnedAt: Date?
    var onHover: ((Date?) -> Void)? = nil
    var onPinTap: ((Date) -> Void)? = nil

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
        .chartYScale(domain: 0...1)
        .chartXAxis {
            AxisMarks(values: .automatic(desiredCount: 5))
        }
        .chartYAxis {
            AxisMarks(values: .automatic(desiredCount: 4)) { value in
                AxisGridLine()
                AxisValueLabel {
                    if let v = value.as(Double.self) {
                        Text("\(Int(v * 100))%")
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
                            let plotFrame = proxy.plotFrame.map { geo[$0] } ?? .zero
                            let x = point.x - plotFrame.origin.x
                            if let t: Date = proxy.value(atX: x) {
                                onHover?(t)
                            }
                        case .ended:
                            onHover?(nil)
                        }
                    }
                    .onTapGesture { location in
                        let plotFrame = proxy.plotFrame.map { geo[$0] } ?? .zero
                        let x = location.x - plotFrame.origin.x
                        if let t: Date = proxy.value(atX: x) {
                            onPinTap?(t)
                        }
                    }
            }
        }
    }
}
