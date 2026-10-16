import Foundation
import Observation

/// State of the Usage sub-tab of DISK for one node: the scan, the shown
/// result, the selected recommendations and the open folder.
@Observable
@MainActor
final class DiskUsageModel {
    enum Phase: Equatable {
        case idle
        case running(DiskScanProgress?)
        case done
        case failed(String)
    }

    /// A result older than this asks for a rescan before cleanup.
    static let staleAfter: TimeInterval = 24 * 3600

    let node: Node
    private(set) var phase: Phase = .idle
    /// "~" for the home folder, else an absolute path.
    private(set) var scope: String
    /// The shown result: the last scan of `scope`.
    private(set) var record: DiskScanRecord?
    var selected: Set<String> = []
    /// Names from the root to the open folder.
    private(set) var focus: [String] = []
    private(set) var startedAt: Date?
    /// Remote scans: the last server job.
    private(set) var job: DiskScanJob?
    /// Set when the server or host does not allow scans.
    private(set) var scansOff: String?
    /// During a scan: show the previous result in place of the progress.
    var showPrevious = false

    @ObservationIgnored private let store: DiskScanStore
    @ObservationIgnored private let scanner: any DiskScanner
    @ObservationIgnored private let now: () -> Date
    @ObservationIgnored private var task: Task<Void, Never>?

    init(node: Node, store: DiskScanStore, scanner: any DiskScanner, scope: String,
         now: @escaping () -> Date = Date.init) {
        self.node = node
        self.store = store
        self.scanner = scanner
        self.scope = scope
        self.now = now
        load()
    }

    var result: DiskScanResult? { record?.result }

    var isRunning: Bool {
        if case .running = phase { return true }
        return false
    }

    var progress: DiskScanProgress? {
        if case .running(let p) = phase { return p }
        return nil
    }

    var isStale: Bool {
        guard let record else { return false }
        return now().timeIntervalSince(record.finishedAt) > Self.staleAfter
    }

    func setScope(_ s: String) {
        guard s != scope, !isRunning else { return }
        scope = s
        if case .failed = phase { phase = .idle }
        load()
    }

    /// Reloads the last stored result of the scope.
    func load() {
        record = store.latest(nodeID: node.id, scope: scope)
        selected = defaultSelection
        focus = []
    }

    func start() {
        guard !isRunning else { return }
        let request = DiskScanRequest(root: scope == "~" ? nil : scope)
        job = nil
        scansOff = nil
        run(scanner.scan(node: node, request: request), progress: nil, startedAt: now())
    }

    private func run(_ events: AsyncThrowingStream<DiskScanEvent, Error>, progress: DiskScanProgress?, startedAt: Date) {
        let scope = self.scope
        phase = .running(progress)
        self.startedAt = startedAt
        showPrevious = false
        task = Task { [weak self] in
            do {
                for try await event in events {
                    guard let self else { return }
                    self.handle(event, scope: scope)
                }
                guard let self, !Task.isCancelled, self.isRunning else { return }
                self.phase = .failed("The scan stopped without a result.")
            } catch {
                guard let self, !Task.isCancelled, self.isRunning else { return }
                if case .scansOff(let msg) = error as? DiskScanError { self.scansOff = msg }
                self.phase = .failed(error.localizedDescription)
            }
        }
    }

    /// Stops the scan. The scanner closes stdin, the channel, or asks the
    /// server to cancel the job.
    func cancel() {
        guard isRunning else { return }
        task?.cancel()
        task = nil
        phase = .idle
    }

    /// Hides the error and shows the stored result again.
    func dismissFailure() {
        if case .failed = phase { phase = record == nil ? .idle : .done }
    }

    /// Remote mode: loads the last server job of the node, then follows
    /// new jobs, for example ones that another device starts. Runs until
    /// the calling task is cancelled.
    func attach() async {
        guard let follower = scanner as? any DiskScanFollower else { return }
        let updates = follower.jobs(node: node)
        if let j = await follower.lastJob(node: node) { follow(job: j) }
        for await j in updates { follow(job: j) }
    }

    /// Shows a job that runs or ran on the server. Follows a job that is
    /// not final, and gets the result of a done job that is newer than
    /// the stored result.
    func follow(job j: DiskScanJob) {
        guard !isRunning else { return }
        job = j
        guard let follower = scanner as? any DiskScanFollower else { return }
        let jobScope = DiskScanRequest(root: j.root).scope
        if !j.state.isFinal {
            setScope(jobScope)
            run(follower.follow(node: node, job: j), progress: j.progress, startedAt: j.createdTs ?? now())
        } else if j.state == .done, jobScope == scope,
                  let done = j.finishedTs, done > (record?.finishedAt ?? .distantPast) {
            run(follower.follow(node: node, job: j), progress: nil, startedAt: j.createdTs ?? now())
        }
    }

    func handle(_ event: DiskScanEvent, scope: String) {
        switch event {
        case .progress(let p):
            phase = .running(p)
        case .job(let j):
            job = j
            if !j.state.isFinal, isRunning { phase = .running(j.progress ?? progress) }
        case .result(let r):
            store.save(nodeID: node.id, scope: scope, result: r, finishedAt: now())
            if scope == self.scope { load() }
            phase = .done
        case .error(let msg):
            if msg.contains("--allow-disk-scan") { scansOff = msg }
            phase = .failed(msg)
        case .cancelled:
            phase = .idle
        }
    }

    // MARK: Selection

    var defaultSelection: Set<String> {
        Set(result?.recs.filter { $0.group == .safe }.map(\.id) ?? [])
    }

    var selectedRecs: [DiskRec] { result?.recs.filter { selected.contains($0.id) } ?? [] }

    var selectedBytes: Int64 { selectedRecs.reduce(0) { $0 + $1.bytes } }

    /// Tree node ids of the selected recommendations.
    var selectedNodeIDs: Set<String> { Set(selectedRecs.flatMap(\.nodeIDs)) }

    func toggle(_ id: String) {
        if selected.contains(id) { selected.remove(id) } else { selected.insert(id) }
    }

    func selectSafe() { selected = defaultSelection }

    func clearSelection() { selected = [] }

    func script(now: Date = Date()) -> CleanupScript? {
        result.map { CleanupScript(result: $0, selected: selected, now: now) }
    }

    // MARK: Explorer

    var focusedNode: DiskNode? {
        guard var n = result?.tree else { return nil }
        for name in focus {
            guard let c = n.children.first(where: { $0.name == name }) else { return n }
            n = c
        }
        return n
    }

    /// Tiles of the open folder. At the root, a "No access" tile shows the
    /// estimated size that the scan could not read.
    var mapNodes: [DiskNode] {
        guard let n = focusedNode else { return [] }
        var out = n.children.isEmpty ? [n] : n.children
        if focus.isEmpty, let est = result?.noAccess.bytesEst, est > 0 {
            out.append(DiskNode(n: "No access", b: est, cat: .none))
        }
        return out
    }

    var mapTotal: Int64 { mapNodes.reduce(0) { $0 + $1.bytes } }

    func open(_ name: String) {
        guard let n = focusedNode, n.children.contains(where: { $0.name == name && !$0.children.isEmpty }) else { return }
        focus.append(name)
    }

    /// Goes back to `level` folders below the root (0 is the root).
    func goUp(to level: Int) {
        focus = Array(focus.prefix(max(0, level)))
    }
}
