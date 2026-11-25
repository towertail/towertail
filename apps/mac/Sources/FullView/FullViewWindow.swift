import SwiftUI

/// Restarts the frame loop on a host swap or an active-state change.
private struct TaskKey: Hashable {
    let host: UUID
    let active: Bool
}

/// Restarts the Cloud history load on a host, tab or source change.
private struct HistoryKey: Hashable {
    let host: UUID
    let disks: Bool
    let cloud: Bool
}

/// The data of a window that the live series do not hold.
private struct OlderKey: Hashable {
    let host: UUID
    let from: Date
    let to: Date
}

/// The host workbench: header, tabs, range control and the body of the tab.
struct FullViewWindow: View {
    let context: FullViewContext
    @Environment(ServerStore.self) private var store
    @Environment(ServerSettings.self) private var settings
    @Environment(NodeStore.self) private var nodeStore
    @Environment(\.backend) private var backend
    @Environment(\.viewLease) private var viewLease
    /// `.background` means that the window is closed but retained. The body
    /// then reads no observable state, so the hidden window costs no CPU.
    @Environment(\.scenePhase) private var scenePhase
    @State private var model: FullViewModel
    @State private var activeHostId: UUID
    @State private var leaseWindowID = UUID()

    /// Charts read this frame. A loop rebuilds it about once per second.
    @State private var frame = WorkbenchFrame.empty
    @State private var older: [HistoryPoint] = []
    @State private var olderKey: OlderKey?
    @State private var olderAt = Date.distantPast
    /// Oldest time of the local store, for the range control.
    @State private var storeOldest: Date?
    @State private var historyLoading = false
    @State private var historyError: String?

    private static let frameInterval: Duration = .seconds(1)
    private static let olderPoints = 300
    private static let olderRefresh: TimeInterval = 30

    init(context: FullViewContext, initialTab: HostTab? = nil, prepare: ((FullViewModel) -> Void)? = nil) {
        self.context = context
        let model = FullViewModel(context: context, source: .poll)
        if let initialTab { model.tab = initialTab }
        prepare?(model)
        _model = State(initialValue: model)
        _activeHostId = State(initialValue: context.hostId)
    }

    var body: some View {
        ZStack {
            if scenePhase == .background {
                Color.clear.frame(minWidth: 980, minHeight: 600)
            } else {
                activeBody
            }
        }
        .onAppear { reportLease() }
        .onDisappear { reportLease(visible: false) }
        .onChange(of: scenePhase) { _, _ in reportLease() }
        .onChange(of: activeHostId) { _, _ in reportLease() }
        .onChange(of: model.tab) { _, _ in reportLease() }
        .onChange(of: model.processesView) { _, _ in reportLease() }
        .onChange(of: model.diskSubTab) { _, _ in reportLease() }
    }

    private func reportLease(visible: Bool? = nil) {
        viewLease?.setFullView(
            window: leaseWindowID, host: activeHostId, tab: model.detailTab,
            visible: visible ?? (scenePhase != .background)
        )
    }

    private var vm: ServerViewModel? {
        store.serverVMs.first(where: { $0.id == activeHostId })
    }

    @ViewBuilder
    private var activeBody: some View {
        let vm = vm
        VStack(spacing: 0) {
            if let vm {
                WorkbenchHeader(vm: vm, score: vm.healthScore, hostId: $activeHostId)
                Divider().overlay(WB.border)
                WorkbenchTabBar(
                    model: model, lastSample: frame.latest ?? vm.cpu.latest?.t,
                    oldest: [frame.oldest, storeOldest].compactMap { $0 }.min()
                )
                HStack(alignment: .top, spacing: 0) {
                    tabBody(vm: vm)
                        .overlay(alignment: .top) { historyOverlay }
                    if let ref = model.openProcess, model.tab != .alerts {
                        ProcessPanelHost(
                            vm: vm, model: model, ref: ref, frame: frame,
                            node: nodeStore.node(withId: activeHostId), onIgnore: ignoreProc
                        )
                        .id(ref)
                        .padding(.trailing, 14).padding(.bottom, 12)
                        .transition(.move(edge: .trailing).combined(with: .opacity))
                    }
                }
                .animation(.easeOut(duration: 0.16), value: model.openProcess)
            } else {
                ContentUnavailableView("No data", systemImage: "questionmark.circle")
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }
        }
        .background(WB.bg)
        .frame(minWidth: 980, minHeight: 600)
        .focusable()
        .focusEffectDisabled()
        .onKeyPress(.space) {
            model.pinLatest(frame.latest)
            return .handled
        }
        .onKeyPress(.escape) { model.handleEscape() ? .handled : .ignored }
        .onKeyPress(.leftArrow) { model.stepPin(by: -1, span: stepSpan, now: Date()) ? .handled : .ignored }
        .onKeyPress(.rightArrow) { model.stepPin(by: 1, span: stepSpan, now: Date()) ? .handled : .ignored }
        .onAppear {
            ActivationPolicyCoordinator.shared.acquire()
            // Only this window reads proc history, so the SQLite load waits for it.
            store.ensureProcsHydrated(for: activeHostId)
            rebuild(force: true)
        }
        .onDisappear { ActivationPolicyCoordinator.shared.release() }
        // `.task` ends with the view. A hidden window (scene phase) builds no frames.
        .task(id: TaskKey(host: activeHostId, active: scenePhase == .active)) {
            guard scenePhase == .active else { return }
            while !Task.isCancelled {
                rebuild()
                try? await Task.sleep(for: Self.frameInterval)
            }
        }
        .task(id: HistoryKey(host: activeHostId, disks: model.tab == .disks, cloud: vm?.liveSource == .cloud)) {
            await loadCloudHistory()
        }
        .onChange(of: model.range) { _, _ in rebuild(force: true) }
        .onChange(of: model.zoomRange) { _, _ in rebuild(force: true) }
        .onChange(of: model.diskMount) { _, _ in rebuild(force: true) }
        .onChange(of: model.diskDevice) { _, _ in rebuild(force: true) }
        .onChange(of: activeHostId) { _, newId in
            // The caches belong to the old host.
            frame = .empty
            older = []
            olderKey = nil
            storeOldest = nil
            model.diskMount = .max
            model.diskDevice = .total
            model.offenderSelected = nil
            model.openProcess = nil
            store.ensureProcsHydrated(for: newId)
            rebuild(force: true)
        }
    }

