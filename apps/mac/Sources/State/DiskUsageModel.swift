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
    /// The cleanup picks of the scope, oldest first.
    private(set) var picks: [CleanupPick] = []
    /// A short message for the toast.
    var notice: String?

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
        picks = store.picks(nodeID: node.id, scope: scope)
        prunePicks()
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
        Set(visibleRecs.filter { $0.group == .safe }.map(\.id))
    }

    /// The recommendations that the user did not hide.
    var visibleRecs: [DiskRec] { result?.recs.filter { !isHidden($0) } ?? [] }

    var hiddenRecs: [DiskRec] { result?.recs.filter(isHidden) ?? [] }

    func isHidden(_ r: DiskRec) -> Bool {
        picks.contains { $0.kind == .hidden && $0.key == CleanupPick.hiddenKey(r) }
    }

    var selectedRecs: [DiskRec] { visibleRecs.filter { selected.contains($0.id) } }

    /// The items that the user added, with their nodes in the tree.
    var mineItems: [MineItem] {
        guard let result else { return [] }
        return picks.compactMap { p in
            guard p.kind == .mine, let names = result.names(of: p.key),
                  let n = result.tree.node(at: names[...]) else { return nil }
            return MineItem(path: p.key, displayPath: result.displayPath(p.key), names: names, node: n, on: p.on)
        }
    }

    var selectedMine: [MineItem] { mineItems.filter(\.on) }

    var selectedCount: Int { selectedRecs.count + selectedMine.count }

    var selectedBytes: Int64 {
        selectedRecs.reduce(0) { $0 + $1.bytes } + selectedMine.reduce(0) { $0 + $1.node.bytes }
    }

    /// Tree node ids of the selected recommendations.
    var selectedNodeIDs: Set<String> { Set(selectedRecs.flatMap(\.nodeIDs)) }

    func toggle(_ id: String) {
        if selected.contains(id) { selected.remove(id) } else { selected.insert(id) }
    }

    func selectSafe() { selected = defaultSelection }

    func clearSelection() {
        selected = []
        for p in picks where p.kind == .mine && p.on { setMine(p.key, on: false) }
    }

    func script(now: Date = Date()) -> CleanupScript? {
        result.map {
            CleanupScript(result: $0, selected: Set(selectedRecs.map(\.id)), mine: selectedMine.map(\.path), now: now)
        }
    }

    // MARK: Cleanup list

    struct MineItem: Equatable, Identifiable {
        var path: String
        var displayPath: String
        var names: [String]
        var node: DiskNode
        var on: Bool

        var id: String { path }
        var label: String {
            let last = node.name.split(whereSeparator: { $0 == "/" || $0 == "\\" }).last.map(String.init) ?? node.name
            return node.isFile ? last : last + "/"
        }
    }

    /// The state of a map tile in the cleanup list.
    enum TileMark: Equatable {
        case none
        /// A recommendation points to the tile.
        case rule(DiskRec, on: Bool)
        /// The user added the tile.
        case mine(on: Bool)
        /// A folder above the tile is in the list and checked.
        case covered(by: String)
    }

    func mark(_ names: [String]) -> TileMark {
        guard let result, !names.isEmpty else { return .none }
        if let by = coveredBy(names) { return .covered(by: by) }
        if let r = rec(at: names), !isHidden(r) { return .rule(r, on: selected.contains(r.id)) }
        let path = result.absolutePath(of: names)
        if let p = picks.first(where: { $0.kind == .mine && $0.key == path }) { return .mine(on: p.on) }
        return .none
    }

    /// True when a checked item is at or below the tile.
    func holdsSelection(_ names: [String]) -> Bool {
        guard let result, let n = result.tree.node(at: names[...]) else { return false }
        if !n.subtreeIDs.isDisjoint(with: selectedNodeIDs) { return true }
        let path = result.absolutePath(of: names)
        return selectedMine.contains { DiskScanResult.path($0.path, isIn: path, separator: result.separator) }
    }

    /// The name of the checked folder above `names`, if there is one.
    func coveredBy(_ names: [String]) -> String? {
        guard let result, names.count > 1 else { return nil }
        let ids = selectedNodeIDs
        let on = Set(selectedMine.map(\.path))
        for k in stride(from: names.count - 1, through: 1, by: -1) {
            let up = Array(names.prefix(k))
            if let id = result.tree.node(at: up[...])?.id, ids.contains(id) { return up.last }
            if on.contains(result.absolutePath(of: up)) { return up.last }
        }
        return nil
    }

    /// Puts the tile in the list, checked. A recommendation of the tile is
    /// shown again and selected. Items of the user below it leave the list.
    func add(_ names: [String]) {
        guard let result, !names.isEmpty, coveredBy(names) == nil else { return }
        if let r = rec(at: names) {
            if isHidden(r) { unhide(r) }
            selected.insert(r.id)
            return
        }
        let path = result.absolutePath(of: names)
        for p in picks where p.kind == .mine && p.key != path
            && DiskScanResult.path(p.key, isIn: path, separator: result.separator) {
            removeMine(p.key)
        }
        setMine(path, on: true)
    }

    /// Takes the tile out of the list. A recommendation becomes hidden.
    func remove(_ names: [String]) {
        switch mark(names) {
        case .rule(let r, _): hide(r)
        case .mine: if let result { removeMine(result.absolutePath(of: names)) }
        case .none, .covered: break
        }
    }

    /// The box on a tile: adds the tile, or checks or clears it.
    func toggleTile(_ names: [String]) {
        switch mark(names) {
        case .none: add(names)
        case .rule(let r, _): toggle(r.id)
        case .mine(let on): if let result { setMine(result.absolutePath(of: names), on: !on) }
        case .covered: break
        }
    }

    func setMine(_ path: String, on: Bool) {
        var p = picks.first { $0.kind == .mine && $0.key == path } ?? CleanupPick(kind: .mine, key: path, addedAt: now())
        p.on = on
        savePick(p)
    }

    func removeMine(_ path: String) {
        store.removePick(.mine, key: path, nodeID: node.id, scope: scope)
        picks.removeAll { $0.kind == .mine && $0.key == path }
    }

    func hide(_ r: DiskRec) {
        savePick(CleanupPick(kind: .hidden, key: CleanupPick.hiddenKey(r), addedAt: now()))
        selected.remove(r.id)
    }

    func unhide(_ r: DiskRec) {
        let key = CleanupPick.hiddenKey(r)
        store.removePick(.hidden, key: key, nodeID: node.id, scope: scope)
        picks.removeAll { $0.kind == .hidden && $0.key == key }
    }

    private func savePick(_ p: CleanupPick) {
        store.savePick(p, nodeID: node.id, scope: scope)
        if let i = picks.firstIndex(where: { $0.kind == p.kind && $0.key == p.key }) {
            picks[i] = p
        } else {
            picks.append(p)
        }
    }

    /// The recommendation that points to the node at `names`.
    private func rec(at names: [String]) -> DiskRec? {
        guard let id = result?.tree.node(at: names[...])?.id else { return nil }
        return result?.recs.first { $0.nodeIDs.contains(id) }
    }

    /// Drops the items of the user that are not in the tree, and tells the user.
    private func prunePicks() {
        guard let result else { return }
        let gone = picks.filter { $0.kind == .mine && result.names(of: $0.key) == nil }
        guard !gone.isEmpty else { return }
        for p in gone { removeMine(p.key) }
        let names = gone.map { result.displayPath($0.key) }
        notice = names.count == 1
            ? "\(names[0]) is gone, so it left the cleanup list."
            : "\(names.joined(separator: ", ")) are gone, so they left the cleanup list."
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
