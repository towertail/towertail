import AppKit
import Foundation

/// Tells Cloud which hosts the user looks at. It builds the `view` frame
/// from what is on screen, sends it at each change, and renews it while
/// something is visible. A hidden popover or a sleeping Mac gives an empty
/// view at once.
@MainActor
final class ViewLeaseController {
    private struct FullViewEntry {
        let window: UUID
        let host: UUID?
        let tab: DetailTab?
    }

    private(set) var current: LiveView = .empty

    /// The cards that are on screen now.
    var visibleCards: Set<UUID> { cards }

    private let renewEvery: Duration
    private let cardDebounce: Duration
    private let send: @MainActor (LiveView) -> Void
    private let sleep: @Sendable (Duration) async throws -> Void

    private var popoverVisible = false
    private var cards: Set<UUID> = []
    /// Visible full view windows. The last one changed last.
    private var fullViews: [FullViewEntry] = []
    private var awake = true
    private var limited = false
    private var renewTask: Task<Void, Never>?
    private var debounceTask: Task<Void, Never>?

    private var systemAsleep = false
    private var screensAsleep = false
    private var sleepObservers: [NSObjectProtocol] = []

    /// `sleep` is injected for tests.
    init(renewEvery: Duration, cardDebounce: Duration,
         send: @escaping @MainActor (LiveView) -> Void,
         sleep: @escaping @Sendable (Duration) async throws -> Void) {
        self.renewEvery = renewEvery
        self.cardDebounce = cardDebounce
        self.send = send
        self.sleep = sleep
    }

    convenience init(send: @escaping @MainActor (LiveView) -> Void,
                     sleep: @escaping @Sendable (Duration) async throws -> Void) {
        self.init(renewEvery: .seconds(30), cardDebounce: .milliseconds(250), send: send, sleep: sleep)
    }

    convenience init(send: @escaping @MainActor (LiveView) -> Void) {
        self.init(send: send, sleep: { try await Task.sleep(for: $0) })
    }

    func setPopover(visible: Bool) {
        guard popoverVisible != visible else { return }
        popoverVisible = visible
        refresh()
    }

    /// A card scrolls in or out of the popover. Changes within the debounce
    /// time give one view.
    func setCard(_ id: UUID, visible: Bool) {
        let changed = visible ? cards.insert(id).inserted : cards.remove(id) != nil
        if changed { refresh(debounced: true) }
    }

    func setFullView(window: UUID, host: UUID?, tab: DetailTab?, visible: Bool) {
        let entry = FullViewEntry(window: window, host: host, tab: tab)
        let old = fullViews.first { $0.window == window }
        if !visible, old == nil { return }
        if visible, let old, old.host == host, old.tab == tab { return }
        fullViews.removeAll { $0.window == window }
        if visible { fullViews.append(entry) }
        refresh()
    }

    func setSystemAwake(_ awake: Bool) {
        guard self.awake != awake else { return }
        self.awake = awake
        refresh()
    }

    /// A new connection or a restarted server has no lease: send the view again.
    func streamDidConnect() {
        guard current != .empty else { return }
        emit(current)
    }

    /// The server ended the lease because of its limit. Do not renew until
    /// the next change of the input.
    func didReceiveLimit() {
        limited = true
        renewTask?.cancel()
        renewTask = nil
    }

    // MARK: - System sleep

    /// Follows the sleep and the wake of the system and of the screens.
    func startObservingSleep() {
        guard sleepObservers.isEmpty else { return }
        sleepObservers = [
            observe(NSWorkspace.willSleepNotification) { $0.systemAsleep = true },
            observe(NSWorkspace.didWakeNotification) { $0.systemAsleep = false },
            observe(NSWorkspace.screensDidSleepNotification) { $0.screensAsleep = true },
            observe(NSWorkspace.screensDidWakeNotification) { $0.screensAsleep = false },
        ]
    }

    private func observe(_ name: Notification.Name,
                         _ update: @escaping @MainActor @Sendable (ViewLeaseController) -> Void) -> NSObjectProtocol {
        NSWorkspace.shared.notificationCenter.addObserver(forName: name, object: nil, queue: .main) { [weak self] _ in
            MainActor.assumeIsolated {
                guard let self else { return }
                update(self)
                self.setSystemAwake(!self.systemAsleep && !self.screensAsleep)
            }
        }
    }

    func stopObservingSleep() {
        let center = NSWorkspace.shared.notificationCenter
        sleepObservers.forEach(center.removeObserver)
        sleepObservers = []
    }

    // MARK: - View

    private func desired() -> LiveView {
        guard awake else { return .empty }
        var hosts = popoverVisible ? cards : []
        for entry in fullViews {
            if let host = entry.host { hosts.insert(host) }
        }
        guard !hosts.isEmpty else { return .empty }
        var detail: ViewDetail?
        if let last = fullViews.last, let host = last.host, let tab = last.tab {
            detail = ViewDetail(host: host, tab: tab)
        }
        return LiveView(hosts: hosts.sorted { $0.uuidString < $1.uuidString }, detail: detail)
    }

    private func refresh(debounced: Bool = false) {
        debounceTask?.cancel()
        debounceTask = nil
        guard debounced else { return flush() }
        debounceTask = Task { [weak self, sleep, cardDebounce] in
            try? await sleep(cardDebounce)
            guard !Task.isCancelled else { return }
            self?.debounceTask = nil
            self?.flush()
        }
    }

    private func flush() {
        let next = desired()
        let changed = next != current
        let wasLimited = limited
        limited = false
        current = next
        if next == .empty {
            renewTask?.cancel()
            renewTask = nil
            if changed { send(next) }
        } else if changed || wasLimited {
            emit(next)
        }
    }

    /// Sends a non-empty view and starts the renew timer.
    private func emit(_ view: LiveView) {
        send(view)
        renewTask?.cancel()
        renewTask = Task { [weak self, sleep, renewEvery] in
            while !Task.isCancelled {
                try? await sleep(renewEvery)
                guard !Task.isCancelled, let self else { return }
                self.send(self.current)
            }
        }
    }
}
