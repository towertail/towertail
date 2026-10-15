import Foundation

/// Runs Test and Reinstall sampler on one or more nodes and keeps the
/// last result for the Servers pane status line.
@Observable
@MainActor
final class ServerTester {
    struct Outcome: Identifiable {
        let id = UUID()
        let ok: Bool
        let message: String
    }

    private(set) var outcome: Outcome?
    private(set) var running = false

    func clear() { outcome = nil }

    /// Runs a sample end to end (uploads the sampler if missing). A host
    /// that passes is marked warm and its pacer respawns, so a pacer that
    /// halted on a permanent error resumes.
    func test(_ nodes: [Node], backend: (any Backend)?, nodeStore: NodeStore) {
        guard !nodes.isEmpty, !running else { return }
        running = true
        outcome = Outcome(ok: true, message: nodes.count == 1 ? "Testing \(nodes[0].displayName)…" : "Testing \(nodes.count) hosts…")
        Task {
            var ok: [(Node, String)] = []
            var failed: [(Node, String)] = []
            for node in nodes {
                do {
                    ok.append((node, try await Self.sample(node)))
                } catch {
                    failed.append((node, Self.describe(error)))
                }
            }
            for (node, _) in ok {
                nodeStore.markConnected(id: node.id)
                await backend?.respawnPacer(id: node.id)
            }
            running = false
            outcome = Self.summary(verb: "Tested", total: nodes.count, ok: ok, failed: failed)
        }
    }

    /// Force-uploads the bundled sampler. Local nodes are skipped: they
    /// always run the bundled binary.
    func reinstall(_ nodes: [Node]) {
        let nodes = nodes.filter { $0.kind == .ssh }
        guard !nodes.isEmpty, !running else { return }
        running = true
        outcome = Outcome(ok: true, message: nodes.count == 1 ? "Reinstalling sampler on \(nodes[0].displayName)…" : "Reinstalling sampler on \(nodes.count) hosts…")
        Task {
            var ok: [(Node, String)] = []
            var failed: [(Node, String)] = []
            for node in nodes {
                do {
                    let r = try await SSHBootstrap.bootstrapAndVerify(node: node)
                    ok.append((node, "Reinstalled (\(r.triple)) → \(r.remotePath)"))
                } catch {
                    failed.append((node, Self.describe(error)))
                }
            }
            running = false
            outcome = Self.summary(verb: "Reinstalled", total: nodes.count, ok: ok, failed: failed)
        }
    }

    private static func sample(_ node: Node) async throws -> String {
        if node.kind == .ssh {
            let r = try await SSHBootstrap.bootstrapAndVerify(node: node)
            let s = r.sample
            return "OK (\(r.triple)): \(s.host.name) · cpu \(Int(s.cpu.pct.rounded()))% · cores \(s.cpu.cores)"
        }
        let s = try await makeInvoker(for: node).invokeOnce(node: node)
        return "OK: \(s.host.name) · cpu \(Int(s.cpu.pct.rounded()))% · cores \(s.cpu.cores)"
    }

    private static func describe(_ error: Error) -> String {
        (error as? SamplerInvokeError)?.errorDescription ?? error.localizedDescription
    }

    /// One host: its own message. Several: a count plus the first failures.
    private static func summary(verb: String, total: Int, ok: [(Node, String)], failed: [(Node, String)]) -> Outcome {
        if total == 1 {
            if let f = failed.first { return Outcome(ok: false, message: "\(f.0.displayName): \(f.1)") }
            return Outcome(ok: true, message: ok.first?.1 ?? "")
        }
        if failed.isEmpty { return Outcome(ok: true, message: "\(verb) \(ok.count)/\(total) hosts OK") }
        let detail = failed.prefix(3).map { "\($0.0.displayName): \($0.1)" }.joined(separator: " · ")
        let tail = failed.count > 3 ? " · +\(failed.count - 3) more" : ""
        return Outcome(ok: false, message: "\(verb) \(ok.count)/\(total) OK · \(failed.count) failed: \(detail)\(tail)")
    }
}
