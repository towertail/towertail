import Foundation
@testable import Towertail

/// A clock that moves only when the test calls `advance`.
final class ManualClock: Clock, @unchecked Sendable {
    struct Instant: InstantProtocol {
        var offset: Duration
        func advanced(by duration: Duration) -> Instant { Instant(offset: offset + duration) }
        func duration(to other: Instant) -> Duration { other.offset - offset }
        static func < (a: Instant, b: Instant) -> Bool { a.offset < b.offset }
    }

    private struct Waiter {
        let id: UInt64
        let deadline: Instant
        let continuation: CheckedContinuation<Void, Error>
    }

    private let lock = NSLock()
    private var current = Instant(offset: .zero)
    private var waiters: [Waiter] = []
    private var nextID: UInt64 = 0

    var now: Instant {
        lock.lock()
        defer { lock.unlock() }
        return current
    }

    var minimumResolution: Duration { .nanoseconds(1) }

    var sleeperCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return waiters.count
    }

    func sleep(until deadline: Instant, tolerance: Duration?) async throws {
        lock.lock()
        let id = nextID
        nextID += 1
        lock.unlock()
        try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { (c: CheckedContinuation<Void, Error>) in
                lock.lock()
                if Task.isCancelled {
                    lock.unlock()
                    c.resume(throwing: CancellationError())
                } else if deadline <= current {
                    lock.unlock()
                    c.resume()
                } else {
                    waiters.append(Waiter(id: id, deadline: deadline, continuation: c))
                    lock.unlock()
                }
            }
        } onCancel: {
            lock.lock()
            let hit = waiters.first { $0.id == id }
            waiters.removeAll { $0.id == id }
            lock.unlock()
            hit?.continuation.resume(throwing: CancellationError())
        }
    }

    func advance(_ d: Duration) {
        lock.lock()
        current = current.advanced(by: d)
        let due = waiters.filter { $0.deadline <= current }
        waiters.removeAll { $0.deadline <= current }
        lock.unlock()
        due.forEach { $0.continuation.resume() }
    }
}

/// Lets the tasks of the main actor run until they wait.
@MainActor
func settle() async {
    for _ in 0..<60 { await Task.yield() }
}

/// A `CloudHistoryLoader` with stored answers. A latency sleeps on the clock.
@MainActor
final class FakeCloudLoader: CloudHistoryLoader {
    struct SnapKey: Hashable {
        let bucket: ProcBucket
        let by: ProcSort
    }

    var clock: ManualClock?
    var latency: Duration = .zero
    var cache: [SnapKey: RemoteProcessSnapshot] = [:]
    private(set) var snapshotCalls: [ProcBucket] = []
    private(set) var windowCalls: [(metrics: [String], window: ClosedRange<Date>, step: Duration)] = []
    private(set) var diskCalls = 0
    private(set) var offenderCalls = 0
    private(set) var processCalls: [String] = []
    var windowSeries: [String: [(Date, Double)]] = [:]
    var detail: RemoteProcessDetail?
    var offenderBody: RemoteOffenders?
    var failWindow: Error?

    static let node = UUID(uuidString: "11111111-1111-1111-1111-111111111111")!

    static func decode<T: Decodable>(_ json: String) -> T {
        try! SampleCodec.decoder().decode(T.self, from: Data(json.utf8))
    }

    static func snapshot(ts: Date, tier: String = "raw", rows: [String] = []) -> RemoteProcessSnapshot {
        let iso = ISO8601DateFormatter().string(from: ts)
        return decode("""
        {"node_id":"\(node)","ts":"\(iso)","tier":"\(tier)","bucket_s":0,"by":"cpu","total":\(rows.count),
         "processes":[\(rows.joined(separator: ","))]}
        """)
    }

    func backfillCards(_ ids: [UUID]) async {}

    func window(host: UUID, metrics: [String], window: ClosedRange<Date>, step: Duration) async throws -> RemoteHistoryResponse {
        windowCalls.append((metrics, window, step))
        if let failWindow { throw failWindow }
        let f = ISO8601DateFormatter()
        let body = metrics.compactMap { m -> String? in
            guard let pts = windowSeries[m] else { return nil }
            let items = pts.map { #"{"ts":"\#(f.string(from: $0.0))","value":\#($0.1)}"# }.joined(separator: ",")
            return #""\#(m)":[\#(items)]"#
        }.joined(separator: ",")
        return Self.decode("""
        {"node_id":"\(host)","series":{\(body)},"from":"\(f.string(from: window.lowerBound))","to":"\(f.string(from: window.upperBound))","resolution":"raw"}
        """)
    }

    func disks(host: UUID, window: ClosedRange<Date>) async throws -> RemoteDiskHistory {
        diskCalls += 1
        return Self.decode(#"{"mounts":[]}"#)
    }

    func processesAt(host: UUID, bucket: ProcBucket, by: ProcSort) async throws -> RemoteProcessSnapshot {
        snapshotCalls.append(bucket)
        if latency > .zero, let clock { try await clock.sleep(for: latency) }
        return Self.snapshot(ts: bucket.start)
    }

    func cachedProcessesAt(host: UUID, bucket: ProcBucket, by: ProcSort) async -> RemoteProcessSnapshot? {
        cache[SnapKey(bucket: bucket, by: by)]
    }

    func offenders(host: UUID, window: ClosedRange<Date>, by: ProcSort) async throws -> RemoteOffenders {
        offenderCalls += 1
        guard let offenderBody else { throw FakeCloudError.missing }
        return offenderBody
    }

    func process(host: UUID, key: String, window: ClosedRange<Date>, step: Duration) async throws -> RemoteProcessDetail {
        processCalls.append(key)
        guard let detail else { throw FakeCloudError.missing }
        return detail
    }
}

enum FakeCloudError: Error { case missing }
