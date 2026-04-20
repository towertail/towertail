import SwiftUI
import Charts

struct DiskBars: View {
    let samples: [MetricPoint]
    let tint: Color

    var body: some View {
        let recent = Array(samples.suffix(16))
        Chart {
            ForEach(Array(recent.enumerated()), id: \.offset) { idx, p in
                BarMark(
                    x: .value("i", idx),
                    y: .value("v", max(p.v, 0.02))
                )
                .foregroundStyle(tint)
                .cornerRadius(1)
            }
        }
        .chartXAxis(.hidden)
        .chartYAxis(.hidden)
        .chartPlotStyle { $0.background(.clear) }
        .chartYScale(domain: 0...1)
        .frame(height: 28)
        .drawingGroup()
    }
}
