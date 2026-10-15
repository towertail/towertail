import Foundation

/// Per-mount disk capacity history. Keyed by mount path (the sampler's
/// `DiskSample.mount`). Each mount gets its own MetricSeries of
/// fractional-used values (0...1).
///
/// A "max" aggregate series is maintained in parallel for the default
/// "worst mount" view — kept in sync with the dictionary so consumers
/// don't have to recompute it on every render.
struct DiskSeries: Sendable {
    private(set) var byMount: [String: MetricSeries] = [:]
    private(set) var max: MetricSeries = MetricSeries()

    /// Mount names in stable alphabetical order for use in pickers. Ordering
    /// is derived lazily from `byMount`; SwiftUI re-renders pick this up
    /// automatically via value-type mutation semantics.
    var mounts: [String] {
        byMount.keys.sorted()
    }

    mutating func append(_ disks: [DiskSample], at t: Date) {
        var worst = 0.0
        for d in disks {
            let frac = d.total > 0 ? Double(d.used) / Double(d.total) : 0
            let v = min(Swift.max(frac, 0), 1)
            byMount[d.mount, default: MetricSeries()].append(MetricPoint(t: t, v: v))
            if v > worst { worst = v }
        }
        max.append(MetricPoint(t: t, v: worst))
    }

    /// Returns the series for a mount, or nil if that mount hasn't been
    /// seen. Callers fall back to `max` for the "Max" selection.
    func series(forMount mount: String) -> MetricSeries? {
        byMount[mount]
    }

    /// Replay persisted per-mount rows from SQLite. Rows must be sorted
    /// ascending by timestamp. Rows sharing a timestamp are collapsed
    /// into one tick so the `max` aggregate matches what `append` would
    /// have produced live.
    mutating func hydrate(rows: [HistoryStore.DiskCapacityRow]) {
        guard !rows.isEmpty else { return }
        var i = 0
        while i < rows.count {
            let t = rows[i].t
            var worst = 0.0
            while i < rows.count && rows[i].t == t {
                let r = rows[i]
                let v = min(Swift.max(r.frac, 0), 1)
                byMount[r.mount, default: MetricSeries()].append(MetricPoint(t: t, v: v))
                if v > worst { worst = v }
                i += 1
            }
            max.append(MetricPoint(t: t, v: worst))
        }
    }
}

/// Per-device disk I/O history. Tracks both read and write rates as
/// MB/s (to match the normalization used for NetInfo rates). Also
/// maintains the "total" aggregate across all devices for the default
/// view.
struct DiskIOSeries: Sendable {
    struct DeviceSeries: Sendable {
        var read: MetricSeries = MetricSeries()
        var write: MetricSeries = MetricSeries()
    }

    private(set) var byDevice: [String: DeviceSeries] = [:]
    private(set) var total: DeviceSeries = DeviceSeries()

    var devices: [String] {
        byDevice.keys.sorted()
    }

    /// Append one sample. `devices` is the per-device breakdown from the
    /// sampler; if the sampler didn't emit per-device (old builds, or
    /// --no-disk), pass `nil` and we'll still update the total from the
    /// scalar aggregate so the I/O chart isn't empty.
    mutating func append(
        at t: Date,
        totalReadBps: Int64, totalWriteBps: Int64,
        devices: [DiskIODevice]?
    ) {
        total.read.append(MetricPoint(t: t, v: bytesToMBps(totalReadBps)))
        total.write.append(MetricPoint(t: t, v: bytesToMBps(totalWriteBps)))
        guard let devices else { return }
        for d in devices {
            var ds = byDevice[d.name] ?? DeviceSeries()
            ds.read.append(MetricPoint(t: t, v: bytesToMBps(d.readBps)))
            ds.write.append(MetricPoint(t: t, v: bytesToMBps(d.writeBps)))
            byDevice[d.name] = ds
        }
    }

    func series(forDevice name: String) -> DeviceSeries? {
        byDevice[name]
    }

    /// Replay persisted per-device rows from SQLite. Rows must be sorted
    /// ascending by timestamp. Rows with `device == ""` represent the
    /// scalar total, matching how `append(at:…, devices:)` persists it.
    mutating func hydrate(rows: [HistoryStore.DiskIORow]) {
        for r in rows {
            if r.device.isEmpty {
                total.read.append(MetricPoint(t: r.t, v: Swift.max(r.readMBps, 0)))
                total.write.append(MetricPoint(t: r.t, v: Swift.max(r.writeMBps, 0)))
            } else {
                var ds = byDevice[r.device] ?? DeviceSeries()
                ds.read.append(MetricPoint(t: r.t, v: Swift.max(r.readMBps, 0)))
                ds.write.append(MetricPoint(t: r.t, v: Swift.max(r.writeMBps, 0)))
                byDevice[r.device] = ds
            }
        }
    }

    private func bytesToMBps(_ b: Int64) -> Double {
        Double(Swift.max(b, 0)) / 1_048_576.0
    }
}
