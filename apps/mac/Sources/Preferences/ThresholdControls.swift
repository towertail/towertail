import SwiftUI

/// Column widths shared by threshold table rows so headers line up.
enum ThresholdColumns {
    static let name: CGFloat = 116
    static let value: CGFloat = 70
    static let sustain: CGFloat = 104
    static let notify: CGFloat = 136
    static let spacing: CGFloat = 10
}

/// Warn and critical number fields for one pair. Fractions edit as percent.
/// A value that crosses the other one moves the other one with it.
struct ThresholdPairFields: View {
    @Binding var pair: ThresholdPair
    /// Display multiplier: 100 for fractions, 1 for counts and PSI percent.
    var scale: Double = 1
    var unit: String = ""
    var range: ClosedRange<Double> = 0...Double(Int.max)

    var body: some View {
        HStack(spacing: ThresholdColumns.spacing) {
            ThresholdField(value: pair.warn * scale, range: range, unit: unit, tint: .warn, label: "Warn") {
                let v = $0 / scale
                pair = ThresholdPair(warn: v, critical: max(pair.critical, v))
            }
            ThresholdField(value: pair.critical * scale, range: range, unit: unit, tint: .critical, label: "Critical") {
                let v = $0 / scale
                pair = ThresholdPair(warn: min(pair.warn, v), critical: v)
            }
        }
    }
}

/// Number field that keeps its own text while the user types. The value
/// applies after 2 s without typing, on Return, or when focus leaves.
private struct ThresholdField: View {
    let value: Double
    let range: ClosedRange<Double>
    let unit: String
    let tint: ThresholdTint
    let label: String
    let onCommit: (Double) -> Void

    @State private var text = ""
    @State private var pending: Task<Void, Never>?
    @FocusState private var focused: Bool

    var body: some View {
        HStack(spacing: 3) {
            TextField(label, text: $text)
                .labelsHidden()
                .focused($focused)
                .multilineTextAlignment(.trailing)
                .textFieldStyle(.plain)
                .padding(.horizontal, 5)
                .padding(.vertical, 2)
                .background(RoundedRectangle(cornerRadius: 5).fill(Color(nsColor: .textBackgroundColor)))
                .overlay(RoundedRectangle(cornerRadius: 5).strokeBorder(tint.color.opacity(0.8), lineWidth: 1))
                .help(label)
                .onSubmit(apply)
            Text(unit)
                .font(.caption)
                .foregroundStyle(.secondary)
                .frame(width: 10, alignment: .leading)
        }
        .frame(width: ThresholdColumns.value)
        .onAppear { text = Self.format(value) }
        .onChange(of: value) { _, v in
            if !focused { text = Self.format(v) }
        }
        .onChange(of: text) { _, _ in
            guard focused else { return }
            pending?.cancel()
            pending = Task {
                try? await Task.sleep(for: .seconds(2))
                if !Task.isCancelled { apply() }
            }
        }
        .onChange(of: focused) { _, f in
            if !f { apply() }
        }
        .onDisappear { pending?.cancel() }
    }

    private func apply() {
        pending?.cancel()
        pending = nil
        guard let typed = Double(text.filter { $0.isNumber || $0 == "." }) else {
            text = Self.format(value)
            return
        }
        let v = min(max(typed.rounded(), range.lowerBound), range.upperBound)
        text = Self.format(v)
        if v != value.rounded() { onCommit(v) }
    }

    private static func format(_ v: Double) -> String {
        v.formatted(.number.precision(.fractionLength(0)))
    }
}

/// One server's current value of a metric, for the fleet preview.
struct FleetPoint: Identifiable {
    let id: UUID
    let name: String
    let value: Double
}

/// Track with warn and critical zones and one dot per server's current value.
struct FleetBar: View {
    let metric: ThresholdMetric
    let pair: ThresholdPair
    let points: [FleetPoint]

    /// Upper end of the log scale for count metrics.
    private static let countMax = 50_000.0

    var body: some View {
        GeometryReader { geo in
            let w = geo.size.width
            let warnX = position(pair.warn) * w
            let critX = position(pair.critical) * w
            ZStack(alignment: .topLeading) {
                Capsule().fill(Color.secondary.opacity(0.15))
                    .frame(width: w, height: 6)
                    .offset(y: 8)
                Rectangle().fill(ThresholdTint.warn.color.opacity(0.4))
                    .frame(width: max(0, critX - warnX), height: 6)
                    .offset(x: warnX, y: 8)
                Rectangle().fill(ThresholdTint.critical.color.opacity(0.4))
                    .frame(width: max(0, w - critX), height: 6)
                    .clipShape(UnevenRoundedRectangle(bottomTrailingRadius: 3, topTrailingRadius: 3))
                    .offset(x: critX, y: 8)
                tick(at: warnX, tint: .warn)
                tick(at: critX, tint: .critical)
                ForEach(points) { p in
                    Circle()
                        .fill(level(p.value).color)
                        .overlay(Circle().strokeBorder(.background, lineWidth: 1.5))
                        .frame(width: 10, height: 10)
                        .offset(x: position(p.value) * w - 5, y: 6)
                        .help("\(p.name): \(metric.format(p.value))")
                }
            }
        }
        .frame(height: 22)
        .help(metric.isFraction ? "Each dot is one server's current value." : "Log scale, 1 to 50,000. Each dot is one server's current value.")
    }

    /// Summary for the row label, e.g. "1 critical · 2 warn".
    var summary: (text: String, tint: ThresholdTint) {
        guard !points.isEmpty else { return ("No live data", .stale) }
        let crit = points.filter { level($0.value) == .critical }.count
        let warn = points.filter { level($0.value) == .warn }.count
        if crit + warn == 0 { return ("All \(points.count) OK", .stale) }
        let parts = [crit > 0 ? "\(crit) critical" : nil, warn > 0 ? "\(warn) warn" : nil].compactMap { $0 }
        return (parts.joined(separator: " · "), crit > 0 ? .critical : .warn)
    }

    private func level(_ v: Double) -> ThresholdTint {
        v >= pair.critical ? .critical : v >= pair.warn ? .warn : .nominal
    }

    private func position(_ v: Double) -> CGFloat {
        if metric.isFraction { return CGFloat(min(max(v, 0), 1)) }
        guard v > 1 else { return 0 }
        return CGFloat(min(log(v) / log(Self.countMax), 1))
    }

    private func tick(at x: CGFloat, tint: ThresholdTint) -> some View {
        RoundedRectangle(cornerRadius: 1)
            .fill(tint.color)
            .frame(width: 2, height: 16)
            .offset(x: x - 1, y: 3)
    }
}
