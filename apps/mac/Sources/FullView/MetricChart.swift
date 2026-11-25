import SwiftUI
import Charts

/// One line of a chart. The first series of a chart is also filled.
struct ChartSeries: Identifiable, Equatable {
    let name: String
    let color: Color
    let points: [MetricPoint]
    var id: String { name }
}

struct MetricChart: View {
    let series: [ChartSeries]
    /// The whole window of the x axis, also where the data is shorter.
    let xDomain: ClosedRange<Date>
    let warn: Double?
    let critical: Double?
    let hoverAt: Date?
    let pinnedAt: Date?
    /// When nil the Y axis auto-scales to the data's peak (+15%) so
    /// low-magnitude metrics like NET remain legible.
    var yDomain: ClosedRange<Double>? = 0...1
    /// Formats the Y axis tick labels. Defaults to a 0-100% formatter.
    var yAxisLabel: (Double) -> String = { v in "\(Int(v * 100))%" }
    /// Formats the values of the tooltip.
    var valueFormat: (Double) -> String = { v in "\(Int((v * 100).rounded()))%" }
    /// Live drag selection (start, end dates) — not necessarily ordered.
    /// Drawn as a translucent rectangle for the span that a drag zooms to.
    var selectionRange: ClosedRange<Date>? = nil
    var onHover: ((Date?) -> Void)? = nil
    var onPinTap: ((Date) -> Void)? = nil
    /// Drag lifecycle. `onDragBegin` fires on the first movement past the
    /// click threshold, `onDragUpdate` on every subsequent movement, and
    /// `onDragEnd` once the mouse is released. A gesture with no movement
    /// is treated as a tap and routes through `onPinTap` instead.
    var onDragBegin: ((Date) -> Void)? = nil
    var onDragUpdate: ((Date) -> Void)? = nil
    var onDragEnd: (() -> Void)? = nil

    @State private var lastHoverEmit: Date = .distantPast
    @State private var dragStartLocation: CGPoint?
    @State private var dragActive = false

    /// Pixels the mouse must travel before we treat a click as a drag.
    /// Below this, the gesture is interpreted as a tap (pin) on release.
    private static let dragActivationThreshold: CGFloat = 3

