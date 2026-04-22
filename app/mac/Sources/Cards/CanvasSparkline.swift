import SwiftUI

/// Canvas-based sparkline used by the popover cards. Mirrors the visual
/// output of `Sparkline` (Swift Charts) but without the Charts dependency
/// — each popover card renders 4 of these, and Charts' per-instance
/// construction cost dominated first-open and scroll in a 10+ server
/// fleet. A plain `Canvas` draw is roughly an order of magnitude cheaper
/// to instantiate.
///
/// Deliberately stripped down vs. `Sparkline`:
/// - No hover overlay (popover cards don't consume hover; the full-view
///   charts still use `Sparkline`/`Chart` and keep hover there).
/// - No x/y axes, no chart proxy, no plot frame.
struct CanvasSparkline: View {
    let samples: [MetricPoint]
    let tint: Color
    /// When non-nil a dashed horizontal line is drawn at this y-value.
    let warn: Double?
    let windowSeconds: TimeInterval
    /// When nil the y-axis auto-scales to the visible window's peak with
    /// 15% headroom (used by the NET sparkline where rates vary wildly).
    var yDomain: ClosedRange<Double>? = 0...1

    var body: some View {
        Canvas(opaque: false, rendersAsynchronously: false) { ctx, size in
            draw(into: &ctx, size: size)
        }
        .frame(height: 28)
        .drawingGroup()
    }

    private func draw(into ctx: inout GraphicsContext, size: CGSize) {
        guard !samples.isEmpty else { return }
        let xDomain = computedXDomain
        let yDomain = effectiveYDomain(xDomain: xDomain)
        let xSpan = xDomain.upperBound.timeIntervalSince(xDomain.lowerBound)
        guard xSpan > 0 else { return }
        let ySpan = yDomain.upperBound - yDomain.lowerBound
        // Floor the y-span so a flat-zero line still has a visible baseline
        // rather than a division-by-zero path.
        let ySpanSafe = max(ySpan, 0.0001)

        // Project samples into the canvas. Iterate once, keep only points
        // that fall inside the x-window plus one point on either side so
        // the edge segments clip cleanly instead of stopping short.
        var pts: [CGPoint] = []
        pts.reserveCapacity(samples.count)
        for s in samples {
            let tx = s.t.timeIntervalSince(xDomain.lowerBound)
            guard tx >= -xSpan * 0.02, tx <= xSpan * 1.02 else { continue }
            let x = (tx / xSpan) * size.width
            let yNorm = (s.v - yDomain.lowerBound) / ySpanSafe
            let y = size.height - (yNorm * size.height)
            pts.append(CGPoint(x: x, y: y))
        }
        guard pts.count >= 2 else {
            // Single-point or empty windows — draw a flat baseline dot so
            // the UI still conveys "we have data, it's just flat".
            if let p = pts.first {
                let dot = Path(ellipseIn: CGRect(x: p.x - 1, y: p.y - 1, width: 2, height: 2))
                ctx.fill(dot, with: .color(tint))
            }
            return
        }

        let linePath = monotoneCubicPath(points: pts)

        // Area fill — close the line path down to the bottom edge and
        // fill with a top-to-bottom tint gradient (matches Chart's
        // AreaMark styling in Sparkline.swift).
        var areaPath = linePath
        areaPath.addLine(to: CGPoint(x: pts.last!.x, y: size.height))
        areaPath.addLine(to: CGPoint(x: pts.first!.x, y: size.height))
        areaPath.closeSubpath()
        let gradient = Gradient(colors: [tint.opacity(0.35), tint.opacity(0.02)])
        ctx.fill(
            areaPath,
            with: .linearGradient(
                gradient,
                startPoint: CGPoint(x: 0, y: 0),
                endPoint: CGPoint(x: 0, y: size.height)
            )
        )

        // Warn rule — dashed horizontal line at warn, but only when it
        // falls inside the visible y-range (otherwise the reference line
        // is off-screen and drawing it off-canvas is wasted work).
        if let w = warn, w >= yDomain.lowerBound, w <= yDomain.upperBound {
            let wy = size.height - ((w - yDomain.lowerBound) / ySpanSafe) * size.height
            var rule = Path()
            rule.move(to: CGPoint(x: 0, y: wy))
            rule.addLine(to: CGPoint(x: size.width, y: wy))
            ctx.stroke(
                rule,
                with: .color(tint.opacity(0.15)),
                style: StrokeStyle(lineWidth: 0.5, dash: [2, 2])
            )
        }

        ctx.stroke(
            linePath,
            with: .color(tint),
            style: StrokeStyle(lineWidth: 1.2, lineCap: .round, lineJoin: .round)
        )
    }