    /// The time of one sample, for the arrow keys.
    private var stepSpan: TimeInterval {
        let pts = frame.points(.cpu)
        guard pts.count >= 2 else { return 5 }
        let dt = pts[pts.count - 1].t.timeIntervalSince(pts[pts.count - 2].t)
        return min(max(dt, 1), 60)
    }

    // MARK: Frame

    private func rebuild(force: Bool = false) {
        guard let vm else { return }
        let now = Date()
        let w = model.window(now: now)
        if vm.liveSource != .cloud { loadOlder(vm: vm, window: w, now: now, force: force) }
        let next = WorkbenchFrame(vm: vm, window: w, older: older)
        if next != frame { frame = next }
    }

    /// Local rows from SQLite for the part of the window that the live series lack.
    private func loadOlder(vm: ServerViewModel, window w: ClosedRange<Date>, now: Date, force: Bool) {
        let liveFirst = vm.cpu.snapshot().first?.t ?? now
        if storeOldest == nil || force || now.timeIntervalSince(olderAt) > Self.olderRefresh {
            let wide = store.loadRange(
                for: vm.id, from: now.addingTimeInterval(-HostRange.d30.seconds), to: now, maxPoints: 60
            )
            storeOldest = wide.first?.t
        }
        guard w.lowerBound < liveFirst else {
            if !older.isEmpty { older = [] }
            olderKey = nil
            return
        }
        // The key holds the window to the minute, so a moving window reloads rarely.
        let to = min(w.upperBound, liveFirst)
        let key = OlderKey(
            host: vm.id,
            from: Date(timeIntervalSince1970: (w.lowerBound.timeIntervalSince1970 / 60).rounded(.down) * 60),
            to: Date(timeIntervalSince1970: (to.timeIntervalSince1970 / 60).rounded(.down) * 60)
        )
        guard force || key != olderKey || now.timeIntervalSince(olderAt) > Self.olderRefresh else { return }
        older = store.loadRange(for: vm.id, from: w.lowerBound, to: to, maxPoints: Self.olderPoints)
        olderKey = key
        olderAt = now
    }

    /// Loads the 2 h series of a Cloud host over REST. Local and SSH hosts
    /// read SQLite instead.
    private func loadCloudHistory() async {
        guard let loader = backend?.cloudHistory, vm?.liveSource == .cloud else {
            historyLoading = false
            historyError = nil
            return
        }
        historyLoading = true
        historyError = nil
        do {
            try await loader.loadFullView(host: activeHostId, metric: model.tab == .disks ? .disk : .cpu)
        } catch {
            guard !Task.isCancelled else { return }
            historyError = CloudHistoryError.message(error)
        }
        guard !Task.isCancelled else { return }
        historyLoading = false
        rebuild(force: true)
    }

    @ViewBuilder
    private var historyOverlay: some View {
        if let historyError {
            Text(historyError)
                .font(.callout)
                .foregroundStyle(.secondary)
                .padding(10)
                .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 8))
                .padding(.top, 40)
        } else if historyLoading {
            ProgressView().controlSize(.large).padding(.top, 60)
        }
    }

    // MARK: Tabs

    @ViewBuilder
    private func tabBody(vm: ServerViewModel) -> some View {
        let node = nodeStore.node(withId: activeHostId)
        switch model.tab {
        case .overview:
            OverviewTab(vm: vm, model: model, frame: frame, score: vm.healthScore, node: node, onIgnore: ignoreProc)
        case .processes:
            ProcessesTab(vm: vm, model: model, frame: frame, node: node, onIgnore: ignoreProc)
        case .health:
            HealthTab(vm: vm, model: model, frame: frame, score: vm.healthScore, node: node, onIgnore: ignoreProc)
        case .disks:
            DisksTab(vm: vm, model: model, frame: frame, node: node)
        case .alerts:
            AlertsTab(vm: vm)
        case .settings:
            SettingsTab(vm: vm, node: node)
        }
    }

    private func ignoreProc(_ name: String, scope: LongProcIgnore.Scope) {
        let action = LongProcIgnore(settings: settings, nodes: nodeStore, servers: store, backend: backend)
        Task { await action.ignore(name, on: scope) }
    }

    /// Formats a byte-rate with an adaptive unit so small values stay readable.
    /// 500 B/s stays in bytes; 120_000 B/s becomes "117.2 KB/s"; >1 MiB/s in MB/s.
    static func formatByteRate(bytesPerSec: Double) -> String {
        let v = max(0, bytesPerSec)
        if v < 1_024 {
            return String(format: "%.0f B/s", v)
        }
        if v < 1_048_576 {
            return String(format: "%.1f KB/s", v / 1_024)
        }
        if v < 1_073_741_824 {
            return String(format: "%.1f MB/s", v / 1_048_576)
        }
        return String(format: "%.2f GB/s", v / 1_073_741_824)
    }
}
