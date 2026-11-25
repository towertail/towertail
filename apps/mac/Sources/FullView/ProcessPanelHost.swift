import SwiftUI

/// Connects the panel to the host: the data source, the cursor and the actions.
struct ProcessPanelHost: View {
    let vm: ServerViewModel
    @Bindable var model: FullViewModel
    let ref: ProcessRef
    let frame: WorkbenchFrame
    var node: Node? = nil
    var onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil

    @Environment(ServerSettings.self) private var settings
    @State private var panel: ProcessPanelModel
    @State private var killTarget: KillTarget?

    private struct LoadKey: Hashable {
        let from: Date
        let to: Date
        let latest: Date?
    }

    init(
        vm: ServerViewModel, model: FullViewModel, ref: ProcessRef, frame: WorkbenchFrame,
        node: Node? = nil, onIgnore: ((String, LongProcIgnore.Scope) -> Void)? = nil
    ) {
        self.vm = vm
        self.model = model
        self.ref = ref
        self.frame = frame
        self.node = node
        self.onIgnore = onIgnore
        _panel = State(initialValue: ProcessPanelModel(ref: ref, source: LocalProcessSource(vm: vm)))
    }

    var body: some View {
        let latest = vm.procs.latest?.t
        let key = LoadKey(from: Self.round(frame.window.lowerBound), to: Self.round(frame.window.upperBound), latest: latest.map(Self.round))
        ProcessPanel(
            model: panel,
            cursor: model.effectiveTimestamp(latest: latest),
            cursorState: CursorState(model: model),
            window: frame.window,
            canKill: livePid != nil && node != nil && node?.kind != .cloud,
            ignoredScope: ignoredScope,
            hostId: node?.id,
            canCompare: model.tab != .processes,
            hostCores: vm.cpuCores,
            hostMemTotal: vm.memTotalBytes,
            longRunningFor: longProc.map { $0.highFor(at: Date()) },
            alerts: alerts,
            onIgnore: { name, scope in onIgnore?(name, scope) },
            onCompare: { model.compare(ref) },
            onKill: { if let pid = livePid { killTarget = KillTarget(pid: pid, name: ref.name) } },
            onClose: { model.openProcess = nil }
        )
        .processKillFlow(node: node, target: $killTarget)
        .task(id: key) { await panel.load(window: frame.window) }
    }

    /// The window moves each second. A key of 10 s steps reloads rarely.
    private static func round(_ d: Date) -> Date {
        Date(timeIntervalSince1970: (d.timeIntervalSince1970 / 10).rounded(.down) * 10)
    }

    private var longProc: LongProc? {
        vm.longProcs.first { ProcessRef.key(name: $0.name, cmd: $0.cmd) == ref.key }
    }

    /// The PID that runs now. The row may hold a PID of an older run.
    private var livePid: Int32? {
        let items = vm.procs.latest?.items.filter { ProcessRef.key(name: $0.name, cmd: $0.cmd) == ref.key } ?? []
        return items.first { $0.pid == ref.pid }?.pid ?? items.first?.pid
    }

    private var ignoredScope: LongProcIgnore.Scope? {
        let name = ref.name.lowercased()
        if settings.longProc.ignoreSet().contains(name) { return .allHosts }
        if let node, node.ignoredProcs.contains(where: { $0.lowercased() == name }) { return .host(node.id) }
        return nil
    }

    private var alerts: [ProcessAlertLine] {
        guard let p = longProc else { return [] }
        return [ProcessAlertLine(
            id: "long-\(p.pid)", tint: .warn,
            text: "Long-running · \(Int(p.cpuPct.rounded()))% CPU for " + ShortDuration.long(p.highFor(at: Date())),
            detail: p.highSince.formatted(date: .abbreviated, time: .shortened) + " · open"
        )]
    }
}
