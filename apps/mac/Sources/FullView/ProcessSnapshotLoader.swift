import Foundation

/// The process snapshot of a Cloud host at the cursor. It waits for the
/// cursor to rest, keeps the old rows while it loads, and gets the buckets
/// around the new one.
@MainActor
@Observable
final class ProcessSnapshotLoader {
    private(set) var snapshot: RemoteProcessSnapshot?
    /// The bucket of `snapshot`.
    private(set) var bucket: ProcBucket?
    private(set) var showsLoading = false
    private(set) var error: String?
    /// The request path with its size and time, or "cache hit".
    private(set) var lastRequest: String?

    @ObservationIgnored private let host: UUID
    @ObservationIgnored private let hostLabel: String
    @ObservationIgnored private let loader: any CloudHistoryLoader
    @ObservationIgnored private let clock: any Clock<Duration>
    @ObservationIgnored private let debounce: Duration
    @ObservationIgnored private let loadingDelay: Duration
    @ObservationIgnored private var task: Task<Void, Never>?
    @ObservationIgnored private var prefetch: Task<Void, Never>?
    @ObservationIgnored private var target: Target?

    private struct Target: Equatable {
        let bucket: ProcBucket
        let by: ProcSort
    }

    init(
        host: UUID, hostLabel: String? = nil, loader: any CloudHistoryLoader,
        clock: any Clock<Duration> = ContinuousClock(),
        debounce: Duration = .milliseconds(120), loadingDelay: Duration = .milliseconds(80)
    ) {
        self.host = host
        self.hostLabel = hostLabel ?? host.uuidString
        self.loader = loader
        self.clock = clock
        self.debounce = debounce
        self.loadingDelay = loadingDelay
    }

    /// Asks for the snapshot of the bucket of `t`. Many calls in a row send one request.
    func request(at t: Date, by: ProcSort, now: Date = Date()) {
        let next = Target(bucket: ProcBucket.of(t, now: now), by: by)
        guard next != target else { return }
        target = next
        task?.cancel()
        prefetch?.cancel()
        task = Task { await run(next, now: now) }
    }

    func cancel() {
        task?.cancel()
        prefetch?.cancel()
        task = nil
        prefetch = nil
        target = nil
        showsLoading = false
    }

    private func run(_ t: Target, now: Date) async {
        let path = "GET /v1/nodes/\(hostLabel)/processes/at?ts=\(Int(t.bucket.start.timeIntervalSince1970))&by=\(t.by.rawValue)&limit=\(t.bucket.limit)"
        if let hit = await loader.cachedProcessesAt(host: host, bucket: t.bucket, by: t.by) {
            guard !Task.isCancelled else { return }
            show(hit, t.bucket, "cache hit · " + path)
            showsLoading = false
            startPrefetch(t, now: now)
            return
        }
        do { try await clock.sleep(for: debounce) } catch { return }
        let started = ContinuousClock.now
        let delay = Task {
            do { try await clock.sleep(for: loadingDelay) } catch { return }
            showsLoading = true
        }
        defer { delay.cancel() }
        do {
            let snap = try await loader.processesAt(host: host, bucket: t.bucket, by: t.by)
            guard !Task.isCancelled else { return }
            let ms = Int(started.duration(to: .now) / .milliseconds(1))
            show(snap, t.bucket, path + String(format: " · %.1f KB · %d ms", Double(snap.bytes) / 1024, ms))
            showsLoading = false
            startPrefetch(t, now: now)
        } catch {
            guard !Task.isCancelled else { return }
            self.error = CloudHistoryError.message(error)
            showsLoading = false
            target = nil
        }
    }

    private func show(_ snap: RemoteProcessSnapshot, _ b: ProcBucket, _ request: String) {
        snapshot = snap
        bucket = b
        error = nil
        lastRequest = request
    }

    /// Two buckets on each side, none in the future.
    private func startPrefetch(_ t: Target, now: Date) {
        let loader = loader
        let host = host
        let targets = [-2, -1, 1, 2].map { t.bucket.shifted($0) }.filter { $0.start <= now }
        prefetch = Task(priority: .background) {
            for b in targets {
                if Task.isCancelled { return }
                _ = try? await loader.processesAt(host: host, bucket: b, by: t.by)
            }
        }
    }
}

// MARK: - Rows for the table

extension ProcessSnapshotLoader {
    /// The rows of the snapshot. Raw rows have a PID. Rollup rows have a peak.
    var rows: [ProcessSnapshotRow] {
        (snapshot?.processes ?? []).map { p in
            ProcessSnapshotRow(
                ref: ProcessRef(key: p.key, pid: p.pid, name: p.name, cmd: p.cmd),
                user: p.user, cpuPct: p.cpuPct, cpuPctMax: p.cpuPctMax,
                rss: Int64(clamping: p.rss), ioBps: p.ioBps, threads: p.threads, started: p.startTS
            )
        }
    }

    /// "14:02:11 · now", "minute 14:02 · 3 min ago" or "hour 14:00 · 5 h ago".
    func whenLabel(now: Date) -> String {
        guard let snap = snapshot, let bucket else { return "no snapshot" }
        let t = snap.ts
        let age = now.timeIntervalSince(t)
        let ago = age < 15 ? "now" : ShortDuration.long(age) + " ago"
        switch bucket.tier {
        case .raw: return t.formatted(date: .omitted, time: .standard) + " · " + ago
        case .minute: return "minute " + t.formatted(date: .abbreviated, time: .shortened) + " · " + ago
        case .hour: return "hour " + t.formatted(date: .abbreviated, time: .shortened) + " · " + ago
        }
    }

    /// The text at the left of the footer.
    var note: String? {
        guard let snap = snapshot, let bucket else { return nil }
        if bucket.tier == .raw, let total = snap.total { return "\(snap.processes.count) of \(total) processes · " + bucket.label }
        return bucket.label
    }
}
