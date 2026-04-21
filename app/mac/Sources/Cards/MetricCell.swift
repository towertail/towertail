import SwiftUI

enum MetricCellMode {
    case percent
    case netDualRate
    case diskBars
}

struct MetricCell: View {
    let label: String
    let series: MetricSeries
    let mode: MetricCellMode
    let offline: Bool
    let warn: Double
    let critical: Double
    var rxMBps: Double = 0
    var txMBps: Double = 0
    var hoverValue: Double? = nil
    var pollingIntervalSeconds: Int = 15
    var sparklineSlots: Int = 60

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(alignment: .firstTextBaseline, spacing: 0) {
                Text(label.uppercased())
                    .font(Typography.cellLabel)
                    .tracking(0.8)
                    .foregroundStyle(.secondary)
                Spacer(minLength: 4)
                valueView
            }
            chartView
                .frame(maxWidth: .infinity)
        }
        .padding(10)
        .frame(height: 86)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(
            RoundedRectangle(cornerRadius: 6, style: .continuous)
                .fill(Color.black.opacity(0.12))
        )
    }

    @ViewBuilder private var valueView: some View {
        if offline {
            Text("—")
                .font(Typography.bigNumber)
                .foregroundStyle(.secondary)
        } else {
            switch mode {
            case .percent, .diskBars:
                let v = hoverValue ?? series.latest?.v ?? 0
                Text("\(Int(round(v * 100)))%")
                    .font(Typography.bigNumber)
                    .foregroundStyle(Color.threshold(v, warn: warn, critical: critical))
            case .netDualRate:
                HStack(spacing: 6) {
                    HStack(spacing: 1) {
                        Text("↓").foregroundStyle(Color.blue)
                        Text(String(format: "%.1f", rxMBps)).foregroundStyle(Color.blue)
                    }
                    HStack(spacing: 1) {
                        Text("↑").foregroundStyle(Color("Tint/Critical"))
                        Text(String(format: "%.1f", txMBps)).foregroundStyle(Color("Tint/Critical"))
                    }
                }
                .font(Typography.netRate)
            }
        }
    }

    @ViewBuilder private var chartView: some View {
        if offline {
            switch mode {
            case .diskBars:
                emptyBars
            default:
                Rectangle()
                    .fill(Color.secondary.opacity(0.08))
                    .frame(height: 28)
                    .cornerRadius(2)
            }
        } else {
            let points = series.snapshot()
            let windowSeconds = TimeInterval(max(1, pollingIntervalSeconds) * sparklineSlots)
            switch mode {
            case .percent:
                Sparkline(samples: points, tint: seriesTint, warn: warn, windowSeconds: windowSeconds)
            case .netDualRate:
                Sparkline(samples: points, tint: ThresholdTint.nominal.color, warn: nil, windowSeconds: windowSeconds)
            case .diskBars:
                DiskBars(samples: points, tint: seriesTint, slots: sparklineSlots)
            }
        }
    }

    private var emptyBars: some View {
        HStack(spacing: 2) {
            ForEach(0..<14, id: \.self) { _ in
                RoundedRectangle(cornerRadius: 1)
                    .fill(Color.secondary.opacity(0.12))
                    .frame(width: 6, height: 10)
            }
        }
        .frame(height: 28, alignment: .bottomLeading)
    }

    private var seriesTint: Color {
        series.tint(warn: warn, critical: critical).color
    }
}
