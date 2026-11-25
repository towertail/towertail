import Foundation

/// Builds the detail of a Cloud process from the one-process route.
@MainActor
final class CloudProcessSource: ProcessDetailSource {
    private let host: UUID
    private let loader: any CloudHistoryLoader

    init(host: UUID, loader: any CloudHistoryLoader) {
        self.host = host
        self.loader = loader
    }

    func detail(of ref: ProcessRef, window: ClosedRange<Date>) async throws -> ProcessDetail {
        let span = window.upperBound.timeIntervalSince(window.lowerBound)
        let step = Duration.seconds(max(10, Int((span / 300).rounded(.up))))
        let d = try await loader.process(host: host, key: ref.key, window: window, step: step)
        return ProcessDetail(
            facts: Self.facts(d),
            cpu: d.points.map { MetricPoint(t: $0.ts, v: $0.cpuPct) },
            rss: d.points.map { MetricPoint(t: $0.ts, v: Double($0.rss)) },
            io: d.points.compactMap { p in p.ioBps.map { MetricPoint(t: p.ts, v: $0) } }
        )
    }

    static func facts(_ d: RemoteProcessDetail) -> ProcessFacts {
        guard let f = d.facts else { return ProcessFacts(repo: d.repo) }
        let ports = (f.listenTCP ?? []).map { "tcp \($0)" } + (f.listenUDP ?? []).map { "udp \($0)" }
        return ProcessFacts(
            user: f.user, ppid: f.ppid, parent: f.parent, cwd: f.cwd, repo: d.repo,
            ports: ports, started: f.startTS, factsAt: f.seenAt, threads: f.threads
        )
    }
}
