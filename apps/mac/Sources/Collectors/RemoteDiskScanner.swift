import Foundation

/// The disk scan endpoints of the server that `RemoteDiskScanner` uses.
@MainActor
protocol DiskScanJobService: AnyObject, Sendable {
    func startDiskScan(nodeId: UUID, request: DiskScanRequest) async throws -> DiskScanJob
    func diskScans(nodeId: UUID) async throws -> [DiskScanJob]
    func diskScan(jobId: String) async throws -> DiskScanJob
    func diskScanResult(jobId: String) async throws -> DiskScanResult?
    func cancelDiskScan(jobId: String) async throws -> DiskScanJob
}

extension RemoteClient: DiskScanJobService {}

/// Sends job changes (from `disk_scan_updated`, polls and refetches) to the
/// tasks that wait for them.
@MainActor
final class DiskScanJobFeed {
    private struct Sub {
        let match: (DiskScanJob) -> Bool
        let cont: AsyncStream<DiskScanJob>.Continuation
    }

    private var subs: [UUID: Sub] = [:]
    /// Jobs that are not final, by job id. A refetch after a reconnect
    /// reads these again.
    private(set) var active: [String: DiskScanJob] = [:]

    func subscribe(jobId: String) -> AsyncStream<DiskScanJob> {
        subscribe { $0.id == jobId }
    }

    func subscribe(nodeId: UUID) -> AsyncStream<DiskScanJob> {
        let id = nodeId.uuidString.lowercased()
        return subscribe { $0.nodeId.lowercased() == id }
    }

    private func subscribe(_ match: @escaping (DiskScanJob) -> Bool) -> AsyncStream<DiskScanJob> {
        let key = UUID()
        let (stream, cont) = AsyncStream<DiskScanJob>.makeStream()
        subs[key] = Sub(match: match, cont: cont)
        cont.onTermination = { [weak self] _ in
            Task { @MainActor in self?.subs[key] = nil }
        }
        return stream
    }

    func publish(_ job: DiskScanJob) {
        if job.state.isFinal { active[job.id] = nil } else { active[job.id] = job }
        for sub in subs.values where sub.match(job) { sub.cont.yield(job) }
    }

    /// Reads the jobs that are not final again, for example after the
    /// WebSocket was lost.
    func refetch(using service: any DiskScanJobService) async {
        for id in active.keys {
            if let j = try? await service.diskScan(jobId: id) { publish(j) }
        }
    }
}

/// A scanner that can also show jobs that run on the server already, for
/// example a job that another device started.
protocol DiskScanFollower: DiskScanner {
    /// The newest job of the node, or nil.
    func lastJob(node: Node) async -> DiskScanJob?
    /// Events of an existing job, as `scan` sends them.
    func follow(node: Node, job: DiskScanJob) -> AsyncThrowingStream<DiskScanEvent, Error>
    /// All job changes of the node.
    @MainActor func jobs(node: Node) -> AsyncStream<DiskScanJob>
}

/// Runs scans through the server (docs/wire.md §3.1). The stream sends a
/// `.job` event for each job change, then the result, error or cancel.
/// Cancel the consuming task to cancel the job on the server.
struct RemoteDiskScanner: DiskScanFollower {
    let service: any DiskScanJobService
    let feed: DiskScanJobFeed
    /// Poll for the job in case a WebSocket message is lost.
    var pollInterval: TimeInterval = 10

    func scan(node: Node, request: DiskScanRequest) -> AsyncThrowingStream<DiskScanEvent, Error> {
        let service = self.service
        return stream {
            do {
                return try await service.startDiskScan(nodeId: node.id, request: request)
            } catch DiskScanError.alreadyRunning(let job?) {
                // Show the job that runs, for example from another device.
                return job
            }
        }
    }

    func follow(node: Node, job: DiskScanJob) -> AsyncThrowingStream<DiskScanEvent, Error> {
        stream { job }
    }

    func lastJob(node: Node) async -> DiskScanJob? {
        try? await service.diskScans(nodeId: node.id).first
    }

    @MainActor func jobs(node: Node) -> AsyncStream<DiskScanJob> {
        feed.subscribe(nodeId: node.id)
    }

    private func stream(
        _ first: @escaping @MainActor @Sendable () async throws -> DiskScanJob
    ) -> AsyncThrowingStream<DiskScanEvent, Error> {
        AsyncThrowingStream { c in
            let task = Task { @MainActor in await run(first, c) }
            c.onTermination = { _ in task.cancel() }
        }
    }

    @MainActor
    private func run(
        _ first: @MainActor () async throws -> DiskScanJob,
        _ c: AsyncThrowingStream<DiskScanEvent, Error>.Continuation
    ) async {
        var jobId: String?
        do {
            var job = try await first()
            jobId = job.id
            feed.publish(job)
            c.yield(.job(job))
            if !job.state.isFinal {
                let updates = feed.subscribe(jobId: job.id)
                let poller = poll(jobId: job.id)
                defer { poller.cancel() }
                // A change can come before the subscription.
                if let j = try? await service.diskScan(jobId: job.id) { feed.publish(j) }
                for await j in updates where !job.state.isFinal {
                    job = j
                    c.yield(.job(j))
                    if j.state.isFinal { break }
                }
            }
            try Task.checkCancellation()
            try await finish(job, c)
        } catch {
            if Task.isCancelled {
                if let jobId { cancelOnServer(jobId) }
                c.finish()
            } else {
                c.finish(throwing: error)
            }
        }
    }

    @MainActor
    private func poll(jobId: String) -> Task<Void, Never> {
        let service = self.service
        let feed = self.feed
        let ns = UInt64(max(pollInterval, 0.05) * 1_000_000_000)
        return Task { @MainActor in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: ns)
                if Task.isCancelled { return }
                if let j = try? await service.diskScan(jobId: jobId) { feed.publish(j) }
            }
        }
    }

    @MainActor
    private func cancelOnServer(_ jobId: String) {
        let service = self.service
        let feed = self.feed
        Task { @MainActor in
            if let j = try? await service.cancelDiskScan(jobId: jobId) { feed.publish(j) }
        }
    }

    @MainActor
    private func finish(_ job: DiskScanJob, _ c: AsyncThrowingStream<DiskScanEvent, Error>.Continuation) async throws {
        switch job.state {
        case .done:
            guard let r = try await service.diskScanResult(jobId: job.id) else {
                throw DiskScanError.remote("The server has no result for this scan.")
            }
            c.yield(.result(r))
        case .cancelled:
            c.yield(.cancelled)
        case .expired:
            c.yield(.error(job.error.isEmpty ? "No sampler took the scan in time. Make sure that the sampler of the node runs." : job.error))
        case .failed, .queued, .running:
            c.yield(.error(job.error.isEmpty ? "The scan failed." : job.error))
        }
        c.finish()
    }
}