    /// Fritsch–Carlson monotone cubic interpolation. Produces the same
    /// "smooth but never overshoots a sample" curve that Swift Charts'
    /// `.interpolationMethod(.monotone)` does. The algorithm picks
    /// tangents that preserve monotonicity of the input samples — spiky
    /// CPU data reads as gentle curves without the line dipping below
    /// local minima or shooting above local maxima.
    private func monotoneCubicPath(points: [CGPoint]) -> Path {
        var path = Path()
        guard points.count >= 2 else { return path }
        path.move(to: points[0])
        if points.count == 2 {
            path.addLine(to: points[1])
            return path
        }

        let n = points.count
        // Secant slopes between consecutive points.
        var d = [CGFloat](repeating: 0, count: n - 1)
        for i in 0..<(n - 1) {
            let dx = points[i + 1].x - points[i].x
            d[i] = dx == 0 ? 0 : (points[i + 1].y - points[i].y) / dx
        }
        // Tangents at each point.
        var m = [CGFloat](repeating: 0, count: n)
        m[0] = d[0]
        m[n - 1] = d[n - 2]
        for i in 1..<(n - 1) {
            if d[i - 1] * d[i] <= 0 {
                // Sign change or flat segment — tangent is zero so the
                // curve hits a horizontal at the local extremum.
                m[i] = 0
            } else {
                m[i] = (d[i - 1] + d[i]) / 2
            }
        }
        // Enforce monotonicity: if the tangent is too steep relative to
        // the secant, scale it down. Without this step cubic segments
        // overshoot their endpoints and the line wiggles outside the data
        // envelope.
        for i in 0..<(n - 1) {
            if d[i] == 0 {
                m[i] = 0
                m[i + 1] = 0
                continue
            }
            let a = m[i] / d[i]
            let b = m[i + 1] / d[i]
            let h = hypot(a, b)
            if h > 3 {
                let t = 3 / h
                m[i] = t * a * d[i]
                m[i + 1] = t * b * d[i]
            }
        }

        for i in 0..<(n - 1) {
            let p0 = points[i]
            let p1 = points[i + 1]
            let dx = p1.x - p0.x
            let c1 = CGPoint(x: p0.x + dx / 3, y: p0.y + m[i] * dx / 3)
            let c2 = CGPoint(x: p1.x - dx / 3, y: p1.y - m[i + 1] * dx / 3)
            path.addCurve(to: p1, control1: c1, control2: c2)
        }
        return path
    }

    private var computedXDomain: ClosedRange<Date> {
        let latest = samples.last?.t ?? Date()
        let earliest = latest.addingTimeInterval(-max(1, windowSeconds))
        return earliest...latest
    }

    private func effectiveYDomain(xDomain: ClosedRange<Date>) -> ClosedRange<Double> {
        if let fixed = yDomain { return fixed }
        var peak: Double = 0
        for s in samples where s.t >= xDomain.lowerBound && s.t <= xDomain.upperBound {
            if s.v > peak { peak = s.v }
        }
        let ceiling = max(peak * 1.15, 0.0001)
        return 0...ceiling
    }
}
