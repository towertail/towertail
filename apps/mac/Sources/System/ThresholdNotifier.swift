import Foundation
import SwiftUI
import UserNotifications
import AppKit

/// Evaluates per-(host, metric) alert transitions and fires a macOS
/// notification on warn/critical escalation. Uses the sustained alert level
/// and the metric's notify rule, plus per-node opt-in (notifyOnWarn,
/// notifyOnCritical), the global on/off, and a debounce so a metric that
/// flaps around the line doesn't buzz the user continuously. A muted signal
/// (`ServerViewModel.mutedSignals`) never escalates.
///
/// Tap routing: each notification carries the hostId + metric in userInfo.
/// A tap posts to `NotificationTapRouter`, which invokes a handler
/// registered by the App layer.
@MainActor
final class ThresholdNotifier {
    private let settings: ServerSettings
    private let reachability: SystemReachabilityMonitor?
    private var lastTint: [Key: ThresholdTint] = [:]
    /// Timestamp of the most recent notification for a (host, metric) pair.
    /// Used to suppress repeats while the metric stays above threshold.
    private var lastFired: [Key: Date] = [:]
    private var started = false
    /// Hosts in the no-data state, so each episode notifies one time.
    private var noDataActive: Set<UUID> = []
    private var noDataFired: [UUID: Date] = [:]
    /// Keys flagged at the last evaluation, so each process notifies one time.
    private var longProcSeen: [UUID: Set<LongProcKey>] = [:]
    private var longProcFired: [FiredKey: Date] = [:]

    struct FiredKey: Hashable {
        let hostId: UUID
        let proc: LongProcKey
    }

    struct Key: Hashable {
        let hostId: UUID
        let metric: Metric
    }

    init(settings: ServerSettings, reachability: SystemReachabilityMonitor? = nil) {
        self.settings = settings
        self.reachability = reachability
    }

    func start() {
        guard !started else { return }
        started = true
        let center = UNUserNotificationCenter.current()
        center.delegate = NotificationTapRouter.shared
        // Authorization is requested lazily on the first would-be fire so the
        // user doesn't get prompted at launch before they've set anything up.
    }

    func evaluate(vm: ServerViewModel, node: Node) {
        for metric in [Metric.cpu, .mem, .disk, .health] {
            let key = Key(hostId: vm.id, metric: metric)
            let current = vm.alertTint(for: metric)
            let previous = lastTint[key] ?? .nominal
            lastTint[key] = current

            // Only fire on escalation (nominal→warn, nominal→critical, warn→critical).
            // Downgrades update state silently so the next escalation can fire.
            guard current.rank > previous.rank else { continue }
            guard vm.alertRules[metric].notify.allows(current) else { continue }

            let allowed: Bool = {
                switch current {
                case .warn: return settings.notifyWarn && node.notifyOnWarn
                case .critical: return settings.notifyCritical && node.notifyOnCritical
                default: return false
                }
            }()
            guard settings.notificationsEnabled, allowed else { continue }
            // Suppress during sleep / no-internet / post-wake warm-up.
            // We explicitly don't notify about a threshold crossing when
            // we're in a state where the UI itself says "paused" — the
            // signal would be noise, not information.
            if let r = reachability, !r.availability.notificationsAllowed { continue }
            // Snooze gate. We still update lastTint above so that if the
            // user snoozes at warn and the host later escalates to critical
            // after the snooze expires, we treat that as a real escalation
            // rather than the initial warn→critical that happened while
            // muted.
            if node.isSnoozed { continue }

            if let last = lastFired[key],
               Date().timeIntervalSince(last) < Double(settings.notifyDebounceSeconds) {
                continue
            }
            lastFired[key] = Date()

            let level = current == .critical ? "Critical" : "Warning"
            post(
                host: vm, kind: metric.rawValue, metric: metric, critical: current == .critical,
                title: "\(vm.hostname): \(metric.displayName) \(level)",
                body: Self.bodyText(vm: vm, metric: metric)
            )
        }
    }

