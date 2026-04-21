import SwiftUI
import Charts

struct DiskBars: View {
    let samples: [MetricPoint]
    let tint: Color
    var slots: Int = 16

    var body: some View {
        let recent = Array(samples.suffix(slots))
        let padCount = max(0, slots - recent.count)
        Chart {
            ForEach(0..<padCount, id: \.self) { idx in
                BarMark(
                    x: .value("i", idx),
                    y: .value("v", 0.0)
                )
                .foregroundStyle(Color.clear)
            }
            ForEach(Array(recent.enumerated()), id: \.offset) { idx, p in
                BarMark(
                    x: .value("i", padCount + idx),
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
        .chartXScale(domain: 0...(slots - 1))
        .frame(height: 28)
        .drawingGroup()
    }
}
