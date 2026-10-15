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
    /// Live drag selection (start, end dates) — not necessarily ordered.
    /// When non-nil, rendered as a translucent orange rectangle so the
    /// user can see what the "Zoom in" button will act on.
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
            // NOTE: hover + pinned indicators are intentionally NOT plotted
            // as RuleMarks here. Any change to their date would rebuild the
            // whole Chart (diffing every sample), which caps their refresh
            // at the chart's throttle rate and produces a visibly laggy
            // cursor. They're drawn in `chartOverlay` below instead, where
            // the overlay redraws independently of the Chart body at full
            // SwiftUI refresh rate.
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