    /// One critical notification when a host enters the no-data state.
    func evaluateNoData(vm: ServerViewModel, node: Node, now: Date = Date()) {
        guard let since = vm.noDataSince else {
            noDataActive.remove(vm.id)
            return
        }
        guard noDataActive.insert(vm.id).inserted else { return }
        guard settings.notificationsEnabled, settings.notifyCritical, node.notifyOnCritical else { return }
        if let r = reachability, !r.availability.notificationsAllowed { return }
        if node.isSnoozed { return }
        if let last = noDataFired[vm.id],
           now.timeIntervalSince(last) < Double(settings.notifyDebounceSeconds) {
            return
        }
        noDataFired[vm.id] = now
        post(
            host: vm, kind: "no_data", metric: .cpu, critical: true,
            title: "\(vm.hostname): no data",
            body: Self.noDataBody(vm: vm, since: since)
        )
    }

    /// One warn notification for each newly flagged long-running process.
    func evaluateLongProcs(vm: ServerViewModel, node: Node, now: Date = Date()) {
        let current = Set(vm.longProcs.map(\.key))
        let previous = longProcSeen[vm.id] ?? []
        longProcSeen[vm.id] = current
        for key in longProcFired.keys where key.hostId == vm.id && !current.contains(key.proc) {
            if let t = longProcFired[key], now.timeIntervalSince(t) >= Double(settings.notifyDebounceSeconds) {
                longProcFired[key] = nil
            }
        }
        let fresh = vm.longProcs.filter { !previous.contains($0.key) }
        guard !fresh.isEmpty, vm.longProcRule.notifies else { return }
        guard settings.notificationsEnabled, settings.notifyWarn, node.notifyOnWarn else { return }
        if let r = reachability, !r.availability.notificationsAllowed { return }
        if node.isSnoozed { return }
        for p in fresh {
            let key = FiredKey(hostId: vm.id, proc: p.key)
            if let last = longProcFired[key],
               now.timeIntervalSince(last) < Double(settings.notifyDebounceSeconds) {
                continue
            }
            longProcFired[key] = now
            post(
                host: vm, kind: "long_proc", metric: .cpu, critical: false,
                title: "\(vm.hostname): long-running process",
                body: Self.longProcBody(p, now: now)
            )
        }
    }

    /// "OrbStack Helper (pid 812) at 85% CPU for 32m. Started 3d ago."
    static func longProcBody(_ p: LongProc, now: Date) -> String {
        var s = "\(p.shortName) (pid \(p.pid)) at \(Int(p.cpuPct.rounded()))% CPU for \(ShortDuration.text(p.highFor(at: now)))."
        if let start = p.startTS { s += " Started \(ShortDuration.age(now.timeIntervalSince(start))) ago." }
        return s
    }

    static func noDataBody(vm: ServerViewModel, since: Date) -> String {
        let reference = vm.lastIngestAt ?? vm.staleBaseline
        var s = "No sample for \(ShortDuration.text(since.timeIntervalSince(reference)))."
        if let err = vm.lastError { s += " Last error: \(err)." }
        return s
    }

    /// `metric` picks the full-view tab that a tap opens.
    private func post(
        host: ServerViewModel, kind: String, metric: Metric, critical: Bool,
        title: String, body: String
    ) {
        let center = UNUserNotificationCenter.current()
        center.requestAuthorization(options: [.alert, .sound]) { _, _ in }

        let content = UNMutableNotificationContent()
        content.title = title
        content.body = body
        content.sound = critical ? .defaultCritical : .default
        content.userInfo = [
            "hostId": host.id.uuidString,
            "metric": metric.rawValue,
        ]

        let req = UNNotificationRequest(
            identifier: "towertail.\(host.id.uuidString).\(kind).\(Int(Date().timeIntervalSince1970))",
            content: content,
            trigger: nil
        )
        center.add(req, withCompletionHandler: nil)
        Logger.shared.info(
            "notification: fired",
            category: "notifications",
            hostID: host.id, host: host.hostname,
            kv: ["metric": kind, "level": critical ? "critical" : "warn"]
        )
    }

