import Foundation
import AppKit
import Network

/// Whether collectors should actively try to reach remote hosts right now.
///
/// The reason we model this as a single state — instead of letting each
/// feature subscribe to sleep and network separately — is so the UI,
/// collector, and notifier all agree on *why* things are paused. A card
/// saying "paused (asleep)" during a Wi‑Fi outage would lie to the user.
enum CollectorAvailability: Equatable, Sendable {
    case available
    case sleeping
    case networkDown
    /// Short window after wake / network-return where we resume polling
    /// but suppress offline notifications. Gives DHCP, Tailscale, and the
    /// first SSH dial time to settle without paging.
    case warmingUp(until: Date)

    var shouldPoll: Bool {
        switch self {
        case .available, .warmingUp: return true
        case .sleeping, .networkDown: return false
        }
    }

    /// Whether offline/threshold notifications are allowed to fire.
    /// During warm-up we suppress — a lone failed post-wake sample should
    /// never surface as "your server is down."
    var notificationsAllowed: Bool {
        self == .available
    }

    /// Human-readable reason for cards, suitable for subtitle text.
    var suspendedReason: String? {
        switch self {
        case .available, .warmingUp: return nil
        case .sleeping: return "Mac is asleep"
        case .networkDown: return "no internet"
        }
    }
}

/// Observes macOS sleep/wake and local network reachability. Publishes a
/// combined `CollectorAvailability` so downstream code has a single source
/// of truth for "is it safe to poll / notify right now?".
///
/// Thread model: all external reads and writes are on the main actor. The
/// internal `NWPathMonitor` posts to a background queue which hops to the
/// main actor before mutating state.
@MainActor
@Observable
final class SystemReachabilityMonitor {
    /// Current gate for collectors/notifier. Updated on sleep, wake, and
    /// network transitions.
    private(set) var availability: CollectorAvailability = .available

    private let settings: AppSettings
    private let pathMonitor: NWPathMonitor
    private let pathQueue: DispatchQueue
    private var observers: [NSObjectProtocol] = []
    private var warmupTimer: Timer?
    /// Tracks whether the most recent network path was satisfied. Starts
    /// true so we don't emit a spurious "network is back" on launch.
    private var lastNetworkSatisfied = true
    private var isAsleep = false
    private var started = false

    init(settings: AppSettings) {
        self.settings = settings
        self.pathMonitor = NWPathMonitor()
        self.pathQueue = DispatchQueue(label: "towertail.reachability", qos: .utility)
    }

    func start() {
        guard !started else { return }
        started = true

        let workspace = NSWorkspace.shared.notificationCenter
        observers.append(workspace.addObserver(
            forName: NSWorkspace.willSleepNotification,
            object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.handleWillSleep() }
        })
        observers.append(workspace.addObserver(
            forName: NSWorkspace.didWakeNotification,
            object: nil, queue: .main
        ) { [weak self] _ in
            Task { @MainActor in self?.handleDidWake() }
        })

        pathMonitor.pathUpdateHandler = { [weak self] path in
            let satisfied = path.status == .satisfied
            Task { @MainActor in self?.handleNetworkUpdate(satisfied: satisfied) }
        }
        pathMonitor.start(queue: pathQueue)

        Logger.shared.info(
            "reachability: started",
            category: "lifecycle",
            kv: ["postWakeGrace": String(settings.postWakeGraceSeconds)]
        )
    }

    func stop() {
        guard started else { return }
        started = false
        let center = NSWorkspace.shared.notificationCenter
        for o in observers { center.removeObserver(o) }
        observers.removeAll()
        pathMonitor.cancel()
        warmupTimer?.invalidate()
        warmupTimer = nil
    }

    // MARK: - Transitions

    private func handleWillSleep() {
        isAsleep = true
        warmupTimer?.invalidate()
        warmupTimer = nil
        updateAvailability(Logger.shared, event: "will-sleep")
    }

    private func handleDidWake() {
        isAsleep = false
        // Start warm-up even if network says "satisfied" — Tailscale and
        // DHCP can need a few seconds to reconverge after wake, during
        // which SSH dials will fail. Suppress notifications meanwhile.
        scheduleWarmup()
        updateAvailability(Logger.shared, event: "did-wake")
    }

    private func handleNetworkUpdate(satisfied: Bool) {
        let wasSatisfied = lastNetworkSatisfied
        lastNetworkSatisfied = satisfied
        if satisfied && !wasSatisfied {
            // Network just came back — honor the grace period the same way
            // we do on wake, for the same reason (first dial often fails
            // before the route table settles).
            scheduleWarmup()
            updateAvailability(Logger.shared, event: "network-up")
        } else if !satisfied && wasSatisfied {
            warmupTimer?.invalidate()
            warmupTimer = nil
            updateAvailability(Logger.shared, event: "network-down")
        }
    }

    private func scheduleWarmup() {
        warmupTimer?.invalidate()
        let grace = max(0, settings.postWakeGraceSeconds)
        guard grace > 0 else {
            warmupTimer = nil
            return
        }
        warmupTimer = Timer.scheduledTimer(
            withTimeInterval: TimeInterval(grace),
            repeats: false
        ) { [weak self] _ in
            Task { @MainActor in
                self?.warmupTimer = nil
                self?.updateAvailability(Logger.shared, event: "warmup-ended")
            }
        }
    }

    private func updateAvailability(_ logger: Logger, event: String) {
        let next: CollectorAvailability
        if isAsleep {
            next = .sleeping
        } else if !lastNetworkSatisfied {
            next = .networkDown
        } else if warmupTimer != nil {
            next = .warmingUp(until: Date().addingTimeInterval(TimeInterval(settings.postWakeGraceSeconds)))
        } else {
            next = .available
        }
        guard next != availability else { return }
        let from = Self.label(availability)
        let to = Self.label(next)
        availability = next
        logger.info(
            "reachability: \(from) → \(to)",
            category: "lifecycle",
            kv: ["event": event]
        )
    }

    private static func label(_ a: CollectorAvailability) -> String {
        switch a {
        case .available: return "available"
        case .sleeping: return "sleeping"
        case .networkDown: return "network-down"
        case .warmingUp: return "warming-up"
        }
    }
}