    var body: some View {
        Chart {
            ForEach(Array(visible.enumerated()), id: \.element.id) { index, s in
                ForEach(s.points) { p in
                    if index == 0 {
                        AreaMark(x: .value("t", p.t), y: .value("v", p.v), series: .value("s", s.name))
                            .foregroundStyle(LinearGradient(
                                colors: [s.color.opacity(0.28), s.color.opacity(0.02)],
                                startPoint: .top, endPoint: .bottom))
                            .interpolationMethod(.monotone)
                    }
                    LineMark(x: .value("t", p.t), y: .value("v", p.v), series: .value("s", s.name))
                        .foregroundStyle(s.color)
                        .lineStyle(.init(lineWidth: 1.4))
                        .interpolationMethod(.monotone)
                }
            }
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
            // The hover and pin lines are in `chartOverlay`. A change of
            // their date would rebuild the chart and slow the cursor.
        }
        .chartXScale(domain: xDomain)
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
                ZStack(alignment: .topLeading) {
                    // Selection highlight. Drawn under the gesture surface
                    // so it doesn't block hover/drag.
                    selectionOverlay(proxy: proxy, geo: geo)
                        .allowsHitTesting(false)

                    // Pinned & hover rules — drawn as plain shapes over the
                    // plot frame. Changes here re-layout only this overlay,
                    // so the cursor line moves at full refresh rate even
                    // while the underlying Chart is throttled.
                    cursorOverlay(proxy: proxy, geo: geo)
                        .allowsHitTesting(false)

                    tooltipOverlay(proxy: proxy, geo: geo)
                        .allowsHitTesting(false)

                    Rectangle().fill(.clear).contentShape(.rect)
                        .onContinuousHover { phase in
                            switch phase {
                            case .active(let point):
                                let now = Date()
                                // 60fps. The hover indicator now lives in
                                // an overlay that doesn't rebuild the Chart,
                                // so we can emit on every frame for a
                                // cursor that tracks the mouse immediately.
                                guard now.timeIntervalSince(lastHoverEmit) >= 1.0 / 60.0 else { return }
                                lastHoverEmit = now
                                if let t = time(at: point, proxy: proxy, geo: geo) {
                                    onHover?(clampToSamples(t))
                                }
                            case .ended:
                                lastHoverEmit = .distantPast
                                onHover?(nil)
                            }
                        }
                        .gesture(
                            DragGesture(minimumDistance: 0)
                                .onChanged { value in
                                    handleDragChanged(value, proxy: proxy, geo: geo)
                                }
                                .onEnded { value in
                                    handleDragEnded(value, proxy: proxy, geo: geo)
                                }
                        )
                }
            }
        }
    }

    @ViewBuilder
    private func cursorOverlay(proxy: ChartProxy, geo: GeometryProxy) -> some View {
        if let plot = proxy.plotFrame.map({ geo[$0] }) {
            if let pinnedAt, let x = proxy.position(forX: pinnedAt) {
                Rectangle()
                    .fill(Color.orange)
                    .frame(width: 1.2, height: plot.height)
                    .offset(x: plot.origin.x + x - 0.6, y: plot.origin.y)
            }
            if let hoverAt, let x = proxy.position(forX: hoverAt) {
                DashedVerticalLine()
                    .stroke(Color.primary.opacity(0.35),
                            style: StrokeStyle(lineWidth: 0.6, dash: [2, 2]))
                    .frame(width: 0.6, height: plot.height)
                    .offset(x: plot.origin.x + x - 0.3, y: plot.origin.y)
            }
        }
    }

    @ViewBuilder
    private func selectionOverlay(proxy: ChartProxy, geo: GeometryProxy) -> some View {
        if let range = selectionRange,
           let plot = proxy.plotFrame.map({ geo[$0] }),
           let xStart = proxy.position(forX: range.lowerBound),
           let xEnd = proxy.position(forX: range.upperBound) {
            let lo = min(xStart, xEnd)
            let hi = max(xStart, xEnd)
            Rectangle()
                .fill(Color.orange.opacity(0.18))
                .overlay(
                    Rectangle()
                        .stroke(Color.orange.opacity(0.7), lineWidth: 1)
                )
                .frame(width: max(hi - lo, 1), height: plot.height)
                .offset(x: plot.origin.x + lo, y: plot.origin.y)
        }
    }

    private func handleDragChanged(_ value: DragGesture.Value, proxy: ChartProxy, geo: GeometryProxy) {
        if dragStartLocation == nil {
            dragStartLocation = value.startLocation
        }
        let dx = value.location.x - (dragStartLocation?.x ?? value.location.x)
        let dy = value.location.y - (dragStartLocation?.y ?? value.location.y)
        let moved = (dx * dx + dy * dy).squareRoot()

        if !dragActive && moved >= Self.dragActivationThreshold {
            dragActive = true
            if let t = time(at: value.startLocation, proxy: proxy, geo: geo) {
                onDragBegin?(clampToSamples(t))
            }
        }

        if dragActive, let t = time(at: value.location, proxy: proxy, geo: geo) {
            onDragUpdate?(clampToSamples(t))
        }
    }

    private func handleDragEnded(_ value: DragGesture.Value, proxy: ChartProxy, geo: GeometryProxy) {
        defer {
            dragStartLocation = nil
            dragActive = false
        }
        if dragActive {
            onDragEnd?()
            return
        }
        // No drag movement → treat as a tap/pin.
        if let t = time(at: value.location, proxy: proxy, geo: geo) {
            onPinTap?(clampToSamples(t))
        }
    }

    private func time(at point: CGPoint, proxy: ChartProxy, geo: GeometryProxy) -> Date? {
        let plotFrame = proxy.plotFrame.map { geo[$0] } ?? .zero
        let x = point.x - plotFrame.origin.x
        return proxy.value(atX: x)
    }

    /// The time of the cursor stays in the window and not after now.
    private func clampToSamples(_ t: Date) -> Date {
        let upper = min(xDomain.upperBound, Date())
        return min(max(t, xDomain.lowerBound), max(upper, xDomain.lowerBound))
    }

    /// The points inside the window.
    private var visible: [ChartSeries] {
        series.map { s in
            ChartSeries(name: s.name, color: s.color, points: s.points.filter { xDomain.contains($0.t) })
        }
    }

    @ViewBuilder
    private func tooltipOverlay(proxy: ChartProxy, geo: GeometryProxy) -> some View {
        if let hoverAt, let plot = proxy.plotFrame.map({ geo[$0] }), let x = proxy.position(forX: hoverAt) {
            let rows = series.compactMap { s in Self.nearest(in: s.points, to: hoverAt).map { (s, $0) } }
            if !rows.isEmpty {
                let flip = x > plot.width * 0.6
                VStack(alignment: .leading, spacing: 2) {
                    Text(hoverAt.formatted(date: .abbreviated, time: .standard))
                        .foregroundStyle(.secondary)
                    ForEach(rows, id: \.0.id) { s, p in
                        HStack(spacing: 5) {
                            Circle().fill(s.color).frame(width: 6, height: 6)
                            if rows.count > 1 { Text(s.name).foregroundStyle(.secondary) }
                            Text(valueFormat(p.v)).fontWeight(.semibold)
                        }
                    }
                }
                .font(.system(size: 11, design: .monospaced))
                .padding(.horizontal, 8).padding(.vertical, 5)
                .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 7))
                .overlay(RoundedRectangle(cornerRadius: 7).stroke(Color.primary.opacity(0.12), lineWidth: 0.5))
                .fixedSize()
                .alignmentGuide(.leading) { d in flip ? d.width + 8 : -8 }
                .offset(x: plot.origin.x + x, y: plot.origin.y + 4)
            }
        }
    }

    /// The point of a sorted series that is nearest to `t`.
    static func nearest(in points: [MetricPoint], to t: Date) -> MetricPoint? {
        guard !points.isEmpty else { return nil }
        var lo = 0
        var hi = points.count - 1
        while lo < hi {
            let mid = (lo + hi) / 2
            if points[mid].t < t { lo = mid + 1 } else { hi = mid }
        }
        if lo > 0, t.timeIntervalSince(points[lo - 1].t) < points[lo].t.timeIntervalSince(t) { lo -= 1 }
        return points[lo]
    }

    private var effectiveYDomain: ClosedRange<Double> {
        if let fixed = yDomain { return fixed }
        let peak = series.flatMap(\.points).map(\.v).max() ?? 0
        let ceiling = max(peak * 1.15, 0.0001)
        return 0...ceiling
    }
}

/// A vertical stroke path used by the hover overlay. Drawing the dash via
/// `stroke(style:)` on a `Path` is cheaper than going through the Chart's
/// RuleMark pipeline and, crucially, doesn't invalidate the Chart body.
private struct DashedVerticalLine: Shape {
    func path(in rect: CGRect) -> Path {
        var p = Path()
        let x = rect.midX
        p.move(to: CGPoint(x: x, y: rect.minY))
        p.addLine(to: CGPoint(x: x, y: rect.maxY))
        return p
    }
}
