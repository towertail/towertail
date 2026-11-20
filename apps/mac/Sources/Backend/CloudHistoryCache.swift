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

    func nodeHistory(_ id: UUID, metrics: [String], range: TimeInterval) async throws -> RemoteHistoryResponse {
        let client = client
        let to = now()
        let from = to.addingTimeInterval(-range)
        let step: Duration? = range > 3600 ? .seconds(10) : nil
        return try await cached("node|\(id)|\(metrics.joined(separator: ","))|\(Int(range))") {
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

    func disks(_ id: UUID, range: TimeInterval) async throws -> RemoteDiskHistory {
        let client = client
        let to = now()
        let from = to.addingTimeInterval(-range)
        return try await cached("disks|\(id)|\(Int(range))") {
            try await client.diskHistory(nodeID: id, from: from, to: to)
        }
    }

    func processes(_ id: UUID, from: Date, to: Date) async throws -> [RemoteTopProcess] {
        let client = client
        let key = "procs|\(id)|\(Int(from.timeIntervalSince1970))|\(Int(to.timeIntervalSince1970))"
        return try await cached(key) {
            try await client.topProcesses(nodeID: id, from: from, to: to, limit: 20)
        }
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