    static func bodyText(vm: ServerViewModel, metric: Metric) -> String {
        switch metric {
        case .cpu:
            if let v = vm.cpu.latest?.v {
                var s = "CPU at \(Int(round(v * 100)))%"
                if let top = topProcesses(vm: vm, metric: .cpu), !top.isEmpty {
                    s += "\nTop: " + top.joined(separator: ", ")
                }
                return s
            }
        case .mem:
            if let v = vm.mem.latest?.v {
                var s = "Memory at \(Int(round(v * 100)))%"
                if let top = topProcesses(vm: vm, metric: .mem), !top.isEmpty {
                    s += "\nTop: " + top.joined(separator: ", ")
                }
                return s
            }
        case .disk:
            if let v = vm.disk.latest?.v { return "Disk at \(Int(round(v * 100)))%" }
        case .health:
            let text = vm.health.reasons(unmuted: vm.mutedSignals).map(\.text).joined(separator: " · ")
            if !text.isEmpty { return text }
        case .net:
            break
        }
        return "Threshold crossed"
    }

    /// Top-2 processes by the alerting metric, formatted for a notification
    /// body. Returns nil when there's no proc snapshot yet (hydration lag
    /// or sampler without proc visibility).
    private static func topProcesses(vm: ServerViewModel, metric: Metric) -> [String]? {
        guard let items = vm.procs.latest?.items, !items.isEmpty else { return nil }
        switch metric {
        case .cpu:
            return items
                .sorted { $0.cpuPct > $1.cpuPct }
                .prefix(2)
                .map { "\(procLabel($0)) \(String(format: "%.0f", $0.cpuPct))%" }
        case .mem:
            return items
                .sorted { $0.rss > $1.rss }
                .prefix(2)
                .map { "\(procLabel($0)) \(formatBytes($0.rss))" }
        case .disk, .net, .health:
            return nil
        }
    }

    private static func procLabel(_ p: ProcSample) -> String {
        // Prefer the short name; it's what the user scans for at a glance.
        // Truncate very long names so two entries still fit on a notification line.
        let name = p.name.isEmpty ? "pid \(p.pid)" : p.name
        if name.count > 24 { return String(name.prefix(23)) + "…" }
        return name
    }

    private static func formatBytes(_ b: Int64) -> String {
        let v = Double(max(b, 0))
        if v < 1024 { return String(format: "%.0f B", v) }
        if v < 1_048_576 { return String(format: "%.0f KB", v / 1024) }
        if v < 1_073_741_824 { return String(format: "%.1f MB", v / 1_048_576) }
        return String(format: "%.2f GB", v / 1_073_741_824)
    }
}

/// UNUserNotificationCenter delegate singleton. When the user taps a
/// notification this invokes the `onTap` handler that the App layer
/// registers at launch. Using a direct callback (rather than SwiftUI's
/// @Observable + .onChange) avoids relying on the menu-bar popover's
/// content closure being instantiated — MenuBarExtra builds its content
/// lazily, so a modifier attached there never observes anything until the
/// user opens the popover at least once.
final class NotificationTapRouter: NSObject, UNUserNotificationCenterDelegate, @unchecked Sendable {
    static let shared = NotificationTapRouter()

    private let lock = NSLock()
    private var handler: ((FullViewContext) -> Void)?
    private var buffered: [FullViewContext] = []

    func setHandler(_ h: @escaping (FullViewContext) -> Void) {
        lock.lock()
        handler = h
        let pending = buffered
        buffered.removeAll()
        lock.unlock()
        // Drain taps that came in before the App had a chance to wire up.
        if !pending.isEmpty {
            DispatchQueue.main.async {
                for ctx in pending { h(ctx) }
            }
        }
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification,
        withCompletionHandler completionHandler: @escaping (UNNotificationPresentationOptions) -> Void
    ) {
        completionHandler([.banner, .sound])
    }

    func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse,
        withCompletionHandler completionHandler: @escaping () -> Void
    ) {
        let userInfo = response.notification.request.content.userInfo
        if let idStr = userInfo["hostId"] as? String,
           let id = UUID(uuidString: idStr),
           let metricStr = userInfo["metric"] as? String,
           let metric = Metric(rawValue: metricStr) {
            let ctx = FullViewContext(hostId: id, metric: metric)
            lock.lock()
            let h = handler
            if h == nil { buffered.append(ctx) }
            lock.unlock()
            if let h {
                DispatchQueue.main.async { h(ctx) }
            }
        }
        completionHandler()
    }
}
