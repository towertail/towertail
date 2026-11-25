import Foundation

/// Keeps the last REST history answers in memory for a short time. A quick
/// hide and show of the popover, or a tab switch, reuses the answer.
actor CloudHistoryCache {
    private struct Entry {
        let at: Date
        let value: any Sendable
    }

    private let client: RemoteClient
    private let ttl: TimeInterval
    private let now: @Sendable () -> Date
    private var entries: [String: Entry] = [:]
    private var inflight: [String: Task<any Sendable, Error>] = [:]

    init(client: RemoteClient, ttl: Duration = .seconds(60), now: @escaping @Sendable () -> Date = { Date() }) {
        self.client = client
        let c = ttl.components
        self.ttl = TimeInterval(c.seconds) + TimeInterval(c.attoseconds) / 1e18
        self.now = now
    }

    /// The key rounds the window down to the step, so a window that moves by
    /// less than one step reuses the entry.
    func nodeHistory(
        _ id: UUID, metrics: [String], from: Date, to: Date, step: Duration
    ) async throws -> RemoteHistoryResponse {
        let client = client
        let key = "node|\(id)|\(metrics.joined(separator: ","))|\(Self.floor(from, step))|\(Self.floor(to, step))|\(step.components.seconds)"
        return try await cached(key) {
            try await client.history(nodeID: id, metrics: metrics, from: from, to: to, step: step)
        }
    }

    func fleet(_ ids: [UUID], range: TimeInterval) async throws -> RemoteFleetHistory {
        let client = client
        let to = now()
        let from = to.addingTimeInterval(-range)
        let key = "fleet|\(ids.map(\.uuidString).joined(separator: ","))|\(Int(range))"
        return try await cached(key) {
            try await client.fleetHistory(
                nodeIDs: ids, metrics: ["cpu", "mem", "net_rx", "net_tx"], from: from, to: to
            )
        }
    }

    func disks(_ id: UUID, from: Date, to: Date) async throws -> RemoteDiskHistory {
        let client = client
        let key = "disks|\(id)|\(Self.floor(from, .seconds(60)))|\(Self.floor(to, .seconds(60)))"
        return try await cached(key) {
            try await client.diskHistory(nodeID: id, from: from, to: to)
        }
    }

    func offenders(_ id: UUID, from: Date, to: Date, by: ProcSort) async throws -> RemoteOffenders {
        let client = client
        let key = "offenders|\(id)|\(by.rawValue)|\(Self.floor(from, .seconds(10)))|\(Self.floor(to, .seconds(10)))"
        return try await cached(key) {
            try await client.offenders(nodeID: id, from: from, to: to, by: by, limit: 10, series: 60)
        }
    }

    func process(_ id: UUID, key processKey: String, from: Date, to: Date, step: Duration) async throws -> RemoteProcessDetail {
        let client = client
        let key = "process|\(id)|\(processKey)|\(Self.floor(from, step))|\(Self.floor(to, step))|\(step.components.seconds)"
        return try await cached(key) {
            try await client.process(nodeID: id, key: processKey, from: from, to: to, step: step)
        }
    }

    // MARK: Snapshots

    private struct SnapshotEntry {
        let value: RemoteProcessSnapshot
        let at: Date
        /// The bucket was over when the entry was stored.
        let closed: Bool
    }

    private static let snapshotCapacity = 200
    private static let openBucketTTL: TimeInterval = 10
    private var snapshots: [String: SnapshotEntry] = [:]
    /// Oldest use first.
    private var snapshotOrder: [String] = []

    private static func snapshotKey(_ id: UUID, _ bucket: ProcBucket, _ by: ProcSort) -> String {
        "\(id)|\(by.rawValue)|\(bucket.tier.rawValue)|\(Int(bucket.start.timeIntervalSince1970))"
    }

    /// The stored snapshot, or nil. It sends no request.
    func cachedProcessesAt(_ id: UUID, bucket: ProcBucket, by: ProcSort) -> RemoteProcessSnapshot? {
        let key = Self.snapshotKey(id, bucket, by)
        guard let e = snapshots[key] else { return nil }
        if !e.closed, now().timeIntervalSince(e.at) >= Self.openBucketTTL {
            snapshots[key] = nil
            snapshotOrder.removeAll { $0 == key }
            return nil
        }
        touch(key)
        return e.value
    }

    func processesAt(_ id: UUID, bucket: ProcBucket, by: ProcSort) async throws -> RemoteProcessSnapshot {
        if let hit = cachedProcessesAt(id, bucket: bucket, by: by) { return hit }
        let key = Self.snapshotKey(id, bucket, by)
        let client = client
        let task: Task<any Sendable, Error>
        if let running = inflight["snap|" + key] {
            task = running
        } else {
            task = Task {
                try await client.processesAt(nodeID: id, ts: bucket.start, by: by, limit: bucket.limit)
            }
            inflight["snap|" + key] = task
        }
        defer { inflight["snap|" + key] = nil }
        guard let value = try await task.value as? RemoteProcessSnapshot else {
            throw RemoteClient.RemoteError.decode("cache type mismatch")
        }
        let t = now()
        snapshots[key] = SnapshotEntry(
            value: value, at: t, closed: bucket.start.addingTimeInterval(bucket.span) < t
        )
        touch(key)
        while snapshotOrder.count > Self.snapshotCapacity {
            snapshots[snapshotOrder.removeFirst()] = nil
        }
        return value
    }

    private func touch(_ key: String) {
        snapshotOrder.removeAll { $0 == key }
        snapshotOrder.append(key)
    }

    private static func floor(_ d: Date, _ step: Duration) -> Int {
        let s = max(Int(step.components.seconds), 1)
        return Int(d.timeIntervalSince1970) / s * s
    }

    /// Returns a fresh entry, joins a request that runs, or starts one.
    /// A failure is not stored.
    private func cached<T: Sendable>(_ key: String, _ fetch: @escaping @Sendable () async throws -> T) async throws -> T {
        if let e = entries[key], now().timeIntervalSince(e.at) < ttl, let v = e.value as? T {
            return v
        }
        let task: Task<any Sendable, Error>
        if let running = inflight[key] {
            task = running
        } else {
            task = Task { try await fetch() }
            inflight[key] = task
        }
        do {
            let value = try await task.value
            inflight[key] = nil
            let t = now()
            entries = entries.filter { t.timeIntervalSince($0.value.at) < ttl }
            entries[key] = Entry(at: t, value: value)
            guard let typed = value as? T else { throw RemoteClient.RemoteError.decode("cache type mismatch") }
            return typed
        } catch {
            inflight[key] = nil
            throw error
        }
    }
}
