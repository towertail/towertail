import SwiftUI

/// Current host health values for the HEALTH tab. One row per signal the
/// host reports; rows the OS does not expose are hidden.
struct HealthDetails: View {
    let health: HealthStatus
    let thresholds: MetricThresholds

    var body: some View {
        if let h = health.info {
            Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 4) {
                row("Processes", HealthStatus.count(h.procs),
                    limit: "warn \(HealthStatus.count(thresholds.procsWarn)) · critical \(HealthStatus.count(thresholds.procsCritical))",
                    tint: health.tint(of: .procs))
                if let z = h.zombies {
                    row("Zombies", HealthStatus.count(z),
                        limit: zombieParents(h.zombieParents),
                        tint: health.tint(of: .zombies))
                }
                if let used = h.pidsUsed, let max = h.pidsMax {
                    row("PIDs", "\(HealthStatus.count(Int(used))) / \(HealthStatus.count(Int(max)))",
                        limit: percent(HealthStatus.fraction(used, max)),
                        tint: health.tint(of: .pids))
                }
                if let used = h.filesUsed, let max = h.filesMax {
                    row("Open files", HealthStatus.count(Int(used)),
                        limit: percent(HealthStatus.fraction(used, max)) + " of limit",
                        tint: health.tint(of: .files))
                }
                if let p = h.psi {
                    row("Pressure",
                        String(format: "mem %.0f%% · io %.0f%% · cpu %.0f%%", p.memSome, p.ioFull, p.cpuSome),
                        limit: "share of the last 10 s with stalled tasks",
                        tint: worst(health.tint(of: .memPressure), health.tint(of: .ioPressure)))
                }
                if let lvl = h.memPressure {
                    row("Memory pressure", lvl == 4 ? "critical" : lvl == 2 ? "high" : "normal",
                        limit: "macOS",
                        tint: health.tint(of: .memPressure))
                }
                ForEach(inodeReasons, id: \.text) { r in
                    row("Inodes", r.text, limit: "", tint: r.tint)
                }
            }
            .font(.callout)
        } else {
            Text("No health data from this host's sampler.")
                .font(.callout)
                .foregroundStyle(.secondary)
        }
    }

    private var inodeReasons: [HealthStatus.Reason] {
        health.reasons.filter { $0.signal == .inodes }
    }

    private func worst(_ a: ThresholdTint, _ b: ThresholdTint) -> ThresholdTint {
        a == .critical || b == .critical ? .critical : a == .warn || b == .warn ? .warn : .nominal
    }

    private func percent(_ f: Double?) -> String {
        guard let f else { return "" }
        return "\(Int((f * 100).rounded()))%"
    }

    private func zombieParents(_ parents: [ZombieParent]?) -> String {
        guard let parents, !parents.isEmpty else { return "" }
        return parents.map { "\($0.name) (pid \($0.pid)): \(HealthStatus.count($0.count))" }
            .joined(separator: " · ")
    }

    @ViewBuilder
    private func row(_ label: String, _ value: String, limit: String, tint: ThresholdTint) -> some View {
        GridRow {
            HStack(spacing: 6) {
                Circle().fill(tint.color).frame(width: 7, height: 7)
                Text(label).foregroundStyle(.secondary)
            }
            Text(value).monospacedDigit().foregroundStyle(tint == .nominal ? Color.primary : tint.color)
            Text(limit).font(.caption).foregroundStyle(.secondary).lineLimit(1)
        }
    }
}
